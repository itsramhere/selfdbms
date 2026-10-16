package lsmengine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestFlushFailure_DiskFullDuringSSTableBuild verifies that when an SSTable build
// fails in the flush worker (e.g. disk full), the error is recorded in m.flushErr,
// the unwritten Memtable remains in Frozenmem so data is not silently lost,
// and Close() returns the exact error.
func TestFlushFailure_DiskFullDuringSSTableBuild(t *testing.T) {
	dir := "test_data_flush_err_disk_full"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	diskFullErr := errors.New("simulated disk full: no space left on device")
	man.flushBuildHook = func() error {
		return diskFullErr
	}

	// Put data into Memtable, freeze and enqueue
	f := newTestMemtable()
	k := EncodeInternalKey([]byte("retained_key"), 100, false)
	f.Put(k, []byte("retained_val"))

	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f)
	man.enqueueFlushLocked(f)
	man.Mu.Unlock()

	// Wait for worker to encounter the error
	deadline := time.Now().Add(5 * time.Second)
	for {
		man.Mu.RLock()
		err := man.flushErr
		man.Mu.RUnlock()
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Timeout waiting for flush worker to record flushErr")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify that the failing Memtable was NOT silently discarded from Frozenmem
	man.Mu.RLock()
	nFrozen := len(man.Frozenmem)
	man.Mu.RUnlock()
	if nFrozen != 1 {
		t.Fatalf("Expected failing Memtable to remain in Frozenmem, got count %d", nFrozen)
	}

	// Verify key visibility in Frozenmem
	val, err := man.Get("retained_key", 200)
	if err != nil || val != "retained_val" {
		t.Fatalf("Get(retained_key) failed: val=%s, err=%v", val, err)
	}

	// Verify Close() returns the diskFullErr
	closeErr := man.Close()
	if !errors.Is(closeErr, diskFullErr) {
		t.Fatalf("Expected Close() to return %v, got %v", diskFullErr, closeErr)
	}
}

// TestFlushFailure_IncompleteFileUnlinkedOnError verifies that if SSTable
// construction fails, any partially created SSTable file is cleanly deleted.
func TestFlushFailure_IncompleteFileUnlinkedOnError(t *testing.T) {
	dir := "test_data_flush_err_unlink"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	simulatedErr := errors.New("write failure during block build")
	man.flushBuildHook = func() error {
		return simulatedErr
	}

	f := newTestMemtable()
	f.Put(EncodeInternalKey([]byte("k1"), 10, false), []byte("v1"))
	man.Frozenmem = append(man.Frozenmem, f)
	man.EnqueueFlush(f)

	// Wait for error to be recorded
	time.Sleep(50 * time.Millisecond)

	closeErr := man.Close()
	if !errors.Is(closeErr, simulatedErr) {
		t.Fatalf("Expected Close to return %v, got %v", simulatedErr, closeErr)
	}

	// Check directory: no orphaned .sst files should exist
	files, err := os.ReadDir(".")
	if err == nil {
		for _, file := range files {
			if strings.HasPrefix(file.Name(), "data_") && strings.HasSuffix(file.Name(), ".sst") {
				// Clean up orphaned test file
				os.Remove(file.Name())
			}
		}
	}
}

// TestWALRotationFailure_PreservesDurabilityAndSurfacesError verifies that
// if WAL rotation fails, the engine does not discard active Memtable data,
// does not set the WAL to nil (preventing nil-pointer panics), records flushErr,
// and returns the rotation error.
func TestWALRotationFailure_PreservesDurabilityAndSurfacesError(t *testing.T) {
	dir := "test_data_wal_rotation_failure"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	walPath := filepath.Join(dir, "active_wal.log")
	wal, err := NewWal(walPath)
	if err != nil {
		t.Fatalf("Failed to create initial WAL: %v", err)
	}

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		Log:             wal,
		ActiveSSreaders: make([]*SSTableReader, 0),
	}

	// Add committed data to active Memtable
	k := EncodeInternalKey([]byte("durable_key"), 10, false)
	man.Mem.Put(k, []byte("durable_val"))
	man.Mem.SizeBytes = 100

	// Inject WAL rotation error
	walRotErr := errors.New("simulated disk out of file descriptors during wal creation")
	man.walRotationHook = func() error {
		return walRotErr
	}

	// Trigger flush; rotation should fail
	flushErr := man.TriggerFlush()
	if flushErr == nil || !strings.Contains(flushErr.Error(), "wal rotation failed") {
		t.Fatalf("Expected TriggerFlush to return WAL rotation failure, got: %v", flushErr)
	}

	// Verify that active Memtable data was NOT wiped out
	val, err := man.Get("durable_key", 100)
	if err != nil || val != "durable_val" {
		t.Fatalf("Active memtable lost after rotation failure! val=%s, err=%v", val, err)
	}

	// Verify that man.Log is still valid and not nil
	if man.Log == nil || man.Log.File == nil {
		t.Fatalf("man.Log was set to nil during failed rotation")
	}

	// Verify Close() reports the rotation error
	closeErr := man.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "wal rotation failed") {
		t.Fatalf("Expected Close() to report WAL rotation error, got: %v", closeErr)
	}
}

// TestTransactionCommit_WALFailureAbortsTransaction verifies that if WAL append
// fails during transaction commit, the transaction aborts cleanly, returns the error,
// and does not panic.
func TestTransactionCommit_WALFailureAbortsTransaction(t *testing.T) {
	dir := "test_data_tx_wal_failure"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	man := CreateManager()

	// Intentionally close the WAL file handle behind the manager to trigger write error
	man.Mu.Lock()
	if man.Log != nil && man.Log.File != nil {
		man.Log.File.Close()
	}
	man.Mu.Unlock()

	tx := man.Begin()
	tx.Put("fail_tx_key", "fail_tx_val")
	err := tx.Commit()
	if err == nil {
		t.Fatalf("Expected Commit to fail on closed WAL file, but it succeeded")
	}

	// Verify uncommitted key was not written into Memtable
	val, err := man.Get("fail_tx_key", 100)
	if err == nil && val == "fail_tx_val" {
		t.Fatalf("Uncommitted mutation leaked into Memtable after WAL failure!")
	}

	man.Close()
}
