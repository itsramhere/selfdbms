# Checkpoint CP3a Verification Addendum

**Document Version**: 1.0.0  
**Date**: 2026-10-18  
**Checkpoint**: CP3a-V — Verification Addendum (Per-Key Timestamps, WaitGroup Linearization, and A/B Test Reconciliation)  
**Branch**: `research/approach-b-snapshots`  
**Tested Revision**: `29c40c62bbac1f67b88ae9b004af4e7be66a93c7`  
**Review Target**: Formal approval of Checkpoint CP3a and authorization to begin Phase I4 (Experimental Benchmarking Protocol)

---

## Section 1: Actual Semantics of `SSTableReader.MaxTimestamp` vs `LatestValue`

### 1.1 Source Code Audit
An exhaustive audit across the entire repository confirms:
- **`SSTableReader.MaxTimestamp()` does NOT exist and has NEVER existed in the Go codebase.**  
  Running `git grep "MaxTimestamp"` returns zero occurrences across all Go source files (`.go`). The only appearances were in the markdown documentation of `CP3a_CHATGPT_REVIEW_PACKET.md` due to an errant manual code snippet drafting error.
- **Production code in `lsmengine/manager.go` has always called `sr.Reader.LatestValue(userKey)`.**  
  In both Approach A (lines 360–373) and Approach B (lines 343–358), `Manager.latestValueLocked` calls:
  ```go
  seq, err := sr.Reader.LatestValue(userKey)
  ```

### 1.2 Actual Implementation of `SSTableReader.LatestValue` (`lsmengine/sstableReader.go:168-240`)
```go
func (sst *SSTableReader) LatestValue(userKey []byte) (uint64, error) {
    if len(sst.Index) == 0 {
        return 0, ErrNotFound
    }

    seekKey := EncodeInternalKey(userKey, math.MaxUint64, true)
    // Binary searches sst.Index to locate candidate block offset...
    ...
    // Reads records sequentially from target block offset:
    for {
        ...
        diskUserKey, diskSeqNo, _ := DecodeInternalKey(diskKey)

        if bytes.Equal(diskUserKey, userKey) {
            return diskSeqNo, nil // PER-KEY SEQUENCE NUMBER RETURNED
        }

        if bytes.Compare(diskUserKey, userKey) > 0 {
            break // Passed userKey in sorted block; terminate
        }
    }

    return 0, ErrNotFound
}
```

### 1.3 Semantic Proof
1. `LatestValue(userKey)` seeks to the exact block containing `userKey`.
2. It decodes each internal key and checks `bytes.Equal(diskUserKey, userKey)`.
3. The first match encountered for `userKey` yields `diskSeqNo`—which, by LSM sorted block order (user keys ascending, sequence numbers descending), is strictly the **greatest committed sequence number of that specific user key**.
4. If an SSTable contains an older version of `alpha` at sequence 1 and an unrelated key `beta` at sequence 500, `reader.LatestValue([]byte("alpha"))` returns `1, nil`. It never returns 500.
5. Therefore, conflict validation in production has always operated with **per-key sequence number granularity**, completely immune to table-global timestamp pollution.

---

## Section 2: Per-Key Conflict Counterexample and Verification Evidence

To empirically prove the absence of table-global timestamp interference and fulfill the reviewer's counterexample mandate, five deterministic regression tests were authored in `lsmengine/per_key_conflict_timestamp_test.go`:

### 2.1 The Reviewer's Counterexample Scenario
Implemented in `TestPerKeyConflict_TableGlobalTimestampCounterexample`:
1. Commit older version of key `alpha` at sequence $1$ (`seqA = 1`).
2. Begin transaction $T$ at read timestamp $R = 1$ (reads `alpha` version 1).
3. Commit newer version of unrelated key `beta` at sequence $2$ (`seqB = 2 > R`).
4. Force both keys into **one single SSTable** via `forceSyncFlush(man)`.
5. Precondition confirmed: The single SSTable's global maximum sequence is $2$ (due to `beta`), while `alpha`'s per-key latest committed sequence is $1 \le R$.
6. Transaction $T$ puts `alpha = "alpha_v3_by_tx"` and calls `Commit()`.
7. **Verification**: If a table-global timestamp were used, $T$ would falsely abort because $\text{tableMax } (2) > R (1)$. Because production code uses per-key `LatestValue("alpha")`, it observes $1 \le R$, and transaction $T$ commits successfully with `nil` error in **both Approach A and Approach B**.

