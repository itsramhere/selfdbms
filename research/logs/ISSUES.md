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
- **Actual Root Cause**: Recursive read lock acquisition while holding exclusive write lock on `m.Mu`.
- **Status**: Open (deferred to Phase I1 per Phase I0 operating rules: no feature changes during baseline audit).
- **Residual Risk**: 3 of 4 unit tests (`TestBasicPutGet`, `TestDeleteAndTombstone`, `TestWALRecovery`) and all write benchmarks are blocked until this deadlock is resolved in Phase I1.
- **Evidence Path**: [`research/debug/E-001/before.log`](../debug/E-001/before.log)

---

### E-002: Missing CGO / C Toolchain for `go test -race` on Windows

- **ID**: `E-002`
- **Detected**: 2026-10-16, commit `08d3189`, Windows 11 amd64
- **Sub-phase**: I0.4
- **Severity**: Environment / Benchmark / Tooling
- **Symptom**: Executing `go test -race` fails immediately with `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- **Minimal Reproduction**:
  ```powershell
  go test -race ./lsmengine
  ```
- **Expected / Actual**:
  - *Expected*: Tests execute with thread sanitizer instrumentation.
  - *Actual*: Tool fails because no C compiler (`gcc` or `clang`) is available in host PATH.
- **Actual Root Cause**: Host Windows environment lacks a C compiler required by Go's CGO runtime for the race detector.
- **Status**: Open (documented environment constraint).
- **Residual Risk**: Automated data race checking via `-race` cannot be executed locally until a C toolchain (e.g. MinGW-w64 or LLVM/Clang) is installed or provided.
