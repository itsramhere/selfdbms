package harness2

import (
	"encoding/json"
	"math"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"owndbms/lsmengine"
)

func TestXSpecValidation(t *testing.T) {
	for _, block := range []string{"X1", "X2", "X3"} {
		s := Spec{Block: block, WalSync: true, PublicationMode: "physical"}
		want := ModeClosedLoop
		if block == "X2" {
			want = ModePublicationOnly
		}
		if got, err := s.ResolveMode(); err != nil || got != want {
			t.Fatalf("%s: %v %v", block, got, err)
		}
		s.WalSync = false
		if _, err := s.ResolveMode(); err == nil {
			t.Fatal("X accepted missing/false WAL sync")
		}
	}
	for _, tc := range []struct {
		path  string
		delay int
		valid bool
		mode  lsmengine.ReadPathMode
	}{
		{"", 0, true, lsmengine.ReadPathBlock}, {"block", 200, true, lsmengine.ReadPathBlock},
		{"legacy_scan", 0, true, lsmengine.ReadPathLegacyScan}, {"bad", 0, false, 0},
		{"block", -1, false, 0}, {"legacy_scan", 1, false, 0},
	} {
		s := Spec{Block: "X1", WalSync: true, ReadPath: tc.path, ReadIODelayUs: tc.delay}
		got, err := s.ResolveReadPath()
		if (err == nil) != tc.valid || (err == nil && got != tc.mode) {
			t.Fatalf("%+v: %v %v", tc, got, err)
		}
		if _, err := s.ResolveMode(); (err == nil) != tc.valid {
			t.Fatalf("ResolveMode accepted invalid treatment: %+v", tc)
		}
	}
	if _, err := (Spec{Block: "X2", WalSync: true}).ResolveMode(); err == nil {
		t.Fatal("X2 accepted missing publication mode")
	}
	for _, block := range []string{"X1", "E1"} {
		b, err := json.Marshal(Spec{Block: block})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "read_io_delay_us") != (block == "X1") {
			t.Fatalf("JSON extension: %s", b)
		}
	}
}

func TestHistogramAccuracyAndMerge(t *testing.T) {
	rng := rand.New(rand.NewSource(731))
	var full, left, right Histogram
	samples := make([]int64, 100000)
	for i := range samples {
		samples[i] = int64(math.Exp(rng.Float64() * math.Log(float64(histogramMax))))
		full.Record(samples[i])
		if i%2 == 0 {
			left.Record(samples[i])
		} else {
			right.Record(samples[i])
		}
	}
	left.Merge(&right)
	if full != left {
		t.Fatal("merge differs from direct recording")
	}
	exact, got := Stats(samples), full.Stats()
	if exact.Count != got.Count || exact.MinNanos != got.MinNanos || exact.MaxNanos != got.MaxNanos || math.Abs(exact.MeanNanos-got.MeanNanos) > .01 {
		t.Fatal("exact aggregates differ")
	}
	for _, pair := range [][2]int64{{exact.P50Nanos, got.P50Nanos}, {*exact.P95Nanos, *got.P95Nanos}, {*exact.P99Nanos, *got.P99Nanos}, {*exact.P999Nanos, *got.P999Nanos}} {
		if math.Abs(float64(pair[1]-pair[0]))/float64(pair[0]) > .01 {
			t.Fatalf("percentile error >1%%: %v", pair)
		}
	}
	for v := int64(0); v < 1000; v++ {
		i := histogramIndex(v)
		if low := histogramValue(i); low > v || float64(v-low) > math.Max(1, float64(v)*.01) {
			t.Fatalf("bucket %d -> %d", v, low)
		}
	}
	if allocs := testing.AllocsPerRun(1000, func() { full.Record(123456) }); allocs != 0 {
		t.Fatalf("Record allocated %f", allocs)
	}
	sh := NewHistogramShard()
	for i := 0; i < 200000; i++ {
		sh.RecordService(100)
		sh.RecordIssue(0, 0, 100)
		sh.RecordIssueLate(0, 0, 100)
	}
	if sh.Dropped != 0 || sh.ServiceHistogram.count != 200000 || len(sh.Issues)+len(sh.IssuesLate) != 0 {
		t.Fatal("X recorder dropped or stored intervals")
	}
}

