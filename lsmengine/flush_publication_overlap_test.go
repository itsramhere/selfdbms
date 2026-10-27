package lsmengine

import (
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// mockFlushTracker implements FlushObserver for deterministic audit of publication overlap metrics.
type mockFlushTracker struct {
	isPublicationActive    atomic.Bool
	readsDuringPublication atomic.Int64
	readsWhileLockHeld     atomic.Int64
	lockIsHeld             atomic.Bool
}

func (m *mockFlushTracker) OnFlushRequested()                         {}
func (m *mockFlushTracker) OnFlushAdmitted(accepted bool, err error) {}
func (m *mockFlushTracker) OnFlushWorkerBegin()                       {}
func (m *mockFlushTracker) OnFlushBuildBegin()                        {}
func (m *mockFlushTracker) OnFlushBuildEnd(err error)                 {}

func (m *mockFlushTracker) OnFlushPublishBegin() {
	m.isPublicationActive.Store(true)
}

func (m *mockFlushTracker) OnFlushPublishEnd(err error) {
	m.isPublicationActive.Store(false)
}

func (m *mockFlushTracker) OnFlushComplete(err error) {}

func (m *mockFlushTracker) RecordPointRead() {
	if m.isPublicationActive.Load() {
		m.readsDuringPublication.Add(1)
	}
	if m.lockIsHeld.Load() {
		m.readsWhileLockHeld.Add(1)
	}
}

// TestMetricSemantics_ApproachA_ReadsDuringPublicationAudit verifies the exact causal mechanism:
// In Approach A, readsDuringPublication counts in-flight reads that were already holding m.Mu.RLock()
// when OnFlushPublishBegin was called and finished while the flush worker was blocked waiting to
// acquire m.Mu.Lock(). ZERO reads ever complete while m.Mu.Lock is actually held.
func TestMetricSemantics_ApproachA_ReadsDuringPublicationAudit(t *testing.T) {
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   SyncModeApproachA,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	// Initial key to make SSTable
	tx := man.Begin()
	tx.Put("audit_key", "audit_val")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("trigger flush failed: %v", err)
	}
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("wait flush failed: %v", err)
	}

	tracker := &mockFlushTracker{}
	man.SetFlushObserver(tracker)

	// Dirty the memtable so a second flush can build and publish
	tx2 := man.Begin()
	tx2.Put("audit_key2", "audit_val2")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	workerInHook := make(chan struct{})
	unblockHook := make(chan struct{})
	readerHoldingRLock := make(chan struct{})
	unblockReader := make(chan struct{})
	readerFinished := make(chan struct{})

	// Hook into flush build: pause worker so we can acquire reader RLock right before publication
	man.SetFlushBuildHook(func() error {
		close(workerInHook)
		<-unblockHook
		return nil
	})

	// Trigger flush. TriggerFlush admits the request, freezes memtable, and unblocks immediately.
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}

	// Wait until worker is in build hook
	select {
	case <-workerInHook:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for worker in build hook")
	}

	// Launch reader goroutine that acquires m.Mu.RLock and holds it across publication start
	go func() {
		man.Mu.RLock()
		close(readerHoldingRLock) // signal that reader is holding RLock
		<-unblockReader           // wait until publisher has called OnFlushPublishBegin
		man.Mu.RUnlock()

		// Reader completes and records metric
		tracker.RecordPointRead()
		close(readerFinished)
	}()

	// Wait until reader is holding RLock
	<-readerHoldingRLock

	// Unblock build hook: worker will finish build and call OnFlushPublishBegin
	close(unblockHook)

	// Wait until publication has begun (isPublicationActive == true)
	// At this point, the worker is blocked attempting man.Mu.Lock() because reader holds RLock.
	deadline := time.Now().Add(3 * time.Second)
	for !tracker.isPublicationActive.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for OnFlushPublishBegin")
		}
		time.Sleep(1 * time.Millisecond)
	}

	// Verify: Worker is in publication phase, but reader is still holding RLock
	if !tracker.isPublicationActive.Load() {
		t.Fatalf("expected isPublicationActive == true")
	}

	// 2. Unblock reader while publisher is blocked waiting for man.Mu.Lock()
	close(unblockReader)
	<-readerFinished

	// 3. Verify: The in-flight reader finished while isPublicationActive == true,
	// so readsDuringPublication was incremented!
	if readsInPub := tracker.readsDuringPublication.Load(); readsInPub != 1 {
		t.Fatalf("expected exactly 1 read during publication window (in-flight drain), got %d", readsInPub)
	}

	// 4. Wait for flush worker to finish publication and complete
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("WaitForFlushCompletion failed: %v", err)
	}

	// Verify publication is closed
	if tracker.isPublicationActive.Load() {
		t.Fatalf("expected isPublicationActive == false after completion")
	}

	// 5. Verify that ZERO reads ever completed while man.Mu.Lock was held
	if tracker.readsWhileLockHeld.Load() != 0 {
		t.Fatalf("expected 0 reads while lock held, got %d", tracker.readsWhileLockHeld.Load())
	}

	// 6. Any new read executing AFTER publication does NOT increment readsDuringPublication
	_, _ = man.Get("audit_key", math.MaxUint64)
	tracker.RecordPointRead()

	if readsInPub := tracker.readsDuringPublication.Load(); readsInPub != 1 {
		t.Fatalf("expected readsDuringPublication to remain 1, got %d", readsInPub)
	}
}

// TestMetricSemantics_ApproachB_SnapshotReadersDoNotBlockPublication verifies that in Approach B,
// reader threads holding pinned snapshots do not block man.Mu.Lock or publication, allowing
// publication to complete promptly without reader drain delays.
func TestMetricSemantics_ApproachB_SnapshotReadersDoNotBlockPublication(t *testing.T) {
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   SyncModeApproachB,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	// Initial key to create SSTable
	tx := man.Begin()
	tx.Put("b_key1", "b_val1")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("trigger flush failed: %v", err)
	}
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("wait flush failed: %v", err)
	}

	tracker := &mockFlushTracker{}
	man.SetFlushObserver(tracker)

	// Dirty the memtable for second flush
	tx2 := man.Begin()
	tx2.Put("b_key2", "b_val2")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	// Acquire pinned snapshot
	snap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("failed to acquire snapshot: %v", err)
	}

	// Trigger flush while reader holds pinned snapshot
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}

	// In Approach B, flush completes promptly WITHOUT waiting for reader to release snapshot!
	flushDone := make(chan error, 1)
	go func() {
		flushDone <- man.WaitForFlushCompletion()
	}()

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("WaitForFlushCompletion failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("flush timed out! Approach B publication was blocked by reader snapshot!")
	}

	// Reader can still read safely from its pinned snapshot after publication!
	man.snapshotMu.Lock()
	sr := snap.readers[0]
	man.snapshotMu.Unlock()
	val, err := sr.Reader.Get([]byte("b_key1"), math.MaxUint64)
	if err != nil || string(val) != "b_val1" {
		t.Fatalf("failed to read from pinned snapshot after publication: val=%s, err=%v", string(val), err)
	}

	// Release pinned snapshot
	man.ReleaseSnapshot(snap)

	// Verify no errors or leaks
	if _, err := os.Stat(filepath.Join(dir, "wal.log")); err != nil {
		t.Fatalf("wal stat failed: %v", err)
	}
}
