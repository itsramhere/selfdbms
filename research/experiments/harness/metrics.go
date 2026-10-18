package harness

import (
	"math"
	"sort"
	"sync"
	"time"
)

// LatencyStats encapsulates summary statistics for operation latencies.
type LatencyStats struct {
	Count       int64   `json:"sample_count"`
	MinNanos    int64   `json:"min"`
	MaxNanos    int64   `json:"max"`
	MeanNanos   float64 `json:"mean"`
	P50Nanos    int64   `json:"p50"`
	P95Nanos    int64   `json:"p95"`
	P99Nanos    int64   `json:"p99"`
	P999Nanos   int64   `json:"p999"`
}

// LatencyRecorder collects individual operation latencies with thread-safety and nanosecond precision.
type LatencyRecorder struct {
	mu      sync.Mutex
	samples []int64
	maxCap  int
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

// Record stores a single operation duration in nanoseconds.
func (lr *LatencyRecorder) Record(d time.Duration) {
	nanos := d.Nanoseconds()
	lr.mu.Lock()
	if len(lr.samples) < lr.maxCap {
		lr.samples = append(lr.samples, nanos)
	}
	lr.mu.Unlock()
}

// Snapshot computes summary statistics across all collected samples.
func (lr *LatencyRecorder) Snapshot() LatencyStats {
	lr.mu.Lock()
	n := len(lr.samples)
	if n == 0 {
		lr.mu.Unlock()
		return LatencyStats{}
	}
	sorted := make([]int64, n)
	copy(sorted, lr.samples)
	lr.mu.Unlock()

	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, val := range sorted {
		sum += val
	}

	p50Idx := int(math.Floor(float64(n) * 0.50))
	p95Idx := int(math.Floor(float64(n) * 0.95))
	p99Idx := int(math.Floor(float64(n) * 0.99))
	p999Idx := int(math.Floor(float64(n) * 0.999))

	if p50Idx >= n {
		p50Idx = n - 1
	}
	if p95Idx >= n {
		p95Idx = n - 1
	}
	if p99Idx >= n {
		p99Idx = n - 1
	}
	if p999Idx >= n {
		p999Idx = n - 1
	}

	return LatencyStats{
		Count:     int64(n),
		MinNanos:  sorted[0],
		MaxNanos:  sorted[n-1],
		MeanNanos: float64(sum) / float64(n),
		P50Nanos:  sorted[p50Idx],
		P95Nanos:  sorted[p95Idx],
		P99Nanos:  sorted[p99Idx],
		P999Nanos: sorted[p999Idx],
	}
}
