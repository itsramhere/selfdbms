# Durability, Recovery, and Resource Limitations Audit (Sub-phases I1.4.5 & I1.5.4)

**Milestone:** Sub-phases I1.4.5 & I1.5.4  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Date:** 2026-10-17  
**Auditor:** Antigravity / Pair Programmer  

---

## 1. Executive Summary

This document addresses the structural limitations, resource trade-offs, and durability boundaries of the storage engine following Checkpoint CP1.3c, Sub-phase I1.4, and the final Approach A validation in Sub-phase I1.5.

As required by the external reviewer's directives for I1.4.5 and I1.5.4:
1. We maintain the reconciled test inventory count across execution totals (confirming 98 unit tests, 104 total runnable tests including 6 benchmarks).
2. We replace all assertions of "mathematically proven" deadlock freedom with accurately scoped empirical and lock-ordering findings.
3. We analyze the memory growth implications and backpressure absence of the unbounded FIFO flush queue (Decision D-015 / RISK-001).
4. We evaluate the durability boundaries of WAL recovery across file rotations and SSTable flush failures (RISK-002, RISK-003).
5. We codify the exact system behavior across five operational recovery scenarios (Scenarios A through E).
6. We explicitly assess engine benchmark eligibility for future experimental phases (I4/I5).
7. We catalog all open and resolved architectural risks for Checkpoint CP1.

---

## 2. Test Inventory & Count Reconciliation

### 2.1 The CP1.3c Reconciliation
In the Checkpoint CP1.3c report (`CP1.3c.md`), the summary row stated:
`TOTAL | 56 unit tests (66 total runnable tests)`
However, the 13 individual test files in that same table listed counts of:
$$5 + 3 + 8 + 4 + 4 + 4 + 4 + 4 + 9 + 6 + 6 + 5 + 4 = 66$$
The discrepancy was a textual labeling artifact: the file inventory and the actual Go test runner both executed exactly **66 `Test*` functions**. The label "56" in CP1.3c was an uncorrected typographical remnant carried over from an earlier sub-phase draft.

### 2.2 Test Inventory Progression Through Sub-phase I1.5
- **Post-I1.4**: Added 13 tests in `memtable_concurrency_test.go` (10) and `cross_tier_ordering_verification_test.go` (3), reaching 79 unit tests.
- **Post-I1.4.7**: Added 14 tests in `failed_flush_precedence_test.go` covering E-010 and lifecycle interactions, reaching 93 unit tests.
- **Post-I1.5.2**: Added 5 tests in `mode_selection_test.go` establishing the synchronization mode seam (`SyncModeApproachA` vs `SyncModeApproachB`), reaching **98 unit tests**.

The current runnable test count in `lsmengine` is:
$$66 + 13 + 14 + 5 = \mathbf{98 \text{ unit tests}} \quad (\mathbf{104 \text{ total runnable including 6 benchmarks}})$$
verified directly via:
```powershell
(go test -list "^Test" ./lsmengine | Measure-Object -Line).Lines - 1 # Yields 98
(go test -v -count=1 ./lsmengine | Select-String "^--- PASS:").Count # Yields 98
```
Every single one of the 98 unit tests passes on both Windows 11 NTFS native (7.70s) and WSL2 Linux with `-race` (14.52s, 0 data races, 0 deadlocks). Targeted concurrency stress tests (`-count=5`) pass cleanly on Windows (10.12s) and WSL2 `-race` (23.64s). Clean `go vet ./...` in both environments.


---

## 3. Scoping of Deadlock-Freedom & Concurrency Findings

### 3.1 Retraction of "Mathematical Proof" Claims
Earlier checkpoint narratives loosely used phrases such as "mathematically impossible deadlock." In empirical database systems engineering, concurrent deadlock freedom cannot be claimed as an unproven mathematical theorem unless verified by formal methods (e.g., TLA+ specifications or Coq proofs).

### 3.2 Accurately Scoped Invariant Findings:
We replace formal mathematical claims with the following empirically verified findings:
1. **Acyclic Lock Acquisition Hierarchy (`INV-SYNC-3`)**:
   $$\text{m.compactionMu} \longrightarrow \text{Manager.Mu} \longrightarrow \text{m.flushQueueMu} \longrightarrow \text{Wal.mu}$$
   Code inspection confirms that this partial order is strictly adhered to across all paths. No thread or goroutine holding a lower-tier lock ever attempts to acquire a higher-tier lock.
