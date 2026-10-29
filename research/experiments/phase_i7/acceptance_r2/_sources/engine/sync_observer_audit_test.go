package lsmengine

// Blocker-4 audit tests (R2 engineering acceptance): the synchronization
// instrumentation must be (a) behavior-transparent — attaching an observer
// changes no read result, no tier accounting, and no lifecycle outcome — and
// (b) coverage-complete — every read-path operation leaves the active holder
// counter drained to zero, which catches any read path that bypasses
// rLock/rUnlock or loses an RUnlock on some exit.

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

// runAuditSequenceOn executes a fixed operation sequence against a manager
// and returns the observable behavior: read results in order plus the engine
// diagnostic counters. The sequence deliberately touches every read-path
// entry point (Get hit/miss, LatestValue, FlushErr, ActiveSSTableCount,
// AcquireSnapshot for B) and every lifecycle operation (commit, flush,
// compaction, close).
func runAuditSequenceOn(t *testing.T, man *Manager, mode SyncMode) []string {
	t.Helper()
	var out []string
	note := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	for i := 0; i < 6; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i))
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("flush wait: %v", err)
	}
	for i := 0; i < 9; i++ { // memtable hits, an SSTable-tier hit, and hard misses
		v, err := man.Get(fmt.Sprintf("k%d", i), math.MaxUint64)
		note("get %d = %q err %v", i, v, err)
	}
	seq, err := man.LatestValue([]byte("k2"))
	note("latest k2 = %d err %v", seq, err)
	note("flushErr = %v", man.FlushErr())
	note("sstables = %d", man.ActiveSSTableCount())
	note("memReads = %d diskReads = %d", man.MemtablePointReads(), man.SSTablePointReads())
	if err := man.CompactAll(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	note("compactions = %d sstables = %d", man.CompactionsCompleted(), man.ActiveSSTableCount())
	if mode == SyncModeApproachB {
		snap, err := man.AcquireSnapshot()
		if err != nil {
			t.Fatalf("acquire snapshot: %v", err)
		}
		note("snapshot id = %d readers = %d", snap.ID(), len(snap.Readers()))
		man.ReleaseSnapshot(snap)
	}
	if err := man.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out
}

func newAuditManager(t *testing.T, mode SyncMode) *Manager {
	t.Helper()
	dir := t.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   mode,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 1 << 30,
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	return man
}

// TestObserverTransparencyNoBehaviorChange asserts that attaching the sync
// observer and a flush observer changes nothing observable in engine
// behavior, for each of the three architectures.
func TestObserverTransparencyNoBehaviorChange(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			baseline := runAuditSequenceOn(t, newAuditManager(t, mode), mode)

			man := newAuditManager(t, mode)
			obs := newRecordingObserver()
			if err := man.SetSyncObserver(obs, (&stepClock{step: 50}).Now); err != nil {
				t.Fatalf("set observer: %v", err)
			}
			man.SetFlushObserver(obs)
			instrumented := runAuditSequenceOn(t, man, mode)

			if len(instrumented) != len(baseline) {
				t.Fatalf("observation count changed with instrumentation: %d vs %d", len(instrumented), len(baseline))
			}
			for i := range baseline {
				if instrumented[i] != baseline[i] {
					t.Fatalf("behavior changed with instrumentation at record %d:\n  baseline: %s\n  observed: %s", i, baseline[i], instrumented[i])
				}
			}
			// The observer must actually have been exercised.
			obs.mu.Lock()
			readCS, waits, pubs := len(obs.readCS[mode]), len(obs.waits), len(obs.pubs)
			obs.mu.Unlock()
			if readCS == 0 || waits == 0 || pubs == 0 {
				t.Fatalf("instrumentation was not exercised: readCS=%d waits=%d pubs=%d", readCS, waits, pubs)
			}
		})
	}
}

// TestHolderCounterDrainsAfterEveryOperation asserts holder-counter coverage:
// after every operation that may take a read lock, on every architecture, the
// counter is back to zero. A nonzero value here means a read path bypassed
// rLock/rUnlock or lost an RUnlock on some exit.
func TestHolderCounterDrainsAfterEveryOperation(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man := newAuditManager(t, mode)
			check := func(stage string) {
				t.Helper()
				if h := man.ActiveRLockHolders(); h != 0 {
					t.Fatalf("%s: active holder counter did not drain: %d", stage, h)
				}
			}
			for i := 0; i < 5; i++ {
				tx := man.Begin()
				tx.Put(fmt.Sprintf("k%d", i), "v")
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit: %v", err)
				}
				check("after Commit")
			}
			_, _ = man.Get("k0", math.MaxUint64)
			check("after Get hit")
			_, _ = man.Get("missing", math.MaxUint64)
			check("after Get miss")
			_, _ = man.LatestValue([]byte("k0"))
			check("after LatestValue")
			_ = man.FlushErr()
			check("after FlushErr")
			_ = man.ActiveSSTableCount()
			check("after ActiveSSTableCount")
			if err := man.TriggerFlush(); err != nil {
				t.Fatalf("trigger: %v", err)
			}
			check("after TriggerFlush")
			if err := man.WaitForFlushCompletion(); err != nil {
				t.Fatalf("flush: %v", err)
			}
			check("after WaitForFlushCompletion")
			if err := man.CompactAll(); err != nil {
				t.Fatalf("compact: %v", err)
			}
			check("after CompactAll")
			if mode == SyncModeApproachB {
				snap, err := man.AcquireSnapshot()
				if err != nil {
					t.Fatalf("acquire: %v", err)
				}
				man.ReleaseSnapshot(snap)
				check("after AcquireSnapshot/Release")
			}
			if err := man.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			check("after Close")
		})
	}
}

// TestHolderCounterExactUnderParkedReaders verifies the counter equals the
// number of readers parked inside their critical sections (the quiescent
// case has no overcount), for each architecture.
func TestHolderCounterExactUnderParkedReaders(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man := newAuditManager(t, mode)
			tx := man.Begin()
			tx.Put("k", "v")
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
			var enteredOnce sync.Once
			entered := make(chan struct{})
			release := make(chan struct{})
			man.duringMemtableReadHook = func() {
				enteredOnce.Do(func() { close(entered) })
				<-release
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = man.Get("k", math.MaxUint64)
			}()
			<-entered
			if h := man.ActiveRLockHolders(); h != 1 {
				t.Fatalf("%s: parked reader counted as %d, want exactly 1", mode, h)
			}
			close(release)
			<-done
			if h := man.ActiveRLockHolders(); h != 0 {
				t.Fatalf("counter did not drain after release: %d", h)
			}
			_ = man.Close()
		})
	}
}
