# Checkpoint CP1.4a: Validation and Repair of Failed-Flush Version Precedence (RISK-004 / Issue E-010)

**Milestone:** Checkpoint CP1.4a  
**Sub-phase:** I1.4.7 (I1.4.7a through I1.4.7e)  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Git Branch:** `research/reader-compactor-sync`  
**Current HEAD Commit:** `b4fd55c` (tracking commit `592461c`)  
**Prior Approved Milestone:** CP1.4 (`6eb59b3`, tracking `9e8692f`)  
**Status:** **CONDITIONALLY RESOLVED — AWAITING EXTERNAL REVIEW**  

---

## 1. Executive Summary

In response to the external reviewer's conditional acceptance of CP1.4 identifying **`RISK-004` (Flush Failure Precedence Inversion)** as an unresolved correctness risk, sub-phase **I1.4.7** was executed in strict accordance with the review mandate.

### Core Findings & Outcomes:
1. **Defect Confirmed & Cataloged (`E-010`)**: An audit of `flushLoop`, `flushToDisk`, `Manager.Get`, and `latestValueLocked` confirmed that when a background flush $F_0$ failed, the worker loop continued processing subsequent flushes ($F_1, F_2$). When $F_1$ succeeded, it was published to `m.ActiveSSreaders` while $F_0$ remained in `m.Frozenmem`. Because `Manager.Get` and `latestValueLocked` examine `Frozenmem` before `ActiveSSreaders`, older data in $F_0$ masked newer writes and tombstones in $F_1$.
2. **Deterministic Reproduction**: Built an 8-case deterministic reproduction suite using fault-injection hooks and channel barriers in `lsmengine/failed_flush_precedence_test.go`. Confirmed reproducible stale reads, tombstone resurrection, stale deletion masking, 3-generation masking, and transaction write-write conflict detection failure on both Windows native and WSL2 Linux (`-race`). Failing-before logs were preserved in `research/debug/E-010/`.
3. **Architectural Correction Implemented (Option A — Fail-Stop Flush Processing / Decision D-017)**: Implemented Option A. When `flushToDisk` encounters any fatal storage error, it records `m.flushErr`. The background worker `flushLoop` immediately observes `m.flushErr != nil` and halts further processing without dequeuing or attempting subsequent flushes. All unflushed Memtables remain safely in `m.Frozenmem` in strict chronological freeze order ($[F_0, F_1, F_2, \dots]$), strictly preserving `INV-ORDER-1`, `INV-MVCC-6`, and `INV-FLUSH-FAIL-1`. Reverse iteration in `Get()` and `latestValueLocked()` guarantees newest versions are encountered first.
4. **Lifecycle & Concurrency Verification**: Expanded the test suite to 14 deterministic tests validating fail-stop behavior under queued flushes, concurrent point reads, concurrent commits, compaction, shutdown, repeated shutdown, and multiple flush attempts.
5. **Full Platform Test Pass**:
   - **Windows 11 Native (NTFS)**: 93/93 unit tests PASS (7.99s).
   - **WSL2 Ubuntu Linux (`go test -race`)**: 93/93 unit tests PASS (14.03s, 0 data races, 0 deadlocks).
   - Static analysis: `go vet ./...` clean.
6. **Recommendation**: Defect `E-010` is fully resolved; `RISK-004` is marked RESOLVED. The storage engine is in a stable, verified baseline state ready for authorization to proceed to **I1.5 (Reader Snapshot Implementation & Approach B Validation)**.

---

## 2. Original RISK-004 Analysis (Sub-phase I1.4.7a)

During the CP1.4 durability audit, the following failure mode was identified:
- If a background flush $F_0$ encounters a disk I/O error (e.g. disk full, permission denied, file system error) during SSTable building or reader opening, `flushToDisk` returns early without removing $F_0$ from `m.Frozenmem`.
- In the baseline implementation, `flushLoop` did not inspect `m.flushErr` after `flushToDisk` returned. It immediately looped back to dequeue the next task ($F_1$).
- If $F_1$ succeeded, $F_1$'s newly created `SSTableReader` was appended to `m.ActiveSSreaders`, and $F_1$ was removed from `m.Frozenmem`.
- As a consequence:
  - $F_0$ (older generation) remained in `m.Frozenmem` (Tier 2).
  - $F_1$ (newer generation) resided in `m.ActiveSSreaders` (Tier 3).
