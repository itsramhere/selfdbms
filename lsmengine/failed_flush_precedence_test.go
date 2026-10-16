package lsmengine

import (
	"errors"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestFailedFlush_E010_StaleReadRepro verifies that when an older flush F0 fails
// while a newer flush F1 succeeds, Manager.Get returns the stale value from F0
// because Frozenmem is checked before ActiveSSreaders.
func TestFailedFlush_E010_StaleReadRepro(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	oracle := newReferenceOracle()
	man := CreateManager()
	defer func() { _ = man.Close() }()

	f0Started := make(chan struct{})
	f0AllowFail := make(chan struct{})

	var flushIndex atomic.Int32
	man.flushBuildHook = func() error {
		idx := flushIndex.Add(1)
		if idx == 1 {
			// F0 hook
			close(f0Started)
			<-f0AllowFail
			return errors.New("simulated I/O failure on F0 SSTable build")
		}
		// Subsequent flushes succeed
		return nil
	}

	// 1. Commit key = "old" at sequence S1
	tx1 := man.Begin()
	tx1.Put("k1", "old")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit failed: %v", err)
	}
	s1 := tx1.AllocatedSeqNo
	oracle.record("k1", s1, "old", false)

	// 2. Freeze corresponding Memtable as F0
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F0 failed: %v", err)
	}

	// Wait until F0 is picked up by flush worker
	<-f0Started

	// 3. Commit key = "new" at sequence S2 (S2 > S1)
	tx2 := man.Begin()
	tx2.Put("k1", "new")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 commit failed: %v", err)
	}
	s2 := tx2.AllocatedSeqNo
	oracle.record("k1", s2, "new", false)

	// 4. Freeze newer Memtable as F1
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F1 failed: %v", err)
	}

	// 5. Allow F0 to fail
	close(f0AllowFail)

	// 6. Wait for F1 to be published into ActiveSSreaders
	deadline := time.Now().Add(3 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F1 SSTable publication")
		}
		runtime.Gosched()
	}

	// Verify state: F0 retained in Frozenmem, F1 published to ActiveSSreaders
	man.Mu.RLock()
	frozenCount := len(man.Frozenmem)
	readerCount := len(man.ActiveSSreaders)
	man.Mu.RUnlock()

	if frozenCount != 1 || readerCount != 1 {
		t.Fatalf("unexpected state: Frozenmem count=%d (expected 1), ActiveSSreaders count=%d (expected 1)",
			frozenCount, readerCount)
	}

	// 7. Read at timestamps before and after S2
	// At s1: both oracle and engine should return "old"
	valS1, err := man.Get("k1", s1)
	if err != nil {
		t.Errorf("Get at s1 failed: %v", err)
	}
	oracleS1, _ := oracle.get("k1", s1)
	if valS1 != oracleS1 {
		t.Errorf("Get at s1 returned %q, expected %q", valS1, oracleS1)
	}

	// At s2: oracle expects "new", but engine returns "old" from Frozenmem[0]!
	valS2, err := man.Get("k1", s2)
	oracleS2, _ := oracle.get("k1", s2)
	if valS2 != oracleS2 {
		t.Errorf("DEFECT E-010 REPRODUCED: Manager.Get returned stale value %q (expected %q). Failed flush F0 in Frozenmem masked newer SSTable F1!", valS2, oracleS2)
	}
}

