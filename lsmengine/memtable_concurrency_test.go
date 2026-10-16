package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemtable_Case1_ConcurrentPutAndGetSameKey verifies that concurrent
// Put and Get operations on the same user key under Manager.Mu do not produce
// data races, partial reads, corrupted strings, or unhandled panics.
func TestMemtable_Case1_ConcurrentPutAndGetSameKey(t *testing.T) {
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
	defer man.Close()

	// Initial seed write
	tx0 := man.Begin()
	tx0.Put("shared_key", "val_0")
	if err := tx0.Commit(); err != nil {
		t.Fatalf("tx0 commit failed: %v", err)
	}

	var wg sync.WaitGroup
	stopSignal := make(chan struct{})
	var writeCount atomic.Int64
	var readCount atomic.Int64

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 50; i++ {
			select {
			case <-stopSignal:
				return
			default:
				tx := man.Begin()
				tx.Put("shared_key", fmt.Sprintf("val_%d", i))
				if err := tx.Commit(); err == nil {
					writeCount.Add(1)
				}
			}
		}
	}()

	// 4 Reader goroutines
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(readerId int) {
			defer wg.Done()
			for {
				select {
				case <-stopSignal:
					return
				default:
					tx := man.Begin()
					val, err := tx.Get("shared_key")
					if err != nil {
						t.Errorf("reader %d failed: %v", readerId, err)
						return
					}
					if len(val) < 5 || val[:4] != "val_" {
						t.Errorf("reader %d saw corrupted value: %q", readerId, val)
						return
					}
					readCount.Add(1)
					if writeCount.Load() >= 50 && readCount.Load() > 100 {
						return
					}
				}
			}
		}(r)
	}

	// Wait for writer to finish 50 commits
	for writeCount.Load() < 50 {
		time.Sleep(1 * time.Millisecond)
	}
	close(stopSignal)
	wg.Wait()

	t.Logf("Case 1 PASS: Completed %d writes and %d concurrent reads on shared key", writeCount.Load(), readCount.Load())
}

// TestMemtable_Case2_MultipleConcurrentWritersOverlappingKeys verifies that
// two concurrent transactions starting at the same snapshot timestamp attempting
// to update the same key result in exactly one commit and one write-write conflict abort.
func TestMemtable_Case2_MultipleConcurrentWritersOverlappingKeys(t *testing.T) {
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
	defer man.Close()

	// Initial seed
	initTx := man.Begin()
	initTx.Put("overlap_key", "initial")
	if err := initTx.Commit(); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}

	for iter := 0; iter < 10; iter++ {
		t1 := man.Begin()
		t2 := man.Begin()

		t1.Put("overlap_key", fmt.Sprintf("val_t1_%d", iter))
		t2.Put("overlap_key", fmt.Sprintf("val_t2_%d", iter))

		barrier := make(chan struct{})
		var t1Err, t2Err error
		var wg sync.WaitGroup

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-barrier
			t1Err = t1.Commit()
		}()
		go func() {
			defer wg.Done()
			<-barrier
			t2Err = t2.Commit()
		}()

		close(barrier)
		wg.Wait()

		// Exactly one must succeed, and one must abort with write-write conflict
		if (t1Err == nil && t2Err == nil) || (t1Err != nil && t2Err != nil) {
			t.Fatalf("iteration %d: expected exactly one commit success and one conflict abort, got t1Err=%v, t2Err=%v",
				iter, t1Err, t2Err)
		}

		var winner string
		if t1Err == nil {
			winner = fmt.Sprintf("val_t1_%d", iter)
			if t2Err.Error() != "Write-write conflict detected. Transaction aborted." {
				t.Fatalf("unexpected t2 conflict error: %v", t2Err)
			}
		} else {
			winner = fmt.Sprintf("val_t2_%d", iter)
			if t1Err.Error() != "Write-write conflict detected. Transaction aborted." {
				t.Fatalf("unexpected t1 conflict error: %v", t1Err)
			}
		}

		// Verify winning value is in active Memtable
		val, err := man.Get("overlap_key", man.CurrentSeqNo.Load())
		if err != nil {
			t.Fatalf("get failed after iter %d: %v", iter, err)
		}
		if val != winner {
			t.Fatalf("iter %d: expected winner %q, got %q", iter, winner, val)
		}
	}

	t.Log("Case 2 PASS: 10/10 concurrent overlapping writer conflicts detected correctly")
}

