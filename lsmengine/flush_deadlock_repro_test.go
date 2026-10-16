package lsmengine

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFlushDeadlock_E009_BoundedQueueDeadlock reproduces Issue E-009:
// A circular wait deadlock between Manager.Mu and bounded flushChan (capacity 64).
//
// Interleaving:
// 1. Worker begins processing F0 and pauses at beforeFlushPublishHook (before acquiring Manager.Mu).
// 2. 64 tasks (F1..F64) fill the bounded flushChan buffer to 100% capacity.
// 3. Producer invokes EnqueueFlush(F65), acquires Manager.Mu.Lock(), and blocks on m.flushChan <- mem.
// 4. Worker resumes from hook and attempts to acquire Manager.Mu.Lock() to publish F0.
// 5. DEADLOCK:
//    - Worker waits for Manager.Mu.Lock() (held by producer).
//    - Producer holds Manager.Mu.Lock() and waits for worker to dequeue an item from flushChan.
// 6. Test detects the deadlock via watchdog timeout, captures full goroutine stack traces,
//    and frees one item from flushChan to allow clean teardown without hanging the test suite.
func TestFlushDeadlock_E009_BoundedQueueDeadlock(t *testing.T) {
	dir := "test_data_flush_deadlock_repro"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	f0AtHook := make(chan struct{})
	f0Release := make(chan struct{})

	var hookOnce sync.Once
	man.beforeFlushPublishHook = func() {
		hookOnce.Do(func() {
			close(f0AtHook)
			<-f0Release
		})
	}

	// Initialize worker and enqueue F0
	f0 := newTestMemtable()
	k0 := EncodeInternalKey([]byte("key0"), 100, false)
	f0.Put(k0, []byte("val0"))
	man.Frozenmem = append(man.Frozenmem, f0)
	man.EnqueueFlush(f0)

	// Wait until worker picks up F0 and reaches the publication hook
	select {
	case <-f0AtHook:
	case <-time.After(5 * time.Second):
		t.Fatalf("Timeout waiting for F0 to reach beforeFlushPublishHook")
	}

	// Now worker is paused holding no locks, and flushChan is empty (capacity 64).
	// Fill flushChan with exactly 64 items (F1..F64).
	for i := 1; i <= 64; i++ {
		fi := newTestMemtable()
		ki := EncodeInternalKey([]byte(fmt.Sprintf("key%d", i)), uint64(100+i), false)
		fi.Put(ki, []byte("val"))
		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, fi)
		man.enqueueFlushLocked(fi)
		man.Mu.Unlock()
	}

	if len(man.flushChan) != 64 {
		t.Fatalf("Expected flushChan to be full (len=64), got %d", len(man.flushChan))
	}

	// Launch producer goroutine for F65.
	// It will acquire man.Mu.Lock() and block on `m.flushChan <- mem`.
	producerStarted := make(chan struct{})
	producerFinished := make(chan struct{})
	f65 := newTestMemtable()
	k65 := EncodeInternalKey([]byte("key65"), 200, false)
	f65.Put(k65, []byte("val65"))

	go func() {
		close(producerStarted)
		man.EnqueueFlush(f65)
		close(producerFinished)
	}()

	<-producerStarted

	// Verify producer holds man.Mu by checking if TryLock fails
	muHeld := false
	for attempt := 0; attempt < 50; attempt++ {
		time.Sleep(5 * time.Millisecond)
		if !man.Mu.TryLock() {
			muHeld = true
			break
		}
		man.Mu.Unlock()
	}
	if !muHeld {
		t.Fatalf("Producer failed to acquire man.Mu.Lock()")
	}

	// Release F0 in worker. Worker will now execute m.Mu.Lock() at manager.go:396.
	close(f0Release)

	// Deadlock detection watchdog
	deadlockDetected := false
	select {
	case <-producerFinished:
		t.Fatalf("Producer unexpectedly finished; no deadlock occurred")
	case <-time.After(300 * time.Millisecond):
		deadlockDetected = true
	}

	// Capture all goroutine stacks
	buf := make([]byte, 64*1024)
	n := runtime.Stack(buf, true)
	stackDump := string(buf[:n])

	// Verify stack dump contains both blocked goroutines
	hasProducerBlocked := strings.Contains(stackDump, "enqueueFlushLocked") || strings.Contains(stackDump, "EnqueueFlush")
	hasWorkerBlocked := strings.Contains(stackDump, "flushToDisk")

	// Free one slot in flushChan so producer unblocks and releases man.Mu,
	// allowing worker to proceed and preventing test suite hang.
	<-man.flushChan

	select {
	case <-producerFinished:
		// Producer cleanly finished
	case <-time.After(2 * time.Second):
		t.Errorf("Producer failed to unblock after channel drain")
	}

	// Clean up
	man.Close()

	if deadlockDetected && hasProducerBlocked && hasWorkerBlocked {
		t.Logf("Goroutine stack traces during deadlock:\n%s", stackDump)
		t.Fatalf("DEFECT E-009 REPRODUCED: circular wait deadlock between Manager.Mu and full flushChan (capacity 64)")
	} else if deadlockDetected {
		t.Logf("Stack dump:\n%s", stackDump)
		t.Fatalf("Deadlock detected but stacks did not match expected signatures")
	}
}
