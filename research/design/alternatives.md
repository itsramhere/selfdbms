# Approach B: Final Hardened Immutable Reader-Set Snapshots Protocol & Lifecycle Architecture

## 1. Executive Summary & Problem Scope

This document provides the finalized, comprehensive design and protocol closure for **Approach B: Immutable SSTable Reader-Set Snapshots with Two-Tier Reference Ownership**, resolving all protocol consistency items identified in the Checkpoint CP2a external review (Sub-phase I2.7).

Approach B eliminates reader–compactor contention across slow disk I/O operations (`Manager.Get` vs `CompactAll` / `flushToDisk`) while strictly preserving:
1. **`INV-ORDER-1`**: Monotonic chronological SSTable search order (newest to oldest).
2. **`INV-HANDOFF-1`**: Continuous key version visibility across storage tiers with zero loss during memory-to-disk handoffs.
3. **`INV-COMPACT-1`**: Contiguous prefix compaction integrity starting at index 0.
4. **`INV-FLUSH-FIFO-1` & `INV-FLUSH-FAIL-1`**: Strict FIFO flush processing and fail-stop error containment.
5. **`INV-LIFETIME-5`**: Coordinated 4-phase shutdown drain allowing previously admitted flushes to complete and publish safely during `EngineClosing`.
6. **Windows NTFS Compatibility**: Strict two-phase handle closure before unlinking, eliminating `ERROR_SHARING_VIOLATION`.

---

## 2. Coordinated Flush Publication During EngineClosing (I2.7.1)

### 2.1 Distinction of Lifecycle Permissions Across Engine States

In Approach A, `Manager.Close()` coordinates background flushes via a 4-phase drain protocol (`INV-LIFETIME-5`): Memtables enqueued prior to shutdown MUST drain, complete disk builds, and publish before final engine termination. CP2a's preliminary check `if m.state != EngineOpen` prematurely rejected publications during `EngineClosing`. 

The finalized protocol establishes explicit permissions per lifecycle stage:

| Lifecycle Action | `EngineOpen` | `EngineClosing` | `EngineClosed` | Guarding Authority |
| :--- | :--- | :--- | :--- | :--- |
| **New Write Commits** | **Admitted** | **Rejected** (`ErrClosed`) | **Rejected** (`ErrClosed`) | `Manager.Mu.Lock()` |
| **New Flush Admission** (`enqueueFlushLocked`) | **Admitted** | **Rejected** | **Rejected** | `Manager.Mu.Lock()`, `m.flushQueueClosed` |
| **Admitted Flush Execution & Publication** | **Permitted** | **Permitted (Drain Phase)** | **Rejected** | `m.flushQueueMu`, `m.flushWg` |
| **New Query Pin Admission** (`AcquireSnapshot`) | **Admitted** | **Rejected** (`ErrClosed`) | **Rejected** (`ErrClosed`) | `m.snapshotAdmissionClosed atomic.Bool` |
| **Active Query Execution & Release** | **Permitted** | **Permitted (Finishing)** | **Completed** | `m.snapshotMu` |
| **Compaction Publication** | **Permitted** | **Serialized / Rejected** | **Rejected** | `m.compactionMu`, `Manager.Mu.Lock()` |
| **Final Reader & WAL Teardown** | **Forbidden** | **Forbidden** | **Executed** | `Manager.Mu.Lock()` |

### 2.2 Task-Admission Ownership and Generation Tokens

Every flush task receives an explicit task-admission token upon creation in `enqueueFlushLocked`:
- A task is admitted if and only if it is appended to `m.flushQueue` while `m.flushQueueClosed == false` and `m.state == EngineOpen`.
- Once `Manager.Close()` executes, it acquires `m.flushQueueMu.Lock()` and sets `m.flushQueueClosed = true`.
- All tasks already in `m.flushQueue` hold valid admission tokens and are tracked by `m.flushWg`.
- The background FIFO worker processes admitted tasks sequentially. When publishing, it calls `publishFlushLocked(mem, newSR)`.

### 2.3 Corrected `publishFlushLocked` Pseudocode

