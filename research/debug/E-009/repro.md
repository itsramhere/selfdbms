# Reproduction Report: Issue E-009

**Issue ID**: `E-009`  
**Discovered**: External Review CP1.3b / Sub-phase I1.3.8a (Flush Queue Deadlock and Lifecycle Hardening)  
**Severity**: P0 (Liveness / Deadlock / Engine Stall)  
**Status**: REPRODUCED (BLOCKED — Awaiting Phase I1.3.8b Implementation)  

---

## 1. Symptom & Description

Under write load where flushes are triggered faster than the single background worker can construct SSTable files and write them to disk:
1. `m.flushChan` is initialized with a fixed capacity buffer of 64 tasks (`make(chan *Memtable, 64)`).
2. The background flush worker (`flushLoop`) dequeues $F_0$ and executes `flushToDisk(F_0)`.
3. While $F_0$ is being written to disk, 64 subsequent flushes ($F_1 \dots F_{64}$) are submitted via `EnqueueFlush` or `checkAndTriggerFlush`. Because the channel buffer capacity is 64, all 64 slots fill up.
4. A 65th flush task ($F_{65}$) is submitted. The calling thread acquires `m.Mu.Lock()` and calls `enqueueFlushLocked(F_{65})`.
5. Inside `enqueueFlushLocked`, the producer attempts to send to the channel: `m.flushChan <- mem`. Because the channel buffer is completely full, this send operation blocks synchronously while holding `m.Mu.Lock()`.
6. Meanwhile, the background flush worker completes building the SSTable for $F_0$ and reaches line 396 in `flushToDisk`: `m.Mu.Lock()`, which is required to publish the reader into `m.ActiveSSreaders` and remove $F_0$ from `m.Frozenmem`.
7. **Circular Wait Deadlock**:
   - The flush worker goroutine is blocked waiting for `m.Mu.Lock()` (held by the producer).
   - The producer goroutine holds `m.Mu.Lock()` and is blocked waiting for space in `m.flushChan`.
   - Space in `m.flushChan` can only become available when the worker finishes `flushToDisk(F_0)` and dequeues $F_1$ in `flushLoop`.
   - Neither goroutine can make progress. All subsequent reads, writes, transactions, and shutdown operations attempting to acquire `m.Mu` hang indefinitely.

---

## 2. Minimal Deterministic Reproduction