- Because `Manager.Get` and `Manager.latestValueLocked` inspect Tier 2 before Tier 3, this created a severe tier precedence inversion:
  $$\text{Tier 2 (Frozenmem, containing } F_0 \text{)} \succ \text{Tier 3 (ActiveSSreaders, containing } F_1 \text{)}$$
  even though $F_1$ committed chronologically after $F_0$.

---

## 3. Exact Deterministic Reproduction and Baseline Results (Sub-phase I1.4.7b)

In `lsmengine/failed_flush_precedence_test.go`, controlled fault injection via `man.flushBuildHook` and channel barriers (`f0Started`, `f0AllowFail`) isolated the failure of $F_0$ while allowing $F_1$ to proceed.

### Baseline Reproduction Results (Pre-Fix):
- **Windows 11 Native**:
  ```
  === RUN   TestFailedFlush_E010_StaleReadRepro
      failed_flush_precedence_test.go:121: DEFECT E-010 REPRODUCED: Manager.Get returned stale value "old" (expected "new"). Failed flush F0 in Frozenmem masked newer SSTable F1!
  --- FAIL: TestFailedFlush_E010_StaleReadRepro (0.02s)
  === RUN   TestFailedFlush_E010_TombstoneResurrectionRepro
      failed_flush_precedence_test.go:205: DEFECT E-010 REPRODUCED: Manager.Get returned resurrected value "val_to_delete" (error <nil>, expected ErrKeyDeleted key has been deleted). Older Put in failed flush F0 masked newer Tombstone in SSTable F1!
  --- FAIL: TestFailedFlush_E010_TombstoneResurrectionRepro (0.02s)
  === RUN   TestFailedFlush_E010_StaleDeletionMaskingRepro
      failed_flush_precedence_test.go:289: DEFECT E-010 REPRODUCED: Manager.Get returned error key has been deleted, val " " (expected "reborn_at_s2"). Older Tombstone in failed flush F0 masked newer Put in SSTable F1!
  --- FAIL: TestFailedFlush_E010_StaleDeletionMaskingRepro (0.02s)
  === RUN   TestFailedFlush_E010_ThreeGenerationsRepro
      failed_flush_precedence_test.go:369: DEFECT E-010 REPRODUCED: Manager.Get returned "v0" (expected "v2") across 3 generations! Failed flush F0 masked SSTables F1 and F2.
  --- FAIL: TestFailedFlush_E010_ThreeGenerationsRepro (0.04s)
  === RUN   TestFailedFlush_E010_WriteConflictDetectionFailure
      failed_flush_precedence_test.go:574: DEFECT E-010 REPRODUCED: Transaction committed write on k8 without detecting write-write conflict against SSTable F1 (seq S2=2)! latestValueLocked returned seq S1=1 from failed flush F0 in Frozenmem.
  --- FAIL: TestFailedFlush_E010_WriteConflictDetectionFailure (0.03s)
  ```
- **WSL2 Linux (`-race`)**: Identical failure output, confirming 100% deterministic reproducibility across operating systems. Preserved in `research/debug/E-010/before.log` and `research/debug/E-010/wsl_before.log`.

---

## 4. Root Cause Analysis

1. **Unbounded Worker Execution Loop**: `flushLoop` unconditionally continued executing after `flushToDisk(mem)` returned an error, failing to halt the pipeline.
2. **Asymmetric State Transition**: On failure, `mem` was retained in `m.Frozenmem`. On subsequent success, newer Memtables migrated to `m.ActiveSSreaders`, fracturing chronological order across storage tiers.
3. **Hardcoded Hierarchical Tier Assumption**: `Manager.Get` and `latestValueLocked` rely on the invariant that *all* elements in `m.Frozenmem` are strictly newer than *all* elements in `m.ActiveSSreaders`. When this invariant was broken by partial flush progress, reverse iteration within tiers could not prevent cross-tier masking.

