# Checkpoint CP3: Implementation and Validation of Approach B Reader Coordination

**Date**: 2026-10-18  
**Phase**: I3 — Implement Snapshot-Based Reader Coordination  
**Status**: COMPLETE — Awaiting External Reviewer Approval for Phase I4  
**Git Branch**: `research/approach-b-snapshots`  
**Verified Baseline Tags**:
- `baseline-approach-a`: `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`
- `baseline-approach-a-verified`: `ae8c034c438d56abbe6664e6472f7a0c1278797f`

---

## 1. Executive Summary

Phase I3 successfully implements **Approach B (Immutable Reader-Set Snapshots)** for concurrent reader-compactor coordination in accordance with `01_IMPLEMENTATION_PLAN.md` and the design approved in Checkpoint CP2b-V. 

Under Approach B:
1. **Readers execute lock-free disk I/O**: Point queries inspect memory tiers under a brief `Manager.Mu.RLock()`, atomically acquire an active pin on the immutable `ReaderSnapshot` under `m.snapshotMu`, release `Manager.Mu`, and perform all SSTable binary searches and block reads with **zero engine locks held**.
2. **Two-tier reference ownership**: Logical query pinning (`ReaderSnapshot.pins`) is decoupled from physical SSTable handle lifetime (`SharedReader.refCount`), allowing multiple snapshots across generations to safely share unchanged SSTables.
3. **Atomic memory-to-disk publication**: Flushes atomically prepend the newly built SSTable to a new snapshot and remove the corresponding `Memtable` from `Frozenmem` under `Manager.Mu.Lock()`, guaranteeing continuous key visibility (`INV-HANDOFF-1`).
4. **Four-phase safe physical reclamation**: Obsolete compaction inputs (Category A), retired snapshot tables (Category B), shutdown surviving valid tables (Category C), and shutdown in-flight pinned queries (Category D) are reclaimed safely. File descriptors are closed before file unlinking on Windows NTFS.
5. **Acyclic lock hierarchy**: Formal 6-level lock order (`compactionMu` $\to$ `Manager.Mu` $\to$ `snapshotMu` $\to$ `flushQueueMu` $\to$ `Wal.mu`, leaf `cleanupMu`) prevents deadlocks across all concurrent paths.

Dual-platform verification confirms **132/132 unit tests (138 runnable tests)** pass with **zero data races and zero deadlocks** across Windows 11 NTFS native (`go1.26.3`) and WSL2 Ubuntu Linux (`go1.27.2` with `-race`).

---

## 2. Baseline Tag Verification and Lineage

Both historical Approach A baseline tags were verified completely unmodified:
- `git rev-parse 'baseline-approach-a^{commit}'`: `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`
- `git rev-parse 'baseline-approach-a-verified^{commit}'`: `ae8c034c438d56abbe6664e6472f7a0c1278797f`

Lineage check confirms `HEAD` directly descends from `baseline-approach-a-verified`:
`git merge-base --is-ancestor ae8c034c438d56abbe6664e6472f7a0c1278797f HEAD` $\to$ **ANCESTOR OK**.

---

## 3. Sub-Phase Implementation Deliverables

### I3.1 — Snapshot Construction and Immutability
- Implemented `NewReaderSnapshot`, `NewSharedReader`, `rollbackUnpublishedReader`, and accessor methods in `lsmengine/snapshot.go`.
- Delivered `lsmengine/snapshot_construction_test.go` with 7 deterministic tests:
  1. `TestSnapshotConstruction_PrivateUntilPublished` (PASS)
  2. `TestSnapshotConstruction_OrderPreservation` (PASS)
  3. `TestSnapshotConstruction_ReaderImmutability` (PASS)
  4. `TestSnapshotConstruction_SharedUnchangedSSTables` (PASS)
  5. `TestSnapshotConstruction_BuilderOwnershipAndRollback` (PASS)
  6. `TestSnapshotConstruction_EmptySnapshot` (PASS)
  7. `TestSnapshotConstruction_ResurrectionProhibition` (PASS, 6 sub-tests)

