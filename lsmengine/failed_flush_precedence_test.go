package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFailedFlush_E010_Resolved_StaleReadPrevented verifies that under Option A (fail-stop),
// when an older flush F0 fails, the worker halts immediately and does not publish F1 to
// ActiveSSreaders. Both F0 and F1 remain safely in Frozenmem in strict chronological order,
// so Manager.Get at S2 returns the latest value "new", completely preventing stale reads.
func TestFailedFlush_E010_Resolved_StaleReadPrevented(t *testing.T) {
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
			return errors.New("simulated I/O failure on F0 SSTable build")
		}
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

	// 4. Freeze newer Memtable as F1 (enqueued while F0 is in-flight)
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush F1 failed: %v", err)
	}

	// 5. Allow F0 to fail
	close(f0AllowFail)

	// 6. Under Option A (fail-stop), wait for the flush error to be recorded
	deadline := time.Now().Add(3 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush worker to report error")
		}
		runtime.Gosched()
	}

	// Verify fail-stop state:
	// - Zero SSTables published to ActiveSSreaders
	// - Both F0 and F1 preserved in Frozenmem in chronological order
	man.Mu.RLock()
	frozenCount := len(man.Frozenmem)
	readerCount := len(man.ActiveSSreaders)
	man.Mu.RUnlock()

	if readerCount != 0 {
		t.Fatalf("fail-stop invariant violated: expected 0 ActiveSSreaders, got %d", readerCount)
	}
	if frozenCount != 2 {
		t.Fatalf("expected 2 Frozenmem retained in order, got %d", frozenCount)
	}

	// 7. Verify reads at timestamps s1 and s2 against ReferenceOracle
	valS1, err := man.Get("k1", s1)
	if err != nil {
		t.Fatalf("Get at s1 failed: %v", err)
	}
	oracleS1, _ := oracle.get("k1", s1)
	if valS1 != oracleS1 {
		t.Fatalf("Get at s1 returned %q, expected %q", valS1, oracleS1)
	}

	valS2, err := man.Get("k1", s2)
	if err != nil {
		t.Fatalf("Get at s2 failed: %v", err)
	}
	oracleS2, _ := oracle.get("k1", s2)
	if valS2 != oracleS2 {
		t.Fatalf("E-010 defect persists: Get at s2 returned %q, expected %q", valS2, oracleS2)
	}
}

// TestFailedFlush_E010_Resolved_TombstoneResurrectionPrevented verifies that when F0 contains
// a Put and F1 contains a newer Delete, fail-stop keeps both in Frozenmem so that reading
// at S2 returns ErrKeyDeleted, preventing tombstone resurrection.
func TestFailedFlush_E010_Resolved_TombstoneResurrectionPrevented(t *testing.T) {
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

	deadline := time.Now().Add(3 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// Read at s2: must return ErrKeyDeleted matching reference oracle
	_, err = man.Get("k2", s2)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("tombstone resurrection defect persists: expected ErrKeyDeleted, got err=%v", err)
	}
}

// TestFailedFlush_E010_Resolved_StaleDeletionMaskingPrevented verifies that when F0 contains
// a tombstone and F1 contains a newer Put, fail-stop ensures reading at S2 returns the new Put.
func TestFailedFlush_E010_Resolved_StaleDeletionMaskingPrevented(t *testing.T) {
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
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// Read at s2: must return "reborn_at_s2" matching reference oracle
	valS2, err := man.Get("k3", s2)
	if err != nil {
		t.Fatalf("Get at s2 returned error: %v", err)
	}
	oracleVal, _ := oracle.get("k3", s2)
	if valS2 != oracleVal {
		t.Fatalf("stale deletion masking defect persists: expected %q, got %q", oracleVal, valS2)
	}
}

