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
| **Conflict: Non-Conflicting** | `TestCrossTierWriteConflict_NonConflicting` | `lsmengine/cross_tier_conflict_test.go:121` | *N/A* (Added I1.1b) | *N/A* | **PASS (0.02s)** | No | Proves independent non-conflicting transactions commit cleanly across tiers. |
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
1. **Existing Test Pass Rate**: 8/8 passing on Windows native, 8/8 passing under WSL2 `go test -race` with zero data races.
2. **Cross-Tier Conflict Safety**: Verified across active Memtable, frozen Memtables, and flushed SSTables for both Puts and Deletes (E-003 resolved).
3. **Compaction Testing Gap**: `CompactAll` remains 100% untested in the current repository (target of I1.2/I1.3).
4. **Concurrency Testing Gap**: Reader-compactor overlap remains untested until Phase I1.2 / Phase D1.
