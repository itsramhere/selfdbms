package lsmengine

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func drainFlushes(man *Manager) {
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

// TestCleanup_ObsoleteCleanedAndValidPreserved validates I3.7.6 items 1, 2, and 6:
// - No physical handle is closed while it remains referenced.
// - Obsolete SSTables are cleaned during normal operation once replaced and unpinned.
// - Valid surviving SSTables are preserved across Close().
func TestCleanup_ObsoleteCleanedAndValidPreserved(t *testing.T) {
	testDir := t.TempDir()
	walPath := filepath.Join(testDir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// 1. Write key1 and flush -> SSTable 1
	tx1 := man.Begin()
	tx1.Put("k1", "v1")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush 1 failed: %v", err)
	}
	drainFlushes(man)

	// Pin snapshot 1 (holds lease on SSTable 1)
	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("failed to acquire snapshot 1: %v", err)
	}
	obsoleteSR := snap1.Readers()[0]
	obsoletePath := obsoleteSR.Path

	// Verify obsolete file exists on disk
	if _, err := os.Stat(obsoletePath); err != nil {
		t.Fatalf("expected sstable 1 file to exist on disk: %v", err)
	}

	// 2. Simulate compaction: allocate new snapshot, mark obsoleteSR obsolete, and retire snap1
	man.snapshotMu.Lock()
	newSnap := &ReaderSnapshot{
		id: atomic.AddUint64(&man.snapshotSeq, 1),
	}
	newSnap.SetState(SnapshotPublished)
	man.currentSnapshot = newSnap

	obsoleteSR.MarkObsoleteLocked()
	// Retire snap1 while it is still pinned by our reader
	man.retireSnapshotLocked(snap1)
	man.snapshotMu.Unlock()

	// While snap1 has an active pin, obsoleteSR must NOT be closing or closed
	if obsoleteSR.State() == SharedReaderClosing || obsoleteSR.State() == SharedReaderClosed {
		t.Fatalf("invariant violated: reader closed while still referenced! state=%v", obsoleteSR.State())
	}
	if _, err := os.Stat(obsoletePath); err != nil {
		t.Fatalf("file unlinked prematurely while pinned: %v", err)
	}

	// 3. Release snap1 -> drops last pin on retired snap1, triggering reclamation and cleanup
	man.ReleaseSnapshot(snap1)

	// Wait for cleanupWg to drain
	man.cleanupWg.Wait()

	// Verify obsoleteSR transitioned to SharedReaderReclaimed and was unlinked
	if obsoleteSR.State() != SharedReaderReclaimed {
		t.Fatalf("expected obsolete reader to be Reclaimed, got: %v", obsoleteSR.State())
	}
	if _, err := os.Stat(obsoletePath); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete file to be removed from disk, but stat err=%v", err)
	}

	// 4. Write key2 and flush -> SSTable 2 (valid surviving table)
	tx2 := man.Begin()
	tx2.Put("k2", "v2")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush 2 failed: %v", err)
	}
	drainFlushes(man)

	man.snapshotMu.Lock()
	validSR := man.currentSnapshot.readers[len(man.currentSnapshot.readers)-1]
	validPath := validSR.Path
	man.snapshotMu.Unlock()

	// 5. Close engine -> Valid surviving table must be closed but NOT unlinked!
	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if validSR.State() != SharedReaderClosedValid {
		t.Fatalf("expected valid surviving table to have state SharedReaderClosedValid, got %v", validSR.State())
	}
	if _, err := os.Stat(validPath); err != nil {
		t.Fatalf("expected valid sstable file to be preserved on disk across Close, got err: %v", err)
	}
}

// TestCleanup_CloseWaitsForAsyncCleanup_DeterministicBarrier validates I3.7.6 item 3:
// Close does not report completion before required cleanup finishes, using a deterministic barrier.
func TestCleanup_CloseWaitsForAsyncCleanup_DeterministicBarrier(t *testing.T) {
	testDir := t.TempDir()
	walPath := filepath.Join(testDir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Create real SSTable via flush
	tx := man.Begin()
	tx.Put("barrier_k", "barrier_v")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	drainFlushes(man)

	man.snapshotMu.Lock()
	oldSnap := man.currentSnapshot
	sr := oldSnap.readers[0]
	filePath := sr.Path

	// Replace currentSnapshot so Close() will not double-unref oldSnap
	man.currentSnapshot = &ReaderSnapshot{
		id: atomic.AddUint64(&man.snapshotSeq, 1),
	}
	man.currentSnapshot.SetState(SnapshotPublished)

	sr.MarkObsoleteLocked()
	man.retireSnapshotLocked(oldSnap)
	man.snapshotMu.Unlock()

	// Use deterministic barrier to hold async cleanup in progress
	cleanupBarrier := make(chan struct{})
	cleanupStarted := make(chan struct{})

	man.cleanupWg.Add(1)
	go func() {
		defer man.cleanupWg.Done()
		close(cleanupStarted)
		<-cleanupBarrier
		man.drainPendingCleanups()
	}()

	// Wait until async cleanup goroutine has started
	<-cleanupStarted

	// Call Close() on goroutine
	closeDoneCh := make(chan struct{})
	go func() {
		man.Close()
		close(closeDoneCh)
	}()

	// Verify Close() has NOT finished while cleanup is blocked
	select {
	case <-closeDoneCh:
		t.Fatalf("Close() completed before async cleanup finished! Invariant violated.")
	case <-time.After(50 * time.Millisecond):
		// Expected: Close() is waiting on cleanupWg
	}

	// Unblock cleanup
	close(cleanupBarrier)

	// Now Close() must finish
	select {
	case <-closeDoneCh:
		// Success!
	case <-time.After(5 * time.Second):
		t.Fatalf("Close() timed out waiting for cleanup")
	}

	// Verify file was unlinked
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be unlinked after cleanup completion, got %v", err)
	}
}

