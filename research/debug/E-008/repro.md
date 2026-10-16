# Reproduction Report: Issue E-008

**Issue ID**: `E-008`  
**Discovered**: Sub-phase I1.3.6c (Verify flush publication ordering)  
**Severity**: P0 / P1 (Correctness / Ordering Invariant Violation / Stale Read)  
**Status**: REPRODUCED (BLOCKED — Awaiting Reviewer Authorization)  

---

## 1. Symptom & Description

Under concurrent write load, multiple Memtables can exceed the flush threshold (64 MB) in rapid succession, resulting in multiple concurrent background `go m.flushToDisk(frozen)` goroutines.

If an older Memtable flush ($F_1$, e.g. sequence numbers 1–10) completes publication after a newer Memtable flush ($F_2$, e.g. sequence numbers 11–20), $F_1$ is appended after $F_2$ in `m.ActiveSSreaders`:
$$\text{ActiveSSreaders} = [R_2, R_1]$$

Because `Manager.Get` iterates `m.ActiveSSreaders` in reverse order from index $\text{len}-1$ down to 0 under the fundamental invariant `INV-ORDER-1` (assuming newer SSTables are positioned at higher indices):
1. `Manager.Get` visits $R_1$ first.
2. If the user key exists in both $R_1$ and $R_2$, $R_1$'s older version is returned immediately.
3. The newer committed version in $R_2$ is masked.
4. Snapshot isolation and read-your-writes guarantees are broken.

---

## 2. Minimal Deterministic Reproduction

Test: `TestFlushOrdering_ConcurrentFlushesViolateOrder` in [`lsmengine/flush_ordering_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_ordering_test.go).

The test constructs two frozen Memtables:
- $F_1$: `key1` = `"v1_old"` with sequence 10.
- $F_2$: `key1` = `"v2_new"` with sequence 20.

Using `beforeFlushPublishHook`, $F_1$ is delayed immediately prior to `m.Mu.Lock()`, allowing $F_2$ to publish first. Then $F_1$ publishes. A point read `Manager.Get("key1", 100)` is issued.

### Windows Native Output (`go test -v -run TestFlushOrdering_ConcurrentFlushesViolateOrder ./lsmengine`):
```
=== RUN   TestFlushOrdering_ConcurrentFlushesViolateOrder
    flush_ordering_test.go:107: DEFECT E-008 REPRODUCED: Manager.Get returned stale value "v1_old" (expected "v2_new"). Out-of-order flushes corrupted reader chronological order!
--- FAIL: TestFlushOrdering_ConcurrentFlushesViolateOrder (0.01s)
FAIL
FAIL	owndbms/lsmengine	0.942s
FAIL
```

### WSL2 Linux with Race Detector (`/snap/bin/go test -v -race -run TestFlushOrdering_ConcurrentFlushesViolateOrder ./lsmengine`):
```
=== RUN   TestFlushOrdering_ConcurrentFlushesViolateOrder
    flush_ordering_test.go:107: DEFECT E-008 REPRODUCED: Manager.Get returned stale value "v1_old" (expected "v2_new"). Out-of-order flushes corrupted reader chronological order!
--- FAIL: TestFlushOrdering_ConcurrentFlushesViolateOrder (0.03s)
FAIL
FAIL	owndbms/lsmengine	0.055s
FAIL
```

Raw logs preserved at:
- Windows native: [`research/debug/E-008/failing_repro.log`](failing_repro.log)
- WSL2 `-race`: [`research/debug/E-008/wsl_failing_repro.log`](wsl_failing_repro.log)

---

## 3. Root Cause Analysis

1. `checkAndTriggerFlush()` in `lsmengine/manager.go` spawns background flushes via `go m.flushToDisk(frozen)`.
2. There is no synchronization or serialization between concurrent flush workers; Go runtime goroutine preemption, disk I/O differences, or file sizes can cause flushes to finish in arbitrary order.
3. In `flushToDisk()`, publication is performed via unconditional append:
   ```go
   m.ActiveSSreaders = append(m.ActiveSSreaders, reader)
   ```
4. `Manager.Get()` assumes `m.ActiveSSreaders` is strictly ordered by ascending chronological sequence:
   ```go
   for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
       val, err := m.ActiveSSreaders[i].Get(byteKey, readTs)
       ...
   }
   ```
5. An out-of-order append directly inverts chronological precedence, causing silent stale reads in `Manager.Get`.

---

## 4. Architectural Options for Resolution

Per reviewer instructions ("If ordering is violated, preserve the reproduction, create an issue, and STOP before making an architectural correction."):

1. **Option 1 (Serialized Flush Worker / Dedicated Mutex)**:
   - Introduce a dedicated flush mutex (`flushMu sync.Mutex`) or a single background flush worker channel (`chan *Memtable`).
   - Ensures only one Memtable flushes at a time in strict FIFO rotation order.
   - *Advantages*: Simplest, guarantees `INV-ORDER-1` trivially, bounds disk write concurrency, coordinates cleanly with shutdown.
2. **Option 2 (Parallel Build with Ordered Sequence Publication Queue)**:
   - Allow concurrent workers to build SSTables in parallel, but assign each frozen Memtable a monotonically increasing `flushID`.
   - Before appending to `m.ActiveSSreaders`, buffer completed SSTables in a priority queue and publish strictly in `flushID` order.
   - *Advantages*: Allows multi-threaded SSTable construction while preserving slice ordering.
   - *Complexity*: Higher buffering and failure handling logic.
3. **Option 3 (Approach B Reader-Snapshot Architecture preview)**:
   - Level-based or generation-based reader partitioning where SSTables carry explicit sequence number metadata and lookups evaluate greatest sequence numbers rather than slice position.
