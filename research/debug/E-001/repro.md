# Issue E-001 Reproduction & Root Cause Analysis

## Environment
- OS: Windows 11 Home Single Language (Build 26200, 64-bit)
- Go: go1.26.3 windows/amd64
- Base Commit: `08d3189069294e8168ebb5234a3bef1d37f1d649`

## Reproduction Command
```powershell
go test -v ./lsmengine -run TestBasicPutGet -timeout 5s
```

## Symptom
The test hangs indefinitely upon executing line 50: `if err := txn.Commit(); err != nil`.
After 5 seconds, the Go test runner alarm fires and outputs a panic stack trace showing goroutine 21 blocked in `sync.runtime_SemacquireRWMutexR`.

## Exact Call Chain Leading to Deadlock
1. `TestBasicPutGet` calls `txn.Commit()` (`engine_test.go:50`).
2. `Transaction.Commit` executes `t.Man.Mu.Lock()` (`transaction.go:56`).
3. Inside the write conflict check loop, `Transaction.Commit` calls `t.Man.LatestValue([]byte(key))` (`transaction.go:60`).
4. `Manager.LatestValue` executes `m.Mu.RLock()` (`manager.go:120`).
5. Since the calling goroutine already holds an exclusive lock (`Lock()`) on `m.Mu`, attempting to acquire `RLock()` causes a recursive lock acquisition. In Go's `sync.RWMutex`, this immediately blocks waiting for the write lock to be released, producing a self-deadlock.

## Resolution Implemented (Sub-phase I1.1a)
Extracted unexported helper `latestValueLocked(userKey []byte) (uint64, error)` in `lsmengine/manager.go` that traverses `m.Mem` and `m.Frozenmem` assuming the caller already holds `Manager.Mu`.
Public method `Manager.LatestValue` acquires `m.Mu.RLock()`, calls `latestValueLocked`, and unlocks.
`Transaction.Commit` in `lsmengine/transaction.go` now calls `t.Man.latestValueLocked([]byte(key))` directly under its already-held `t.Man.Mu.Lock()`, completely removing recursive mutex acquisition.

## Validation & Evidence
1. **Windows Native (`go test -v ./lsmengine`)**:
   - `TestSkipListLevelDistribution`: PASS (0.00s)
   - `TestBasicPutGet`: PASS (0.05s)
   - `TestDeleteAndTombstone`: PASS (0.01s)
   - `TestWALRecovery`: PASS (0.09s)
   - `TestWriteWriteConflict`: PASS (0.00s)
2. **WSL2 Linux amd64 (`go test -race -v ./lsmengine`)**:
   - All 5 tests PASS with 0 race warnings in 1.459s.
3. **Artifacts**:
   - Failing trace: [`before.log`](before.log)
   - Passing log: [`after.log`](after.log)
   - Code patch: [`diff.patch`](diff.patch)