```go
func (m *Manager) publishFlushLocked(mem *Memtable, newSR *SharedReader) error {
    // PRECONDITION: m.Mu.Lock() is held by the caller (flushLoop / flushToDisk).
    m.snapshotMu.Lock()
    defer m.snapshotMu.Unlock()

    // Permitted in EngineOpen and EngineClosing.
    // Strictly rejected ONLY in EngineClosed.
    if m.state == EngineClosed {
        newSR.SetState(SharedReaderClosing)
        m.pendingCleanupReaders = append(m.pendingCleanupReaders, newSR)
        return ErrClosed
    }

    // 1. Construct new reader slice: copy existing readers, append new reader
    oldSnap := m.currentSnapshot
    var newReaders []*SharedReader
    if oldSnap != nil {
        newReaders = make([]*SharedReader, len(oldSnap.readers)+1)
        copy(newReaders, oldSnap.readers)
        // Increment refCount for all surviving readers copied into new snapshot
        for _, sr := range oldSnap.readers {
            sr.refCount.Add(1)
        }
        newReaders[len(oldSnap.readers)] = newSR
    } else {
        newReaders = []*SharedReader{newSR}
    }

    // 2. Allocate and publish new immutable snapshot
    newSnap := &ReaderSnapshot{
        id:      atomic.AddUint64(&m.snapshotSeq, 1),
        readers: newReaders,
    }
    newSnap.SetState(SnapshotPublished)
    m.currentSnapshot = newSnap

    // 3. Retire old snapshot
    if oldSnap != nil {
        m.retireSnapshotLocked(oldSnap)
    }

    // 4. Atomically remove corresponding flushed Memtable from Frozenmem
    // LINEARIZATION POINT: Key is continuously discoverable in memory or snapshot.
    m.removeFrozenMemtableLocked(mem)

    return nil
}
```

### 2.4 Explicit Barrier Schedule: Admitted Flush During `EngineClosing`

```
Time  Goroutine 1 (Transaction)      Goroutine 2 (Flush Worker)          Goroutine 3 (Manager.Close)
 ───  ────────────────────────      ──────────────────────────          ───────────────────────────
 T0   F0 enqueued under Open
 T1   m.flushWg.Add(1)
 T2                                  F0 begins SSTable disk write
 T3                                                                      Close() acquires Manager.Mu
 T4                                                                      m.state = EngineClosing
 T5                                                                      m.snapshotAdmissionClosed = true
 T6                                                                      m.flushQueueClosed = true
 T7                                                                      Close() releases Manager.Mu
 T8                                                                      Close() blocks on m.flushWg.Wait()
 T9                                  F0 finishes SSTable write
 T10                                 F0 acquires Manager.Mu.Lock()
 T11                                 F0 calls publishFlushLocked()
 T12                                 State is EngineClosing (PERMITTED!)
 T13                                 Snapshot published; Frozenmem removed
 T14                                 F0 releases Manager.Mu.Unlock()
 T15                                 m.flushWg.Done()
 T16                                                                     Close() wakes from flushWg.Wait()
 T17                                                                     Close() drains snapshot pins
 T18                                                                     Close() executes clean teardown
```
*Proof*: F0 completes, publishes its SSTable, removes `mem` from `Frozenmem`, and signals completion to `Close()`. Zero data loss, zero premature rejection, zero file handle leak.

---

## 3. Finalized SharedReader Cleanup Transitions (I2.7.2)

### 3.1 The Unified `checkCleanupEligibilityLocked` Helper

To prevent duplicate cleanup scheduling, race conditions, and reference resurrection, all state transitions affecting references or obsolescence funnel through a single lock-protected helper under `m.snapshotMu`:

```go
// checkCleanupEligibilityLocked evaluates whether a SharedReader has become eligible
// for handle closure or physical unlinking.
// PRECONDITION: m.snapshotMu MUST be held by the caller.
func (m *Manager) checkCleanupEligibilityLocked(sr *SharedReader) {
    if sr == nil {
        return
    }

    refs := sr.refCount.Load()
    isObsolete := sr.obsolete.Load()
    currentState := sr.State()

    // Case A: Obsolete superseded table whose references have all drained to zero.
    // Eligible for handle closure followed by physical unlinking.
    if refs == 0 && isObsolete {
        if currentState == SharedReaderActive || currentState == SharedReaderObsolete {
            sr.SetState(SharedReaderClosing)
            m.pendingCleanupReaders = append(m.pendingCleanupReaders, sr)
            m.hasPendingCleanups = true
        }
        return
    }

    // Case B: Engine shutdown with valid surviving table.
    // The published snapshot reference was released (refs == 0), but the table is NOT obsolete.
    // Eligible for handle closure, but physical file MUST be preserved on disk!
    if m.state >= EngineClosing && refs == 0 && !isObsolete {
        if currentState == SharedReaderActive {
            sr.SetState(SharedReaderClosedValid)
            m.pendingShutdownCloses = append(m.pendingShutdownCloses, sr)
        }
        return
    }

    // Case C: Abandoned unpublished table (e.g. failed snapshot construction or build error).
    // Never published; refs == 0; must be closed and deleted.
    if refs == 0 && currentState == SharedReaderClosing {
        // Already queued for cleanup; no-op to prevent duplicate scheduling.
        return
    }
}
```

