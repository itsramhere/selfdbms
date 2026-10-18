package lsmengine

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestApproachB_ConflictValidation_SnapshotAcquisitionFailureAbortsCommit verifies that if
// snapshot acquisition encounters an error, conflict validation fails fast and aborts commit,
// preventing unvalidated disk writes from committing silently (I3.7.2).
func TestApproachB_ConflictValidation_SnapshotAcquisitionFailureAbortsCommit(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	// Flush an SSTable containing key "conflict_k"
	tx1 := man.Begin()
	tx1.Put("conflict_k", "v1")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}

	for {
		man.flushQueueMu.Lock()
		qLen := len(man.flushQueue)
		man.flushQueueMu.Unlock()
		man.Mu.RLock()
		fLen := len(man.Frozenmem)
		man.Mu.RUnlock()
		if qLen == 0 && fLen == 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	// Begin transaction 2 before closing snapshot admission
	tx2 := man.Begin()
	tx2.Put("conflict_k", "v2")

	// Simulate admission closure (e.g. during shutdown initiation)
	man.snapshotAdmissionClosed.Store(true)

	// Attempting to commit MUST abort because disk snapshot cannot be acquired
	commitErr := tx2.Commit()
	if commitErr == nil {
		t.Fatalf("DEFECT: Transaction.Commit succeeded silently when snapshot acquisition failed!")
	}
	if !strings.Contains(commitErr.Error(), "conflict validation") {
		t.Fatalf("expected error mentioning conflict validation, got: %v", commitErr)
	}
}

// TestApproachB_ConflictValidation_CorruptedNilReaderAbortsCommit verifies that a nil reader
// in the snapshot during conflict validation causes commit abort rather than skipping the disk tier.
func TestApproachB_ConflictValidation_CorruptedNilReaderAbortsCommit(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	// Seed SSTable
	tx1 := man.Begin()
	tx1.Put("conflict_k", "v1")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}

	for {
		man.flushQueueMu.Lock()
		qLen := len(man.flushQueue)
		man.flushQueueMu.Unlock()
		man.Mu.RLock()
		fLen := len(man.Frozenmem)
		man.Mu.RUnlock()
		if qLen == 0 && fLen == 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	// Begin transaction 2 at readTs = 0 (an older transaction that should conflict with seq 1)
	tx2 := &Transaction{
		Man:      man,
		ReadTs:   0,
		WriteSet: make(map[string]PendingWrite),
	}
	tx2.Put("conflict_k", "v2")

	// Corrupt snapshot reader
	man.snapshotMu.Lock()
	sr := man.currentSnapshot.readers[0]
	origReader := sr.Reader
	sr.Reader = nil
	man.snapshotMu.Unlock()

	defer func() {
		sr.Reader = origReader
	}()

	commitErr := tx2.Commit()
	if commitErr == nil {
		t.Fatalf("DEFECT: Transaction.Commit succeeded silently when reader was nil, skipping conflict validation!")
	}
	if !strings.Contains(commitErr.Error(), "conflict validation failed") {
		t.Fatalf("expected conflict validation error, got: %v", commitErr)
	}
}

// TestApproachB_ConflictValidation_SSTableIOErrorAbortsCommit verifies that unexpected I/O errors
// on SSTables during conflict validation abort the transaction in Approach B (matching E-011 in Approach A).
func TestApproachB_ConflictValidation_SSTableIOErrorAbortsCommit(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	tx1 := man.Begin()
	tx1.Put("io_k", "v1")
	tx1.Commit()
	man.TriggerFlush()

	for {
		man.flushQueueMu.Lock()
		qLen := len(man.flushQueue)
		man.flushQueueMu.Unlock()
		man.Mu.RLock()
		fLen := len(man.Frozenmem)
		man.Mu.RUnlock()
		if qLen == 0 && fLen == 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	tx2 := man.Begin()
	tx2.Put("io_k", "v2")

	// Close SSTable file descriptor to simulate I/O fault
	man.snapshotMu.Lock()
	sr := man.currentSnapshot.readers[0]
	man.snapshotMu.Unlock()

	if err := sr.Reader.File.Close(); err != nil {
		t.Fatalf("failed to close file: %v", err)
	}

	commitErr := tx2.Commit()
	if commitErr == nil {
		t.Fatalf("DEFECT: Transaction.Commit succeeded silently when SSTable returned I/O error during conflict check!")
	}
	if !strings.Contains(commitErr.Error(), "conflict validation failed") {
		t.Fatalf("expected conflict validation error, got: %v", commitErr)
	}
}
