package lsmengine

import (
	"os"
	"testing"
)

// pkCommitPut commits a single Put and returns the allocated sequence number.
func pkCommitPut(t *testing.T, man *Manager, key, val string) uint64 {
	t.Helper()
	tx := man.Begin()
	tx.Put(key, val)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit put %q=%q failed: %v", key, val, err)
	}
	return man.CurrentSeqNo.Load()
}

// pkCommitDelete commits a single Delete and returns the allocated sequence number.
func pkCommitDelete(t *testing.T, man *Manager, key string) uint64 {
	t.Helper()
	tx := man.Begin()
	tx.Delete(key)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit delete %q failed: %v", key, err)
	}
	return man.CurrentSeqNo.Load()
}

// pkNewEngine creates a fresh engine of the requested mode with the process working
// directory confined to an isolated temp dir (SSTable and WAL files land there).
func pkNewEngine(t *testing.T, mode SyncMode) *Manager {
	t.Helper()
	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd failed: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir to temp dir failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	return createEngineWithMode(t, dir, mode)
}

// pkVerifiedSingleReader returns the sole SSTableReader of the engine and asserts
// that exactly one SSTable currently exists (so table-global properties are meaningful).
func pkVerifiedSingleReader(t *testing.T, man *Manager) *SSTableReader {
	t.Helper()
	if man.syncMode == SyncModeApproachB {
		man.snapshotMu.Lock()
		defer man.snapshotMu.Unlock()
		if man.currentSnapshot == nil {
			t.Fatal("expected a current published snapshot")
		}
		if len(man.currentSnapshot.readers) != 1 {
			t.Fatalf("expected exactly one SSTable in snapshot, got %d", len(man.currentSnapshot.readers))
		}
		sr := man.currentSnapshot.readers[0]
		if sr == nil || sr.Reader == nil {
			t.Fatal("corrupted snapshot: nil reader")
		}
		return sr.Reader
	}
	man.Mu.RLock()
	defer man.Mu.RUnlock()
	if len(man.ActiveSSreaders) != 1 {
		t.Fatalf("expected exactly one active SSTable, got %d", len(man.ActiveSSreaders))
	}
	return man.ActiveSSreaders[0]
}

// TestPerKeyConflict_TableGlobalTimestampCounterexample implements the exact
// CP3a-V.1 counterexample requested by the external reviewer:
//
//  1. Commit an older version of key alpha (seqA).
//  2. Begin transaction T at read timestamp R (R >= seqA).
//  3. Commit a newer version of unrelated key beta (seqB > R).
//  4. Force both keys into ONE SSTable, so the table-global maximum timestamp
//     (seqB) exceeds R while alpha's per-key latest committed version (seqA)
//     does not.
//  5. Commit T's update to alpha.
//  6. Verify that Approach A permits this nonconflicting update and Approach B
//     does the same (identical semantics).
//
// A table-global MaxTimestamp() lookup would report seqB for alpha and
// spuriously abort the commit; the per-key LatestValue(userKey) mechanism
// reports seqA <= R and must permit it.
func TestPerKeyConflict_TableGlobalTimestampCounterexample(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man := pkNewEngine(t, mode)
			defer man.Close()

			// 1. Older committed version of alpha.
			alphaSeq := pkCommitPut(t, man, "alpha", "alpha_v1")

			// 2. Transaction T begins at read timestamp R.
			tx := man.Begin()
			readTs := tx.ReadTs

			// 3. Newer committed version of unrelated key beta.
			betaSeq := pkCommitPut(t, man, "beta", "beta_v2")
			if betaSeq <= readTs {
				t.Fatalf("counterexample setup invalid: beta seq %d must exceed R=%d", betaSeq, readTs)
			}

			// 4. Both keys must reside in one SSTable.
			forceSyncFlush(man)
			reader := pkVerifiedSingleReader(t, man)

			gotAlphaSeq, err := reader.LatestValue([]byte("alpha"))
			if err != nil || gotAlphaSeq != alphaSeq {
				t.Fatalf("single-SSTable per-key LatestValue(alpha) = (%d, %v), want (%d, nil)", gotAlphaSeq, err, alphaSeq)
			}
			gotBetaSeq, err := reader.LatestValue([]byte("beta"))
			if err != nil || gotBetaSeq != betaSeq {
				t.Fatalf("single-SSTable per-key LatestValue(beta) = (%d, %v), want (%d, nil)", gotBetaSeq, err, betaSeq)
			}

			// Counterexample precondition: table-global max > R, per-key alpha <= R.
			if !(gotBetaSeq > readTs && gotAlphaSeq <= readTs) {
				t.Fatalf("counterexample precondition failed: table-global max=%d, alpha latest=%d, R=%d", gotBetaSeq, gotAlphaSeq, readTs)
			}

			// 5-6. T updates alpha: the per-key latest committed version (seqA <= R)
			// must be used, so the update is nonconflicting and must commit.
			tx.Put("alpha", "alpha_v3_by_tx")
			if err := tx.Commit(); err != nil {
				t.Fatalf("nonconflicting update aborted: per-key alpha latest=%d <= R=%d must not conflict (table-global max=%d would wrongfully conflict); err=%v",
					gotAlphaSeq, readTs, gotBetaSeq, err)
			}

			// Both keys readable with their latest committed values.
			ts := man.CurrentSeqNo.Load()
			if v, err := man.Get("alpha", ts); err != nil || v != "alpha_v3_by_tx" {
				t.Fatalf("Get(alpha) = %q, err=%v; want alpha_v3_by_tx", v, err)
			}
			if v, err := man.Get("beta", ts); err != nil || v != "beta_v2" {
				t.Fatalf("Get(beta) = %q, err=%v; want beta_v2", v, err)
			}
		})
	}
}

