package lsmengine

import (
	"errors"
	"os"
	"testing"
)

// Helper to write and flush a key-value pair into an active SSTable
func commitAndFlush(t *testing.T, man *Manager, key, val string) {
	txn := man.Begin()
	txn.Put(key, val)
	if err := txn.Commit(); err != nil {
		t.Fatalf("commit failed for key %s: %v", key, err)
	}
	forceSyncFlush(man)
}

// 1. Core deterministic test: Compaction merges A and B; concurrent flush publishes C;
// Resulting reader set must include M and C, with A and B retired.
func TestCompactionPublication_ConcurrentFlushOverlap(t *testing.T) {
	dir := "test_data_compact_overlap"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	os.Chdir(dir)
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// 1. Create SSTable A and SSTable B
	commitAndFlush(t, man, "key_a", "val_a")
	commitAndFlush(t, man, "key_b", "val_b")

	man.Mu.RLock()
	if len(man.ActiveSSreaders) != 2 {
		man.Mu.RUnlock()
		t.Fatalf("expected 2 active readers, got %d", len(man.ActiveSSreaders))
	}
	readerA := man.ActiveSSreaders[0]
	readerB := man.ActiveSSreaders[1]
	man.Mu.RUnlock()

	// Channels to coordinate deterministic interleaving
	compactionPaused := make(chan struct{})
	resumeCompaction := make(chan struct{})

	man.beforeCompactionPublishHook = func() {
		close(compactionPaused)
		<-resumeCompaction
	}

	// 2. Start compaction in background goroutine
	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	// 3. Wait until compaction has finished k-way merge and paused right before publication
	<-compactionPaused

	// 4. Concurrent flush publishes SSTable C
	commitAndFlush(t, man, "key_c", "val_c")

	// Verify that at this exact instant, ActiveSSreaders contains A, B, and C
	man.Mu.RLock()
	if len(man.ActiveSSreaders) != 3 {
		man.Mu.RUnlock()
		t.Fatalf("expected 3 readers while compaction paused, got %d", len(man.ActiveSSreaders))
	}
	readerC := man.ActiveSSreaders[2]
	man.Mu.RUnlock()

	// 5. Resume compaction publication
	close(resumeCompaction)

	// Wait for compaction to finish
	if err := <-compactionDone; err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// 6. Verify resulting reader set
	man.Mu.RLock()
	defer man.Mu.RUnlock()

	if len(man.ActiveSSreaders) != 2 {
		t.Fatalf("expected 2 active readers after publication (M and C), got %d", len(man.ActiveSSreaders))
	}

	readerM := man.ActiveSSreaders[0]
	if man.ActiveSSreaders[1] != readerC {
		t.Fatalf("expected reader C to be preserved at index 1, got %v", man.ActiveSSreaders[1].File.Name())
	}

	if readerM == readerA || readerM == readerB || readerM == readerC {
		t.Fatalf("expected reader M to be a new compacted reader, got %v", readerM.File.Name())
	}

	// Verify readers A and B are NOT present
	for _, r := range man.ActiveSSreaders {
		if r == readerA || r == readerB {
			t.Fatalf("compacted input reader %v was not retired from active set!", r.File.Name())
		}
	}

	// 7. Verify all committed logical keys remain accessible
	valA, errA := man.Get("key_a", 1000)
	if errA != nil || valA != "val_a" {
		t.Errorf("expected val_a, got %q, err %v", valA, errA)
	}
	valB, errB := man.Get("key_b", 1000)
	if errB != nil || valB != "val_b" {
		t.Errorf("expected val_b, got %q, err %v", valB, errB)
	}
	valC, errC := man.Get("key_c", 1000)
	if errC != nil || valC != "val_c" {
		t.Errorf("expected val_c, got %q, err %v", valC, errC)
	}
	t.Logf("Compaction overlap verification passed: Readers [M, C] correctly published without losing C")
}

// 2. Test concurrent flush publishing a newer version of an existing key during compaction
func TestCompactionPublication_ConcurrentFlushNewerVersion(t *testing.T) {
	dir := "test_data_compact_newer_ver"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	os.Chdir(dir)
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// Initial version of key "k" = "v_initial" in SSTable A
	commitAndFlush(t, man, "k", "v_initial")
	commitAndFlush(t, man, "other", "v_other")

	compactionPaused := make(chan struct{})
	resumeCompaction := make(chan struct{})

	man.beforeCompactionPublishHook = func() {
		close(compactionPaused)
		<-resumeCompaction
	}

	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	<-compactionPaused

	// Concurrent flush writes newer version "k" = "v_updated"
	commitAndFlush(t, man, "k", "v_updated")

	close(resumeCompaction)

	if err := <-compactionDone; err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Newer version "v_updated" must be returned by Get
	val, err := man.Get("k", 1000)
	if err != nil || val != "v_updated" {
		t.Fatalf("expected newer version v_updated, got %q, err %v", val, err)
	}
	t.Logf("Newer version visibility verified: %q", val)
}

// 3. Test tombstone visibility during compaction publication
func TestCompactionPublication_ConcurrentFlushTombstone(t *testing.T) {
	dir := "test_data_compact_tombstone"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	os.Chdir(dir)
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	commitAndFlush(t, man, "del_key", "active_val")
	commitAndFlush(t, man, "keep_key", "keep_val")

	compactionPaused := make(chan struct{})
	resumeCompaction := make(chan struct{})

	man.beforeCompactionPublishHook = func() {
		close(compactionPaused)
		<-resumeCompaction
	}

	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	<-compactionPaused

	// Concurrent flush deletes "del_key"
	tDel := man.Begin()
	tDel.Delete("del_key")
	if err := tDel.Commit(); err != nil {
		t.Fatalf("delete commit failed: %v", err)
	}
	forceSyncFlush(man)

	close(resumeCompaction)

	if err := <-compactionDone; err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// At snapshot 1000, key must be reported deleted
	_, err := man.Get("del_key", 1000)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("expected ErrKeyDeleted, got: %v", err)
	}
	t.Logf("Tombstone visibility correctly preserved after publication: %v", err)
}

// 4. Test competing compactions selecting overlapping inputs
func TestCompactionPublication_OverlappingInputsValidation(t *testing.T) {
	dir := "test_data_compact_overlapping"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	os.Chdir(dir)
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	commitAndFlush(t, man, "k1", "v1")
	commitAndFlush(t, man, "k2", "v2")

	compactionPaused := make(chan struct{})
	resumeCompaction := make(chan struct{})

	man.beforeCompactionPublishHook = func() {
		close(compactionPaused)
		<-resumeCompaction
	}

	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	<-compactionPaused

	// Simulate an overlapping compaction or reader removal while Compaction 1 is paused:
	// Remove one of the input readers from ActiveSSreaders
	man.Mu.Lock()
	removedReader := man.ActiveSSreaders[0]
	man.ActiveSSreaders = man.ActiveSSreaders[1:]
	man.Mu.Unlock()

	close(resumeCompaction)

	err := <-compactionDone
	if err == nil {
		t.Fatalf("expected compaction to abort due to missing input reader, got nil")
	}
	expected := "compaction aborted: input reader is no longer active"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
	t.Logf("Overlapping inputs safely rejected with validation error: %v", err)

	removedReader.Close()
}
