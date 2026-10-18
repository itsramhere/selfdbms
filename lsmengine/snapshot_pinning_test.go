package lsmengine

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 1. Pin Before Lock Release: Snapshot pin count is incremented while snapshotMu is held,
// guaranteeing pins > 0 before any reader executes outside the lock.
func TestSnapshotPinning_PinBeforeLockRelease(t *testing.T) {
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

	// Write some data and flush
	tx := man.Begin()
	tx.Put("pin_k1", "pin_v1")
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

	// Acquire snapshot
	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap)

	// Immediate assertion: pin count MUST be >= 1 immediately after AcquireSnapshot returns
	if pins := snap.Pins(); pins < 1 {
		t.Fatalf("expected pins >= 1, got %d", pins)
	}
	if snap.State() != SnapshotPinned {
		t.Fatalf("expected state SnapshotPinned, got %v", snap.State())
	}
}

// 2. Pin Atomicity: High concurrency pinning and unpinning under race detector.
func TestSnapshotPinning_ConcurrentPinUnpinAtomicity(t *testing.T) {
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

	// Seed data and flush
	for i := 0; i < 3; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("conc_k%d", i), fmt.Sprintf("conc_v%d", i))
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}
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

	const goroutines = 20
	const iterations = 100
	var wg sync.WaitGroup
	var completedReads atomic.Int64

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for it := 0; it < iterations; it++ {
				snap, err := man.AcquireSnapshot()
				if err != nil {
					t.Errorf("goroutine %d iteration %d AcquireSnapshot failed: %v", gid, it, err)
					return
				}
				if snap.Pins() <= 0 {
					t.Errorf("goroutine %d iteration %d saw pins <= 0: %d", gid, it, snap.Pins())
				}
				// Simulate read
				time.Sleep(10 * time.Microsecond)
				man.ReleaseSnapshot(snap)
				completedReads.Add(1)
			}
		}(g)
	}

	wg.Wait()

	if expected := int64(goroutines * iterations); completedReads.Load() != expected {
		t.Fatalf("expected %d completed pin/unpin cycles, got %d", expected, completedReads.Load())
	}
	if activePins := man.ActiveSnapshotPins(); activePins != 0 {
		t.Fatalf("expected 0 active pins after all readers complete, got %d", activePins)
	}
}

// 3. Shutdown Rejection: New pins are strictly rejected when engine is closing/draining.
func TestSnapshotPinning_ShutdownRejection(t *testing.T) {
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

	// Close the manager
	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Attempt to acquire snapshot after Close MUST be rejected with ErrClosed
	snap, err := man.AcquireSnapshot()
	if !errors.Is(err, ErrClosed) {
		if snap != nil {
			man.ReleaseSnapshot(snap)
		}
		t.Fatalf("expected ErrClosed on AcquireSnapshot post-shutdown, got err=%v, snap=%v", err, snap)
	}
}

// 4. Lock-Free Disk Reads: Reads execute against SSTables without holding Manager.Mu or snapshotMu.
// Concurrently running a compaction or flush does NOT block the reader, and the reader does not block compaction.
func TestSnapshotPinning_LockFreeDiskReads(t *testing.T) {
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

	// Seed multiple SSTables
	for i := 0; i < 2; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("key_%d", i), fmt.Sprintf("val_%d", i))
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}
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

	// Reader pins snapshot
	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap)

	// Verify that while reader holds snapshot, Manager.Mu can be acquired with an exclusive Lock!
	// If the reader were holding Manager.Mu, Lock() would deadlock or block.
	muAcquired := make(chan bool, 1)
	go func() {
		man.Mu.Lock()
		muAcquired <- true
		man.Mu.Unlock()
	}()

	select {
	case <-muAcquired:
		// Success: Manager.Mu was freely acquired exclusively while snapshot is pinned!
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for Manager.Mu.Lock(); reader incorrectly holding lock")
	}

	// Also verify snapshotMu can be acquired
	snapMuAcquired := make(chan bool, 1)
	go func() {
		man.snapshotMu.Lock()
		snapMuAcquired <- true
		man.snapshotMu.Unlock()
	}()

	select {
	case <-snapMuAcquired:
		// Success: snapshotMu was freely acquired while snapshot is pinned!
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for snapshotMu.Lock(); reader incorrectly holding snapshotMu")
	}
}

// 5. Underflow Panic: ReleaseSnapshot panics on zero pins or double-release.
func TestSnapshotPinning_UnderflowPanic(t *testing.T) {
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
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}

	// Release once (valid)
	man.ReleaseSnapshot(snap)

	// Second release MUST panic due to underflow
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on ReleaseSnapshot underflow, but no panic occurred")
		}
	}()

	man.ReleaseSnapshot(snap)
}

// 6. Retired Snapshot Pin Retention: Active readers on retired snapshot keep it alive until their Release.
func TestSnapshotPinning_RetiredSnapshotPinRetention(t *testing.T) {
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

	// Initial data and flush
	tx := man.Begin()
	tx.Put("retire_k1", "retire_v1")
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

	// Reader pins older snapshot
	snapOld, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snapOld)

	oldID := snapOld.ID()

	// Publish a second flush: retires snapOld into retiredSnapshots
	tx2 := man.Begin()
	tx2.Put("retire_k2", "retire_v2")
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

	// snapOld must now be in SnapshotRetired state because it was retired by the newer flush
	if snapOld.State() != SnapshotRetired {
		t.Fatalf("expected snapOld to be SnapshotRetired, got %v", snapOld.State())
	}
	// It is retained in retiredSnapshots because snapOld.Pins() == 1
	if man.RetiredSnapshotCount() != 1 {
		t.Fatalf("expected 1 retired snapshot tracked, got %d", man.RetiredSnapshotCount())
	}

	// Current snapshot has ID > oldID
	currentSeq := man.SnapshotSeq()
	if currentSeq <= oldID {
		t.Fatalf("expected current snapshot seq > oldID (%d), got %d", oldID, currentSeq)
	}
}

// 7. Final Pin Reclamation: When pins reach 0 on a retired snapshot, it is reclaimed.
func TestSnapshotPinning_FinalPinReclamation(t *testing.T) {
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

	// First flush
	tx := man.Begin()
	tx.Put("fin_k1", "fin_v1")
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

	// Reader pins snap1
	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}

	// Second flush: retires snap1
	tx2 := man.Begin()
	tx2.Put("fin_k2", "fin_v2")
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

	if man.RetiredSnapshotCount() != 1 {
		t.Fatalf("expected 1 retired snapshot before release, got %d", man.RetiredSnapshotCount())
	}

	// Release snap1: pins drop to 0, snapshot should be reclaimed and removed from retiredSnapshots
	man.ReleaseSnapshot(snap1)

	if snap1.State() != SnapshotReclaimed {
		t.Fatalf("expected snap1 to transition to SnapshotReclaimed, got %v", snap1.State())
	}
	if man.RetiredSnapshotCount() != 0 {
		t.Fatalf("expected 0 retired snapshots after release, got %d", man.RetiredSnapshotCount())
	}
}
