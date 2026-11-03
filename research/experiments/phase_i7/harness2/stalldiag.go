package harness2

import (
	"math"
	"runtime/debug"
	"runtime/metrics"
	"sync/atomic"
	"time"

	"owndbms/lsmengine"
	"owndbms/research/experiments/harness"
)

const diagnosticCapacity = 4096

var commitPhaseNames = [...]string{"lock_wait", "validate", "wal_write", "wal_sync", "apply", "flush_check", "total"}

type CommitTrace struct {
	Index int64 `json:"index"` // one-based callback order; X4 has one writer
	lsmengine.CommitPhases
}

type TimeGap struct {
	StartNs  int64 `json:"start_ns"`
	LengthNs int64 `json:"length_ns"`
}

type SlowRead struct {
	Worker int `json:"worker"`
	TimeGap
}

func (r *SlowRead) record(start, done int64) {
	if done-start > r.LengthNs {
		r.StartNs, r.LengthNs = start, done-start
	}
}

type HistogramBucket struct {
	LowerNs int64 `json:"lower_ns"`
	Count   int64 `json:"count"`
}

type PhaseHistogram struct {
	Stats   LatencyStats      `json:"stats"`
	Buckets []HistogramBucket `json:"buckets"`
}

// Nil metric bounds denote negative/positive infinity, respectively.
type RuntimeBucket struct {
	LowerSeconds *float64 `json:"lower_seconds"`
	UpperSeconds *float64 `json:"upper_seconds"`
	Count        uint64   `json:"count"`
}

type RuntimeDiagnostics struct {
	GCCountDelta   int64           `json:"gc_count_delta"`
	GCPauseTotalNs int64           `json:"gc_pause_total_delta_ns"`
	GCPauses       []TimeGap       `json:"gc_pauses"`
	GCHistoryLost  bool            `json:"gc_history_lost"`
	SchedLatencies []RuntimeBucket `json:"sched_latencies_seconds"`
	GCTotalPauses  []RuntimeBucket `json:"sched_pauses_total_gc_seconds"`
	SnapshotEndNs  int64           `json:"snapshot_end_ns"`
}

type StallDiagnostics struct {
	WindowNs         int64                     `json:"window_ns"`
	Commits          []CommitTrace             `json:"commits"`
	CommitsDropped   int64                     `json:"commits_dropped"`
	CommitCount      int64                     `json:"commit_count"`
	PhaseHistograms  map[string]PhaseHistogram `json:"phase_histograms"`
	HeartbeatGaps    []TimeGap                 `json:"heartbeat_gaps"`
	HeartbeatDropped int64                     `json:"heartbeat_dropped"`
	SlowestReads     []SlowRead                `json:"slowest_reads"`
	Runtime          RuntimeDiagnostics        `json:"runtime"`
}

// Atomic reservation permits concurrent commit callbacks after the engine unlocks.
// Histograms use the X collector's atomic buckets, materialized as Histogram after join.
type stallCollector struct {
	*SyncCollector
	baseNs, windowNs  int64
	commits           []CommitTrace
	next, count       atomic.Int64
	phases            [len(commitPhaseNames)]dist
	gaps              []TimeGap
	gapCount, gapDrop int
	reads             []SlowRead
	before, after     runtimeSnapshot
}

type runtimeSnapshot struct {
	gc      debug.GCStats
	samples []metrics.Sample
}

func newRuntimeSnapshot() runtimeSnapshot {
	return runtimeSnapshot{
		gc:      debug.GCStats{Pause: make([]time.Duration, 256), PauseEnd: make([]time.Time, 256)},
		samples: []metrics.Sample{{Name: "/sched/latencies:seconds"}, {Name: "/sched/pauses/total/gc:seconds"}},
	}
}

func (s *runtimeSnapshot) read() {
	debug.ReadGCStats(&s.gc)
	metrics.Read(s.samples)
}

func newStallCollector(sync *SyncCollector, readers int) *stallCollector {
	c := &stallCollector{
		SyncCollector: sync, commits: make([]CommitTrace, diagnosticCapacity),
		gaps: make([]TimeGap, diagnosticCapacity), reads: make([]SlowRead, readers),
		before: newRuntimeSnapshot(), after: newRuntimeSnapshot(),
	}
	for i := range c.phases {
		c.phases[i].histogram = new([histogramBuckets]atomic.Int64)
	}
	return c
}

func (c *stallCollector) reset() {
	c.next.Store(0)
	c.count.Store(0)
	c.gapCount, c.gapDrop = 0, 0
	for i := range c.phases {
		c.phases[i].reset()
	}
	for i := range c.reads {
		c.reads[i] = SlowRead{Worker: i}
	}
}

