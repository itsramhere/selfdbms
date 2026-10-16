package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestFlushOrdering_Case1_TwoFlushesWithReversedTiming tests requirement 1:
// Two flushes with reversed timing (assert newest is at end of ActiveSSreaders).
// Even if older flush F1 is delayed at the publication boundary, the dedicated FIFO
// flush worker guarantees that F1 publishes before F2, so F2 is appended at the end
// of ActiveSSreaders, maintaining INV-ORDER-1 and preventing stale reads.
func TestFlushOrdering_Case1_TwoFlushesWithReversedTiming(t *testing.T) {
	dir := "test_data_flush_ordering_case1"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// F1: older memtable with seq 10, key "key1" = "v1_old"
	f1 := newTestMemtable()
	k1_seq10 := EncodeInternalKey([]byte("key1"), 10, false)
	f1.Put(k1_seq10, []byte("v1_old"))

	// F2: newer memtable with seq 20, key "key1" = "v2_new"
	f2 := newTestMemtable()
	k1_seq20 := EncodeInternalKey([]byte("key1"), 20, false)
	f2.Put(k1_seq20, []byte("v2_new"))

	man.Mu.Lock()
	man.Frozenmem = []*Memtable{f1, f2}
	man.Mu.Unlock()

	f1Started := make(chan struct{})
	canF1Publish := make(chan struct{})

	// Hook pauses only the F1 flush before publication
	man.beforeFlushPublishHook = func() {
		select {
		case <-f1Started:
			return // F2 or subsequent
		default:
			close(f1Started)
			<-canF1Publish
		}
	}

	// Enqueue F1 then F2 into FIFO flush queue
	man.EnqueueFlush(f1)
	man.EnqueueFlush(f2)

	// Wait until F1 is executing and paused at the hook
	<-f1Started

	// F2 is sitting in the FIFO channel. Verify F2 has NOT published.
	man.Mu.RLock()
	activeCountWhileF1Paused := len(man.ActiveSSreaders)
	man.Mu.RUnlock()
	if activeCountWhileF1Paused != 0 {
		t.Fatalf("Expected 0 active readers while F1 is paused, got %d", activeCountWhileF1Paused)
	}

	// Release F1 to publish
	close(canF1Publish)

	// Wait for both flushes to drain
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Verify ActiveSSreaders ordering: F1 must be index 0, F2 must be index 1
	man.Mu.RLock()
	defer man.Mu.RUnlock()

	if len(man.ActiveSSreaders) != 2 {
		t.Fatalf("Expected 2 readers in ActiveSSreaders, got %d", len(man.ActiveSSreaders))
	}

	// Read key1 at readTs = 100
	// Manager.Get scans ActiveSSreaders in reverse: encounters index 1 (F2) first!
	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Manager.Get failed: %v", err)
	}
	if val != "v2_new" {
		t.Fatalf("Expected newest value 'v2_new', got %q. Defect E-008 still present!", val)
	}

	// Capture filenames for disk cleanup
	var toClean []string
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			toClean = append(toClean, r.File.Name())
		}
	}
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			r.Close()
		}
	}
	man.ActiveSSreaders = nil
	for _, f := range toClean {
		os.Remove(f)
	}
}

