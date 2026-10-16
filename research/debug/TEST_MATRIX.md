# Baseline Test Inventory & Coverage Matrix

**Phase:** I0.4 / D0.2 — Baseline Test Reconnaissance  
**Target Commit:** `08d3189069294e8168ebb5234a3bef1d37f1d649`  

---

## 1. Test Categorization & Coverage Gaps

| Category | Test Function | Source File | Status in Baseline | Compaction Active? | Notes |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **SkipList / In-Memory** | `TestSkipListLevelDistribution` | `lsmengine/engine_test.go:9` | **PASS (0.00s)** | No | Tests SkipList geometric level distribution. Does not touch manager or SSTable. |
| **Point Read / Write** | `TestBasicPutGet` | `lsmengine/engine_test.go:37` | **FAIL (Deadlock)** | No | Times out after 5s due to self-deadlock in `Transaction.Commit` (`E-001`). |
| **MVCC / Tombstone** | `TestDeleteAndTombstone` | `lsmengine/engine_test.go:66` | **FAIL (Deadlock)** | No | Times out after 5s due to self-deadlock in `Transaction.Commit` (`E-001`). |
| **WAL / Crash Recovery** | `TestWALRecovery` | `lsmengine/engine_test.go:98` | **FAIL (Deadlock)** | No | Times out after 5s due to self-deadlock in `Transaction.Commit` (`E-001`). |
| **Flush / Swap** | *None* | N/A | **UNTESTED** | No | No direct unit test for memtable flush or swap. |
| **Compaction** | *None* | N/A | **UNTESTED** | No | `CompactAll` is never invoked in any test or benchmark across the codebase. |
| **Concurrent Read + Compaction** | *None* | N/A | **UNTESTED** | No | Zero tests exercise readers while compaction is active. |
| **Reader Set Lifecycle / Pinning** | *None* | N/A | **UNTESTED** | No | No tests for reader pinning, safe reclamation, or use-after-close prevention. |
| **Benchmark: Sequential Writes** | `BenchmarkSequentialWrites` | `lsmengine/manager_bench_test.go:9` | **BLOCKED** | No | Blocked by `E-001` deadlock. |
| **Benchmark: Parallel Writes** | `BenchmarkParallelWrites` | `lsmengine/manager_bench_test.go:41` | **BLOCKED** | No | Blocked by `E-001` deadlock. |
| **Benchmark: Parallel Reads** | `BenchmarkParallelReads` | `lsmengine/manager_bench_test.go:68` | **BLOCKED** | No | Pre-population write phase blocked by `E-001` deadlock. |
| **Benchmark: SSTable Reads** | `BenchmarkSSTableReads` | `lsmengine/manager_bench_test.go:108` | **BLOCKED** | No | Pre-population write phase blocked by `E-001` deadlock. |
| **External Comparison** | `BenchmarkBoltDBSequentialWrites` | `lsmengine/comparison_bench_test.go:13` | Runnable | No | Third-party BoltDB benchmark only. |
| **External Comparison** | `BenchmarkBoltDBParallelReads` | `lsmengine/comparison_bench_test.go:51` | Runnable | No | Third-party BoltDB benchmark only. |

---

## 2. Summary of Findings
1. **Existing Test Pass Rate**: 1 passing (`TestSkipListLevelDistribution`), 3 failing/deadlocked (`TestBasicPutGet`, `TestDeleteAndTombstone`, `TestWALRecovery`).
2. **Compaction Testing Gap**: `CompactAll` is 100% untested in the current baseline.
3. **Concurrency Testing Gap**: There are zero concurrent correctness tests validating read safety during background flushes or compactions.
