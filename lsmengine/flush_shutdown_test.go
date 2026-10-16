package lsmengine

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// TestFlushShutdown_CaseA_InflightFlushWhileCloseBegins tests that an in-flight
// background flush registered with flushWg completes and publishes its SSTable during
// CLOSING, and then Manager.Close() transitions to CLOSED and cleanly closes the
// newly published reader without leaking file descriptors on Windows NTFS.
func TestFlushShutdown_CaseA_InflightFlushWhileCloseBegins(t *testing.T) {
	dir := "test_data_flush_shutdown_case_a"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	frozenMem := newTestMemtable()
	internalKey := EncodeInternalKey([]byte("keyA"), 10, false)
	frozenMem.Put(internalKey, []byte("valA"))
	man.Frozenmem = []*Memtable{frozenMem}

	flushStarted := make(chan struct{})
	canPublish := make(chan struct{})

	man.beforeFlushPublishHook = func() {
		close(flushStarted)
		<-canPublish
	}

	// Register in-flight flush with flushWg
	man.flushWg.Add(1)
	go func() {
		defer man.flushWg.Done()
		man.flushToDisk(frozenMem)
	}()

	// Wait until flush has built the SSTable and reached the publication hook
	<-flushStarted

	// Launch Close() in a separate goroutine; it should transition to CLOSING and wait on flushWg
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- man.Close()
	}()

	// Verify that the manager transitions to CLOSING state
	var stateDuringWait EngineState
	for i := 0; i < 100; i++ {
		man.Mu.Lock()
		stateDuringWait = man.state
		man.Mu.Unlock()
		if stateDuringWait == EngineClosing {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	if stateDuringWait != EngineClosing {
		t.Fatalf("Expected manager state to be EngineClosing while draining, got %v", stateDuringWait)
	}

	// Release flush hook so worker proceeds to publish under m.Mu.Lock()
	close(canPublish)

	// Wait for Close() to complete
	err := <-closeDone
	if err != nil {
		t.Fatalf("Manager.Close returned error: %v", err)
	}

	// Verify final state: EngineClosed, ActiveSSreaders is nil
	man.Mu.Lock()
	finalState := man.state
	activeCount := len(man.ActiveSSreaders)
	man.Mu.Unlock()

	if finalState != EngineClosed {
		t.Fatalf("Expected final state EngineClosed, got %v", finalState)
	}
	if activeCount != 0 {
		t.Fatalf("Expected ActiveSSreaders to be nil after Close, got %d", activeCount)
	}

	// Verify clean directory removal on Windows NTFS (proves no file descriptor was leaked)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove test directory (handle leak detected): %v", err)
	}
}

// TestFlushShutdown_CaseB_CloseBeforeQueuedFlushExecution tests that if Close() is called
// while a flush task is queued in flushWg, the task finishes execution during CLOSING,
// and shutdown does not terminate prematurely.
func TestFlushShutdown_CaseB_CloseBeforeQueuedFlushExecution(t *testing.T) {
	dir := "test_data_flush_shutdown_case_b"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	frozenMem := newTestMemtable()
	internalKey := EncodeInternalKey([]byte("keyB"), 20, false)
	frozenMem.Put(internalKey, []byte("valB"))
	man.Frozenmem = []*Memtable{frozenMem}

	startTask := make(chan struct{})

	man.flushWg.Add(1)
	go func() {
		defer man.flushWg.Done()
		<-startTask
		man.flushToDisk(frozenMem)
	}()

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- man.Close()
	}()

	// Start the queued task after Close has already begun
	time.Sleep(10 * time.Millisecond)
	close(startTask)

	err := <-closeDone
	if err != nil {
		t.Fatalf("Manager.Close failed: %v", err)
	}

	man.Mu.Lock()
	if man.state != EngineClosed {
		t.Fatalf("Expected EngineClosed, got %v", man.state)
	}
	man.Mu.Unlock()

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove directory: %v", err)
	}
}

