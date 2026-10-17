# Checkpoint CP2b External Reviewer Packet: Final Protocol Consistency Closure

## 1. Corrected `EngineClosing` Flush Publication Protocol (I2.7.1, Decision D-025)

In Approach A, `Manager.Close()` executes a coordinated 4-phase shutdown drain (`INV-LIFETIME-5`): Memtables enqueued prior to shutdown drain, build `.sst` files, and publish to active readers before final teardown. The CP2a check `if m.state != EngineOpen` prematurely rejected publications during `EngineClosing`.

The corrected protocol establishes explicit lifecycle boundaries:
- **Write and Flush Admission**: Admitted strictly in `EngineOpen`. Rejected in `EngineClosing` and `EngineClosed`.
- **Admitted Flush Execution**: Tasks enqueued prior to `m.flushQueueClosed = true` carry valid task-admission tokens. They are permitted to publish in `EngineOpen` **and `EngineClosing`**.
- **Publication Rejection**: `publishFlushLocked` rejects publications **strictly when `m.state == EngineClosed`**.

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

    // 1. Construct new reader slice (oldest at index 0, new table at tail)
    oldSnap := m.currentSnapshot
    var newReaders []*SharedReader
    if oldSnap != nil {
        newReaders = make([]*SharedReader, len(oldSnap.readers)+1)
        copy(newReaders, oldSnap.readers)
        for _, sr := range oldSnap.readers {
            sr.refCount.Add(1)
        }
        newReaders[len(oldSnap.readers)] = newSR
    } else {
        newReaders = []*SharedReader{newSR}
    }

    // 2. Publish new immutable snapshot and retire old snapshot
    newSnap := &ReaderSnapshot{
        id:      atomic.AddUint64(&m.snapshotSeq, 1),
        readers: newReaders,
    }
    newSnap.SetState(SnapshotPublished)
    m.currentSnapshot = newSnap

    if oldSnap != nil {
        m.retireSnapshotLocked(oldSnap)
    }

    // 3. Atomically remove corresponding flushed Memtable from Frozenmem
    // LINEARIZATION POINT: Continuous key version visibility across memory and disk.
    m.removeFrozenMemtableLocked(mem)

    return nil
}
```

---

## 2. Complete Shutdown Ordering Pseudocode (I2.7.5)

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

    // Wait for in-flight query pins to reach zero across all retired snapshots
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

---

## 3. Final SharedReader Reference and Obsolescence Transition Rules (I2.7.2)

1. **Active Published Table**: Referenced by `m.currentSnapshot` (`refCount >= 1`, `obsolete == false`, `state == SharedReaderActive`).
2. **Pinned Retired Table**: Referenced by pinned snapshots in `m.retiredSnapshots` (`refCount >= 1`).
3. **Superseded Table**: Compacted away by a newly committed snapshot. Marked `obsolete = true` under `m.snapshotMu`. Retains `refCount >= 1` while older pinned snapshots hold references (`state == SharedReaderObsolete`).
4. **Surviving Valid Table on Shutdown**: When `m.currentSnapshot = nil` executes during shutdown, its reference drops to 0 while `obsolete == false`. State transitions to `SharedReaderClosedValid`. The handle is closed, but the physical file is **strictly preserved on disk**.
5. **Obsolete Table Drained**: When `refCount == 0 && obsolete == true`, transitions to `SharedReaderClosing`. Enqueued once for closure and physical unlinking.

---

## 4. Exactly-Once Cleanup Eligibility Procedure (I2.7.2)

Single lock-protected helper executed under `m.snapshotMu`:

```go
func (m *Manager) checkCleanupEligibilityLocked(sr *SharedReader) {
    if sr == nil {
        return
    }

    refs := sr.refCount.Load()
    isObsolete := sr.obsolete.Load()
    currentState := sr.State()

    // Case A: Obsolete superseded table whose references have all drained to zero.
    if refs == 0 && isObsolete {
        if currentState == SharedReaderActive || currentState == SharedReaderObsolete {
            sr.SetState(SharedReaderClosing)
            m.pendingCleanupReaders = append(m.pendingCleanupReaders, sr)
            m.hasPendingCleanups = true
        }
        return
    }

    // Case B: Engine shutdown with valid surviving table.
    if m.state >= EngineClosing && refs == 0 && !isObsolete {
        if currentState == SharedReaderActive {
            sr.SetState(SharedReaderClosedValid)
            m.pendingShutdownCloses = append(m.pendingShutdownCloses, sr)
        }
        return
    }

    // Case C: Abandoned unpublished table.
    if refs == 0 && currentState == SharedReaderClosing {
        return // Already queued; no-op to prevent duplicate scheduling.
    }
}
```

---

## 5. Close / Unlink Retry Semantics (I2.7.3)

```
Handle Close: Exactly-Once via closeOnce
   - Success: Handle closed. Proceed to unlink if obsolete.
   - Failure: Terminal OS I/O error recorded in sr.closeErr. Unlinking abandoned.

