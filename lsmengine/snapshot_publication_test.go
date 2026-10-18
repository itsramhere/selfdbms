package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 1. Flush Publication: Atomic addition of new SSTable reader to snapshot and removal of frozen memtable.
func TestSnapshotPublication_FlushAtomicSwap(t *testing.T) {
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

	// Initial: 0 readers, 0 frozen memtables
	snap0, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	man.ReleaseSnapshot(snap0)
	if len(snap0.Readers()) != 0 {
		t.Fatalf("expected 0 readers initially, got %d", len(snap0.Readers()))
	}

	// Write and flush
	tx := man.Begin()
	tx.Put("swap_k1", "swap_v1")
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

	// After flush completes: exactly 1 reader in snapshot, 0 in Frozenmem
	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap1)

	if len(snap1.Readers()) != 1 {
		t.Fatalf("expected 1 reader after flush, got %d", len(snap1.Readers()))
	}
	if snap1.ID() <= snap0.ID() {
		t.Fatalf("expected snapshot seq > initial snap0.ID() (%d), got %d", snap0.ID(), snap1.ID())
	}
	if snap1.Readers()[0].State() != SharedReaderActive {
		t.Fatalf("expected reader state Active, got %v", snap1.Readers()[0].State())
	}
}

// 2. Reads Crossing Swaps: A reader holding snapshot S_k observes a completely consistent view
// even while S_{k+1} is concurrently published.
func TestSnapshotPublication_ReadsCrossingSwap(t *testing.T) {
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

	// Initial version: k1 = "v1"
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

	// Reader 1 pins snapshot S1
	snap1, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snap1)

	// Update k1 = "v2" and flush S2 while Reader 1 holds S1
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

	// Reader 1 reads against snap1: MUST see "v1"
	val1, err := snap1.Readers()[0].Reader.Get([]byte("k1"), 1)
	if err != nil {
		t.Fatalf("snap1 reader Get failed: %v", err)
	}
	if string(val1) != "v1" {
		t.Fatalf("snap1 reader read dirty data; got %q, expected %q", string(val1), "v1")
	}

	// New reader pins S2 and queries Manager.Get with current readTs
	readTs := man.CurrentSeqNo.Load()
	val2, err := man.Get("k1", readTs)
	if err != nil {
		t.Fatalf("Manager.Get failed: %v", err)
	}
	if val2 != "v2" {
		t.Fatalf("new reader on S2 got %q, expected %q", val2, "v2")
	}
}

// 3. Compaction Atomic Swap: Compaction replaces input SSTables with compacted SSTables in atomic swap.
func TestSnapshotPublication_CompactionAtomicSwap(t *testing.T) {
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

	// Seed two SSTables
	for i := 1; i <= 2; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("comp_k%d", i), fmt.Sprintf("comp_v%d", i))
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

	preSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(preSnap)

	if len(preSnap.Readers()) != 2 {
		t.Fatalf("expected 2 readers before compaction, got %d", len(preSnap.Readers()))
	}
	input0 := preSnap.Readers()[0]
	input1 := preSnap.Readers()[1]

	// Run compaction
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	postSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(postSnap)

	// Exactly 1 compacted reader in postSnap
	if len(postSnap.Readers()) != 1 {
		t.Fatalf("expected 1 reader after compaction, got %d", len(postSnap.Readers()))
	}
	compactedReader := postSnap.Readers()[0]
	if compactedReader == input0 || compactedReader == input1 {
		t.Fatalf("compacted reader must be a newly allocated SharedReader")
	}

	// Verify both keys are queryable in the compacted reader
	ts := man.CurrentSeqNo.Load()
	valA, err := man.Get("comp_k1", ts)
	if err != nil || valA != "comp_v1" {
		t.Fatalf("failed to query comp_k1: got val=%q, err=%v", valA, err)
	}
	valB, err := man.Get("comp_k2", ts)
	if err != nil || valB != "comp_v2" {
		t.Fatalf("failed to query comp_k2: got val=%q, err=%v", valB, err)
	}
}

// 4. Obsolete Marking: Replaced readers are marked obsolete and retired safely.
func TestSnapshotPublication_ObsoleteMarking(t *testing.T) {
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

	// Seed two SSTables
	for i := 1; i <= 2; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("obs_k%d", i), fmt.Sprintf("obs_v%d", i))
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

	oldSnap, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}

	in0 := oldSnap.Readers()[0]
	in1 := oldSnap.Readers()[1]

	// Compact while oldSnap is pinned
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Both input readers MUST be marked obsolete
	if !in0.IsObsolete() {
		t.Fatalf("expected in0 to be marked obsolete post-compaction")
	}
	if !in1.IsObsolete() {
		t.Fatalf("expected in1 to be marked obsolete post-compaction")
	}

	// While oldSnap is pinned, files must NOT be unlinked yet
	if _, err := os.Stat(in0.Path); os.IsNotExist(err) {
		t.Fatalf("in0 file prematurely deleted while oldSnap is pinned")
	}
	if _, err := os.Stat(in1.Path); os.IsNotExist(err) {
		t.Fatalf("in1 file prematurely deleted while oldSnap is pinned")
	}

	// Release oldSnap: triggers drainage and unlinking of obsolete input tables
	man.ReleaseSnapshot(oldSnap)

	// Wait briefly for asynchronous background cleanup to complete
	time.Sleep(50 * time.Millisecond)

	// Input readers should have transitioned to Reclaimed or Closed
	if in0.State() != SharedReaderReclaimed && in0.State() != SharedReaderClosed {
		t.Fatalf("expected in0 to be Reclaimed/Closed, got %v", in0.State())
	}
}

// 5. Fail-Stop Preservation: If flush fails, fail-stop state is preserved,
// snapshot is not corrupted, and memtable remains safely in Frozenmem.
func TestSnapshotPublication_FailStopPreservation(t *testing.T) {
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

	// Initial successful flush
	tx1 := man.Begin()
	tx1.Put("good_k", "good_v")
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

	snapBefore, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snapBefore)

	// Inject flush build failure
	simulatedErr := errors.New("simulated disk full on flush")
	man.flushBuildHook = func() error {
		return simulatedErr
	}

	tx2 := man.Begin()
	tx2.Put("fail_k", "fail_v")
	if err := tx2.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}

	// Wait for worker to encounter the failure
	time.Sleep(50 * time.Millisecond)

	// Engine MUST be in fail-stop state
	man.Mu.RLock()
	flushErr := man.flushErr
	frozenLen := len(man.Frozenmem)
	man.Mu.RUnlock()

	if !errors.Is(flushErr, simulatedErr) {
		t.Fatalf("expected flushErr %v, got %v", simulatedErr, flushErr)
	}
	// The failed Memtable MUST NOT be silently discarded from Frozenmem
	if frozenLen == 0 {
		t.Fatalf("failed Memtable was discarded from Frozenmem")
	}

	// Current snapshot must NOT be corrupted (remains identical to snapBefore)
	snapAfter, err := man.AcquireSnapshot()
	if err != nil {
		t.Fatalf("AcquireSnapshot failed: %v", err)
	}
	defer man.ReleaseSnapshot(snapAfter)

	if len(snapAfter.Readers()) != len(snapBefore.Readers()) {
		t.Fatalf("snapshot readers changed during failed flush; before=%d, after=%d",
			len(snapBefore.Readers()), len(snapAfter.Readers()))
	}
}
