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
// Invariant: ZERO access to Manager.Mu-guarded mutable state (e.g. m.state).
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
    // Inspected strictly via snapshotMu-guarded m.snapshotPhase (never Manager.Mu state).
    // The published snapshot reference was released (refs == 0), but the table is NOT obsolete.
    // Eligible for handle closure, but physical file MUST be preserved on disk!
    if m.snapshotPhase >= SnapshotPhaseDraining && refs == 0 && !isObsolete {
        if currentState == SharedReaderActive {
            sr.SetState(SharedReaderClosedValid)
            m.pendingShutdownCloses = append(m.pendingShutdownCloses, sr)
        }
        return
    }

    // Case C: Abandoned unpublished table (e.g. failed snapshot construction or build rollback).
    // Never published to active snapshots (isObsolete == false); refs == 0.
    // Initial builder reference relinquished via rollback; must be closed and deleted.
    if refs == 0 && currentState == SharedReaderUnpublished {
        sr.SetState(SharedReaderClosing)
        m.pendingCleanupReaders = append(m.pendingCleanupReaders, sr)
        m.hasPendingCleanups = true
        return
    }

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
4. Whenever snapshot construction rolls back candidate readers after an error (`rollbackUnpublishedReader`).
5. During `Manager.Close()` when `m.currentSnapshot` is retired and drained.

### 3.3 Why a Valid `SharedReader` May Have `refCount == 0 && obsolete == false`
- **Under Normal Runtime**: Every valid SSTable is referenced by at least `m.currentSnapshot` (`refCount >= 1`).
- **During Graceful Shutdown**: `Manager.Close()` sets `m.currentSnapshot = nil` to release memory structures. This decrements each surviving table's snapshot reference from 1 to 0. Because `obsolete == false` and `m.snapshotPhase >= SnapshotPhaseDraining`, Case B triggers: the reader transitions to `SharedReaderClosedValid`. The handle is closed cleanly, but `os.Remove(Path)` is **strictly skipped**. The file remains durable on disk for subsequent startup and recovery.

### 3.4 Abandoned Unpublished SSTable Cleanup Protocol (CP2b-V.4)

When an SSTable is created by a flush worker (`flushToDisk`) or compaction worker (`CompactAll`), but subsequent snapshot publication fails, the reader is cleaned up according to the following explicit protocol:

1. **Initial Ownership & Reference Count**:
   - The builder goroutine allocates the `*sstableReader` and wraps it into a `*SharedReader`.
   - Initial state: `sr.state.Store(int32(SharedReaderUnpublished))`.
   - Initial reference: `sr.refCount.Store(1)` (owned strictly by the builder goroutine).
   - Initial obsolescence: `sr.obsolete.Store(false)` (never published; never obsolete).
2. **Construction Rollback Relinquishing Ownership**:
   - If publication fails (e.g. engine closed, I/O failure, context cancelled), the builder calls `m.rollbackUnpublishedReader(sr)`.
   - Under `m.snapshotMu.Lock()`:
     - `sr.refCount.Add(-1)` decrements the builder's reference count from 1 to 0.
     - Calls `m.checkCleanupEligibilityLocked(sr)`.
     - Matches **Case C** (`refs == 0 && currentState == SharedReaderUnpublished`).
     - Atomically transitions `sr.SetState(SharedReaderClosing)` and enqueues `sr` into `m.pendingCleanupReaders`.
   - Under `m.snapshotMu.Unlock()`.
3. **Close-Before-Unlink on Windows NTFS**:
   - Physical disposal occurs in `m.drainPendingCleanups()` strictly outside all locks.
   - `sr.closeOnce.Do(func() { sr.closeErr = sr.Reader.Close() })` closes the OS file descriptor first.
   - If `Close()` fails: `sr.SetState(SharedReaderFailed)`. File unlinking is aborted to prevent Windows sharing violations.
   - If `Close()` succeeds: `sr.SetState(SharedReaderClosed)`. `os.Remove(sr.Path)` is executed with bounded retries (3 attempts, 10ms backoff). Upon success, `sr.SetState(SharedReaderReclaimed)`.