// TestFailedFlush_E010_TombstoneResurrectionRepro verifies that when F0 contains a Put
// and F1 contains a newer Delete (tombstone), the failure of F0 causes the deleted key
// to be resurrected because F0 in Frozenmem shadows the tombstone in ActiveSSreaders.
func TestFailedFlush_E010_TombstoneResurrectionRepro(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	oracle := newReferenceOracle()
	man := CreateManager()
	defer func() { _ = man.Close() }()

	f0Started := make(chan struct{})
	f0AllowFail := make(chan struct{})

	var flushIndex atomic.Int32
	man.flushBuildHook = func() error {
		idx := flushIndex.Add(1)
		if idx == 1 {
			close(f0Started)
			<-f0AllowFail
			return errors.New("simulated I/O failure on F0 build")
		}
		return nil
	}

	// 1. F0 contains Put("k2", "val_to_delete")
	tx1 := man.Begin()
	tx1.Put("k2", "val_to_delete")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit failed: %v", err)
	}
	s1 := tx1.AllocatedSeqNo
	oracle.record("k2", s1, "val_to_delete", false)

	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F0 failed: %v", err)
	}
	<-f0Started

	// 2. F1 contains Delete("k2") at S2
	tx2 := man.Begin()
	tx2.Delete("k2")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 commit failed: %v", err)
	}
	s2 := tx2.AllocatedSeqNo
	oracle.record("k2", s2, "", true)

	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F1 failed: %v", err)
	}

	close(f0AllowFail)

	// Wait for F1 to publish
	deadline := time.Now().Add(3 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F1 SSTable publication")
		}
		runtime.Gosched()
	}

	// Read at s2: oracle expects ErrKeyDeleted, but engine returns "val_to_delete" from F0!
	valS2, err := man.Get("k2", s2)
	oracleVal, oracleErr := oracle.get("k2", s2)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Errorf("DEFECT E-010 REPRODUCED: Manager.Get returned resurrected value %q (error %v, expected ErrKeyDeleted %v). Older Put in failed flush F0 masked newer Tombstone in SSTable F1! (oracle=%q, err=%v)",
			valS2, err, ErrKeyDeleted, oracleVal, oracleErr)
	}
}

// TestFailedFlush_E010_StaleDeletionMaskingRepro verifies that when F0 contains a tombstone
// and F1 contains a newer Put, the failure of F0 causes the resurrected key to appear
// deleted because F0 in Frozenmem shadows the newer Put in ActiveSSreaders.
func TestFailedFlush_E010_StaleDeletionMaskingRepro(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	oracle := newReferenceOracle()
	man := CreateManager()
	defer func() { _ = man.Close() }()

	f0Started := make(chan struct{})
	f0AllowFail := make(chan struct{})

	var flushIndex atomic.Int32
	man.flushBuildHook = func() error {
		idx := flushIndex.Add(1)
		if idx == 1 {
			close(f0Started)
			<-f0AllowFail
			return errors.New("simulated I/O failure on F0 build")
		}
		return nil
	}

	// 1. F0 contains Delete("k3")
	tx1 := man.Begin()
	tx1.Delete("k3")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit failed: %v", err)
	}
	s1 := tx1.AllocatedSeqNo
	oracle.record("k3", s1, "", true)

	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F0 failed: %v", err)
	}
	<-f0Started

	// 2. F1 contains Put("k3", "reborn_at_s2")
	tx2 := man.Begin()
	tx2.Put("k3", "reborn_at_s2")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 commit failed: %v", err)
	}
	s2 := tx2.AllocatedSeqNo
	oracle.record("k3", s2, "reborn_at_s2", false)

	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F1 failed: %v", err)
	}

	close(f0AllowFail)

	deadline := time.Now().Add(3 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F1 SSTable publication")
		}
		runtime.Gosched()
	}

	// Read at s2: oracle expects "reborn_at_s2", but engine returns ErrKeyDeleted from F0!
	valS2, err := man.Get("k3", s2)
	oracleVal, _ := oracle.get("k3", s2)
	if valS2 != oracleVal {
		t.Errorf("DEFECT E-010 REPRODUCED: Manager.Get returned error %v, val %q (expected %q). Older Tombstone in failed flush F0 masked newer Put in SSTable F1!",
			err, valS2, oracleVal)
	}
}

