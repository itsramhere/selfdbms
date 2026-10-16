# Checkpoint CP1.3c: Flush Queue Deadlock and Lifecycle Hardening

**Milestone:** Checkpoint CP1.3c  
**Sub-phase Executed:** I1.3.8 (I1.3.8a, I1.3.8b, I1.3.8c, I1.3.8d, I1.3.8e, I1.3.8f)  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Date:** 2026-10-16  
**Status:** COMPLETE — STOPPED FOR EXTERNAL REVIEWER (ChatGPT) AUTHORIZATION  

---

## 1. Executive Summary

In Sub-phase I1.3.8, we executed the complete flush queue deadlock and lifecycle hardening authorized following Checkpoint CP1.3b. The external reviewer's conditional rejection correctly identified that holding `Manager.Mu.Lock()` during a channel send to a bounded channel (`make(chan *Memtable, 64)`) whose background consumer requires `Manager.Mu.Lock()` creates an unconditional circular wait deadlock under write load.

We have deterministically reproduced the defect (**Issue E-009**), implemented an unbounded internal FIFO task queue protected by a dedicated mutex (`m.flushQueueMu`) and condition variable (`m.flushQueueCond`) (Decision **D-015**), hardened concurrent `Close()` semantics, hardened WAL rotation and failure propagation paths, and validated the entire engine across Windows 11 NTFS native and WSL2 Linux with the Go race detector (`-race`).

### Key Deliverables & Outcomes:
1. **Deadlock Reproduction (I1.3.8a — Commit `2d5417e`)**:
   - Traced the circular wait: Producer holds `Manager.Mu.Lock()` inside `enqueueFlushLocked` waiting on `m.flushChan <- mem` (channel full at capacity 64), while worker in `flushToDisk(F0)` blocks on `m.Mu.Lock()` to publish.
   - Constructed a deterministic test in [`lsmengine/flush_deadlock_repro_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_deadlock_repro_test.go) reproducing this circular wait cleanly without hanging the test runner.
   - Captured full goroutine stack traces during deadlock on Windows native and WSL2 `-race`.
   - Registered defect **E-009** in [`research/logs/ISSUES.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/logs/ISSUES.md). Preserved failing logs in `research/debug/E-009/before.log` and `wsl_before.log`. Zero production code modified during reproduction.
2. **Safe, Ordered Flush Admission (I1.3.8b — Commit `ed78f2c`)**:
   - Replaced raw bounded channel with unbounded FIFO queue slice (`m.flushQueue []*Memtable`) guarded by `m.flushQueueMu sync.Mutex` and `m.flushQueueCond *sync.Cond` (Decision **D-015**).
   - Enqueueing under `Manager.Mu.Lock()` is an instantaneous, non-blocking $O(1)$ slice append and condition signal; it NEVER blocks on queue capacity or worker progress.
   - The single background worker dequeues tasks under `m.flushQueueMu`, builds SSTables without holding locks, and acquires `m.Mu.Lock()` exclusively to publish into `ActiveSSreaders` and remove from `Frozenmem`.
   - Updated `flush_deadlock_repro_test.go` and added 4 new admission tests:
     - `TestFlushDeadlock_E009_Resolved_NoDeadlockOnQueueSaturation`: 65th flush enqueues immediately and finishes without deadlock.
     - `TestFlushAdmission_QueueSaturated128Items`: 128 flushes enqueued in rapid succession (under 2.4ms) without stalling, completing in exact generation order.
     - `TestFlushAdmission_WorkerPublicationDuringAdmission`: Concurrent admissions, publications, and point reads interleave safely.
     - `TestFlushAdmission_ShutdownDuringSaturatedQueue`: `Close()` initiated while 80 flushes are queued cleanly drains all 80 tasks before final teardown.
     - `TestFlushAdmission_KeyVisibilityAcrossLifecycle`: Keys remain continuously visible across Memtable $\to$ Frozen Queue $\to$ In-Flight $\to$ Published SSTable tiers.
   - Saved passing logs in `research/debug/E-009/after.log` and `wsl_after.log`.
