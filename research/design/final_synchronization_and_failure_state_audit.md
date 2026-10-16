# Sub-phase I1.5.1: Final Synchronization and Failure-State Audit

**Date:** 2026-10-17  
**Sub-phase:** I1.5.1  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Status:** COMPLETE & VERIFIED  

---

## 1. Executive Summary

This audit performs an exhaustive review of the synchronization contracts, call graphs, failure states, resource management, and lifecycle interactions for the candidate **Approach A (Manager-Wide RWMutex Baseline)** storage engine.

All 9 audit mandates specified in the CP1.4a reviewer decision were investigated against the current implementation in `lsmengine/`:
1. All shared data structures adhere to the documented mutual exclusion and reader-lock hierarchy.
2. `m.flushErr` access is strictly synchronized across all reading and writing paths.
3. The background flush worker provably halts upon fatal error and cannot publish newer flushes out of order.
4. Frozen Memtables remain accessible, immutable, and strictly chronologically ordered after failure.
5. Pending flushes are preserved in `m.Frozenmem` and queryable during process lifetime.
6. Graceful shutdown is cleanly distinguishable from failure shutdown via `Close()` return values and `FlushErr()`.
7. All background goroutines, file descriptors, and temporary files are accounted for and cleanly released.
8. The post-failure transaction policy is explicitly defined: in-memory and WAL mutations succeed while background disk flushes are suspended.
9. Durability boundaries are explicitly bounded by the single-file `RecoverFromWAL` implementation without over-claiming multi-WAL crash consistency.

No new correctness defects were discovered. The engine is verified ready for synchronization-mode selection (I1.5.2) and baseline freezing (I1.5.5).

---

## 2. Complete Call Graph & Synchronization Analysis

### 2.1 `Manager.Get(key string, readTs uint64) (string, error)`
- **Synchronization**: Holds `m.Mu.RLock()` throughout execution.
- **Call Graph**:
  1. `m.Mu.RLock()`
  2. State check: `m.state == EngineClosed` $\to$ return `ErrClosed`.
  3. `duringMemtableReadHook()` (test hook).
  4. `m.Mem.Get(byteKey, readTs)`: SkipList index traversal on active Memtable.
     - Found: returns value or `ErrKeyDeleted`.
     - `ErrNotFound`: continues to Tier 2.
  5. Tier 2 Search (`m.Frozenmem`):
     - Iterates backwards: `for i := len(m.Frozenmem) - 1; i >= 0; i--`.
     - `m.Frozenmem[i].Get(byteKey, readTs)`.
     - Found: returns value or `ErrKeyDeleted`.
     - `ErrNotFound`: continues to next frozen generation.
  6. `duringPointReadHook()` (test hook).
  7. Tier 3 Search (`m.ActiveSSreaders`):
     - Iterates backwards: `for i := len(m.ActiveSSreaders) - 1; i >= 0; i--`.
     - `m.ActiveSSreaders[i].Get(byteKey, readTs)`.
     - Found: returns value or `ErrKeyDeleted`.
     - `ErrNotFound`: continues to older SSTable.
  8. Return `ErrNotFound`.
  9. `defer m.Mu.RUnlock()`.
- **Precedence Guarantee**: $\text{Tier 1 (Mem)} \succ \text{Tier 2 (Frozenmem, newest to oldest)} \succ \text{Tier 3 (ActiveSSreaders, newest to oldest)}$.
- **Lock Order**: Operates entirely under `m.Mu.RLock()`. No nested locks acquired.

---

### 2.2 `Transaction.Commit() error`
- **Synchronization**: Holds `t.Man.Mu.Lock()` across validation, sequence allocation, WAL append, and Memtable insertion.
- **Call Graph**:
  1. `t.Man.Mu.Lock()`.
  2. State check: `t.Man.state != EngineOpen` $\to$ abort and return `ErrClosed`.
  3. Conflict Detection Loop:
     - For each `key` in `t.WriteSet`:
     - Calls internal unexported helper `t.Man.latestValueLocked([]byte(key))`.
     - If `latestSeqNo > t.ReadTs`: conflict detected $\to$ abort and return `"Write-write conflict detected"`.
  4. WAL Availability check: `t.Man.Log == nil || t.Man.Log.File == nil` $\to$ return `"wal is unavailable"`.
  5. Sequence Number Allocation: `t.Man.CurrentSeqNo.Add(1)`.
  6. WAL Append and Memtable Insert Loop:
     - For each `key, pendingWrite` in `t.WriteSet`:
     - `encodedKey := EncodeInternalKey(...)`.
     - `t.Man.Log.Append(encodedKey, pendingWrite.Data)`. (Executes `w.File.Sync()`).
     - If WAL write fails $\to$ return error.
     - `t.Man.Mem.Put(encodedKey, pendingWrite.Data)`.
     - `t.Man.Mem.SizeBytes += ...`.
  7. `t.WriteSet = nil`.
  8. `t.Man.Mu.Unlock()`.
  9. Post-Commit Trigger: `t.Man.checkAndTriggerFlush()`.