### I3.2 — Reader-Compactor Synchronization (AcquireSnapshot / ReleaseSnapshot)
- Implemented `acquireSnapshotLocked`, `AcquireSnapshot`, `ReleaseSnapshot`, `retireSnapshotLocked`, `reclaimSnapshotLocked` in `lsmengine/snapshot.go`.
- Delivered `lsmengine/snapshot_pinning_test.go` with 7 deterministic tests:
  1. `TestSnapshotPinning_PinBeforeLockRelease` (PASS)
  2. `TestSnapshotPinning_ConcurrentPinUnpinAtomicity` (PASS, 2000 pin/unpin cycles under race detector)
  3. `TestSnapshotPinning_ShutdownRejection` (PASS)
  4. `TestSnapshotPinning_LockFreeDiskReads` (PASS, proven exclusive `Manager.Mu` acquisition while reader reads disk)
  5. `TestSnapshotPinning_UnderflowPanic` (PASS, panic-safe deferred mutex release)
  6. `TestSnapshotPinning_RetiredSnapshotPinRetention` (PASS)
  7. `TestSnapshotPinning_FinalPinReclamation` (PASS)

### I3.3 — Atomic Publication and Reader-Set Swap
- Implemented `publishFlushLockedApproachB` in `lsmengine/snapshot.go` and `compactAllApproachB` in `lsmengine/compaction.go`.
- Delivered `lsmengine/snapshot_publication_test.go` with 5 deterministic tests:
  1. `TestSnapshotPublication_FlushAtomicSwap` (PASS)
  2. `TestSnapshotPublication_ReadsCrossingSwap` (PASS)
  3. `TestSnapshotPublication_CompactionAtomicSwap` (PASS)
  4. `TestSnapshotPublication_ObsoleteMarking` (PASS)
  5. `TestSnapshotPublication_FailStopPreservation` (PASS)

### I3.4 — Lifetime Tracking and Safe Reclamation
- Implemented `drainPendingCleanups`, `drainSingleReaderCleanup`, `evaluateShutdownReaderCleanupLocked`, `closeApproachB` in `lsmengine/snapshot.go`.
- Delivered `lsmengine/snapshot_shutdown_test.go` with 6 deterministic tests:
  1. `TestSnapshotShutdown_CategoryA_CompactionObsoleteReclamation` (PASS)
  2. `TestSnapshotShutdown_CategoryB_RetiredSnapshotDrainedReclamation` (PASS)
  3. `TestSnapshotShutdown_CategoryC_SurvivingTablesPreservedOnDisk` (PASS)
  4. `TestSnapshotShutdown_CategoryD_ShutdownWaitsForPinnedReaders` (PASS)
  5. `TestSnapshotShutdown_WindowsNTFSSafety` (PASS)
  6. `TestSnapshotShutdown_ConcurrentClose` (PASS, 10 concurrent goroutines)

### I3.5 — Validate Approach A vs Approach B Equivalence
- Implemented `getApproachB` and synchronized `latestValueLocked` snapshot pinning (Decision D-030, Invariant INV-SNAP-10).
- Delivered `lsmengine/approach_b_mvcc_test.go` with 5 equivalence tests:
  1. `TestApproachEquivalence_SingleKeyCRUD` (PASS)
  2. `TestApproachEquivalence_MultiVersionVisibility` (PASS)
  3. `TestApproachEquivalence_TombstoneShadowingAcrossTiers` (PASS)
  4. `TestApproachEquivalence_CompactionQueryParity` (PASS)
  5. `TestApproachEquivalence_ConcurrentWorkload` (PASS, 25 goroutines running concurrent writers, readers, flushes, compactions)

---

## 4. Verification Summary

| Platform | Command | Total Tests | Pass Count | Races | Deadlocks | Duration |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Windows 11 NTFS native** (`go1.26.3`) | `go test -count=1 ./...` | 132 unit | 132 | 0 | 0 | 7.97s |
| **WSL2 Ubuntu Linux** (`go1.27.2`) | `go test -race -count=1 ./...` | 132 unit | 132 | 0 | 0 | 16.38s |
| **Windows 11 stress** | `go test -count=5 (I3 suites)` | 25 suites x 5 | 125 | 0 | 0 | 11.53s |
| **WSL2 Linux stress** | `go test -race -count=5 (I3 suites)` | 25 suites x 5 | 125 | 0 | 0 | 25.06s |
| **Dual-platform Vet** | `go vet ./...` | All packages | Clean | N/A | N/A | Clean exit 0 |

---

## 5. Scope Boundary Compliance

In strict compliance with the reviewer's instructions:
- **NO performance benchmarking** (I4/I5) has been conducted.
- The default synchronization mode remains `SyncModeApproachA`.
- All historical baseline tags are preserved without alteration.
- Execution halts at Checkpoint CP3 pending external reviewer authorization.