4. **Distinction from Obsolete SSTables**:
   - *Obsolete SSTable*: Previously published, superseded by newer snapshot, `isObsolete == true`. Must remain open while older pinned snapshots hold references (`refs > 0`). Disposed via Case A when pins drain.
   - *Unpublished Abandoned SSTable*: Never published, `isObsolete == false`, `state == SharedReaderUnpublished`. Disposed immediately via Case C upon builder rollback.
5. **Error Propagation**:
   - `sr.closeErr` and `sr.unlinkErr` are recorded on `sr`.
   - Flushes fail-stop under `INV-FLUSH-FAIL-1`; compaction aborts its job and retains inputs; `Close()` surfaces accumulated cleanup errors.

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

## 6. Complete Shutdown Ordering Pseudocode (I2.7.5 / CP2b-V.2 / CP2b-V.3)

```go
func (m *Manager) Close() error {
    // 1. Initiate shutdown or wait for in-progress shutdown to complete (CP2b-V.2)
    m.Mu.Lock()
    if m.state != EngineOpen {
        m.Mu.Unlock()
        // Concurrent Close callers wait outside ALL locks until teardown finishes!
        // Holds ZERO locks required by flush worker, pin release, or cleanup path.
        <-m.closeDone
        m.Mu.Lock()
        err := m.closeErr
        m.Mu.Unlock()
        return err
    }
    m.state = EngineClosing
    m.isClosed = true

    // Close query pin admission under snapshotMu (CP2b-V.1)
    m.snapshotMu.Lock()
    m.snapshotPhase = SnapshotPhaseDraining
    m.snapshotAdmissionClosed.Store(true)
    m.snapshotMu.Unlock()
    m.Mu.Unlock() // Crucial: Release Manager.Mu before drain waits!

    // 2. Drain previously admitted flushes
    m.flushQueueMu.Lock()
    m.flushQueueClosed = true
    m.flushQueueCond.Broadcast()
    m.flushQueueMu.Unlock()
    m.flushWg.Wait() // Waits for admitted flushes (publishFlushLocked succeeds in EngineClosing!)

    // 3. Coordinate background compaction (Choice B, CP2b-V.3)
    m.compactionMu.Lock()
    m.compactionAdmissionClosed = true // Reject new compaction jobs
    // Active compaction merge (if any) completes or aborts here.
    m.compactionMu.Unlock() // Crucial: Release compactionMu BEFORE waiting on snapshot pins!

    // 4. Retire current published snapshot and wait for query pins
    m.snapshotMu.Lock()
    if m.currentSnapshot != nil {
        m.retireSnapshotLocked(m.currentSnapshot)
        m.currentSnapshot = nil
    }

    // Wait for in-flight pins to reach zero across all retired snapshots.
    // NOTE: sync.Cond.Wait releases ONLY its associated mutex (&m.snapshotMu).
    // Because compactionMu was unlocked in Step 3 and Manager.Mu was unlocked in Step 1,
    // ZERO outer locks are held during this wait!
    for m.countTotalActivePinsLocked() > 0 {
        m.snapshotDrainCond.Wait()
    }

    // 5. All pins drained: evaluate cleanup eligibility for all readers
    m.evaluateShutdownReaderCleanupLocked()
    m.snapshotPhase = SnapshotPhaseClosed
    m.snapshotMu.Unlock()

    // 6. Execute physical handle closures and unlinks outside all locks
    cleanupErr := m.drainPendingCleanups()

    // 7. Close WAL file outside all locks
    var walErr error
    if m.Log != nil && m.Log.File != nil {
        walErr = m.Log.File.Close()
    }

    // 8. Determine unified final error and transition to EngineClosed
    var finalErr error
    if m.flushErr != nil {
        finalErr = m.flushErr
    } else if cleanupErr != nil {
        finalErr = cleanupErr
    } else {
        finalErr = walErr
    }

    m.Mu.Lock()
    m.state = EngineClosed
    m.closeErr = finalErr
    m.Mu.Unlock()

    // 9. Unblock all waiting concurrent Close() callers (CP2b-V.2)
    close(m.closeDone)

    return finalErr
}
```

### Lock and Wait Safety Invariants:
1. `m.snapshotDrainCond` is initialized with `&m.snapshotMu`.
2. While `m.snapshotDrainCond.Wait()` sleeps, `m.snapshotMu` is completely unlocked.
3. `ReleaseSnapshot` acquires `m.snapshotMu`, decrements `snap.pins`, and calls `m.snapshotDrainCond.Broadcast()`.
4. Neither caller holds `Manager.Mu` or `compactionMu` during the wait. Circular wait is eliminated.