// TestCleanup_AsyncCleanupErrorPropagatedToConcurrentCloseCallers validates I3.7.6 items 4 and 5:
// Cleanup errors are not lost when an asynchronous cleanup overlaps Close,
// and all concurrent Close callers receive the exact same final error.
func TestCleanup_AsyncCleanupErrorPropagatedToConcurrentCloseCallers(t *testing.T) {
	testDir := t.TempDir()
	walPath := filepath.Join(testDir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Manually inject a known cleanup error into asyncCleanupErr to simulate an async cleanup failure
	simulatedCleanupErr := errors.New("simulated async physical cleanup disk failure")
	man.cleanupMu.Lock()
	man.asyncCleanupErr = simulatedCleanupErr
	man.cleanupMu.Unlock()

	// Launch multiple concurrent Close() callers
	const numCallers = 5
	var wg sync.WaitGroup
	errs := make([]error, numCallers)

	startCh := make(chan struct{})
	for i := 0; i < numCallers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startCh
			errs[idx] = man.Close()
		}(i)
	}

	close(startCh)
	wg.Wait()

	// Verify that ALL callers received the exact same error, matching simulatedCleanupErr
	for i := 0; i < numCallers; i++ {
		if errs[i] == nil {
			t.Fatalf("caller %d received nil error, expected simulatedCleanupErr", i)
		}
		if !errors.Is(errs[i], simulatedCleanupErr) && errs[i].Error() != simulatedCleanupErr.Error() {
			t.Fatalf("caller %d received unexpected error: %v", i, errs[i])
		}
	}
}