// TestMemtable_Case3_ConcurrentPutAndDelete verifies serialization and tombstone
// behavior when Put and Delete interleave on the same user key.
func TestMemtable_Case3_ConcurrentPutAndDelete(t *testing.T) {
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
	defer man.Close()

	// Initial key
	t0 := man.Begin()
	t0.Put("del_put_key", "existing")
	if err := t0.Commit(); err != nil {
		t.Fatalf("t0 commit failed: %v", err)
	}
	seq0 := t0.AllocatedSeqNo

	// Delete key
	tDel := man.Begin()
	tDel.Delete("del_put_key")
	if err := tDel.Commit(); err != nil {
		t.Fatalf("tDel commit failed: %v", err)
	}
	seqDel := tDel.AllocatedSeqNo

	// Put new value after delete
	tPut := man.Begin()
	tPut.Put("del_put_key", "resurrected")
	if err := tPut.Commit(); err != nil {
		t.Fatalf("tPut commit failed: %v", err)
	}
	seqPut := tPut.AllocatedSeqNo

	// Verify snapshot visibility across history in active Memtable
	// 1. Read at seq0 sees "existing"
	v0, err := man.Get("del_put_key", seq0)
	if err != nil || v0 != "existing" {
		t.Fatalf("at seq0 expected 'existing', got val=%q, err=%v", v0, err)
	}

	// 2. Read at seqDel sees ErrKeyDeleted
	_, err = man.Get("del_put_key", seqDel)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("at seqDel expected ErrKeyDeleted, got: %v", err)
	}

	// 3. Read at seqPut sees "resurrected"
	vPut, err := man.Get("del_put_key", seqPut)
	if err != nil || vPut != "resurrected" {
		t.Fatalf("at seqPut expected 'resurrected', got val=%q, err=%v", vPut, err)
	}

	t.Log("Case 3 PASS: Put/Delete/Resurrect MVCC sequence verified in active Memtable")
}

// TestMemtable_Case4_ReadAtSupportedMVCCTimestampsDuringConcurrentWrites verifies
// that snapshot reads at older timestamps continue returning the exact version visible
// at that timestamp while concurrent writers are committing new versions.
func TestMemtable_Case4_ReadAtSupportedMVCCTimestampsDuringConcurrentWrites(t *testing.T) {
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
	defer man.Close()

	// Seed version 1 at seq 1
	tx1 := man.Begin()
	tx1.Put("mvcc_stream", "v1")
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 failed: %v", err)
	}
	ts1 := tx1.AllocatedSeqNo

	// Seed version 2 at seq 2
	tx2 := man.Begin()
	tx2.Put("mvcc_stream", "v2")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("tx2 failed: %v", err)
	}
	ts2 := tx2.AllocatedSeqNo

	// Snapshot transaction reading at ts1
	snapTx := man.Begin()
	snapTx.ReadTs = ts1

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	var readerSaw string
	var readerErr error

	// Reader goroutine performing read at ts1
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		readerSaw, readerErr = snapTx.Get("mvcc_stream")
	}()

	// Concurrent writer committing versions v3, v4, v5, v6
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-startBarrier
		for i := 3; i <= 6; i++ {
			w := man.Begin()
			w.Put("mvcc_stream", fmt.Sprintf("v%d", i))
			_ = w.Commit()
		}
	}()

	close(startBarrier)
	wg.Wait()

	if readerErr != nil {
		t.Fatalf("snapshot read failed: %v", readerErr)
	}
	if readerSaw != "v1" {
		t.Fatalf("snapshot read at ts1 violated: expected 'v1', got %q", readerSaw)
	}

	// Verify that reading at ts2 sees v2
	v2, err := man.Get("mvcc_stream", ts2)
	if err != nil || v2 != "v2" {
		t.Fatalf("read at ts2 expected 'v2', got val=%q, err=%v", v2, err)
	}

	// Verify latest read sees v6
	vLatest, err := man.Get("mvcc_stream", man.CurrentSeqNo.Load())
	if err != nil || vLatest != "v6" {
		t.Fatalf("read at latest expected 'v6', got val=%q, err=%v", vLatest, err)
	}

	t.Log("Case 4 PASS: Multi-version MVCC snapshot isolation verified during active concurrent commits")
}