// TestFailedFlush_E010_ThreeGenerationsRepro verifies behavior across 3 frozen generations:
// F0 (fails), F1 (succeeds), F2 (succeeds).
func TestFailedFlush_E010_ThreeGenerationsRepro(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	oracle := newReferenceOracle()
	man := CreateManager()
	defer func() { _ = man.Close() }()

	f0Started := make(chan struct{})
	f0AllowFail := make(chan struct{})

	var flushIndex atomic.Int32
	man.flushBuildHook = func() error {
		idx := flushIndex.Add(1)
		if idx == 1 {
			close(f0Started)
			<-f0AllowFail
			return errors.New("simulated I/O failure on F0 build")
		}
		return nil
	}

	// Gen 0 (F0): k4 = "v0"
	tx0 := man.Begin()
	tx0.Put("k4", "v0")
	_ = tx0.Commit()
	oracle.record("k4", tx0.AllocatedSeqNo, "v0", false)
	_ = man.TriggerFlush()
	<-f0Started

	// Gen 1 (F1): k4 = "v1"
	tx1 := man.Begin()
	tx1.Put("k4", "v1")
	_ = tx1.Commit()
	oracle.record("k4", tx1.AllocatedSeqNo, "v1", false)
	_ = man.TriggerFlush()

	// Gen 2 (F2): k4 = "v2"
	tx2 := man.Begin()
	tx2.Put("k4", "v2")
	_ = tx2.Commit()
	s2 := tx2.AllocatedSeqNo
	oracle.record("k4", s2, "v2", false)
	_ = man.TriggerFlush()

	close(f0AllowFail)

	// Wait for F1 and F2 to publish (2 readers)
	deadline := time.Now().Add(3 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F1 and F2 SSTable publication")
		}
		runtime.Gosched()
	}

	// Read at s2: oracle expects "v2", but engine returns "v0" from F0!
	valS2, _ := man.Get("k4", s2)
	oracleVal, _ := oracle.get("k4", s2)
	if valS2 != oracleVal {
		t.Errorf("DEFECT E-010 REPRODUCED: Manager.Get returned %q (expected %q) across 3 generations! Failed flush F0 masked SSTables F1 and F2.",
			valS2, oracleVal)
	}
}

// TestFailedFlush_E010_ActiveMemtablePrecedence verifies that a newer value in the active
// Memtable correctly takes precedence over an older failed flush in Frozenmem.
func TestFailedFlush_E010_ActiveMemtablePrecedence(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	man := CreateManager()
	defer func() { _ = man.Close() }()

	f0Started := make(chan struct{})
	f0AllowFail := make(chan struct{})

	var flushIndex atomic.Int32
	man.flushBuildHook = func() error {
		idx := flushIndex.Add(1)
		if idx == 1 {
			close(f0Started)
			<-f0AllowFail
			return errors.New("simulated I/O failure on F0")
		}
		return nil
	}

	// F0 has k5 = "v0"
	tx0 := man.Begin()
	tx0.Put("k5", "v0")
	_ = tx0.Commit()
	_ = man.TriggerFlush()
	<-f0Started

	close(f0AllowFail)

	// Wait for F0 to finish failing
	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		fErr := man.flushErr
		man.Mu.RUnlock()
		if fErr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F0 error")
		}
		runtime.Gosched()
	}

	// Write k5 = "v_active" into active Memtable
	tx1 := man.Begin()
	tx1.Put("k5", "v_active")
	_ = tx1.Commit()
	s1 := tx1.AllocatedSeqNo

	// Read k5: active Memtable is checked before Frozenmem, so it should return "v_active"
	val, err := man.Get("k5", s1)
	if err != nil {
		t.Fatalf("Get k5 failed: %v", err)
	}
	if val != "v_active" {
		t.Fatalf("expected active Memtable value 'v_active', got %q", val)
	}
}