// TestFlushShutdown_CaseC_FlushFailsWhileShutdownDraining tests that if a background
// flush fails during shutdown drain, the error is recorded, returned by Manager.Close(),
// and the frozen Memtable is preserved in memory.
func TestFlushShutdown_CaseC_FlushFailsWhileShutdownDraining(t *testing.T) {
	dir := "test_data_flush_shutdown_case_c"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	frozenMem := newTestMemtable()
	internalKey := EncodeInternalKey([]byte("keyC"), 30, false)
	frozenMem.Put(internalKey, []byte("valC"))
	man.Frozenmem = []*Memtable{frozenMem}

	simulatedErr := errors.New("simulated disk I/O failure during flush")
	man.flushBuildHook = func() error {
		return simulatedErr
	}

	man.flushWg.Add(1)
	go func() {
		defer man.flushWg.Done()
		man.flushToDisk(frozenMem)
	}()

	err := man.Close()
	if err == nil {
		t.Fatalf("Expected Manager.Close to return flush error, got nil")
	}
	if !errors.Is(err, simulatedErr) && err.Error() != simulatedErr.Error() {
		t.Fatalf("Expected simulatedErr, got: %v", err)
	}

	// Verify frozen Memtable was not discarded on failure
	man.Mu.Lock()
	frozenCount := len(man.Frozenmem)
	man.Mu.Unlock()

	if frozenCount != 1 {
		t.Fatalf("Expected frozen memtable to be preserved in RAM on failure, got %d", frozenCount)
	}
}

// TestFlushShutdown_CaseD_ConcurrentOrRepeatedClose tests that calling Close()
// repeatedly or concurrently is completely safe, idempotent, and deadlock-free.
func TestFlushShutdown_CaseD_ConcurrentOrRepeatedClose(t *testing.T) {
	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			errs[idx] = man.Close()
		}()
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Close goroutine %d failed: %v", i, err)
		}
	}

	man.Mu.Lock()
	if man.state != EngineClosed {
		t.Fatalf("Expected EngineClosed, got %v", man.state)
	}
	man.Mu.Unlock()
}

// TestFlushShutdown_CaseE_PostShutdownOperationRejection tests that after shutdown,
// Transaction.Commit, Manager.Get, and checkAndTriggerFlush reject operations cleanly.
func TestFlushShutdown_CaseE_PostShutdownOperationRejection(t *testing.T) {
	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	if err := man.Close(); err != nil {
		t.Fatalf("Manager.Close failed: %v", err)
	}

	// 1. Transaction.Commit must reject with ErrClosed
	txn := man.Begin()
	txn.Put("test_key", "test_val")
	err := txn.Commit()
	if err == nil {
		t.Fatalf("Expected Transaction.Commit to return error on closed manager, got nil")
	}
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Expected ErrClosed from Commit, got: %v", err)
	}

	// 2. Manager.Get must reject with ErrClosed
	_, err = man.Get("test_key", 100)
	if err == nil {
		t.Fatalf("Expected Manager.Get to return error on closed manager, got nil")
	}
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Expected ErrClosed from Get, got: %v", err)
	}

	// 3. checkAndTriggerFlush must safely return without panicking or spawning
	man.checkAndTriggerFlush()
}

// TestFlushShutdown_CaseF_CompactionAndFlushActiveDuringShutdown tests that if a compaction
// and a flush are both active when Close() is called, compactionMu serializes Close with compaction,
// and flushWg coordinates cleanly with flush, avoiding lock inversion and handle leaks.
func TestFlushShutdown_CaseF_CompactionAndFlushActiveDuringShutdown(t *testing.T) {
	dir := "test_data_flush_shutdown_case_f"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kA", 10, "valA", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kB", 20, "valB", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}

	frozenMem := newTestMemtable()
	frozenMem.Put(EncodeInternalKey([]byte("kC"), 30, false), []byte("valC"))
	man.Frozenmem = []*Memtable{frozenMem}

	compactionInMerge := make(chan struct{})
	canFinishCompaction := make(chan struct{})
	man.duringCompactionMergeHook = func() {
		close(compactionInMerge)
		<-canFinishCompaction
	}

	flushInBuild := make(chan struct{})
	canFinishFlush := make(chan struct{})
	man.beforeFlushPublishHook = func() {
		close(flushInBuild)
		<-canFinishFlush
	}

	// Launch active compaction
	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	// Wait for compaction to acquire compactionMu and enter merge
	<-compactionInMerge

	// Launch active flush
	man.flushWg.Add(1)
	go func() {
		defer man.flushWg.Done()
		man.flushToDisk(frozenMem)
	}()

	<-flushInBuild

	// Launch Close() in background; it will wait on compactionMu.Lock()
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- man.Close()
	}()

	// Release compaction first
	close(canFinishCompaction)
	compErr := <-compactionDone
	if compErr != nil {
		t.Fatalf("CompactAll failed: %v", compErr)
	}

	// Release flush
	close(canFinishFlush)

	// Close() should now unblock, drain the flush, and finalize
	closeErr := <-closeDone
	if closeErr != nil {
		t.Fatalf("Manager.Close failed: %v", closeErr)
	}

	// Verify all readers were closed cleanly and test dir can be deleted on NTFS
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove test dir on NTFS (handle leak detected): %v", err)
	}
}