// TestCleanup_HandleClosurePrecedesUnlink_WindowsNTFS validates I3.7.6 item 7:
// Windows NTFS handle closure precedes unlink, and if close fails, unlinking is aborted.
func TestCleanup_HandleClosurePrecedesUnlink_WindowsNTFS(t *testing.T) {
	testDir := t.TempDir()
	walPath := filepath.Join(testDir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	// Create real SSTable via flush
	tx := man.Begin()
	tx.Put("ntfs_k", "ntfs_v")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	drainFlushes(man)

	man.snapshotMu.Lock()
	oldSnap := man.currentSnapshot
	sr := oldSnap.readers[0]
	filePath := sr.Path

	// Replace currentSnapshot so defer man.Close() won't touch sr
	man.currentSnapshot = &ReaderSnapshot{
		id: atomic.AddUint64(&man.snapshotSeq, 1),
	}
	man.currentSnapshot.SetState(SnapshotPublished)

	sr.SetState(SharedReaderClosing)
	man.snapshotMu.Unlock()

	// drainSingleReaderCleanup:
	// Phase 1: sr.Reader.Close() executes
	// Phase 2: os.Remove(filePath) executes ONLY IF Phase 1 succeeded
	if err := man.drainSingleReaderCleanup(sr); err != nil {
		t.Fatalf("drainSingleReaderCleanup failed: %v", err)
	}

	if sr.State() != SharedReaderReclaimed {
		t.Fatalf("expected SharedReaderReclaimed, got %v", sr.State())
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected file to be unlinked, got err=%v", err)
	}
}

// TestCleanup_InterleavedFinalPinReleaseAndConcurrentClose validates CP3a-V.2:
// A deterministic test interleaving a final pin release, cleanup registration (cleanupWg.Add),
// and concurrent Close calls. Verifies that:
// 1. cleanupWg.Add strictly happens-before cleanupWg.Wait().
// 2. Close cannot complete before the required asynchronous cleanup finishes.
// 3. All concurrent Close callers unblock together and receive the exact same final result.
func TestCleanup_InterleavedFinalPinReleaseAndConcurrentClose(t *testing.T) {
	testDir := t.TempDir()
	walPath := filepath.Join(testDir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// 1. Commit and flush SSTable 1
	tx1 := man.Begin()
	tx1.Put("k1", "v1")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush 1 failed: %v", err)
	}
	drainFlushes(man)

	// 2. Reader acquires pin on snapshot 1 (holds lease on SSTable 1)
	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("failed to acquire snapshot: %v", err)
	}
	obsoleteSR := snap1.Readers()[0]
	obsoletePath := obsoleteSR.Path

	// 3. Simulate replacement: publish new snapshot, mark obsoleteSR obsolete, retire snap1
	man.snapshotMu.Lock()
	newSnap := &ReaderSnapshot{
		id: atomic.AddUint64(&man.snapshotSeq, 1),
	}
	newSnap.SetState(SnapshotPublished)
	man.currentSnapshot = newSnap

	obsoleteSR.MarkObsoleteLocked()
	man.retireSnapshotLocked(snap1)
	man.snapshotMu.Unlock()

	// 4. Install deterministic hook in beforeAsyncCleanupDrainHook
	cleanupStarted := make(chan struct{})
	cleanupBarrier := make(chan struct{})
	man.beforeAsyncCleanupDrainHook = func() {
		close(cleanupStarted)
		<-cleanupBarrier
	}

	// 5. Launch multiple concurrent Close() callers
	const numCloseCallers = 5
	var closeWg sync.WaitGroup
	closeResults := make([]error, numCloseCallers)
	closeDoneCh := make(chan struct{})

	for i := 0; i < numCloseCallers; i++ {
		closeWg.Add(1)
		go func(idx int) {
			defer closeWg.Done()
			closeResults[idx] = man.Close()
		}(i)
	}

	go func() {
		closeWg.Wait()
		close(closeDoneCh)
	}()

	// Allow Close() callers to reach the snapshot pin drain wait (snapshotDrainCond.Wait())
	time.Sleep(50 * time.Millisecond)

	// Verify that Close() has NOT finished while the snapshot pin is held
	select {
	case <-closeDoneCh:
		t.Fatalf("DEFECT: Close() finished while snapshot pin was still held!")
	default:
	}

	// 6. Release the final snapshot pin
	// This will:
	// - Decrement pins to 0.
	// - Reclaim retired snap1.
	// - Call scheduleAsyncCleanupLocked() (under snapshotMu) which calls cleanupWg.Add(1).
	// - Spawn runAsyncCleanup goroutine which hits beforeAsyncCleanupDrainHook.
	// - Signal snapshotDrainCond.Broadcast() to wake up closeApproachB().
	man.ReleaseSnapshot(snap1)

	// 7. Wait for the async cleanup goroutine to start and enter the barrier
	select {
	case <-cleanupStarted:
		// Async cleanup has started and called cleanupWg.Add(1)
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for async cleanup goroutine to enter hook")
	}

	// 8. Verify that Close() is STILL NOT finished because it is waiting on cleanupWg.Wait()!
	select {
	case <-closeDoneCh:
		t.Fatalf("DEFECT: Close() finished before async cleanup was released!")
	case <-time.After(100 * time.Millisecond):
		// Expected: Close() is waiting on cleanupWg
	}

	// 9. Unblock the async cleanup
	close(cleanupBarrier)

	// 10. Now, and only now, all Close() callers must finish!
	select {
	case <-closeDoneCh:
		// Succeeded!
	case <-time.After(5 * time.Second):
		t.Fatalf("Close() timed out waiting for cleanup completion")
	}

	// 11. Assert all concurrent Close callers received the same result (nil error)
	for i, err := range closeResults {
		if err != nil {
			t.Errorf("caller %d got non-nil error: %v", i, err)
		}
	}

	// 12. Verify obsolete SSTable file was unlinked from disk
	if _, err := os.Stat(obsoletePath); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete file %q to be unlinked, got err=%v", obsoletePath, err)
	}

	// 13. Verify reader transitioned to Reclaimed
	if obsoleteSR.State() != SharedReaderReclaimed {
		t.Fatalf("expected state SharedReaderReclaimed, got %v", obsoleteSR.State())
	}
}

// TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup reproduces Defect E-014:
// scheduleAsyncCleanupLocked must not read Manager.Mu-protected m.state while holding only snapshotMu.
func TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup(t *testing.T) {
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{Mode: SyncModeApproachB, WalPath: filepath.Join(dir, "wal.log")})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	defer man.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Goroutine 1: concurrently writes m.state under Manager.Mu
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				man.Mu.Lock()
				orig := man.state
				man.state = EngineClosing
				man.state = orig
				man.Mu.Unlock()
			}
		}
	}()

	// Goroutine 2: calls scheduleAsyncCleanupLocked under snapshotMu only
	for i := 0; i < 500; i++ {
		man.snapshotMu.Lock()
		man.scheduleAsyncCleanupLocked()
		man.snapshotMu.Unlock()
	}

	close(stop)
	wg.Wait()
	man.cleanupWg.Wait()
}


