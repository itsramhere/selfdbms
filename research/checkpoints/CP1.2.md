# CHECKPOINT CP1.2 — Protected Compaction Publication

**Commit:** `f4019e2`  
**Branch:** `research/reader-compactor-sync`  
**Date:** 2026-10-16  

---

### Completed Sub-phases

- **I1.2.0 — Verify CP0.75's Version-Lookup Invariants**:
  - Audited 7 invariant checks on `latestValueLocked`, `SSTableReader.LatestValue`, `EncodeInternalKey`, sparse-index boundary search (spanning 3 blocks / 11.5 KB), and `math.MaxUint64` 56-bit sequence space.
  - Identified defect [`E-004`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/logs/ISSUES.md#L109) where `latestValueLocked` returned on the first match and failed if SSTables were reordered.
  - Repaired via Option A ([`D-006`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/logs/DECISIONS.md)): iterating all `ActiveSSreaders` and selecting `maxSeq` while preserving RAM monotonic early-returns (`INV-MVCC-6`).
  - Audited `Manager.Get`: proved that point reads rely strictly on invariant `INV-ORDER-1` (ActiveSSreaders ordered oldest to newest).

- **I1.2.1 — Map the Compaction Publication Lifecycle**:
  - Mapped complete call graph of `CompactAll`, `flushToDisk`, and `ActiveSSreaders`.
  - Identified the publication race where `CompactAll`'s blind slice replacement `m.ActiveSSreaders = []*SSTableReader{newReader}` permanently erased any SSTables concurrently published by `flushToDisk` ([`research/design/compaction_publication_lifecycle.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/compaction_publication_lifecycle.md)).

- **I1.2.2 — Implement Identity-Preserving Publication**:
  - Implemented `replaceCompactedReaders` in [`lsmengine/compaction.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go):
    - Validates presence of all input readers under `m.Mu.Lock()`.
    - If validation fails, safely aborts, deletes partial file, and returns error.
    - Replaces only the compacted inputs in-place, preserving all concurrently flushed readers.
    - Preserves strict chronological search ordering `INV-ORDER-1`.

- **I1.2.3 — Add Deterministic Concurrency Tests**:
  - Added test hook `beforeCompactionPublishHook` on `Manager`.
  - Created 4 deterministic concurrency tests in [`lsmengine/compaction_concurrency_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction_concurrency_test.go):
    1. `TestCompactionPublication_ConcurrentFlushOverlap`: Verifies concurrent flush C is preserved when [A, B] -> M.
    2. `TestCompactionPublication_ConcurrentFlushNewerVersion`: Verifies newer version flushed during compaction is visible.
    3. `TestCompactionPublication_ConcurrentFlushTombstone`: Verifies tombstone flushed during compaction is visible.
    4. `TestCompactionPublication_OverlappingInputsValidation`: Verifies validation aborts when input reader was concurrently removed.

- **I1.2.4 — Verify Ownership and Error Handling**:
  - Audited reader ownership, error propagation, atomic swap, and Windows NTFS sharing violation avoidance ([`research/design/compaction_ownership_audit.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/compaction_ownership_audit.md)).

- **I1.2.5 — Validation Across Windows and WSL2 `-race`**:
  - `go vet ./...` clean (0 warnings).
  - All 20 unit tests PASS on Windows 11 native (`1.47s`).
  - All 20 unit tests PASS under WSL2 Linux amd64 `go test -race` (`2.44s`) with **zero data races**.

---

### Test Suite Pass Summary (20 / 20 Tests Passing)

| Test Function | Location | Purpose | Windows Status | WSL2 `-race` |
| :--- | :--- | :--- | :--- | :--- |
| `TestCompactionPublication_ConcurrentFlushOverlap` | `compaction_concurrency_test.go` | Concurrent flush preservation | **PASS (0.08s)** | **PASS (0.16s)** |
| `TestCompactionPublication_ConcurrentFlushNewerVersion` | `compaction_concurrency_test.go` | Newer version visibility | **PASS (0.05s)** | **PASS (0.10s)** |
| `TestCompactionPublication_ConcurrentFlushTombstone` | `compaction_concurrency_test.go` | Tombstone visibility | **PASS (0.09s)** | **PASS (0.11s)** |
| `TestCompactionPublication_OverlappingInputsValidation` | `compaction_concurrency_test.go` | Competing compaction abort | **PASS (0.03s)** | **PASS (0.05s)** |
| `TestCrossTierWriteConflict_FlushedSSTable` | `cross_tier_conflict_test.go` | Cross-tier Put conflict | **PASS (0.01s)** | **PASS (0.05s)** |
| `TestCrossTierWriteConflict_FlushedTombstone` | `cross_tier_conflict_test.go` | Cross-tier Tombstone conflict | **PASS (0.02s)** | **PASS (0.05s)** |
| `TestCrossTierWriteConflict_NonConflicting` | `cross_tier_conflict_test.go` | Non-conflicting isolation | **PASS (0.01s)** | **PASS (0.05s)** |
| `TestSkipListLevelDistribution` | `engine_test.go` | SkipList math | **PASS (0.00s)** | **PASS (0.00s)** |
| `TestBasicPutGet` | `engine_test.go` | Basic Put/Get | **PASS (0.01s)** | **PASS (0.06s)** |
| `TestDeleteAndTombstone` | `engine_test.go` | Tombstone visibility | **PASS (0.01s)** | **PASS (0.04s)** |
| `TestWALRecovery` | `engine_test.go` | WAL recovery & CRC32 | **PASS (0.09s)** | **PASS (0.33s)** |
| `TestWriteWriteConflict` | `engine_test.go` | Memtable write-write conflict | **PASS (0.01s)** | **PASS (0.03s)** |
| `TestI1_2_0_MaxUint64EncodingBehavior` | `i1_2_0_invariants_test.go` | 56-bit sequence math | **PASS (0.00s)** | **PASS (0.00s)** |
| `TestI1_2_0_MultipleSSTablesDifferentVersions` | `i1_2_0_invariants_test.go` | Multi-SSTable versions | **PASS (0.01s)** | **PASS (0.06s)** |
| `TestI1_2_0_TombstoneAndSubsequentPut` | `i1_2_0_invariants_test.go` | Tombstone + reinsertion | **PASS (0.01s)** | **PASS (0.08s)** |
| `TestI1_2_0_KeySpanningMultipleBlocks` | `i1_2_0_invariants_test.go` | 3-block sparse index boundary | **PASS (0.02s)** | **PASS (0.06s)** |
| `TestI1_2_0_MissingKeyVsIOError` | `i1_2_0_invariants_test.go` | NotFound vs IO error | **PASS (0.02s)** | **PASS (0.04s)** |
| `TestI1_2_0_SSTableOrderingAnomaly` | `i1_2_0_invariants_test.go` | E-004 fix: inverted SSTables | **PASS (0.01s)** | **PASS (0.06s)** |
| `TestE004_TxnConflict_NewestVersionNotInLastSSTable` | `i1_2_0_invariants_test.go` | E-004 txn conflict abort | **PASS (0.02s)** | **PASS (0.07s)** |
| `TestManagerGet_OrderingAudit` | `i1_2_0_invariants_test.go` | Manager.Get ordering audit | **PASS (0.01s)** | **PASS (0.06s)** |

---

### Remaining Architectural Risks (Prior to Sub-phase I1.3)
1. **Reader Retirement Race Under Long Disk I/O (Sub-phase I1.3)**:
   In Approach A, `Manager.Get` holds `m.Mu.RLock()`. Compaction closes old readers (`r.Close()`) after releasing `m.Mu.Lock()`. If Approach A readers ever release locks early or if an iterator outlives the query, closing files can race. This will be formally addressed in Sub-phase I1.3 (safe retirement / ref-counting).
2. **Crash-Safe File Deletion on Windows NTFS (Sub-phase I1.3 / I3.4)**:
   `os.Remove` on Windows requires that all file descriptors are closed. Handled in `CompactAll` by closing all iterators before closing and unlinking readers.

---
**STATUS: STOPPED AT CHECKPOINT CP1.2 AWAITING HUMAN AND CHATGPT REVIEW.**