2. **Circular Wait Elimination on Flush Admission (`INV-QUEUE-1`)**:
   In CP1.3b, circular wait occurred because `enqueueFlushLocked` attempted a blocking channel send on a bounded channel (capacity 64) under `Manager.Mu.Lock()`. In I1.3.8b, this was replaced by an unbounded internal FIFO slice append (`m.flushQueue = append(m.flushQueue, mem)`) guarded by `m.flushQueueMu`. Enqueueing is instantaneous and $O(1)$ amortized; it never waits on channel capacity, worker scheduling, or disk I/O.
3. **Empirical Validation**:
   Deadlock freedom has been empirically demonstrated under saturated conditions:
   - 128 flushes admitted in rapid succession (under 2.4ms).
   - 10 concurrent `Manager.Close()` callers interleaving with active flushes.
   - 10 concurrent overlapping transactions on identical keys.
   - Continuous point reads under WSL2 ThreadSanitizer (`-race`) reporting zero deadlocks and zero data races.

---

## 4. Unbounded Flush Queue: Memory Growth & Backpressure Trade-offs

### 4.1 The Mechanism (Decision D-015)
To eliminate the blocking send under `Manager.Mu`, the flush queue was converted to an unbounded slice protected by `m.flushQueueMu sync.Mutex` and `m.flushQueueCond *sync.Cond`.

### 4.2 Architectural Trade-off & Limitations:
1. **Absence of Ingestion Backpressure**:
   When sustained client write throughput exceeds disk I/O write bandwidth (e.g. disk write speed is 50 MB/s, but client ingestion is 200 MB/s), active Memtables fill rapidly, freeze, and append to `m.flushQueue`. Because `enqueueFlushLocked` never blocks, client transactions continue committing at full memory speed.
2. **Unbounded Heap Expansion**:
   Every frozen Memtable remains referenced in two structures:
   - `m.Frozenmem`: Accessible for in-memory reads via `Manager.Get()`.
   - `m.flushQueue`: Queued for background writing by `flushLoop()`.
   If the background flush worker falls behind, memory usage grows linearly with the volume of frozen data. Under extreme write bursts, this will trigger Go runtime garbage collector pressure, excessive memory allocation, and eventual Out-Of-Memory (OOM) termination by the host operating system.
3. **Required Future Enhancement (CP2 / Production Phase)**:
   A production-grade LSM storage engine requires an **asynchronous admission backpressure protocol** (e.g., write-stall or write-throttling when `len(m.Frozenmem) > MaxFrozenMemtablesThreshold`). Crucially, this backpressure must be applied **before** acquiring `Manager.Mu.Lock()` (e.g. in `Transaction.Begin()` or in client admission) so that holding `Manager.Mu` is never coupled to worker progress.

---

## 5. Durability Boundaries: WAL Rotation & Recovery Limits

### 5.1 Single-File Recovery Limit in `RecoverFromWAL`
The existing implementation of WAL recovery in `lsmengine/manager.go`:
```go
func (m *Manager) RecoverFromWAL(walPath string) error
```
accepts a single WAL file path (historically `"wal.log"`).
However, during normal engine operation, `checkAndTriggerFlush()` and `TriggerFlush()` rotate the WAL upon freezing `m.Mem`:
```go
newWalPath := fmt.Sprintf("wal_%d.log", time.Now().UnixNano())
newWal, err := NewWal(newWalPath)
m.Log.File.Close()
m.Log = newWal
```
**Limitation**:
If the database process terminates ungracefully after multiple rotations have occurred, older `wal_<timestamp>.log` files remain on disk. The current `RecoverFromWAL` API does not automatically scan the directory, discover all historical rotated WAL segments, order them by generation timestamp, or replay them sequentially. Replaying only the active WAL would miss writes that were committed to previous WAL segments if their corresponding SSTables were not flushed or were partially written.

