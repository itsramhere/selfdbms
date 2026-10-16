package lsmengine

import (
	"os"
	"testing"
)

// Helper to synchronously force an active Memtable flush into an SSTable
func forceSyncFlush(man *Manager) {
	man.Mu.Lock()
	frozen := man.Mem
	man.Frozenmem = append(man.Frozenmem, frozen)
	man.Mem = &Memtable{
		SK: &SkipList{
			headNode:     &Node{Forward: make([]*Node, 32)},
			maxLevel:     32,
			currentLevel: 1,
		},
		SizeBytes: 0,
	}
	man.Mu.Unlock()

	man.flushToDisk(frozen)
}

func TestCrossTierWriteConflict_FlushedSSTable(t *testing.T) {
	os.MkdirAll("test_data_crosstier_sst", 0755)
	defer os.RemoveAll("test_data_crosstier_sst")

	os.Chdir("test_data_crosstier_sst")
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// 1. Begin transaction T1 (ReadTs = 0)
	t1 := man.Begin()

	// 2. Begin and commit T2 (writes "conflict_sst_key" after T1 begins)
	t2 := man.Begin()
	t2.Put("conflict_sst_key", "val_t2_committed")
	if err := t2.Commit(); err != nil {
		t.Fatalf("T2 commit failed: %v", err)
	}

	// 3. Force T2's version to be written to disk in an SSTable
	forceSyncFlush(man)

	// Verify the version has genuinely moved to SSTable and is absent from memory tiers
	man.Mu.RLock()
	if len(man.ActiveSSreaders) == 0 {
		man.Mu.RUnlock()
		t.Fatalf("Expected at least 1 active SSTable reader after flush, got 0")
	}
	if len(man.Frozenmem) != 0 {
		man.Mu.RUnlock()
		t.Fatalf("Expected 0 frozen memtables after flush, got %d", len(man.Frozenmem))
	}
	_, inActiveMem := man.Mem.LatestValue([]byte("conflict_sst_key"))
	if inActiveMem == nil {
		man.Mu.RUnlock()
		t.Fatalf("Key should not be in active Memtable after flush")
	}
	man.Mu.RUnlock()

	// 4. Attempt to commit T1 modifying the same key
	t1.Put("conflict_sst_key", "val_t1_stale_overwrite")
	err := t1.Commit()

	// 5. Assert that T1 detects the conflict and aborts
	if err == nil {
		t.Fatalf("DEFECT REPRODUCED: T1 committed without detecting conflict with SSTable-resident version!")
	}
	expected := "Write-write conflict detected. Transaction aborted."
	if err.Error() != expected {
		t.Errorf("Expected %q, got %q", expected, err.Error())
	}
}

func TestCrossTierWriteConflict_FlushedTombstone(t *testing.T) {
	os.MkdirAll("test_data_crosstier_tomb", 0755)
	defer os.RemoveAll("test_data_crosstier_tomb")

	os.Chdir("test_data_crosstier_tomb")
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// Setup: initial value committed
	tSetup := man.Begin()
	tSetup.Put("tombstone_key", "initial_val")
	if err := tSetup.Commit(); err != nil {
		t.Fatalf("Setup commit failed: %v", err)
	}

	// 1. Begin T1 after initial write
	t1 := man.Begin()

	// 2. T2 deletes "tombstone_key" and commits
	t2 := man.Begin()
	t2.Delete("tombstone_key")
	if err := t2.Commit(); err != nil {
		t.Fatalf("T2 delete commit failed: %v", err)
	}

	// 3. Force tombstone to reach SSTable on disk
	forceSyncFlush(man)

	// Verify tombstone is in SSTable and absent from memory
	man.Mu.RLock()
	if len(man.ActiveSSreaders) == 0 {
		man.Mu.RUnlock()
		t.Fatalf("Expected SSTable reader after flush")
	}
	if len(man.Frozenmem) != 0 {
		man.Mu.RUnlock()
		t.Fatalf("Expected 0 frozen memtables")
	}
	man.Mu.RUnlock()

	// 4. T1 attempts to write "tombstone_key"
	t1.Put("tombstone_key", "re_written_by_t1")
	err := t1.Commit()

	// 5. T1 must detect conflict with the flushed tombstone
	if err == nil {
		t.Fatalf("DEFECT REPRODUCED: T1 committed without detecting conflict with SSTable tombstone!")
	}
	expected := "Write-write conflict detected. Transaction aborted."
	if err.Error() != expected {
		t.Errorf("Expected %q, got %q", expected, err.Error())
	}
}

func TestCrossTierWriteConflict_NonConflicting(t *testing.T) {
	os.MkdirAll("test_data_crosstier_nonconf", 0755)
	defer os.RemoveAll("test_data_crosstier_nonconf")

	os.Chdir("test_data_crosstier_nonconf")
	defer os.Chdir("..")

	man := CreateManager()
	defer man.Close()

	// 1. Begin T1
	t1 := man.Begin()

	// 2. T2 writes a different key and commits
	t2 := man.Begin()
	t2.Put("key_t2", "val_t2")
	if err := t2.Commit(); err != nil {
		t.Fatalf("T2 commit failed: %v", err)
	}

	// 3. Force T2's key to disk SSTable
	forceSyncFlush(man)

	// 4. T1 writes its own independent key and commits
	t1.Put("key_t1", "val_t1")
	if err := t1.Commit(); err != nil {
		t.Fatalf("T1 should have committed successfully without conflict, got: %v", err)
	}

	// 5. Verify both keys can be read correctly
	tRead := man.Begin()
	v1, err1 := tRead.Get("key_t1")
	if err1 != nil || v1 != "val_t1" {
		t.Errorf("Expected val_t1, got %q, err %v", v1, err1)
	}
	v2, err2 := tRead.Get("key_t2")
	if err2 != nil || v2 != "val_t2" {
		t.Errorf("Expected val_t2, got %q, err %v", v2, err2)
	}
}