- **Lock Invariant**: Mutex released before calling `checkAndTriggerFlush()`, eliminating circular wait.

---

### 2.3 `Manager.latestValueLocked(userKey []byte) (uint64, error)`
- **Synchronization**: Assumes caller holds `m.Mu` (`Lock` or `RLock`). Does not acquire mutex.
- **Call Graph**:
  1. Tier 1: `m.Mem.LatestValue(userKey)`. If found $\to$ returns `seq`.
  2. Tier 2: `for i := len(m.Frozenmem) - 1; i >= 0; i--`:
     - `m.Frozenmem[i].LatestValue(userKey)`. If found $\to$ returns `seq`.
  3. Tier 3: `for _, reader := range m.ActiveSSreaders`:
     - `reader.LatestValue(userKey)`.
     - Tracks `maxSeq` across all active readers (E-004 Option A fix).
  4. Returns `maxSeq` or `ErrNotFound`.

---

### 2.4 `Manager.CompactAll() error`
- **Synchronization**: Governed by `m.compactionMu.Lock()`. Short critical sections under `m.Mu.Lock()`.
- **Call Graph**:
  1. `m.compactionMu.Lock()`. (Serializes all compactions against overlapping races, E-006).
  2. Critical Section 1 (`m.Mu.Lock()`):
     - Check `m.isClosed || m.state != EngineOpen`.
     - If `len(m.ActiveSSreaders) <= 1` $\to$ return nil.
     - Snapshot readers: `copy(readersToCompact, m.ActiveSSreaders)`.
     - Open SSTable iterators for input files.
     - `m.Mu.Unlock()`.
  3. Merge & Deduplication (Outside Locks):
     - `duringCompactionMergeHook()` (test hook).
     - Multi-way k-way merge via `minHeap` (`CompactionHeap`).
     - Write merged blocks to `data_compacted_<ts>.sst`.
     - Close builder and open `newReader := NewSSTableReader(...)`.
  4. Critical Section 2 (`m.Mu.Lock()`):
     - Validate input presence, contiguous prefix from index 0 (`INV-COMPACT-1` / E-005).
     - `replaceCompactedReaders(m.ActiveSSreaders, readersToCompact, newReader)`.
     - In-place prefix replacement preserves concurrently flushed readers at tail.
     - `m.Mu.Unlock()`.
  5. Cleanup (Outside Locks):
     - Close all input iterators and retired reader file handles.
     - Delete obsolete on-disk SSTable files (`os.Remove`).
  6. `m.compactionMu.Unlock()`.

---

### 2.5 `flushLoop()` & `flushToDisk(mem *Memtable)`
- **Synchronization**: Dequeue under `m.flushQueueMu.Lock()`. Build outside locks. Publication under `m.Mu.Lock()`. Fail-stop check under `m.Mu.RLock()`.
- **Call Graph**:
  1. `defer m.flushWg.Done()`.
  2. Loop:
     - `m.flushQueueMu.Lock()`.
     - Wait while `len(m.flushQueue) == 0 && !m.flushQueueClosed` on `m.flushQueueCond`.
     - If closed and empty $\to$ `m.flushQueueMu.Unlock()`, return.
     - `mem := m.flushQueue[0]`, pop front.
     - `m.flushQueueMu.Unlock()`.
     - `m.flushToDisk(mem)`.
     - Fail-Stop Verification:
       ```go
       m.Mu.RLock()
       err := m.flushErr
       m.Mu.RUnlock()
       if err != nil {
           return // Halt immediately on fatal error
       }
       ```
  3. `flushToDisk(mem)`:
     - `flushBuildHook()` error check.
     - Create SSTable file: `os.Create(filename)`.
     - Iterate SkipList Level 0: `mem.SK.headNode.Forward[0]`.
     - `builder.Close()`. On error $\to$ unlink file, set `m.flushErr`, return.
     - `NewSSTableReader(filename)`. On error $\to$ unlink file, set `m.flushErr`, return.
     - `beforeFlushPublishHook()` (test hook).
     - Critical Section (`m.Mu.Lock()`):
       - If `m.state == EngineClosed` $\to$ close reader, unlink, return.
       - `m.ActiveSSreaders = append(m.ActiveSSreaders, reader)`.
       - Remove `mem` from `m.Frozenmem`.
       - `m.Mu.Unlock()`.

