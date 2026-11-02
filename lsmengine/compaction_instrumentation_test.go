package lsmengine

import (
	"os"
	"sync/atomic"
	"testing"
)

type lifecycleRecorder struct {
	*recordingObserver
	wall      atomic.Int64
	wallCount atomic.Int64
	inputs    atomic.Int64
	retired   atomic.Int64
	removed   atomic.Int64
	lag       atomic.Int64
	high      atomic.Int64
	live      atomic.Int64
}

func (o *lifecycleRecorder) OnCompaction(ns int64, inputs int) {
	o.wallCount.Add(1)
	o.wall.Add(ns)
	o.inputs.Add(int64(inputs))
}
func (o *lifecycleRecorder) OnTableRetired() {
	o.retired.Add(1)
	n := o.live.Add(1)
	for {
		old := o.high.Load()
		if n <= old || o.high.CompareAndSwap(old, n) {
			break
		}
	}
}
func (o *lifecycleRecorder) OnTableRemoved(ns int64) {
	o.lag.Add(ns)
	o.removed.Add(1)
	o.live.Add(-1)
}

func TestCompactionLifecycleObserved(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man, _ := aminFixture(t, mode, 3, 10)
			defer man.Close()
			clock := &stepClock{step: 100}
			obs := &lifecycleRecorder{recordingObserver: newRecordingObserver()}
			if err := man.SetSyncObserver(obs, clock.Now); err != nil {
				t.Fatal(err)
			}
			var paths []string
			var release func()
			switch mode {
			case SyncModeApproachB:
				snap, err := man.AcquireSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				for _, sr := range snap.readers {
					paths = append(paths, sr.Path)
				}
				release = func() { man.ReleaseSnapshot(snap); man.cleanupWg.Wait() }
			case SyncModeApproachAMin:
				man.rLock()
				readers := append([]*SSTableReader(nil), man.ActiveSSreaders...)
				for _, sr := range readers {
					sr.lease()
					paths = append(paths, sr.File.Name())
				}
				man.rUnlock()
				release = func() {
					for _, sr := range readers {
						if sr.releaseLease() {
							man.reclaimIfDrained(sr)
						}
					}
				}
			default:
				// A readers retain Mu: publication cannot retire their inputs until release.
				man.rLock()
				for _, sr := range man.ActiveSSreaders {
					paths = append(paths, sr.File.Name())
				}
				release = man.rUnlock
			}
			done := make(chan error, 1)
			if mode == SyncModeApproachA {
				entered := make(chan struct{})
				go func() { close(entered); done <- man.CompactAll() }()
				<-entered
				// The reader's lock is still held; input files cannot be removed.
				for _, path := range paths {
					if _, err := os.Stat(path); err != nil {
						t.Fatal(err)
					}
				}
				if obs.retired.Load() != 0 {
					t.Fatal("retired while A reader held Mu")
				}
				release()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			} else {
				if err := man.CompactAll(); err != nil {
					t.Fatal(err)
				}
				for _, path := range paths {
					if _, err := os.Stat(path); err != nil {
						t.Fatal("removed before release", err)
					}
				}
				if obs.removed.Load() != 0 || obs.live.Load() != 3 {
					t.Fatal("premature removal")
				}
				clock.Now() // Advance the synthetic timeline while the reader remains pinned.
				release()
			}
			for _, path := range paths {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("input not removed", err)
				}
			}
			if obs.wallCount.Load() != 1 || obs.wall.Load() <= 0 || obs.inputs.Load() != 3 || obs.retired.Load() != 3 || obs.removed.Load() != 3 || obs.lag.Load() <= 0 || obs.high.Load() != 3 || obs.live.Load() != 0 {
				t.Fatal("missing lifecycle metrics")
			}
			start, pub := LockSiteCompactionStart, LockSiteCompactionPublish
			if mode == SyncModeApproachB {
				pub = LockSiteCompactionSnapshotPublish
			}
			obs.mu.Lock()
			counts := map[LockSite]int{}
			waits := map[LockSite]int{}
			for _, r := range obs.pubs {
				if r.waitNs <= 0 {
					t.Fatal("nonpositive hold")
				}
				counts[r.site]++
			}
			for _, r := range obs.waits {
				if r.waitNs <= 0 {
					t.Fatal("nonpositive wait")
				}
				waits[r.site]++
			}
			obs.mu.Unlock()
			if counts[pub] != 1 || waits[pub] != 1 {
				t.Fatal("publication site absent")
			}
			if mode != SyncModeApproachB && (counts[start] != 1 || waits[start] != 1) {
				t.Fatal("start site absent")
			}
			wall := obs.wall.Load()
			if err := man.CompactAll(); err != nil {
				t.Fatal(err)
			}
			if obs.wall.Load() != wall || obs.inputs.Load() != 3 {
				t.Fatal("no-op counted as work")
			}
		})
	}
}

func TestCompactionStartObservedOnEarlyExits(t *testing.T) {
	for _, closed := range []bool{false, true} {
		man, obs := newInstrumentedManager(t, SyncModeApproachA, &stepClock{step: 100})
		if closed {
			if err := man.Close(); err != nil {
				t.Fatal(err)
			}
		}
		err := man.CompactAll()
		if (err != nil) != closed {
			t.Fatal(err)
		}
		obs.mu.Lock()
		n := 0
		for _, r := range obs.pubs {
			if r.site == LockSiteCompactionStart {
				n++
			}
		}
		obs.mu.Unlock()
		if n != 1 {
			t.Fatal("early exit hold not recorded")
		}
	}
}

func TestCompactionStartObservedOnIteratorError(t *testing.T) {
	man, _ := aminFixture(t, SyncModeApproachA, 2, 10)
	defer man.Close()
	man.Mu.Lock()
	original := man.ActiveSSreaders[1].File
	// Use a handle whose path is absent so the second iterator open fails.
	missing, err := os.CreateTemp(t.TempDir(), "missing-*.sst")
	if err != nil {
		t.Fatal(err)
	}
	missing.Close()
	if err := os.Remove(missing.Name()); err != nil {
		t.Fatal(err)
	}
	man.ActiveSSreaders[1].File = missing
	man.Mu.Unlock()
	defer func() { man.Mu.Lock(); man.ActiveSSreaders[1].File = original; man.Mu.Unlock() }()
	obs := &lifecycleRecorder{recordingObserver: newRecordingObserver()}
	clock := &stepClock{step: 100}
	if err := man.SetSyncObserver(obs, clock.Now); err != nil {
		t.Fatal(err)
	}
	if err := man.CompactAll(); err == nil {
		t.Fatal("expected iterator open error")
	}
	if obs.wall.Load() != 0 {
		t.Fatal("failed work counted")
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.pubs) != 1 || obs.pubs[0].site != LockSiteCompactionStart || obs.pubs[0].waitNs <= 0 {
		t.Fatal(obs.pubs)
	}
}
