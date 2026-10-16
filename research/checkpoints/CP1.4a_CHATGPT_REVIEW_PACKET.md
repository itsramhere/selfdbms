# EXTERNAL REVIEW PACKET: Checkpoint CP1.4a (Validation and Repair of Failed-Flush Version Precedence)

**To:** External Reviewer / ChatGPT  
**From:** Antigravity Pairing Assistant  
**Date:** 2026-10-17  
**Governing Sub-phase:** I1.4.7 (I1.4.7a through I1.4.7e)  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Git Branch:** `research/reader-compactor-sync`  
**Current HEAD Commit:** `291c5f9` (tracking commit `592461c`)  
**Prior Approved Milestone:** CP1.4 (`6eb59b3`)  
**Status:** COMPLETE — STOPPED FOR EXTERNAL REVIEWER DECISION  

---

## 1. Overview of Authorized Sub-phase I1.4.7

In the CP1.4 review decision, the external reviewer provisionally accepted the mutable Memtable synchronization findings and reconciled test inventory, but authorized sub-phase **I1.4.7** to investigate and resolve **`RISK-004` (Flush Failure Precedence Inversion)**.

This review packet provides all supporting evidence, reproduction logs, architectural evaluations, before/after code diffs, lifecycle interaction validations, and test results demonstrating that `RISK-004` (cataloged as issue `E-010`) has been conclusively resolved via **Option A: Fail-Stop Flush Processing (Decision D-017)**.

---

## 2. Original RISK-004 Analysis (Sub-phase I1.4.7a)

### The Architectural Hazard:
Prior to this repair, the background flush pipeline operated under the unvalidated assumption that every Memtable flushed would succeed or that an error in one flush would not affect the chronological ordering of subsequent flushes.

### Inspection Trace:
1. **`flushLoop()`**: The single background worker popped tasks from `m.flushQueue` and invoked `m.flushToDisk(mem)`. After `flushToDisk(mem)` returned, `flushLoop()` performed **no check** of `m.flushErr`. It immediately looped back to dequeue the next Memtable.
2. **`flushToDisk(mem)`**: Upon encountering an SSTable build failure (e.g., `os.Create` error, `builder.Close` error, or reader initialization failure), it cleaned up partial files and recorded `m.flushErr = err` under `m.Mu.Lock()`, but returned **without removing `mem` from `m.Frozenmem`**.
3. **Subsequent Flushes**: When subsequent queued flushes ($F_1, F_2$) succeeded, their newly constructed SSTables were published to `m.ActiveSSreaders` and they were removed from `m.Frozenmem`.
4. **Precedence Inversion**:
   - $F_0$ (older generation) remained in `m.Frozenmem` (Tier 2).
   - $F_1$ (newer generation) was published to `m.ActiveSSreaders` (Tier 3).
5. **Read Path Corruption**:
   Because `Manager.Get` and `latestValueLocked` examine Tier 2 (`Frozenmem`) before Tier 3 (`ActiveSSreaders`), reverse iteration checked $F_0$ first. If key $K$ existed in $F_0$, it was returned immediately:
   - **Stale Read**: If $F_1$ was an update (Put), $F_0$'s older value masked $F_1$.
   - **Tombstone Resurrection**: If $F_1$ was a tombstone (Delete), $F_0$'s prior Put value was returned, resurrecting the deleted record.
   - **Stale Deletion Masking**: If $F_0$ was a tombstone and $F_1$ was a reinserted Put, $F_0$'s tombstone masked $F_1$, returning `ErrKeyDeleted`.
   - **Serializability Violation**: `latestValueLocked` returned $F_0$'s older sequence number, blinding concurrent transactions from detecting write-write conflicts against $F_1$.

---

## 3. Deterministic Reproduction Evidence (Sub-phase I1.4.7b)

To verify the vulnerability empirically, 8 deterministic test cases were created in `lsmengine/failed_flush_precedence_test.go` utilizing `man.flushBuildHook` and channel barriers:

```go
f0Started := make(chan struct{})
f0AllowFail := make(chan struct{})

man.flushBuildHook = func() error {
    if flushCount.Add(1) == 1 {
        close(f0Started)
        <-f0AllowFail
        return errors.New("simulated I/O failure on F0 SSTable build")
    }
    return nil
}
```

### Pre-Fix Failure Logs (Windows 11 Native & WSL2 Linux `-race`):
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
*Full raw failure outputs preserved in `research/debug/E-010/before.log` and `research/debug/E-010/wsl_before.log`.*

