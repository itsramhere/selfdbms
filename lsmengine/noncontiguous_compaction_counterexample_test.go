package lsmengine

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// TestE005_NoncontiguousCompactionRejected verifies that attempting noncontiguous
// compaction input selection is rejected by replaceCompactedReaders with a validation
// error without mutating the active reader set (safeguarding INV-ORDER-1 and INV-COMPACT-1).
func TestE005_NoncontiguousCompactionRejected(t *testing.T) {
	dir := "test_data_e005_rejected"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// R0: written at seq 10, key1 = "v0_old"
	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 10, "v0_old", false},
	})
	defer r0.Close()

	// R1: written at seq 20, key1 = "v1_mid"
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "v1_mid", false},
	})
	defer r1.Close()

	// R2: written at seq 30, key1 = "v2_new"
	r2 := createTestSSTable(t, dir+"/r2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 30, "v2_new", false},
	})
	defer r2.Close()

	mCompact := createTestSSTable(t, dir+"/m_compact.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 30, "v2_new", false},
	})
	defer mCompact.Close()

	current := []*SSTableReader{r0, r1, r2}
	// Noncontiguous inputs: [r0, r2] (skips r1)
	noncontiguousInputs := []*SSTableReader{r0, r2}

	updated, err := replaceCompactedReaders(current, noncontiguousInputs, mCompact)
	if err == nil {
		t.Fatalf("Expected replaceCompactedReaders to reject noncontiguous inputs, but got nil error and updated: %v", updated)
	}

	if !strings.Contains(err.Error(), "inputs are not contiguous in current active readers") {
		t.Fatalf("Unexpected error message: %v", err)
	}

	// Verify current slice was not mutated
	if len(current) != 3 || current[0] != r0 || current[1] != r1 || current[2] != r2 {
		t.Fatalf("Active readers slice was mutated despite validation error: %v", current)
	}

	// Verify reads against current readers remain completely correct
	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: current,
	}

	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Manager.Get failed: %v", err)
	}
	if val != "v2_new" {
		t.Fatalf("Expected 'v2_new', got %q", val)
	}
}

// TestE005_NonPrefixCompactionRejected verifies that attempting to compact a contiguous range
// that does not start at index 0 is rejected under the full-prefix policy.
func TestE005_NonPrefixCompactionRejected(t *testing.T) {
	dir := "test_data_e005_nonprefix"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 10, "v0", false}})
	defer r0.Close()

	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 20, "v1", false}})
	defer r1.Close()

	r2 := createTestSSTable(t, dir+"/r2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 30, "v2", false}})
	defer r2.Close()

	mCompact := createTestSSTable(t, dir+"/m_compact.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 30, "v2", false}})
	defer mCompact.Close()

	current := []*SSTableReader{r0, r1, r2}
	// Contiguous range [r1, r2], but does not start at index 0
	nonPrefixInputs := []*SSTableReader{r1, r2}

	_, err := replaceCompactedReaders(current, nonPrefixInputs, mCompact)
	if err == nil {
		t.Fatalf("Expected rejection for non-prefix inputs, but got nil error")
	}
	if !strings.Contains(err.Error(), "inputs must start at index 0 to preserve full-prefix policy") {
		t.Fatalf("Unexpected error message: %v", err)
	}
}

// TestE005_DuplicateInputsRejected verifies that duplicate input readers are rejected.
func TestE005_DuplicateInputsRejected(t *testing.T) {
	dir := "test_data_e005_duplicate"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 10, "v0", false}})
	defer r0.Close()

	mCompact := createTestSSTable(t, dir+"/m_compact.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 10, "v0", false}})
	defer mCompact.Close()

	current := []*SSTableReader{r0}
	duplicateInputs := []*SSTableReader{r0, r0}

	_, err := replaceCompactedReaders(current, duplicateInputs, mCompact)
	if err == nil {
		t.Fatalf("Expected rejection for duplicate inputs, but got nil error")
	}
	if !strings.Contains(err.Error(), "more inputs than active readers") && !strings.Contains(err.Error(), "duplicate input reader in inputs") {
		t.Fatalf("Unexpected error message: %v", err)
	}
}

// TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility verifies that
// compacting a valid prefix preserves chronological order, correctly presents
// concurrent flushes, does not mask tombstones or newer values, and respects read timestamps.
func TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility(t *testing.T) {
	dir := "test_data_e005_valid_prefix"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// R0: seq 10, kA = "vA_10", kB = "vB_10"
	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kA", 10, "vA_10", false},
		{"kB", 10, "vB_10", false},
	})
	defer r0.Close()

	// R1: seq 20, kA = "vA_20", kB deleted (tombstone)
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kA", 20, "vA_20", false},
		{"kB", 20, "", true},
	})
	defer r1.Close()

	// R_flush: concurrently flushed table at seq 30, kA = "vA_30", kC = "vC_30"
	rFlush := createTestSSTable(t, dir+"/r_flush.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kA", 30, "vA_30", false},
		{"kC", 30, "vC_30", false},
	})
	defer rFlush.Close()

	// Merged M01 produced from [r0, r1]: contains kA = "vA_20", kB tombstone dropped or not present
	m01 := createTestSSTable(t, dir+"/m01.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"kA", 20, "vA_20", false},
	})
	defer m01.Close()

	current := []*SSTableReader{r0, r1, rFlush}
	inputs := []*SSTableReader{r0, r1}

	updated, err := replaceCompactedReaders(current, inputs, m01)
	if err != nil {
		t.Fatalf("replaceCompactedReaders failed unexpectedly on valid prefix: %v", err)
	}

	// Verify updated slice: [m01, rFlush]
	if len(updated) != 2 {
		t.Fatalf("Expected 2 readers in updated slice, got %d", len(updated))
	}
	if updated[0] != m01 || updated[1] != rFlush {
		t.Fatalf("Unexpected updated slice composition: %v", updated)
	}

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: updated,
	}

	// 1. Read at ts 100:
	// kA: newer version in rFlush (seq 30) must be returned, NOT masked by m01 (seq 20)
	valA, err := man.Get("kA", 100)
	if err != nil {
		t.Fatalf("Get kA at ts 100 failed: %v", err)
	}
	if valA != "vA_30" {
		t.Fatalf("Expected 'vA_30', got %q", valA)
	}

	// kB: was deleted in r1 (seq 20) and not in rFlush -> must return ErrNotFound
	_, err = man.Get("kB", 100)
	if err != ErrNotFound {
		t.Fatalf("Expected ErrNotFound for kB at ts 100, got: %v", err)
	}

	// kC: in rFlush (seq 30) -> must be visible
	valC, err := man.Get("kC", 100)
	if err != nil {
		t.Fatalf("Get kC at ts 100 failed: %v", err)
	}
	if valC != "vC_30" {
		t.Fatalf("Expected 'vC_30', got %q", valC)
	}

	// 2. MVCC timestamp checks:
	// At ts 25:
	// kA in rFlush has seq 30 > 25 (skipped). kA in m01 has seq 20 <= 25 -> returns "vA_20"
	valA_25, err := man.Get("kA", 25)
	if err != nil {
		t.Fatalf("Get kA at ts 25 failed: %v", err)
	}
	if valA_25 != "vA_20" {
		t.Fatalf("Expected 'vA_20' at ts 25, got %q", valA_25)
	}

	// kC at ts 25: only exists at seq 30 > 25 -> must return ErrNotFound
	_, err = man.Get("kC", 25)
	if err != ErrNotFound {
		t.Fatalf("Expected ErrNotFound for kC at ts 25, got: %v", err)
	}

	// At ts 5: nothing was written before ts 5 -> ErrNotFound for all
	_, err = man.Get("kA", 5)
	if err != ErrNotFound {
		t.Fatalf("Expected ErrNotFound for kA at ts 5, got: %v", err)
	}
}