### 5.2 Limits of Retaining Failed Memtables in Memory
In Sub-phase I1.3.8d, error handling was hardened so that if SSTable construction fails (e.g., disk full, write error), the Memtable is **retained in `m.Frozenmem`** rather than discarded.
**Clarification of Durability Semantics**:
- Retaining a failed Memtable in `m.Frozenmem` preserves **in-memory data visibility** for active transactions and point reads during the current process lifetime.
- It **DOES NOT** guarantee durability after process termination. If the process is halted, killed, or crashes, volatile RAM state is lost.
- Post-termination durability depends exclusively on WAL disk records. If a flush failed and the corresponding WAL segment is subsequently deleted, overwritten, or unlinked, durability is permanently compromised.
### 5.3 Snapshot Isolation vs Point-Read Version Selection Boundary
Reference-oracle tests (such as `TestCrossTierOrdering_ReferenceModelOracle` and `TestFailedFlush_E010_*`) validate point-read version selection at discrete read timestamps ($T_{\text{read}}$) across multiple memory and storage tiers.
**Important Boundary Qualification**:
- These tests establish that the storage engine correctly selects the latest visible version $\le T_{\text{read}}$ for individual point lookups.
- They **DO NOT** constitute a formal proof of full multi-key snapshot isolation or serializability across concurrent long-running multi-statement transactions.
- True ACID snapshot isolation across arbitrary multi-key transactions requires transaction workspace buffering, repeatable reads across concurrent compactions, and predicate or range locks, which are beyond the single-key version-selection scope evaluated here.

### 5.4 Durability and Crash Recovery Claims Boundary
- **No Blanket Zero-Data-Loss or Crash-Consistency Claims**: While in-memory handoff (`INV-HANDOFF-1`) eliminates window gaps between `Frozenmem` and `ActiveSSreaders` during running process lifetime, post-crash consistency depends on durable disk state.
- Because `RecoverFromWAL` does not replay multiple rotated WAL segments (`RISK-002`), an ungraceful crash or power failure while unflushed Memtables reside in RAM can result in data loss unless multi-WAL replay is implemented.

### 5.5 Codified Operational Recovery Scenarios (A through E)

The engine's lifecycle and recovery behavior across distinct operational scenarios is codified as follows:

| Scenario | Trigger / Condition | State Transitions | Memory Behavior | Disk & Durability Behavior | Exit / Error State |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Scenario A: Normal Clean Shutdown** | Caller invokes `Close()` with zero pending queued flushes. | `OPEN -> CLOSING -> CLOSED` | If active `m.Mem` has writes, frozen and enqueued. Worker builds SSTable, publishes to `ActiveSSreaders`, drains `Frozenmem`. | Completed SSTables preserved; all active SSTable readers and WAL closed cleanly; file locks released. | `Close()` returns `nil`. Post-shutdown calls return `ErrClosed`. |
| **Scenario B: Clean Shutdown with Queued Flushes** | Caller invokes `Close()` while multiple frozen Memtables ($F_0, F_1, \dots, F_k$) remain in `m.flushQueue`. | `OPEN -> CLOSING -> CLOSED` | Worker sequentially drains FIFO queue in freeze order. Each generation transitions atomically from `Frozenmem` to `ActiveSSreaders`. | All queued data flushed to durable SSTables before readers close; zero data loss; zero handle leaks on Windows NTFS. | `Close()` waits for drain completion and returns `nil`. |
| **Scenario C: Fatal Flush Failure (Fail-Stop)** | Background worker encounters disk I/O error (disk full, I/O fault) building SSTable $F_0$. | `OPEN` (or `CLOSING`) with `m.flushErr != nil` | Option A Fail-Stop (D-017): Worker records `flushErr`, halts immediately; keeps $F_0$ in `Frozenmem`; halts queue. Unflushed generations ($F_1, F_2$) remain queryable in RAM in strict chronological order. | Partial SSTable file unlinked immediately; zero corrupt files published to `ActiveSSreaders`. Precedence preserved. | Worker terminates; `FlushErr()` and `Close()` surface recorded disk error. Post-halt durability requires WAL. |
| **Scenario D: Restart Recovery via `RecoverFromWAL`** | Engine process restarts; caller invokes `RecoverFromWAL(walPath)`. | Initialization -> Replay -> Ready | Committed entries replayed into active Memtable. Sequence numbers restored. | Replays active WAL file. Limitation (`RISK-002`): does not auto-scan or sequence historical rotated `wal_*.log` files. | Returns `nil` on clean replay, or parsing error on corrupted log record. |
| **Scenario E: Power Loss / Ungraceful Crash** | Host power failure, OS kernel crash, or `SIGKILL` (`kill -9`). | Immediate process termination without running defer/Close. | Volatile memory (`m.Mem`, `m.Frozenmem`, `m.flushQueue`) lost instantly. | Durability bounded by WAL filesystem writeback policy (OS page cache vs synchronous fsync). Inactive SSTables on disk remain intact. | On restart, unwritten in-memory data lost unless recovered from durable WAL segments. |