func TestSyncHistogramConcurrent(t *testing.T) {
	c := NewSyncHistogramCollector()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 1000; j++ {
				c.OnReadCriticalSection(lsmengine.SyncModeApproachA, 1000)
				c.OnWriterLockWait(lsmengine.LockSiteCommit, 2000, 1)
			}
		})
	}
	wg.Wait()
	st := c.Summary()
	read := st.ReadCriticalSectionNanos[lsmengine.SyncModeApproachA.String()]
	if read.Count != 8000 || read.P50Nanos == nil || *read.P99Nanos != 1000 {
		t.Fatalf("read stats: %+v", read)
	}
	wait := st.WriterLockWaitNanos[lsmengine.LockSiteCommit.String()]
	if wait.Count != 8000 || wait.P95Nanos == nil || *wait.P95Nanos != 2000 {
		t.Fatalf("wait stats: %+v", wait)
	}
	if allocs := testing.AllocsPerRun(1000, func() { c.OnReadCriticalSection(lsmengine.SyncModeApproachA, 1234) }); allocs != 0 {
		t.Fatal("atomic histogram allocated")
	}
	old := NewSyncCollector()
	old.OnReadCriticalSection(lsmengine.SyncModeApproachA, 1)
	b, _ := json.Marshal(old.Summary())
	if strings.Contains(string(b), "p50_nanos") {
		t.Fatal("old sync JSON changed")
	}
}

func TestSyncCollectorReset(t *testing.T) {
	for _, histogram := range []bool{false, true} {
		name := "plain"
		if histogram {
			name = "histogram"
		}
		t.Run(name, func(t *testing.T) {
			c := NewSyncCollector()
			if histogram {
				c = NewSyncHistogramCollector()
			}
			for i := 0; i < 3; i++ {
				c.readCS[i].add(1_000_000)
				c.wait[i].add(2_000_000)
				c.pub[i].add(3_000_000)
				c.waitHolders[i].Store(20)
				c.waitArrivals[i].Store(1)
				c.pubHolders[i].Store(30)
				c.pubArrivals[i].Store(1)
			}
			readHistogram, waitHistogram := c.readCS[0].histogram, c.wait[0].histogram
			c.Reset()
			if c.readCS[0].histogram != readHistogram || c.wait[0].histogram != waitHistogram {
				t.Fatal("reset replaced histogram storage")
			}
			for i := 0; i < 3; i++ {
				for _, d := range []*dist{&c.readCS[i], &c.wait[i], &c.pub[i]} {
					if d.count.Load() != 0 || d.sum.Load() != 0 || d.min.Load() != 0 || d.max.Load() != 0 {
						t.Fatal("reset retained aggregate observations")
					}
					if d.histogram != nil {
						for j := range d.histogram {
							if d.histogram[j].Load() != 0 {
								t.Fatal("reset retained histogram observations")
							}
						}
					}
				}
				if c.waitHolders[i].Load() != 0 || c.waitArrivals[i].Load() != 0 || c.pubHolders[i].Load() != 0 || c.pubArrivals[i].Load() != 0 {
					t.Fatal("reset retained holder/arrival observations")
				}
			}
			empty := c.Summary()
			if len(empty.ReadCriticalSectionNanos)+len(empty.WriterLockWaitNanos)+len(empty.PublicationNanos) != 0 {
				t.Fatal("reset summary is not empty")
			}
			c.OnReadCriticalSection(lsmengine.SyncModeApproachA, 1000)
			c.OnWriterLockWait(lsmengine.LockSiteCommit, 2000, 2)
			c.OnPublication(lsmengine.LockSiteFlushPublish, 3000, 3)
			summary := c.Summary()
			read := summary.ReadCriticalSectionNanos[lsmengine.SyncModeApproachA.String()]
			wait := summary.WriterLockWaitNanos[lsmengine.LockSiteCommit.String()]
			pub := summary.PublicationNanos[lsmengine.LockSiteFlushPublish.String()]
			for _, st := range []SyncStats{read, wait, pub} {
				if st.Count != 1 || st.MinNanos != st.MaxNanos || st.MeanNanos != float64(st.MinNanos) {
					t.Fatalf("post-reset aggregates retained old observations: %+v", st)
				}
			}
			if read.MinNanos != 1000 || wait.MinNanos != 2000 || pub.MinNanos != 3000 || wait.MeanHoldersAtArrival != 2 || pub.MeanHoldersAtArrival != 3 {
				t.Fatal("post-reset observations or holder means are incorrect")
			}
			if histogram {
				if read.P99Nanos == nil || *read.P99Nanos != 1000 || wait.P99Nanos == nil || *wait.P99Nanos != 2000 {
					t.Fatal("post-reset histogram retained warmup distribution")
				}
			} else if read.P50Nanos != nil || wait.P50Nanos != nil {
				t.Fatal("reset changed the plain collector's percentile view")
			}
			if n := testing.AllocsPerRun(10, c.Reset); n != 0 {
				t.Fatalf("reset allocated %f objects", n)
			}
		})
	}
}

