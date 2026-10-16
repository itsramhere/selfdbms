package lsmengine

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCompactionPublication_NilReplacementRejected verifies that passing a nil
// replacement reader to replaceCompactedReaders is rejected with an explicit error
// and leaves the current active reader slice completely unmutated.
func TestCompactionPublication_NilReplacementRejected(t *testing.T) {
	dir := "test_data_pub_nil_rejected"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 10, "val0", false},
	})
	defer r0.Close()

	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "val1", false},
	})
	defer r1.Close()

	current := []*SSTableReader{r0, r1}
	inputs := []*SSTableReader{r0, r1}

	// Attempt publication with nil newReader
	updated, err := replaceCompactedReaders(current, inputs, nil)
	if err == nil {
		t.Fatalf("Expected error for nil newReader, but got nil error and updated: %v", updated)
	}

	if !strings.Contains(err.Error(), "nil replacement reader") {
		t.Fatalf("Unexpected error message: %v", err)
	}

	// Verify current slice was not mutated
	if len(current) != 2 || current[0] != r0 || current[1] != r1 {
		t.Fatalf("Current active reader slice was corrupted: %v", current)
	}

	// Verify reads against current readers remain completely functional
	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: current,
	}

	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Manager.Get failed: %v", err)
	}
	if val != "val1" {
		t.Fatalf("Expected 'val1', got %q", val)
	}
}

// TestCompactionPublication_ValidReplacementSucceeds verifies that publishing
// a valid replacement reader replaces the compacted prefix at index 0 and preserves
// any concurrently flushed readers appended after the prefix.
func TestCompactionPublication_ValidReplacementSucceeds(t *testing.T) {
	dir := "test_data_pub_valid_success"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 10, "val0", false},
	})
	defer r0.Close()

	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "val1", false},
	})
	defer r1.Close()

	// r2 simulates a concurrent flush appended while compaction was running
	r2 := createTestSSTable(t, dir+"/r2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 30, "val2_concurrent", false},
	})
	defer r2.Close()

	rCompacted := createTestSSTable(t, dir+"/r_compacted.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "val1", false},
	})
	defer rCompacted.Close()

	current := []*SSTableReader{r0, r1, r2}
	inputs := []*SSTableReader{r0, r1}

	updated, err := replaceCompactedReaders(current, inputs, rCompacted)
	if err != nil {
		t.Fatalf("Unexpected error from replaceCompactedReaders: %v", err)
	}

	if len(updated) != 2 {
		t.Fatalf("Expected 2 updated readers, got %d", len(updated))
	}
	if updated[0] != rCompacted {
		t.Fatalf("Expected index 0 to be rCompacted, got %v", updated[0])
	}
	if updated[1] != r2 {
		t.Fatalf("Expected index 1 to be r2, got %v", updated[1])
	}

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: updated,
	}

	// Read at ts 100 should see r2 (val2_concurrent)
	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Manager.Get failed: %v", err)
	}
	if val != "val2_concurrent" {
		t.Fatalf("Expected 'val2_concurrent', got %q", val)
	}

	// Read at ts 25 should see rCompacted (val1)
	val25, err := man.Get("key1", 25)
	if err != nil {
		t.Fatalf("Manager.Get failed at ts 25: %v", err)
	}
	if val25 != "val1" {
		t.Fatalf("Expected 'val1' at ts 25, got %q", val25)
	}
}