---

## 7. Complete Lock and Wait Dependency Graph (CP2b-V.3)

```
Level 0: Concurrent Close Barrier (m.closeDone chan struct{}: non-mutex channel wait; zero locks held)
Level 1: Manager.Mu       (sync.RWMutex: engine state m.state, isClosed, active Mem, Frozenmem, closeErr)
Level 2: compactionMu     (sync.Mutex: serializes compaction routines, INV-SERIAL-COMPACT-1; released before pin drain)
Level 3: flushQueueMu     (sync.Mutex: FIFO flush task queue slice and flushQueueCond, INV-QUEUE-1)
Level 4: snapshotMu       (sync.Mutex: leaf state, snapshotPhase, currentSnapshot, retiredSnapshots, SharedReader states, snapshotDrainCond)
Level 5: Wal.mu           (sync.Mutex: append-only WAL disk append and sync)
Leaf:    cleanupMu        (sync.Mutex: serialized background file cleanup outside all engine locks)
```

*Properties*:
- **Strict Ascending Acquisition**: If multiple locks are held, they must be acquired in ascending order.
  - Flush publication: holds `Manager.Mu` -> acquires `m.snapshotMu`.
  - Compaction publication: holds `compactionMu` -> acquires `m.snapshotMu`.
  - Leaf lock `snapshotMu` NEVER acquires `Manager.Mu`, `compactionMu`, or `flushQueueMu`.
- **Zero Locks During Slow I/O**: SSTable reads, disk appends, and unlinks execute with zero locks held.
- **Condition Variable Scope**: `sync.Cond.Wait()` releases *only* its associated mutex:
  - `flushQueueCond.Wait()` releases/reacquires `flushQueueMu`.
  - `snapshotDrainCond.Wait()` releases/reacquires `snapshotMu`.
  - `compactionMu` is explicitly unlocked prior to `snapshotDrainCond.Wait()` (Choice B).

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

---

## 9. Five Deterministic Concurrency & Lifecycle Interleaving Traces (CP2b-V.5)

### Case 1 — Flush During Shutdown
- **Context**: Flush task F0 is admitted while `m.state == EngineOpen`. `Manager.Close()` begins while F0 is building on disk.
- **Trace**:
  1. Flush worker pops F0 from FIFO queue. State is `EngineOpen`. Disk write begins for `data_001.sst`.
  2. Caller 1 invokes `Manager.Close()`.
  3. Under `Manager.Mu.Lock()`, Caller 1 observes `m.state == EngineOpen`. Transitions `m.state = EngineClosing`, `m.isClosed = true`.
  4. Caller 1 acquires `m.snapshotMu.Lock()`, sets `m.snapshotPhase = SnapshotPhaseDraining`, `m.snapshotAdmissionClosed = true`, and unlocks `m.snapshotMu`.
  5. Caller 1 unlocks `Manager.Mu`.
  6. Caller 1 closes flush admission: acquires `m.flushQueueMu.Lock()`, sets `m.flushQueueClosed = true`, signals condvar, unlocks `m.flushQueueMu`.
  7. Caller 1 blocks on `m.flushWg.Wait()`. Zero locks held.
  8. Flush worker completes disk write, opens `reader`, constructs `sr` with `state = SharedReaderUnpublished`, `refCount = 1`.
  9. Flush worker acquires `Manager.Mu.Lock()` and invokes `publishFlushLocked(mem, sr)`.
  10. `publishFlushLocked` acquires `m.snapshotMu.Lock()`.
  11. Inspects `m.state`: value is `EngineClosing`. Permitted by Decision D-025 and `INV-SNAP-7`.
  12. Copies readers from `m.currentSnapshot`, increments their `refCount`, appends `sr` (`sr.SetState(SharedReaderActive)`).
  13. Publishes new `ReaderSnapshot`, retires previous snapshot, atomically removes `mem` from `m.Frozenmem`.
  14. Unlocks `m.snapshotMu`, unlocks `Manager.Mu`.
  15. Flush worker calls `m.flushWg.Done()`.
  16. Caller 1 unblocks from `m.flushWg.Wait()`. Drains compaction, retires final snapshot, drains pins, executes cleanup, closes WAL, sets `EngineClosed`.
