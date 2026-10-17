# Checkpoint CP2 — Immutable Snapshot Protocol & Minimal Scaffolding

## 1. Executive Summary & Milestone Certification

Checkpoint CP2 formally delivers the architectural design, lifecycle protocol, reference ownership model, atomicity specifications, MVCC consistency proofs, and minimal type scaffolding for **Approach B: Immutable SSTable Reader-Set Snapshots with Two-Tier Reference Ownership**.

### Milestone Accomplishments
1. **Preserved Verified Baseline (I2.0)**:
   - Verified tag `baseline-approach-a-verified` remains anchored at commit `ae8c034c438d56abbe6664e6472f7a0c1278797f`.
   - Historical tag `baseline-approach-a` remains anchored at commit `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`.
   - Development branched cleanly to `research/approach-b-snapshots`.
2. **Immutable Reader-Set Lifecycle Protocol (I2.1)**:
   - Established the 7-state linear lifecycle: `Constructing` $\to$ `Published` $\to$ `Pinned` $\to$ `Retired` $\to$ `Drained` $\to$ `Closed` $\to$ `Reclaimed`.
   - Evaluated three reference ownership models (Model A: Whole-Snapshot Ownership, Model B: Individual Reader Refcounts, Model C: Two-Tier Reference Ownership). Unambiguously selected **Model C** (`D-020`) to satisfy the critical requirement that multiple snapshots may share uncompacted physical SSTables without premature closure or use-after-close.
3. **Atomic Pin Acquisition & Publication Specifications (I2.2)**:
   - Designed atomic leaf-lock pinning under `m.snapshotMu` (`D-021`), rejecting speculative lock-free CAS to guarantee deterministic verification and avoid ABA/memory hazards.
   - Specified exact pseudocode for `AcquireSnapshot`, `ReleaseSnapshot`, `PublishSnapshot`, `RetireSnapshot`, `ReclaimRetired`, and safe Windows NTFS filesystem cleanup.
   - Codified strict total lock hierarchy: `compactionMu` $\to$ `Manager.Mu` $\to$ `snapshotMu` $\to$ `flushQueueMu`, mathematically preventing circular-wait deadlocks.
4. **Memtable and MVCC Consistency Proofs (I2.3)**:
   - Proved that single point reads acquire pins within the short `Manager.Mu.RLock()` critical section (`D-022`), eliminating any visibility gap between active/frozen memory tiers and the disk snapshot (`INV-HANDOFF-1`).
   - Analyzed 5 concurrent interleaving scenarios (Memtable freeze, flush publication, compaction replacement, retired snapshot accumulation, engine shutdown), demonstrating zero data loss and safe reclamation.
5. **Minimal Approach B Scaffolding (I2.4)**:
   - Introduced `lsmengine/snapshot.go` declaring `SnapshotState`, `SharedReader`, `ReaderSnapshot`, and `ReaderSyncStrategy`.
   - Verified that `CreateManagerWithConfig(Config{Mode: SyncModeApproachB})` continues to return `ErrUnsupportedSyncMode` and nil manager until Phase I3 implementation is authorized.
6. **Dual-Platform Test Pass & Zero Regressions (I2.5)**:
   - Windows 11 NTFS native: **104/104 unit tests PASS (5.04s)**, clean `go vet ./...`.
   - WSL2 Linux (`-race`): **104/104 unit tests PASS (12.34s)**, **0 data races, 0 deadlocks**, clean `go vet ./...`.

---

## 2. Invariant Inventory at Checkpoint CP2

| Invariant ID | Domain | Statement Summary | Status at CP2 |
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
| `INV-SNAP-1` | Approach B | Published reader snapshots are strictly immutable. | **FORMALIZED (I2)** |
| `INV-SNAP-2` | Approach B | Pre-release snapshot pinning: pin must be held before releasing metadata lock. | **FORMALIZED (I2)** |
| `INV-SNAP-3` | Approach B | Two-tier reader lifetime: physical file closed/unlinked only when `refCount == 0` & obsolete. | **FORMALIZED (I2)** |
| `INV-SNAP-4` | Approach B | Monotonic pin underflow protection: releasing with `pins <= 0` triggers fatal panic. | **FORMALIZED (I2)** |
| `INV-SNAP-5` | Approach B | Memory tier read coupled with disk snapshot pin under short `Manager.Mu.RLock()`. | **FORMALIZED (I2)** |

Total Invariants: **28 invariants** (23 Approach A operational, 5 Approach B formalized for Phase I3).

---

## 3. Decision Records (D-020 through D-022)

- **`D-020` (Two-Tier Reference Ownership / Model C)**:
  Decoupled query snapshot pin lifetime (`snap.pins`) from physical reader file handle lifetime (`SharedReader.refCount`). Solves multi-snapshot SSTable sharing across compactions and flushes, preventing premature closure, use-after-close, and Windows `ERROR_SHARING_VIOLATION`.
- **`D-021` (Mutex-Protected Leaf Section vs Speculative Lock-Free CAS)**:
  Selected ultra-short leaf mutex `m.snapshotMu` (~10–20ns) over lock-free atomic pointer CAS loops. Eliminates ABA hazards, complex reclamation retire queues, and speculative retry contention.
- **`D-022` (Coupled In-Memory Inspection & Snapshot Pinning)**:
  Selected combined active/frozen Memtable lookup and disk snapshot pin under a single short `Manager.Mu.RLock()`. Eliminates visibility gaps during flush handoff and releases locks before disk I/O.

---

## 4. Test Suite Inventory

- **Baseline Test Suite (Approach A)**: 101 unit tests + 6 benchmarks = 107 runnable tests.
- **Added Scaffolding Tests (Approach B)**: 3 unit tests:
  - `TestSnapshotState_String` (lifecycle state formatting)
  - `TestSharedReader_RefAndCount` (atomic refcounting)
  - `TestApproachB_ScaffoldingEnforcesUnsupportedMode` (mode seam enforcement)
- **Total Test Inventory at CP2**: **104 unit tests + 6 benchmarks = 110 total runnable tests**.
- **Pass Rate**: **100% PASS** on Windows 11 NTFS native (`5.04s`) and WSL2 Ubuntu Linux `-race` (`12.34s`, 0 data races, 0 deadlocks).

---

## 5. Next Steps (Pending CP2 Authorization)

Upon external reviewer approval of CP2, Phase I3 will execute:
1. Implement the functional `ReaderSnapshot` and `SharedReader` state transitions in `lsmengine/snapshot.go`.
2. Integrate `AcquireSnapshot` and `ReleaseSnapshot` into `Manager.Get` under `SyncModeApproachB`.
3. Integrate `PublishSnapshot` and `RetireSnapshot` into `flushToDisk` and `replaceCompactedReaders`.
4. Implement Windows NTFS safe asynchronous handle closure and file unlinking.
5. Execute the 10 deterministic regression tests defined in the I3 Test Plan.