// TestMemtable_Case5_MemtableFreezeWhileReadInProgress verifies that an in-progress
// point read holding m.Mu.RLock() safely completes before a concurrent freeze acquires m.Mu.Lock().
func TestMemtable_Case5_MemtableFreezeWhileReadInProgress(t *testing.T) {
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
	defer man.Close()

	// Seed key
	tx := man.Begin()
	tx.Put("freeze_key", "freeze_val")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	readStarted := make(chan struct{})
	readCanFinish := make(chan struct{})

	// Hook into point read to hold RLock open right before reading Memtable
	man.duringMemtableReadHook = func() {
		close(readStarted)
		<-readCanFinish
	}

	var wg sync.WaitGroup
	var readVal string
	var readErr error

	// Launch reader
	wg.Add(1)
	go func() {
		defer wg.Done()
		readVal, readErr = man.Get("freeze_key", man.CurrentSeqNo.Load())
	}()

	// Wait until reader is inside duringPointReadHook (holding m.Mu.RLock)
	<-readStarted

	// Launch freeze in background (must block waiting for reader to release RLock)
	freezeDone := make(chan struct{})
	go func() {
		_ = man.TriggerFlush()
		close(freezeDone)
	}()

	// Verify freeze is blocked
	select {
	case <-freezeDone:
		t.Fatal("Freeze acquired m.Mu.Lock() while point read was still holding RLock!")
	case <-time.After(50 * time.Millisecond):
		// Expected: freeze is correctly blocked by RLock
	}

	// Release reader
	close(readCanFinish)
	wg.Wait()
	<-freezeDone
	man.duringMemtableReadHook = nil

	if readErr != nil || readVal != "freeze_val" {
		t.Fatalf("reader failed: val=%q, err=%v", readVal, readErr)
	}

	// After freeze completes, key should now be accessible in Frozenmem or SSTable
	postVal, err := man.Get("freeze_key", man.CurrentSeqNo.Load())
	if err != nil || postVal != "freeze_val" {
		t.Fatalf("post-freeze get failed: val=%q, err=%v", postVal, err)
	}

	t.Log("Case 5 PASS: Point read holding RLock safely blocks concurrent freeze; zero race or deadlock")
}

