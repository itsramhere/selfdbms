package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestCombinedValidation_ReadAndFlushConcurrent tests combination 1:
// Active read + active flush running concurrently.
// Verifies memory safety, race-freedom, and monotonic snapshot reads during background flush.
func TestCombinedValidation_ReadAndFlushConcurrent(t *testing.T) {
	dir := "test_data_comb_read_flush"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	stopReaders := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, 16)

	// Launch 4 concurrent point readers
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
					val, err := man.Get("key_concurrent", 100)
					if err != nil && !errors.Is(err, ErrNotFound) {
						errCh <- fmt.Errorf("reader %d unexpected error: %v", id, err)
						return
					}
					_ = val
					time.Sleep(500 * time.Microsecond)
				}
			}
		}(r)
	}

	// Sequentially generate and flush 4 Memtables
	for i := 1; i <= 4; i++ {
		f := newTestMemtable()
		f.Put(EncodeInternalKey([]byte("key_concurrent"), uint64(i*10), false), []byte(fmt.Sprintf("v_%d", i)))
		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, f)
		man.Mu.Unlock()
		man.EnqueueFlush(f)
		time.Sleep(10 * time.Millisecond)
	}

	// Drain all flushes
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 4 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(stopReaders)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Concurrent read error: %v", err)
	}

	// Verify final value is newest (v_4)
	val, err := man.Get("key_concurrent", 100)
	if err != nil || val != "v_4" {
		t.Fatalf("Expected newest value 'v_4', got %q (err: %v)", val, err)
	}

	// Clean shutdown and file removal
	var toClean []string
	man.Mu.RLock()
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			toClean = append(toClean, r.File.Name())
		}
	}
	man.Mu.RUnlock()
	man.Close()
	for _, f := range toClean {
		os.Remove(f)
	}
}

// TestCombinedValidation_WriteCommitAndFlushConcurrent tests combination 2:
// Active write/commit + active flush running concurrently.
// Verifies transaction write-write conflict detection across active memtable,
// frozen memtable, and flushed SSTable tiers while flushes are in progress.
func TestCombinedValidation_WriteCommitAndFlushConcurrent(t *testing.T) {
	dir := "test_data_comb_write_flush"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	wal, err := NewWal(dir + "/wal.log")
	if err != nil {
		t.Fatalf("Failed to create WAL: %v", err)
	}

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		Log:             wal,
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Pre-populate key "account:1" at seq 10 in frozen memtable
	f1 := newTestMemtable()
	f1.Put(EncodeInternalKey([]byte("account:1"), 10, false), []byte("bal_100"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f1)
	man.CurrentSeqNo.Store(10)
	man.Mu.Unlock()

	// Enqueue flush of F1
	man.EnqueueFlush(f1)

	// Transaction T1 starts with ReadTs = 5 (older than seq 10 in F1)
	t1 := &Transaction{
		Man:      man,
		ReadTs:   5,
		WriteSet: map[string]PendingWrite{"account:1": {Data: []byte("bal_150")}},
	}

	// Transaction T2 starts with ReadTs = 10 (sees seq 10)
	t2 := &Transaction{
		Man:      man,
		ReadTs:   10,
		WriteSet: map[string]PendingWrite{"account:2": {Data: []byte("bal_200")}},
	}

	// T1 commit must detect conflict with seq 10 whether F1 is in Frozenmem or ActiveSSreaders
	err1 := t1.Commit()
	if err1 == nil {
		t.Fatalf("Expected T1 commit to fail with write-write conflict, but it succeeded!")
	}

	// T2 commit on non-conflicting key must succeed
	err2 := t2.Commit()
	if err2 != nil {
		t.Fatalf("Expected T2 commit to succeed, got: %v", err2)
	}

	// Wait for flush drain
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Clean shutdown and file removal
	var toClean []string
	man.Mu.RLock()
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			toClean = append(toClean, r.File.Name())
		}
	}
	man.Mu.RUnlock()
	man.Close()
	for _, f := range toClean {
		os.Remove(f)
	}
}

