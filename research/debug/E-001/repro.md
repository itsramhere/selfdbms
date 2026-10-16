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

## Planned Resolution (Phase I1)
In Phase I1, either:
- Create an internal unexported lookup helper `latestValueLocked(userKey []byte)` that does not acquire `m.Mu.RLock()`, or
- Directly query `m.Mem.LatestValue` and `m.Frozenmem` within `Transaction.Commit` where `t.Man.Mu` is already held.