---

## 5. Alternatives Considered & Selected Solution (Decision D-017)

| Criterion | Option A: Fail-Stop Flush Processing (Selected) | Option B: Version-Aware Cross-Tier Lookup |
| :--- | :--- | :--- |
| **Mechanics** | Worker halts immediately when `m.flushErr != nil`; suppresses subsequent SSTable publications; retains unflushed Memtables in `Frozenmem` in strict chronological order. | Read paths (`Get` and `latestValueLocked`) scan ALL memory and disk tiers for every query, comparing sequence numbers to find the maximum. |
| **Tier Precedence (`INV-ORDER-1`)** | **PRESERVED**: Every Memtable in `Frozenmem` is strictly newer than every reader in `ActiveSSreaders`. | **VIOLATED**: $F_1$ is on disk while $F_0$ is in memory; permanently splits chronological ordering. |
| **Point Read Latency** | **Zero Overhead**: Early-exit optimization preserved; stops at first match in newest tier. | **Catastrophic Overhead**: Must scan sparse index of every on-disk SSTable even when key is in active Memtable. |
| **Compaction Safety** | **SAFE**: Compactor only merges `ActiveSSreaders`, which remain strictly older than all unflushed Memtables. | **HAZARDOUS**: Compactor compacts $F_1$ and $F_2$, leaving $F_0$ uncompacted, distorting L0/L1 leveling. |
| **Error Surfacing** | **EXPLICIT**: Pipeline halt surfaces fatal error state immediately via `FlushErr()` and `Close()`. | **SILENT**: Masks disk failure; continues attempting writes to failing disk. |
| **Assessment** | **SELECTED**: Architecturally sound, minimal diff, zero read overhead, preserves all invariants. | **REJECTED**: High read regression, violates chronological ordering, conceals storage failures. |

---

## 6. Before / After Code Excerpts

### 6.1 `flushLoop` in `lsmengine/manager.go`
```diff
@@ -273,6 +273,15 @@ func (m *Manager) flushLoop() {
 		m.flushQueueMu.Unlock()
 
 		m.flushToDisk(mem)
+
+		// Option A (Fail-stop): Halt worker immediately upon encountering any fatal flush error.
+		// Halting prevents subsequent generations from being published to ActiveSSreaders
+		// while an older Memtable remains in Frozenmem, preserving strict chronological
+		// precedence (INV-ORDER-1, INV-MVCC-6). All remaining queued Memtables remain in Frozenmem.
+		m.Mu.RLock()
+		err := m.flushErr
+		m.Mu.RUnlock()
+		if err != nil {
+			return
+		}
 	}
 }
```

### 6.2 Flush Admission & Error Surfacing in `lsmengine/manager.go`
```diff
+// FlushErr returns the fatal background flush error, if any has occurred.
+func (m *Manager) FlushErr() error {
+	m.Mu.RLock()
+	defer m.Mu.RUnlock()
+	return m.flushErr
+}

 func (m *Manager) enqueueFlushLocked(mem *Memtable) {
+	if m.flushErr != nil {
+		return
+	}
 	m.initFlushWorkerLocked()
...
 func (m *Manager) EnqueueFlush(mem *Memtable) {
 	m.Mu.Lock()
 	defer m.Mu.Unlock()
-	if m.state != EngineOpen {
+	if m.state != EngineOpen || m.flushErr != nil {
 		return
 	}
 	m.enqueueFlushLocked(mem)
 }

 func (m *Manager) TriggerFlush() error {
 	m.Mu.Lock()
 	defer m.Mu.Unlock()
 	if m.state != EngineOpen {
 		return ErrClosed
 	}
+	if m.flushErr != nil {
+		return m.flushErr
+	}
```

---

## 7. Put / Delete / Tombstone Visibility Matrix

Under Option A Fail-Stop, unflushed generations remain in `m.Frozenmem` ordered chronologically: $[F_0, F_1, F_2]$. Reverse iteration (`i = len-1 down to 0`) evaluates newest generations first:

| Scenario | Tier Location ($F_0$) | Tier Location ($F_1$) | Operation on $K$ ($F_0$) | Operation on $K$ ($F_1$) | Query Timestamp ($T_{\text{read}}$) | Result | Correctness Invariant |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Put $\to$ Put** | `Frozenmem[0]` | `Frozenmem[1]` | Put(K, "old") @ S1 | Put(K, "new") @ S2 | $T \ge S_2$ | `"new"` | Stale read eliminated |
| **Put $\to$ Put** | `Frozenmem[0]` | `Frozenmem[1]` | Put(K, "old") @ S1 | Put(K, "new") @ S2 | $T = S_1$ | `"old"` | Historical MVCC read preserved |
| **Put $\to$ Delete** | `Frozenmem[0]` | `Frozenmem[1]` | Put(K, "val") @ S1 | Delete(K) @ S2 | $T \ge S_2$ | `ErrKeyDeleted` | Tombstone resurrection eliminated |
| **Delete $\to$ Put** | `Frozenmem[0]` | `Frozenmem[1]` | Delete(K) @ S1 | Put(K, "reborn") @ S2 | $T \ge S_2$ | `"reborn"` | Stale deletion masking eliminated |
| **3-Gen Chain** | `Frozenmem[0]` | `Frozenmem[1,2]` | Put(K, "v0") @ S1 | Put "v1" @ S2, "v2" @ S3 | $T \ge S_3$ | `"v2"` | Reverse iteration finds $F_2$ first |
| **Active Overlap**| `Frozenmem[0]` | Active `Mem` | Put(K, "v0") @ S1 | Put(K, "active") @ S2 | $T \ge S_2$ | `"active"` | Active Memtable checked before Frozen |

---

## 8. Lifecycle & Interaction Verification (Sub-phase I1.4.7d)

All 14 tests in `lsmengine/failed_flush_precedence_test.go` pass deterministically:

| Test Name | Interaction Tested | Windows Native | WSL2 Linux (`-race`) |
| :--- | :--- | :--- | :--- |
| `TestFailedFlush_E010_Resolved_StaleReadPrevented` | Stale read prevention via fail-stop | **PASS (0.05s)** | **PASS (0.07s)** |
| `TestFailedFlush_E010_Resolved_TombstoneResurrectionPrevented` | Tombstone resurrection prevention | **PASS (0.01s)** | **PASS (0.01s)** |
| `TestFailedFlush_E010_Resolved_StaleDeletionMaskingPrevented` | Stale deletion masking prevention | **PASS (0.01s)** | **PASS (0.01s)** |
| `TestFailedFlush_E010_Resolved_ThreeGenerationsPreserved` | 3-generation chronological preservation | **PASS (0.01s)** | **PASS (0.02s)** |
| `TestFailedFlush_E010_Resolved_ActiveMemtablePrecedence` | Active Memtable precedence over Frozenmem | **PASS (0.01s)** | **PASS (0.01s)** |
| `TestFailedFlush_E010_Resolved_NoPartialPublication` | Partial file unlinking and 0 reader publication | **PASS (0.00s)** | **PASS (0.01s)** |
| `TestFailedFlush_E010_Resolved_WriteConflictDetection` | Write-write conflict detection against newest generation | **PASS (0.01s)** | **PASS (0.01s)** |
| `TestFailedFlush_E010_Resolved_ErrorReportingOnClose` | Flush error surfacing on `Manager.Close()` | **PASS (0.00s)** | **PASS (0.01s)** |
| `TestFailedFlush_Interaction_SubsequentQueuedFlushes` | Retention of queued flushes in `Frozenmem` | **PASS (0.01s)** | **PASS (0.02s)** |
| `TestFailedFlush_Interaction_ConcurrentReads` | 10 concurrent readers during and after failure | **PASS (0.01s)** | **PASS (0.01s)** |
| `TestFailedFlush_Interaction_ConcurrentCommits` | Concurrent commits to active Memtable + WAL | **PASS (0.12s)** | **PASS (0.38s)** |
| `TestFailedFlush_Interaction_Compaction` | Compaction of existing SSTables during flush error | **PASS (0.06s)** | **PASS (0.03s)** |
| `TestFailedFlush_Interaction_RepeatedShutdownCalls` | Repeated `Close()` idempotency and error consistency | **PASS (0.01s)** | **PASS (0.01s)** |
| `TestFailedFlush_Interaction_MultipleFailedFlushes` | `TriggerFlush` rejection once `flushErr` active | **PASS (0.01s)** | **PASS (0.02s)** |