func (c *stallCollector) OnCommitPhases(p lsmengine.CommitPhases) {
	if c.windowNs == 0 || p.ArrivalNs < c.baseNs || p.ArrivalNs-c.baseNs >= c.windowNs {
		return
	}
	index := c.count.Add(1)
	durations := [...]int64{p.AcquiredNs - p.ArrivalNs, p.ValidatedNs - p.AcquiredNs,
		p.WalWriteDoneNs - p.ValidatedNs, p.WalSyncDoneNs - p.WalWriteDoneNs,
		p.AppliedNs - p.WalSyncDoneNs, p.FlushCheckDoneNs - p.ReleasedNs,
		p.FlushCheckDoneNs - p.ArrivalNs}
	for i, d := range durations {
		c.phases[i].add(d)
	}
	if index > 5 && durations[6] < int64(3*time.Millisecond) {
		return
	}
	slot := c.next.Add(1) - 1
	if slot >= int64(len(c.commits)) {
		return
	}
	p.ArrivalNs -= c.baseNs
	p.AcquiredNs -= c.baseNs
	p.ValidatedNs -= c.baseNs
	p.WalWriteDoneNs -= c.baseNs
	p.WalSyncDoneNs -= c.baseNs
	p.AppliedNs -= c.baseNs
	p.ReleasedNs -= c.baseNs
	p.FlushCheckDoneNs -= c.baseNs
	c.commits[slot] = CommitTrace{Index: index, CommitPhases: p}
}

func (c *stallCollector) heartbeat(base harness.PrecisionTime, stop <-chan struct{}) {
	previous := int64(0)
	for {
		time.Sleep(time.Millisecond)
		now := ElapsedNs(base)
		if previous >= c.windowNs {
			return
		}
		if now-previous > int64(3*time.Millisecond) {
			if c.gapCount < len(c.gaps) {
				c.gaps[c.gapCount] = TimeGap{StartNs: previous, LengthNs: now - previous}
				c.gapCount++
			} else {
				c.gapDrop++
			}
		}
		previous = now
		select {
		case <-stop:
			return
		default:
		}
	}
}

func metricDelta(before, after metrics.Value) []RuntimeBucket {
	b, a := before.Float64Histogram(), after.Float64Histogram()
	out := make([]RuntimeBucket, 0, len(a.Counts))
	for i, n := range a.Counts {
		bucket := RuntimeBucket{Count: n - b.Counts[i]}
		if !math.IsInf(a.Buckets[i], 0) {
			v := a.Buckets[i]
			bucket.LowerSeconds = &v
		}
		if !math.IsInf(a.Buckets[i+1], 0) {
			v := a.Buckets[i+1]
			bucket.UpperSeconds = &v
		}
		out = append(out, bucket)
	}
	return out
}

func (c *stallCollector) result(wallStart time.Time, snapshotEnd int64) *StallDiagnostics {
	n := c.next.Load()
	kept := min(n, int64(len(c.commits)))
	out := &StallDiagnostics{
		WindowNs: c.windowNs, Commits: c.commits[:kept], CommitsDropped: n - kept,
		CommitCount: c.count.Load(), PhaseHistograms: make(map[string]PhaseHistogram),
		HeartbeatGaps: c.gaps[:c.gapCount], HeartbeatDropped: int64(c.gapDrop), SlowestReads: c.reads,
	}
	for i, name := range commitPhaseNames {
		d := &c.phases[i]
		h := Histogram{count: d.count.Load(), sum: float64(d.sum.Load()), min: d.min.Load(), max: d.max.Load()}
		buckets := make([]HistogramBucket, 0)
		for j := range h.buckets {
			h.buckets[j] = d.histogram[j].Load()
			if h.buckets[j] != 0 {
				buckets = append(buckets, HistogramBucket{LowerNs: histogramValue(j), Count: h.buckets[j]})
			}
		}
		if h.buckets[0] != 0 {
			h.min = 0
		}
		out.PhaseHistograms[name] = PhaseHistogram{Stats: h.Stats(), Buckets: buckets}
	}
	b, a := &c.before.gc, &c.after.gc
	out.Runtime = RuntimeDiagnostics{
		GCCountDelta: a.NumGC - b.NumGC, GCPauseTotalNs: int64(a.PauseTotal - b.PauseTotal),
		GCPauses: make([]TimeGap, 0), GCHistoryLost: a.NumGC-b.NumGC > int64(len(a.Pause)),
		SchedLatencies: metricDelta(c.before.samples[0].Value, c.after.samples[0].Value),
		GCTotalPauses:  metricDelta(c.before.samples[1].Value, c.after.samples[1].Value), SnapshotEndNs: snapshotEnd,
	}
	for i, end := range a.PauseEnd {
		offset := end.Sub(wallStart).Nanoseconds()
		if offset >= 0 && offset < c.windowNs {
			out.Runtime.GCPauses = append(out.Runtime.GCPauses, TimeGap{StartNs: offset - int64(a.Pause[i]), LengthNs: int64(a.Pause[i])})
		}
	}
	return out
}

var _ lsmengine.CommitTraceObserver = (*stallCollector)(nil)