Test: `TestFlushDeadlock_E009_BoundedQueueDeadlock` in [`lsmengine/flush_deadlock_repro_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_deadlock_repro_test.go).

The test uses `beforeFlushPublishHook` to pause the worker in `flushToDisk(F_0)` before it acquires `m.Mu.Lock()`. It fills `m.flushChan` with 64 items, spawns a producer for $F_{65}$ which acquires `m.Mu.Lock()` and blocks on channel send, and releases $F_0$. Both goroutines enter an unrecoverable circular wait. A watchdog detects the stall, captures the runtime stack traces, and unblocks the channel to allow clean teardown without hanging the test runner.

### Windows Native Output (`go test -v -run TestFlushDeadlock_E009_BoundedQueueDeadlock ./lsmengine`):
```
=== RUN   TestFlushDeadlock_E009_BoundedQueueDeadlock
    flush_deadlock_repro_test.go:145: Goroutine stack traces during deadlock:
        goroutine 8 [running]:
        owndbms/lsmengine.TestFlushDeadlock_E009_BoundedQueueDeadlock(...)
        ...
        goroutine 9 [sync.Mutex.Lock]:
        sync.(*RWMutex).Lock(...)
        owndbms/lsmengine.(*Manager).flushToDisk(0x1052198b83c0, 0x10521984a490)
        	C:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:396 +0x3ef
        owndbms/lsmengine.(*Manager).flushLoop(0x1052198b83c0)
        	C:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:242 +0x75
        
        goroutine 18 [chan send]:
        owndbms/lsmengine.(*Manager).enqueueFlushLocked(...)
        	C:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:248
        owndbms/lsmengine.(*Manager).EnqueueFlush(0x1052198b83c0, 0x1052199204f0)
        	C:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:259 +0x97
    flush_deadlock_repro_test.go:146: DEFECT E-009 REPRODUCED: circular wait deadlock between Manager.Mu and full flushChan (capacity 64)
--- FAIL: TestFlushDeadlock_E009_BoundedQueueDeadlock (0.61s)
FAIL
```

### WSL2 Linux with Race Detector (`/snap/bin/go test -race -v -run TestFlushDeadlock_E009_BoundedQueueDeadlock ./lsmengine`):
```
=== RUN   TestFlushDeadlock_E009_BoundedQueueDeadlock
    flush_deadlock_repro_test.go:145: Goroutine stack traces during deadlock:
        goroutine 21 [sync.Mutex.Lock]:
        sync.(*RWMutex).Lock(...)
        owndbms/lsmengine.(*Manager).flushToDisk(0xc00014a0c0, 0xc000124500)
        	/mnt/c/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:396 +0x86e
        owndbms/lsmengine.(*Manager).flushLoop(0xc00014a0c0)
        	/mnt/c/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:242 +0xc8
        
        goroutine 34 [chan send]:
        owndbms/lsmengine.(*Manager).enqueueFlushLocked(...)
        	/mnt/c/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:248
        owndbms/lsmengine.(*Manager).EnqueueFlush(0xc00014a0c0, 0xc000220530)
        	/mnt/c/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go:259 +0x127
    flush_deadlock_repro_test.go:146: DEFECT E-009 REPRODUCED: circular wait deadlock between Manager.Mu and full flushChan (capacity 64)
--- FAIL: TestFlushDeadlock_E009_BoundedQueueDeadlock (1.14s)
FAIL
```

Raw logs preserved at:
- Windows native: [`research/debug/E-009/before.log`](before.log)
- WSL2 `-race`: [`research/debug/E-009/wsl_before.log`](wsl_before.log)

---

## 3. Call Graph & Deadlock Cycle

```
[Producer Goroutine]                          [Flush Worker Goroutine]
EnqueueFlush(F65) / checkAndTriggerFlush()   flushLoop()
  │                                             │
  ├── m.Mu.Lock()  [HOLDS Manager.Mu]           ├── mem := <-m.flushChan  [Dequeues F0]
  ├── enqueueFlushLocked(F65)                   └── flushToDisk(F0)
  │     └── m.flushChan <- F65                         ├── Build SSTable on disk
  │           │                                        │
  │     [BLOCKED: channel buffer is full (64)]         ├── m.Mu.Lock()
  │     (Holds m.Mu while blocking)                    │     │
  ▼                                                    ▼     ▼
  WAITING FOR CONSUMER TO READ                         WAITING FOR Manager.Mu
  FROM m.flushChan                                     HELD BY PRODUCER
  ────────────────────────► CIRCULAR WAIT ◄─────────────────────────
```

---

## 4. Root Cause Analysis

1. In `lsmengine/manager.go`, `enqueueFlushLocked` performs a synchronous send into a bounded buffered channel (`chan *Memtable`, cap 64):
   ```go
   func (m *Manager) enqueueFlushLocked(mem *Memtable) {
       m.initFlushWorkerLocked()
       m.flushChan <- mem // BLOCKS IF BUFFER FULL
   }
   ```
2. The caller of `enqueueFlushLocked` holds `m.Mu.Lock()` (`EnqueueFlush`, `TriggerFlush`, `checkAndTriggerFlush`).
3. Holding a broad lock (`m.Mu`) while blocking on a synchronization primitive whose consumer also requires that same broad lock violates the fundamental locking hierarchy rule: **never block on an external channel/queue while holding an exclusive lock needed by the consumer**.
4. The buffer size of 64 merely delays the onset of the defect under moderate load, but does not prevent it. Any sustained write burst that generates 65 Memtables before the worker finishes one flush unconditionally deadlocks the engine.

---

## 5. Architectural Options for Resolution (Phase I1.3.8b)

1. **Option 1 (Unbounded FIFO Queue with Mutex/Cond)**:
   - Replace the raw channel send with an internal FIFO slice (`flushQueue []*Memtable`) guarded by a dedicated `flushQueueMu sync.Mutex` and `sync.Cond` (or an unbounded channel handoff pattern).
   - Enqueueing under `m.Mu.Lock()` merely appends to the slice and signals the condition variable (or sends non-blockingly to a staging goroutine). Enqueueing never blocks.
   - The background flush worker dequeues tasks in exact FIFO order, releases `flushQueueMu`, builds the SSTable, and acquires `m.Mu.Lock()` only to publish and remove the Memtable from `m.Frozenmem`.
   - *Advantage*: Completely eliminates blocking sends under `m.Mu.Lock()`. Preserves strict FIFO order and guarantees that `Frozenmem` remains searchable until publication.
2. **Option 2 (Backpressure Without Deadlock / Staging Handoff)**:
   - If memory backpressure is required when `Frozenmem` exceeds a threshold (e.g. max frozen count), backpressure must block callers *outside* `m.Mu.Lock()` (e.g., using a separate semaphore or condition variable before acquiring `m.Mu`).
