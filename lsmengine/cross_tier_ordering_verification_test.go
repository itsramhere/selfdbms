package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// ReferenceOracle maintains the ground-truth history of all committed operations
// to verify that LSM storage tier lookups match snapshot isolation semantics.
type ReferenceOracle struct {
	mu      sync.Mutex
	history map[string][]oracleVersion
}

type oracleVersion struct {
	seqNo    uint64
	val      string
	isDelete bool
}

func newReferenceOracle() *ReferenceOracle {
	return &ReferenceOracle{
		history: make(map[string][]oracleVersion),
	}
}

func (o *ReferenceOracle) record(key string, seqNo uint64, val string, isDelete bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.history[key] = append(o.history[key], oracleVersion{
		seqNo:    seqNo,
		val:      val,
		isDelete: isDelete,
	})
	// Sort descending by seqNo so newest is first
	sort.Slice(o.history[key], func(i, j int) bool {
		return o.history[key][i].seqNo > o.history[key][j].seqNo
	})
}

func (o *ReferenceOracle) get(key string, readTs uint64) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	versions, ok := o.history[key]
	if !ok || len(versions) == 0 {
		return "", ErrNotFound
	}
	for _, v := range versions {
		if v.seqNo <= readTs {
			if v.isDelete {
				return "", ErrKeyDeleted
			}
			return v.val, nil
		}
	}
	return "", ErrNotFound
}

