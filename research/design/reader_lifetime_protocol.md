# SSTable Reader Lifetime & Synchronization Protocol (Sub-phase I1.3.1)

**Sub-phase:** I1.3.1 — Specify the complete reader-lifetime protocol  
**Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Date:** 2026-10-16  

---

## 1. Re-examination of Reader Reference Containment

In the preliminary I1.3.0 audit, it was noted that point reads (`Manager.Get`) and transaction conflict checks (`latestValueLocked`) do not allow reader pointers to escape their enclosing `m.Mu.RLock()` / `m.Mu.Lock()` blocks.

However, **forensic inspection of `CompactAll` reveals a critical exception**:
```go
func (m *Manager) CompactAll() error {
    m.Mu.Lock()
    ...
    readersToCompact := make([]*SSTableReader, len(m.ActiveSSreaders))
    copy(readersToCompact, m.ActiveSSreaders)
    ...
    m.Mu.Unlock() // <--- LOCK RELEASED HERE

    // Merge loop runs OUTSIDE Manager.Mu!
    for minHeap.Len() > 0 { ... }
```
During the merge loop, the compaction routine retains borrowed references in `readersToCompact` outside `m.Mu`. Consequently, **reader references DO escape manager lock protection during compaction**.

---

## 2. Concurrency Hazards Outside `Manager.Mu`

While `readersToCompact` are being merged outside `Manager.Mu`, the following hazards arise:

### Hazard A: Overlapping / Concurrent Compactions
- **Scenario**: Goroutine 1 starts `CompactAll()`, captures $[R_0, R_1]$, and releases `m.Mu`. Goroutine 2 simultaneously calls `CompactAll()`, captures the same $[R_0, R_1]$ (still present in `m.ActiveSSreaders`), and begins a second merge.
- **Consequences**:
  1. Goroutine 1 finishes, acquires `m.Mu.Lock()`, replaces $[R_0, R_1]$ with $M_1$, and calls `r.Close()` and `os.Remove()`.
  2. Goroutine 2 is still iterating $[R_0, R_1]$. On Windows, file deletion fails with `ERROR_SHARING_VIOLATION` if open, or Goroutine 2 encounters read I/O errors on unlinked files.
  3. When Goroutine 2 attempts publication, `replaceCompactedReaders` detects that $[R_0, R_1]$ are no longer active and aborts with a validation error, wasting substantial CPU/disk I/O and risking double-close.
- **Required Protection**: All compactions must be strictly serialized so that only one compaction can lease active readers at a time.

### Hazard B: Engine Shutdown (`Manager.Close`) During In-Flight Compaction
- **Scenario**: Compaction captures $[R_0, R_1]$ and begins merging outside `m.Mu`. Concurrently, `Manager.Close()` is invoked.
- **Consequences**:
  1. `Manager.Close()` acquires `m.Mu.Lock()`.
  2. Because publication has not occurred, `m.ActiveSSreaders` still contains $[R_0, R_1]$.
  3. `Manager.Close()` calls `reader.Close()` on all active readers and closes the WAL.
  4. When the in-flight compaction finishes its merge, it attempts publication and cleanup:
     - `replaceCompactedReaders` executes against a closing/closed engine.
     - `readersToCompact` receive a second `r.Close()`, causing duplicate close panics or errors.
     - `os.Remove()` deletes files that the shutdown manager intended to preserve for restart.
- **Required Protection**: `Manager.Close()` must coordinate with in-flight compactions to ensure no readers are closed while in active use.

### Hazard C: Point Read (`Manager.Get`) During Reader Retirement
- **Scenario**: A query `Manager.Get` is reading an SSTable block while `CompactAll` finishes its merge and retires the SSTables.
- **Approach A Protection**:
  1. `Manager.Get` acquires `m.Mu.RLock()` before accessing `m.ActiveSSreaders` and holds it until value retrieval finishes.
  2. Compaction acquires exclusive `m.Mu.Lock()` to perform publication via `replaceCompactedReaders`.
  3. `m.Mu.Lock()` **blocks** until all active `RLock()` holders complete.
  4. Once `m.Mu.Lock()` is acquired, retired readers are removed from `m.ActiveSSreaders`.
  5. Upon `m.Mu.Unlock()`, new `Get` calls can only see the new reader.
  6. Compaction calls `r.Close()` and `os.Remove()` only after `m.Mu.Unlock()`.
  - **Verdict**: Under Approach A, point reads are fully protected by `sync.RWMutex`, provided reads do not retain reader pointers after releasing `RLock()`.

### Hazard D: Iteration / Builder Errors During Compaction
- **Scenario**: Disk full or corruption occurs during the k-way merge.
- **Current Behavior**: Compaction closes all opened iterators, closes the builder, unlinks `newFilename`, and returns `err`.
- **Verdict**: Active readers remain untouched in `m.ActiveSSreaders`. No reader references leak.

---

## 3. Disentangling Reader Lifecycle States

To ensure safe reclamation, we explicitly distinguish between slice membership and operation leases:

```
[Constructing]
      │  NewSSTableReader()
      ▼
   [Active] ◄────────────────────────────────────────┐
      │                                              │
      │ m.Mu.Lock()                                  │ Abort / Error
      ▼                                              │ (Unmodified)
[Compacting Lease] (holds borrowed reference)        │
      │                                              │
      │ Merge completes; requests m.Mu.Lock()        │
      ▼                                              │
  [Draining] (in-flight RLock() point reads drain)   │
      │                                              │
      │ All prior RLock() point reads release;       │
      │ exclusive m.Mu.Lock() acquired               │
      ▼                                              │
  [Replaced] ─── (replaceCompactedReaders fails) ────┘
      │
      │ Removed from m.ActiveSSreaders under m.Mu.Lock()
      │ (No subsequent queries can borrow this reader)
      │ m.Mu.Unlock()
      ▼
   [Closed]  ── Compaction iterators closed, then r.Close()
      │
      ▼
  [Unlinked] ── os.Remove(r.File.Name())
      │
      ▼
     [*]
```

### Definitions:
1. **Active**: The reader resides in `m.ActiveSSreaders`. New point reads (`Get`) and conflict checks (`latestValueLocked`) can discover and borrow it.
2. **Compacting Lease**: The reader is actively being read by the compaction merge routine outside `m.Mu`.
3. **Draining**: Compaction has completed its merge and requested `m.Mu.Lock()`. In-flight `m.Mu.RLock()` point reads drain and complete before the exclusive lock is granted.
4. **Replaced**: Exclusive `m.Mu.Lock()` is acquired. The reader is removed from `m.ActiveSSreaders` via `replaceCompactedReaders`. No subsequent queries can discover it.
5. **Closed**: Compaction iterators are closed, and `r.Close()` closes the underlying OS file descriptor.
6. **Unlinked**: The physical file is safely removed from disk (`os.Remove`).

---

## 4. Approach A Synchronization Boundaries

To satisfy the reviewer's requirements without introducing Approach B snapshots or reference counters:

1. **Compaction Mutual Exclusion (`m.compactionMu sync.Mutex`)**:
   - A dedicated mutex serializes `CompactAll` operations from input capture through file deletion.
   - **Lock Order**: `m.compactionMu.Lock()` $\to$ `m.Mu.Lock()`.
   - Never acquire `m.compactionMu` while holding `m.Mu`.
2. **Shutdown Coordination (`m.isClosed atomic.Bool`)**:
   - `Manager.Close()` sets `m.isClosed = true` under `m.Mu.Lock()`.
   - `Manager.Close()` acquires `m.compactionMu.Lock()` before closing active readers, ensuring any in-flight compaction completes or exits cleanly before file descriptors are closed.
3. **Approach A Retirement Contract**:
   - In Approach A, readers do not maintain individual reference counters.
   - Protection relies on `m.Mu.RLock()` held by readers and `m.Mu.Lock()` held by publication.
   - `r.Close()` and `os.Remove()` occur only after `replaceCompactedReaders` succeeds and `m.Mu.Unlock()` is reached.

---

## 5. Flush Worker Lifecycle & 4-Phase Shutdown Drain Protocol (Sub-phase I1.3.7)

To resolve background flush races (`E-007`) and out-of-order publication (`E-008`):

1. **Dedicated FIFO Flush Worker (`m.flushChan chan *Memtable`)**:
   - At the instant of freezing, `checkAndTriggerFlush()` enqueues the frozen Memtable into `m.flushChan` under `m.Mu.Lock()`.
   - A single background goroutine `flushLoop()` consumes `m.flushChan` strictly in FIFO order.
   - The worker builds the SSTable outside `m.Mu`, then acquires a brief `m.Mu.Lock()` to append the new reader to the tail of `m.ActiveSSreaders` and remove the memtable from `m.Frozenmem`.
   - Chronological ordering `INV-ORDER-1` is mathematically guaranteed ($F_0 \to F_1 \to F_2$).

2. **Deadlock-Free 4-Phase Shutdown Drain**:
   - Holding `m.Mu.Lock()` while waiting for workers on `m.flushWg.Wait()` produces immediate self-deadlock because workers require `m.Mu.Lock()` to publish.
   - The 4-phase drain protocol solves this deadlock:
     - **Phase 1**: Acquire `m.compactionMu.Lock()` (serializes with compactions).
     - **Phase 2**: Acquire `m.Mu.Lock()`, transition `m.state = EngineClosing`, `close(m.flushChan)`, and release `m.Mu.Unlock()`.
     - **Phase 3**: Call `m.flushWg.Wait()` outside `m.Mu`. The FIFO flush worker drains and finishes building and publishing all queued Memtables without lock contention.
     - **Phase 4**: Acquire `m.Mu.Lock()`, transition `m.state = EngineClosed`, cleanly close all readers in `m.ActiveSSreaders`, close WAL, release `m.Mu.Unlock()`, and release `m.compactionMu.Unlock()`.
   - Result: Zero data loss for committed frozen memtables, zero leaking open file handles on Windows NTFS, and zero deadlocks.
