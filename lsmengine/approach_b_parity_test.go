package lsmengine

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Helper to run a parity test across both SyncModeApproachA and SyncModeApproachB.
func runCrossMode(t *testing.T, name string, fn func(t *testing.T, mode SyncMode)) {
	t.Run(name+"_ApproachA", func(t *testing.T) {
		fn(t, SyncModeApproachA)
	})
	t.Run(name+"_ApproachB", func(t *testing.T) {
		fn(t, SyncModeApproachB)
	})
}

// 1. Existing key in Active Memtable
func TestParity_ExistingKey_ActiveMemtable(t *testing.T) {
	runCrossMode(t, "ActiveMem", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		tx := man.Begin()
		tx.Put("key1", "val1")
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}

		val, err := man.Get("key1", 100)
		if err != nil {
			t.Fatalf("expected nil err, got %v", err)
		}
		if val != "val1" {
			t.Fatalf("expected val1, got %q", val)
		}
	})
}

// 2. Existing key in Frozen Memtable
func TestParity_ExistingKey_FrozenMemtable(t *testing.T) {
	runCrossMode(t, "FrozenMem", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		// Pause flush worker so the frozen memtable stays in Frozenmem
		flushBlocker := make(chan struct{})
		var once sync.Once
		man.flushBuildHook = func() error {
			once.Do(func() {
				<-flushBlocker
			})
			return nil
		}

		tx := man.Begin()
		tx.Put("key1", "val1")
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}

		// Freeze active memtable
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}

		// Read while it is in Frozenmem
		val, err := man.Get("key1", 100)
		if err != nil {
			t.Fatalf("expected nil err, got %v", err)
		}
		if val != "val1" {
			t.Fatalf("expected val1, got %q", val)
		}

		close(flushBlocker)
	})
}

// 3. Existing key in SSTable
func TestParity_ExistingKey_SSTable(t *testing.T) {
	runCrossMode(t, "SSTable", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		tx := man.Begin()
		tx.Put("key1", "val1")
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}

		// Drain flush
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

		val, err := man.Get("key1", 100)
		if err != nil {
			t.Fatalf("expected nil err, got %v", err)
		}
		if val != "val1" {
			t.Fatalf("expected val1, got %q", val)
		}
	})
}

// 4. Missing key across all tiers -> (" ", ErrNotFound)
func TestParity_MissingKey(t *testing.T) {
	runCrossMode(t, "MissingKey", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		val, err := man.Get("nonexistent", 100)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got err=%v", err)
		}
		if val != " " {
			t.Fatalf("expected space string %q, got %q", " ", val)
		}
	})
}

// 5. Active Memtable tombstone -> ("Key is Deleted", ErrKeyDeleted)
func TestParity_ActiveMemtable_Tombstone(t *testing.T) {
	runCrossMode(t, "ActiveTombstone", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		tx := man.Begin()
		tx.Put("key1", "val1")
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit failed: %v", err)
		}

		txDel := man.Begin()
		txDel.Delete("key1")
		if err := txDel.Commit(); err != nil {
			t.Fatalf("Delete commit failed: %v", err)
		}

		val, err := man.Get("key1", 100)
		if !errors.Is(err, ErrKeyDeleted) {
			t.Fatalf("expected ErrKeyDeleted, got err=%v", err)
		}
		if val != "Key is Deleted" {
			t.Fatalf("expected %q, got %q", "Key is Deleted", val)
		}
	})
}

// 6. Frozen Memtable tombstone -> (" ", ErrKeyDeleted)
func TestParity_FrozenMemtable_Tombstone(t *testing.T) {
	runCrossMode(t, "FrozenTombstone", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		flushBlocker := make(chan struct{})
		var once sync.Once
		man.flushBuildHook = func() error {
			once.Do(func() {
				<-flushBlocker
			})
			return nil
		}

		txDel := man.Begin()
		txDel.Delete("key1")
		if err := txDel.Commit(); err != nil {
			t.Fatalf("Delete commit failed: %v", err)
		}

		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}

		val, err := man.Get("key1", 100)
		if !errors.Is(err, ErrKeyDeleted) {
			t.Fatalf("expected ErrKeyDeleted, got err=%v", err)
		}
		if val != " " {
			t.Fatalf("expected %q, got %q", " ", val)
		}

		close(flushBlocker)
	})
}

// 7. SSTable tombstone -> (" ", ErrKeyDeleted)
func TestParity_SSTable_Tombstone(t *testing.T) {
	runCrossMode(t, "SSTableTombstone", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		txDel := man.Begin()
		txDel.Delete("key1")
		if err := txDel.Commit(); err != nil {
			t.Fatalf("Delete commit failed: %v", err)
		}

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

		val, err := man.Get("key1", 100)
		if !errors.Is(err, ErrKeyDeleted) {
			t.Fatalf("expected ErrKeyDeleted, got err=%v", err)
		}
		if val != " " {
			t.Fatalf("expected %q, got %q", " ", val)
		}
	})
}