// TestPerKeyConflict_ApproachB_SameKeyStillAborts verifies that a genuine same-key
// conflict against a single SSTable-resident version still aborts under Approach B.
func TestPerKeyConflict_ApproachB_SameKeyStillAborts(t *testing.T) {
	man := pkNewEngine(t, SyncModeApproachB)
	defer man.Close()

	pkCommitPut(t, man, "alpha", "v1")
	tx := man.Begin()
	seq2 := pkCommitPut(t, man, "alpha", "v2")
	if seq2 <= tx.ReadTs {
		t.Fatalf("setup invalid: newer alpha seq %d must exceed R=%d", seq2, tx.ReadTs)
	}
	forceSyncFlush(man)
	pkVerifiedSingleReader(t, man)

	tx.Put("alpha", "v3_must_abort")
	err := tx.Commit()
	if err == nil {
		t.Fatal("DEFECT: same-key conflict against SSTable-resident newer version was not detected")
	}
	if err.Error() != "Write-write conflict detected. Transaction aborted." {
		t.Fatalf("unexpected conflict error: %v", err)
	}
}

// TestPerKeyConflict_ApproachB_TombstoneCountsAsVersion verifies that a flushed
// tombstone counts as a committed key version for per-key conflict detection.
func TestPerKeyConflict_ApproachB_TombstoneCountsAsVersion(t *testing.T) {
	man := pkNewEngine(t, SyncModeApproachB)
	defer man.Close()

	pkCommitPut(t, man, "alpha", "v1")
	tx := man.Begin()
	tombSeq := pkCommitDelete(t, man, "alpha")
	if tombSeq <= tx.ReadTs {
		t.Fatalf("setup invalid: tombstone seq %d must exceed R=%d", tombSeq, tx.ReadTs)
	}
	forceSyncFlush(man)

	reader := pkVerifiedSingleReader(t, man)
	latest, err := reader.LatestValue([]byte("alpha"))
	if err != nil {
		t.Fatalf("LatestValue(alpha) on flushed tombstone failed: %v", err)
	}
	if latest != tombSeq {
		t.Fatalf("LatestValue(alpha) = %d, want tombstone sequence %d (tombstones must count as committed versions)", latest, tombSeq)
	}

	tx.Put("alpha", "resurrect_must_abort")
	err = tx.Commit()
	if err == nil {
		t.Fatal("DEFECT: flushed tombstone did not count as a committed version during conflict validation")
	}
	if err.Error() != "Write-write conflict detected. Transaction aborted." {
		t.Fatalf("unexpected conflict error: %v", err)
	}
}