// TestFlushOrdering_Case2_ThreeOrMoreFrozenMemtablesInQueue tests requirement 2:
// Three or more frozen Memtables in queue.
// Verifies that enqueuing multiple Memtables (F1, F2, F3) processes strictly in order,
// drains Frozenmem completely, and maintains chronological ordering in ActiveSSreaders.
func TestFlushOrdering_Case2_ThreeOrMoreFrozenMemtablesInQueue(t *testing.T) {
	dir := "test_data_flush_ordering_case2"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// F1: seq 10, shared="v10", k1="a"
	f1 := newTestMemtable()
	f1.Put(EncodeInternalKey([]byte("shared"), 10, false), []byte("v10"))
	f1.Put(EncodeInternalKey([]byte("k1"), 10, false), []byte("a"))

	// F2: seq 20, shared="v20", k2="b"
	f2 := newTestMemtable()
	f2.Put(EncodeInternalKey([]byte("shared"), 20, false), []byte("v20"))
	f2.Put(EncodeInternalKey([]byte("k2"), 20, false), []byte("b"))

	// F3: seq 30, shared="v30", k3="c"
	f3 := newTestMemtable()
	f3.Put(EncodeInternalKey([]byte("shared"), 30, false), []byte("v30"))
	f3.Put(EncodeInternalKey([]byte("k3"), 30, false), []byte("c"))

	man.Mu.Lock()
	man.Frozenmem = []*Memtable{f1, f2, f3}
	man.Mu.Unlock()

	// Enqueue all three into the FIFO queue in order
	man.EnqueueFlush(f1)
	man.EnqueueFlush(f2)
	man.EnqueueFlush(f3)

	// Wait until all 3 flushes have completed and published
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	man.Mu.RLock()
	// Assert Frozenmem has been completely drained
	if len(man.Frozenmem) != 0 {
		man.Mu.RUnlock()
		t.Fatalf("Expected Frozenmem to be fully drained (len 0), got %d", len(man.Frozenmem))
	}

	// Verify shared key returns newest version (seq 30 -> "v30")
	valShared, err := man.Get("shared", 100)
	if err != nil {
		man.Mu.RUnlock()
		t.Fatalf("Get('shared') failed: %v", err)
	}
	if valShared != "v30" {
		man.Mu.RUnlock()
		t.Fatalf("Expected 'v30', got %q", valShared)
	}

	// Verify unique keys from each generation are readable
	valK1, err := man.Get("k1", 100)
	if err != nil || valK1 != "a" {
		man.Mu.RUnlock()
		t.Fatalf("Expected k1='a', got %q (err: %v)", valK1, err)
	}
	valK2, err := man.Get("k2", 100)
	if err != nil || valK2 != "b" {
		man.Mu.RUnlock()
		t.Fatalf("Expected k2='b', got %q (err: %v)", valK2, err)
	}
	valK3, err := man.Get("k3", 100)
	if err != nil || valK3 != "c" {
		man.Mu.RUnlock()
		t.Fatalf("Expected k3='c', got %q (err: %v)", valK3, err)
	}
	man.Mu.RUnlock()

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

// TestFlushOrdering_Case3_MultipleVersionsOfSameKeyAcrossFlushes tests requirement 3:
// Multiple versions of same key across flushes.
// Tests multi-generation MVCC snapshot isolation across distinct flushed SSTables.
func TestFlushOrdering_Case3_MultipleVersionsOfSameKeyAcrossFlushes(t *testing.T) {
	dir := "test_data_flush_ordering_case3"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Gen 1: "user:1", seq 10, "profile_v1"
	f1 := newTestMemtable()
	f1.Put(EncodeInternalKey([]byte("user:1"), 10, false), []byte("profile_v1"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f1)
	man.Mu.Unlock()
	man.EnqueueFlush(f1)

	// Gen 2: "user:1", seq 20, "profile_v2"
	f2 := newTestMemtable()
	f2.Put(EncodeInternalKey([]byte("user:1"), 20, false), []byte("profile_v2"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f2)
	man.Mu.Unlock()
	man.EnqueueFlush(f2)

	// Gen 3: "user:1", seq 30, "profile_v3"
	f3 := newTestMemtable()
	f3.Put(EncodeInternalKey([]byte("user:1"), 30, false), []byte("profile_v3"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f3)
	man.Mu.Unlock()
	man.EnqueueFlush(f3)

	// Gen 4: kept in active Memtable (seq 40, "profile_v4_ram")
	man.Mu.Lock()
	man.Mem.Put(EncodeInternalKey([]byte("user:1"), 40, false), []byte("profile_v4_ram"))
	man.Mu.Unlock()

	// Wait for all 3 flushes to publish
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Test point queries at historical read timestamps
	tests := []struct {
		readTs      uint64
		expectedVal string
		expectErr   error
	}{
		{5, "", ErrNotFound},
		{10, "profile_v1", nil},
		{15, "profile_v1", nil},
		{20, "profile_v2", nil},
		{25, "profile_v2", nil},
		{30, "profile_v3", nil},
		{35, "profile_v3", nil},
		{40, "profile_v4_ram", nil},
		{100, "profile_v4_ram", nil},
	}

	for _, tc := range tests {
		val, err := man.Get("user:1", tc.readTs)
		if tc.expectErr != nil {
			if !errors.Is(err, tc.expectErr) {
				t.Errorf("At readTs %d: expected err %v, got %v", tc.readTs, tc.expectErr, err)
			}
		} else {
			if err != nil {
				t.Errorf("At readTs %d: unexpected err %v", tc.readTs, err)
			} else if val != tc.expectedVal {
				t.Errorf("At readTs %d: expected %q, got %q", tc.readTs, tc.expectedVal, val)
			}
		}
	}

	// Verify LatestValue returns greatest committed seq (40)
	latestSeq, err := man.LatestValue([]byte("user:1"))
	if err != nil || latestSeq != 40 {
		t.Fatalf("Expected LatestValue seq 40, got %d (err: %v)", latestSeq, err)
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

// TestFlushOrdering_Case4_DeleteFollowedByPutAcrossFlushes tests requirement 4:
// Delete followed by Put across flushes.
// Verifies that tombstones do not resurrect older deleted versions, and subsequent
// Puts correctly supersede earlier tombstones across distinct SSTable flushes.
func TestFlushOrdering_Case4_DeleteFollowedByPutAcrossFlushes(t *testing.T) {
	dir := "test_data_flush_ordering_case4"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// F1: Put "doc:1", seq 10, val="draft_1"
	f1 := newTestMemtable()
	f1.Put(EncodeInternalKey([]byte("doc:1"), 10, false), []byte("draft_1"))

	// F2: Delete "doc:1", seq 20 (tombstone)
	f2 := newTestMemtable()
	f2.Put(EncodeInternalKey([]byte("doc:1"), 20, true), []byte{})

	// F3: Put "doc:1", seq 30, val="revised_3"
	f3 := newTestMemtable()
	f3.Put(EncodeInternalKey([]byte("doc:1"), 30, false), []byte("revised_3"))

	man.Mu.Lock()
	man.Frozenmem = []*Memtable{f1, f2, f3}
	man.Mu.Unlock()

	man.EnqueueFlush(f1)
	man.EnqueueFlush(f2)
	man.EnqueueFlush(f3)

	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Point queries:
	// readTs 15: sees F1 ("draft_1")
	val15, err15 := man.Get("doc:1", 15)
	if err15 != nil || val15 != "draft_1" {
		t.Fatalf("readTs 15: expected 'draft_1', got %q (err: %v)", val15, err15)
	}

	// readTs 25: sees F2 tombstone (ErrKeyDeleted)
	_, err25 := man.Get("doc:1", 25)
	if !errors.Is(err25, ErrKeyDeleted) {
		t.Fatalf("readTs 25: expected ErrKeyDeleted, got %v", err25)
	}

	// readTs 35: sees F3 ("revised_3")
	val35, err35 := man.Get("doc:1", 35)
	if err35 != nil || val35 != "revised_3" {
		t.Fatalf("readTs 35: expected 'revised_3', got %q (err: %v)", val35, err35)
	}

	// readTs 100: sees F3 ("revised_3")
	val100, err100 := man.Get("doc:1", 100)
	if err100 != nil || val100 != "revised_3" {
		t.Fatalf("readTs 100: expected 'revised_3', got %q (err: %v)", val100, err100)
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

// TestFlushOrdering_Case5_CompactionInterleavedWithQueuedFlushes tests requirement 5:
// Compaction interleaved with queued flushes.
// Verifies that when CompactAll is merging older SSTables, flushes queued concurrently
// publish safely to the tail of ActiveSSreaders, and replaceCompactedReaders replaces
// the compacted prefix without disturbing the newly flushed tables or violating INV-ORDER-1.
func TestFlushOrdering_Case5_CompactionInterleavedWithQueuedFlushes(t *testing.T) {
	dir := "test_data_flush_ordering_case5"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// Pre-create on-disk SSTables R0 and R1
	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 5, "v5_old", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k2", 10, "v10_old", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}

	compactionPaused := make(chan struct{})
	canPublishCompaction := make(chan struct{})

	// Hook pauses compaction right before replaceCompactedReaders
	man.beforeCompactionPublishHook = func() {
		close(compactionPaused)
		<-canPublishCompaction
	}

	// Start compaction in background goroutine
	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	// Wait for compaction to pause before publication
	<-compactionPaused

	// While compaction is paused, enqueue F2 and F3
	f2 := newTestMemtable()
	f2.Put(EncodeInternalKey([]byte("k1"), 20, false), []byte("v20_new"))

	f3 := newTestMemtable()
	f3.Put(EncodeInternalKey([]byte("k2"), 30, false), []byte("v30_new"))

	man.Mu.Lock()
	man.Frozenmem = []*Memtable{f2, f3}
	man.Mu.Unlock()

	man.EnqueueFlush(f2)
	man.EnqueueFlush(f3)

	// Wait until F2 and F3 have published
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == 4 { // [R0, R1, R2, R3]
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Release compaction to publish its replacement
	close(canPublishCompaction)

	if err := <-compactionDone; err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Verify reader slice state:
	// Prefix [R0, R1] was replaced by R_compact at index 0.
	// R2 and R3 remain at indices 1 and 2.
	man.Mu.RLock()
	defer man.Mu.RUnlock()

	if len(man.ActiveSSreaders) != 3 {
		t.Fatalf("Expected 3 readers in ActiveSSreaders, got %d", len(man.ActiveSSreaders))
	}

	// Manager.Get should find the newer versions in R2 and R3
	valK1, err := man.Get("k1", 100)
	if err != nil || valK1 != "v20_new" {
		t.Fatalf("Expected k1='v20_new', got %q (err: %v)", valK1, err)
	}
	valK2, err := man.Get("k2", 100)
	if err != nil || valK2 != "v30_new" {
		t.Fatalf("Expected k2='v30_new', got %q (err: %v)", valK2, err)
	}

	// Cleanup
	var toClean []string
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			toClean = append(toClean, r.File.Name())
		}
	}
	for _, r := range man.ActiveSSreaders {
		if r != nil {
			r.Close()
		}
	}
	man.ActiveSSreaders = nil
	for _, f := range toClean {
		os.Remove(f)
	}
}

// TestFlushOrdering_Case6_PointReadsAtSupportedTimestamps tests requirement 6:
// Point reads at supported timestamps across flushes.
// Concurrent stress test executing concurrent point reads while the FIFO worker
// processes multiple queued flushes, verifying race freedom and version consistency.
func TestFlushOrdering_Case6_PointReadsAtSupportedTimestamps(t *testing.T) {
	dir := "test_data_flush_ordering_case6"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	const numFlushes = 5
	for i := 1; i <= numFlushes; i++ {
		f := newTestMemtable()
		for k := 0; k < 10; k++ {
			key := fmt.Sprintf("bench_key_%d", k)
			val := fmt.Sprintf("val_gen_%d", i)
			f.Put(EncodeInternalKey([]byte(key), uint64(i*10), false), []byte(val))
		}
		man.Mu.Lock()
		man.Frozenmem = append(man.Frozenmem, f)
		man.Mu.Unlock()
		man.EnqueueFlush(f)
	}

	// Launch concurrent readers querying at different timestamps
	var wg sync.WaitGroup
	errCh := make(chan error, 16)

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for iter := 0; iter < 50; iter++ {
				// Query key at readTs 25 (should return val_gen_2)
				val, err := man.Get("bench_key_0", 25)
				if err != nil {
					// Might still be flushing gen 1 or gen 2
					if !errors.Is(err, ErrNotFound) {
						errCh <- fmt.Errorf("unexpected error at readTs 25: %v", err)
						return
					}
				} else if val != "val_gen_2" && val != "val_gen_1" {
					errCh <- fmt.Errorf("readTs 25 returned invalid value %q", val)
					return
				}
				time.Sleep(1 * time.Millisecond)
			}
		}(r)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Reader error during concurrent flush: %v", err)
	}

	// Wait for all flushes to complete
	for {
		man.Mu.RLock()
		n := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if n == numFlushes {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Verify final state
	finalVal, err := man.Get("bench_key_0", 100)
	if err != nil || finalVal != fmt.Sprintf("val_gen_%d", numFlushes) {
		t.Fatalf("Expected final value 'val_gen_%d', got %q (err: %v)", numFlushes, finalVal, err)
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