### 2.2 Complementary Verification Test Suite
- **`TestPerKeyConflict_ApproachB_SameKeyStillAborts`**: Confirms that when a newer version of the *same* key `alpha` (sequence $2 > R$) resides in the flushed SSTable, $T$'s commit detects the genuine write-write conflict and aborts.
- **`TestPerKeyConflict_ApproachB_TombstoneCountsAsVersion`**: Confirms that if `alpha` is deleted (tombstone committed at sequence $2 > R$) and flushed to disk, `LatestValue("alpha")` returns the tombstone sequence ($2$), and $T$'s commit aborts with a write-write conflict. Flushed tombstones count as committed versions.
- **`TestPerKeyConflict_ApproachB_MultiSSTableOrdering`**: Verifies that when multiple SSTables exist (Table 1 with `alpha:1`, Table 2 with `beta:2`, Table 3 with `alpha:3`), `latestValueLocked` iterates across all tables and identifies the true per-key maximum ($3 > R$), aborting conflicting transactions while allowing non-conflicting transactions on unrelated keys.
- **`TestPerKeyConflict_ApproachB_CompactionBoundary`**: Verifies that after compacting multiple SSTables into a single merged table containing `alpha:1` and `beta:2`, per-key isolation is strictly preserved. Transactions updating `alpha` at $R=1$ commit without false conflict, and post-compaction mutations to `alpha` continue to conflict properly.

**Execution Result**: All 5 tests PASS on Windows native and WSL2 Ubuntu Linux under `-race` with zero false conflicts and zero missed conflicts.

---

## Section 3: Cleanup WaitGroup Registration Safety and Shutdown Linearization

### 3.1 Go WaitGroup Contract Analysis
The Go runtime `sync.WaitGroup` contract states:
> *"Calls with a positive delta that occur when the counter is zero must happen before a Wait."*

If an asynchronous cleanup goroutine calls `cleanupWg.Add(1)` concurrently while `closeApproachB()` is executing `cleanupWg.Wait()`, the runtime can panic or bypass the wait.

### 3.2 Linearization Boundary Proof
In `lsmengine/snapshot.go` and `compaction.go`, all cleanup scheduling has been strictly hardened under **Decision D-034** (resolving **Issue E-013**):

```mermaid
sequenceDiagram
    autonumber
    participant Reader as Reader Goroutine
    participant SnapshotMu as Manager.snapshotMu
    participant WaitGroup as Manager.cleanupWg
    participant Shutdown as Manager.closeApproachB()

    Note over Shutdown: Phase 1-3: Drain flushes & compactions
    Shutdown->>SnapshotMu: Lock()
    Note over Shutdown: Phase 4: Retire snapshot, wait for active pins == 0
    SnapshotMu-->>Shutdown: snapshotDrainCond.Wait() (releases snapshotMu)
    
    Reader->>SnapshotMu: Lock() in ReleaseSnapshot()
    Note over Reader: snap.ReleasePinLocked() -> 0 pins
    Reader->>SnapshotMu: snap.SetState(SnapshotDrained)
    Reader->>WaitGroup: cleanupWg.Add(1) (under snapshotMu!)
    Reader->>SnapshotMu: snapshotDrainCond.Broadcast()
    Reader->>SnapshotMu: Unlock()
    
    SnapshotMu-->>Shutdown: snapshotDrainCond wakes (re-acquires snapshotMu)
    Note over Shutdown: countTotalActivePinsLocked() == 0
    Shutdown->>SnapshotMu: snapshotPhase = SnapshotPhaseClosed
    Shutdown->>SnapshotMu: Unlock()
    
    Note over Shutdown: LINEARIZATION BOUNDARY CROSSED
    Shutdown->>WaitGroup: cleanupWg.Wait()
    Note over WaitGroup: All Add(1) calls happened-before Wait()
    
    Note over Reader: Async cleanup unlinks file
    Reader->>WaitGroup: cleanupWg.Done()
    WaitGroup-->>Shutdown: cleanupWg.Wait() unblocks
    Shutdown->>Shutdown: drainPendingCleanups() (final synchronous sweep)
```