---

### 2.6 `TriggerFlush()` & `checkAndTriggerFlush()`
- **Synchronization**: Holds `m.Mu.Lock()`.
- **Call Graph**:
  1. `m.Mu.Lock()`.
  2. State check: `m.state != EngineOpen` $\to$ return `ErrClosed`.
  3. Error check: `m.flushErr != nil` $\to$ return `m.flushErr`.
  4. Rotate WAL:
     - `walRotationHook()`.
     - `newWal, err := NewWal(newWalPath)`.
     - `m.Log.File.Close()`.
     - `m.Log = newWal`.
  5. Freeze Active Memtable:
     - `frozen := m.Mem`.
     - `m.Frozenmem = append(m.Frozenmem, frozen)`.
     - `m.Mem = &Memtable{SK: newSkipList()}`.
  6. Enqueue Task:
     - `m.enqueueFlushLocked(frozen)`:
     - Checks `m.flushErr != nil`.
     - `m.flushQueueMu.Lock()`.
     - `m.flushQueue = append(m.flushQueue, frozen)`.
     - `m.flushQueueCond.Signal()`.
     - `m.flushQueueMu.Unlock()`.
  7. `m.Mu.Unlock()`.

---

### 2.7 `Manager.Close() error`
- **Synchronization**: Acquires `m.compactionMu.Lock()`, then coordinates shutdown via 4-phase drain protocol.
- **Call Graph**:
  1. `m.compactionMu.Lock()`. (Prevents compaction starting or publishing during shutdown).
  2. Critical Section 1 (`m.Mu.Lock()`):
     - If `m.state != EngineOpen` $\to$ return `m.flushErr`.
     - `m.state = EngineClosing`.
     - `m.isClosed = true`.
     - Signal queue close:
       - `m.flushQueueMu.Lock()`.
       - `m.flushQueueClosed = true`.
       - `m.flushQueueCond.Broadcast()`.
       - `m.flushQueueMu.Unlock()`.
     - `m.Mu.Unlock()`.
  3. Drain Background Flushes:
     - `m.flushWg.Wait()`. (Drains in-flight and queued flushes outside `m.Mu` to prevent self-deadlock).
  4. Critical Section 2 (`m.Mu.Lock()`):
     - `m.state = EngineClosed`.
     - Close all `m.ActiveSSreaders`.
     - `m.ActiveSSreaders = nil`.
     - Close `m.Log.File`.
     - Return `m.flushErr`.
     - `m.Mu.Unlock()`.
  5. `m.compactionMu.Unlock()`.

---

## 3. Detailed Verification of Audit Items

### Item 1: Synchronization Protocol Compliance
All shared variables are strictly protected by their designated synchronization primitives:
- `m.Mem`, `m.Frozenmem`, `m.ActiveSSreaders`, `m.state`, `m.isClosed`: Protected by `m.Mu` (`Lock` for writes, `RLock` for reads).
- `m.flushQueue`, `m.flushQueueClosed`: Protected by `m.flushQueueMu` and synchronized via `m.flushQueueCond`.
- `m.compactionMu`: Held across the entirety of `CompactAll` and `Close`.
- `m.CurrentSeqNo`: Updated via atomic operation `Add(1)` under `m.Mu.Lock()`.
- Lock hierarchy is strictly acyclic: $\text{m.compactionMu} \to \text{m.Mu} \to \text{m.flushQueueMu} \to \text{Wal.mu}$.

### Item 2: `m.flushErr` Synchronization
Every mutation of `m.flushErr` occurs while holding `m.Mu.Lock()`:
- `manager.go:437, 449, 471, 482`: In `flushToDisk` upon build or reader error.
- `manager.go:314, 324, 338, 348`: In `TriggerFlush` and `checkAndTriggerFlush` upon WAL rotation error.
- `manager.go:178, 186`: In `Close` upon reader or WAL file close error.
Every read of `m.flushErr` occurs under `m.Mu.RLock()` or `m.Mu.Lock()`:
- `manager.go:281`: Worker fail-stop check under `m.Mu.RLock()`.
- `manager.go:292`: Public `FlushErr()` accessor under `m.Mu.RLock()`.
- `manager.go:298`: `enqueueFlushLocked` under `m.Mu.Lock()`.
- `manager.go:313, 326, 377`: `EnqueueFlush`, `TriggerFlush`, and `checkAndTriggerFlush` under `m.Mu.Lock()`.
- `manager.go:153, 189`: `Close()` under `m.Mu.Lock()`.
There are zero unprotected reads or writes. Verified race-free under Go ThreadSanitizer.

