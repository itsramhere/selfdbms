package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestCP1V2_TransactionCommit_FlushTrigger_NoDeadlock verifies that when Transaction.Commit
// triggers checkAndTriggerFlush (e.g. when Mem.SizeBytes >= 64MB), it executes cleanly
// without self-deadlock on Manager.Mu.
func TestCP1V2_TransactionCommit_FlushTrigger_NoDeadlock(t *testing.T) {
	testDir := "test_data_cp1v2_nodeadlock"
	os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)
	defer os.RemoveAll(testDir)

	walPath := testDir + "/wal.log"
	cfg := Config{
		Mode:    SyncModeApproachA,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	var sstFiles []string
	defer func() {
		man.Close()
		for _, f := range sstFiles {
			os.Remove(f)
		}
	}()

	// Artificially prime active Memtable size to meet the 64MB threshold
	man.Mu.Lock()
	man.Mem.SizeBytes = 64 * 1024 * 1024
	man.Mu.Unlock()

	// Run commit with a strict timeout to catch any circular wait or recursive mutex lock
	done := make(chan error, 1)
	go func() {
		txn := man.Begin()
		txn.Put("trigger_key", "trigger_val")
		done <- txn.Commit()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Transaction.Commit failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("DEADLOCK DETECTED: Transaction.Commit timed out while executing checkAndTriggerFlush")
	}

	// Verify that checkAndTriggerFlush executed: active Memtable was rotated
	man.Mu.RLock()
	frozenCount := len(man.Frozenmem)
	man.Mu.RUnlock()

	if frozenCount == 0 {
		t.Fatalf("expected at least 1 frozen Memtable after threshold flush trigger, got 0")
	}

	// Verify point read still accesses the committed key
	val, err := man.Get("trigger_key", 100)
	if err != nil || val != "trigger_val" {
		t.Fatalf("expected 'trigger_val', got val=%q, err=%v", val, err)
	}

	// Wait for background flush worker to publish SSTable so it can be cleaned up
	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		readerCount := len(man.ActiveSSreaders)
		if readerCount > 0 {
			for _, r := range man.ActiveSSreaders {
				if r != nil && r.File != nil {
					sstFiles = append(sstFiles, r.File.Name())
				}
			}
			man.Mu.RUnlock()
			break
		}
		man.Mu.RUnlock()
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError tests whether
// Transaction.Commit aborts and returns an error when latestValueLocked encounters
// an unexpected I/O error reading an SSTable.
func TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError(t *testing.T) {
	testDir := "test_data_cp1v3_errorprop"
	os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)
	defer os.RemoveAll(testDir)

	walPath := testDir + "/wal.log"
	cfg := Config{
		Mode:    SyncModeApproachA,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	var sstFiles []string
	defer func() {
		man.Close()
		for _, f := range sstFiles {
			os.Remove(f)
		}
	}()

	// Commit a key and flush it to disk as an SSTable
	txn0 := man.Begin()
	txn0.Put("audit_key", "val0")
	if err := txn0.Commit(); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	// Wait for background worker to publish the SSTable
	pubDeadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		readerCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if readerCount > 0 {
			break
		}
		if time.Now().After(pubDeadline) {
			t.Fatalf("timed out waiting for SSTable publication")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Induce an unexpected I/O error by prematurely closing the SSTable file descriptor
	man.Mu.Lock()
	if len(man.ActiveSSreaders) == 0 {
		man.Mu.Unlock()
		t.Fatalf("expected at least 1 active SSTable reader")
	}
	targetReader := man.ActiveSSreaders[0]
	sstFiles = append(sstFiles, targetReader.File.Name())
	// Close the file handle to force subsequent Read operations to fail with os.ErrClosed
	targetReader.File.Close()
	man.Mu.Unlock()

	// Verify that latestValueLocked now returns an I/O error (not ErrNotFound)
	man.Mu.RLock()
	_, lookupErr := man.latestValueLocked([]byte("audit_key"))
	man.Mu.RUnlock()

	if lookupErr == nil || errors.Is(lookupErr, ErrNotFound) {
		t.Fatalf("expected unexpected I/O error from latestValueLocked, got %v", lookupErr)
	}
	t.Logf("Confirmed latestValueLocked returns unexpected error: %v", lookupErr)

	// Now attempt a transaction commit that touches audit_key
	txn1 := man.Begin()
	txn1.Put("audit_key", "val1")
	commitErr := txn1.Commit()

	// EXPECTATION: Transaction must NOT commit when conflict validation fails with I/O error!
	if commitErr == nil {
		t.Fatalf("DEFECT CONFIRMED: Transaction.Commit succeeded silently despite unexpected I/O error during conflict validation!")
	}
	t.Logf("Transaction.Commit properly rejected with: %v", commitErr)
}

// TestCP1V7_CleanShutdown_ActiveMemtableAndWALRecovery verifies that:
// 1. Manager.Close() does NOT flush the active below-threshold Memtable to disk.
// 2. Unflushed committed records remain durably in the WAL file on disk.
// 3. When a new Manager recovers via RecoverFromWAL(walPath), all committed records
//    and sequence numbers are restored accurately into the active Memtable.
// 4. If a transaction releases Manager.Mu before invoking checkAndTriggerFlush(),
//    and Manager.Close() completes first, checkAndTriggerFlush() exits cleanly without panic or deadlock.
func TestCP1V7_CleanShutdown_ActiveMemtableAndWALRecovery(t *testing.T) {
	testDir := "test_data_cp1v7_shutdown_wal"
	os.RemoveAll(testDir)
	os.MkdirAll(testDir, 0755)
	defer os.RemoveAll(testDir)

	walPath := testDir + "/wal.log"
	cfg := Config{
		Mode:    SyncModeApproachA,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Commit 5 keys with below-threshold payloads
	for i := 1; i <= 5; i++ {
		txn := man.Begin()
		txn.Put(fmt.Sprintf("key_%d", i), fmt.Sprintf("val_%d", i))
		if err := txn.Commit(); err != nil {
			t.Fatalf("txn %d commit failed: %v", i, err)
		}
	}

	// Verify pre-shutdown state: exactly 5 keys in active Memtable, 0 frozen, 0 SSTables
	man.Mu.RLock()
	activeSize := man.Mem.SizeBytes
	frozenCount := len(man.Frozenmem)
	sstCount := len(man.ActiveSSreaders)
	lastSeq := man.CurrentSeqNo.Load()
	man.Mu.RUnlock()

	if activeSize == 0 || frozenCount != 0 || sstCount != 0 || lastSeq != 5 {
		t.Fatalf("unexpected pre-shutdown state: activeSize=%d, frozen=%d, sst=%d, seq=%d",
			activeSize, frozenCount, sstCount, lastSeq)
	}

	// Perform clean shutdown
	if err := man.Close(); err != nil {
		t.Fatalf("Manager.Close failed: %v", err)
	}

	// Verify post-shutdown invariants:
	// 1. Active Memtable was NOT flushed to SSTable on Close (0 SSTables created)
	man.Mu.RLock()
	postSSTCount := len(man.ActiveSSreaders)
	postState := man.state
	man.Mu.RUnlock()

	if postSSTCount != 0 {
		t.Fatalf("expected 0 SSTables after clean shutdown of active Memtable, got %d", postSSTCount)
	}
	if postState != EngineClosed {
		t.Fatalf("expected state EngineClosed, got %v", postState)
	}

	// Verify that late checkAndTriggerFlush() exits cleanly without deadlock or mutation
	done := make(chan struct{})
	go func() {
		man.checkAndTriggerFlush()
		close(done)
	}()
	select {
	case <-done:
		// Clean exit
	case <-time.After(1 * time.Second):
		t.Fatalf("DEADLOCK: late checkAndTriggerFlush hung after Close()")
	}

	// Recover into a fresh manager instance using the same WAL file
	man2, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager2: %v", err)
	}
	defer man2.Close()

	if err := man2.RecoverFromWAL(walPath); err != nil {
		t.Fatalf("RecoverFromWAL failed: %v", err)
	}

	// Verify recovered state: CurrentSeqNo restored to 5, all 5 keys visible via point read
	if man2.CurrentSeqNo.Load() != 5 {
		t.Fatalf("expected recovered CurrentSeqNo=5, got %d", man2.CurrentSeqNo.Load())
	}

	for i := 1; i <= 5; i++ {
		val, err := man2.Get(fmt.Sprintf("key_%d", i), 100)
		if err != nil {
			t.Fatalf("failed to read recovered key_%d: %v", i, err)
		}
		expected := fmt.Sprintf("val_%d", i)
		if val != expected {
			t.Fatalf("key_%d mismatch: expected %q, got %q", i, expected, val)
		}
	}
}

