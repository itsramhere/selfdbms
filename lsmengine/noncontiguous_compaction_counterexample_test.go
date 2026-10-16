package lsmengine

import (
	"os"
	"sync"
	"testing"
)

// Deterministic counterexample proving that noncontiguous compaction inputs
// in a flat slice architecture violate invariant INV-ORDER-1, causing stale reads in Manager.Get.
func TestCounterexample_NoncontiguousCompactionViolatesOrder(t *testing.T) {
	dir := "test_data_noncontiguous_counterexample"
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

	// Suppose compaction selects noncontiguous inputs: [r0, r2]
	// The merged output M contains the newest version of key1 between r0 and r2, which is "v2_new" (seq 30).
	mCompact := createTestSSTable(t, dir+"/m_compact.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 30, "v2_new", false},
	})
	defer mCompact.Close()

	// When replaceCompactedReaders replaces [r0, r2] with mCompact in [r0, r1, r2],
	// mCompact is placed at index 0 (r0's position), resulting in [mCompact, r1].
	current := []*SSTableReader{r0, r1, r2}
	inputs := []*SSTableReader{r0, r2}

	updated, err := replaceCompactedReaders(current, inputs, mCompact)
	if err != nil {
		t.Fatalf("replaceCompactedReaders failed: %v", err)
	}

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: updated,
	}

	// Now a reader attempts to read key1 at timestamp 100.
	// Expected latest version: "v2_new" (from R2/M, seq 30).
	// Actual result from Manager.Get:
	val, err := man.Get("key1", 100)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	t.Logf("Result of Manager.Get after noncontiguous compaction: %q", val)

	if val != "v2_new" {
		t.Fatalf("COUNTEREXAMPLE PROVEN: Noncontiguous compaction resulted in stale read %q (expected %q). r1 (seq 20) masked newer compacted version mCompact (seq 30) because mCompact was placed before r1 in the flat slice!", val, "v2_new")
	}
}
