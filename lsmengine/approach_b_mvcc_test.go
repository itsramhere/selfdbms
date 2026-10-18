package lsmengine

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Helper to create an engine in the specified SyncMode
func createEngineWithMode(t *testing.T, dir string, mode SyncMode) *Manager {
	walPath := filepath.Join(dir, fmt.Sprintf("wal_%d.log", time.Now().UnixNano()))
	cfg := Config{
		Mode:    mode,
		WalPath: walPath,
	}
	man, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create manager for mode %v: %v", mode, err)
	}
	return man
}

// Helper to flush and wait
func flushAndWait(t *testing.T, man *Manager) {
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("TriggerFlush failed: %v", err)
	}
	for {
		man.flushQueueMu.Lock()
		qLen := len(man.flushQueue)
		man.flushQueueMu.Unlock()
		man.Mu.RLock()
		fLen := len(man.Frozenmem)
		man.Mu.RUnlock()
		if qLen == 0 && fLen == 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
}

// 1. Single-key read/write parity across Approach A and Approach B
func TestApproachEquivalence_SingleKeyCRUD(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			man := createEngineWithMode(t, dir, mode)
			defer man.Close()

			// 1. Non-existent key
			_, err := man.Get("missing", 100)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound for missing key, got %v", err)
			}

			// 2. Put and Get
			tx1 := man.Begin()
			tx1.Put("k1", "v1")
			if err := tx1.Commit(); err != nil {
				t.Fatalf("Commit failed: %v", err)
			}
			ts1 := man.CurrentSeqNo.Load()

			val1, err := man.Get("k1", ts1)
			if err != nil || val1 != "v1" {
				t.Fatalf("expected 'v1', got %q, err=%v", val1, err)
			}

			// 3. Overwrite
			tx2 := man.Begin()
			tx2.Put("k1", "v2")
			if err := tx2.Commit(); err != nil {
				t.Fatalf("Commit failed: %v", err)
			}
			ts2 := man.CurrentSeqNo.Load()

			val2, err := man.Get("k1", ts2)
			if err != nil || val2 != "v2" {
				t.Fatalf("expected 'v2', got %q, err=%v", val2, err)
			}

			// 4. Delete
			tx3 := man.Begin()
			tx3.Delete("k1")
			if err := tx3.Commit(); err != nil {
				t.Fatalf("Commit failed: %v", err)
			}
			ts3 := man.CurrentSeqNo.Load()

			_, err = man.Get("k1", ts3)
			if !errors.Is(err, ErrKeyDeleted) {
				t.Fatalf("expected ErrKeyDeleted, got %v", err)
			}
		})
	}
}

// 2. Multi-version read visibility across historical timestamps
func TestApproachEquivalence_MultiVersionVisibility(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			man := createEngineWithMode(t, dir, mode)
			defer man.Close()

			// Version 1 at ts1
			tx1 := man.Begin()
			tx1.Put("mvcc_k", "val_1")
			tx1.Commit()
			ts1 := man.CurrentSeqNo.Load()

			// Flush to SSTable
			flushAndWait(t, man)

			// Version 2 at ts2
			tx2 := man.Begin()
			tx2.Put("mvcc_k", "val_2")
			tx2.Commit()
			ts2 := man.CurrentSeqNo.Load()

			// Flush to SSTable
			flushAndWait(t, man)

			// Version 3 at ts3
			tx3 := man.Begin()
			tx3.Put("mvcc_k", "val_3")
			tx3.Commit()
			ts3 := man.CurrentSeqNo.Load()

			// Historical queries
			// At ts0 (0): not found
			_, err := man.Get("mvcc_k", 0)
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound at ts=0, got %v", err)
			}

			// At ts1: val_1
			v1, err := man.Get("mvcc_k", ts1)
			if err != nil || v1 != "val_1" {
				t.Fatalf("expected 'val_1' at ts=%d, got %q, err=%v", ts1, v1, err)
			}

			// At ts2: val_2
			v2, err := man.Get("mvcc_k", ts2)
			if err != nil || v2 != "val_2" {
				t.Fatalf("expected 'val_2' at ts=%d, got %q, err=%v", ts2, v2, err)
			}

			// At ts3: val_3
			v3, err := man.Get("mvcc_k", ts3)
			if err != nil || v3 != "val_3" {
				t.Fatalf("expected 'val_3' at ts=%d, got %q, err=%v", ts3, v3, err)
			}
		})
	}
}

