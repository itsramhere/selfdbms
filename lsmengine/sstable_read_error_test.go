package lsmengine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestApproachB_UnexpectedReadError_PropagatesError verifies that an unexpected physical read error
// on a newer SSTable propagates immediately, rather than silently falling back to an older version (E-012).
func TestApproachB_UnexpectedReadError_PropagatesError(t *testing.T) {
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

	// 1. Older SSTable: k1 = "v1"
	tx1 := man.Begin()
	tx1.Put("k1", "v1")
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

	// 2. Newer SSTable: k1 = "v2"
	tx2 := man.Begin()
	tx2.Put("k1", "v2")
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

	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	if len(snap.Readers()) != 2 {
		man.ReleaseSnapshot(snap)
		t.Fatalf("expected 2 readers, got %d", len(snap.Readers()))
	}
	man.ReleaseSnapshot(snap)

	// 3. Inject unexpected physical read error into the newer SSTable reader (at index 1)
	// by closing its file descriptor.
	man.snapshotMu.Lock()
	newerSR := man.currentSnapshot.readers[1]
	man.snapshotMu.Unlock()

	if newerSR.Reader == nil || newerSR.Reader.File == nil {
		t.Fatalf("newer reader or file is nil")
	}
	// Close the file descriptor underneath the reader
	if err := newerSR.Reader.File.Close(); err != nil {
		t.Fatalf("failed to close file: %v", err)
	}

	// 4. Call B-mode Manager.Get
	ts := man.CurrentSeqNo.Load()
	val, getErr := man.Get("k1", ts)

	// 5. Verify the engine does NOT return the older value ("v1") or ErrNotFound
	if val == "v1" {
		t.Fatalf("DEFECT E-012 REPRODUCED: getApproachB swallowed physical read error on newer SSTable and returned stale older value %q!", val)
	}
	if errors.Is(getErr, ErrNotFound) {
		t.Fatalf("DEFECT E-012 REPRODUCED: getApproachB swallowed physical read error on newer SSTable and returned ErrNotFound!")
	}

	// 6. Verify it reports the original unexpected error
	if getErr == nil {
		t.Fatalf("expected error from read on closed file, got nil error and val=%q", val)
	}
	if !errors.Is(getErr, os.ErrClosed) && getErr.Error() == "" {
		t.Fatalf("expected non-empty I/O error, got: %v", getErr)
	}
}

// TestApproachB_UnexpectedReadError_SingleSSTable verifies that an unexpected error on a single SSTable
// is not converted to ErrNotFound.
func TestApproachB_UnexpectedReadError_SingleSSTable(t *testing.T) {
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

	tx := man.Begin()
	tx.Put("single_k", "single_v")
	if err := tx.Commit(); err != nil {
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

	man.snapshotMu.Lock()
	sr := man.currentSnapshot.readers[0]
	man.snapshotMu.Unlock()

	// Close the file descriptor
	if err := sr.Reader.File.Close(); err != nil {
		t.Fatalf("failed to close file: %v", err)
	}

	ts := man.CurrentSeqNo.Load()
	val, getErr := man.Get("single_k", ts)

	if errors.Is(getErr, ErrNotFound) {
		t.Fatalf("DEFECT E-012 REPRODUCED: getApproachB converted unexpected read error to ErrNotFound!")
	}
	if getErr == nil {
		t.Fatalf("expected error on read from closed file, got nil and val=%q", val)
	}
}

// TestApproachB_NilReaderInSnapshot_DoesNotFallBack verifies that a corrupted nil reader in a snapshot
// causes an immediate error rather than silently continuing to older tables.
func TestApproachB_NilReaderInSnapshot_DoesNotFallBack(t *testing.T) {
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

	// 1. Older table: k1 = "v1"
	tx1 := man.Begin()
	tx1.Put("k1", "v1")
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

	// 2. Newer table: k1 = "v2"
	tx2 := man.Begin()
	tx2.Put("k1", "v2")
	tx2.Commit()
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

	// Corrupt newer reader: set Reader = nil
	man.snapshotMu.Lock()
	newerSR := man.currentSnapshot.readers[1]
	oldReader := newerSR.Reader
	newerSR.Reader = nil
	man.snapshotMu.Unlock()

	defer func() {
		// Restore to avoid panic in cleanup
		newerSR.Reader = oldReader
	}()

	ts := man.CurrentSeqNo.Load()
	val, getErr := man.Get("k1", ts)

	// If it silently continues, it returns "v1"!
	if val == "v1" {
		t.Fatalf("DEFECT E-012 REPRODUCED: getApproachB silently skipped nil reader and returned stale older value %q!", val)
	}
	if getErr == nil {
		t.Fatalf("expected error on nil reader, got nil error and val=%q", val)
	}
}
