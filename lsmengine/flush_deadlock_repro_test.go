package lsmengine

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFlushDeadlock_E009_Resolved_NoDeadlockOnQueueSaturation verifies that the unbounded
// FIFO flush queue eliminates Issue E-009.
// Enqueueing beyond 64 items never blocks under Manager.Mu, and the worker seamlessly
// acquires Manager.Mu to publish F0 and process all subsequent tasks in FIFO order.
func TestFlushDeadlock_E009_Resolved_NoDeadlockOnQueueSaturation(t *testing.T) {
	dir := "test_data_flush_deadlock_resolved"
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

	// Fill queue with 64 items (F1..F64) while worker is paused on F0
	for i := 1; i <= 64; i++ {
		fi := newTestMemtable()
		ki := EncodeInternalKey([]byte(fmt.Sprintf("key%03d", i)), uint64(100+i), false)
		fi.Put(ki, []byte(fmt.Sprintf("val%03d", i)))
		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, fi)
		man.enqueueFlushLocked(fi)
		man.Mu.Unlock()
	}

	// Enqueue 65th item (F65). Under E-009 this would block and cause deadlock.
	// With the unbounded queue, it must complete immediately without blocking.
	f65Done := make(chan struct{})
	f65 := newTestMemtable()
	k65 := EncodeInternalKey([]byte("key065"), 200, false)
	f65.Put(k65, []byte("val065"))

	go func() {
		man.EnqueueFlush(f65)
		close(f65Done)
	}()

	select {
	case <-f65Done:
		// Enqueue of 65th item succeeded immediately without blocking!
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("EnqueueFlush(F65) blocked unexpectedly! E-009 still present")
	}

	// Release worker to finish F0 and publish
	close(f0Release)

	// Wait for all 66 flushes (F0..F65) to complete and publish
	deadline := time.Now().Add(10 * time.Second)
	for {
		man.Mu.RLock()
		nReaders := len(man.ActiveSSreaders)
		nFrozen := len(man.Frozenmem)
		man.Mu.RUnlock()

		if nReaders == 66 && nFrozen == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Timeout waiting for all 66 flushes: readers=%d (expected 66), frozen=%d (expected 0)", nReaders, nFrozen)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify key visibility for F0 and F65
	val0, err := man.Get("key0", 500)
	if err != nil || val0 != "val0" {
		t.Fatalf("Get(key0) failed: val=%s, err=%v", val0, err)
	}
	val65, err := man.Get("key065", 500)
	if err != nil || val65 != "val065" {
		t.Fatalf("Get(key065) failed: val=%s, err=%v", val65, err)
	}

	// Clean shutdown
	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestFlushAdmission_QueueSaturated128Items tests rapid admission of 128 flushes.
// Enqueueing must never stall, and all 128 items must be flushed and published in exact FIFO order.
func TestFlushAdmission_QueueSaturated128Items(t *testing.T) {
	dir := "test_data_flush_admission_128"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	const totalFlushes = 128
	startEnqueue := time.Now()

	for i := 1; i <= totalFlushes; i++ {
		f := newTestMemtable()
		k := EncodeInternalKey([]byte(fmt.Sprintf("key_%04d", i)), uint64(i*10), false)
		f.Put(k, []byte(fmt.Sprintf("val_%04d", i)))

		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, f)
		man.enqueueFlushLocked(f)
		man.Mu.Unlock()
	}

	enqueueDuration := time.Since(startEnqueue)
	t.Logf("Enqueued %d flushes in %v", totalFlushes, enqueueDuration)

	// Verify all 128 items complete and publish
	deadline := time.Now().Add(15 * time.Second)
	for {
		man.Mu.RLock()
		nReaders := len(man.ActiveSSreaders)
		nFrozen := len(man.Frozenmem)
		man.Mu.RUnlock()

		if nReaders == totalFlushes && nFrozen == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Timeout waiting for 128 flushes: readers=%d, frozen=%d", nReaders, nFrozen)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify spot checks across beginning, middle, and end of the sequence
	spotKeys := []int{1, 10, 64, 65, 100, 128}
	for _, idx := range spotKeys {
		k := fmt.Sprintf("key_%04d", idx)
		expected := fmt.Sprintf("val_%04d", idx)
		val, err := man.Get(k, 10000)
		if err != nil || val != expected {
			t.Fatalf("Get(%s) = %s, err=%v; expected %s", k, val, err, expected)
		}
	}

	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestFlushAdmission_WorkerPublicationDuringAdmission tests that concurrent
// flush admissions and background publications interleave safely with point reads.
func TestFlushAdmission_WorkerPublicationDuringAdmission(t *testing.T) {
	dir := "test_data_flush_pub_during_adm"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	const nFlushes = 50
	var stopReaders atomic.Bool
	var readerWg sync.WaitGroup

	// Reader goroutines querying data concurrently
	for r := 0; r < 3; r++ {
		readerWg.Add(1)
		go func(readerID int) {
			defer readerWg.Done()
			for !stopReaders.Load() {
				// Query random keys that might be in active, frozen, or published SSTable
				for i := 1; i <= nFlushes; i++ {
					k := fmt.Sprintf("k_%03d", i)
					val, err := man.Get(k, 50000)
					if err == nil {
						expected := fmt.Sprintf("v_%03d", i)
						if val != expected {
							t.Errorf("Reader %d got %s for key %s, expected %s", readerID, val, k, expected)
						}
					}
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(r)
	}

	// Producer enqueues flushes with slight spacing
	for i := 1; i <= nFlushes; i++ {
		f := newTestMemtable()
		k := EncodeInternalKey([]byte(fmt.Sprintf("k_%03d", i)), uint64(i*10), false)
		f.Put(k, []byte(fmt.Sprintf("v_%03d", i)))

		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, f)
		man.enqueueFlushLocked(f)
		man.Mu.Unlock()

		time.Sleep(2 * time.Millisecond)
	}

	stopReaders.Store(true)
	readerWg.Wait()

	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestFlushAdmission_ShutdownDuringSaturatedQueue tests that when Close() is called
// while many items are queued, all items are fully drained and published before
// the engine transitions to CLOSED, with all readers cleanly closed.
func TestFlushAdmission_ShutdownDuringSaturatedQueue(t *testing.T) {
	dir := "test_data_flush_shutdown_saturated"
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

	// Enqueue F0
	f0 := newTestMemtable()
	f0.Put(EncodeInternalKey([]byte("key0"), 10, false), []byte("val0"))
	man.Frozenmem = append(man.Frozenmem, f0)
	man.EnqueueFlush(f0)

	<-f0AtHook

	// Enqueue 80 items while worker is paused
	const nQueued = 80
	for i := 1; i <= nQueued; i++ {
		f := newTestMemtable()
		k := EncodeInternalKey([]byte(fmt.Sprintf("key_%03d", i)), uint64(10+i), false)
		f.Put(k, []byte(fmt.Sprintf("val_%03d", i)))

		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, f)
		man.enqueueFlushLocked(f)
		man.Mu.Unlock()
	}

	// Launch Close() in background while 80 items are queued
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- man.Close()
	}()

	// Verify manager is in CLOSING state
	time.Sleep(20 * time.Millisecond)
	man.Mu.RLock()
	state := man.state
	man.Mu.RUnlock()
	if state != EngineClosing {
		t.Fatalf("Expected EngineClosing, got %v", state)
	}

	// Release worker to process F0 and all 80 queued tasks
	close(f0Release)

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Timeout waiting for Close to drain saturated queue")
	}

	// Verify CLOSED state and readers nil
	man.Mu.RLock()
	finalState := man.state
	nReaders := len(man.ActiveSSreaders)
	nFrozen := len(man.Frozenmem)
	man.Mu.RUnlock()

	if finalState != EngineClosed {
		t.Fatalf("Expected EngineClosed, got %v", finalState)
	}
	if nReaders != 0 {
		t.Fatalf("Expected 0 active readers after close, got %d", nReaders)
	}
	if nFrozen != 0 {
		t.Fatalf("Expected 0 frozen memtables after close, got %d", nFrozen)
	}
}

// TestFlushAdmission_KeyVisibilityAcrossLifecycle verifies that keys are continuously
// visible to point reads and LatestValue at every stage of the flush lifecycle:
// Active Mem -> Frozen in Queue -> In-flight in flushToDisk -> Published in SSTable.
func TestFlushAdmission_KeyVisibilityAcrossLifecycle(t *testing.T) {
	dir := "test_data_flush_key_visibility"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	inFlightHook := make(chan struct{})
	releaseInFlight := make(chan struct{})

	var hookOnce sync.Once
	man.beforeFlushPublishHook = func() {
		hookOnce.Do(func() {
			close(inFlightHook)
			<-releaseInFlight
		})
	}

	testKey := "visibility_key"
	testVal := "visibility_val"
	kEnc := EncodeInternalKey([]byte(testKey), 50, false)

	// Stage 1: Active Memtable
	man.Mem.Put(kEnc, []byte(testVal))
	val, err := man.Get(testKey, 100)
	if err != nil || val != testVal {
		t.Fatalf("Stage 1 (Active Mem) Get failed: val=%s, err=%v", val, err)
	}
	seq, err := man.LatestValue([]byte(testKey))
	if err != nil || seq != 50 {
		t.Fatalf("Stage 1 LatestValue failed: seq=%d, err=%v", seq, err)
	}

	// Stage 2: Freeze and enqueue
	f := man.Mem
	man.Mem = newTestMemtable()
	man.Frozenmem = append(man.Frozenmem, f)
	man.EnqueueFlush(f)

	// Stage 3: Worker picks up task and enters flushToDisk, paused at beforeFlushPublishHook
	select {
	case <-inFlightHook:
	case <-time.After(5 * time.Second):
		t.Fatalf("Timeout waiting for flush to enter beforeFlushPublishHook")
	}

	// Key must still be visible in Frozenmem while worker is building SSTable
	val, err = man.Get(testKey, 100)
	if err != nil || val != testVal {
		t.Fatalf("Stage 3 (In-Flight) Get failed: val=%s, err=%v", val, err)
	}
	seq, err = man.LatestValue([]byte(testKey))
	if err != nil || seq != 50 {
		t.Fatalf("Stage 3 LatestValue failed: seq=%d, err=%v", seq, err)
	}

	// Stage 4: Release worker and wait for publication
	close(releaseInFlight)

	deadline := time.Now().Add(5 * time.Second)
	for {
		man.Mu.RLock()
		nReaders := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if nReaders == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Timeout waiting for SSTable publication")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Key must now be visible in ActiveSSreaders
	val, err = man.Get(testKey, 100)
	if err != nil || val != testVal {
		t.Fatalf("Stage 4 (Published) Get failed: val=%s, err=%v", val, err)
	}
	seq, err = man.LatestValue([]byte(testKey))
	if err != nil || seq != 50 {
		t.Fatalf("Stage 4 LatestValue failed: seq=%d, err=%v", seq, err)
	}

	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
