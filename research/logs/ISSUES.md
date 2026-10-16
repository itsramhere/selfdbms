# Issue and Anomaly Records (E-###)

This document tracks every bug, failure, unexpected behavior, race condition, leak, or test failure discovered during implementation and debugging.

Format:
`ID | severity | symptom | minimal reproduction | expected/actual | trace/log | root-cause hypothesis | experiments | actual root cause | fix | regression test | status | commit`

---

### E-001: Self-Deadlock on Transaction.Commit via Recursive RLock in Manager.LatestValue

- **ID**: `E-001`
- **Detected**: 2026-10-16, commit `08d3189`, baseline mode, Windows 11 amd64
- **Sub-phase**: I0.4
- **Severity**: Deadlock / Correctness (P0 blocker)
- **Symptom**: Every test or benchmark invoking `Transaction.Commit` hangs indefinitely and times out.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestBasicPutGet -timeout 5s
  ```
- **Expected / Actual**:
  - *Expected*: Transaction successfully commits key-value mutations to WAL and active Memtable.
  - *Actual*: Panics on test timeout with goroutine blocked in `sync.(*RWMutex).RLock()`.
- **Trace / Log**:
  ```
  goroutine 21 [sync.RWMutex.RLock]:
  sync.runtime_SemacquireRWMutexR(...)
  sync.(*RWMutex).RLock(...)
  owndbms/lsmengine.(*Manager).LatestValue(...)
      lsmengine/manager.go:120 +0x73
  owndbms/lsmengine.(*Transaction).Commit(...)
      lsmengine/transaction.go:60 +0xb4
  owndbms/lsmengine.TestBasicPutGet(...)
      lsmengine/engine_test.go:50 +0x216
  ```
- **Hypotheses Considered**:
  1. *Contention with background flush*: Rejected. Test is strictly single-threaded; no flush was triggered.
  2. *Recursive locking on non-reentrant sync.RWMutex*: Confirmed. `Transaction.Commit` acquires `t.Man.Mu.Lock()` on line 56, and then calls `t.Man.LatestValue()` on line 60, which attempts to acquire `m.Mu.RLock()`. In Go, `sync.RWMutex` is not re-entrant; an active writer attempting to acquire `RLock()` blocks indefinitely.
- **Fix**: Extracted internal helper `m.latestValueLocked(userKey)` in `lsmengine/manager.go` which assumes `m.Mu` is already held. Updated `Transaction.Commit` in `lsmengine/transaction.go` to call `latestValueLocked` directly. Public `LatestValue` continues to acquire `m.Mu.RLock()` and delegates to `latestValueLocked`.
- **Regression Test**: `TestBasicPutGet`, `TestDeleteAndTombstone`, `TestWALRecovery`, and newly added `TestWriteWriteConflict` in `lsmengine/engine_test.go`.
- **Validation**: 
  - Windows Native: `go test -v ./lsmengine` (5/5 PASS, 0.97s).
  - WSL2 Linux: `/snap/bin/go test -race -v ./lsmengine` (5/5 PASS, 0 race warnings, 1.46s).
- **Status**: Fixed (Sub-phase I1.1a).
- **Residual Risk**: `CompactAll` publication and file retirement logic remains uncoordinated (scheduled for future sub-phases I1.2/I1.3).
- **Evidence Path**: [`research/debug/E-001/after.log`](../debug/E-001/after.log), [`research/debug/E-001/diff.patch`](../debug/E-001/diff.patch), [`research/debug/E-001/repro.md`](../debug/E-001/repro.md)
- **Commit**: `85775ba`

---

### E-002: Missing CGO / C Toolchain for `go test -race` on Windows

- **ID**: `E-002`
- **Detected**: 2026-10-16, commit `08d3189`, Windows 11 amd64
- **Sub-phase**: I0.4
- **Severity**: Environment / Benchmark / Tooling
- **Symptom**: Executing `go test -race` fails immediately on Windows with `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- **Minimal Reproduction**:
  ```powershell
  go test -race ./lsmengine
  ```
- **Expected / Actual**:
  - *Expected*: Tests execute with thread sanitizer instrumentation.
  - *Actual*: Tool fails because no C compiler (`gcc` or `clang`) is available in host Windows PATH.
