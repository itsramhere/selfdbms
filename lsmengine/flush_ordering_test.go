package lsmengine

import (
	"os"
	"sync"
	"testing"
)

// TestFlushOrdering_ConcurrentFlushesViolateOrder reproduces Issue E-008:
// When multiple Memtables are flushed concurrently, if an older flush (F1) completes
// publication after a newer flush (F2), F1 is appended after F2 in m.ActiveSSreaders.
// Because Manager.Get scans m.ActiveSSreaders in reverse order (assuming newer tables
// are at the end), Manager.Get encounters F1 first and returns stale data, violating INV-ORDER-1.
func TestFlushOrdering_ConcurrentFlushesViolateOrder(t *testing.T) {
	dir := "test_data_flush_ordering_repro"
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

	man.Frozenmem = []*Memtable{f1, f2}

	// We force out-of-order publication:
	// F1 starts first, but pauses before publication until F2 has finished publishing.
	f1Started := make(chan struct{})
	canF1Publish := make(chan struct{})
	f1Done := make(chan struct{})
	f2Done := make(chan struct{})

	// Custom hook to pause only the F1 flush
	man.beforeFlushPublishHook = func() {
		// Only pause the first flush (F1)
		select {
		case <-f1Started:
			// F2 or subsequent flush: do not pause
			return
		default:
			close(f1Started)
			<-canF1Publish
		}
	}

	// Start F1 flush
	go func() {
		man.flushToDisk(f1)
		close(f1Done)
	}()

	// Wait for F1 to reach publication hook
	<-f1Started

	// Now run F2 flush to completion while F1 is paused
	go func() {
		man.flushToDisk(f2)
		close(f2Done)
	}()

	// Wait for F2 to completely publish
	<-f2Done

	// Now release F1 to publish its older SSTable
	close(canF1Publish)
	<-f1Done

	// Check reader order in ActiveSSreaders
	man.Mu.RLock()
	readerCount := len(man.ActiveSSreaders)
	man.Mu.RUnlock()

	if readerCount != 2 {
		t.Fatalf("Expected 2 readers in ActiveSSreaders, got %d", readerCount)
	}

	// Read key1 at readTs = 100
	// Expected: "v2_new" (since seq 20 is newer than seq 10)
	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Manager.Get failed: %v", err)
	}

	// Cleanup readers and files
	man.Mu.Lock()
	for _, r := range man.ActiveSSreaders {
		fname := r.File.Name()
		r.Close()
		os.Remove(fname)
	}
	man.ActiveSSreaders = nil
	man.Mu.Unlock()

	if val != "v2_new" {
		t.Fatalf("DEFECT E-008 REPRODUCED: Manager.Get returned stale value %q (expected %q). Out-of-order flushes corrupted reader chronological order!",
			val, "v2_new")
	}
}
