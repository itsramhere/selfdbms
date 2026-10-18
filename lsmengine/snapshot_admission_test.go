package lsmengine

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestApproachB_ReadAdmission_EngineClosing_ConsistentTierRejection validates I3.7.3:
// Enforces a consistent admission policy for new Get operations during EngineClosing:
// A. A key present in the active Memtable.
// B. A key present only in an SSTable.
// C. Close reaching EngineClosing before Get admission rejects new requests across all tiers with ErrClosed.
// D. A previously pinned disk read continuing safely after Close begins and completing without error.
func TestApproachB_ReadAdmission_EngineClosing_ConsistentTierRejection(t *testing.T) {
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

	// 1. Write keyB and flush to disk -> KeyB is present ONLY in an SSTable
	tx1 := man.Begin()
	tx1.Put("sst_key", "sst_val")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("failed to commit tx1: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("failed to trigger flush: %v", err)
	}

	// Drain flush queue
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

	// 2. Write keyA to active memtable -> KeyA is present ONLY in active Memtable
	tx2 := man.Begin()
	tx2.Put("mem_key", "mem_val")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("failed to commit tx2: %v", err)
	}

	// Verify both keys are discoverable before shutdown
	valA, errA := man.Get("mem_key", 100)
	if errA != nil || valA != "mem_val" {
		t.Fatalf("expected mem_val, got val=%s, err=%v", valA, errA)
	}
	valB, errB := man.Get("sst_key", 100)
	if errB != nil || valB != "sst_val" {
		t.Fatalf("expected sst_val, got val=%s, err=%v", valB, errB)
	}

	// 3. (Case D) Pre-admit a disk read BEFORE Close begins by pinning the current snapshot
	preAdmittedSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("failed to acquire pre-admitted snapshot: %v", err)
	}
	if preAdmittedSnap == nil {
		t.Fatalf("expected non-nil snapshot")
	}

	// 4. (Case C) Start Close() on another goroutine.
	// Since preAdmittedSnap has active pins, closeApproachB will enter EngineClosing,
	// retire currentSnapshot, and wait on snapshotDrainCond for pins to drain to zero.
	closeErrCh := make(chan error, 1)
	go func() {
		closeErrCh <- man.Close()
	}()

	// Wait until Close() has transitioned state to EngineClosing
	deadline := time.Now().Add(5 * time.Second)
	for {
		man.Mu.RLock()
		st := man.state
		man.Mu.RUnlock()
		if st == EngineClosing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for manager to enter EngineClosing (current state: %v)", st)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Verify snapshot admission is closed
	if !man.snapshotAdmissionClosed.Load() {
		t.Fatalf("expected snapshotAdmissionClosed to be true during EngineClosing")
	}

	// 5. Test new Get admissions while in EngineClosing:
	// Case A: Key present in active Memtable
	newMemVal, newMemErr := man.Get("mem_key", 100)
	if !errors.Is(newMemErr, ErrClosed) {
		t.Fatalf("Case A failed: expected ErrClosed for active Memtable key during EngineClosing, got val=%q, err=%v", newMemVal, newMemErr)
	}
	if newMemVal != "" {
		t.Fatalf("Case A failed: expected empty string on ErrClosed, got %q", newMemVal)
	}

	// Case B: Key present only in SSTable
	newSstVal, newSstErr := man.Get("sst_key", 100)
	if !errors.Is(newSstErr, ErrClosed) {
		t.Fatalf("Case B failed: expected ErrClosed for SSTable key during EngineClosing, got val=%q, err=%v", newSstVal, newSstErr)
	}
	if newSstVal != "" {
		t.Fatalf("Case B failed: expected empty string on ErrClosed, got %q", newSstVal)
	}

	// Also verify missing key during EngineClosing
	newMissingVal, newMissingErr := man.Get("missing_key", 100)
	if !errors.Is(newMissingErr, ErrClosed) {
		t.Fatalf("expected ErrClosed for missing key during EngineClosing, got val=%q, err=%v", newMissingVal, newMissingErr)
	}

	// 6. (Case D) Pre-admitted query continues safely with zero locks held
	readers := preAdmittedSnap.Readers()
	if len(readers) == 0 {
		t.Fatalf("expected at least one reader in pre-admitted snapshot")
	}
	readVal, readErr := readers[0].Reader.Get([]byte("sst_key"), 100)
	if readErr != nil {
		t.Fatalf("Case D failed: pre-admitted read failed: %v", readErr)
	}
	if string(readVal) != "sst_val" {
		t.Fatalf("Case D failed: expected sst_val from pre-admitted read, got %q", string(readVal))
	}

	// Release pre-admitted snapshot pin to unblock Close()
	man.ReleaseSnapshot(preAdmittedSnap)

	// Close() should now unblock and complete successfully
	select {
	case closeErr := <-closeErrCh:
		if closeErr != nil {
			t.Fatalf("Manager.Close failed: %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Close() to finish after releasing pin")
	}

	// Post-shutdown verification: state must be EngineClosed
	man.Mu.RLock()
	finalState := man.state
	man.Mu.RUnlock()
	if finalState != EngineClosed {
		t.Fatalf("expected final state EngineClosed, got %v", finalState)
	}

	// Post-shutdown calls must still return ErrClosed across all tiers
	if _, err := man.Get("mem_key", 100); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed post-shutdown for mem_key, got %v", err)
	}
	if _, err := man.Get("sst_key", 100); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed post-shutdown for sst_key, got %v", err)
	}
}

// TestApproachA_ReadAdmission_EngineClosing validates consistent read admission in Approach A.
func TestApproachA_ReadAdmission_EngineClosing(t *testing.T) {
	testDir := t.TempDir()
	walPath := filepath.Join(testDir, "wal.log")
	cfg := Config{
		Mode:    SyncModeApproachA,
		WalPath: walPath,
	}

	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	// Write SSTable key
	tx1 := man.Begin()
	tx1.Put("sst_key", "sst_val")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("failed to commit tx1: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("failed to trigger flush: %v", err)
	}

	// Drain flush queue
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

	// Write Memtable key
	tx2 := man.Begin()
	tx2.Put("mem_key", "mem_val")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("failed to commit tx2: %v", err)
	}

	// Hook to pause flush during shutdown to hold state at EngineClosing
	flushBlocker := make(chan struct{})
	flushStarted := make(chan struct{})
	var once sync.Once
	man.flushBuildHook = func() error {
		once.Do(func() {
			close(flushStarted)
			<-flushBlocker
		})
		return nil
	}

	// Freeze memtable and queue flush
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("failed to trigger flush: %v", err)
	}

	// Wait until flushBuildHook has entered
	<-flushStarted

	// Call Close() on goroutine
	closeErrCh := make(chan error, 1)
	go func() {
		closeErrCh <- man.Close()
	}()

	// Wait until Approach A enters EngineClosing
	deadline := time.Now().Add(5 * time.Second)
	for {
		man.Mu.RLock()
		st := man.state
		man.Mu.RUnlock()
		if st == EngineClosing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Approach A to enter EngineClosing")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// While in EngineClosing, both RAM and SSTable keys must be rejected with ErrClosed
	if _, err := man.Get("mem_key", 100); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed during EngineClosing for mem_key in Approach A, got %v", err)
	}
	if _, err := man.Get("sst_key", 100); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed during EngineClosing for sst_key in Approach A, got %v", err)
	}

	// Unblock flush
	close(flushBlocker)

	select {
	case closeErr := <-closeErrCh:
		if closeErr != nil {
			t.Fatalf("Approach A Close failed: %v", closeErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Approach A Close")
	}
}