- **Result**: Zero data loss, F0 completes durably, frozen Memtable removed, clean shutdown.

### Case 2 — Two Concurrent Close Calls
- **Context**: Caller A and Caller B call `Manager.Close()` concurrently. An injected error occurs during SSTable handle closure (`io.ErrUnexpectedEOF`).
- **Trace**:
  1. Caller A invokes `Close()`. Acquires `m.Mu.Lock()`. Sees `m.state == EngineOpen`.
  2. Caller A transitions `m.state = EngineClosing`, `m.isClosed = true`. Unlocks `m.Mu`.
  3. Caller B invokes `Close()`. Acquires `m.Mu.Lock()`. Sees `m.state == EngineClosing` (`!= EngineOpen`).
  4. Caller B unlocks `m.Mu`.
  5. Caller B executes `<-m.closeDone`. Caller B blocks, holding **ZERO** mutexes.
  6. Caller A closes flush admission, waits on `m.flushWg.Wait()`.
  7. Caller A coordinates compaction under `compactionMu`, sets `compactionAdmissionClosed = true`, unlocks `compactionMu`.
  8. Caller A retires `m.currentSnapshot`, waits on `snapshotDrainCond.Wait()` until all pins drain.
  9. Caller A evaluates cleanup under `m.snapshotMu`. Sets `m.snapshotPhase = SnapshotPhaseClosed`, unlocks `m.snapshotMu`.
  10. Caller A invokes `m.drainPendingCleanups()`. Injected error: SSTable reader `Close()` returns `io.ErrUnexpectedEOF`.
  11. `cleanupErr` captures `io.ErrUnexpectedEOF`. `finalErr = io.ErrUnexpectedEOF`.
  12. Caller A closes WAL file.
  13. Caller A acquires `m.Mu.Lock()`, sets `m.state = EngineClosed`, records `m.closeErr = finalErr`, unlocks `m.Mu`.
  14. Caller A executes `close(m.closeDone)`.
  15. Caller A returns `finalErr` (`io.ErrUnexpectedEOF`).
  16. Caller B unblocks from `<-m.closeDone`.
  17. Caller B acquires `m.Mu.Lock()`, reads `err := m.closeErr`, unlocks `m.Mu`.
  18. Caller B returns `err` (`io.ErrUnexpectedEOF`).
- **Result**: Exactly-once teardown; Caller B waited outside locks; both callers received identical final error; no duplicate cleanup.

### Case 3 — Retired Reader with Outstanding Snapshot Pin
- **Context**: Snapshot S0 contains reader R1 (`refCount == 2`). Query Q1 holds pin on S0 (`S0.pins == 1`). Compaction completes S1 superseding R1.
- **Trace**:
  1. Q1 acquires pin on S0: under `m.snapshotMu`, `S0.pins.Add(1)`. Q1 begins reading.
  2. Compaction worker builds replacement table R2, acquires `compactionMu`.
  3. Compaction worker publishes S1 containing R2 (excluding R1).
  4. Under `m.snapshotMu`: S0 is retired to `m.retiredSnapshots`.
  5. R1 is marked `R1.obsolete.Store(true)`.
  6. R1 refCount is decremented by 1 (no longer in active snapshot): `R1.refCount` becomes 1 (held by S0).
  7. `checkCleanupEligibilityLocked(R1)` observes `refs == 1 != 0`. No cleanup scheduled. `R1.State()` is `SharedReaderObsolete`.
  8. Q1 continues executing binary searches and data block reads on R1 via open OS handle.
  9. Q1 finishes query and calls `ReleaseSnapshot(S0)`.
  10. Under `m.snapshotMu`, `S0.pins.Add(-1)` reaches 0. S0 is drained.
  11. S0 dereferences constituent reader R1: `R1.refCount.Add(-1)` reaches 0.
  12. `checkCleanupEligibilityLocked(R1)` is dispatched under `m.snapshotMu`.
  13. Case A matches: `refs == 0 && isObsolete == true`.
  14. Transitions `R1.SetState(SharedReaderClosing)`.
  15. Appends R1 to `m.pendingCleanupReaders`.
  16. Outside locks: `R1.closeOnce.Do(R1.Reader.Close)` closes file descriptor.
  17. `os.Remove(R1.Path)` unlinks file from disk. `R1.SetState(SharedReaderReclaimed)`.
