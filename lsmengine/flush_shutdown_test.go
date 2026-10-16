package lsmengine

import (
	"os"
	"sync"
	"testing"
)

// TestFlushShutdown_InFlightFlushPublishesAfterClose reproduces Issue E-007:
// An in-flight background flushToDisk goroutine that completes building an SSTable
// publishes its SSTableReader into m.ActiveSSreaders AFTER Manager.Close() has completed,
// leaking an open file descriptor on Windows NTFS and corrupting the closed engine state.
func TestFlushShutdown_InFlightFlushPublishesAfterClose(t *testing.T) {
	dir := "test_data_flush_shutdown_repro"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Create a frozen memtable with committed data
	frozenMem := newTestMemtable()
	internalKey := EncodeInternalKey([]byte("user_key"), 10, false)
	frozenMem.Put(internalKey, []byte("user_val"))
	man.Frozenmem = []*Memtable{frozenMem}

	flushStarted := make(chan struct{})
	canPublish := make(chan struct{})
	flushDone := make(chan struct{})

	man.beforeFlushPublishHook = func() {
		close(flushStarted)
		<-canPublish
	}

	// Launch background flush
	go func() {
		man.flushToDisk(frozenMem)
		close(flushDone)
	}()

	// Wait until flush has built the SSTable and is about to acquire m.Mu.Lock()
	<-flushStarted

	// Close the manager while flush is in-flight
	if err := man.Close(); err != nil {
		t.Fatalf("Manager.Close failed: %v", err)
	}

	man.Mu.Lock()
	if !man.isClosed {
		t.Fatalf("Expected isClosed to be true after Close")
	}
	if len(man.ActiveSSreaders) != 0 {
		t.Fatalf("Expected ActiveSSreaders to be empty after Close, got %d", len(man.ActiveSSreaders))
	}
	man.Mu.Unlock()

	// Now allow the in-flight flush to proceed to publication
	close(canPublish)
	<-flushDone

	// Check whether the closed manager was corrupted with a new published reader
	man.Mu.Lock()
	readerCountAfterFlush := len(man.ActiveSSreaders)
	var leakedReader *SSTableReader
	if readerCountAfterFlush > 0 {
		leakedReader = man.ActiveSSreaders[0]
	}
	man.Mu.Unlock()

	if readerCountAfterFlush > 0 {
		var leakedPath string
		if leakedReader != nil && leakedReader.File != nil {
			leakedPath = leakedReader.File.Name()
			leakedReader.Close()
			os.Remove(leakedPath)
		}
		t.Fatalf("DEFECT E-007 REPRODUCED: In-flight flush published %d reader(s) into closed Manager! Leaked reader: %s",
			readerCountAfterFlush, leakedPath)
	}
}