3. **Manager.Close Concurrent Hardening (I1.3.8c — Commit `0e0b502`)**:
   - Audited `Manager.Close()` under concurrent invocation:
     - Teardown executes **once and only once**, serialized by `m.compactionMu.Lock()`.
     - All concurrent callers receive the **exact same error** (`m.flushErr`) rather than subsequent callers receiving `nil`.
     - Reader close errors and WAL file close errors are captured and returned.
     - In-flight compactions (holding `compactionMu`) complete before `Close()` tears down readers.
     - In-flight transaction commits (holding `Manager.Mu`) complete, and subsequent commits return `ErrClosed`.
   - Added Cases G, H, I to [`lsmengine/flush_shutdown_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_shutdown_test.go).
   - Codified Section 6 in [`research/design/reader_lifetime_protocol.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/reader_lifetime_protocol.md).
4. **Flush Failure and WAL Error Propagation (I1.3.8d — Commit `540c5ef`)**:
   - Audited all error paths in `flushToDisk`, WAL rotation, and transaction commits.
   - Incomplete SSTables are unlinked immediately on build failure (`os.Remove(filename)`).
   - Failing Memtables are **retained in `m.Frozenmem`** so committed data is never silently lost from RAM.
   - Implemented hardened atomic WAL rotation: creates the new WAL file **before** closing the old WAL or resetting `m.Mem`. If rotation fails, old WAL and active Memtable remain intact, preventing durability loss and nil pointer panics.
   - Added 4 failure-injection tests in [`lsmengine/flush_error_propagation_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_error_propagation_test.go) (disk full, builder unlink, rotation failure, WAL commit error).
   - Codified Section 8 in [`research/design/flush_lifecycle_and_ordering_protocol.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/flush_lifecycle_and_ordering_protocol.md).
5. **Combined Verification & Invariant Synchronization (I1.3.8e & I1.3.8f — Commit `560f6c0`)**:
   - Static analysis: `go vet ./...` reported zero issues.
   - Full regression suite: **56/56 unit tests (66 total runnable tests) PASS** on Windows 11 native (5.06s) and **56/56 unit tests PASS on WSL2 Linux with `-race`** (11.37s) with **zero data races and zero deadlocks**.
   - Codified `INV-QUEUE-1` (unbounded flush admission and circular wait freedom), updated `INV-SYNC-3` (`m.compactionMu -> Manager.Mu -> m.flushQueueMu -> Wal.mu`), updated `INV-LIFETIME-5`, updated architecture model and mutex state matrix in [`research/design/architecture.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/architecture.md), and synchronized [`research/debug/TEST_MATRIX.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/debug/TEST_MATRIX.md).

---

## 2. Invariant Status Matrix

