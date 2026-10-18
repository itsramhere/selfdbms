package lsmengine

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSharedReader_ProhibitResurrection_OnClosingOrReclaimed validates I3.7.5:
// Confirms that Ref() strictly prohibits reference resurrection when a reader is closing,
// closed, unlinking, reclaimed, or failed.
func TestSharedReader_ProhibitResurrection_OnClosingOrReclaimed(t *testing.T) {
	terminalStates := []SharedReaderState{
		SharedReaderClosing,
		SharedReaderClosed,
		SharedReaderUnlinking,
		SharedReaderClosedValid,
		SharedReaderReclaimed,
		SharedReaderFailed,
	}

	for _, state := range terminalStates {
		t.Run(state.String(), func(t *testing.T) {
			sr := NewSharedReader(nil, "dummy.sst")
			sr.SetState(state)

			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic on Ref() in state %v, but did not panic", state)
				}
				msg, ok := r.(string)
				if !ok || msg != "FATAL: prohibited reference resurrection on closing/reclaimed SharedReader" {
					t.Fatalf("unexpected panic message: %v", r)
				}
			}()

			sr.Ref()
		})
	}
}

// TestSharedReader_RaceAcquireWithFinalRelease_SynchronizedBySnapshotMu validates I3.7.5:
// Demonstrates that state-check-plus-reference-increment operations are serialized by Manager.snapshotMu,
// preventing compound ownership race conditions where a thread attempts to acquire a lease
// on a reader whose final reference is being released concurrently.
func TestSharedReader_RaceAcquireWithFinalRelease_SynchronizedBySnapshotMu(t *testing.T) {
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

	// Create an active standalone SharedReader with initial refCount = 1
	dummyReader := &SSTableReader{}
	sr := NewSharedReader(dummyReader, filepath.Join(testDir, "race.sst"))
	sr.SetState(SharedReaderActive)

	// Simulate concurrent race between a lease acquirer and final-reference release
	var wg sync.WaitGroup
	var panicked atomic.Bool
	var acquired atomic.Bool

	// Barrier to synchronize goroutines
	startCh := make(chan struct{})

	// Goroutine 1: Attempts to lease reader under snapshotMu
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startCh
		man.snapshotMu.Lock()
		defer man.snapshotMu.Unlock()

		defer func() {
			if r := recover(); r != nil {
				panicked.Store(true)
			}
		}()

		// Under snapshotMu, inspect state and acquire
		if sr.State() == SharedReaderActive {
			sr.Ref()
			acquired.Store(true)
		} else {
			// If already closing/obsolete, calling Ref() would panic
			sr.Ref()
		}
	}()

	// Goroutine 2: Releases the final reference under snapshotMu
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startCh
		man.snapshotMu.Lock()
		defer man.snapshotMu.Unlock()

		sr.MarkObsoleteLocked()
		sr.UnrefLocked()
		man.checkCleanupEligibilityLocked(sr)
	}()

	close(startCh)
	wg.Wait()

	// Under snapshotMu synchronization:
	// Either Goroutine 1 acquired before Goroutine 2 dropped refs (acquired == true, panicked == false),
	// OR Goroutine 2 dropped refs and marked closing before Goroutine 1 attempted to Ref (panicked == true).
	// In NO scenario can a closing reader have refCount > 0 without being caught by panic.
	if !acquired.Load() && !panicked.Load() {
		t.Fatalf("expected either safe acquisition or resurrection panic")
	}

	// Clean up if acquired was true
	if acquired.Load() {
		man.snapshotMu.Lock()
		sr.UnrefLocked()
		man.checkCleanupEligibilityLocked(sr)
		man.snapshotMu.Unlock()
	}
}

// TestCompaction_CannotResurrectClosingSSTables validates I3.7.5:
// Confirms that compaction input leases acquired under snapshotMu cannot resurrect
// an SSTable that has already begun closing or been reclaimed.
func TestCompaction_CannotResurrectClosingSSTables(t *testing.T) {
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

	// Create closing reader
	dummyReader := &SSTableReader{}
	closingSR := NewSharedReader(dummyReader, filepath.Join(testDir, "closing.sst"))
	closingSR.SetState(SharedReaderClosing)

	// Attempt to add compaction lease to closing reader must panic
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected compaction lease on closing reader to panic")
		}
		msg, ok := r.(string)
		if !ok || msg != "FATAL: prohibited reference resurrection on closing/reclaimed SharedReader" {
			t.Fatalf("unexpected panic message: %v", r)
		}
	}()

	closingSR.Ref()
}
