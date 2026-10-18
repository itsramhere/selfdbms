package harness

import (
	"math"
	"sort"
	"sync"
	"time"
)

// LatencyStats encapsulates summary statistics for operation latencies.
type LatencyStats struct {
	Count         int64   `json:"sample_count"`
	ZeroCount     int64   `json:"zero_sample_count"`
	OverflowCount int64   `json:"overflow_count"`
	MinNanos      int64   `json:"min"`
	MaxNanos      int64   `json:"max"`
	MeanNanos     float64 `json:"mean"`
	P50Nanos      int64   `json:"p50"`
	P95Nanos      int64   `json:"p95"`
	P99Nanos      int64   `json:"p99"`
	P999Nanos     *int64  `json:"p999"`
}

// LatencyRecorder collects individual operation latencies with thread-safety and high precision.
type LatencyRecorder struct {
	mu            sync.Mutex
	samples       []int64
	maxCap        int
	zeroCount     int64
	overflowCount int64
}

// NewLatencyRecorder creates a recorder with pre-allocated capacity to avoid GC churn during measurement.
func NewLatencyRecorder(capacity int) *LatencyRecorder {
	if capacity <= 0 {
		capacity = 500000
	}
	return &LatencyRecorder{
		samples: make([]int64, 0, capacity),
		maxCap:  capacity,
	}
}

// Record stores a single operation duration from time.Duration.
func (lr *LatencyRecorder) Record(d time.Duration) {
	lr.RecordNanos(d.Nanoseconds())
}

// RecordNanos stores a single operation duration in nanoseconds directly.
func (lr *LatencyRecorder) RecordNanos(nanos int64) {
	if nanos < 0 {
		nanos = 0
	}
	lr.mu.Lock()
	if nanos == 0 {
		lr.zeroCount++
	}
	if len(lr.samples) < lr.maxCap {
		lr.samples = append(lr.samples, nanos)
	} else {
		lr.overflowCount++
	}
	lr.mu.Unlock()
}

// Snapshot computes summary statistics across all collected samples.
func (lr *LatencyRecorder) Snapshot() LatencyStats {
	lr.mu.Lock()
	n := len(lr.samples)
	zeroCnt := lr.zeroCount
	overflowCnt := lr.overflowCount
	if n == 0 {
		lr.mu.Unlock()
		return LatencyStats{
			ZeroCount:     zeroCnt,
			OverflowCount: overflowCnt,
		}
	}
	sorted := make([]int64, n)
	copy(sorted, lr.samples)
	lr.mu.Unlock()

	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, val := range sorted {
		sum += val
	}

	// Nearest-rank percentile: rank = ceil(p * n), index = rank - 1
	calcPercentile := func(p float64) int64 {
		rank := int(math.Ceil(p * float64(n)))
		idx := rank - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		return sorted[idx]
	}

	p50 := calcPercentile(0.50)
	p95 := calcPercentile(0.95)
	p99 := calcPercentile(0.99)

	var p999 *int64
	if n >= 1000 {
		val := calcPercentile(0.999)
		p999 = &val
	}

	return LatencyStats{
		Count:         int64(n),
		ZeroCount:     zeroCnt,
		OverflowCount: overflowCnt,
		MinNanos:      sorted[0],
		MaxNanos:      sorted[n-1],
		MeanNanos:     float64(sum) / float64(n),
		P50Nanos:      p50,
		P95Nanos:      p95,
		P99Nanos:      p99,
		P999Nanos:     p999,
	}
}