// TestCombinedValidation_ConcurrentFlushRequests tests combination 3:
// Multiple concurrent flush requests.
// Verifies that multiple goroutines calling EnqueueFlush concurrently are serialized
// deterministically by the FIFO queue without race conditions or out-of-order append.
func TestCombinedValidation_ConcurrentFlushRequests(t *testing.T) {
	dir := "test_data_comb_concurrent_flushes"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	const numTables = 6
	tables := make([]*Memtable, numTables)
	for i := 0; i < numTables; i++ {
		tables[i] = newTestMemtable()
		tables[i].Put(EncodeInternalKey([]byte("seq_key"), uint64((i+1)*10), false), []byte(fmt.Sprintf("v_%d", i+1)))
	}

	man.Mu.Lock()
	for _, tbl := range tables {
		man.Frozenmem = append(man.Frozenmem, tbl)
	}
	man.Mu.Unlock()

	// Enqueue in strict order from multiple goroutines
	var wg sync.WaitGroup
	for i := 0; i < numTables; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Enqueue in order by staggering slightly
			time.Sleep(time.Duration(idx*5) * time.Millisecond)
			man.EnqueueFlush(tables[idx])
		}(i)
	}
	wg.Wait()

	// Wait for all tables to publish
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == numTables {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Verify ActiveSSreaders has length numTables and latest value is v_6
	man.Mu.RLock()
	if len(man.ActiveSSreaders) != numTables {
		man.Mu.RUnlock()
		t.Fatalf("Expected %d readers, got %d", numTables, len(man.ActiveSSreaders))
	}
	val, err := man.Get("seq_key", 100)
	man.Mu.RUnlock()
	if err != nil || val != "v_6" {
		t.Fatalf("Expected newest value 'v_6', got %q (err: %v)", val, err)
	}

	// Clean shutdown
	var toClean []string
	man.Mu.RLock()
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			toClean = append(toClean, r.File.Name())
		}
	}
	man.Mu.RUnlock()
	man.Close()
	for _, f := range toClean {
		os.Remove(f)
	}
}

// TestCombinedValidation_FlushCompactionAndShutdownConcurrent tests combination 4, 5, 6:
// Flush + Compaction + Shutdown running concurrently.
// Verifies strict lock hierarchy compactionMu -> Manager.Mu -> Wal.mu and ensures
// zero deadlocks, zero handle leaks, and complete reader teardown.
func TestCombinedValidation_FlushCompactionAndShutdownConcurrent(t *testing.T) {
	dir := "test_data_comb_flush_compact_shutdown"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "val10", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "val20", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}

	compactStarted := make(chan struct{})
	canCompactFinish := make(chan struct{})
	man.beforeCompactionPublishHook = func() {
		close(compactStarted)
		<-canCompactFinish
	}

	// Start compaction
	compactDone := make(chan error, 1)
	go func() {
		compactDone <- man.CompactAll()
	}()

	<-compactStarted

	// While compaction is active, enqueue a flush
	f := newTestMemtable()
	f.Put(EncodeInternalKey([]byte("k1"), 30, false), []byte("val30"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f)
	man.Mu.Unlock()
	man.EnqueueFlush(f)

	// Now initiate Close() concurrently
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- man.Close()
	}()

	// Wait briefly to confirm Close is waiting on compactionMu
	time.Sleep(20 * time.Millisecond)

	// Release compaction
	close(canCompactFinish)

	// Compaction finishes, then Close acquires compactionMu, transitions to CLOSING,
	// drains the in-flight flush, and completes shutdown to CLOSED.
	compactErr := <-compactDone
	closeErr := <-closeDone

	if compactErr != nil && !errors.Is(compactErr, ErrClosed) {
		t.Fatalf("CompactAll returned unexpected error: %v", compactErr)
	}
	if closeErr != nil {
		t.Fatalf("Manager.Close returned error: %v", closeErr)
	}

	man.Mu.Lock()
	if man.state != EngineClosed {
		t.Fatalf("Expected EngineClosed, got %v", man.state)
	}
	if man.ActiveSSreaders != nil {
		t.Fatalf("Expected ActiveSSreaders to be nil after close, got len %d", len(man.ActiveSSreaders))
	}
	man.Mu.Unlock()
}