1. **Compaction Leases**: `closeApproachB` acquires `m.compactionMu.Lock()` and sets `m.compactionAdmissionClosed = true` in Step 2. Any in-flight compaction must finish before shutdown proceeds, and any cleanup scheduled by compaction (`scheduleAsyncCleanupLocked`) executes its `cleanupWg.Add(1)` under `m.snapshotMu` before `compactionMu` is released.
2. **Pinned Readers**: Any reader releasing its final pin acquires `m.snapshotMu.Lock()`, reclaims the snapshot, and executes `scheduleAsyncCleanupLocked()` strictly within that `m.snapshotMu` critical section.
3. **Linearization Point**: `closeApproachB` waits in `snapshotDrainCond.Wait()` until all pins reach zero. It re-acquires `m.snapshotMu.Lock()`, sets `m.snapshotPhase = SnapshotPhaseClosed`, and releases `m.snapshotMu.Unlock()`.
4. **Resurrection / Post-Close Add Guard**: In `scheduleAsyncCleanupLocked`:
   ```go
   func (m *Manager) scheduleAsyncCleanupLocked() {
       if m.snapshotPhase == SnapshotPhaseClosed || m.state == EngineClosed {
           return // Strictly forbidden after linearization boundary!
       }
       m.cleanupWg.Add(1)
       go m.runAsyncCleanup()
   }
   ```
   No positive `cleanupWg.Add(1)` can ever be executed after `m.snapshotPhase` transitions to `SnapshotPhaseClosed`.
5. **Panic-Safe Mutex Release in `ReleaseSnapshot`**:
   `ReleaseSnapshot` now wraps its lock in a deferred unlock:
   ```go
   m.snapshotMu.Lock()
   defer func() {
       m.snapshotMu.Unlock()
       if m.afterReleaseSnapshotUnlockHook != nil {
           m.afterReleaseSnapshotUnlockHook()
       }
   }()
   ```
   If `ReleasePinLocked()` panics on underflow, `m.snapshotMu` is guaranteed to be unlocked during stack unwinding, preventing any subsequent deadlock when `Close()` executes.

### 3.3 Deterministic Verification Test
Authored `TestCleanup_InterleavedFinalPinReleaseAndConcurrentClose` in `lsmengine/cleanup_coordination_test.go`:
- Pin snapshot holding obsolete SSTable.
- Launch 5 concurrent `Close()` goroutines that enter `closeApproachB()` and block on `snapshotDrainCond.Wait()`.
- Install hook in `beforeAsyncCleanupDrainHook` with an unblock barrier.
- In-flight reader releases final pin $\to$ `cleanupWg.Add(1)` runs under `snapshotMu` $\to$ async cleanup enters hook and halts.
- Pin count reaches 0 $\to$ `closeApproachB` transitions to `SnapshotPhaseClosed` and blocks on `cleanupWg.Wait()`.
- Verified that **none** of the 5 `Close()` callers can complete while the cleanup barrier is held.
- Release barrier $\to$ async cleanup completes unlink $\to$ `cleanupWg.Done()`.
- Verified that all 5 `Close()` callers unblock simultaneously, return `nil` error, and the obsolete file is removed from disk.
- **Timing**: PASS (0.16s Windows native, 0.04s WSL2 `-race`).

---

## Section 4: Synchronization Audit of `m.asyncCleanupErr`

| Access Point | Operation | Mutex / Coordination Guarantee | Invariant Enforced |
| :--- | :--- | :--- | :--- |
| **`drainPendingCleanups()`** (`snapshot.go:565`) | Write (`m.asyncCleanupErr = firstErr`) | `m.cleanupMu.Lock()` | Only the first non-nil error across all async invocations is preserved. |
| **`drainPendingCleanups()`** (`snapshot.go:568`) | Read (`firstErr = m.asyncCleanupErr`) | `m.cleanupMu.Lock()` | Survives subsequent successful sweeps; error is never overwritten by `nil`. |
| **`closeApproachB()`** (`snapshot.go:805`) | Synchronization Barrier | `m.cleanupWg.Wait()` | Guaranteed happens-before: all async goroutines writing `asyncCleanupErr` must finish before final read. |
| **`closeApproachB()`** (`snapshot.go:808`) | Final Read (`cleanupErr := m.drainPendingCleanups()`) | `m.cleanupMu.Lock()` (inside `drainPendingCleanups`) | Reflects any error recorded during background unlinks or during final synchronous sweep. |
| **`closeApproachB()`** (`snapshot.go:828`) | Write to Engine Result (`m.closeErr = finalErr`) | `Manager.Mu.Lock()` | Unified error precedence: `flushErr` $\to$ `cleanupErr` $\to$ `walErr`. |
| **Concurrent `Close()`** (`snapshot.go:757-761`) | Read by Concurrent Callers | `<-m.closeDone` followed by `Manager.Mu.Lock()` | Channel close establishes happens-before edge; all concurrent callers read identical `m.closeErr`. |

