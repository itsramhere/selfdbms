# Reproduction Report: Issue E-007

**Issue ID**: `E-007`  
**Discovered**: Sub-phase I1.3.6b (Audit background flush versus Manager.Close)  
**Severity**: P1 (Lifecycle / Resource Leak / Shutdown Invariant)  
**Status**: REPRODUCED (BLOCKED — Awaiting Reviewer Authorization)  

---

## 1. Symptom & Description

When a background Memtable flush (`flushToDisk`) is in-flight while `Manager.Close()` is invoked:
1. `Manager.Close()` acquires `m.compactionMu.Lock()` and `m.Mu.Lock()`, sets `m.isClosed = true`, closes all currently active readers in `m.ActiveSSreaders`, sets `m.ActiveSSreaders = nil`, closes `m.Log.File`, and returns successfully.
2. The in-flight `flushToDisk` completes building its SSTable file, opens an `*SSTableReader`, and acquires `m.Mu.Lock()`.
3. `flushToDisk` does not check `m.isClosed`. It executes `m.ActiveSSreaders = append(m.ActiveSSreaders, reader)`.
4. As a result, a newly opened `SSTableReader` is published into a closed `Manager`.
5. Because `Manager.Close()` has already returned, this new reader is never closed. On Windows NTFS, the underlying file handle remains open, preventing file deletion or clean directory removal (`Access is denied` / file in use error).
6. Furthermore, `Manager.Close()` does not wait for in-flight flushes to complete; committed data in `Frozenmem` can be lost if the process exits before the background goroutine writes to disk.

---

## 2. Minimal Deterministic Reproduction

Test: `TestFlushShutdown_InFlightFlushPublishesAfterClose` in [`lsmengine/flush_shutdown_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_shutdown_test.go).

Using a deterministic hook (`beforeFlushPublishHook`), the test pauses `flushToDisk` immediately after creating the reader file and before acquiring `m.Mu.Lock()`. The test thread invokes `man.Close()`, verifies that `isClosed == true` and `len(ActiveSSreaders) == 0`. Then the hook allows `flushToDisk` to proceed.

### Windows Native Output (`go test -v -run TestFlushShutdown_InFlightFlushPublishesAfterClose ./lsmengine`):
```
=== RUN   TestFlushShutdown_InFlightFlushPublishesAfterClose
    flush_shutdown_test.go:83: DEFECT E-007 REPRODUCED: In-flight flush published 1 reader(s) into closed Manager! Leaked reader: data_1792164148154987600.sst
--- FAIL: TestFlushShutdown_InFlightFlushPublishesAfterClose (0.01s)
FAIL
FAIL	owndbms/lsmengine	1.094s
FAIL
```

### WSL2 Linux with Race Detector (`/snap/bin/go test -v -race -run TestFlushShutdown_InFlightFlushPublishesAfterClose ./lsmengine`):
```
=== RUN   TestFlushShutdown_InFlightFlushPublishesAfterClose
    flush_shutdown_test.go:83: DEFECT E-007 REPRODUCED: In-flight flush published 1 reader(s) into closed Manager! Leaked reader: data_1792164184729197924.sst
--- FAIL: TestFlushShutdown_InFlightFlushPublishesAfterClose (0.02s)
FAIL
FAIL	owndbms/lsmengine	0.044s
FAIL
```

Raw logs preserved at:
- Windows native: [`research/debug/E-007/failing_repro.log`](failing_repro.log)
- WSL2 `-race`: [`research/debug/E-007/wsl_failing_repro.log`](wsl_failing_repro.log)

---

## 3. Call Graph & Execution Trace

```
checkAndTriggerFlush()
  ├── m.Mu.Lock()
  ├── frozen := m.Mem; m.Frozenmem = append(...)
  ├── Reset m.Mem, Rotate m.Log
  ├── m.Mu.Unlock()
  └── go m.flushToDisk(frozen)
         ├── os.Create("data_<ts>.sst")
         ├── SSTableBuilder.Add(...)
         ├── SSTableBuilder.Close()
         ├── reader := NewSSTableReader(...)
         │
         │   [INTERLEAVING: Manager.Close() executes]
         │   Manager.Close()
         │     ├── m.compactionMu.Lock()
         │     ├── m.Mu.Lock()
         │     ├── m.isClosed = true
         │     ├── for r in ActiveSSreaders: r.Close()
         │     ├── m.ActiveSSreaders = nil
         │     ├── m.Log.File.Close()
         │     ├── m.Mu.Unlock()
         │     └── m.compactionMu.Unlock() -> returns nil
         │
         ├── m.Mu.Lock()
         ├── [BUG: No check for m.isClosed!]
         ├── m.ActiveSSreaders = append(m.ActiveSSreaders, reader)  <-- PUBLISHED INTO CLOSED ENGINE
         └── m.Mu.Unlock()
```

---

## 4. Architectural Analysis & Proposed Options

Per reviewer instructions ("If the repair requires larger lifecycle changes, STOP and request authorization before implementing them. Do not assume that acquiring `compactionMu` everywhere is automatically the correct solution."):

1. **Option A (Defensive Reader Drop on Closed Engine)**:
   - In `flushToDisk()`, after acquiring `m.Mu.Lock()`, check `if m.isClosed { reader.Close(); os.Remove(filename); return }`.
   - *Limitation*: While this eliminates the leaked file descriptor and prevents corrupting `m.ActiveSSreaders`, it discards the committed frozen memtable data without persistence if `Close()` ran before flush completed.
2. **Option B (Flush Drain on Shutdown via `sync.WaitGroup`)**:
   - Add a `flushWg sync.WaitGroup` to `Manager`.
   - `checkAndTriggerFlush` increments `flushWg.Add(1)` before spawning `flushToDisk`, and `flushToDisk` calls `defer flushWg.Done()`.
   - In `Manager.Close()`, before closing readers, wait for in-flight flushes to complete (`m.flushWg.Wait()`).
   - Prevent new transactions or flushes from starting after `Close()` is initiated.
   - *Advantage*: Guarantees zero data loss for committed frozen memtables, ensures all SSTables are fully flushed and published before `Close()` tears down readers, and cleanly closes all readers without handle leaks.