### 3.2 Invocation Points for `checkCleanupEligibilityLocked`:
1. Whenever a snapshot drains and decrements constituent reader refcounts (`reclaimSnapshotLocked`).
2. Whenever compaction commits and marks input readers `obsolete = true`.
3. Whenever compaction releases its borrowed input leases (`sr.refCount.Add(-1)`).
4. Whenever snapshot construction rolls back candidate readers after an error.
5. During `Manager.Close()` when `m.currentSnapshot` is retired and drained.

### 3.3 Why a Valid `SharedReader` May Have `refCount == 0 && obsolete == false`
- **Under Normal Runtime**: Every valid SSTable is referenced by at least `m.currentSnapshot` (`refCount >= 1`).
- **During Graceful Shutdown**: `Manager.Close()` sets `m.currentSnapshot = nil` to release memory structures. This decrements each surviving table's snapshot reference from 1 to 0. Because `obsolete == false`, Case B triggers: the reader transitions to `SharedReaderClosedValid`. The handle is closed cleanly, but `os.Remove(Path)` is **strictly skipped**. The file remains durable on disk for subsequent startup and recovery.

---

## 4. Reconciling Close Retries with `sync.Once` (I2.7.3)

In Go, `sync.Once` guarantees exactly-once execution and cannot be reset. Retrying `Close()` on an already-closed `os.File` returns `os.ErrClosed`.

The protocol resolves physical handle closure vs file unlinking as follows:

```
Step 1: Physical File Handle Close
        ┌─────────────────────────┐
        │ sr.closeOnce.Do(Close)  │
        └────────────┬────────────┘
                     │
            Success  │  Failure
        ┌────────────┴────────────┐
        ▼                         ▼
   Handle Closed             Terminal Error
 (sr.closeErr == nil)      (sr.closeErr != nil)
        │                  (sr.state = Failed)
        ▼                         │
Step 2: File Unlink               ▼
 ┌───────────────┐         Reported on Shutdown
 │ os.Remove()   │
 └───────┬───────┘
         │
Success  │  Failure (Windows sharing violation)
 ┌───────┴───────┐
 ▼               ▼
Reclaimed   Retry Unlink (Bounded: 3 attempts, 10ms backoff)
                 │
                 ├─► Success: Reclaimed
                 └─► Exhausted: sr.state = Failed (Reported on Shutdown)
```

### Rules:
1. **Handle `Close()`**: Attempted **exactly once** via `sr.closeOnce`. If `Close()` fails, it is an unrecoverable terminal OS I/O failure on that descriptor; `sr.closeErr` is recorded, and unlinking is abandoned.
2. **File `os.Remove()`**: Executed **only after** `sr.closeErr == nil`. If `os.Remove()` fails due to a temporary Windows sharing violation or antivirus lock, unlinking is retried independently up to 3 times with a 10ms sleep. If retries are exhausted, `sr.unlinkErr` is recorded, `sr.state = SharedReaderFailed`, and the error is surfaced on `Manager.Close()`.
3. **No Unbounded Goroutines**: Retries are executed synchronously within the serialized `drainPendingCleanups()` worker, never in unbounded background goroutines.

---

## 5. Authoritative Synchronization Contract (I2.7.4)

### The Single Authority Rule
- **`Manager.snapshotMu sync.Mutex` is the sole authoritative guard** for all compound lifecycle transitions, reference mutations, and cleanup scheduling on both `ReaderSnapshot` and `SharedReader`.
- **Role of Atomic Fields**: Atomic types (`atomic.Int64`, `atomic.Int32`, `atomic.Bool`) are retained strictly for thread-safe, lock-free **read-only observability** (e.g. diagnostic logging, testing assertions, metrics). They **do not** provide compound atomicity for multi-field decisions.
- **Audit of Accessors**:
  - `ReaderSnapshot.State()`: Thread-safe read accessor (`SnapshotState(rs.state.Load())`).
  - `ReaderSnapshot.Pins()`: Thread-safe read accessor (`rs.pins.Load()`).
  - `ReaderSnapshot.SetState()`: Internal state mutator. In production code, caller MUST hold `m.snapshotMu`.
  - `SharedReader.RefCount()` / `State()`: Thread-safe read accessors.
  - `SharedReader.Ref()` / `SetState()`: Internal state mutators. In production code, caller MUST hold `m.snapshotMu`.

---

## 6. Complete Shutdown Ordering Pseudocode (I2.7.5)