// TestCompactionPublication_AbortedPublicationPreservesActiveReaders verifies that
// when publication validation fails, original active SSTables remain intact on disk,
// their readers remain functional in m.ActiveSSreaders, and no file descriptor leaks occur.
func TestCompactionPublication_AbortedPublicationPreservesActiveReaders(t *testing.T) {
	dir := "test_data_pub_abort_preserve"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0Path := dir + "/r0.sst"
	r1Path := dir + "/r1.sst"

	r0 := createTestSSTable(t, r0Path, []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kA", 10, "valA", false},
	})
	r1 := createTestSSTable(t, r1Path, []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kB", 20, "valB", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}

	// Simulate publication failure: before publication, mutate ActiveSSreaders so
	// replaceCompactedReaders detects an inconsistency (e.g. invalid prefix).
	man.beforeCompactionPublishHook = func() {
		man.Mu.Lock()
		// Remove r0 from ActiveSSreaders to simulate that it's no longer the active prefix
		man.ActiveSSreaders = []*SSTableReader{r1}
		man.Mu.Unlock()
	}

	err := man.CompactAll()
	if err == nil {
		t.Fatalf("Expected CompactAll to fail due to publication prefix mismatch, but got nil")
	}

	// Verify original active reader r1 is still functional and was not destroyed
	valB, err := man.Get("kB", 100)
	if err != nil {
		t.Fatalf("Get kB failed after aborted compaction: %v", err)
	}
	if valB != "valB" {
		t.Fatalf("Expected 'valB', got %q", valB)
	}

	// Verify that r1 file still exists on disk
	if _, err := os.Stat(r1Path); os.IsNotExist(err) {
		t.Fatalf("r1 file was deleted despite aborted compaction")
	}

	// Cleanup
	r0.Close()
	r1.Close()
	man.ActiveSSreaders = nil

	// Verify files can be cleanly removed on NTFS
	if err := os.Remove(r0Path); err != nil {
		t.Fatalf("Failed to remove r0Path on NTFS (possible handle leak): %v", err)
	}
	if err := os.Remove(r1Path); err != nil {
		t.Fatalf("Failed to remove r1Path on NTFS (possible handle leak): %v", err)
	}
}

// TestCompactionPublication_EmptyCompactionProducesValidReader verifies that
// when all input records are tombstones, CompactAll produces an 8-byte valid SSTable
// reader with empty index, successfully replaces the inputs, and lookups return ErrNotFound.
func TestCompactionPublication_EmptyCompactionProducesValidReader(t *testing.T) {
	dir := "test_data_pub_empty_compaction"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0Path := fmt.Sprintf("%s/r0_%d.sst", dir, time.Now().UnixNano())
	r1Path := fmt.Sprintf("%s/r1_%d.sst", dir, time.Now().UnixNano())

	// Both SSTables contain only tombstones
	r0 := createTestSSTable(t, r0Path, []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"deletedKey1", 10, "", true},
	})
	r1 := createTestSSTable(t, r1Path, []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"deletedKey2", 20, "", true},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		compactionMu:    sync.Mutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}

	// Compact the two tombstone-only tables
	err := man.CompactAll()
	if err != nil {
		t.Fatalf("CompactAll failed on tombstone-only tables: %v", err)
	}

	// After compaction, len(ActiveSSreaders) should be 1 (the new compacted SSTable)
	man.Mu.RLock()
	activeCount := len(man.ActiveSSreaders)
	var newR *SSTableReader
	if activeCount == 1 {
		newR = man.ActiveSSreaders[0]
	}
	man.Mu.RUnlock()

	if activeCount != 1 {
		t.Fatalf("Expected 1 active reader after compaction, got %d", activeCount)
	}
	if newR == nil {
		t.Fatalf("Expected non-nil active reader after compaction")
	}

	// Check that new reader has len(Index) == 0
	if len(newR.Index) != 0 {
		t.Fatalf("Expected empty index in compacted reader, got %d", len(newR.Index))
	}

	// Lookups for deleted keys must return ErrNotFound
	_, err = man.Get("deletedKey1", 100)
	if err != ErrNotFound {
		t.Fatalf("Expected ErrNotFound for deletedKey1, got %v", err)
	}
	_, err = man.Get("deletedKey2", 100)
	if err != ErrNotFound {
		t.Fatalf("Expected ErrNotFound for deletedKey2, got %v", err)
	}

	// Clean shutdown / close
	newR.Close()
	os.Remove(newR.File.Name())
}