// TestCrossTierOrdering_ReferenceModelOracle validates point read correctness
// across Active Memtable, multiple Frozen Memtables, and SSTables against an independent
// snapshot reference model for every sequence number.
func TestCrossTierOrdering_ReferenceModelOracle(t *testing.T) {
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
	defer man.Close()

	keys := []string{"apple", "banana", "cherry", "date"}

	// Generation 1: Initial writes (will end up on disk in SSTable)
	for i, k := range keys {
		tx := man.Begin()
		val := fmt.Sprintf("%s_gen1", k)
		tx.Put(k, val)
		if err := tx.Commit(); err != nil {
			t.Fatalf("gen1 commit failed: %v", err)
		}
		oracle.record(k, tx.AllocatedSeqNo, val, false)
		_ = i
	}

	// Trigger flush for Generation 1
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush gen1 failed: %v", err)
	}

	// Wait for Gen 1 to become SSTable
	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Gen 1 SSTable")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Block flush worker for subsequent generations so multiple frozen Memtables accumulate
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

	// Generation 2: Updates and deletes (will be in Frozenmem[0])
	// Update apple, delete banana
	txA2 := man.Begin()
	txA2.Put("apple", "apple_gen2")
	_ = txA2.Commit()
	oracle.record("apple", txA2.AllocatedSeqNo, "apple_gen2", false)

	txB2 := man.Begin()
	txB2.Delete("banana")
	_ = txB2.Commit()
	oracle.record("banana", txB2.AllocatedSeqNo, "", true)

	// Trigger flush for Gen 2 (now in Frozenmem)
	_ = man.TriggerFlush()

	// Generation 3: Updates and reinsertion (will be in Frozenmem[1])
	// Reinsert banana, update cherry
	txB3 := man.Begin()
	txB3.Put("banana", "banana_gen3_resurrected")
	_ = txB3.Commit()
	oracle.record("banana", txB3.AllocatedSeqNo, "banana_gen3_resurrected", false)

	txC3 := man.Begin()
	txC3.Put("cherry", "cherry_gen3")
	_ = txC3.Commit()
	oracle.record("cherry", txC3.AllocatedSeqNo, "cherry_gen3", false)

	// Trigger flush for Gen 3 (now in Frozenmem)
	_ = man.TriggerFlush()

	// Generation 4: Current active Memtable
	// Delete cherry, update date
	txC4 := man.Begin()
	txC4.Delete("cherry")
	_ = txC4.Commit()
	oracle.record("cherry", txC4.AllocatedSeqNo, "", true)

	txD4 := man.Begin()
	txD4.Put("date", "date_gen4")
	_ = txD4.Commit()
	oracle.record("date", txD4.AllocatedSeqNo, "date_gen4", false)

	maxSeq := man.CurrentSeqNo.Load()

	// Verify every key at every possible timestamp from 0 to maxSeq+2 against the reference oracle
	for _, k := range keys {
		for ts := uint64(0); ts <= maxSeq+2; ts++ {
			expectedVal, expectedErr := oracle.get(k, ts)
			actualVal, actualErr := man.Get(k, ts)

			if expectedErr != nil {
				if !errors.Is(actualErr, expectedErr) {
					t.Fatalf("key=%s ts=%d: expected error %v, got actualVal=%q, actualErr=%v",
						k, ts, expectedErr, actualVal, actualErr)
				}
			} else {
				if actualErr != nil {
					t.Fatalf("key=%s ts=%d: expected val=%q, got error: %v",
						k, ts, expectedVal, actualErr)
				}
				if actualVal != expectedVal {
					t.Fatalf("key=%s ts=%d: expected val=%q, got actualVal=%q",
						k, ts, expectedVal, actualVal)
				}
			}
		}
	}

	// Now unblock worker and let all frozen Memtables drain to SSTables
	unblock()

	deadline2 := time.Now().Add(3 * time.Second)
	for {
		man.Mu.RLock()
		fCount := len(man.Frozenmem)
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if fCount == 0 && rCount == 3 {
			break
		}
		if time.Now().After(deadline2) {
			t.Fatalf("timed out waiting for all frozen memtables to flush: frozen=%d, readers=%d", fCount, rCount)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Re-verify against oracle after all flushes are written to disk SSTables
	for _, k := range keys {
		for ts := uint64(0); ts <= maxSeq+2; ts++ {
			expectedVal, expectedErr := oracle.get(k, ts)
			actualVal, actualErr := man.Get(k, ts)

			if expectedErr != nil {
				if !errors.Is(actualErr, expectedErr) {
					t.Fatalf("post-flush key=%s ts=%d: expected error %v, got actualVal=%q, actualErr=%v",
						k, ts, expectedErr, actualVal, actualErr)
				}
			} else {
				if actualErr != nil {
					t.Fatalf("post-flush key=%s ts=%d: expected val=%q, got error: %v",
						k, ts, expectedVal, actualErr)
				}
				if actualVal != expectedVal {
					t.Fatalf("post-flush key=%s ts=%d: expected val=%q, got actualVal=%q",
						k, ts, expectedVal, actualVal)
				}
			}
		}
	}

	t.Logf("CrossTierOrdering PASS: Successfully validated %d points against reference oracle across all tiers",
		len(keys)*int(maxSeq+3)*2)
}

// TestCrossTierOrdering_OlderAndNewerValuesNoFirstTierBias verifies that if an active
// Memtable contains an update with seq > readTs, the reader does not stop and return NotFound,
// but correctly falls through to frozen and disk tiers to find the visible older version.
func TestCrossTierOrdering_OlderAndNewerValuesNoFirstTierBias(t *testing.T) {
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

	// Step 1: Write version 1 at seq 10, flush to SSTable
	f1 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f1.Put(EncodeInternalKey([]byte("bias_key"), 10, false), []byte("v1_disk"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f1)
	man.Mu.Unlock()
	man.EnqueueFlush(f1)

	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for SSTable flush")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Step 2: Write version 2 at seq 20 into a frozen Memtable (held from flushing)
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
	f2.Put(EncodeInternalKey([]byte("bias_key"), 20, false), []byte("v2_frozen"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f2)
	man.Mu.Unlock()
	man.EnqueueFlush(f2)

	// Step 3: Write version 3 at seq 30 into active Memtable
	man.Mem.Put(EncodeInternalKey([]byte("bias_key"), 30, false), []byte("v3_active"))

	// Query at readTs = 15:
	// - Active tier has seq 30 (> 15). Must NOT return NotFound!
	// - Frozen tier has seq 20 (> 15). Must NOT return NotFound!
	// - SSTable tier has seq 10 (<= 15). Must return "v1_disk"!
	v15, err := man.Get("bias_key", 15)
	if err != nil || v15 != "v1_disk" {
		t.Fatalf("readTs 15: expected 'v1_disk', got val=%q, err=%v", v15, err)
	}

	// Query at readTs = 25:
	// - Active tier has seq 30 (> 25).
	// - Frozen tier has seq 20 (<= 25). Must return "v2_frozen"!
	v25, err := man.Get("bias_key", 25)
	if err != nil || v25 != "v2_frozen" {
		t.Fatalf("readTs 25: expected 'v2_frozen', got val=%q, err=%v", v25, err)
	}

	// Query at readTs = 35:
	// - Active tier has seq 30 (<= 35). Must return "v3_active"!
	v35, err := man.Get("bias_key", 35)
	if err != nil || v35 != "v3_active" {
		t.Fatalf("readTs 35: expected 'v3_active', got val=%q, err=%v", v35, err)
	}

	unblock()
	t.Log("OlderAndNewerValues PASS: Zero tier-bias confirmed; reads correctly traverse tiers for older versions")
}

// TestCrossTierOrdering_ReinsertionAfterTombstoneAcrossTiers verifies complex
// deletion and reinsertion patterns across all three tiers.
func TestCrossTierOrdering_ReinsertionAfterTombstoneAcrossTiers(t *testing.T) {
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

	// SSTable: key1=v10 (seq 10), key2=v10 (seq 10)
	f1 := &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}}
	f1.Put(EncodeInternalKey([]byte("reinsert_key1"), 10, false), []byte("v10"))
	f1.Put(EncodeInternalKey([]byte("reinsert_key2"), 10, false), []byte("v10"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f1)
	man.Mu.Unlock()
	man.EnqueueFlush(f1)

	deadline := time.Now().Add(2 * time.Second)
	for {
		man.Mu.RLock()
		rCount := len(man.ActiveSSreaders)
		man.Mu.RUnlock()
		if rCount == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for SSTable flush")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Frozen: key1=deleted (seq 20), key2=v20 (seq 20)
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
	f2.Put(EncodeInternalKey([]byte("reinsert_key1"), 20, true), []byte{}) // tombstone
	f2.Put(EncodeInternalKey([]byte("reinsert_key2"), 20, false), []byte("v20"))
	man.Mu.Lock()
	man.Frozenmem = append(man.Frozenmem, f2)
	man.Mu.Unlock()
	man.EnqueueFlush(f2)

	// Active: key1=reinserted_v30 (seq 30), key2=deleted (seq 30)
	man.Mem.Put(EncodeInternalKey([]byte("reinsert_key1"), 30, false), []byte("reinserted_v30"))
	man.Mem.Put(EncodeInternalKey([]byte("reinsert_key2"), 30, true), []byte{}) // tombstone

	// Key 1 checks:
	// ts 15: v10
	v1_15, err := man.Get("reinsert_key1", 15)
	if err != nil || v1_15 != "v10" {
		t.Fatalf("key1 ts 15: expected 'v10', got val=%q, err=%v", v1_15, err)
	}
	// ts 25: ErrKeyDeleted
	_, err = man.Get("reinsert_key1", 25)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("key1 ts 25: expected ErrKeyDeleted, got: %v", err)
	}
	// ts 35: reinserted_v30
	v1_35, err := man.Get("reinsert_key1", 35)
	if err != nil || v1_35 != "reinserted_v30" {
		t.Fatalf("key1 ts 35: expected 'reinserted_v30', got val=%q, err=%v", v1_35, err)
	}

	// Key 2 checks:
	// ts 15: v10
	v2_15, err := man.Get("reinsert_key2", 15)
	if err != nil || v2_15 != "v10" {
		t.Fatalf("key2 ts 15: expected 'v10', got val=%q, err=%v", v2_15, err)
	}
	// ts 25: v20
	v2_25, err := man.Get("reinsert_key2", 25)
	if err != nil || v2_25 != "v20" {
		t.Fatalf("key2 ts 25: expected 'v20', got val=%q, err=%v", v2_25, err)
	}
	// ts 35: ErrKeyDeleted
	_, err = man.Get("reinsert_key2", 35)
	if !errors.Is(err, ErrKeyDeleted) {
		t.Fatalf("key2 ts 35: expected ErrKeyDeleted, got: %v", err)
	}

	unblock()
	t.Log("ReinsertionAfterTombstone PASS: Verified across SSTable, Frozen, and Active tiers")
}
