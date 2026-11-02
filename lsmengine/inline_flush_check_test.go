package lsmengine

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func inlineTestManager(t *testing.T, mode SyncMode, inline bool, threshold int) *Manager {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode, cfg.DataDir, cfg.InlineFlushCheck = mode, t.TempDir(), inline
	cfg.DisableWALSync, cfg.MemtableThresholdBytes = true, threshold
	m, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestInlineFlushCheckAcquisitions(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		for _, inline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", mode, inline), func(t *testing.T) {
				m := inlineTestManager(t, mode, inline, 0)
				count := 0
				m.commitAcquisitionHook = func() { count++ }
				tx := m.Begin()
				tx.Put("key", "value")
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				want := 2
				if inline {
					want = 1
				}
				if count != want {
					t.Fatalf("acquisitions=%d want=%d", count, want)
				}
				m.Mu.Lock()
				for _, threshold := range []int{0, -1, 1024} {
					m.memtableThresholdBytes = threshold
					limit := int64(threshold)
					if threshold <= 0 {
						limit = 64 << 20
					}
					m.Mem.SizeBytes = limit - 1
					if m.memtableAtThresholdLocked() {
						t.Error("early threshold")
					}
					m.Mem.SizeBytes++
					if !m.memtableAtThresholdLocked() {
						t.Error("missed threshold")
					}
				}
				m.Mu.Unlock()
			})
		}
	}
}

func TestInlineFlushCheckRotation(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			m := inlineTestManager(t, mode, true, 128)
			old := m.Mem
			for i := 0; i < 12; i++ {
				tx := m.Begin()
				tx.Put(fmt.Sprint(i), strings.Repeat("v", 128))
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if m.Mem == old {
				t.Fatal("memtable did not rotate")
			}
			if err := m.WaitForFlushCompletion(); err != nil {
				t.Fatal(err)
			}
			if m.FlushesCompleted() == 0 {
				t.Fatal("no flush publication")
			}
			for i := 0; i < 12; i++ {
				v, err := m.Get(fmt.Sprint(i), m.CurrentSeqNo.Load())
				if err != nil || v != strings.Repeat("v", 128) {
					t.Fatalf("key %d: %q %v", i, v, err)
				}
			}
			late := m.Begin()
			late.Put("late", "value")
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			if err := late.Commit(); !errors.Is(err, ErrClosed) {
				t.Fatalf("late commit: %v", err)
			}
		})
	}
}

func TestInlineFlushCheckConcurrent(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			m := inlineTestManager(t, mode, true, 512)
			var wg sync.WaitGroup
			for worker := 0; worker < 4; worker++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					for i := 0; i < 50; i++ {
						key := fmt.Sprintf("%d/%d", worker, i)
						tx := m.Begin()
						tx.Put(key, "value")
						if err := tx.Commit(); err != nil {
							t.Error(err)
							return
						}
						if v, err := m.Get(key, m.CurrentSeqNo.Load()); err != nil || v != "value" {
							t.Errorf("read %q: %q %v", key, v, err)
							return
						}
					}
				}(worker)
			}
			wg.Wait()
			if err := m.WaitForFlushCompletion(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
