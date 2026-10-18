package lsmengine

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Helper to flush a key-value write
func flushSingleKV(t *testing.T, man *Manager, key, val string) {
	tx := man.Begin()
	tx.Put(key, val)
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
}

// 1. Category A: Obsolete SSTable from compaction with zero pins is closed and unlinked.
func TestSnapshotShutdown_CategoryA_CompactionObsoleteReclamation(t *testing.T) {
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

	flushSingleKV(t, man, "catA_1", "v1")
	flushSingleKV(t, man, "catA_2", "v2")

	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	in0Path := snap.Readers()[0].Path
	in1Path := snap.Readers()[1].Path
	man.ReleaseSnapshot(snap)

	// Run compaction with 0 pins held
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Wait briefly for background cleanup
	time.Sleep(50 * time.Millisecond)

	// In Category A, obsolete input SSTables must be unlinked
	if _, err := os.Stat(in0Path); !os.IsNotExist(err) {
		t.Fatalf("expected in0 to be unlinked, but file still exists: %s", in0Path)
	}
	if _, err := os.Stat(in1Path); !os.IsNotExist(err) {
		t.Fatalf("expected in1 to be unlinked, but file still exists: %s", in1Path)
	}
}

// 2. Category B: Retired snapshot with drained pins unlinks obsolete SSTables upon final release.
func TestSnapshotShutdown_CategoryB_RetiredSnapshotDrainedReclamation(t *testing.T) {
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

	flushSingleKV(t, man, "catB_1", "v1")
	flushSingleKV(t, man, "catB_2", "v2")

	// Pin snapshot containing input tables
	pinnedSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	in0Path := pinnedSnap.Readers()[0].Path
	in1Path := pinnedSnap.Readers()[1].Path

	// Compact while pinnedSnap is held: retires pinnedSnap and marks input readers obsolete
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// While pin is active: files MUST still exist on disk!
	if _, err := os.Stat(in0Path); os.IsNotExist(err) {
		t.Fatalf("in0 file was prematurely unlinked while pinned: %s", in0Path)
	}
	if _, err := os.Stat(in1Path); os.IsNotExist(err) {
		t.Fatalf("in1 file was prematurely unlinked while pinned: %s", in1Path)
	}

	// Release pin: triggers Category B reclamation
	man.ReleaseSnapshot(pinnedSnap)

	time.Sleep(50 * time.Millisecond)

	// Now files must be unlinked
	if _, err := os.Stat(in0Path); !os.IsNotExist(err) {
		t.Fatalf("expected in0 to be unlinked after release, but still exists: %s", in0Path)
	}
	if _, err := os.Stat(in1Path); !os.IsNotExist(err) {
		t.Fatalf("expected in1 to be unlinked after release, but still exists: %s", in1Path)
	}
}

// 3. Category C: Surviving valid SSTables on normal shutdown have file handles closed,
// but physical files are strictly PRESERVED on disk for recovery.
func TestSnapshotShutdown_CategoryC_SurvivingTablesPreservedOnDisk(t *testing.T) {
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

	flushSingleKV(t, man, "catC_1", "v1")
	flushSingleKV(t, man, "catC_2", "v2")

	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	var survivingPaths []string
	for _, sr := range snap.Readers() {
		survivingPaths = append(survivingPaths, sr.Path)
	}
	man.ReleaseSnapshot(snap)

	// Normal engine shutdown
	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Files MUST be preserved on disk!
	for _, p := range survivingPaths {
		if fi, err := os.Stat(p); os.IsNotExist(err) || fi.Size() == 0 {
			t.Fatalf("surviving SSTable was corrupted or deleted during shutdown: %s", p)
		}
	}
}

// 4. Category D: Shutdown waits for in-flight pinned readers to finish, then completes cleanly.
func TestSnapshotShutdown_CategoryD_ShutdownWaitsForPinnedReaders(t *testing.T) {
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

	flushSingleKV(t, man, "catD_1", "v1")

	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	survivingPath := snap.Readers()[0].Path

	// Start shutdown in background goroutine
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- man.Close()
	}()

	// Wait briefly: Close() must be blocked waiting for snap pin to be released
	select {
	case <-shutdownDone:
		t.Fatalf("Close() completed prematurely while snapshot was still pinned!")
	case <-time.After(100 * time.Millisecond):
		// Expected: Close() is actively waiting on snapshotDrainCond
	}

	// Now reader finishes and releases snapshot
	man.ReleaseSnapshot(snap)

	// Shutdown should now unblock and complete successfully
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for Close() to finish after releasing pin")
	}

	// Valid SSTable must remain on disk
	if _, err := os.Stat(survivingPath); os.IsNotExist(err) {
		t.Fatalf("SSTable was unlinked during Category D shutdown: %s", survivingPath)
	}
}

// 5. Windows NTFS Safety: Handles are closed before unlinking, avoiding sharing violations;
// os.RemoveAll succeeds immediately after engine Close.
func TestSnapshotShutdown_WindowsNTFSSafety(t *testing.T) {
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

	for i := 0; i < 5; i++ {
		flushSingleKV(t, man, fmt.Sprintf("ntfs_k%d", i), fmt.Sprintf("ntfs_v%d", i))
	}

	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// On Windows NTFS, if any file descriptor is leaked or held open, os.RemoveAll fails with
	// ERROR_SHARING_VIOLATION or Access Denied. Verify entire directory can be removed cleanly!
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("os.RemoveAll failed on NTFS directory after Close (file descriptor leak): %v", err)
	}
}

// 6. Concurrent Close: Multiple goroutines calling Close() concurrently do not hang or deadlock.
func TestSnapshotShutdown_ConcurrentClose(t *testing.T) {
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

	flushSingleKV(t, man, "conc_close_k", "v")

	const goroutines = 10
	var wg sync.WaitGroup
	var completed atomic.Int64

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = man.Close()
			completed.Add(1)
		}()
	}

	wg.Wait()

	if completed.Load() != int64(goroutines) {
		t.Fatalf("expected %d goroutines to finish Close(), got %d", goroutines, completed.Load())
	}
}
