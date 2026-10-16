# CP1.3a Review Packet for External Reviewer (ChatGPT)

**Phase:** Phase I1 (Approach A Baseline — Reader-Compactor Synchronization)  
**Milestone:** Checkpoint CP1.3a (Defensive Publication and Shutdown Verification)  
**Date:** 2026-10-16  
**Git Branch:** `research/reader-compactor-sync`  
**Latest Commit:** `d30f658` (`I1.3.6: Validate compacted output, audit flush vs shutdown (E-007) and flush ordering (E-008), deliver Checkpoint CP1.3a`)  
**Operating Systems Tested:** Windows 11 amd64 (NTFS) & WSL2 Ubuntu (Linux 5.15 amd64)  
**Go Versions:** `go1.26.3 windows/amd64` (Windows host) & `go1.27.1 linux/amd64` (WSL2 snap)  
**Current Status:** **BLOCKED — AWAITING REVIEWER DECISION ON ISSUES E-007 AND E-008**  

---

## 1. Executive Summary & Review Request

In accordance with the conditional approval instructions for CP1.3, we executed Sub-phase **I1.3.6 (Defensive Publication and Shutdown Verification)**.

### Primary Audit Findings:
1. **Compacted Output Validation (I1.3.6a — Implemented & Verified)**:
   - Fixed silent SSTable deletion vulnerability in `replaceCompactedReaders`: replaced conditional append with strict rejection of `newReader == nil` with error `"compaction aborted: nil replacement reader"`.
   - Confirmed empty-output compaction (all tombstones) produces a valid 8-byte SSTable file (magic number `0x4C534D31`, `len(Index) == 0`) which returns `ErrNotFound` on point lookups.
   - Added 4 deterministic regression tests in [`lsmengine/compaction_publication_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction_publication_test.go).
2. **Defect E-007 (I1.3.6b — In-Flight Flush Publishes After Manager.Close)**:
   - Proved that `flushToDisk()` does not check `m.isClosed` under `m.Mu.Lock()`.
   - If an in-flight flush finishes after `Manager.Close()` sets `ActiveSSreaders = nil` and `isClosed = true`, the newly opened reader is published into `m.ActiveSSreaders`.
   - The reader file descriptor is leaked indefinitely, causing Windows NTFS file locking violations.
   - Deterministic reproduction test created and verified in [`lsmengine/flush_shutdown_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_shutdown_test.go).