---

## 9. Comprehensive Platform Verification

### Test Suite Execution Summary:
- **Total Unit Tests**: **93 tests** (expanded from 79 by 14 failed-flush tests).
- **Total Runnable Tests**: **99 tests** (including 6 performance/comparison benchmarks).
- **Windows 11 Native (NTFS)**:
  - Command: `go test -v ./lsmengine`
  - Result: **93/93 PASS (7.99s)**.
- **WSL2 Ubuntu Linux (`go test -race`)**:
  - Command: `wsl --cd /mnt/c/Users/ramst/Documents/Projects/selfdbms -e /snap/bin/go test -race -v -count=1 ./lsmengine`
  - Result: **93/93 PASS (14.03s)**, **0 data races**, **0 deadlocks**.
- **Static Analysis**:
  - Command: `go vet ./...`
  - Result: Clean (exit code 0).

---

## 10. Updated Invariants & Risk Catalog Status

### Invariants:
1. **`INV-FLUSH-FAIL-1` (Fail-Stop Flush Pipeline Precedence)**: **ADOPTED & ENFORCED**. Fatal flush error immediately halts worker and suppresses subsequent SSTable publications; retains all unflushed Memtables in `m.Frozenmem` in strict chronological order.
2. **`INV-HANDOFF-1` (Continuous Key Version Visibility Across Storage Tiers)**: **UPDATED**. Explicitly includes active `m.Mem`, distinguishes version visibility from tombstone masking, and qualifies reference-oracle point-read validation.
3. **`INV-ORDER-1` (Chronological Reader Order)**: **MAINTAINED**. Zero out-of-order readers published.

### Open Risks Status for Checkpoint CP1:
- **`RISK-001` (Unbounded Flush Queue Memory Growth)**: **OPEN**. Mitigated by non-blocking admission and clean drain; write-stall admission control cataloged for future work.
- **`RISK-002` (Single-File WAL Recovery Limit in `RecoverFromWAL`)**: **OPEN**. `RecoverFromWAL` accepts a single file path; multi-WAL segment sequencing cataloged for future work.
- **`RISK-003` (Volatile Retention of Failed Memtables in Memory)**: **OPEN**. Failed Memtables remain queryable in RAM during process lifetime; post-crash durability depends on multi-WAL replay (`RISK-002`).
- **`RISK-004` (Flush Failure Precedence Inversion)**: **RESOLVED** via Option A Fail-Stop (D-017 / Issue E-010).

---

## 11. Git Tracking & Commit Reference

- **Base CP1.4 Commit:** `6eb59b3`
- **I1.4.7a Audit Commit:** `064e157`
- **I1.4.7b Deterministic Repro Commit:** `5f7573a`
- **I1.4.7c-e Implementation & Docs Commit:** `592461c`
- **Tracking Commit:** `b4fd55c`

---

## 12. Recommendation on Readiness for Sub-phase I1.5

Sub-phase **I1.4.7** has conclusively verified and repaired defect `E-010` (`RISK-004`). The engine maintains strict chronological precedence, acyclic locking, zero data races, zero file handle leaks, and complete test suite integrity across Windows native and WSL2 `-race`.

**Recommendation:** Checkpoint CP1.4a is **CONDITIONALLY COMPLETE AND READY FOR REVIEW**. Progression to **I1.5 (Reader Snapshot Implementation & Approach B Validation)** is recommended upon external reviewer authorization.

**STOP AT CP1.4a — EXTERNAL REVIEW REQUIRED.**