// TestFailedFlush_E010_Resolved_ThreeGenerationsPreserved verifies correct newest-generation
// precedence across 3 frozen generations under fail-stop.
func TestFailedFlush_E010_Resolved_ThreeGenerationsPreserved(t *testing.T) {
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

	deadline := time.Now().Add(3 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// Read at s2: must return "v2" from F2
	valS2, err := man.Get("k4", s2)
	if err != nil {
		t.Fatalf("Get at s2 failed: %v", err)
	}
	oracleVal, _ := oracle.get("k4", s2)
	if valS2 != oracleVal {
		t.Fatalf("three-generation precedence defect persists: expected %q, got %q", oracleVal, valS2)
	}
}

// TestFailedFlush_E010_Resolved_ActiveMemtablePrecedence verifies that active Memtable
// writes take precedence over failed frozen Memtables.
func TestFailedFlush_E010_Resolved_ActiveMemtablePrecedence(t *testing.T) {
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

	tx0 := man.Begin()
	tx0.Put("k5", "v0")
	_ = tx0.Commit()
	_ = man.TriggerFlush()
	<-f0Started

	close(f0AllowFail)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F0 error")
		}
		runtime.Gosched()
	}

	tx1 := man.Begin()
	tx1.Put("k5", "v_active")
	_ = tx1.Commit()
	s1 := tx1.AllocatedSeqNo

	val, err := man.Get("k5", s1)
	if err != nil {
		t.Fatalf("Get k5 failed: %v", err)
	}
	if val != "v_active" {
		t.Fatalf("expected active Memtable value 'v_active', got %q", val)
	}
}

// TestFailedFlush_E010_Resolved_NoPartialPublication verifies no corrupt reader is published.
func TestFailedFlush_E010_Resolved_NoPartialPublication(t *testing.T) {
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
		if man.FlushErr() != nil {
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

// TestFailedFlush_E010_Resolved_WriteConflictDetection verifies that write-write conflict
// detection correctly observes the newest version in Frozenmem under fail-stop.
func TestFailedFlush_E010_Resolved_WriteConflictDetection(t *testing.T) {
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

	_ = man.TriggerFlush()
	close(f0AllowFail)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// 3. Begin a transaction T3 at S1 attempting to write k8
	// T3 must detect conflict against S2 (which is in Frozenmem[1]) and abort!
	t3 := man.Begin()
	t3.ReadTs = s1
	t3.Put("k8", "t3_conflict")
	commitErr := t3.Commit()

	if commitErr == nil {
		t.Fatalf("write-write conflict defect persists: transaction at S1 committed without detecting conflict against S2!")
	}
}

// TestFailedFlush_E010_Resolved_ErrorReportingOnClose verifies Close() surfaces flushErr.
func TestFailedFlush_E010_Resolved_ErrorReportingOnClose(t *testing.T) {
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

// TestFailedFlush_Interaction_SubsequentQueuedFlushes verifies that all flushes queued
// after a failed flush remain safely in Frozenmem and are not dropped.
func TestFailedFlush_Interaction_SubsequentQueuedFlushes(t *testing.T) {
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
			return errors.New("disk failure on F0")
		}
		return nil
	}

	// Write and freeze 3 flushes
	for i := 0; i < 3; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("key_%d", i), fmt.Sprintf("val_%d", i))
		_ = tx.Commit()
		_ = man.TriggerFlush()
		if i == 0 {
			<-f0Started
		}
	}

	close(f0AllowFail)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// All 3 memtables must remain in Frozenmem
	man.Mu.RLock()
	frozenCount := len(man.Frozenmem)
	readerCount := len(man.ActiveSSreaders)
	man.Mu.RUnlock()

	if readerCount != 0 {
		t.Fatalf("expected 0 readers published, got %d", readerCount)
	}
	if frozenCount != 3 {
		t.Fatalf("expected 3 memtables preserved in Frozenmem, got %d", frozenCount)
	}

	// All values must be queryable
	for i := 0; i < 3; i++ {
		val, err := man.Get(fmt.Sprintf("key_%d", i), 1000)
		if err != nil {
			t.Fatalf("Get key_%d failed: %v", i, err)
		}
		if val != fmt.Sprintf("val_%d", i) {
			t.Fatalf("expected val_%d, got %q", i, val)
		}
	}
}