3. **Defect E-008 (I1.3.6c — Concurrent Out-of-Order Flushes Violate Reader Order)**:
   - Proved that when multiple Memtables flush concurrently, an older flush ($F_1$, seq 10) can complete publication after a newer flush ($F_2$, seq 20), resulting in `ActiveSSreaders = [R2, R1]`.
   - Because `Manager.Get` iterates backwards from `len-1` down to 0, it visits $R_1$ first and returns stale data (`"v1_old"`), masking newer committed data (`"v2_new"`).
   - This directly breaks `INV-ORDER-1` and snapshot isolation.
   - Deterministic reproduction test created and verified in [`lsmengine/flush_ordering_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_ordering_test.go).
4. **Correction of Research Documentation Claims (I1.3.6d — Completed)**:
   - Flat-slice compaction limitation (`INV-COMPACT-1`) qualified as specific to this engine's first-matching lookup; universal impossibility claims removed.
   - Lock hierarchy scope qualified: prevents lock inversion across audited paths but does not mathematically eliminate all deadlocks.
   - Approach A latency claims qualified: blocking is mechanically verified, but quantitative degradation is deferred to I1.4 benchmarks.
   - Lifecycle diagram clarified: point reads drain before exclusive publication lock is granted.
   - CP1.3 review packet commit hash updated to `40c9a3d`.
   - Dual-toolchain environment documented (Go 1.26.3 vs Go 1.27.1).

**Per explicit reviewer instructions, execution is STOPPED at Checkpoint CP1.3a to obtain authorization before implementing architectural corrections for E-007 and E-008.**

---

## 2. Issue E-007 Reproduction & Call Trace

### Problem Statement
An in-flight background flush goroutine (`go m.flushToDisk(frozen)`) can acquire `m.Mu.Lock()` after `Manager.Close()` has completed. It appends its newly built reader to `m.ActiveSSreaders`, which was set to `nil` by `Close()`. The reader is never closed, leaking an open OS file descriptor on Windows NTFS.

### Call Trace
```
checkAndTriggerFlush()
  └── go m.flushToDisk(frozen)
         ├── Builder.Add(...)
         ├── Builder.Close()
         ├── reader := NewSSTableReader(filename)
         │
         │   [INTERLEAVING: Manager.Close() completes]
         │   Manager.Close()
         │     ├── m.compactionMu.Lock()
         │     ├── m.Mu.Lock()
         │     ├── m.isClosed = true
         │     ├── for r in ActiveSSreaders: r.Close()
         │     ├── m.ActiveSSreaders = nil
         │     ├── m.Log.File.Close()
         │     └── m.Mu.Unlock() -> returns nil
         │
         ├── m.Mu.Lock()
         ├── [DEFECT: No check for m.isClosed!]
         ├── m.ActiveSSreaders = append(m.ActiveSSreaders, reader)  <-- PUBLISHED TO CLOSED ENGINE
         └── m.Mu.Unlock()
```

### Raw Failure Logs
- **Windows Native (`go test -v -run TestFlushShutdown_InFlightFlushPublishesAfterClose ./lsmengine`)**:
  ```text
  === RUN   TestFlushShutdown_InFlightFlushPublishesAfterClose
      flush_shutdown_test.go:83: DEFECT E-007 REPRODUCED: In-flight flush published 1 reader(s) into closed Manager! Leaked reader: data_1792164148154987600.sst
  --- FAIL: TestFlushShutdown_InFlightFlushPublishesAfterClose (0.01s)
  FAIL
  FAIL	owndbms/lsmengine	1.094s
  FAIL
  ```
- **WSL2 Linux `-race` (`/snap/bin/go test -v -race -run TestFlushShutdown_InFlightFlushPublishesAfterClose ./lsmengine`)**:
  ```text
  === RUN   TestFlushShutdown_InFlightFlushPublishesAfterClose
      flush_shutdown_test.go:83: DEFECT E-007 REPRODUCED: In-flight flush published 1 reader(s) into closed Manager! Leaked reader: data_1792164184729197924.sst
  --- FAIL: TestFlushShutdown_InFlightFlushPublishesAfterClose (0.02s)
  FAIL
  FAIL	owndbms/lsmengine	0.044s
  FAIL
  ```

---

## 3. Issue E-008 Reproduction & Chronological Order Violation

### Problem Statement
`checkAndTriggerFlush()` spawns `go m.flushToDisk(frozen)` without serialization. If two flushes run concurrently and newer flush $F_2$ completes before older flush $F_1$, $F_1$ is appended to `m.ActiveSSreaders` after $F_2$. When `Manager.Get` executes a point lookup, its backward iteration encounters $F_1$ first and returns stale data.

### Call Trace & Search Inversion
```
Memtable F1 (seq 10, key1="v1_old")  <-- Logically older
Memtable F2 (seq 20, key1="v2_new")  <-- Logically newer

Flush F1 and Flush F2 execute concurrently.
F2 finishes first: ActiveSSreaders = [R2]
F1 finishes second: ActiveSSreaders = [R2, R1]

Reader query: Manager.Get("key1", 100)
  for i := len(ActiveSSreaders) - 1; i >= 0; i--:
    Index 1 (R1): Contains key1 at seq 10 <= 100 -> RETURNS "v1_old"!
    Index 0 (R2): Never reached! "v2_new" is masked!
```

### Raw Failure Logs
- **Windows Native (`go test -v -run TestFlushOrdering_ConcurrentFlushesViolateOrder ./lsmengine`)**:
  ```text
  === RUN   TestFlushOrdering_ConcurrentFlushesViolateOrder
      flush_ordering_test.go:107: DEFECT E-008 REPRODUCED: Manager.Get returned stale value "v1_old" (expected "v2_new"). Out-of-order flushes corrupted reader chronological order!
  --- FAIL: TestFlushOrdering_ConcurrentFlushesViolateOrder (0.01s)
  FAIL
  FAIL	owndbms/lsmengine	0.942s
  FAIL
  ```
- **WSL2 Linux `-race` (`/snap/bin/go test -v -race -run TestFlushOrdering_ConcurrentFlushesViolateOrder ./lsmengine`)**:
  ```text
  === RUN   TestFlushOrdering_ConcurrentFlushesViolateOrder
      flush_ordering_test.go:107: DEFECT E-008 REPRODUCED: Manager.Get returned stale value "v1_old" (expected "v2_new"). Out-of-order flushes corrupted reader chronological order!
  --- FAIL: TestFlushOrdering_ConcurrentFlushesViolateOrder (0.03s)
  FAIL
  FAIL	owndbms/lsmengine	0.055s
  FAIL
  ```

---

## 4. Code Changes in Sub-phase I1.3.6

### 4.1 Strict Rejection of Nil Replacement Reader (`lsmengine/compaction.go`)
```diff
@@ -20,6 +20,10 @@ func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader,
 		return current, nil
 	}
 
+	if newReader == nil {
+		return nil, errors.New("compaction aborted: nil replacement reader")
+	}
+
 	// 1. Ensure each input reader appears exactly once (no duplicates) and is non-nil
 	inputSet := make(map[*SSTableReader]bool, len(inputs))
@@ -57,9 +57,7 @@ func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader,
 	// 5. Construct updated reader set:
 	// New reader is published at index 0, followed by all concurrently flushed readers.
 	updated := make([]*SSTableReader, 0, len(current)-len(inputs)+1)