- **Actual Root Cause**: Host Windows environment lacks a C compiler required by Go's CGO runtime for the race detector.
- **Resolution**: Resolved via Decision 3. WSL2 Ubuntu Linux environment provides Go 1.27.1 and GCC (`/usr/bin/gcc`), enabling native `-race` execution against the codebase mounted at `/mnt/c/Users/ramst/Documents/Projects/selfdbms` without altering the Windows host toolchain.
- **Validation**: `wsl bash -lc "cd /mnt/c/Users/ramst/Documents/Projects/selfdbms && /snap/bin/go test -race -v ./lsmengine"` executes with 0 race warnings.
- **Status**: Resolved (via WSL2 test execution).
- **Residual Risk**: Automated CI or scripts running purely in PowerShell on native Windows cannot run `-race` unless invoked through WSL2.
- **Commit**: `85775ba`

---

### E-003: Cross-Tier Write Conflict Detection Failure After Memtable Flush

- **ID**: `E-003`
- **Detected**: 2026-10-16, commit `4ca8787`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.1b
- **Severity**: Correctness / Data Integrity (P0 Blocker)
- **Symptom**: When a conflicting transaction's committed version (Put or Delete) is flushed from Memtable to an SSTable, concurrent uncommitted transactions fail to detect the write-write conflict and commit successfully, silently overwriting or resurrecting the key.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestCrossTierWriteConflict
  ```
- **Expected / Actual**:
  - *Expected*: Transaction aborts with `"Write-write conflict detected. Transaction aborted."`.
  - *Actual*: Transaction committed without detecting conflict with SSTable-resident version.
- **Trace / Log**:
  ```
  === RUN   TestCrossTierWriteConflict_FlushedSSTable
      cross_tier_conflict_test.go:72: DEFECT REPRODUCED: T1 committed without detecting conflict with SSTable-resident version!
  --- FAIL: TestCrossTierWriteConflict_FlushedSSTable (0.09s)
  ```
- **Hypotheses Considered**:
  1. *Compaction deduplication error*: Rejected. Compaction was not involved; issue occurs on single flush.
  2. *Missing tier inspection in `latestValueLocked`*: Confirmed. `latestValueLocked` only inspected `m.Mem` and `m.Frozenmem`, returning `ErrNotFound` as soon as the key reached an SSTable.
- **Actual Root Cause**: Conflict detection logic in `latestValueLocked` was not cross-tier; it omitted `m.ActiveSSreaders`.
- **Fix**: Implemented `LatestValue(userKey []byte) (uint64, error)` on `SSTableReader` using index seek `EncodeInternalKey(userKey, math.MaxUint64, true)`. Updated `latestValueLocked` to scan `m.ActiveSSreaders` in reverse order (`len(m.ActiveSSreaders)-1` down to 0) if absent from memory tiers.
- **Regression Test**: `TestCrossTierWriteConflict_FlushedSSTable`, `TestCrossTierWriteConflict_FlushedTombstone`, `TestCrossTierWriteConflict_NonConflicting` in `lsmengine/cross_tier_conflict_test.go`.
- **Validation**:
  - Windows Native: 8/8 tests PASS (1.27s).
  - WSL2 Linux: 8/8 tests PASS under `-race` with 0 race warnings (1.71s, repeated 3x).
- **Status**: Fixed (Sub-phase I1.1b).
- **Residual Risk**: If SSTables are merged by `CompactAll`, `CompactAll`'s publication path must preserve all active tables and not overwrite concurrently flushed tables (addressed in I1.2).
- **Evidence Path**: [`research/debug/E-003/before.log`](../debug/E-003/before.log), [`research/debug/E-003/after.log`](../debug/E-003/after.log), [`research/debug/E-003/diff.patch`](../debug/E-003/diff.patch), [`research/debug/E-003/repro.md`](../debug/E-003/repro.md)
- **Commit**: `4a4264f`

---

### E-004: `latestValueLocked` Fails to Identify Greatest Sequence Number when SSTable Ordering Changes

- **ID**: `E-004`
- **Detected**: 2026-10-16, commit `fbe9d81`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.2.0 (Invariant Verification)
- **Severity**: Correctness / Concurrency Hazard (P1)
- **Symptom**: If `ActiveSSreaders` contains SSTables whose order deviates from strict chronological sequence (e.g., during compaction publication if a compacted table is published out of place, or if multi-level SSTables are placed in arbitrary order), `latestValueLocked` returns the sequence number from the SSTable with the highest slice index instead of the greatest sequence number across all SSTables. A concurrent transaction with $ReadTs$ between the returned sequence number and the true committed sequence number will fail to detect the write-write conflict and commit, silently overwriting committed data.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestI1_2_0_SSTableOrderingAnomaly
  ```
- **Expected / Actual**:
  - *Expected*: Conflict detector returns greatest sequence number (50) regardless of SSTable slice ordering.
  - *Actual*: Conflict detector returns sequence number (10) from the table at the highest slice index (`m.ActiveSSreaders[len-1]`), failing to check older index positions.