// TestMemtable_Case6_FrozenMemtableVisibilityDuringQueuedAndInflightFlushes verifies
// that keys in frozen Memtables remain continuously readable while queued and while
// actively being written to SSTable by the background worker.
func TestMemtable_Case6_FrozenMemtableVisibilityDuringQueuedAndInflightFlushes(t *testing.T) {
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
	defer man.Close()

	// Seed data in active Memtable
	tx := man.Begin()
	tx.Put("inflight_key_1", "v_inf_1")
	tx.Put("inflight_key_2", "v_inf_2")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	ts := tx.AllocatedSeqNo

	workerInBuild := make(chan struct{})
	workerCanPublish := make(chan struct{})

	man.flushBuildHook = func() error {
		close(workerInBuild)
		<-workerCanPublish
		return nil
	}

	// Trigger freeze -> queued -> worker picks up task
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}

	// Wait until worker is inside flushBuildHook (building SSTable)
	<-workerInBuild

	// Confirm that keys are in Frozenmem
	man.Mu.RLock()
	frozenCount := len(man.Frozenmem)
	man.Mu.RUnlock()
	if frozenCount != 1 {
		t.Fatalf("expected 1 frozen memtable during build, got %d", frozenCount)
	}

	// Perform point reads while worker is mid-build
	v1, err := man.Get("inflight_key_1", ts)
	if err != nil || v1 != "v_inf_1" {
		t.Fatalf("read during build failed for key 1: val=%q, err=%v", v1, err)
	}

	v2, err := man.Get("inflight_key_2", ts)
	if err != nil || v2 != "v_inf_2" {
		t.Fatalf("read during build failed for key 2: val=%q, err=%v", v2, err)
	}

	// Check latestValueLocked on frozen tier
	man.Mu.RLock()
	seq1, err1 := man.latestValueLocked([]byte("inflight_key_1"))
	man.Mu.RUnlock()
	if err1 != nil || seq1 != ts {
		t.Fatalf("latestValueLocked failed during build: seq=%d, err=%v", seq1, err1)
	}

	// Allow worker to finish build and publish SSTable
	close(workerCanPublish)

	// Wait for worker to finish flush
	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		fLen := len(man.Frozenmem)
		sLen := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if fLen == 0 && sLen == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for flush publication: frozen=%d, readers=%d", fLen, sLen)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Reads should now transparently hit the published SSTable
	v1Post, err := man.Get("inflight_key_1", ts)
	if err != nil || v1Post != "v_inf_1" {
		t.Fatalf("post-publish read failed: %v", err)
	}

	t.Log("Case 6 PASS: Continuous key visibility verified during queued and in-flight SSTable build")
}

// TestMemtable_Case7_MultipleFrozenMemtablesDifferentVersions verifies that
// multiple frozen Memtables containing different generations of the same key
// are searched in strict reverse chronological order (newest to oldest).
func TestMemtable_Case7_MultipleFrozenMemtablesDifferentVersions(t *testing.T) {
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
	defer man.Close()

	// Block flush worker so multiple frozen Memtables accumulate in Frozenmem
	workerBlocked := make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() {
		unblockOnce.Do(func() {
			close(workerBlocked)
		})
	}
	defer unblock()

	man.flushBuildHook = func() error {
		<-workerBlocked
		return nil
	}

	// Gen 1: v1 at seq 10
	f1 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f1.Put(EncodeInternalKey([]byte("multi_gen_key"), 10, false), []byte("v1_gen1"))

	// Gen 2: v2 at seq 20
	f2 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f2.Put(EncodeInternalKey([]byte("multi_gen_key"), 20, false), []byte("v2_gen2"))

	// Gen 3: v3 at seq 30
	f3 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f3.Put(EncodeInternalKey([]byte("multi_gen_key"), 30, false), []byte("v3_gen3"))

	// Append to Frozenmem and Enqueue in chronological order
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f1, f2, f3)
	man.Mu.Unlock()
	man.EnqueueFlush(f1)
	man.EnqueueFlush(f2)
	man.EnqueueFlush(f3)

	// Gen 4: v4 at seq 40 in active Memtable
	man.Mem.Put(EncodeInternalKey([]byte("multi_gen_key"), 40, false), []byte("v4_active"))

	// Test snapshot reads across all generation timestamps:
	// ReadTs = 5 (before all writes) -> ErrNotFound
	_, err = man.Get("multi_gen_key", 5)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("readTs 5 expected ErrNotFound, got: %v", err)
	}

	// ReadTs = 15 (sees Gen 1: v1)
	v15, err := man.Get("multi_gen_key", 15)
	if err != nil || v15 != "v1_gen1" {
		t.Fatalf("readTs 15 expected 'v1_gen1', got val=%q, err=%v", v15, err)
	}

	// ReadTs = 25 (sees Gen 2: v2)
	v25, err := man.Get("multi_gen_key", 25)
	if err != nil || v25 != "v2_gen2" {
		t.Fatalf("readTs 25 expected 'v2_gen2', got val=%q, err=%v", v25, err)
	}

	// ReadTs = 35 (sees Gen 3: v3)
	v35, err := man.Get("multi_gen_key", 35)
	if err != nil || v35 != "v3_gen3" {
		t.Fatalf("readTs 35 expected 'v3_gen3', got val=%q, err=%v", v35, err)
	}

	// ReadTs = 45 (sees Active: v4)
	v45, err := man.Get("multi_gen_key", 45)
	if err != nil || v45 != "v4_active" {
		t.Fatalf("readTs 45 expected 'v4_active', got val=%q, err=%v", v45, err)
	}

	// LatestValue should return 40 from active tier
	man.Mu.RLock()
	latestSeq, err := man.latestValueLocked([]byte("multi_gen_key"))
	man.Mu.RUnlock()
	if err != nil || latestSeq != 40 {
		t.Fatalf("latestValue expected 40, got seq=%d, err=%v", latestSeq, err)
	}

	// Unblock worker and let flushes drain
	unblock()
	t.Log("Case 7 PASS: Multiple frozen memtables searched in strict reverse chronological order")
}

