package lsmengine

import (
	"bytes"
	"errors"
	"math"
	"os"
	"testing"
)

func newTestMemtable() *Memtable {
	return &Memtable{
		SK: &SkipList{
			headNode:     &Node{Forward: make([]*Node, 32)},
			maxLevel:     32,
			currentLevel: 1,
		},
		SizeBytes: 0,
	}
}

// Helper to create a single-key SSTable for testing
func createTestSSTable(t *testing.T, filename string, records []struct {
	key      string
	seq      uint64
	val      string
	isDelete bool
}) *SSTableReader {
	file, err := os.Create(filename)
	if err != nil {
		t.Fatalf("failed to create sstable file %s: %v", filename, err)
	}

	builder := &SSTableBuilder{
		File:        file,
		BlockBuffer: new(bytes.Buffer),
	}

	for _, rec := range records {
		internalKey := EncodeInternalKey([]byte(rec.key), rec.seq, rec.isDelete)
		if err := builder.Add(internalKey, []byte(rec.val)); err != nil {
			t.Fatalf("failed to add record to builder: %v", err)
		}
	}

	if err := builder.Close(); err != nil {
		t.Fatalf("failed to close builder: %v", err)
	}

	reader, err := NewSSTableReader(filename)
	if err != nil {
		t.Fatalf("failed to open reader: %v", err)
	}

	return reader
}

// 1. Verify behavior of math.MaxUint64 with actual internal-key encoding and sequence range
func TestI1_2_0_MaxUint64EncodingBehavior(t *testing.T) {
	userKey := []byte("test_key")
	seekKey := EncodeInternalKey(userKey, math.MaxUint64, true)

	// Verify decoding seekKey
	decodedUser, decodedSeq, isDel := DecodeInternalKey(seekKey)
	if !bytes.Equal(decodedUser, userKey) {
		t.Errorf("expected userKey %s, got %s", userKey, decodedUser)
	}
	if !isDel {
		t.Errorf("expected isDelete=true")
	}

	// Note that math.MaxUint64 << 8 truncates the top 8 bits:
	// math.MaxUint64 = 0xFFFFFFFFFFFFFFFF
	// packed = (math.MaxUint64 << 8) | 1 = 0xFFFFFFFFFFFFFF01
	// decodedSeq = packed >> 8 = 0x00FFFFFFFFFFFFFF = (1 << 56) - 1 = 72,057,594,037,927,935
	max56Bit := uint64(0x00FFFFFFFFFFFFFF)
	if decodedSeq != max56Bit {
		t.Errorf("expected decodedSeq %d, got %d", max56Bit, decodedSeq)
	}

	// Test comparison with any lower sequence number
	testSeqs := []uint64{0, 1, 100, 1000000, max56Bit - 1, max56Bit}
	for _, seq := range testSeqs {
		kPut := EncodeInternalKey(userKey, seq, false)
		kTomb := EncodeInternalKey(userKey, seq, true)

		// seekKey should sort <= every valid internal key for userKey
		if CompareInternalKeys(seekKey, kPut) > 0 {
			t.Errorf("seekKey should sort before put key at seq %d", seq)
		}
		if seq < max56Bit && CompareInternalKeys(seekKey, kTomb) >= 0 {
			t.Errorf("seekKey should sort before tombstone key at seq %d", seq)
		}
		if seq == max56Bit && CompareInternalKeys(seekKey, kTomb) != 0 {
			t.Errorf("seekKey should be identical to tombstone key at max56Bit")
		}
	}
}

// 2. Test multiple SSTables containing different versions of the same key
func TestI1_2_0_MultipleSSTablesDifferentVersions(t *testing.T) {
	dir := "test_data_i120_multiversion"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	sst1 := createTestSSTable(t, dir+"/sst1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"mykey", 10, "v1", false},
	})
	defer sst1.Close()

	sst2 := createTestSSTable(t, dir+"/sst2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"mykey", 20, "v2", false},
	})
	defer sst2.Close()

	// Normal chronological ordering: sst1 (older), sst2 (newer)
	man := &Manager{
		Mem:             newTestMemtable(),
		Frozenmem:       nil,
		ActiveSSreaders: []*SSTableReader{sst1, sst2},
	}

	seq, err := man.latestValueLocked([]byte("mykey"))
	if err != nil || seq != 20 {
		t.Fatalf("expected seq 20, got seq %d, err %v", seq, err)
	}
}

