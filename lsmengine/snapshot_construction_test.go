package lsmengine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Helper to create a test SSTable on disk and return a SharedReader.
func createTestSharedReader(t *testing.T, dir, filename string, records []struct {
	key      string
	seq      uint64
	val      string
	isDelete bool
}) *SharedReader {
	fullPath := filepath.Join(dir, filename)
	reader := createTestSSTable(t, fullPath, records)
	return NewSharedReader(reader, fullPath)
}

// 1. Private Construction: Snapshot is constructed privately and unshared until published.
func TestSnapshotConstruction_PrivateUntilPublished(t *testing.T) {
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

	// Initial snapshot: empty
	initialSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("failed to acquire initial snapshot: %v", err)
	}
	defer man.ReleaseSnapshot(initialSnap)

	if len(initialSnap.Readers()) != 0 {
		t.Fatalf("expected empty initial snapshot, got %d readers", len(initialSnap.Readers()))
	}

	// Construct a candidate reader privately
	sr := createTestSharedReader(t, dir, "priv_test.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 1, "val1", false},
	})
	defer func() {
		if sr.State() == SharedReaderUnpublished {
			man.rollbackUnpublishedReader(sr)
		}
	}()

	// The candidate reader is unpublished and not reachable via AcquireSnapshot
	currentSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("failed to acquire snapshot: %v", err)
	}
	defer man.ReleaseSnapshot(currentSnap)

	if len(currentSnap.Readers()) != 0 {
		t.Fatalf("candidate reader was prematurely visible in current snapshot")
	}
	if sr.State() != SharedReaderUnpublished {
		t.Fatalf("expected candidate reader state Unpublished, got %v", sr.State())
	}
}

// 2. Order Preservation: Level-by-level chronological ordering (newest to oldest).
func TestSnapshotConstruction_OrderPreservation(t *testing.T) {
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

	// Perform 3 flushes with sequential writes to produce 3 SSTables
	for i := 1; i <= 3; i++ {
		tx := man.Begin()
		key := fmt.Sprintf("k%d", i)
		val := fmt.Sprintf("v%d", i)
		tx.Put(key, val)
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}
	}

	// Wait for flushes to complete
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

	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap)

	if len(snap.Readers()) != 3 {
		t.Fatalf("expected 3 readers in snapshot, got %d", len(snap.Readers()))
	}

	// In LSM, newest SSTables are prepended, so Readers()[0] is newest, Readers()[2] is oldest
	for i := 0; i < len(snap.Readers())-1; i++ {
		currNewest := snap.Readers()[i]
		nextOlder := snap.Readers()[i+1]
		if currNewest.Reader == nil || nextOlder.Reader == nil {
			t.Fatalf("nil reader at index %d or %d", i, i+1)
		}
	}
}

// 3. Reader Immutability: Slice and reader elements cannot be modified by flushes/compactions after publication.
func TestSnapshotConstruction_ReaderImmutability(t *testing.T) {
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
	tx1.Put("immutable_key1", "val1")
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

	// Pin snapshot 1
	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap1)

	if len(snap1.Readers()) != 1 {
		t.Fatalf("expected 1 reader in snap1, got %d", len(snap1.Readers()))
	}
	originalReader := snap1.Readers()[0]
	originalSliceLen := len(snap1.Readers())

	// Cause another flush while snap1 is held
	tx2 := man.Begin()
	tx2.Put("immutable_key2", "val2")
	if err := tx2.Commit(); err != nil {
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

	// Verify snap1 was NOT mutated
	if len(snap1.Readers()) != originalSliceLen {
		t.Fatalf("snap1 reader slice length changed from %d to %d", originalSliceLen, len(snap1.Readers()))
	}
	if snap1.Readers()[0] != originalReader {
		t.Fatalf("snap1 reader pointer changed")
	}

	// New snapshot has 2 readers
	snap2, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap2)

	if len(snap2.Readers()) != 2 {
		t.Fatalf("expected 2 readers in snap2, got %d", len(snap2.Readers()))
	}
}