- **Trace / Log**:
  ```
  === RUN   TestI1_2_0_SSTableOrderingAnomaly
      i1_2_0_invariants_test.go:341: FAIL: conflict detector returned seq 10 instead of greatest committed sequence 50 when SSTable ordering changed
  --- FAIL: TestI1_2_0_SSTableOrderingAnomaly (0.01s)
  FAIL
  FAIL	owndbms/lsmengine	0.929s
  FAIL
  ```
- **Hypotheses Considered**:
  1. *Early-return optimization in `latestValueLocked` assumes strict chronological ordering*: Confirmed. `latestValueLocked` returns on the first match (`return seq, nil`), which is valid ONLY if `ActiveSSreaders` is guaranteed to be chronologically ordered.
- **Fix**: Implemented Option A approved by reviewer: `latestValueLocked` now iterates across all `ActiveSSreaders` tracking `maxSeq` while preserving RAM monotonic early-returns (INV-MVCC-6).
- **Regression Tests**: `TestI1_2_0_SSTableOrderingAnomaly`, `TestE004_TxnConflict_NewestVersionNotInLastSSTable` in `lsmengine/i1_2_0_invariants_test.go`.
- **Validation**:
  - Windows Native: 16/16 tests PASS (1.26s).
  - WSL2 Linux: 16/16 tests PASS under `-race` with 0 race warnings (1.77s).
- **Status**: FIXED (Option A implemented).
- **Residual Risk**: Point-read `Manager.Get` still requires chronological ordering (`INV-ORDER-1`), which must be preserved during compaction publication in Sub-phase I1.2.2.
- **Evidence Path**: [`research/debug/E-004/repro.md`](../debug/E-004/repro.md), [`research/debug/E-004/before.log`](../debug/E-004/before.log), [`research/debug/E-004/after.log`](../debug/E-004/after.log).
- **Commit**: `5bf95d2`

---

### E-005: Noncontiguous Compaction Input Selection Violates Invariant `INV-ORDER-1`

- **ID**: `E-005`
- **Detected**: 2026-10-16, commit `6527d7f`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.3.0 (Reader Ownership & Ordering Audit)
- **Severity**: Architectural Invariant Hazard (P1)
- **Symptom**: If compaction selects noncontiguous input SSTables (e.g. $[R_0, R_2]$ skipping $R_1$), replacing them with a single merged table in the flat slice `m.ActiveSSreaders` violates chronological ordering `INV-ORDER-1`. As a result, older uncompacted versions in $R_1$ mask newer visible versions from $R_2$ in `Manager.Get`, returning stale data and violating snapshot isolation.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestCounterexample_NoncontiguousCompactionViolatesOrder
  ```
- **Expected / Actual**:
  - *Expected*: Point lookup returns latest visible version `"v2_new"` (seq 30).
  - *Actual*: Point lookup returns stale version `"v1_mid"` (seq 20) because $R_1$ sits after the merged table in the reverse search order.
- **Trace / Log**:
  ```
  === RUN   TestCounterexample_NoncontiguousCompactionViolatesOrder
      noncontiguous_compaction_counterexample_test.go:85: Result of Manager.Get after noncontiguous compaction: "v1_mid"
      noncontiguous_compaction_counterexample_test.go:88: COUNTEREXAMPLE PROVEN: Noncontiguous compaction resulted in stale read "v1_mid" (expected "v2_new"). r1 (seq 20) masked newer compacted version mCompact (seq 30) because mCompact was placed before r1 in the flat slice!
  --- FAIL: TestCounterexample_NoncontiguousCompactionViolatesOrder (0.02s)
  FAIL
  ```
- **Root Cause**: In a single flat slice architecture where `Manager.Get` iterates backwards, replacing noncontiguous inputs by inserting the merged table at the first input's position places the output before unselected intermediate tables, inverting relative version order.
- **Proposed Fix (Awaiting Authorization)**: Formally codify invariant `INV-COMPACT-1` (compaction inputs in a flat slice must be contiguous), and enforce in `replaceCompactedReaders` that `inputs` form a contiguous sub-slice of `current`.
- **Status**: REPRODUCED / COUNTEREXAMPLE VERIFIED / AWAITING AUTHORIZATION BEFORE OUT-OF-SCOPE REPAIR.
- **Evidence Path**: [`research/debug/E-005/repro.md`](../debug/E-005/repro.md), [`research/debug/E-005/failing_counterexample.log`](../debug/E-005/failing_counterexample.log), [`lsmengine/noncontiguous_compaction_counterexample_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/noncontiguous_compaction_counterexample_test.go).
- **Commit**: Pending authorization