- **Result**: Reader remains open throughout pinned query execution; file handle closed before unlinking; zero use-after-free or sharing violations.

### Case 4 — Abandoned Unpublished Reader
- **Context**: Flush worker builds `temp_data.sst`, but snapshot publication fails (e.g. disk quota error during metadata write, or engine closed).
- **Trace**:
  1. Flush worker builds `temp_data.sst` on disk, opens `reader`, wraps in `sr := &SharedReader{Path: "temp_data.sst"}`.
  2. Initial state: `sr.SetState(SharedReaderUnpublished)`, `sr.refCount = 1`, `sr.obsolete = false`.
  3. Flush worker attempts publication, encounters fatal error (e.g. `m.state == EngineClosed`).
  4. Builder recognizes publication failure and calls `m.rollbackUnpublishedReader(sr)`.
  5. Under `m.snapshotMu.Lock()`:
     - Relinquishes builder's reference: `sr.refCount.Add(-1)` drops count from 1 to 0.
     - Calls `m.checkCleanupEligibilityLocked(sr)`.
     - Case C matches: `refs == 0 && currentState == SharedReaderUnpublished`.
     - Atomically transitions `sr.SetState(SharedReaderClosing)`.
     - Enqueues `sr` into `m.pendingCleanupReaders`.
  6. Under `m.snapshotMu.Unlock()`.
  7. Outside all locks, cleanup worker executes:
     - `sr.closeOnce.Do(sr.Reader.Close)`. OS file handle closed cleanly.
     - `sr.SetState(SharedReaderClosed)`.
     - `os.Remove(sr.Path)` deletes `temp_data.sst`.
     - `sr.SetState(SharedReaderReclaimed)`.
  8. Error returned to flush worker, which halts under `INV-FLUSH-FAIL-1`.
- **Result**: Abandoned reader disposed without ever being marked obsolete; handle closed before unlinking; zero disruption to active snapshots.

### Case 5 — Cleanup During Shutdown
- **Context**: Query Q2 holds pin on snapshot S2. `Manager.Close()` begins teardown concurrently.
- **Trace**:
  1. `Close()` caller executes Steps 1-3: sets `EngineClosing`, `snapshotPhase = SnapshotPhaseDraining`, drains flushes, coordinates compaction and unlocks `compactionMu`.
  2. `Close()` acquires `m.snapshotMu.Lock()`. Retires `m.currentSnapshot`.
  3. `Close()` checks `countTotalActivePinsLocked()`: sees Q2 active on S2 (`pins == 1`).
  4. `Close()` enters wait loop: calls `m.snapshotDrainCond.Wait()`.
  5. `sync.Cond.Wait()` unlocks `m.snapshotMu` and blocks. Zero locks held by `Close()`.
  6. Q2 completes query and calls `ReleaseSnapshot(S2)`.
  7. Q2 acquires `m.snapshotMu.Lock()`. Decrements `S2.pins` to 0.
  8. Q2 signals `m.snapshotDrainCond.Broadcast()`.
  9. Q2 dereferences constituent readers under `m.snapshotMu`:
     - Surviving active table R_surv: `refs == 0`, `isObsolete == false`.
     - `checkCleanupEligibilityLocked(R_surv)` observes `m.snapshotPhase == SnapshotPhaseDraining && refs == 0 && !isObsolete` (Case B).
     - Transitions `R_surv.SetState(SharedReaderClosedValid)`.
     - Enqueues R_surv to `m.pendingShutdownCloses` (file preserved on disk!).
  10. Q2 unlocks `m.snapshotMu`.
  11. `Close()` wakes from `Wait()`, re-acquires `m.snapshotMu`.
  12. `Close()` checks `countTotalActivePinsLocked()`: count is 0. Loop exits.
  13. `Close()` sets `m.snapshotPhase = SnapshotPhaseClosed`. Unlocks `m.snapshotMu`.
  14. `Close()` executes `m.drainPendingCleanups()`: closes handles in `pendingShutdownCloses` without unlinking; closes and unlinks obsolete tables in `pendingCleanupReaders`.
  15. `Close()` closes WAL, sets `EngineClosed`, unblocks waiting callers via `closeDone`.
- **Result**: Zero data races, no deadlocks, clean drain, valid database files preserved durably on disk.