// 8. Historical read timestamp
func TestParity_HistoricalTimestamp(t *testing.T) {
	runCrossMode(t, "HistoricalTs", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		tx1 := man.Begin()
		tx1.Put("k1", "v1")
		if err := tx1.Commit(); err != nil {
			t.Fatalf("tx1 commit failed: %v", err)
		}
		seq1 := man.CurrentSeqNo.Load()

		tx2 := man.Begin()
		tx2.Put("k1", "v2")
		if err := tx2.Commit(); err != nil {
			t.Fatalf("tx2 commit failed: %v", err)
		}

		// Read at historical seq1 -> must see "v1"
		valOld, errOld := man.Get("k1", seq1)
		if errOld != nil || valOld != "v1" {
			t.Fatalf("expected v1 at seq1, got val=%q, err=%v", valOld, errOld)
		}

		// Read at readTs 0 (prior to tx1) -> must be missing
		valBefore, errBefore := man.Get("k1", 0)
		if !errors.Is(errBefore, ErrNotFound) || valBefore != " " {
			t.Fatalf("expected (\" \", ErrNotFound) at ts 0, got val=%q, err=%v", valBefore, errBefore)
		}
	})
}

// 9. Unexpected SSTable I/O failure -> ("", err)
func TestParity_UnexpectedSSTableReadError(t *testing.T) {
	runCrossMode(t, "IOError", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}
		defer man.Close()

		tx := man.Begin()
		tx.Put("k1", "v1")
		if err := tx.Commit(); err != nil {
			t.Fatalf("tx commit failed: %v", err)
		}
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

		// Inject unexpected I/O error into SSTable reader by closing its underlying file
		man.Mu.Lock()
		if mode == SyncModeApproachA {
			if err := man.ActiveSSreaders[0].File.Close(); err != nil {
				t.Fatalf("failed to close file in Approach A: %v", err)
			}
		} else {
			sr := man.currentSnapshot.readers[0]
			if err := sr.Reader.File.Close(); err != nil {
				t.Fatalf("failed to close file in Approach B: %v", err)
			}
		}
		man.Mu.Unlock()

		val, err := man.Get("k1", 100)
		if err == nil {
			t.Fatalf("expected error from unexpected IO on closed file, got nil")
		}
		if val != "" {
			t.Fatalf("expected empty string on IO failure, got %q", val)
		}
	})
}

// 10. Closed engine -> ("", ErrClosed)
func TestParity_ClosedEngine(t *testing.T) {
	runCrossMode(t, "ClosedEngine", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}

		if err := man.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}

		val, err := man.Get("any_key", 100)
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed, got %v", err)
		}
		if val != "" {
			t.Fatalf("expected empty string, got %q", val)
		}
	})
}

// 11. EngineClosing admission -> ("", ErrClosed)
func TestParity_EngineClosingAdmission(t *testing.T) {
	runCrossMode(t, "EngineClosing", func(t *testing.T, mode SyncMode) {
		dir := t.TempDir()
		cfg := Config{Mode: mode, WalPath: filepath.Join(dir, "wal.log")}
		man, err := CreateManagerWithConfig(cfg)
		if err != nil {
			t.Fatalf("CreateManager failed: %v", err)
		}

		// Hold flush in flight to catch EngineClosing state
		flushBlocker := make(chan struct{})
		flushStarted := make(chan struct{})
		var once sync.Once
		man.flushBuildHook = func() error {
			once.Do(func() {
				close(flushStarted)
				<-flushBlocker
			})
			return nil
		}

		tx := man.Begin()
		tx.Put("k1", "v1")
		if err := tx.Commit(); err != nil {
			t.Fatalf("tx commit failed: %v", err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("TriggerFlush failed: %v", err)
		}

		<-flushStarted

		closeErrCh := make(chan error, 1)
		go func() {
			closeErrCh <- man.Close()
		}()

		// Wait until EngineClosing
		deadline := time.Now().Add(5 * time.Second)
		for {
			man.Mu.RLock()
			st := man.state
			man.Mu.RUnlock()
			if st == EngineClosing {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for EngineClosing")
			}
			time.Sleep(5 * time.Millisecond)
		}

		val, err := man.Get("k1", 100)
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("expected ErrClosed during EngineClosing, got val=%q, err=%v", val, err)
		}
		if val != "" {
			t.Fatalf("expected empty string on ErrClosed, got %q", val)
		}

		close(flushBlocker)
		if err := <-closeErrCh; err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	})
}