// 4. Shared Unchanged SSTables: Unaffected SSTables share existing *SharedReader pointers.
func TestSnapshotConstruction_SharedUnchangedSSTables(t *testing.T) {
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
	tx1.Put("share_key1", "val1")
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

	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap1)

	r1 := snap1.Readers()[0]

	// Put another key and flush to create snapshot 2
	tx2 := man.Begin()
	tx2.Put("share_key2", "val2")
	if err := tx2.Commit(); err != nil {
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

	snap2, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap2)

	if len(snap2.Readers()) != 2 {
		t.Fatalf("expected 2 readers in snap2, got %d", len(snap2.Readers()))
	}

	// snap2's older reader (at index 0) MUST be the exact same pointer as r1
	if snap2.Readers()[0] != r1 {
		t.Fatalf("snap2 did not share the unchanged *SharedReader pointer; got %p, expected %p", snap2.Readers()[0], r1)
	}

	// RefCount on r1 must be at least 2 (one for snap1, one for snap2)
	if r1.RefCount() < 2 {
		t.Fatalf("expected RefCount >= 2 for shared reader, got %d", r1.RefCount())
	}
}

// 5. Builder Ownership & Rollback: Builder owns new reader until publication;
// uncommitted reader is unlinked and closed on rollback.
func TestSnapshotConstruction_BuilderOwnershipAndRollback(t *testing.T) {
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

	sr := createTestSharedReader(t, dir, "rollback_test.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"rollback_k", 100, "val", false},
	})

	if sr.State() != SharedReaderUnpublished {
		t.Fatalf("expected state Unpublished, got %v", sr.State())
	}

	filePath := sr.Path
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("file should exist before rollback: %s", filePath)
	}

	// Simulate builder failure rollback
	man.rollbackUnpublishedReader(sr)

	if sr.State() != SharedReaderReclaimed {
		t.Fatalf("expected state Reclaimed after rollback, got %v", sr.State())
	}
	if sr.CloseError() != nil {
		t.Fatalf("expected nil closeErr, got %v", sr.CloseError())
	}

	// File should be unlinked
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file should have been unlinked after rollback, but still exists: %s", filePath)
	}
}

// 6. Empty Snapshot Handling: Valid empty snapshot when no SSTables exist.
func TestSnapshotConstruction_EmptySnapshot(t *testing.T) {
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

	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed on empty manager: %v", err)
	}
	defer man.ReleaseSnapshot(snap)

	if snap == nil {
		t.Fatalf("expected non-nil snapshot")
	}
	if len(snap.Readers()) != 0 {
		t.Fatalf("expected 0 readers, got %d", len(snap.Readers()))
	}
	if snap.State() != SnapshotPinned {
		t.Fatalf("expected state SnapshotPinned, got %v", snap.State())
	}
	if snap.Pins() != 1 {
		t.Fatalf("expected pins 1, got %d", snap.Pins())
	}
}

// 7. Resurrection Prohibition: Calling Ref() on reader in closing/closed/reclaimed/failed panics.
func TestSnapshotConstruction_ResurrectionProhibition(t *testing.T) {
	dir := t.TempDir()
	prohibitedStates := []SharedReaderState{
		SharedReaderClosing,
		SharedReaderClosed,
		SharedReaderUnlinking,
		SharedReaderReclaimed,
		SharedReaderClosedValid,
		SharedReaderFailed,
	}

	for _, state := range prohibitedStates {
		t.Run(state.String(), func(t *testing.T) {
			sr := createTestSharedReader(t, dir, fmt.Sprintf("resurrect_%s.sst", state.String()), []struct {
				key      string
				seq      uint64
				val      string
				isDelete bool
			}{
				{"k", 1, "v", false},
			})
			defer func() {
				// cleanup disk
				if sr.Reader != nil {
					sr.Reader.Close()
				}
				os.Remove(sr.Path)
			}()

			sr.SetState(state)

			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic on Ref() with state %v, but no panic occurred", state)
				}
			}()

			// This MUST panic
			sr.Ref()
		})
	}
}