// TestMemtable_Case8_TombstonesAcrossActiveAndFrozenTiers verifies that tombstones
// in newer tiers correctly mask older values in frozen tiers and SSTables.
func TestMemtable_Case8_TombstonesAcrossActiveAndFrozenTiers(t *testing.T) {
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
	defer man.Close()

	// Tier 3 (SSTable): user:100 = "profile_v1" at seq 10
	f1 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f1.Put(EncodeInternalKey([]byte("user:100"), 10, false), []byte("profile_v1"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f1)
	man.Mu.Unlock()
	man.EnqueueFlush(f1)

	// Wait for F1 to flush to disk
	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		readers := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if readers == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for F1 to become SSTable")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Tier 2 (Frozenmem): Tombstone for user:100 at seq 20
	workerBlocked := make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() {
		unblockOnce.Do(func() {
			close(workerBlocked)
		})
	}
	defer unblock()

	man.flushBuildHook = func() error {
		<-workerBlocked
		return nil
	}
	f2 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f2.Put(EncodeInternalKey([]byte("user:100"), 20, true), []byte{}) // tombstone
	f2.Put(EncodeInternalKey([]byte("user:200"), 15, false), []byte("profile200_v1"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f2)
	man.Mu.Unlock()
	man.EnqueueFlush(f2)

	// Tier 1 (Active Memtable): Re-created user:100 = "profile_v3" at seq 30
	man.Mem.Put(EncodeInternalKey([]byte("user:100"), 30, false), []byte("profile_v3"))

	// Also add key "user:200" with tombstone in Active at seq 25
	man.Mem.Put(EncodeInternalKey([]byte("user:200"), 25, true), []byte{}) // tombstone in active

	// Verification of user:100
	// At readTs 15: should see SSTable profile_v1
	v100_15, err := man.Get("user:100", 15)
	if err != nil || v100_15 != "profile_v1" {
		t.Fatalf("user:100 at ts 15 expected 'profile_v1', got val=%q, err=%v", v100_15, err)
	}

	// At readTs 25: should hit tombstone in Frozenmem
	_, err = man.Get("user:100", 25)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("user:100 at ts 25 expected ErrKeyDeleted, got: %v", err)
	}

	// At readTs 35: should hit active profile_v3
	v100_35, err := man.Get("user:100", 35)
	if err != nil || v100_35 != "profile_v3" {
		t.Fatalf("user:100 at ts 35 expected 'profile_v3', got val=%q, err=%v", v100_35, err)
	}

	// Verification of user:200
	// At readTs 20: should see Frozenmem profile200_v1
	v200_20, err := man.Get("user:200", 20)
	if err != nil || v200_20 != "profile200_v1" {
		t.Fatalf("user:200 at ts 20 expected 'profile200_v1', got val=%q, err=%v", v200_20, err)
	}

	// At readTs 30: should hit active tombstone
	_, err = man.Get("user:200", 30)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("user:200 at ts 30 expected ErrKeyDeleted, got: %v", err)
	}

	unblock()
	t.Log("Case 8 PASS: Cross-tier tombstone masking and multi-version precedence verified")
}