// 3. Test tombstones and subsequent puts across different SSTables
func TestI1_2_0_TombstoneAndSubsequentPut(t *testing.T) {
	dir := "test_data_i120_tombput"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// SST 1 has initial put at seq 10
	sst1 := createTestSSTable(t, dir+"/sst1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "val1", false},
	})
	defer sst1.Close()

	// SST 2 has delete tombstone at seq 20
	sst2 := createTestSSTable(t, dir+"/sst2.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "", true},
	})
	defer sst2.Close()

	// SST 3 has subsequent put at seq 30
	sst3 := createTestSSTable(t, dir+"/sst3.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 30, "val2", false},
	})
	defer sst3.Close()

	man := &Manager{
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{sst1, sst2, sst3},
	}

	seq, err := man.latestValueLocked([]byte("k1"))
	if err != nil || seq != 30 {
		t.Fatalf("expected latest seq 30, got %d, err %v", seq, err)
	}

	// Now test manager where SST3 is not yet flushed (sst1 and sst2 only)
	man2 := &Manager{
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{sst1, sst2},
	}
	seq2, err2 := man2.latestValueLocked([]byte("k1"))
	if err2 != nil || seq2 != 20 {
		t.Fatalf("expected tombstone seq 20, got %d, err %v", seq2, err2)
	}
}

// 4. Test a key with enough versions to span multiple 4 KB blocks
func TestI1_2_0_KeySpanningMultipleBlocks(t *testing.T) {
	dir := "test_data_i120_multiblock"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	file, err := os.Create(dir + "/multiblock.sst")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	builder := &SSTableBuilder{
		File:        file,
		BlockBuffer: new(bytes.Buffer),
	}

	// We create 50 versions of "multi_block_key", each with 200 bytes of value.
	// 50 * (200 + ~30 bytes) = ~11.5 KB, which will span at least 3 blocks (4KB each).
	userKey := []byte("multi_block_key")
	payload := make([]byte, 200)
	for i := range payload {
		payload[i] = 'X'
	}

	// Note: In an SSTable, keys are added in ascending internal key order.
	// Inverted sequence numbers sort descending, so seq 50 has smaller inverted key than seq 1.
	// Therefore, seq 50 MUST be added FIRST to preserve ascending internal key order!
	for seq := uint64(50); seq >= 1; seq-- {
		k := EncodeInternalKey(userKey, seq, false)
		if err := builder.Add(k, payload); err != nil {
			t.Fatalf("builder.Add failed: %v", err)
		}
	}

	if err := builder.Close(); err != nil {
		t.Fatalf("builder.Close failed: %v", err)
	}

	reader, err := NewSSTableReader(dir + "/multiblock.sst")
	if err != nil {
		t.Fatalf("NewSSTableReader failed: %v", err)
	}
	defer reader.Close()

	if len(reader.Index) < 2 {
		t.Fatalf("Expected at least 2 index blocks to test multi-block boundary, got %d", len(reader.Index))
	}
	t.Logf("SSTable successfully spanned %d index blocks", len(reader.Index))

	// Verify LatestValue retrieves the greatest sequence number (50)
	latestSeq, err := reader.LatestValue(userKey)
	if err != nil {
		t.Fatalf("LatestValue failed: %v", err)
	}
	if latestSeq != 50 {
		t.Fatalf("Expected latestSeq 50, got %d", latestSeq)
	}
}

// 5. Test distinguishing missing key from I/O errors
func TestI1_2_0_MissingKeyVsIOError(t *testing.T) {
	dir := "test_data_i120_ioerror"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	sst := createTestSSTable(t, dir+"/test.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"present_key", 10, "val", false},
	})

	// Missing key returns ErrNotFound
	_, err := sst.LatestValue([]byte("absent_key"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for absent key, got: %v", err)
	}

	// Now close the file descriptor under the reader to induce an I/O error on read
	sst.File.Close()

	_, ioErr := sst.LatestValue([]byte("present_key"))
	if ioErr == nil {
		t.Fatalf("expected I/O error on closed file, got nil")
	}
	if errors.Is(ioErr, ErrNotFound) {
		t.Fatalf("I/O error was falsely reported as ErrNotFound!")
	}
	t.Logf("I/O error correctly distinguished: %v", ioErr)
}

// 6. Test SSTable ordering change behavior in conflict detection
func TestI1_2_0_SSTableOrderingAnomaly(t *testing.T) {
	dir := "test_data_i120_ordering"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// SST A has seq 50
	sstA := createTestSSTable(t, dir+"/sstA.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k", 50, "valA", false},
	})
	defer sstA.Close()

	// SST B has seq 10
	sstB := createTestSSTable(t, dir+"/sstB.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k", 10, "valB", false},
	})
	defer sstB.Close()

	// Case 1: Chronological [sstB (old), sstA (new)]
	man1 := &Manager{
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{sstB, sstA},
	}
	seq1, err1 := man1.latestValueLocked([]byte("k"))
	if err1 != nil || seq1 != 50 {
		t.Errorf("Chronological order failed: expected 50, got %d, %v", seq1, err1)
	}

	// Case 2: Out of order [sstA (new), sstB (old)]
	// If latestValueLocked returns on the first match, it returns 10 instead of 50.
	man2 := &Manager{
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{sstA, sstB},
	}
	seq2, err2 := man2.latestValueLocked([]byte("k"))
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if seq2 != 50 {
		t.Fatalf("FAIL: conflict detector returned seq %d instead of greatest committed sequence 50 when SSTable ordering changed", seq2)
	}
}
