# Checkpoint CP3a: Correctness Verification, Error Hardening, and Lifetime Safety

**Date**: 2026-10-18  
**Phase**: I3.7 — CP3 Correctness Verification and Minimal Repair  
**Status**: COMPLETE — Awaiting External Reviewer Approval for Phase I4  
**Git Branch**: `research/approach-b-snapshots`  
**Tested Commit**: `4c93ca0a4ec6ac3852f3d070f435272bcf0e55ad`  
**Verified Baseline Tags**:
- `baseline-approach-a`: `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`
- `baseline-approach-a-verified`: `ae8c034c438d56abbe6664e6472f7a0c1278797f`

---

## 1. Executive Summary

Sub-phase **I3.7 (CP3 Correctness Verification and Minimal Repair)** resolves all correctness, error-handling, lifetime-safety, and parity concerns raised by the external reviewer following Checkpoint CP3.

Key hardened mechanisms:
1. **Strict Error Classification (E-012, Decision D-031)**: Eliminated silent error suppression in `getApproachB` and `Manager.Get`. Only `ErrNotFound` continues traversal across SSTables. Unexpected disk or block I/O errors immediately abort traversal and bubble up to the caller with exact error fidelity. Nil readers in snapshots return a fatal corrupted-snapshot error rather than falling back to stale data.
2. **Snapshot Integrity in Transaction Commit Validation**: `Manager.latestValueLocked` now strictly propagates snapshot acquisition and reader errors. If snapshot acquisition fails or encounters a nil reader during write-conflict validation, `Transaction.Commit` immediately aborts with an error rather than silently bypassing the disk tier.
3. **Consistent EngineClosing Read Admission (Decision D-032)**: Both memory and snapshot read entry points strictly reject reads when `m.state != EngineOpen`. In-flight reads that acquired their snapshot pin prior to shutdown continue lock-free to completion.
4. **Full A/B Semantic Parity**: Verified 11-point equivalence matrix between Approach A and Approach B across active/frozen/SSTable hits, missing keys, active/frozen/SSTable tombstones, historical MVCC timestamps, unexpected I/O errors, closed engines, and closing engine admissions.
5. **SharedReader Ref() Resurrection Proof**: Proved under invariant `INV-REF-1` that state checks and increments executed under `m.snapshotMu` guarantee zero resurrection from terminal states (`Closing`, `Closed`, `Unlinking`, `ClosedValid`, `Reclaimed`, `Failed`).
6. **Compaction Lease Safety**: Proved that compaction reader leases cannot resurrect closing or obsolete SSTables.
7. **Background Cleanup Synchronization (Decision D-033)**: Background cleanups are tracked with `m.cleanupWg`. `Manager.Close()` waits for all async cleanups to complete, ensuring file descriptor handles close prior to unlinking and propagating async errors to concurrent callers.

Dual-platform test execution confirms **163/163 unit tests (169 runnable tests)** pass with **zero data races and zero deadlocks** across Windows 11 NTFS native (`go1.26.3`) and WSL2 Ubuntu Linux (`go1.27.2` with `-race`).

---

## 2. Baseline Tag Verification and Lineage

Both historical Approach A baseline tags remain completely unmodified:
- `git rev-parse 'baseline-approach-a^{commit}'`: `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`
- `git rev-parse 'baseline-approach-a-verified^{commit}'`: `ae8c034c438d56abbe6664e6472f7a0c1278797f`

Lineage verification confirms tested commit `4c93ca0a4ec6ac3852f3d070f435272bcf0e55ad` descends from `baseline-approach-a-verified`:
`git merge-base --is-ancestor ae8c034c438d56abbe6664e6472f7a0c1278797f HEAD` $\to$ **ANCESTOR OK**.

---

## 3. Sub-Phase Implementation Deliverables

### I3.7.1 — Audit, Reproduce, and Repair Swallowed SSTable Read Errors
- Resolved Defect E-012 under Decision D-031 in `lsmengine/snapshot.go` and `lsmengine/manager.go`.
- Authored `lsmengine/sstable_read_error_test.go` with 3 deterministic tests:
  1. `TestApproachB_UnexpectedReadError_PropagatesError` (PASS)
  2. `TestApproachB_UnexpectedReadError_SingleSSTable` (PASS)
  3. `TestApproachB_NilReaderInSnapshot_DoesNotFallBack` (PASS)
- Captured complete repro artifacts in `research/debug/E-012/` (`before.log`, `after.log`, `diff.patch`, `repro.md`, `wsl_before.log`, `wsl_after.log`).

### I3.7.2 — Audit Snapshot Acquisition in Transaction Conflict Validation
- Hardened `latestValueLocked` in `lsmengine/manager.go` and `Transaction.Commit` in `lsmengine/transaction.go`.
- Authored `lsmengine/snapshot_conflict_test.go` with 3 deterministic tests:
  1. `TestApproachB_ConflictValidation_SnapshotAcquisitionFailureAbortsCommit` (PASS)
  2. `TestApproachB_ConflictValidation_CorruptedNilReaderAbortsCommit` (PASS)
  3. `TestApproachB_ConflictValidation_SSTableIOErrorAbortsCommit` (PASS)