| Invariant ID | Name | Core Mandate | Status | Verification Evidence |
| :--- | :--- | :--- | :--- | :--- |
| **`INV-SYNC-1`** | SkipList Mutation Safety | Memtable Put/Get synchronized. | **Verified** | `t.Man.Mu.Lock()` / `RLock()` in `transaction.go` & `manager.go`. |
| **`INV-SYNC-2`** | Re-entrant Lock Freedom | No recursive lock acquisition on `m.Mu`. | **Verified** | `latestValueLocked` extracted (Issue E-001 fixed in I1.1a). |
| **`INV-SYNC-3`** | Lock Acquisition Hierarchy | Order: `m.compactionMu -> Manager.Mu -> m.flushQueueMu -> Wal.mu`. | **Verified** | Verified across all paths in `compaction.go`, `manager.go`, `transaction.go`. |
| **`INV-QUEUE-1`** | Unbounded Flush Admission | Enqueueing under `m.Mu` never blocks; circular wait eliminated. | **Verified** | Unbounded FIFO queue slice with `m.flushQueueMu` (D-015, E-009 resolved in I1.3.8b). |
| **`INV-LIFETIME-1`** | Pin-Protected Reader Access | Readers not closed during point lookup. | **Verified** | Protected by `m.Mu.RLock()` in Approach A baseline (`reader_lifetime_test.go`). |
| **`INV-LIFETIME-2`** | Safe Reader Retirement | File descriptors closed before `os.Remove`. | **Verified** | Zero `ERROR_SHARING_VIOLATION` on Windows NTFS (`filesystem_cleanup_test.go`). |
| **`INV-LIFETIME-3`** | Non-Destructive Compaction | Compaction does not erase concurrent flushes. | **Verified** | In-place prefix replacement in `replaceCompactedReaders` (`compaction_publication_test.go`). |
| **`INV-LIFETIME-4`** | Compaction Serialization | Compactions serialized via `compactionMu`. | **Verified** | Zero overlapping compaction races (`reader_lifetime_test.go`). |
| **`INV-LIFETIME-5`** | Coordinated Flush Shutdown Drain | 4-phase shutdown drain and FIFO flush queue. | **Verified** | 9 tests in `flush_shutdown_test.go`, 6 tests in `flush_ordering_test.go`, 5 tests in `flush_deadlock_repro_test.go`. |
| **`INV-ORDER-1`** | Chronological Reader Order | `ActiveSSreaders` ordered oldest to newest. | **Verified** | Dedicated FIFO worker guarantees order (Issue E-008 resolved). |
| **`INV-COMPACT-1`** | Compaction Input Contiguity | Compactions restricted to contiguous prefix [0..k]. | **Verified** | Defensive contiguous-prefix validation (`noncontiguous_compaction_counterexample_test.go`). |
| **`INV-MVCC-1..6`** | Multi-Version Concurrency Control | Descending sort, tombstone masking, monotonic RAM. | **Verified** | Option A `latestValueLocked` scans all SSTables tracking `maxSeq` (`i1_2_0_invariants_test.go`). |
| **`INV-DUR-1`** | Write-Ahead Log Before Memory | Sync before Memtable mutation. | **Verified** | `t.Man.Log.Append` executes `File.Sync()` before `Mem.Put`. |

---

## 3. Issue Registry Status

| Issue ID | Sub-phase | Severity | Description | Status | Evidence / Resolution |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`E-001`** | I0.4 / I1.1a | P0 Blocker | Self-deadlock in `Transaction.Commit` via recursive RLock in `LatestValue` | **FIXED** | Extracted `latestValueLocked`. 5/5 tests PASS (`85775ba`). |
| **`E-002`** | I0.4 / I1.1a | Tooling | Missing CGO compiler on Windows for `-race` | **RESOLVED** | WSL2 Ubuntu environment with GCC and Go 1.27.1 (`85775ba`). |
| **`E-003`** | I1.1b | P0 Blocker | Conflict detection bypassed after Memtable flush to SSTable | **FIXED** | Implemented `SSTableReader.LatestValue` with index seek (`4a4264f`). |
| **`E-004`** | I1.2.0 | P1 Hazard | `latestValueLocked` returned on first match, failing if SSTables reorder | **FIXED** | Option A: scans all active readers tracking `maxSeq` (`5bf95d2`). |
| **`E-005`** | I1.3.0 | P0 Blocker | Noncontiguous compaction violates chronological order in `Manager.Get` | **FIXED** | Enforced contiguous-prefix invariant `INV-COMPACT-1` (`b6f531a`). |
| **`E-006`** | I1.3.2 | P0 Blocker | Overlapping compactions race on active readers; shutdown races | **FIXED** | Introduced `compactionMu` and `isClosed` flag (`40c9a3d`). |
| **`E-007`** | I1.3.6b | P1 Hazard | Background flush publishes after `Close()`, leaking handle on NTFS | **FIXED** | 4-phase deadlock-free drain protocol (`396ed81`). |
| **`E-008`** | I1.3.6c | P0 Blocker | Concurrent flushes publish out-of-order, causing stale reads | **FIXED** | Dedicated background FIFO flush worker (`624a5f5`). |
| **`E-009`** | I1.3.8a | P0 Blocker | Circular wait deadlock between `Manager.Mu` and bounded `flushChan` (cap 64) | **FIXED** | Unbounded FIFO queue slice with `m.flushQueueMu` (D-015, `ed78f2c`). |