---

## 4. Evaluation of Architectural Alternatives (Decision D-017)

Two alternative architectures were evaluated to resolve defect `E-010`:

### Option A — Fail-Stop Flush Processing (Chosen):
- **Mechanism**: The single background worker `flushLoop` halts immediately upon observing `m.flushErr != nil` after `flushToDisk` returns.
- **Queue and Tier State**: All subsequent queued Memtables remain in `m.flushQueue` and in `m.Frozenmem`. Zero SSTables are published to `m.ActiveSSreaders` for failed or subsequent generations.
- **Precedence Integrity**: Because all unflushed Memtables remain in `m.Frozenmem` in strict chronological freeze order ($[F_0, F_1, F_2, \dots]$), reverse iteration (`i = len-1 down to 0`) in `Manager.Get` and `Manager.latestValueLocked` naturally encounters the newest generation first.
- **Invariant Preservation**: `INV-ORDER-1`, `INV-MVCC-6`, and new `INV-FLUSH-FAIL-1` are 100% maintained. Point-read lookup performance is completely unaffected (no extra disk scans).
- **Storage Error Surfacing**: An explicit storage error state is surfaced via `m.FlushErr()` and on `Manager.Close()`.

### Option B — Version-Aware Cross-Tier Lookup (Rejected):
- **Mechanism**: Modify `Manager.Get` and `latestValueLocked` to scan all memory and disk tiers, comparing sequence numbers across all matching records to find the maximum.
- **Fatal Flaws**:
  1. Destroys the fundamental LSM tiered lookup performance contract: point reads would be forced to scan all on-disk SSTables even when the key is present in memory.
  2. Permanently breaks chronological reader ordering: $F_1$ is on disk while $F_0$ is in memory, creating an uncompactable, split state.
  3. Compaction would merge $F_1$ while omitting $F_0$, permanently inverting L0 leveling.
  4. Masks fatal disk I/O errors and encourages continued writes to a failing disk subsystem.

**Conclusion**: Option A was chosen and adopted as **Decision D-017**.

---

## 5. Complete Implementation Diff

```diff
diff --git a/lsmengine/manager.go b/lsmengine/manager.go
index 7f873ca..5875dc5 100644
--- a/lsmengine/manager.go
+++ b/lsmengine/manager.go
@@ -273,10 +273,31 @@ func (m *Manager) flushLoop() {
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
 
+// FlushErr returns the fatal background flush error, if any has occurred.
+func (m *Manager) FlushErr() error {
+	m.Mu.RLock()
+	defer m.Mu.RUnlock()
+	return m.flushErr
+}
+
 func (m *Manager) enqueueFlushLocked(mem *Memtable) {
+	if m.flushErr != nil {
+		return
+	}
 	m.initFlushWorkerLocked()
 	m.flushQueueMu.Lock()
 	m.flushQueue = append(m.flushQueue, mem)
@@ -289,7 +310,7 @@ func (m *Manager) enqueueFlushLocked(mem *Memtable) {
 func (m *Manager) EnqueueFlush(mem *Memtable) {
 	m.Mu.Lock()
 	defer m.Mu.Unlock()
-	if m.state != EngineOpen {
+	if m.state != EngineOpen || m.flushErr != nil {
 		return
 	}
 	m.enqueueFlushLocked(mem)
@@ -302,6 +323,9 @@ func (m *Manager) TriggerFlush() error {
 	if m.state != EngineOpen {
 		return ErrClosed
 	}
+	if m.flushErr != nil {
+		return m.flushErr
+	}
 	if m.Mem == nil || (m.Mem.SizeBytes == 0 && m.Mem.SK.headNode.Forward[0] == nil) {
 		return nil
 	}
@@ -350,7 +374,7 @@ func (m *Manager) TriggerFlush() error {
 
 func (m *Manager) checkAndTriggerFlush() {
 	m.Mu.Lock()
-	if m.state != EngineOpen {
+	if m.state != EngineOpen || m.flushErr != nil {
 		m.Mu.Unlock()
 		return
 	}
```

---

## 6. Put / Delete / Tombstone Visibility Matrix

Under fail-stop flush processing, all unflushed generations reside in `m.Frozenmem` ordered chronologically from index 0 ($F_0$) to index $N-1$ ($F_{N-1}$):