### Item 3: Prohibition of Out-of-Order Post-Failure Publication
When `flushToDisk(F0)` encounters an error:
1. `m.flushErr` is set.
2. `flushToDisk` returns without publishing $F_0$ and without removing $F_0$ from `m.Frozenmem`.
3. `flushLoop` evaluates `m.Mu.RLock(); err := m.flushErr; m.Mu.RUnlock()`.
4. Because `err != nil`, `flushLoop` immediately returns (`defer m.flushWg.Done()`).
5. Subsequent tasks in `m.flushQueue` are NEVER dequeued by the worker.
6. Zero SSTables are published to `m.ActiveSSreaders` for $F_1, F_2, \dots$.
7. Precedence inversion between `m.Frozenmem` and `m.ActiveSSreaders` is provably prevented.

### Item 4: Frozen Memtable Accessibility and Ordering
- Unflushed Memtables remain in `m.Frozenmem` in strict chronological freeze order: $[F_0, F_1, F_2, \dots]$.
- Point reads via `Manager.Get` iterate backwards (`i = len-1 down to 0`), reading $F_2$, then $F_1$, then $F_0$.
- Write-write conflict detection via `latestValueLocked` iterates backwards, returning the sequence number of the newest generation.
- Verified by `TestFailedFlush_E010_Resolved_StaleReadPrevented` and `TestFailedFlush_Interaction_SubsequentQueuedFlushes`.

### Item 5: Pending Queue Item Accounting
- When the worker halts on error, items remaining in `m.flushQueue` remain pinned in memory via `m.Frozenmem`.
- No queued item is discarded.
- State is explicitly queryable in memory for all surviving transactions.
- On `Manager.Close()`, `m.flushErr` is returned, alerting the operator that unflushed state resides in memory.

### Item 6: Distinguishability of Shutdown State
- Normal Graceful Shutdown: `Manager.Close()` returns `nil`.
- Failure Shutdown: `Manager.Close()` returns non-nil `m.flushErr`.
- In-flight check: `m.FlushErr()` returns non-nil error before `Close()` is invoked.

### Item 7: Goroutine, Handle, and File Accounting
- **Worker Goroutine**: Terminated cleanly when `flushLoop` returns; `flushWg.Wait()` completes without leaking goroutines.
- **File Descriptors**: All active reader descriptors closed in `Close()`. Active WAL closed in `Close()`.
- **Temp Files**: Any incomplete SSTable file created before error is immediately unlinked via `os.Remove(filename)`.
- Verified on Windows NTFS: directory can be removed immediately after `Close()` without file-sharing violations.

### Item 8: Post-Failure Transaction Policy
- **Policy Definition**: Once a fatal background flush error occurs:
  1. `m.flushErr` is set and `FlushErr()` returns the error.
  2. `TriggerFlush` returns `m.flushErr`.
  3. `checkAndTriggerFlush` skips freezing to avoid accumulating unbounded frozen memory.
  4. In-flight and new transaction commits continue to write to active `m.Mem` and sync to the active WAL file as long as WAL append succeeds.
  5. Point reads continue to observe newly committed writes in active `m.Mem` and historical writes in `m.Frozenmem`.
  6. Disk SSTable publications are suspended.
  7. When `Close()` is called, `m.flushErr` is returned.
- Verified by `TestFailedFlush_Interaction_ConcurrentCommits`.

### Item 9: Durability Boundary Specification
- The storage engine maintains post-crash durability exclusively through WAL records.
- Because `RecoverFromWAL` currently accepts only a single file path (`wal.log`), writes committed to rotated logs (`wal_<timestamp>.log`) require WAL discovery and multi-segment replay upon restart (`RISK-002`).
- No claims of multi-WAL crash consistency or power-loss durability are made for the Approach A baseline candidate.

---

## 4. Audit Conclusion

Sub-phase **I1.5.1** confirms that Approach A's synchronization contracts, fail-stop mechanics, error propagation, resource lifetimes, and operational boundaries are sound, verified, and free of known race conditions or lock inversions.

Execution proceeds to **I1.5.2 (Establish explicit synchronization-mode selection)**.