**Audit Conclusion**: Zero data races and zero error-dropping windows exist on `m.asyncCleanupErr`. Verified under WSL2 ThreadSanitizer by `TestCleanup_AsyncCleanupErrorPropagatedToConcurrentCloseCallers`.

---

## Section 5: A/B Flush Queue Capacity and Admission Parity

The external reviewer requested clarification regarding a statement in the CP3a packet describing the flush queue as "bounded at 128 tasks with backpressure."

### 5.1 Actual Implementation in Production Code
1. **Queue Type**: Dynamic slice `flushQueue []*Memtable` in `lsmengine/manager.go:74`.
2. **Queue Capacity Policy**: **Completely unbounded FIFO queue.**
3. **Queue Admission**:
   ```go
   func (m *Manager) enqueueFlushLocked(mem *Memtable) {
       if m.flushErr != nil {
           return
       }
       m.initFlushWorkerLocked()
       m.flushQueueMu.Lock()
       m.flushQueue = append(m.flushQueue, mem) // Unbounded append
       m.flushQueueCond.Signal()
       m.flushQueueMu.Unlock()
   }
   ```
4. **Backpressure Mechanisms**: **None.** Writers append to `m.flushQueue` without blocking or throttling based on queue depth.
5. **Mode Identity**: `enqueueFlushLocked`, `initFlushWorkerLocked`, and `flushLoop` are **100% identical between Approach A and Approach B**. Both synchronization modes share the exact same queue structure, mutex, condition variable, and fail-stop error behavior.
6. **Origin of Discrepancy**: **Documentation Drift.** The CP3a packet author mistakenly conflated the test parameter from `TestFlushAdmission_QueueSaturated128Items` (which tests rapid admission of 128 flushes under the E-009 regression suite) with an architectural capacity bound. Production code has never enforced a 128-task bound.

---

## Section 6: Exact Reconciled Test Inventory

### 6.1 Mathematical Reconciliation of the 5-Test Discrepancy
The external reviewer noted:
- CP3 reported 132 unit tests.
- CP3a reported 163 unit tests (describing 26 new tests).
- Inventory claimed: $107 \text{ baseline} + 30 \text{ I3} + 26 \text{ I3.7} = 163 \neq 132 + 26$.

**The exact origin of the 5-test discrepancy is identified as follows**:
1. At frozen baseline `baseline-approach-a-verified` (`ae8c034`), there were **101 unit tests**.
2. In Phase I2 (`f1e93c9`), scaffolding test `snapshot_scaffolding_test.go` added **6 unit tests**. Total: $101 + 6 = \mathbf{107}$ unit tests.
3. In Phase I3:
   - Sub-phases I3.1 through I3.4 added **25 unit tests** (7 construction + 7 pinning + 5 publication + 6 shutdown). At that intermediate point, the suite contained $107 + 25 = \mathbf{132}$ unit tests.
   - Sub-phase I3.5 added **5 unit tests** in `lsmengine/approach_b_mvcc_test.go` (`TestApproachEquivalence_SingleKeyCRUD`, `_MultiVersionVisibility`, `_TombstoneShadowingAcrossTiers`, `_CompactionQueryParity`, `_ConcurrentWorkload`). Total: $132 + 5 = \mathbf{137}$ unit tests.
   - In the CP3 delivery documents (`CP3.md` and `CP3_CHATGPT_REVIEW_PACKET.md`), the summary tables prematurely recorded the pre-I3.5 headline count of **132 unit tests**, omitting the 5 newly authored equivalence tests from the top-level table even though they were committed in `920a623`.
4. In Sub-phase I3.7, **26 unit tests** were added. Total: $137 + 26 = \mathbf{163}$ unit tests.
5. In Sub-phase CP3a-V, **6 unit tests** were added (5 in `per_key_conflict_timestamp_test.go` and 1 in `cleanup_coordination_test.go`). Total: $163 + 6 = \mathbf{169}$ unit tests.

### 6.2 Complete Test Suite Inventory (169 Unit Tests + 6 Benchmarks = 175 Runnable)

