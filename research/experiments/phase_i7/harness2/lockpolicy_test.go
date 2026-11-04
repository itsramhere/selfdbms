package harness2

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"owndbms/lsmengine"
)

func TestLockPolicySpec(t *testing.T) {
	for _, policy := range []string{"", "go", "phasefair", "readerpref", "bad"} {
		for _, block := range []string{"X1", "X2", "X3", "X4", "E1", "SMOKE"} {
			s := Spec{Block: block, LockPolicy: policy, WalSync: true, PublicationMode: "none",
				WriterConcurrency: 1, PacedWriteRatePerSec: 100}
			_, err := s.ResolveMode()
			valid := policy == "" || (s.IsX() && policy != "bad")
			if (err == nil) != valid {
				t.Fatalf("%s/%q: %v", block, policy, err)
			}
		}
	}
	for _, policy := range []string{"", "go", "phasefair", "readerpref"} {
		b, err := json.Marshal(Spec{Block: "X1", LockPolicy: policy})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "lock_policy") != (policy == "phasefair" || policy == "readerpref") {
			t.Fatal(string(b))
		}
	}
	s := Spec{Block: "X1", Cell: "L1_r04_d0000_readerpref", Arm: "A", LockPolicy: "readerpref",
		WriterConcurrency: 1, ReaderConcurrency: 4}
	if !s.IsLocksCell() {
		t.Fatal("locks cell not recognized")
	}
	for _, edit := range []func(*Spec){
		func(s *Spec) { s.Cell = "X1_r04_d0000" },
		func(s *Spec) { s.LockPolicy = "phasefair" },
		func(s *Spec) { s.Block = "E1" },
		func(s *Spec) { s.Arm = "B" },
		func(s *Spec) { s.ReadIODelayUs = 20 },
	} {
		bad := s
		edit(&bad)
		if bad.IsLocksCell() {
			t.Fatalf("scope too broad: %+v", bad)
		}
	}
	b, _ := json.Marshal(TrialResult{})
	if strings.Contains(string(b), "latency_write") {
		t.Fatal("old result JSON changed")
	}
}

type stopOnCommitLock struct {
	*SyncCollector
	stop chan struct{}
	once sync.Once
}

func (c *stopOnCommitLock) OnWriterLockWait(site lsmengine.LockSite, ns int64, holders int32) {
	c.SyncCollector.OnWriterLockWait(site, ns, holders)
	if site == lsmengine.LockSiteCommit {
		c.once.Do(func() { close(c.stop) })
	}
}

// A single manager operation with a deterministic stop signal verifies the
// writer recorder, without invoking Run or launching a campaign observation.
func TestLockPolicyWriteLatencyRecording(t *testing.T) {
	for _, late := range []bool{false, true} {
		m, err := lsmengine.CreateManagerWithConfig(lsmengine.Config{DataDir: t.TempDir(), DisableWALSync: true})
		if err != nil {
			t.Fatal(err)
		}
		stop := make(chan struct{})
		obs := &stopOnCommitLock{SyncCollector: NewSyncHistogramCollector(), stop: stop}
		if err := m.SetSyncObserver(obs, qpcElapsedNanos); err != nil {
			t.Fatal(err)
		}
		sh := &Shard{WriteHistogram: &Histogram{}, WriteLateHistogram: &Histogram{}}
		window := int64(time.Hour)
		if late {
			window = 0
		}
		unpacedWriter(m, sh, stop, Now(), window, Spec{WriterConcurrency: 1}, NewKeyStream(1, 1, 1), 0)
		if sh.WritesCompleted != 1 || sh.WriteHistogram.count+sh.WriteLateHistogram.count != 1 {
			t.Fatal("write sample missing")
		}
		if late && sh.WriteLateHistogram.count != 1 || !late && sh.WriteHistogram.count != 1 {
			t.Fatal("completion window mixed")
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
