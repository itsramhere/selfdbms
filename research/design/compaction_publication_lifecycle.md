# Compaction Publication Lifecycle & Concurrency Investigation (Sub-phase I1.2.1)

**Sub-phase:** I1.2.1 — Map the compaction publication lifecycle  
**Target Code:** [`lsmengine/compaction.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go), [`lsmengine/manager.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go)  
**Date:** 2026-10-16  

---

## 1. Architectural Call Flow & Component Overview

In `owndbms/lsmengine`, compaction and flush operate as asynchronous background workers that interact with the shared active reader slice `m.ActiveSSreaders`.

```
                  ┌─────────────────────────────────────────────────────────┐
                  │                 Manager (m.Mu RWMutex)                   │
                  │   - ActiveSSreaders: []*SSTableReader                   │
                  │   - Frozenmem:       []*Memtable                        │
                  │   - Mem:             *Memtable                          │
                  └───────────────────────────┬─────────────────────────────┘
                                              │
                ┌─────────────────────────────┴─────────────────────────────┐
                │                                                           │
     [Flush Worker Pipeline]                                     [Compaction Pipeline]
       go m.flushToDisk(frozen)                                      m.CompactAll()
                │                                                           │
  1. Writes Memtable to .sst                                   1. m.Mu.Lock()
     via SSTableBuilder                                           - Captures readersToCompact
  2. Opens NewSSTableReader                                       - Opens SSTableIterators
  3. m.Mu.Lock()                                               2. m.Mu.Unlock()
     - m.ActiveSSreaders = append(..., reader)                 3. Background k-way merge
     - Drops frozen from m.Frozenmem                              - Iterators -> CompactionHeap
  4. m.Mu.Unlock()                                                - Writes data_compacted_*.sst
                                                                  - Opens newReader
                                                               4. m.Mu.Lock()
                                                                  - [HAZARD]: Overwrites slice
                                                                    m.ActiveSSreaders = []*SSTableReader{newReader}
                                                               5. m.Mu.Unlock()
                                                               6. Deletes old files outside lock
```

---

## 2. Step-by-Step Lifecycle Tracing

### A. Input Capture Phase (`CompactAll` lines 13-35)
1. Compaction begins by acquiring the exclusive manager lock: `m.Mu.Lock()`.
2. Guard check: if `len(m.ActiveSSreaders) <= 1`, it immediately unlocks and returns `nil`.
3. Input Capture:
   ```go
   readersToCompact := m.ActiveSSreaders
   var filenames []string
   for _, r := range readersToCompact {
       filenames = append(filenames, r.File.Name())
   }
   ```
   - Compaction captures the slice of readers currently in `m.ActiveSSreaders`.
   - It extracts their file paths.
4. Iterator Initialization:
   For each file path, it calls `NewSSTableIterator(fname)`. Each iterator opens a new, independent read-only `os.File` handle to that SSTable file and reads its header/index.
5. Lock Release:
   `m.Mu.Unlock()` is invoked.
   - **Critical Design Property**: The exclusive lock is held only briefly to snapshot the reader list and open iterators. Merging does NOT hold `m.Mu`, allowing concurrent reads and writes to proceed.

### B. Unprotected Merge Phase (`CompactAll` lines 36-103)
1. A min-heap (`CompactionHeap`) is initialized with the first record from each iterator.
2. An output SSTable file is created: `fmt.Sprintf("data_compacted_%d.sst", time.Now().UnixNano())`.
3. An `SSTableBuilder` writes deduplicated KV records. Higher sequence numbers sort before lower sequence numbers; duplicate keys are discarded.
4. If a key is marked deleted (`isDelete == true`), and it is the latest version, it is discarded (tombstone purging).
5. `builder.Close()` writes the block index, footer, and syncs to disk.
6. `newReader, err := NewSSTableReader(newFilename)` initializes the new reader.

### C. Publication Phase (`CompactAll` lines 105-108) — THE CRITICAL DEFECT
```go
m.Mu.Lock()
m.ActiveSSreaders = []*SSTableReader{newReader}
m.Mu.Unlock()
```
- Compaction acquires `m.Mu.Lock()`.
- It executes a **destructive slice replacement**: `m.ActiveSSreaders = []*SSTableReader{newReader}`.
- **The Concurrency Hazard**:
  If `flushToDisk` ran while `CompactAll` was in Phase B:
  - `flushToDisk` appended a new `SSTableReader` ($S_{\text{flush}}$) to `m.ActiveSSreaders`.
  - Line 106 blindly replaces `m.ActiveSSreaders` with only `{newReader}`.
  - $S_{\text{flush}}$ is completely discarded and permanently erased from the active reader set!
  - Any commits written to $S_{\text{flush}}$ become invisible to queries and conflict detection.

### D. Retirement & Cleanup Phase (`CompactAll` lines 109-116)
```go
for _, it := range iterators {
    it.Close()
}
for _, r := range readersToCompact {
    r.Close()
    os.Remove(r.File.Name())
}
```
- Compaction closes the iterators used during merge.
- Compaction calls `r.Close()` on the original input readers and unlinks the files via `os.Remove`.
- **Secondary Hazards**:
  - `r.Close()` and `os.Remove` occur outside `m.Mu`.
  - If a concurrent point query (`Manager.Get`) is actively reading through `r`, `r.Close()` closes the underlying file descriptor mid-read, causing `os.ErrClosed` or `io.EOF`.
  - On Windows NTFS, unlinking an open file (`os.Remove`) fails with `ERROR_SHARING_VIOLATION` if any handle remains open.

---

## 3. Protocol Deficiencies Identified

1. **No Input Set Identity Tracking**:
   The code does not track *which specific readers* were compacted; it assumes *all* readers in the system were compacted.
2. **Destructive Publication**:
   `m.ActiveSSreaders = []*SSTableReader{newReader}` destroys concurrent flushes.
3. **No Validation / Pre-publication Check**:
   Before swapping, compaction does not check if the selected inputs are still present in `ActiveSSreaders`. If two compactions ran concurrently, they could select overlapping inputs and corrupt state.
4. **Order Inversion Risk**:
   Even if concurrent flushes were preserved, appending `newReader` to the end of `ActiveSSreaders` would violate chronological ordering `INV-ORDER-1`, causing older compacted data to mask newer visible flushed versions in `Manager.Get`.

---

## 4. Required Publication Architecture for I1.2.2

Under the authorized scope of Sub-phase I1.2.2:
1. **Identity Capture**: Record stable identities (e.g. pointer equality `*SSTableReader` or file paths) of the input readers $S_{\text{input}}$.
2. **Merge Outside Lock**: Construct `newReader` privately without holding `m.Mu`.
3. **Validation Under `m.Mu.Lock()`**:
   - Verify that all readers in $S_{\text{input}}$ are still present in `m.ActiveSSreaders`.
   - If validation fails (e.g. concurrent overlapping compaction), abort and clean up `newReader`.
4. **Identity-Preserving In-Place Replacement**:
   - Replace $S_{\text{input}}$ with `newReader` at the slice position occupied by $S_{\text{input}}$.
   - Preserve all readers added by concurrent flushes:
     $$\text{ActiveSSreaders}_{\text{new}} = \text{replaceInputs}(\text{ActiveSSreaders}_{\text{current}}, S_{\text{input}}, \text{newReader})$$
   - Ensure that any concurrently flushed SSTables remain *after* `newReader` in the slice, preserving strict chronological search ordering `INV-ORDER-1`.