-	if newReader != nil {
-		updated = append(updated, newReader)
-	}
+	updated = append(updated, newReader)
 	updated = append(updated, current[len(inputs):]...)
 
 	return updated, nil
```

### 4.2 Test Hook for Deterministic Flush Interleavings (`lsmengine/manager.go`)
```diff
@@ -25,6 +25,7 @@ type Manager struct {
 	beforeCompactionPublishHook func()
 	duringCompactionMergeHook   func()
 	duringPointReadHook         func()
+	beforeFlushPublishHook      func()
 }
@@ -252,6 +252,10 @@ func (m *Manager) flushToDisk(mem *Memtable) {
 		return
 	}
 
+	if m.beforeFlushPublishHook != nil {
+		m.beforeFlushPublishHook()
+	}
+
 	// Swap the newly built SSTable into the read path and remove the frozen memtable
 	m.Mu.Lock()
 	defer m.Mu.Unlock()
```

---

## 5. Test Suite Verification & Raw Evidence

### Comprehensive Test Matrix (36 Passing Tests)
| Test Suite | File | Tests | Windows Native | WSL2 `-race` |
| :--- | :--- | :---: | :---: | :---: |
| Compaction Publication | `compaction_publication_test.go` | 4 | PASS (0.05s) | PASS (0.21s) |
| Compaction Concurrency | `compaction_concurrency_test.go` | 4 | PASS (0.13s) | PASS (0.35s) |
| Noncontiguous Ordering | `noncontiguous_compaction_counterexample_test.go` | 4 | PASS (0.15s) | PASS (0.25s) |
| Reader Lifetime Cases A–D | `reader_lifetime_test.go` | 4 | PASS (0.26s) | PASS (0.38s) |
| Filesystem & Windows NTFS | `filesystem_cleanup_test.go` | 4 | PASS (0.10s) | PASS (0.19s) |
| Option A Conflict & Ordering | `i1_2_0_invariants_test.go` | 8 | PASS (0.22s) | PASS (0.31s) |
| Cross-Tier Conflict Detection | `cross_tier_conflict_test.go` | 3 | PASS (0.11s) | PASS (0.12s) |
| Engine Core & Recovery | `engine_test.go` | 5 | PASS (0.10s) | PASS (0.29s) |
| **Total Passing Tests** | | **36** | **PASS (2.01s)** | **PASS (3.18s, 0 races)** |

`go vet ./...`: Clean (Exit Code 0).

---

## 6. Architectural Options for Reviewer Authorization

### For Issue E-007 (Flush vs Shutdown):
- **Option 7A (Defensive Reader Drop)**:
  Under `m.Mu.Lock()`, check `if m.isClosed { reader.Close(); os.Remove(filename); return }`.
  *Trade-off*: Prevents handle leaks, but committed data in `Frozenmem` is discarded without reaching disk if `Close()` ran before flush completed.
- **Option 7B (Coordinated Flush Drain via `sync.WaitGroup`) [RECOMMENDED]**:
  Add `m.flushWg sync.WaitGroup`. `checkAndTriggerFlush` calls `m.flushWg.Add(1)`; `Manager.Close()` waits for all in-flight flushes to complete (`m.flushWg.Wait()`) before closing readers and WAL.
  *Advantage*: Guarantees zero data loss for committed frozen Memtables and clean teardown without leaks.

### For Issue E-008 (Concurrent Flush Ordering):
- **Option 8A (Serialized Flush Worker / Dedicated Mutex) [RECOMMENDED]**:
  Introduce `m.flushMu sync.Mutex` or a single background flush worker channel. Flushes are executed strictly in FIFO sequence.
  *Advantage*: Guarantees `INV-ORDER-1` trivially, bounds disk write concurrency, simple and robust.
- **Option 8B (Parallel Build with Sequence-Ordered Publication Queue)**:
  Allow concurrent background SSTable construction, but buffer and insert completed readers into `m.ActiveSSreaders` sorted by Memtable creation sequence.
  *Complexity*: Higher buffering and failure handling logic.
- **Option 8C (Approach B Preview — Explicit Generation/Level Ordering)**:
  Defer multi-flush ordering to Approach B where tables carry explicit sequence metadata rather than relying on slice index.

---

## 7. Reviewer Decision Checklist

Please indicate authorization for:
1. Approval of Checkpoint CP1.3a audit findings.
2. Authorization to repair Issue E-007 (recommend Option 7B: `flushWg` flush drain on shutdown).
3. Authorization to repair Issue E-008 (recommend Option 8A: serialized flush worker via `flushMu`).
4. Authorization to proceed to Sub-phase I1.4 (Empirical Benchmarking of Approach A) once E-007 and E-008 are repaired and verified.

---

EXTERNAL REVIEW REQUIRED — CP1.3a AWAITING APPROVAL.
