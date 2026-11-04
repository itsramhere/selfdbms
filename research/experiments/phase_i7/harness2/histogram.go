package harness2

import (
	"math"
	"math/bits"
)

// Histogram uses 128 subdivisions per power of two, giving bucket width <0.8%
// over 1 ns..120 s. Small values are exact. Storage is fixed and Record has no
// allocation, locks or atomics; each measurement worker owns its histogram.
// Values outside the design range retain exact min/max/mean but saturate the
// percentile bucket. The process timeout bounds campaign observations to 120 s.
const histogramBuckets = 4096
const histogramMax = int64(120_000_000_000)

type Histogram struct {
	buckets  [histogramBuckets]int64
	count    int64
	sum      float64
	min, max int64
}

func histogramIndex(v int64) int {
	if v < 0 {
		v = 0
	}
	if v > histogramMax {
		v = histogramMax
	}
	shift := bits.Len64(uint64(v)) - 8
	if shift < 0 {
		shift = 0
	}
	return shift*128 + int(v>>shift)
}

func histogramValue(i int) int64 {
	if i < 128 {
		return int64(i)
	}
	return int64(128+i%128) << (i/128 - 1)
}

func (h *Histogram) Record(v int64) {
	if v < 0 {
		v = 0
	}
	h.buckets[histogramIndex(v)]++
	if h.count == 0 || v < h.min {
		h.min = v
	}
	if v > h.max {
		h.max = v
	}
	h.count++
	h.sum += float64(v)
}

func (h *Histogram) Merge(other *Histogram) {
	if other == nil || other.count == 0 {
		return
	}
	for i, n := range other.buckets {
		h.buckets[i] += n
	}
	if h.count == 0 || other.min < h.min {
		h.min = other.min
	}
	if other.max > h.max {
		h.max = other.max
	}
	h.count += other.count
	h.sum += other.sum
}

func (h *Histogram) percentile(p float64) int64 {
	if h.count == 0 {
		return 0
	}
	rank := int64(math.Ceil(float64(h.count) * p))
	var n int64
	for i, v := range h.buckets {
		n += v
		if n >= rank {
			return histogramValue(i)
		}
	}
	return h.max
}

func (h *Histogram) Stats() LatencyStats {
	st := LatencyStats{Count: h.count, MinNanos: h.min, MaxNanos: h.max}
	if h.count == 0 {
		return st
	}
	st.MeanNanos = h.sum / float64(h.count)
	st.P50Nanos = h.percentile(.5)
	if h.count >= 20 {
		v := h.percentile(.95)
		st.P95Nanos = &v
	}
	if h.count >= 100 {
		v := h.percentile(.99)
		st.P99Nanos = &v
	}
	if h.count >= 1000 {
		v := h.percentile(.999)
		st.P999Nanos = &v
	}
	return st
}

func MergeHistograms(shards []*Shard, late bool) LatencyStats {
	var h Histogram
	for _, s := range shards {
		if late {
			h.Merge(s.ServiceLateHistogram)
		} else {
			h.Merge(s.ServiceHistogram)
		}
	}
	return h.Stats()
}

// Export only after workers stop. The read path and its existing histogram
// recording are unchanged. Persist all buckets (including zeros) so an empty
// measured window still has an explicit, nonempty histogram.
func mergeReadHistogramBuckets(shards []*Shard) []HistogramBucket {
	var h Histogram
	for _, s := range shards {
		h.Merge(s.ServiceHistogram)
	}
	buckets := make([]HistogramBucket, histogramBuckets)
	for i, n := range h.buckets {
		buckets[i] = HistogramBucket{LowerNs: histogramValue(i), Count: n}
	}
	return buckets
}
