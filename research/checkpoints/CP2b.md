# Checkpoint CP2b — Final Protocol Consistency & Architectural Closure

## 1. Executive Summary & Milestone Certification

Checkpoint CP2b delivers the final architectural closure and protocol consistency resolutions for **Approach B: Immutable SSTable Reader-Set Snapshots with Two-Tier Reference Ownership**, resolving all remaining synchronization items identified in the CP2a external review (Sub-phase I2.7).

### Summary of Completed Consistency Resolutions:
1. **Reconciled `EngineClosing` Flush Publication (I2.7.1, Decision D-025, `INV-SNAP-7`)**:
   - Resolved the contradiction with Approach A's 4-phase shutdown drain (`INV-LIFETIME-5`).
   - Admitted flush tasks carrying valid generation tokens enqueued before shutdown are permitted to complete disk builds, publish new snapshots, and remove corresponding frozen Memtables during `EngineClosing`.
   - `publishFlushLocked` rejects publications strictly when `m.state == EngineClosed`.
2. **Unified Cleanup Eligibility Helper (I2.7.2)**:
   - Designed a single lock-protected helper `checkCleanupEligibilityLocked(sr)` under `m.snapshotMu`.
   - Dispatched across all reference drops, obsolescence markers, construction rollbacks, and shutdown teardowns.
   - Prevents duplicate cleanup scheduling and reference resurrection.
   - Clarified that surviving valid SSTables have `refCount == 0 && obsolete == false` during shutdown, transitioning safely to `SharedReaderClosedValid` (closed but kept on disk).
3. **Reconciled Close vs Unlink Retries (I2.7.3)**:
   - Harmonized `sr.closeOnce sync.Once` with retry semantics: physical handle `Close()` is executed exactly once; if it fails, it is an unrecoverable terminal error.
   - File unlinking (`os.Remove`) occurs only after handle closure succeeds, and is retried independently (up to 3 attempts, 10ms backoff) to handle transient Windows sharing violations without unbounded goroutines.
4. **Harmonized Scaffolding and Authoritative Contract (I2.7.4)**:
   - Formally documented `Manager.snapshotMu` as the sole authoritative guard for all compound lifecycle transitions.
   - Retained atomic fields in `lsmengine/snapshot.go` strictly for lock-free read-only observability (diagnostics, testing, metrics).
   - Documented that accessors like `SetState` are internal helpers requiring `m.snapshotMu` in production.
5. **Exact Shutdown Ordering Pseudocode (I2.7.5)**:
   - Formally specified 8-step shutdown sequence.
   - Proved `Close()` drops all locks while waiting on `m.snapshotDrainCond.Wait()`, ensuring zero circular wait with `ReleaseSnapshot` or background flushes.
6. **Corrected and Extended Phase I3 Test Matrix (I2.7.6)**:
   - Added `TestApproachB_Shutdown_AdmittedFlushCompletesDuringEngineClosing`.
   - Included both cleanup eligibility orderings (`CaseA` and `CaseB`).
   - Total test suite: 16 deterministic concurrency test specifications.
7. **Verification & Provenance**:
   - 106/106 unit tests passing across Windows 11 NTFS native (5.07s) and WSL2 Linux with `-race` (12.14s, 0 data races, 0 deadlocks).

---

## 2. Invariant Inventory at Checkpoint CP2b

| Invariant ID | Domain | Statement Summary | Status at CP2b |
| :--- | :--- | :--- | :--- |
| `INV-ORDER-1` | Ordering | SSTable readers queried in reverse chronological order (newest to oldest). | **VERIFIED** |
| `INV-COMPACT-1` | Compaction | Compaction inputs must span a contiguous prefix starting at index 0. | **VERIFIED** |
| `INV-SERIAL-COMPACT-1`| Compaction | Only one background compaction may execute at any given time (`compactionMu`). | **VERIFIED** |
| `INV-FLUSH-FIFO-1` | Flush | Memtables enqueued and flushed strictly in chronological freeze order. | **VERIFIED** |
| `INV-FLUSH-FAIL-1` | Flush | Fail-stop flush processing: fatal error halts flush worker, preserving `Frozenmem` precedence. | **VERIFIED** |
| `INV-MUTABLE-1` | Memory Tier | Frozen Memtables are permanently sealed against mutations. | **VERIFIED** |
| `INV-HANDOFF-1` | Memory-to-Disk | Continuous key version visibility during memory-to-disk handoff. | **VERIFIED** |
| `INV-QUEUE-1` | Queue | Flush enqueueing is an $O(1)$ non-blocking slice append under `flushQueueMu`. | **VERIFIED** |
| `INV-LIFETIME-5` | Shutdown | 4-phase deadlock-free shutdown drain protocol (`Open -> Closing -> Closed`). | **VERIFIED** |
| `INV-APPROACH-A` | Mode Seam | Typed `SyncMode` configuration; defaults to Approach A; rejects Approach B until Phase I3. | **VERIFIED** |
| `INV-SNAP-1` | Approach B | Published reader snapshots are strictly immutable. | **FORMALIZED** |
| `INV-SNAP-2` | Approach B | Pre-release snapshot pinning: pin must be held before releasing metadata lock. | **FORMALIZED** |
| `INV-SNAP-3` | Approach B | Two-tier reader lifetime: physical file closed/unlinked only when `refCount == 0` & obsolete. | **FORMALIZED** |
| `INV-SNAP-4` | Approach B | Monotonic pin underflow protection: releasing with `pins <= 0` triggers fatal panic. | **FORMALIZED** |
| `INV-SNAP-5` | Approach B | Memory tier read coupled with disk snapshot pin under short `Manager.Mu.RLock()`. | **FORMALIZED** |
| `INV-SNAP-6` | Approach B | Obsolete marking post-commit only; two-phase cleanup; shutdown preserves valid files. | **FORMALIZED** |
| `INV-SNAP-7` | Approach B | Admitted flushes permitted to publish during `EngineClosing`; rejected only in `EngineClosed`. | **FORMALIZED (I2.7)** |

Total Invariants: **30 invariants** (23 Approach A operational, 7 Approach B formalized).

---

## 3. Decision Records Summary

- **`D-025`**: Admitted Flush Publication During EngineClosing (`INV-SNAP-7`). Permitted in `EngineOpen` and `EngineClosing`; rejected strictly in `EngineClosed`.

---

## 4. Test Suite Inventory

- **Baseline Test Suite (Approach A)**: 101 unit tests + 6 benchmarks = 107 runnable tests.
- **Scaffolding Tests (Approach B)**: 5 unit tests:
  - `TestSnapshotState_String`
  - `TestSharedReaderState_String`
  - `TestSharedReader_RefAndCount`
  - `TestReaderSnapshot_AtomicAccessors`
  - `TestApproachB_ScaffoldingEnforcesUnsupportedMode`
- **Total Test Inventory at CP2b**: **106 unit tests + 6 benchmarks = 112 runnable tests**.
- **Pass Rate**: **100% PASS** on Windows 11 NTFS native (`5.07s`) and WSL2 Ubuntu Linux `-race` (`12.14s`, 0 data races, 0 deadlocks).
