package harness

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
)

// WorkloadID identifies one of the five formal evaluation workloads.
type WorkloadID string

const (
	WorkloadA_ReadOnly                     WorkloadID = "WORKLOAD_A_READ_ONLY"
	WorkloadB_MixedFlush                   WorkloadID = "WORKLOAD_B_MIXED_FLUSH" // Legacy alias
	WorkloadB1_NaturalWriteProgress        WorkloadID = "WORKLOAD_B1_NATURAL_WRITE_PROGRESS"
	WorkloadB2_ControlledFlushInterference WorkloadID = "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE"
	WorkloadC_CompactionOverlap            WorkloadID = "WORKLOAD_C_COMPACTION_OVERLAP"
	WorkloadD_ReaderChurn                  WorkloadID = "WORKLOAD_D_READER_CHURN"
	WorkloadE_NegativeControlMemtable      WorkloadID = "WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE"
)

// FormatKey generates a fixed-width deterministic key string.
func FormatKey(idx int) string {
	return fmt.Sprintf("user_key_%010d", idx)
}

// FormatVal generates a deterministic value payload of the requested length.
func FormatVal(idx int, length int) string {
	prefix := fmt.Sprintf("val_%010d_", idx)
	if len(prefix) >= length {
		return prefix[:length]
	}
	padding := strings.Repeat("x", length-len(prefix))
	return prefix + padding
}

// KeySampler samples keys according to either uniform random or Zipfian skewed distribution.
type KeySampler struct {
	rnd          *rand.Rand
	cardinality  int
	isZipf       bool
	hitRatio     float64
	zipfCdf      []float64
}

// NewKeySampler constructs a deterministic key sampler.
func NewKeySampler(seed int64, cardinality int, hitRatio float64, distribution string, s float64) *KeySampler {
	ks := &KeySampler{
		rnd:         rand.New(rand.NewSource(seed)),
		cardinality: cardinality,
		hitRatio:    hitRatio,
		isZipf:      distribution == "zipfian_skewed",
	}

	if ks.isZipf && cardinality > 0 {
		ks.zipfCdf = make([]float64, cardinality)
		var sum float64
		for i := 1; i <= cardinality; i++ {
			sum += 1.0 / math.Pow(float64(i), s)
			ks.zipfCdf[i-1] = sum
		}
		for i := 0; i < cardinality; i++ {
			ks.zipfCdf[i] /= sum
		}
	}

	return ks
}

// SampleKey returns a key according to the configured hit ratio and distribution.
func (ks *KeySampler) SampleKey() (string, bool) {
	// Miss injection: return a key outside the known keyspace
	if ks.rnd.Float64() > ks.hitRatio {
		missIdx := ks.cardinality + ks.rnd.Intn(10000) + 1
		return FormatKey(missIdx), false
	}

	if !ks.isZipf || len(ks.zipfCdf) == 0 {
		idx := ks.rnd.Intn(ks.cardinality)
		return FormatKey(idx), true
	}

	// Binary search in Zipfian CDF
	u := ks.rnd.Float64()
	low, high := 0, len(ks.zipfCdf)-1
	idx := high
	for low <= high {
		mid := (low + high) / 2
		if ks.zipfCdf[mid] >= u {
			idx = mid
			high = mid - 1
		} else {
			low = mid + 1
		}
	}
	return FormatKey(idx), true
}
