package lsmengine

import (
	"fmt"
	"os"
	"testing"
)

func TestSkipListLevelDistribution(t *testing.T) {
	sk := &SkipList{
		headNode:     &Node{Forward: make([]*Node, 32)},
		maxLevel:     32,
		currentLevel: 1,
	}

	levelCounts := make(map[int]int)
	iterations := 10000

	for i := 0; i < iterations; i++ {
		lvl := sk.randomLevel()
		levelCounts[lvl]++
	}

	t.Logf("Level distribution over %d iterations: %v", iterations, levelCounts)

	// Level 1 should be roughly ~50% of total (p=0.5 geometric distribution)
	// Level 2 should be roughly ~25%, Level 3 ~12.5%, etc.
	if levelCounts[1] < iterations/3 || levelCounts[1] > (iterations*2)/3 {
		t.Errorf("Expected Level 1 count to be ~50%% of %d, got %d", iterations, levelCounts[1])
	}

	if levelCounts[2] >= levelCounts[1] {
		t.Errorf("Level 2 count (%d) should be significantly lower than Level 1 (%d)", levelCounts[2], levelCounts[1])
	}
}

func TestBasicPutGet(t *testing.T) {
	os.MkdirAll("test_data_basic", 0755)
	defer os.RemoveAll("test_data_basic")

	os.Chdir("test_data_basic")
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	txn := man.Begin()
	txn.Put("key1", "value1")
	txn.Put("key2", "value2")
	if err := txn.Commit(); err != nil {
		t.Fatalf("Failed to commit txn: %v", err)
	}

	txnRead := man.Begin()
	val1, err := txnRead.Get("key1")
	if err != nil || val1 != "value1" {
		t.Errorf("Expected value1, got %q, err %v", val1, err)
	}

	val2, err := txnRead.Get("key2")
	if err != nil || val2 != "value2" {
		t.Errorf("Expected value2, got %q, err %v", val2, err)
	}
}

func TestDeleteAndTombstone(t *testing.T) {
	os.MkdirAll("test_data_delete", 0755)
	defer os.RemoveAll("test_data_delete")

	os.Chdir("test_data_delete")
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// Put key
	txn1 := man.Begin()
	txn1.Put("k1", "v1")
	if err := txn1.Commit(); err != nil {
		t.Fatalf("Failed commit txn1: %v", err)
	}

	// Delete key
	txn2 := man.Begin()
	txn2.Delete("k1")
	if err := txn2.Commit(); err != nil {
		t.Fatalf("Failed commit txn2: %v", err)
	}

	// Get key should return error / deleted
	txn3 := man.Begin()
	_, err := txn3.Get("k1")
	if err == nil {
		t.Errorf("Expected error reading deleted key k1, got nil")
	}
}

func TestWALRecovery(t *testing.T) {
	os.MkdirAll("test_data_wal", 0755)
	defer os.RemoveAll("test_data_wal")

	os.Chdir("test_data_wal")
	defer os.Chdir("..")

	man := CreateManager()
	txn := man.Begin()
	for i := 0; i < 50; i++ {
		txn.Put(fmt.Sprintf("wal_key_%d", i), fmt.Sprintf("wal_val_%d", i))
	}
	if err := txn.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	man.Close()

	// Recover into a fresh manager
	man2 := &Manager{
		Mu:  man.Mu,
		Mem: &Memtable{SK: &SkipList{headNode: &Node{Forward: make([]*Node, 32)}, maxLevel: 32, currentLevel: 1}},
	}
	if err := man2.RecoverFromWAL("wal.log"); err != nil {
		t.Fatalf("Failed to recover from WAL: %v", err)
	}

	txn2 := man2.Begin()
	val, err := txn2.Get("wal_key_25")
	if err != nil || val != "wal_val_25" {
		t.Errorf("Expected wal_val_25 after recovery, got %q, err %v", val, err)
	}
}

func TestWriteWriteConflict(t *testing.T) {
	os.MkdirAll("test_data_conflict", 0755)
	defer os.RemoveAll("test_data_conflict")

	os.Chdir("test_data_conflict")
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// txn1 starts before any writes (ReadTs = 0)
	txn1 := man.Begin()

	// txn2 starts and commits a write to key "conflict_key"
	txn2 := man.Begin()
	txn2.Put("conflict_key", "val_from_txn2")
	if err := txn2.Commit(); err != nil {
		t.Fatalf("txn2 commit failed: %v", err)
	}

	// txn1 tries to overwrite "conflict_key", whose latest sequence number is now > txn1.ReadTs
	txn1.Put("conflict_key", "val_from_txn1")
	err := txn1.Commit()
	if err == nil {
		t.Fatalf("Expected write-write conflict error for txn1, but got nil")
	}
	expectedErr := "Write-write conflict detected. Transaction aborted."
	if err.Error() != expectedErr {
		t.Errorf("Expected %q, got %q", expectedErr, err.Error())
	}

	// Verify reading "conflict_key" still returns txn2's value
	txnRead := man.Begin()
	v, err := txnRead.Get("conflict_key")
	if err != nil || v != "val_from_txn2" {
		t.Errorf("Expected 'val_from_txn2', got %q, err %v", v, err)
	}
}