func TestXSyncExcludesWarmup(t *testing.T) {
	// A longer warmup than window makes the old whole-trial scope conspicuous.
	// No writers run in this synthetic spec, so each completed Get contributes
	// exactly one read critical section; late completions remain in both totals.
	s := miniSpec(t, "X1", "sync_window", "A")
	s.WalSync = true
	s.InstrumentSync = true
	s.ReaderConcurrency = 1
	s.WriterConcurrency = 0
	s.WarmupDurationMs = 100
	s.MeasurementDurationMs = 20
	r, err := Run(s, testEnv(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "SUCCESS" || r.Sync == nil {
		t.Fatalf("synthetic X1 failed: %s %s", r.Status, r.Error)
	}
	count := r.Sync.ReadCriticalSectionNanos[lsmengine.SyncModeApproachA.String()].Count
	windowReads := r.Operations.ReadOpsCompletedInWindow
	allReads := r.Operations.ReadOpsCompleted
	t.Logf("read critical sections=%d window reads=%d late reads=%d", count, windowReads, r.Operations.ReadOpsCompletedLate)
	if windowReads == 0 || count != allReads || count < windowReads {
		t.Fatalf("sync count includes work outside measured reads: count=%d window=%d completed=%d", count, windowReads, allReads)
	}
}

func TestX2ShortfallAndFault(t *testing.T) {
	for _, block := range []string{"X2", "E2"} {
		s := Spec{Block: block, WalSync: true, PublicationMode: "physical"}
		r := &TrialResult{Status: "SUCCESS"}
		publicationTimingOutcome(r, s, true, 20, 1, 19)
		if (r.Status == "SUCCESS") != (block == "X2") {
			t.Fatalf("%s shortfall: %+v", block, r)
		}
		r = &TrialResult{Status: "FAILED", Error: "flush drain: synthetic fault"}
		publicationTimingOutcome(r, s, true, 20, 1, 19)
		if r.Status != "FAILED" || r.Error != "flush drain: synthetic fault" {
			t.Fatal("fault masked")
		}
	}
	// A real fixture validation fault still propagates through Run for X2.
	s := miniSpec(t, "X2", "synthetic", "A")
	s.WalSync = true
	s.PublicationMode = "physical"
	s.InitialSSTableCount = 0
	r, err := Run(s, testEnv(), "synthetic")
	if err == nil || r == nil || r.Status != "FAILED" {
		t.Fatalf("X2 lost real fault: %+v %v", r, err)
	}
}