| Scenario | $F_0$ (Oldest) | $F_1$ (Newer) | Query Timestamp ($T_{\text{read}}$) | Expected Result | Actual Result | Status |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Put Overwrite** | Put("k1", "old") @ S1 | Put("k1", "new") @ S2 | $T_{\text{read}} \ge S_2$ | `"new"` | `"new"` | **PASS** (Stale read eliminated) |
| **Historical Read** | Put("k1", "old") @ S1 | Put("k1", "new") @ S2 | $T_{\text{read}} = S_1$ | `"old"` | `"old"` | **PASS** (MVCC history preserved) |
| **Tombstone Overwrite** | Put("k2", "val") @ S1 | Delete("k2") @ S2 | $T_{\text{read}} \ge S_2$ | `ErrKeyDeleted` | `ErrKeyDeleted` | **PASS** (Tombstone resurrection eliminated) |
| **Reinserted Put** | Delete("k3") @ S1 | Put("k3", "reborn") @ S2 | $T_{\text{read}} \ge S_2$ | `"reborn"` | `"reborn"` | **PASS** (Stale deletion masking eliminated) |
| **3-Gen Versioning** | "v0" @ S1 | "v1" @ S2, "v2" @ S3 | $T_{\text{read}} \ge S_3$ | `"v2"` | `"v2"` | **PASS** (Latest version selected) |
| **Active Memtable Overlap**| "v0" @ S1 (in Frozen) | "v_active" @ S2 (in Mem) | $T_{\text{read}} \ge S_2$ | `"v_active"` | `"v_active"` | **PASS** (Active Memtable takes priority) |
| **Conflict Detection** | "old" @ S1 | "new" @ S2 | Tx ReadTs = S1 | Conflict Abort | Conflict Abort | **PASS** (Conflict detected against S2) |

---

## 7. Lifecycle and Concurrency Interaction Results (Sub-phase I1.4.7d)

The 14 tests in `lsmengine/failed_flush_precedence_test.go` validate all required lifecycle combinations:

```
=== RUN   TestFailedFlush_E010_Resolved_StaleReadPrevented
--- PASS: TestFailedFlush_E010_Resolved_StaleReadPrevented (0.05s)
=== RUN   TestFailedFlush_E010_Resolved_TombstoneResurrectionPrevented
--- PASS: TestFailedFlush_E010_Resolved_TombstoneResurrectionPrevented (0.01s)
=== RUN   TestFailedFlush_E010_Resolved_StaleDeletionMaskingPrevented
--- PASS: TestFailedFlush_E010_Resolved_StaleDeletionMaskingPrevented (0.01s)
=== RUN   TestFailedFlush_E010_Resolved_ThreeGenerationsPreserved
--- PASS: TestFailedFlush_E010_Resolved_ThreeGenerationsPreserved (0.01s)
=== RUN   TestFailedFlush_E010_Resolved_ActiveMemtablePrecedence
--- PASS: TestFailedFlush_E010_Resolved_ActiveMemtablePrecedence (0.01s)
=== RUN   TestFailedFlush_E010_Resolved_NoPartialPublication
--- PASS: TestFailedFlush_E010_Resolved_NoPartialPublication (0.00s)
=== RUN   TestFailedFlush_E010_Resolved_WriteConflictDetection
--- PASS: TestFailedFlush_E010_Resolved_WriteConflictDetection (0.01s)
=== RUN   TestFailedFlush_E010_Resolved_ErrorReportingOnClose
--- PASS: TestFailedFlush_E010_Resolved_ErrorReportingOnClose (0.00s)
=== RUN   TestFailedFlush_Interaction_SubsequentQueuedFlushes
--- PASS: TestFailedFlush_Interaction_SubsequentQueuedFlushes (0.01s)
=== RUN   TestFailedFlush_Interaction_ConcurrentReads
--- PASS: TestFailedFlush_Interaction_ConcurrentReads (0.01s)
=== RUN   TestFailedFlush_Interaction_ConcurrentCommits
--- PASS: TestFailedFlush_Interaction_ConcurrentCommits (0.12s)
=== RUN   TestFailedFlush_Interaction_Compaction
--- PASS: TestFailedFlush_Interaction_Compaction (0.06s)
=== RUN   TestFailedFlush_Interaction_RepeatedShutdownCalls
--- PASS: TestFailedFlush_Interaction_RepeatedShutdownCalls (0.01s)
=== RUN   TestFailedFlush_Interaction_MultipleFailedFlushes
--- PASS: TestFailedFlush_Interaction_MultipleFailedFlushes (0.01s)
PASS
```
*All 14 tests pass on Windows native (1.33s) and WSL2 Linux with `-race` (1.66s, 0 data races).*

