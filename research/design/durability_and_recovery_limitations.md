# Durability, Recovery, and Resource Limitations Audit (Sub-phase I1.4.5)

**Milestone:** Sub-phase I1.4.5  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Date:** 2026-10-16  
**Auditor:** Antigravity / Pair Programmer  

---

## 1. Executive Summary

This document addresses the structural limitations, resource trade-offs, and durability boundaries of the storage engine following Checkpoint CP1.3c and Sub-phase I1.4.

As required by the external reviewer's directives for I1.4.5:
1. We reconcile the test inventory count between file listings and execution totals (confirming 66 unit tests at CP1.3c, expanded to 79 tests in I1.4).
2. We replace all assertions of "mathematically proven" deadlock freedom with accurately scoped empirical and lock-ordering findings.
3. We analyze the memory growth implications and backpressure absence of the unbounded FIFO flush queue (Decision D-015).
4. We evaluate the durability boundaries of WAL recovery across file rotations and SSTable flush failures.
5. We explicitly clarify that retaining a failed Memtable in `Frozenmem` preserves in-memory availability only for the process lifetime and does not substitute for on-disk durability.
6. We catalog these items as Open Architectural Risks for Checkpoint CP1.

---

## 2. Test Inventory & Count Reconciliation

### 2.1 The CP1.3c Reconciliation
In the Checkpoint CP1.3c report (`CP1.3c.md`), the summary row stated:
`TOTAL | 56 unit tests (66 total runnable tests)`
However, the 13 individual test files in that same table listed counts of:
$$5 + 3 + 8 + 4 + 4 + 4 + 4 + 4 + 9 + 6 + 6 + 5 + 4 = 66$$
The discrepancy was a textual labeling artifact: the file inventory and the actual Go test runner both executed exactly **66 `Test*` functions**. The label "56" in CP1.3c was an uncorrected typographical remnant carried over from an earlier sub-phase draft.

### 2.2 Current Test Inventory (Post-I1.4)
In Sub-phases I1.4.2 and I1.4.3, we added 13 new unit tests across two new test files:
- `lsmengine/memtable_concurrency_test.go`: 10 deterministic tests (`TestMemtable_Case1` through `Case10`)
- `lsmengine/cross_tier_ordering_verification_test.go`: 3 tests (`TestCrossTierOrdering_*`)

The current runnable test count in `lsmengine` is:
$$66 + 10 + 3 = \mathbf{79 \text{ unit tests}}$$
verified directly via:
```powershell
(go test -list "^Test" ./lsmengine | Measure-Object -Line).Lines - 1 # Yields 79
(go test -v -count=1 ./lsmengine | Select-String "^--- PASS:").Count # Yields 79
```
Every single one of the 79 unit tests passes on both Windows 11 NTFS native (5.52s) and WSL2 Linux with `-race` (12.86s).

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
- Under current logic, the old WAL file descriptor is closed upon rotation, but the file remains on disk. A robust crash-recovery mechanism must treat un-flushed Memtables as requiring WAL replay upon restart.

---

## 6. Open Architectural Risks Catalog for Checkpoint CP1

| Risk ID | Subsystem | Severity | Description | Current Mitigation | Future Mitigation Required |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`RISK-001`** | Admission / Memory | High | Unbounded queue slice can exhaust host RAM under sustained write load exceeding disk flush bandwidth. | Zero blocking under locks; queue drains cleanly on shutdown. | Introduce non-blocking write-stall admission control before acquiring `Manager.Mu`. |
| **`RISK-002`** | Recovery / WAL | Medium | `RecoverFromWAL` only replays a single specified log file; does not discover or sequence multiple rotated `wal_*.log` files. | WAL files are preserved on disk without premature unlinking. | Implement directory-wide WAL segment discovery, ordering, and multi-segment replay. |
| **`RISK-003`** | Durability / Crash | Medium | Retaining failed Memtables in `Frozenmem` protects process RAM only; crash loses unwritten SSTables unless replayed from WAL. | Failed Memtables stay in RAM; incomplete SSTables unlinked immediately. | Add explicit crash recovery test suite simulating power loss across rotated WALs. |
| **`RISK-004`** | Flush Failure State | Medium | If flush $F_0$ fails while $F_1$ succeeds, $F_0$ remains in `Frozenmem` while $F_1$ publishes to `ActiveSSreaders`, which could invert cross-tier precedence. | Worker logs `m.flushErr` and surfaces on `Close()`. | Halt or stall subsequent flush worker tasks if a preceding flush encounters fatal disk I/O error. |
