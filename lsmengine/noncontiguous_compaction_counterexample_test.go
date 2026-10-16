package lsmengine

import (
	"os"
	"sync"
	"testing"
)

// Regression test for Issue E-005 and INV-COMPACT-1:
// Verifies that noncontiguous, non-prefix, duplicate, and invalid compaction inputs
// are strictly rejected by replaceCompactedReaders without mutating the active reader set.
func TestE005_NoncontiguousCompactionSafelyRejected(t *testing.T) {
	dir := "test_data_e005_safely_rejected"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 10, "v0_old", false}})
	defer r0.Close()

	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 20, "v1_mid", false}})
	defer r1.Close()

	r2 := createTestSSTable(t, dir+"/r2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 30, "v2_new", false}})
	defer r2.Close()

	mCompact := createTestSSTable(t, dir+"/m_compact.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{{"key1", 30, "v2_new", false}})
	defer mCompact.Close()

	current := []*SSTableReader{r0, r1, r2}

	// 1. Noncontiguous inputs [r0, r2] skipping r1
	inputsNoncontiguous := []*SSTableReader{r0, r2}
	updated, err := replaceCompactedReaders(current, inputsNoncontiguous, mCompact)
	if err == nil {
		t.Fatalf("expected noncontiguous inputs to be rejected, got nil error")
	}
	expectedErr := "compaction aborted: input readers are not strictly contiguous"
	if err.Error() != expectedErr {
		t.Errorf("expected error %q, got %q", expectedErr, err.Error())
	}
	if updated != nil {
		t.Errorf("expected nil updated slice on error, got %v", updated)
	}
	// Verify current is completely unmutated
	if len(current) != 3 || current[0] != r0 || current[1] != r1 || current[2] != r2 {
		t.Fatalf("current slice was mutated on rejected noncontiguous compaction: %v", current)
	}
	t.Logf("Noncontiguous inputs [r0, r2] safely rejected: %v", err)

	// 2. Non-prefix contiguous inputs [r1, r2] (not starting at index 0 under full-prefix policy)
	inputsNonPrefix := []*SSTableReader{r1, r2}
	updated, err = replaceCompactedReaders(current, inputsNonPrefix, mCompact)
	if err == nil {
		t.Fatalf("expected non-prefix inputs to be rejected under prefix policy, got nil error")
	}
	expectedPrefixErr := "compaction aborted: inputs must form a prefix starting at index 0"
	if err.Error() != expectedPrefixErr {
		t.Errorf("expected error %q, got %q", expectedPrefixErr, err.Error())
	}
	if updated != nil {
		t.Errorf("expected nil updated slice on error, got %v", updated)
	}
	t.Logf("Non-prefix inputs [r1, r2] safely rejected: %v", err)

	// 3. Duplicate inputs [r0, r0]
	inputsDuplicate := []*SSTableReader{r0, r0}
	updated, err = replaceCompactedReaders(current, inputsDuplicate, mCompact)
	if err == nil {
		t.Fatalf("expected duplicate inputs to be rejected, got nil error")
	}
	expectedDupErr := "compaction aborted: duplicate input reader"
	if err.Error() != expectedDupErr {
		t.Errorf("expected error %q, got %q", expectedDupErr, err.Error())
	}
	t.Logf("Duplicate inputs [r0, r0] safely rejected: %v", err)

	// 4. Nil replacement reader
	updated, err = replaceCompactedReaders(current, []*SSTableReader{r0, r1}, nil)
	if err == nil {
		t.Fatalf("expected nil replacement reader to be rejected, got nil error")
	}
	expectedNilErr := "compaction aborted: nil replacement reader"
	if err.Error() != expectedNilErr {
		t.Errorf("expected error %q, got %q", expectedNilErr, err.Error())
	}
	t.Logf("Nil replacement reader safely rejected: %v", err)
}

// Verifies that a valid prefix compaction preserves chronological reader ordering INV-ORDER-1,
// keeps concurrently flushed SSTables visible, and correctly serves point reads across timestamps.
func TestE005_ValidPrefixCompactionAndMVCCCorrectness(t *testing.T) {
	dir := "test_data_e005_valid_prefix"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// R0: key1 = "v0" (seq 10), key2 = "k2_v0" (seq 10)
	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 10, "v0", false},
		{"key2", 10, "k2_v0", false},
	})
	defer r0.Close()

	// R1: key1 = "v1" (seq 20), key3 = "k3_v1" (seq 20)
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "v1", false},
		{"key3", 20, "k3_v1", false},
	})
	defer r1.Close()

	// R2 (concurrently flushed): key1 = "v2" (seq 30), key2 tombstone (seq 30)
	r2 := createTestSSTable(t, dir+"/r2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 30, "v2", false},
		{"key2", 30, "", true},
	})
	defer r2.Close()

	// Compacted output M (merging prefix [r0, r1]):
	// Contains key1="v1" (seq 20), key1="v0" (seq 10), key2="k2_v0" (seq 10), key3="k3_v1" (seq 20)
	mCompact := createTestSSTable(t, dir+"/m_compact.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "v1", false},
		{"key1", 10, "v0", false},
		{"key2", 10, "k2_v0", false},
		{"key3", 20, "k3_v1", false},
	})
	defer mCompact.Close()

	current := []*SSTableReader{r0, r1, r2}
	inputs := []*SSTableReader{r0, r1}

	// Publish prefix compaction [r0, r1] -> mCompact
	updated, err := replaceCompactedReaders(current, inputs, mCompact)
	if err != nil {
		t.Fatalf("valid prefix compaction failed: %v", err)
	}

	// Verify resulting slice ordering: [mCompact, r2]
	if len(updated) != 2 || updated[0] != mCompact || updated[1] != r2 {
		t.Fatalf("unexpected reader order: got %v, expected [mCompact, r2]", updated)
	}
	t.Logf("Valid prefix compaction published: [mCompact, r2]")

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: updated,
	}

	// 1. Read key1 at readTs 100: must return "v2" from r2 (not masked by mCompact)
	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Get key1@100 failed: %v", err)
	}
	if val != "v2" {
		t.Errorf("expected key1@100 = 'v2', got %q", val)
	}

	// 2. Read key1 at readTs 25: must return "v1" from mCompact (r2 has seq 30 > 25)
	val, err = man.Get("key1", 25)
	if err != nil {
		t.Fatalf("Get key1@25 failed: %v", err)
	}
	if val != "v1" {
		t.Errorf("expected key1@25 = 'v1', got %q", val)
	}

	// 3. Read key1 at readTs 15: must return "v0" from mCompact
	val, err = man.Get("key1", 15)
	if err != nil {
		t.Fatalf("Get key1@15 failed: %v", err)
	}
	if val != "v0" {
		t.Errorf("expected key1@15 = 'v0', got %q", val)
	}

	// 4. Read key2 at readTs 100: tombstone in r2 (seq 30) must suppress older version in mCompact (seq 10)
	_, err = man.Get("key2", 100)
	if err != ErrKeyDeleted {
		t.Errorf("expected ErrKeyDeleted for key2@100, got %v", err)
	}

	// 5. Read key2 at readTs 25: tombstone in r2 has seq 30 > 25, so older version in mCompact (seq 10) is visible!
	val, err = man.Get("key2", 25)
	if err != nil {
		t.Fatalf("Get key2@25 failed: %v", err)
	}
	if val != "k2_v0" {
		t.Errorf("expected key2@25 = 'k2_v0', got %q", val)
	}

	// 6. Read key3 at readTs 100: present only in mCompact (seq 20)
	val, err = man.Get("key3", 100)
	if err != nil {
		t.Fatalf("Get key3@100 failed: %v", err)
	}
	if val != "k3_v1" {
		t.Errorf("expected key3@100 = 'k3_v1', got %q", val)
	}

	t.Logf("All MVCC visibility assertions passed across [mCompact, r2]")
}
