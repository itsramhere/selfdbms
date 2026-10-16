package lsmengine

import (
	"errors"
	"fmt"
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

// TestFlushShutdown_CaseG_MultipleConcurrentCloseCallers verifies that when multiple
// goroutines call Close() concurrently while flushes are in progress, teardown runs
// once and only once, all callers unblock with nil error, and no file handles leak.
func TestFlushShutdown_CaseG_MultipleConcurrentCloseCallers(t *testing.T) {
	dir := "test_data_flush_shutdown_case_g"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Enqueue 10 flushes
	for i := 1; i <= 10; i++ {
		f := newTestMemtable()
		f.Put(EncodeInternalKey([]byte(fmt.Sprintf("k%d", i)), uint64(i*10), false), []byte("v"))
		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, f)
		man.enqueueFlushLocked(f)
		man.Mu.Unlock()
	}

	const numCloseCallers = 10
	errs := make([]error, numCloseCallers)
	var wg sync.WaitGroup

	for i := 0; i < numCloseCallers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = man.Close()
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Close caller %d returned error: %v", i, err)
		}
	}

	man.Mu.RLock()
	finalState := man.state
	nReaders := len(man.ActiveSSreaders)
	nFrozen := len(man.Frozenmem)
	man.Mu.RUnlock()

	if finalState != EngineClosed {
		t.Fatalf("Expected EngineClosed, got %v", finalState)
	}
	if nReaders != 0 {
		t.Fatalf("Expected 0 active readers, got %d", nReaders)
	}
	if nFrozen != 0 {
		t.Fatalf("Expected 0 frozen memtables, got %d", nFrozen)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove test dir (handle leak detected): %v", err)
	}
}

// TestFlushShutdown_CaseH_ConcurrentCloseWithErrorPropagation verifies that if a flush
// error occurs during shutdown, ALL concurrent callers of Close() receive the exact
// same non-nil error, and none silently return nil.
func TestFlushShutdown_CaseH_ConcurrentCloseWithErrorPropagation(t *testing.T) {
	dir := "test_data_flush_shutdown_case_h"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	expectedErr := errors.New("simulated disk flush failure")
	man.flushBuildHook = func() error {
		return expectedErr
	}

	f := newTestMemtable()
	f.Put(EncodeInternalKey([]byte("keyErr"), 100, false), []byte("valErr"))
	man.Frozenmem = append(man.Frozenmem, f)
	man.EnqueueFlush(f)

	const numCallers = 5
	errs := make([]error, numCallers)
	var wg sync.WaitGroup

	for i := 0; i < numCallers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = man.Close()
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, expectedErr) {
			t.Fatalf("Close caller %d returned err %v, expected %v", i, err, expectedErr)
		}
	}
}

// TestFlushShutdown_CaseI_CloseConcurrentWithTransactions verifies that active transactions
// committing while Close() executes either complete prior to CLOSING or are rejected with
// ErrClosed once the engine begins shutting down, with zero deadlocks.
func TestFlushShutdown_CaseI_CloseConcurrentWithTransactions(t *testing.T) {
	dir := "test_data_flush_shutdown_case_i"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := CreateManager()

	var stopWriters sync.WaitGroup
	const numWriters = 4
	commitErrors := make([][]error, numWriters)

	for w := 0; w < numWriters; w++ {
		stopWriters.Add(1)
		commitErrors[w] = make([]error, 0)
		go func(writerID int) {
			defer stopWriters.Done()
			for i := 0; i < 50; i++ {
				tx := man.Begin()
				k := fmt.Sprintf("w%d_k%d", writerID, i)
				tx.Put(k, "val")
				err := tx.Commit()
				commitErrors[writerID] = append(commitErrors[writerID], err)
				if errors.Is(err, ErrClosed) {
					// Expected once close begins
					break
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	time.Sleep(15 * time.Millisecond)
	closeErr := man.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	stopWriters.Wait()

	// Verify that every error seen was either nil or ErrClosed
	for w, errList := range commitErrors {
		for _, err := range errList {
			if err != nil && !errors.Is(err, ErrClosed) {
				t.Fatalf("Writer %d saw unexpected error: %v", w, err)
			}
		}
	}
}