// TestFailedFlush_E010_NoPartialPublication verifies that when an SSTable build fails,
// no partial or corrupted SSTable is published to ActiveSSreaders and the temp file is cleaned up.
func TestFailedFlush_E010_NoPartialPublication(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	man := CreateManager()
	defer func() { _ = man.Close() }()

	man.flushBuildHook = func() error {
		return errors.New("controlled disk error on build")
	}

	tx := man.Begin()
	tx.Put("k_partial", "val")
	_ = tx.Commit()

	_ = man.TriggerFlush()

	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		fErr := man.flushErr
		man.Mu.RUnlock()
		if fErr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	man.Mu.RLock()
	readersCount := len(man.ActiveSSreaders)
	frozenCount := len(man.Frozenmem)
	man.Mu.RUnlock()

	if readersCount != 0 {
		t.Fatalf("expected 0 ActiveSSreaders after failure, got %d", readersCount)
	}
	if frozenCount != 1 {
		t.Fatalf("expected 1 Frozenmem retained after failure, got %d", frozenCount)
	}
}

// TestFailedFlush_E010_WriteConflictDetectionFailure verifies that write-write conflict
// detection fails when F0 fails and F1 succeeds because latestValueLocked returns the
// older sequence number from F0 in Frozenmem rather than the newer sequence number in SSTable F1.
func TestFailedFlush_E010_WriteConflictDetectionFailure(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	man := CreateManager()
	defer func() { _ = man.Close() }()

	f0Started := make(chan struct{})
	f0AllowFail := make(chan struct{})

	var flushIndex atomic.Int32
	man.flushBuildHook = func() error {
		idx := flushIndex.Add(1)
		if idx == 1 {
			close(f0Started)
			<-f0AllowFail
			return errors.New("simulated I/O failure on F0 build")
		}
		return nil
	}

	// 1. Commit k8 = "old" at sequence S1 in F0
	tx1 := man.Begin()
	tx1.Put("k8", "old")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit failed: %v", err)
	}
	s1 := tx1.AllocatedSeqNo

	_ = man.TriggerFlush()
	<-f0Started

	// 2. Commit k8 = "new" at sequence S2 in F1
	tx2 := man.Begin()
	tx2.Put("k8", "new")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 commit failed: %v", err)
	}
	s2 := tx2.AllocatedSeqNo

	_ = man.TriggerFlush()
	close(f0AllowFail)

	// Wait for F1 to publish to ActiveSSreaders
	deadline := time.Now().Add(3 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F1 SSTable publication")
		}
		runtime.Gosched()
	}

	// 3. Begin a concurrent transaction T3 that read at S1 (before S2 committed)
	// T3 attempts to write to k8 and commit.
	// Since k8 was committed at S2 > S1, T3 should detect a write-write conflict and abort!
	t3 := man.Begin()
	t3.ReadTs = s1
	t3.Put("k8", "t3_conflict")
	commitErr := t3.Commit()

	if commitErr == nil {
		t.Errorf("DEFECT E-010 REPRODUCED: Transaction committed write on k8 without detecting write-write conflict against SSTable F1 (seq S2=%d)! latestValueLocked returned seq S1=%d from failed flush F0 in Frozenmem.",
			s2, s1)
	}
}

// TestFailedFlush_E010_ErrorReportingOnClose verifies that Manager.Close() surfaces
// the background flush failure.
func TestFailedFlush_E010_ErrorReportingOnClose(t *testing.T) {
	tempDir := t.TempDir()
	origWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir to tempDir: %v", err)
	}
	defer os.Chdir(origWd)

	man := CreateManager()

	expectedErr := errors.New("controlled disk error on build")
	man.flushBuildHook = func() error {
		return expectedErr
	}

	tx := man.Begin()
	tx.Put("k_err", "val")
	_ = tx.Commit()
	_ = man.TriggerFlush()

	closeErr := man.Close()
	if closeErr == nil {
		t.Fatalf("expected Close() to return error, got nil")
	}
	if !errors.Is(closeErr, expectedErr) {
		t.Fatalf("expected close error %v, got %v", expectedErr, closeErr)
	}
}