// TestFailedFlush_Interaction_ConcurrentReads verifies concurrent point reads
// while a flush fails.
func TestFailedFlush_Interaction_ConcurrentReads(t *testing.T) {
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

	tx := man.Begin()
	tx.Put("read_key", "read_val")
	_ = tx.Commit()

	man.flushBuildHook = func() error {
		return errors.New("controlled failure")
	}
	_ = man.TriggerFlush()

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				val, err := man.Get("read_key", 1000)
				if err != nil || val != "read_val" {
					t.Errorf("concurrent read failed: val=%q, err=%v", val, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestFailedFlush_Interaction_ConcurrentCommits verifies transactions can continue
// committing to active Memtable and WAL while flush error is surfaced.
func TestFailedFlush_Interaction_ConcurrentCommits(t *testing.T) {
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
		return errors.New("controlled failure")
	}

	tx0 := man.Begin()
	tx0.Put("init_key", "val")
	_ = tx0.Commit()
	_ = man.TriggerFlush()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// Concurrent commits
	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				tx := man.Begin()
				tx.Put(fmt.Sprintf("g_%d_key_%d", gid, i), "val")
				if err := tx.Commit(); err != nil {
					t.Errorf("commit failed: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestFailedFlush_Interaction_Compaction verifies compaction behaves correctly
// on pre-existing SSTables when a flush failure occurs.
func TestFailedFlush_Interaction_Compaction(t *testing.T) {
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

	// Successfully create 2 SSTables
	for gen := 0; gen < 2; gen++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("k_sst_%d", gen), fmt.Sprintf("v_sst_%d", gen))
		_ = tx.Commit()
		_ = man.TriggerFlush()

		deadline := time.Now().Add(2 * time.Second)
		for {
			man.Mu.RLock()
			rCount := len(man.ActiveSSreaders)
			man.Mu.RUnlock()
			if rCount == gen+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for SSTable gen %d", gen)
			}
			runtime.Gosched()
		}
	}

	// Now inject failure on 3rd flush
	man.flushBuildHook = func() error {
		return errors.New("simulated error on 3rd flush")
	}

	tx3 := man.Begin()
	tx3.Put("k_fail", "v_fail")
	_ = tx3.Commit()
	_ = man.TriggerFlush()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// Compact pre-existing SSTables
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Verify reading both compacted SSTable keys and failed Frozenmem key
	v0, err := man.Get("k_sst_0", 1000)
	if err != nil || v0 != "v_sst_0" {
		t.Fatalf("failed to read k_sst_0: v=%q, err=%v", v0, err)
	}
	v1, err := man.Get("k_sst_1", 1000)
	if err != nil || v1 != "v_sst_1" {
		t.Fatalf("failed to read k_sst_1: v=%q, err=%v", v1, err)
	}
	vFail, err := man.Get("k_fail", 1000)
	if err != nil || vFail != "v_fail" {
		t.Fatalf("failed to read k_fail from Frozenmem: v=%q, err=%v", vFail, err)
	}
}

// TestFailedFlush_Interaction_RepeatedShutdownCalls verifies repeated Close() calls
// return the same flush error without panic or resource leak.
func TestFailedFlush_Interaction_RepeatedShutdownCalls(t *testing.T) {
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
	man.flushBuildHook = func() error {
		return errors.New("controlled failure")
	}

	tx := man.Begin()
	tx.Put("key", "val")
	_ = tx.Commit()
	_ = man.TriggerFlush()

	err1 := man.Close()
	if err1 == nil {
		t.Fatalf("expected error on first Close(), got nil")
	}

	err2 := man.Close()
	if !errors.Is(err2, err1) {
		t.Fatalf("expected same error on second Close(), got %v", err2)
	}

	err3 := man.Close()
	if !errors.Is(err3, err1) {
		t.Fatalf("expected same error on third Close(), got %v", err3)
	}
}

// TestFailedFlush_Interaction_MultipleFailedFlushes verifies TriggerFlush rejects
// flushes once flushErr is active.
func TestFailedFlush_Interaction_MultipleFailedFlushes(t *testing.T) {
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

	expectedErr := errors.New("controlled failure")
	man.flushBuildHook = func() error {
		return expectedErr
	}

	tx := man.Begin()
	tx.Put("key1", "val1")
	_ = tx.Commit()
	_ = man.TriggerFlush()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if man.FlushErr() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush error")
		}
		runtime.Gosched()
	}

	// TriggerFlush should return flush error immediately
	tx2 := man.Begin()
	tx2.Put("key2", "val2")
	_ = tx2.Commit()
	flushErr := man.TriggerFlush()
	if flushErr == nil {
		t.Fatalf("expected TriggerFlush to fail after flushErr, got nil")
	}
	if !errors.Is(flushErr, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, flushErr)
	}
}