// 3. Tombstone shadowing across memory and SSTable tiers
func TestApproachEquivalence_TombstoneShadowingAcrossTiers(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			man := createEngineWithMode(t, dir, mode)
			defer man.Close()

			// 1. Put key and flush to SSTable
			tx1 := man.Begin()
			tx1.Put("tomb_k", "disk_val")
			tx1.Commit()
			flushAndWait(t, man)

			// 2. Put Delete tombstone into active memtable (not yet flushed)
			tx2 := man.Begin()
			tx2.Delete("tomb_k")
			tx2.Commit()
			ts2 := man.CurrentSeqNo.Load()

			// Query at ts2: must see tombstone from Memtable shadowing disk
			_, err := man.Get("tomb_k", ts2)
			if !errors.Is(err, ErrKeyDeleted) {
				t.Fatalf("expected ErrKeyDeleted from memory tombstone, got %v", err)
			}

			// 3. Flush tombstone to disk as second SSTable
			flushAndWait(t, man)

			// Query at ts2: must see tombstone from disk shadowing older SSTable
			_, err = man.Get("tomb_k", ts2)
			if !errors.Is(err, ErrKeyDeleted) {
				t.Fatalf("expected ErrKeyDeleted from disk tombstone, got %v", err)
			}
		})
	}
}

// 4. Compaction query parity: Query results are identical before and after compaction
func TestApproachEquivalence_CompactionQueryParity(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			man := createEngineWithMode(t, dir, mode)
			defer man.Close()

			// Write 10 keys across 3 flushes
			for batch := 0; batch < 3; batch++ {
				tx := man.Begin()
				for k := 0; k < 10; k++ {
					tx.Put(fmt.Sprintf("key_%d", k), fmt.Sprintf("val_%d_b%d", k, batch))
				}
				if batch == 1 {
					// Delete key_5 in batch 1
					tx.Delete("key_5")
				}
				tx.Commit()
				flushAndWait(t, man)
			}

			// Pre-compaction verification
			tsPre := man.CurrentSeqNo.Load()
			preVal0, err := man.Get("key_0", tsPre)
			if err != nil || preVal0 != "val_0_b2" {
				t.Fatalf("pre-compaction key_0 mismatch: %q, err=%v", preVal0, err)
			}
			preVal5, err := man.Get("key_5", tsPre)
			if err != nil || preVal5 != "val_5_b2" {
				t.Fatalf("pre-compaction key_5 mismatch: %q, err=%v", preVal5, err)
			}

			// Run compaction
			if err := man.CompactAll(); err != nil {
				t.Fatalf("CompactAll failed: %v", err)
			}

			// Post-compaction verification
			postVal0, err := man.Get("key_0", tsPre)
			if err != nil || postVal0 != "val_0_b2" {
				t.Fatalf("post-compaction key_0 mismatch: %q, err=%v", postVal0, err)
			}
			postVal5, err := man.Get("key_5", tsPre)
			if err != nil || postVal5 != "val_5_b2" {
				t.Fatalf("post-compaction key_5 mismatch: %q, err=%v", postVal5, err)
			}
		})
	}
}

// 5. Concurrent Workload Parity: Writers, flushes, compactions, and readers running simultaneously
func TestApproachEquivalence_ConcurrentWorkload(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			man := createEngineWithMode(t, dir, mode)
			defer man.Close()

			const writers = 5
			const readers = 10
			const opsPerWorker = 50

			var wg sync.WaitGroup
			stopFlusher := make(chan struct{})

			// Periodic background flush / compaction
			wg.Add(1)
			go func() {
				defer wg.Done()
				ticker := time.NewTicker(15 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stopFlusher:
						return
					case <-ticker.C:
						_ = man.TriggerFlush()
						_ = man.CompactAll()
					}
				}
			}()

			// Concurrent writers
			for w := 0; w < writers; w++ {
				wg.Add(1)
				go func(wid int) {
					defer wg.Done()
					for op := 0; op < opsPerWorker; op++ {
						tx := man.Begin()
						key := fmt.Sprintf("ckey_%d", op%10)
						val := fmt.Sprintf("w%d_val%d", wid, op)
						tx.Put(key, val)
						_ = tx.Commit()
						time.Sleep(1 * time.Millisecond)
					}
				}(w)
			}

			// Concurrent readers
			for r := 0; r < readers; r++ {
				wg.Add(1)
				go func(rid int) {
					defer wg.Done()
					for op := 0; op < opsPerWorker; op++ {
						ts := man.CurrentSeqNo.Load()
						key := fmt.Sprintf("ckey_%d", op%10)
						_, _ = man.Get(key, ts)
						time.Sleep(1 * time.Millisecond)
					}
				}(r)
			}

			// Wait for writers and readers
			time.Sleep(200 * time.Millisecond)
			close(stopFlusher)
			wg.Wait()
		})
	}
}