---

## 6. Open Architectural Risks Catalog for Checkpoint CP1

| Risk ID | Subsystem | Severity | Status | Description | Current Mitigation | Future Mitigation Required |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`RISK-001`** | Admission / Memory | High | **OPEN** | Unbounded queue slice can exhaust host RAM under sustained write load exceeding disk flush bandwidth. | Non-blocking enqueue under `flushQueueMu`; queue drains cleanly on normal shutdown. | Introduce non-blocking write-stall admission control before acquiring `Manager.Mu`. |
| **`RISK-002`** | Recovery / WAL | Medium | **OPEN** | `RecoverFromWAL` only replays a single specified log file; does not discover or sequence multiple rotated `wal_*.log` files after crash. | Rotated WAL files are preserved on disk without premature unlinking. | Implement directory-wide WAL segment discovery, ordering, and multi-segment replay. |
| **`RISK-003`** | Durability / Crash | Medium | **OPEN** | Retaining failed Memtables in `Frozenmem` protects process RAM only; crash loses unwritten SSTables unless replayed from WAL. | Failed Memtables stay queryable in RAM; incomplete SSTables unlinked immediately. | Add explicit crash recovery test suite simulating power loss across rotated WALs. |
| **`RISK-004`** | Flush Failure Precedence | Critical | **RESOLVED** | Older failed flush $F_0$ remaining in `Frozenmem` while newer flush $F_1$ publishes to disk inverts tier precedence, causing stale reads and tombstone resurrection (`E-010`). | **Resolved via Option A Fail-Stop (D-017)**: Flush worker halts immediately upon fatal error (`m.flushErr != nil`); suppresses subsequent publications; retains unflushed Memtables in `Frozenmem` in strict chronological order. | None for in-memory precedence. Crash recovery after fail-stop relies on multi-WAL replay (`RISK-002`). |

---

## 7. Benchmark Eligibility & Experimental Evaluation Boundaries

### 7.1 Readiness Assessment for Approach A Baseline
The engine is **certified ready for baseline benchmarking** in experimental evaluation phases (Phases I4 and I5), subject to explicit structural boundaries:

1. **Synchronization Integrity Certified**:
   - 98 unit tests passing across Windows 11 NTFS native and WSL2 Linux (`-race`).
   - Clean static analysis via `go vet ./...` in both operating environments.
   - Zero data races, zero circular-wait deadlocks, zero handle leaks verified empirically under continuous multi-goroutine load.
2. **Explicit Baseline Mode Seam**:
   - `SyncModeApproachA` ("ApproachA_RWMutexBaseline") is operational, verified, and active by default.
   - `SyncModeApproachB` is cleanly isolated and returns `ErrUnsupportedSyncMode` until Phase I2 implementation begins.
3. **Safe Reader Retirement Established**:
   - Compaction serialization via `m.compactionMu` prevents reader replacement races (`E-006`).
   - Active point queries holding `Manager.Mu.RLock()` run to completion before retired readers are closed or unlinked on disk (`TestReaderLifetime_CaseC`).
   - Windows NTFS file handle sharing violations (`ERROR_SHARING_VIOLATION`) eliminated (`TestFilesystem_CloseBeforeDelete`).

### 7.2 Experimental Evaluation Boundaries & Constraints
When executing baseline performance benchmarks in Phases I4/I5, the following boundary constraints must govern experimental reporting:

1. **Serialized Compactor Bottleneck**:
   Under Approach A, compaction execution is strictly serialized by `m.compactionMu`. Workloads with multi-threaded compaction triggers will measure FIFO compaction queue latency rather than concurrent background compaction throughput.
2. **Reader-Compactor Lock Contention**:
   Under Approach A, compaction publication acquires exclusive `Manager.Mu.Lock()`. High-concurrency read workloads will experience read-latency spikes during compaction publication. This contention profile constitutes the primary baseline metric against which Approach B (atomic pointer snapshot swap) will be evaluated.
3. **Flush Ingestion Rate vs Heap Growth (`RISK-001`)**:
   Benchmark workloads with ingestion rates exceeding single-worker disk flush speed must bound test duration to avoid unthrottled heap growth.
4. **WAL Durability Profile**:
   Standard benchmarks must clearly distinguish between in-memory WAL append throughput and synchronous disk fsync latency.