---

## 8. Full Platform Test Pass & Test Reconciliation

### Complete Unit Test Inventory (93 unit tests across 14 test files):
1. `combined_lifecycle_validation_test.go`: 6 tests
2. `compaction_concurrency_test.go`: 4 tests
3. `compaction_publication_test.go`: 4 tests
4. `cross_tier_conflict_test.go`: 3 tests
5. `cross_tier_ordering_verification_test.go`: 3 tests
6. `engine_test.go`: 5 tests
7. `failed_flush_precedence_test.go`: **14 tests** (Added I1.4.7)
8. `filesystem_cleanup_test.go`: 4 tests
9. `flush_deadlock_repro_test.go`: 5 tests
10. `flush_error_propagation_test.go`: 4 tests
11. `flush_ordering_test.go`: 6 tests
12. `flush_shutdown_test.go`: 9 tests
13. `i1_2_0_invariants_test.go`: 8 tests
14. `memtable_concurrency_test.go`: 10 tests
15. `noncontiguous_compaction_counterexample_test.go`: 4 tests
16. `reader_lifetime_test.go`: 4 tests

**Reconciliation**: Baseline CP1.4 had 79 tests. Sub-phase I1.4.7 added 14 tests. Total unit tests: **$79 + 14 = 93$ unit tests**. (Total runnable tests including 6 benchmarks: **99 tests**).

### Execution Results:
- **Windows 11 Native (NTFS)**: `go test -v ./lsmengine` $\to$ **93/93 PASS (7.99s)**.
- **WSL2 Ubuntu Linux (`go test -race`)**: `wsl ... go test -race -v -count=1 ./lsmengine` $\to$ **93/93 PASS (14.03s)**. Zero data races, zero deadlocks.
- **Static Analysis**: `go vet ./...` $\to$ Clean (exit code 0).

---

## 9. Correctness, Durability, and Recovery Boundaries

As mandated by reviewer instructions, all documentation has been updated to precisely state architectural boundaries:
1. **Point-Read Reference Oracle Scope**: Point-read tests against reference oracles establish correct single-key multi-version selection behavior across tiers; they do not establish complete multi-key transactional snapshot isolation across concurrent long-running transactions.
2. **`INV-HANDOFF-1`**: Codifies continuous key visibility across `m.Mem`, `m.Frozenmem`, and `m.ActiveSSreaders`, explicitly distinguishing version visibility from tombstone masking.
3. **`INV-FLUSH-FAIL-1`**: Codifies fail-stop flush pipeline precedence upon fatal disk errors.
4. **`RISK-001` (Unbounded Queue Memory Growth)**: Remains cataloged as an OPEN risk for CP1.
5. **`RISK-002` (Single-File WAL Recovery)**: Remains cataloged as an OPEN risk for CP1; recovery from rotated WAL files requires future segment discovery.
6. **`RISK-003` (Volatile Retention of Failed Memtables)**: Clarified that failed Memtables remain queryable in RAM during the active process lifetime, but post-crash durability depends on WAL replay.
7. **`RISK-004` (Flush Failure Precedence Inversion)**: Marked **RESOLVED** via Option A Fail-Stop (D-017 / Issue E-010).

---

## 10. Specific Review Questions for ChatGPT / External Reviewer

1. **Fail-Stop Flush Loop Semantics**: Does halting `flushLoop` immediately upon `m.flushErr != nil` and retaining all subsequent flushes in `m.Frozenmem` satisfy the reviewer's safety expectations for Approach A?
2. **Durability and Crash Recovery Boundary**: Is the documented distinction between running-process RAM visibility (`INV-HANDOFF-1`) and post-crash multi-WAL replay (`RISK-002` / `RISK-003`) acceptable for Checkpoint CP1.4a?
3. **Readiness for Sub-phase I1.5**: Are the 14 new deterministic tests, 93 passing race-enabled tests, and updated invariant specifications sufficient to authorize progression to **I1.5 (Reader Snapshot Implementation & Approach B Validation)**?

---

**STOP AT CP1.4a — EXTERNAL REVIEW REQUIRED.**