// TestMemtable_Case9_FrozenToSSTableHandoffZeroLoss verifies that point reads
// continuously succeed during the exact moment a frozen Memtable is retired
// and its corresponding SSTable reader is published into ActiveSSreaders.
func TestMemtable_Case9_FrozenToSSTableHandoffZeroLoss(t *testing.T) {
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
	defer man.Close()

	// Seed key
	tx := man.Begin()
	tx.Put("handoff_test_key", "handoff_constant_value")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	ts := tx.AllocatedSeqNo

	// Trigger flush
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("trigger flush failed: %v", err)
	}

	var readSuccess atomic.Int64
	var readFailures atomic.Int64
	stopReading := make(chan struct{})

	// Launch 4 concurrent readers executing continuous point lookups
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(readerId int) {
			defer wg.Done()
			for {
				select {
				case <-stopReading:
					return
				default:
					val, err := man.Get("handoff_test_key", ts)
					if err != nil || val != "handoff_constant_value" {
						readFailures.Add(1)
					} else {
						readSuccess.Add(1)
					}
				}
			}
		}(i)
	}

	// Wait for flush to complete and publish
	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		fCount := len(man.Frozenmem)
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if fCount == 0 && rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for flush publication")
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(stopReading)
	wg.Wait()

	if readFailures.Load() > 0 {
		t.Fatalf("FATAL: Encountered %d failed reads during frozen-to-SSTable handoff! Value was temporarily unreachable.",
			readFailures.Load())
	}

	t.Logf("Case 9 PASS: Completed %d point reads with 0 dropped values during atomic frozen-to-SSTable publication",
		readSuccess.Load())
}

// TestMemtable_Case10_ReadWriteFlushShutdownOverlap validates total engine stability
// under simultaneous concurrent reading, writing, flushing, and shutdown initiation.
func TestMemtable_Case10_ReadWriteFlushShutdownOverlap(t *testing.T) {
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

	var wg sync.WaitGroup
	stopSignal := make(chan struct{})
	var commits atomic.Int64
	var reads atomic.Int64

	// 2 Writer goroutines
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(writerId int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stopSignal:
					return
				default:
					i++
					tx := man.Begin()
					tx.Put(fmt.Sprintf("k_w%d", writerId), fmt.Sprintf("val_%d", i))
					if err := tx.Commit(); err == nil {
						commits.Add(1)
					}
					time.Sleep(500 * time.Microsecond)
				}
			}
		}(w)
	}

	// 2 Reader goroutines
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func(readerId int) {
			defer wg.Done()
			for {
				select {
				case <-stopSignal:
					return
				default:
					_, _ = man.Get(fmt.Sprintf("k_w%d", readerId), man.CurrentSeqNo.Load())
					reads.Add(1)
					time.Sleep(500 * time.Microsecond)
				}
			}
		}(r)
	}

	// 1 Flusher goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopSignal:
				return
			default:
				_ = man.TriggerFlush()
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	// Run concurrent traffic for 80ms
	time.Sleep(80 * time.Millisecond)

	// Initiate shutdown while traffic is active
	close(stopSignal)
	err = man.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	wg.Wait()

	// Post-shutdown verification: operations return ErrClosed
	txPost := man.Begin()
	txPost.Put("post_key", "post_val")
	if err := txPost.Commit(); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed for post-shutdown commit, got: %v", err)
	}

	_, err = man.Get("k_w0", 100)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed for post-shutdown Get, got: %v", err)
	}

	t.Logf("Case 10 PASS: Clean shutdown after %d commits and %d reads under concurrent flush",
		commits.Load(), reads.Load())
}