```go
func (m *Manager) Close() error {
    // 1. Close write and flush admission under Manager.Mu
    m.Mu.Lock()
    if m.state != EngineOpen {
        m.Mu.Unlock()
        return ErrClosed
    }
    m.state = EngineClosing
    m.isClosed = true

    // Close query pin admission under snapshotMu
    m.snapshotMu.Lock()
    m.snapshotAdmissionClosed.Store(true)
    m.snapshotMu.Unlock()
    m.Mu.Unlock() // Crucial: Release Manager.Mu before drain waits!

    // 2. Drain previously admitted flushes
    m.flushQueueMu.Lock()
    m.flushQueueClosed = true
    m.flushQueueCond.Broadcast()
    m.flushQueueMu.Unlock()
    m.flushWg.Wait() // Waits for admitted flushes (publishFlushLocked succeeds in EngineClosing!)

    // 3. Coordinate background compaction
    m.compactionMu.Lock() // Waits for any active compaction merge to complete or abort

    // 4. Retire current published snapshot and wait for query pins
    m.snapshotMu.Lock()
    if m.currentSnapshot != nil {
        m.retireSnapshotLocked(m.currentSnapshot)
        m.currentSnapshot = nil
    }

    // Wait for in-flight pins to reach zero across all retired snapshots
    for m.countTotalActivePinsLocked() > 0 {
        // sync.Cond.Wait releases snapshotMu while sleeping!
        // Does NOT hold Manager.Mu, compactionMu, or flushQueueMu.
        m.snapshotDrainCond.Wait()
    }

    // 5. All pins drained: evaluate cleanup eligibility for all readers
    m.evaluateShutdownReaderCleanupLocked()
    m.snapshotMu.Unlock()
    m.compactionMu.Unlock()

    // 6. Execute physical handle closures and unlinks outside all locks
    cleanupErr := m.drainPendingCleanups()

    // 7. Transition to final EngineClosed state
    m.Mu.Lock()
    m.state = EngineClosed
    m.Mu.Unlock()

    // 8. Close WAL file and surface errors
    walErr := m.Log.File.Close()
    if m.flushErr != nil {
        return m.flushErr
    }
    if cleanupErr != nil {
        return cleanupErr
    }
    return walErr
}
```

### Lock and Wait Safety Invariants:
1. `m.snapshotDrainCond` is initialized with `&m.snapshotMu`.
2. While `m.snapshotDrainCond.Wait()` sleeps, `m.snapshotMu` is completely unlocked.
3. `ReleaseSnapshot` acquires `m.snapshotMu`, decrements `snap.pins`, and calls `m.snapshotDrainCond.Broadcast()`.
4. Neither caller holds `Manager.Mu` during the wait. Circular wait is eliminated.

---

## 7. Complete Lock and Wait Dependency Graph (I2.7.6)

```
Level 1: compactionMu     (sync.Mutex: serializes background compaction routines)
Level 2: Manager.Mu       (sync.RWMutex: engine state, active Memtable, Frozenmem)
Level 3: snapshotMu       (sync.Mutex: snapshot pointers, pin counters, retired queue)
Level 4: flushQueueMu     (sync.Mutex: FIFO flush task queue slice and condvar)
Level 5: Wal.mu           (sync.Mutex: append-only WAL disk append and sync)
Leaf:    cleanupMu        (sync.Mutex: serialized background file cleanup)
```

*Properties*:
- Strict ascending lock acquisition order.
- Zero locks held across slow disk reads or writes.
- Wait conditions (`flushQueueCond.Wait()`, `snapshotDrainCond.Wait()`) release their guarding mutexes before blocking.

---

## 8. Corrected and Extended Phase I3 Deterministic Test Matrix

The test matrix contains **16 deterministic test specifications** with named synchronization barriers:

1. `TestApproachB_SnapshotLifecycle_LinearProgression`
2. `TestApproachB_PinningUnderflowPanic`
3. `TestApproachB_RefcountZeroAndObsoleteOrdering_CaseA` (refCount to 0, then obsolete=true)
4. `TestApproachB_RefcountZeroAndObsoleteOrdering_CaseB` (obsolete=true, then refCount to 0)
5. `TestApproachB_NoResurrectionAfterZeroRef`
6. `TestApproachB_ConstructionRollback`
7. `TestApproachB_UnchangedSSTableSharedAcrossSnapshots`
8. `TestApproachB_ObsoleteReaderRetainedByOldPinnedSnapshot`
9. `TestApproachB_NormalRuntimeCleanupWithoutClose`
10. `TestApproachB_Shutdown_CloseValidWithoutUnlink`
11. `TestApproachB_CleanupFailure_RetryPolicy`
12. `TestApproachB_EmptySnapshotLookup`
13. `TestApproachB_Shutdown_OverlapWithActivePinsAndCompaction`
14. `TestApproachB_FrozenMemtableHandoff_ZeroVisibilityGap` (two-way barrier test)
15. `TestApproachB_VersionOrderingAndTombstoneMasking`
16. `TestApproachB_Shutdown_AdmittedFlushCompletesDuringEngineClosing` (admitted flush publishes safely during drain)
