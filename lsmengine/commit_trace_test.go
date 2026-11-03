package lsmengine

import "testing"

type commitTraceStub struct {
	SyncObserver
	reports [16]CommitPhases
	count   int
}

func (o *commitTraceStub) OnCommitPhases(p CommitPhases) {
	o.reports[o.count] = p
	o.count++
}

func TestCommitTracePhases(t *testing.T) {
	for _, sync := range []bool{true, false} {
		t.Run(map[bool]string{true: "sync", false: "no_sync"}[sync], func(t *testing.T) {
			clock := &stepClock{step: 100}
			man, plain := newInstrumentedManager(t, SyncModeApproachA, clock)
			obs := &commitTraceStub{SyncObserver: plain}
			if err := man.SetSyncObserver(obs, clock.Now); err != nil {
				t.Fatal(err)
			}
			man.SetWALSync(sync)
			stale := man.Begin()
			stale.Put("key", "stale")
			for i := 0; i < 3; i++ {
				tx := man.Begin()
				tx.Put("key", "value")
				if i == 1 {
					tx.Put("second", "value")
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if obs.count != i+1 {
					t.Fatalf("reports=%d after %d commits", obs.count, i+1)
				}
				p := obs.reports[i]
				values := []int64{p.ArrivalNs, p.AcquiredNs, p.ValidatedNs, p.WalWriteDoneNs, p.WalSyncDoneNs, p.AppliedNs, p.ReleasedNs, p.FlushCheckDoneNs}
				for j := 1; j < len(values); j++ {
					if values[j] < values[j-1] || values[0] == 0 {
						t.Fatalf("unordered phases: %+v", p)
					}
				}
				if !sync && p.WalSyncDoneNs != p.WalWriteDoneNs {
					t.Fatalf("disabled sync has a duration: %+v", p)
				}
			}
			if err := stale.Commit(); err == nil || obs.count != 3 {
				t.Fatalf("failed commit reported: err=%v reports=%d", err, obs.count)
			}
			if err := man.SetSyncObserver(plain, clock.Now); err != nil {
				t.Fatal(err)
			}
			if man.commitTraceObserver != nil {
				t.Fatal("plain observer retained trace extension")
			}
			tx := man.Begin()
			tx.Put("plain", "value")
			if err := tx.Commit(); err != nil || obs.count != 3 {
				t.Fatalf("plain observer commit: %v reports=%d", err, obs.count)
			}
			if err := man.SetSyncObserver(obs, clock.Now); err != nil {
				t.Fatal(err)
			}
			tx = man.Begin()
			tx.Put("wal_failure", "value")
			if err := man.Log.File.Close(); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err == nil || obs.count != 3 {
				t.Fatalf("WAL failure reported: err=%v reports=%d", err, obs.count)
			}
		})
	}
}