---

## 4. Test Execution Summary

| Test Suite File | Test Count | Windows Native Result | WSL2 `-race` Result | Key Properties Verified |
| :--- | :--- | :--- | :--- | :--- |
| `engine_test.go` | 5 | **PASS (0.10s)** | **PASS (0.31s)** | SkipList distribution, basic put/get, deletes/tombstones, WAL recovery, write-write conflict. |
| `cross_tier_conflict_test.go` | 3 | **PASS (0.04s)** | **PASS (0.14s)** | Flushed SSTable conflict, tombstone conflict, non-conflicting concurrent transaction. |
| `i1_2_0_invariants_test.go` | 8 | **PASS (0.19s)** | **PASS (0.31s)** | MaxUint64 encoding, multi-SSTable versions, tombstones, multi-block index, I/O errors, ordering anomaly, reorder abort, Manager.Get audit. |
| `compaction_concurrency_test.go` | 4 | **PASS (0.32s)** | **PASS (0.46s)** | Concurrent flush overlap, newer version visibility, tombstone visibility, overlapping inputs validation. |
| `noncontiguous_compaction_counterexample_test.go` | 4 | **PASS (0.16s)** | **PASS (0.25s)** | Noncontiguous rejection, non-prefix rejection, duplicate inputs rejection, valid prefix MVCC visibility. |
| `compaction_publication_test.go` | 4 | **PASS (0.08s)** | **PASS (0.28s)** | Nil replacement rejection, valid replacement success, aborted publication preservation, empty compaction reader. |
| `reader_lifetime_test.go` | 4 | **PASS (0.36s)** | **PASS (0.52s)** | Case C read during retirement, Case D failure paths, Case A overlapping compactions, Case B shutdown during compaction. |
| `filesystem_cleanup_test.go` | 4 | **PASS (0.08s)** | **PASS (0.23s)** | Close before delete, aborted temp cleanup, active file preservation, shutdown handle cleanup. |
| `flush_shutdown_test.go` | 9 | **PASS (0.24s)** | **PASS (0.25s)** | Cases A–F 4-phase drain protocol, plus Cases G, H, I (10 concurrent callers, error propagation, concurrent commits). |
| `flush_ordering_test.go` | 6 | **PASS (0.27s)** | **PASS (1.89s)** | Cases 1–6 FIFO flush worker ordering (reversed timing, 3+ queue drain, multi-version MVCC, tombstone precedence, interleaved compaction, concurrent reads). |
| `combined_lifecycle_validation_test.go` | 6 | **PASS (0.44s)** | **PASS (0.40s)** | Read+Flush, Write+Flush, Concurrent Flushes, Flush+Compact+Shutdown, Cross-tier transitions, Clean handle release. |
| `flush_deadlock_repro_test.go` | 5 | **PASS (2.04s)** | **PASS (3.96s)** | E-009 resolved deadlock, 128 saturated items, concurrent pub+reads, shutdown saturated queue, key visibility across lifecycle. |
| `flush_error_propagation_test.go` | 4 | **PASS (0.76s)** | **PASS (0.66s)** | Disk full build error, partial file unlink, atomic WAL rotation failure, transaction commit WAL write failure. |
| **TOTAL** | **56 unit tests** (66 total runnable tests) | **PASS (5.06s)** | **PASS (11.37s)** | **ZERO data races detected by ThreadSanitizer; ZERO deadlocks; ZERO resource leaks.** |

---

## 5. Next Steps Pending Authorization

Sub-phase I1.3.8 completes the lifecycle and queue hardening required by the external reviewer.
We now await external reviewer authorization to proceed to:
- **I1.4 — Latency and Throughput Profiling (Approach A Baseline)**:
  - Benchmark point read latency distribution (p50, p99, p99.9) under concurrent compaction and flush.
  - Quantify throughput degradation and lock contention on `Manager.Mu`.
  - Establish empirical baseline for comparison against Approach B (reader snapshots).
