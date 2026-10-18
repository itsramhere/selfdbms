# Reproduction & Resolution: Issue E-014

## Defect Summary
- **ID**: `E-014`
- **Component**: `lsmengine/snapshot.go` (`scheduleAsyncCleanupLocked`)
- **Severity**: Data Race / Synchronization Contract Violation (P2)
- **Symptom**: In `scheduleAsyncCleanupLocked()`, the caller holds only `Manager.snapshotMu`. The function checked `if m.snapshotPhase == SnapshotPhaseClosed || m.state == EngineClosed`. Reading `m.state` while holding only `m.snapshotMu` violates the lock hierarchy because `m.state` is protected by `Manager.Mu`. When concurrent threads update `m.state` under `Manager.Mu.Lock()` (such as during shutdown transitions), ThreadSanitizer detects a concurrent read/write data race on `m.state`.

## Minimal Reproduction
```powershell
wsl bash -c "cd /mnt/c/Users/ramst/Documents/Projects/selfdbms && /snap/bin/go test -race -v -run TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup ./lsmengine"
```

## Before Log (`wsl_before.log`)
```text
=== RUN   TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup
==================
WARNING: DATA RACE
Write at 0x00c000105108 by goroutine 10:
  owndbms/lsmengine.TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup.func1()
      /mnt/c/Users/ramst/Documents/Projects/selfdbms/lsmengine/cleanup_coordination_test.go:488 +0xe7

Previous read at 0x00c000105108 by goroutine 8:
  owndbms/lsmengine.(*Manager).scheduleAsyncCleanupLocked()
      /mnt/c/Users/ramst/Documents/Projects/selfdbms/lsmengine/snapshot.go:506 +0x4d
==================
    testing.go:1867: race detected during execution of test
--- FAIL: TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup (0.13s)
FAIL
```

## Root Cause
`m.snapshotPhase` was introduced in Decision D-026 as the dedicated, `snapshotMu`-protected lifecycle enum to eliminate cross-mutex inspections. The check `m.state == EngineClosed` in `scheduleAsyncCleanupLocked` was an unnecessary and unsynchronized secondary inspection of `Manager.Mu`-guarded state.

## Minimal Fix (Decision D-035)
Remove `m.state == EngineClosed` from `scheduleAsyncCleanupLocked`. `m.snapshotPhase == SnapshotPhaseClosed` is guarded strictly by `m.snapshotMu` and represents the canonical, synchronized authority for shutdown completion in snapshot and cleanup helpers.

## After Log (`wsl_after.log`)
```text
=== RUN   TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup
--- PASS: TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup (0.03s)
PASS
ok  	owndbms/lsmengine	1.069s
```
