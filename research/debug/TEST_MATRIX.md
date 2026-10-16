# Baseline Test Inventory & Coverage Matrix

**Phase:** I0.4 / D0.2 — Baseline Test Reconnaissance  
**Target Commit:** `08d3189069294e8168ebb5234a3bef1d37f1d649`  

---

## 1. Test Categorization & Coverage Gaps

| Category | Test Function | Source File | Status in Baseline (I0.4) | Status in I1.1a | Compaction Active? | Notes |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **SkipList / In-Memory** | `TestSkipListLevelDistribution` | `lsmengine/engine_test.go:9` | **PASS (0.00s)** | **PASS (0.00s)** | No | Tests SkipList geometric level distribution. Does not touch manager or SSTable. |
| **Point Read / Write** | `TestBasicPutGet` | `lsmengine/engine_test.go:37` | **FAIL (Deadlock)** | **PASS (0.05s)** | No | Unblocked by `latestValueLocked` fix for `E-001`. |
| **MVCC / Tombstone** | `TestDeleteAndTombstone` | `lsmengine/engine_test.go:66` | **FAIL (Deadlock)** | **PASS (0.01s)** | No | Unblocked by `latestValueLocked` fix for `E-001`. |
| **WAL / Crash Recovery** | `TestWALRecovery` | `lsmengine/engine_test.go:98` | **FAIL (Deadlock)** | **PASS (0.09s)** | No | Unblocked by `latestValueLocked` fix for `E-001`. |
| **Conflict: In-Memory** | `TestWriteWriteConflict` | `lsmengine/engine_test.go:131` | *N/A* (Added I1.1a) | **PASS (0.00s)** | **PASS (0.00s)** | No | Regression test proving `latestValueLocked` aborts conflicting writes in memory. |
| **Conflict: Flushed SSTable** | `TestCrossTierWriteConflict_FlushedSSTable` | `lsmengine/cross_tier_conflict_test.go:27` | *N/A* (Added I1.1b) | *N/A* | **PASS (0.02s)** | No | Proves conflict detection succeeds against SSTable-resident committed versions (E-003). |
| **Conflict: Flushed Tombstone** | `TestCrossTierWriteConflict_FlushedTombstone` | `lsmengine/cross_tier_conflict_test.go:76` | *N/A* (Added I1.1b) | *N/A* | **PASS (0.01s)** | No | Proves conflict detection succeeds against SSTable-resident tombstones (E-003). |
| **Conflict: SSTable Ordering** | `TestI1_2_0_SSTableOrderingAnomaly` | `lsmengine/i1_2_0_invariants_test.go:312` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.03s)** | No | Verifies conflict detection finds greatest seq (50) even when SSTables are inverted (E-004 Option A). |
| **Conflict: Txn Reorder Abort** | `TestE004_TxnConflict_NewestVersionNotInLastSSTable` | `lsmengine/i1_2_0_invariants_test.go:346` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.01s)** | No | Verifies transaction aborts on conflict against SSTable that was not the last reader in slice. |
| **Invariants: math.MaxUint64** | `TestI1_2_0_MaxUint64EncodingBehavior` | `lsmengine/i1_2_0_invariants_test.go:53` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.00s)** | No | Verifies 56-bit sequence space and mathematical minimal trailer property. |
| **Invariants: Multi-Version** | `TestI1_2_0_MultipleSSTablesDifferentVersions` | `lsmengine/i1_2_0_invariants_test.go:112` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.02s)** | No | Verifies multi-version resolution across multiple distinct SSTables. |
| **Invariants: Tombstone+Put** | `TestI1_2_0_TombstoneAndSubsequentPut` | `lsmengine/i1_2_0_invariants_test.go:153` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.03s)** | No | Verifies tombstone sequence followed by reinserted put version sequence. |
| **Invariants: Multi-Block Index** | `TestI1_2_0_KeySpanningMultipleBlocks` | `lsmengine/i1_2_0_invariants_test.go:205` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.02s)** | No | Verifies sparse-index boundary search across 3 4KB blocks (11.5 KB payload). |
| **Invariants: IO vs NotFound** | `TestI1_2_0_MissingKeyVsIOError` | `lsmengine/i1_2_0_invariants_test.go:262` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.01s)** | No | Verifies ErrNotFound is properly distinguished from physical OS I/O read errors. |
| **Audit: Manager.Get Ordering** | `TestManagerGet_OrderingAudit` | `lsmengine/i1_2_0_invariants_test.go:426` | *N/A* (Added I1.2.0) | *N/A* | **PASS (0.03s)** | No | Audits Manager.Get; proves reliance on invariant INV-ORDER-1 (oldest to newest). |
| **Flush / Swap** | *None* | N/A | **UNTESTED** | **UNTESTED** | **UNTESTED** | No | Direct unit test for background flush trigger pending. |
| **Compaction** | *None* | N/A | **UNTESTED** | **UNTESTED** | **UNTESTED** | No | `CompactAll` is never invoked in any test or benchmark across the codebase. |
| **Concurrent Read + Compaction** | *None* | N/A | **UNTESTED** | **UNTESTED** | **UNTESTED** | No | Zero tests exercise readers while compaction is active (scheduled for I1.2/D1). |
| **Reader Set Lifecycle / Pinning** | *None* | N/A | **UNTESTED** | **UNTESTED** | **UNTESTED** | No | No tests for reader pinning, safe reclamation, or use-after-close prevention. |
| **Benchmark: Sequential Writes** | `BenchmarkSequentialWrites` | `lsmengine/manager_bench_test.go:9` | **BLOCKED** | Runnable | Runnable | No | Unblocked by `E-001` fix. |
| **Benchmark: Parallel Writes** | `BenchmarkParallelWrites` | `lsmengine/manager_bench_test.go:41` | **BLOCKED** | Runnable | Runnable | No | Unblocked by `E-001` fix. |
| **Benchmark: Parallel Reads** | `BenchmarkParallelReads` | `lsmengine/manager_bench_test.go:68` | **BLOCKED** | Runnable | Runnable | No | Unblocked by `E-001` fix. |
| **Benchmark: SSTable Reads** | `BenchmarkSSTableReads` | `lsmengine/manager_bench_test.go:108` | **BLOCKED** | Runnable | Runnable | No | Unblocked by `E-001` fix. |
| **External Comparison** | `BenchmarkBoltDBSequentialWrites` | `lsmengine/comparison_bench_test.go:13` | Runnable | Runnable | Runnable | No | Third-party BoltDB benchmark only. |
| **External Comparison** | `BenchmarkBoltDBParallelReads` | `lsmengine/comparison_bench_test.go:51` | Runnable | Runnable | Runnable | No | Third-party BoltDB benchmark only. |

---

## 2. Summary of Findings
1. **Existing Test Pass Rate**: 16/16 passing on Windows native (1.26s), 16/16 passing under WSL2 `go test -race` with zero data races (1.77s).
2. **Cross-Tier Conflict Safety**: Verified across active Memtable, frozen Memtables, and SSTables regardless of slice ordering (Option A fix for E-004 verified).
3. **Manager.Get Search Semantics**: Audit confirmed that `Manager.Get` relies strictly on `INV-ORDER-1` (ActiveSSreaders ordered chronologically from oldest to newest).
4. **Compaction Testing Gap**: `CompactAll` remains to be repaired and protected in Sub-phase I1.2.

