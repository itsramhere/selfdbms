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
- **Commit**: Pending I1.1a commit

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
- **Commit**: Pending I1.1a commit
