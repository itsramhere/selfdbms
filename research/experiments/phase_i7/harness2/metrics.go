package harness2

// Per-worker metrics (audit findings IA-1/IA-2).
//
// Each worker owns one Shard. Nothing on the read path takes a lock or an atomic
// beyond the engine's own counters; the shards are merged once after the workers
// stop, with the happens-before edge supplied by sync.WaitGroup.

import (
	"math"
	"sort"
)

// Shard accumulates one worker's observations.
type Shard struct {
	Samples []int64 // service latency (completion - issue)
	Resp    []int64 // response latency (completion - intended); open loop only

	Issues      []Issue // interval pairs for event conditioning

	ReadsAttempted int64
	ReadsCompleted int64
	ReadsHit       int64
	ReadsMiss      int64
	ReadsError     int64
	WritesAttempted int64
	WritesCompleted int64
	WritesError     int64
	ValidationErrors int64
	Issued          int64
	Abandoned       int64
	MaxLatenessNs   int64
	Dropped         int64
}

// Issue records one request interval for event-conditioned latency analysis.
// IntendedNs is the open-loop schedule time (equal to IssuedNs in closed loop).
type Issue struct {
	IntendedNs int64
	IssuedNs   int64
	CompleteNs int64
}

// ResponseNs is the coordinated-omission-correct latency.
func (i Issue) ResponseNs() int64 { return i.CompleteNs - i.IntendedNs }

// NewShard allocates a shard sized for the expected operation count.
func NewShard(capacity int, trackResp, trackIssues bool) *Shard {
	if capacity < 16 {
		capacity = 16
	}
	s := &Shard{
		Samples: make([]int64, 0, capacity),
	}
	if trackResp {
		s.Resp = make([]int64, 0, capacity)
	}
	if trackIssues {
		s.Issues = make([]Issue, 0, capacity)
	}
	return s
}

// RecordService appends a service-latency sample. Past capacity the sample is
// dropped and counted, never silently truncated into a biased "first N" set.
func (s *Shard) RecordService(ns int64) {
	if ns < 0 {
		ns = 0
	}
	if len(s.Samples) < cap(s.Samples) {
		s.Samples = append(s.Samples, ns)
	} else {
		s.Dropped++
	}
}

// RecordResponse appends a response-latency sample.
func (s *Shard) RecordResponse(ns int64) {
	if ns < 0 {
		ns = 0
	}
	if len(s.Resp) < cap(s.Resp) {
		s.Resp = append(s.Resp, ns)
	}
}

// RecordIssue appends an interval triple.
func (s *Shard) RecordIssue(intended, issued, complete int64) {
	if len(s.Issues) < cap(s.Issues) {
		s.Issues = append(s.Issues, Issue{IntendedNs: intended, IssuedNs: issued, CompleteNs: complete})
	}
}

// Merge concatenates the shards' samples without assuming equal lengths.
func MergeSamples(shards []*Shard) []int64 {
	n := 0
	for _, s := range shards {
		n += len(s.Samples)
	}
	out := make([]int64, 0, n)
	for _, s := range shards {
		out = append(out, s.Samples...)
	}
	return out
}

// MergeResponses concatenates response-latency samples.
func MergeResponses(shards []*Shard) []int64 {
	n := 0
	for _, s := range shards {
		n += len(s.Resp)
	}
	out := make([]int64, 0, n)
	for _, s := range shards {
		out = append(out, s.Resp...)
	}
	return out
}

// MergeIssues concatenates issue intervals.
func MergeIssues(shards []*Shard) []Issue {
	n := 0
	for _, s := range shards {
		n += len(s.Issues)
	}
	out := make([]Issue, 0, n)
	for _, s := range shards {
		out = append(out, s.Issues...)
	}
	return out
}

// Percentile computes a nearest-rank percentile over an unsorted copy.
// Sufficiency rules match the frozen schema: p95 needs >=20 samples, p99 >=100,
// p999 >=1000; unsupported percentiles are returned as nil.
func Stats(samples []int64) LatencyStats {
	st := LatencyStats{Count: int64(len(samples))}
	if len(samples) == 0 {
		return st
	}
	sorted := make([]int64, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, v := range sorted {
		sum += v
	}
	st.MinNanos = sorted[0]
	st.MaxNanos = sorted[len(sorted)-1]
	st.MeanNanos = float64(sum) / float64(len(sorted))

	at := func(p float64) int64 {
		rank := int(math.Ceil(p * float64(len(sorted))))
		idx := rank - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	st.P50Nanos = at(0.50)
	if n := len(sorted); n >= 20 {
		v := at(0.95)
		st.P95Nanos = &v
	}
	if n := len(sorted); n >= 100 {
		v := at(0.99)
		st.P99Nanos = &v
	}
	if n := len(sorted); n >= 1000 {
		v := at(0.999)
		st.P999Nanos = &v
	}
	return st
}

// Totals aggregates the per-worker counters.
type Totals struct {
	ReadsAttempted  int64
	ReadsCompleted  int64
	ReadsHit        int64
	ReadsMiss       int64
	ReadsError      int64
	WritesAttempted int64
	WritesCompleted int64
	WritesError     int64
	ValidationErrors int64
	Issued          int64
	Abandoned       int64
	MaxLatenessNs   int64
	Dropped         int64
}

// Sum folds the shard counters.
func Sum(shards []*Shard) Totals {
	var t Totals
	for _, s := range shards {
		t.ReadsAttempted += s.ReadsAttempted
		t.ReadsCompleted += s.ReadsCompleted
		t.ReadsHit += s.ReadsHit
		t.ReadsMiss += s.ReadsMiss
		t.ReadsError += s.ReadsError
		t.WritesAttempted += s.WritesAttempted
		t.WritesCompleted += s.WritesCompleted
		t.WritesError += s.WritesError
		t.ValidationErrors += s.ValidationErrors
		t.Issued += s.Issued
		t.Abandoned += s.Abandoned
		if s.MaxLatenessNs > t.MaxLatenessNs {
			t.MaxLatenessNs = s.MaxLatenessNs
		}
		t.Dropped += s.Dropped
	}
	return t
}
