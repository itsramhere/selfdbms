package harness2

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"owndbms/lsmengine"
)

func TestDiagTraceOffJSON(t *testing.T) {
	b, err := json.Marshal(Spec{})
	if err != nil {
		t.Fatal(err)
	}
	const legacy = `{"schema":"","trial_id":"","block":"","cell":"","triplet":0,"arm":"","order_position":0,"runlist_position":0,"seed":0,"workload_id":"","reader_concurrency":0,"writer_concurrency":0,"measurement_duration_ms":0,"warmup_duration_ms":0,"cooldown_duration_ms":0,"keyspace_cardinality":0,"initial_sstable_count":0,"initial_keys_per_sstable":0,"target_read_hit_ratio":0,"memtable_threshold_bytes":0,"flush_trigger_interval_ms":0,"compaction_trigger_interval_ms":0,"wal_sync":false,"read_distribution":"","data_dir":"","expected_reader_set_size":0}`
	if string(b) != legacy {
		t.Fatalf("untraced spec changed: %s", b)
	}
	b, err = json.Marshal(TrialResult{})
	if err != nil || strings.Contains(string(b), "stall_diagnostics") {
		t.Fatalf("untraced result changed: %s %v", b, err)
	}
	s := Spec{DiagTrace: true}
	if _, err := s.ResolveMode(); err == nil || !strings.Contains(err.Error(), "instrument_sync") {
		t.Fatalf("missing sync instrumentation accepted: %v", err)
	}
	// Only traced X trials may override WAL sync; campaign validation stays strict.
	s = Spec{Block: "X4", InstrumentSync: true, WriterConcurrency: 1, PacedWriteRatePerSec: 50}
	if _, err := s.ResolveMode(); err == nil {
		t.Fatal("untraced X accepted disabled WAL sync")
	}
	s.DiagTrace = true
	if _, err := s.ResolveMode(); err != nil {
		t.Fatal(err)
	}
}

func TestDiagTraceShortX4(t *testing.T) {
	s := miniSpec(t, "X4", "stall_diag", "A")
	s.DiagTrace, s.InstrumentSync, s.WalSync = true, true, true
	s.MeasurementDurationMs, s.WarmupDurationMs = 500, 0
	s.PacedWriteRatePerSec = 50
	r, err := Run(s, testEnv(), "test")
	if err != nil || r.Status != "SUCCESS" {
		t.Fatalf("short diagnostic: %v %+v", err, r)
	}
	d := r.StallDiagnostics
	if d == nil || len(d.Commits) < 5 || d.CommitCount == 0 {
		t.Fatalf("missing first commits: %+v", d)
	}
	for i, c := range d.Commits {
		if i < 5 && c.Index != int64(i+1) {
			t.Fatalf("first commits missing: %+v", d.Commits)
		}
		if c.ArrivalNs < 0 || c.ArrivalNs >= d.WindowNs || c.FlushCheckDoneNs > r.Timestamps.MeasuredWindowNs+int64(time.Second) {
			t.Fatalf("commit outside window: %+v", c)
		}
	}
	for name, h := range d.PhaseHistograms {
		if h.Stats.Count != d.CommitCount || len(h.Buckets) == 0 {
			t.Fatalf("bad %s histogram: %+v", name, h)
		}
	}
	if len(d.SlowestReads) != s.ReaderConcurrency || d.SlowestReads[0].LengthNs == 0 {
		t.Fatalf("missing slowest reads: %+v", d.SlowestReads)
	}
	if len(d.Runtime.SchedLatencies) == 0 || len(d.Runtime.GCTotalPauses) == 0 {
		t.Fatal("missing runtime histogram deltas")
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
	t.Logf("500ms X4: %d commits, %d retained, %d heartbeat gaps, %d reader maxima", d.CommitCount, len(d.Commits), len(d.HeartbeatGaps), len(d.SlowestReads))
}

func TestCommitTraceCollectorCapacityAndConcurrency(t *testing.T) {
	c := newStallCollector(NewSyncHistogramCollector(), 0)
	c.reset()
	c.baseNs, c.windowNs = 100, int64(time.Second)
	p := lsmengine.CommitPhases{ArrivalNs: 100, AcquiredNs: 200, ValidatedNs: 300, WalWriteDoneNs: 400, WalSyncDoneNs: 500, AppliedNs: 600, ReleasedNs: 700, FlushCheckDoneNs: 4_000_100}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 600; j++ {
				c.OnCommitPhases(p)
			}
		}()
	}
	wg.Wait()
	if c.count.Load() != 4800 || c.next.Load() != 4800 || c.phases[6].count.Load() != 4800 {
		t.Fatal("lost concurrent observations")
	}
	c.before.read()
	c.after.read()
	d := c.result(time.Now(), c.windowNs)
	if len(d.Commits) != diagnosticCapacity || d.CommitsDropped != 4800-diagnosticCapacity || d.Commits[0].ArrivalNs != 0 {
		t.Fatalf("capacity or relative time incorrect: retained=%d dropped=%d first=%+v", len(d.Commits), d.CommitsDropped, d.Commits[0])
	}
	if n := testing.AllocsPerRun(10, func() { c.OnCommitPhases(p) }); n != 0 {
		t.Fatalf("callback allocated %f objects", n)
	}
	c.reset()
	if c.count.Load() != 0 || c.next.Load() != 0 || c.phases[6].count.Load() != 0 {
		t.Fatal("reset retained observations")
	}
	p.ArrivalNs = 99
	c.OnCommitPhases(p)
	p.ArrivalNs = c.baseNs + c.windowNs
	c.OnCommitPhases(p)
	if c.count.Load() != 0 {
		t.Fatal("out-of-window commit retained")
	}
	p.ArrivalNs = c.baseNs
	p.FlushCheckDoneNs = p.ArrivalNs + int64(time.Millisecond)
	for i := 0; i < 6; i++ {
		c.OnCommitPhases(p)
	}
	if c.next.Load() != 5 || c.phases[6].count.Load() != 6 {
		t.Fatal("first five or whole-window histogram missing")
	}
	p.FlushCheckDoneNs = p.ArrivalNs + int64(3*time.Millisecond)
	c.OnCommitPhases(p)
	if c.next.Load() != 6 || c.commits[5].Index != 7 {
		t.Fatal("3ms threshold commit missing")
	}
}

func TestDiagTraceRuntimeOffsets(t *testing.T) {
	c := newStallCollector(NewSyncHistogramCollector(), 0)
	c.windowNs = int64(100 * time.Millisecond)
	c.before.read()
	c.after.read()
	wall := time.Unix(1000, 0)
	c.before.gc.NumGC, c.after.gc.NumGC = 10, 13
	c.before.gc.PauseTotal, c.after.gc.PauseTotal = time.Millisecond, 4*time.Millisecond
	c.after.gc.Pause = []time.Duration{time.Millisecond, 2 * time.Millisecond, time.Millisecond}
	c.after.gc.PauseEnd = []time.Time{wall.Add(-time.Millisecond), wall.Add(time.Millisecond), wall.Add(101 * time.Millisecond)}
	r := c.result(wall, int64(105*time.Millisecond)).Runtime
	if r.GCCountDelta != 3 || r.GCPauseTotalNs != int64(3*time.Millisecond) || len(r.GCPauses) != 1 || r.GCPauses[0].StartNs != -int64(time.Millisecond) || r.GCPauses[0].LengthNs != int64(2*time.Millisecond) {
		t.Fatalf("GC window offsets or deltas incorrect: %+v", r)
	}
}