// TestPerKeyConflict_ApproachB_MultiSSTableOrdering verifies that Approach B
// validation tracks the per-key maximum committed sequence across ALL SSTables
// (not a single table's global maximum, and not merely the newest table).
func TestPerKeyConflict_ApproachB_MultiSSTableOrdering(t *testing.T) {
	t.Run("newest_alpha_in_newest_table_conflicts", func(t *testing.T) {
		man := pkNewEngine(t, SyncModeApproachB)
		defer man.Close()

		pkCommitPut(t, man, "alpha", "v1") // table 1 {alpha:1}
		forceSyncFlush(man)

		tx := man.Begin() // R = 1
		pkCommitPut(t, man, "beta", "v2") // table 2 {beta:2}
		forceSyncFlush(man)
		pkCommitPut(t, man, "alpha", "v3") // table 3 {alpha:3}
		forceSyncFlush(man)

		tx.Put("alpha", "v4_must_abort")
		err := tx.Commit()
		if err == nil {
			t.Fatal("DEFECT: alpha conflict (max per-key seq across tables = 3 > R = 1) not detected")
		}
		if err.Error() != "Write-write conflict detected. Transaction aborted." {
			t.Fatalf("unexpected conflict error: %v", err)
		}
	})

	t.Run("newer_unrelated_key_in_newer_table_does_not_conflict", func(t *testing.T) {
		man := pkNewEngine(t, SyncModeApproachB)
		defer man.Close()

		pkCommitPut(t, man, "alpha", "v1") // table 1 {alpha:1}
		forceSyncFlush(man)

		tx := man.Begin() // R = 1
		pkCommitPut(t, man, "beta", "v2") // table 2 {beta:2} (newer table, unrelated key)
		forceSyncFlush(man)

		tx.Put("alpha", "v3_must_commit")
		if err := tx.Commit(); err != nil {
			t.Fatalf("nonconflicting update aborted: alpha's per-key latest across all tables is 1 <= R=1; err=%v", err)
		}
	})
}

// TestPerKeyConflict_ApproachB_CompactionBoundary verifies per-key conflict
// semantics across compaction: a compacted single table retaining multiple keys
// must not apply one key's timestamp to another, and newer versions published
// after compaction must still conflict.
func TestPerKeyConflict_ApproachB_CompactionBoundary(t *testing.T) {
	man := pkNewEngine(t, SyncModeApproachB)
	defer man.Close()

	// Table 1: {alpha:1}. Then T begins at R = 1.
	pkCommitPut(t, man, "alpha", "v1")
	forceSyncFlush(man)
	tx := man.Begin()

	// Table 2: {beta:2}. Compact [table1, table2] -> merged {alpha:1, beta:2}.
	pkCommitPut(t, man, "beta", "v2")
	forceSyncFlush(man)
	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	reader := pkVerifiedSingleReader(t, man)
	alphaSeq, err := reader.LatestValue([]byte("alpha"))
	if err != nil || alphaSeq != 1 {
		t.Fatalf("post-compaction LatestValue(alpha) = (%d, %v), want (1, nil)", alphaSeq, err)
	}
	betaSeq, err := reader.LatestValue([]byte("beta"))
	if err != nil || betaSeq != 2 {
		t.Fatalf("post-compaction LatestValue(beta) = (%d, %v), want (2, nil)", betaSeq, err)
	}

	// T (R=1) updates alpha against the merged table whose global max is 2 > R.
	tx.Put("alpha", "v3_after_compaction")
	if err := tx.Commit(); err != nil {
		t.Fatalf("nonconflicting update aborted after compaction: alpha latest=1 <= R=%d, merged table max=%d; err=%v", tx.ReadTs, betaSeq, err)
	}

	// Publish newer alpha versions across post-compaction flushes, then verify
	// a stale transaction still aborts.
	forceSyncFlush(man)                    // {alpha:3}
	tx2 := man.Begin()                     // R2 = 3
	pkCommitPut(t, man, "alpha", "v4")     // seq 4
	forceSyncFlush(man)                    // {alpha:4}
	tx2.Put("alpha", "v5_must_abort")
	err = tx2.Commit()
	if err == nil {
		t.Fatal("DEFECT: alpha conflict after compaction boundary (per-key max = 4 > R2 = 3) not detected")
	}
	if err.Error() != "Write-write conflict detected. Transaction aborted." {
		t.Fatalf("unexpected conflict error: %v", err)
	}
}