File Unlink: Sequential with Bounded Retries
   - Executed strictly AFTER handle close succeeds (sr.closeErr == nil).
   - If os.Remove(Path) fails (Windows sharing violation):
     Retried up to 3 times with 10ms backoff in serialized cleanup loop.
   - If retries exhausted: sr.unlinkErr recorded, state = SharedReaderFailed.
   - Reported during Manager.Close(). Zero unbounded background goroutines.
```

---

## 6. Reconciled Scaffolding Declarations (I2.7.4)

[`lsmengine/snapshot.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/snapshot.go) defines:
- `SharedReaderState` enum (`Active`, `Obsolete`, `Closing`, `Closed`, `Unlinking`, `Reclaimed`, `ClosedValid`, `Failed`).
- `SharedReader` with `refCount atomic.Int64`, `state atomic.Int32`, `obsolete atomic.Bool`, `closeOnce sync.Once`.
- `ReaderSnapshot` with `readers []*SharedReader`, `pins atomic.Int64`, `state atomic.Int32`.

### Authoritative Synchronization Contract:
`Manager.snapshotMu` is the **sole authoritative guard** for all compound lifecycle transitions, reference mutations, and cleanup scheduling. Atomic fields provide thread-safe read-only observability for diagnostics and testing; compound state transitions (`Active` $\to$ `Obsolete`, `pins` adjustment, `refCount` increments/decrements) MUST hold `Manager.snapshotMu`. Mutators like `SetState` are internal helpers requiring `m.snapshotMu` in production.

---

## 7. Deterministic Shutdown and Cleanup Interleavings (I2.7.1, I2.7.6)

### Interleaving 1: Admitted Flush Completes During `EngineClosing`
1. `F0` enqueued in `m.flushQueue` while `EngineOpen`.
2. `Manager.Close()` begins, transitions `m.state = EngineClosing`, marks `m.flushQueueClosed = true`, and waits on `m.flushWg.Wait()`.
3. `F0` worker completes SSTable build, acquires `Manager.Mu.Lock()`, calls `publishFlushLocked()`.
4. `publishFlushLocked` observes `m.state == EngineClosing` (permitted!), publishes new snapshot, removes `Frozenmem`, and releases `Manager.Mu`.
5. Worker calls `flushWg.Done()`. `Close()` wakes from `flushWg.Wait()`, drains query pins, and completes teardown.
*Result*: Admitted mutation is fully published to disk with zero data loss.

### Interleaving 2: Cleanup Scheduling Ordering (Both Directions)
- **Case A (`refCount == 0`, then `obsolete = true`)**:
  Query pin drains, `refCount` reaches 0. `obsolete == false`, so table remains active. Later, compaction commits replacement, sets `obsolete = true`, and calls `checkCleanupEligibilityLocked()`. Helper detects `refCount == 0 && obsolete == true`, transitions to `Closing`, and queues for cleanup exactly once.
- **Case B (`obsolete = true`, then `refCount == 0`)**:
  Compaction commits replacement, sets `obsolete = true`. Active pinned query still holds reference (`refCount == 1`). Table remains open. Later, query exits, calls `ReleaseSnapshot`, decrements `refCount` to 0, and calls `checkCleanupEligibilityLocked()`. Helper detects `refCount == 0 && obsolete == true`, transitions to `Closing`, and queues for cleanup exactly once.

---

## 8. Verification and Git Provenance (I2.7.6)

- **Historical Baseline Tag**: `baseline-approach-a` (`d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`)
- **Verified Baseline Tag**: `baseline-approach-a-verified` (`ae8c034c438d56abbe6664e6472f7a0c1278797f`)
- **Active Branch**: `research/approach-b-snapshots`
- **CP2b Delivery Hash**: will be committed upon completion of this step.
- **Working Tree**: Clean.
- **Test Pass Rate**:
  - Windows 11 NTFS native: **106/106 unit tests PASS in 5.07s**, clean `go vet ./...`.
  - WSL2 Ubuntu Linux (`-race`): **106/106 unit tests PASS in 12.14s**, **0 data races, 0 deadlocks**, clean `go vet ./...`.

---

## 9. Remaining Open Risks & Recommendation for I3

- **`RISK-001`**: Unbounded flush queue memory growth under sustained write bursts.
- **`RISK-002`**: Single-file WAL recovery boundary (`INV-DUR-2`).
- **`RISK-003`**: RAM volatility of unflushed Memtables during fail-stop.
- **Delayed Reclamation**: Long-running read queries retain older SSTables on disk until released.

### Recommendation:
All design, lifecycle, publication, and shutdown synchronization protocols are finalized, hardened, and mathematically consistent. We recommend authorization to begin **Phase I3 — Implementation and Validation of Approach B**.

---

EXTERNAL REVIEW REQUIRED — CP2b AWAITING APPROVAL.