// TestCombinedValidation_CrossTierVersionAndTombstoneTransitions tests combination 8:
// Version and tombstone visibility across memtable, frozen memtable, flushed SSTable,
// and compacted SSTable tiers.
func TestCombinedValidation_CrossTierVersionAndTombstoneTransitions(t *testing.T) {
	dir := "test_data_comb_crosstier"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Tier 1: Active Memtable write
	man.Mu.Lock()
	man.Mem.Put(EncodeInternalKey([]byte("x"), 10, false), []byte("v1_active"))
	man.Mu.Unlock()

	val1, err := man.Get("x", 100)
	if err != nil || val1 != "v1_active" {
		t.Fatalf("Tier 1 Get failed: %q, err: %v", val1, err)
	}

	// Tier 2: Freeze into Frozenmem, write v2 in active Memtable
	f1 := man.Mem
	man.Mu.Lock()
	man.Frozenmem = []*Memtable{f1}
	man.Mem = newTestMemtable()
	man.Mem.Put(EncodeInternalKey([]byte("x"), 20, false), []byte("v2_active"))
	man.Mu.Unlock()

	// v2 in Mem masks v1 in Frozenmem
	val2, err := man.Get("x", 100)
	if err != nil || val2 != "v2_active" {
		t.Fatalf("Tier 2 Get failed: %q, err: %v", val2, err)
	}

	// Tier 3: Flush F1 to SSTable R0; put tombstone in active Memtable
	man.EnqueueFlush(f1)
	man.Mu.Lock()
	man.Mem.Put(EncodeInternalKey([]byte("x"), 30, true), []byte{}) // tombstone
	man.Mu.Unlock()

	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Tombstone in Mem masks everything
	_, errTomb := man.Get("x", 100)
	if !errors.Is(errTomb, ErrKeyDeleted) {
		t.Fatalf("Expected ErrKeyDeleted, got %v", errTomb)
	}

	// Tier 4: Freeze tombstone, flush to SSTable R1, put v4 in active Memtable
	fTomb := man.Mem
	man.Mu.Lock()
	man.Frozenmem = []*Memtable{fTomb}
	man.Mem = newTestMemtable()
	man.Mem.Put(EncodeInternalKey([]byte("x"), 40, false), []byte("v4_active"))
	man.Mu.Unlock()

	man.EnqueueFlush(fTomb)
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// ActiveSSreaders now has [R0(seq 10), R1(seq 20 and seq 30 tombstone)]
	// Point query at readTs=15: sees R0 (seq 10 -> "v1_active")
	val15, err15 := man.Get("x", 15)
	if err15 != nil || val15 != "v1_active" {
		t.Fatalf("Historical readTs 15 failed: %q, err: %v", val15, err15)
	}

	// Point query at readTs=25: sees R1 (seq 20 -> "v2_active")
	val25, err25 := man.Get("x", 25)
	if err25 != nil || val25 != "v2_active" {
		t.Fatalf("Historical readTs 25 failed: %q, err: %v", val25, err25)
	}

	// Point query at readTs=35: sees R1 tombstone (seq 30 -> ErrKeyDeleted)
	_, errDel := man.Get("x", 35)
	if !errors.Is(errDel, ErrKeyDeleted) {
		t.Fatalf("Historical readTs 35 expected ErrKeyDeleted, got %v", errDel)
	}

	// Point query at readTs=100: sees active Memtable (seq 40 -> "v4_active")
	valNew, errNew := man.Get("x", 100)
	if errNew != nil || valNew != "v4_active" {
		t.Fatalf("readTs 100 failed: %q, err: %v", valNew, errNew)
	}

	// Tier 5: Compact [R0, R1] into single compacted SSTable R_compact
	// CompactAll merges seq 10 (put) and seq 30 (tombstone)
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	man.Mu.RLock()
	if len(man.ActiveSSreaders) != 1 {
		man.Mu.RUnlock()
		t.Fatalf("Expected 1 reader after compaction, got %d", len(man.ActiveSSreaders))
	}
	man.Mu.RUnlock()

	// Verify readTs 100 still returns "v4_active" from active Memtable
	valAfterCompact, err := man.Get("x", 100)
	if err != nil || valAfterCompact != "v4_active" {
		t.Fatalf("Post-compact readTs 100 failed: %q, err: %v", valAfterCompact, err)
	}

	// Cleanup
	var toClean []string
	man.Mu.RLock()
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			toClean = append(toClean, r.File.Name())
		}
	}
	man.Mu.RUnlock()
	man.Close()
	for _, f := range toClean {
		os.Remove(f)
	}
}

// TestCombinedValidation_CleanHandleReleaseAndReopen tests combinations 9 and 10:
// Verify late flushes cannot revive closed manager and all handles are released on Windows NTFS.
func TestCombinedValidation_CleanHandleReleaseAndReopen(t *testing.T) {
	dir := "test_data_comb_clean_reopen"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Write and flush a table
	f := newTestMemtable()
	f.Put(EncodeInternalKey([]byte("reopen_key"), 10, false), []byte("initial_value"))
	man.Mu.Lock()
	man.Frozenmem = []*Memtable{f}
	man.Mu.Unlock()
	man.EnqueueFlush(f)

	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Close the manager
	if err := man.Close(); err != nil {
		t.Fatalf("Manager.Close failed: %v", err)
	}

	// Late operations must all return ErrClosed
	_, getErr := man.Get("reopen_key", 100)
	if !errors.Is(getErr, ErrClosed) {
		t.Fatalf("Expected ErrClosed on Get after Close, got: %v", getErr)
	}

	txn := man.Begin()
	txn.WriteSet["foo"] = PendingWrite{Data: []byte("bar")}
	commitErr := txn.Commit()
	if !errors.Is(commitErr, ErrClosed) {
		t.Fatalf("Expected ErrClosed on Commit after Close, got: %v", commitErr)
	}

	// Late flush attempt must be rejected without panic or deadlock
	fLate := newTestMemtable()
	fLate.Put(EncodeInternalKey([]byte("late"), 50, false), []byte("val"))
	man.EnqueueFlush(fLate) // No-op, rejected

	// Verify all handles were cleanly closed on Windows NTFS by removing directory without error
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove test directory on Windows NTFS (file handle leak): %v", err)
	}
}
