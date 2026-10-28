package lsmengine

// E2 publication-only driver seam (Phase I7 G2).
//
// The gate lets a harness publish MEMTABLES FROZEN BEFORE the measurement window
// at scheduled instants, without injecting writes into the window. This is the
// fix for the historical Workload B2 defect (unaccounted 50-key write batches
// that held Manager.Mu across 50 fsyncs; see 03_PACING_AUDIT.md AF-3).

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func gateTestManager(t *testing.T, mode SyncMode) *Manager {
	t.Helper()
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   mode,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 64 << 20,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return man
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func seedAndAdvance(t *testing.T, man *Manager, from, n int) {
	t.Helper()
	tx := man.Begin()
	if tx == nil {
		t.Fatal("Begin returned nil")
	}
	for k := 0; k < n; k++ {
		idx := from + k
		tx.Put(fmt.Sprintf("gate_key_%06d", idx), fmt.Sprintf("gate_val_%06d", idx))
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("trigger flush: %v", err)
	}
}

// The gate must hold publications until a token is released, and release
// exactly one publication per token.
func TestFlushInjectionGate_BlocksUntilReleased(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man := gateTestManager(t, mode)
			defer man.Close()

			man.EnableFlushInjectionGate()
			seedAndAdvance(t, man, 0, 20)
			seedAndAdvance(t, man, 20, 20)

			waitUntil(t, "worker to reach the gate", 3*time.Second, func() bool {
				return man.PendingFlushes() == 2
			})
			time.Sleep(80 * time.Millisecond)
			if got := man.FlushesCompleted(); got != 0 {
				t.Fatalf("publication happened with a closed gate: flushes=%d", got)
			}
			if got := man.ActiveSSTableCount(); got != 0 {
				t.Fatalf("table published with a closed gate: tables=%d", got)
			}

			man.ReleaseOneFlush()
			waitUntil(t, "first publication", 5*time.Second, func() bool {
				return man.FlushesCompleted() == 1
			})
			time.Sleep(60 * time.Millisecond)
			if got := man.FlushesCompleted(); got != 1 {
				t.Fatalf("one token released %d publications", got)
			}

			man.ReleaseOneFlush()
			waitUntil(t, "second publication", 5*time.Second, func() bool {
				return man.FlushesCompleted() == 2
			})
			if got := man.ActiveSSTableCount(); got != 2 {
				t.Fatalf("expected 2 published tables, got %d", got)
			}
			man.DisableFlushInjectionGate()
		})
	}
}

// Shutdown must never block on an armed gate.
func TestFlushInjectionGate_DoesNotBlockClose(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man := gateTestManager(t, mode)
			man.EnableFlushInjectionGate()
			seedAndAdvance(t, man, 0, 20)
			waitUntil(t, "worker to reach the gate", 3*time.Second, func() bool {
				return man.PendingFlushes() == 1
			})
			time.Sleep(50 * time.Millisecond)

			done := make(chan error, 1)
			go func() { done <- man.Close() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Close returned error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close blocked on the publication-injection gate")
			}
		})
	}
}