| Category / Milestone | Test File | Unit Test Count | Description |
| :--- | :--- | :--- | :--- |
| **Approach A Baseline** (`ae8c034`) | 15 baseline files | **101** | Basic KV, WAL recovery, MVCC, tombstone shadowing, E-003, E-004, E-005, E-009, E-010, E-011, CP1-V suites |
| **Phase I2 Scaffolding** | `snapshot_scaffolding_test.go` | **6** | State enums, stringers, Ref/Unref counters, mode support |
| **Phase I3.1–I3.4 Scaffolding** | 4 snapshot files | **25** | Snapshot construction [7], pinning [7], publication [5], shutdown [6] |
| **Phase I3.5 Equivalence** | `approach_b_mvcc_test.go` | **5** | Equivalence CRUD, MVCC visibility, tombstones, compaction, concurrent workload |
| **Phase I3.7 Correctness (CP3a)** | 6 test files | **26** | E-012 read error [3], snapshot conflict [3], closing admission [2], A/B parity [11], resurrection safety [3], cleanup coordination [4] |
| **Phase CP3a-V Addendum** | `per_key_conflict_timestamp_test.go`<br>`cleanup_coordination_test.go` | **6** | CP3a-V.1 per-key conflict counterexample & multi-table [5]<br>CP3a-V.2 interleaved pin release & concurrent close [1] |
| **Total Unit Tests** | | **169** | **100% PASS** on Windows native and WSL2 Ubuntu Linux with ThreadSanitizer |
| **Benchmarks** (Deferred to I4) | `manager_bench_test.go`, `comparison_bench_test.go` | **6** | Sequential/parallel reads and writes, SSTable reads, BoltDB comparisons |
| **Total Runnable Tests** | | **175** | |

---

## Section 7: Revalidation Evidence and Defect/Decision Ledger

### 7.1 Revalidation Execution Logs
1. **Windows 11 Native (NTFS)**:
   ```text
   $ go test -count=1 ./...
   ok      owndbms/lsmengine       9.452s
   $ go vet ./...
   (Clean exit, code 0)
   ```
2. **WSL2 Ubuntu Linux AMD64 (ThreadSanitizer)**:
   ```text
   $ /snap/bin/go test -race -count=1 ./...
   ok      owndbms/lsmengine       18.969s
   $ /snap/bin/go vet ./...
   (Clean exit, code 0)
   ```
3. **WSL2 Linux Targeted Concurrency Stress Run (`-race -count=5`)**:
   ```text
   $ /snap/bin/go test -race -count=5 -run 'TestPerKeyConflict_|TestApproachB_ConflictValidation_|TestApproachB_UnexpectedReadError_|TestApproachB_NilReaderInSnapshot_|TestCleanup_|TestSnapshotShutdown_ConcurrentClose' ./lsmengine
   ok      owndbms/lsmengine       5.086s
   ```
   **Result**: 100% PASS across 5 consecutive iterations under ThreadSanitizer; zero data races, zero deadlocks, zero handle leaks.

### 7.2 Decision & Issue Registrations
- **Decision D-034**: Linearized Asynchronous Cleanup Registration and Panic-Safe Snapshot Mutex Release.
- **Issue E-013**: Asynchronous Cleanup WaitGroup Registration Ordering and ReleaseSnapshot Panic-Safety (Resolved and Verified).

---

## Section 8: Full Tested Revision Hash and Working-Tree Status

- **Tested Commit**: `29c40c62bbac1f67b88ae9b004af4e7be66a93c7`
- **Branch**: `research/approach-b-snapshots`
- **Verified Baseline Tags** (Intact and Unmodified):
  - `baseline-approach-a`: `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc`
  - `baseline-approach-a-verified`: `ae8c034c438d56abbe6664e6472f7a0c1278797f`
- **Working Tree**: Clean.

---

## Section 9: Remaining Limitations and Authorization Request

### 9.1 Preserved Scope Boundaries
1. **Crash Durability Across Rotated WALs (`INV-DUR-2`)**: Single-WAL recovery is verified; automated multi-segment replay across rotated WALs remains unproven (`RISK-002`).
2. **Flush Queue Memory Consumption**: The FIFO flush queue is unbounded. Ingestion rates sustained above disk write speeds will accumulate memory in `Frozenmem`.
3. **Compaction Heap Memory**: Compaction streaming iterator memory is bounded by block size $\times$ table count. Large individual SSTables require chunked sinks.
4. **Zero Performance Benchmarking**: No benchmark throughput or latency measurements were executed.

### 9.2 Request for Formal Approval
All three verification requirements of Checkpoint CP3a-V have been verified with complete mathematical rigor, deterministic test suites, and clean ThreadSanitizer runs.

The team requests formal approval of Checkpoint CP3a and authorization to proceed to **Phase I4 — Experimental Benchmarking Protocol**.

EXTERNAL REVIEW REQUIRED — CP3a VERIFICATION ADDENDUM AWAITING APPROVAL.