### I3.7.3 — Audit Read Admission During EngineClosing
- Enforced unified read admission under Decision D-032 in `Manager.Get`, `getApproachB`, and `acquireSnapshotLocked`.
- Authored `lsmengine/snapshot_admission_test.go` with 2 deterministic tests:
  1. `TestApproachB_ReadAdmission_EngineClosing_ConsistentTierRejection` (PASS)
  2. `TestApproachA_ReadAdmission_EngineClosing` (PASS)

### I3.7.4 — Verify Full A/B Get API Parity
- Verified 11-point parity matrix across all retrieval paths.
- Authored `lsmengine/approach_b_parity_test.go` with 11 test functions (22 cross-mode subtests):
  1. `TestParity_ExistingKey_ActiveMemtable` (PASS)
  2. `TestParity_ExistingKey_FrozenMemtable` (PASS)
  3. `TestParity_ExistingKey_SSTable` (PASS)
  4. `TestParity_MissingKey` (PASS)
  5. `TestParity_ActiveMemtable_Tombstone` (PASS)
  6. `TestParity_FrozenMemtable_Tombstone` (PASS)
  7. `TestParity_SSTable_Tombstone` (PASS)
  8. `TestParity_HistoricalTimestamp` (PASS)
  9. `TestParity_UnexpectedSSTableReadError` (PASS)
  10. `TestParity_ClosedEngine` (PASS)
  11. `TestParity_EngineClosingAdmission` (PASS)

### I3.7.5 — Re-evaluate SharedReader Ref() and Compaction Resurrection
- Verified synchronization rules preventing `Ref()` resurrection on closing/reclaimed readers and compaction reader replacement.
- Authored `lsmengine/shared_reader_resurrection_test.go` with 3 deterministic tests:
  1. `TestSharedReader_ProhibitResurrection_OnClosingOrReclaimed` (PASS, 6 subtests)
  2. `TestSharedReader_RaceAcquireWithFinalRelease_SynchronizedBySnapshotMu` (PASS)
  3. `TestCompaction_CannotResurrectClosingSSTables` (PASS)

### I3.7.6 — Verify Background Cleanup Completion and Shutdown Error Propagation
- Hardened cleanup coordination under Decision D-033 using `m.cleanupWg` and `m.asyncCleanupErr` in `lsmengine/manager.go`, `lsmengine/snapshot.go`, and `lsmengine/compaction.go`.
- Authored `lsmengine/cleanup_coordination_test.go` with 4 deterministic tests:
  1. `TestCleanup_ObsoleteCleanedAndValidPreserved` (PASS)
  2. `TestCleanup_CloseWaitsForAsyncCleanup_DeterministicBarrier` (PASS)
  3. `TestCleanup_AsyncCleanupErrorPropagatedToConcurrentCloseCallers` (PASS)
  4. `TestCleanup_HandleClosurePrecedesUnlink_WindowsNTFS` (PASS)

### I3.7.7 — Reconcile Test Inventory, Invariants Ledger, and Claims
- Updated `research/debug/TEST_MATRIX.md`, `research/design/invariants.md`, `research/design/architecture.md`, `research/logs/DECISIONS.md`, `research/logs/ISSUES.md`, and `research/logs/WORKLOG.md`.
- Test count reconciled: 107 pre-I3 + 30 I3 + 26 I3.7 = **163 unit tests** (169 runnable tests).
- Invariants reconciled: 33 total defined, 32 verified, 1 visibly unproven (`INV-DUR-2` durability across WAL rotation).
- Compaction locking documented accurately: `m.compactionMu` is held throughout compaction, while `Manager.Mu` and `m.snapshotMu` are briefly released during merge.

---

## 4. Dual-Platform Verification Summary

| Platform | Command | Total Tests | Pass Count | Races | Deadlocks | Duration |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Windows 11 NTFS native** (`go1.26.3`) | `go test -count=1 ./...` | 163 unit | 163 | 0 | 0 | 10.58s |
| **WSL2 Ubuntu Linux** (`go1.27.2`) | `go test -race -count=1 ./...` | 163 unit | 163 | 0 | 0 | 19.69s |
| **Windows 11 stress** | `go test -count=5 (I3.7 suites)` | 26 suites x 5 | 130 | 0 | 0 | 4.82s |
| **WSL2 Linux stress** | `go test -race -count=5 (I3.7 suites)` | 26 suites x 5 | 130 | 0 | 0 | 3.44s |

---

## 5. Decision Log Additions

- **D-031**: Strict SSTable Read Error Handling in Point Queries.
- **D-032**: Unified EngineClosing Read Admission between RAM and Disk.
- **D-033**: Asynchronous Cleanup WaitGroup and Shutdown Error Retention.

---

## 6. Closure Status

Checkpoint CP3a is complete and ready for external review. All 10 dossier sections are submitted in `research/checkpoints/CP3a_CHATGPT_REVIEW_PACKET.md`.
