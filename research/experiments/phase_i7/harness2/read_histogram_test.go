package harness2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPersistReadHistogram(t *testing.T) {
	a, b := NewHistogramShard(), NewHistogramShard()
	a.ServiceHistogram.Record(45000)
	b.ServiceHistogram.Record(1600000)
	a.ServiceLateHistogram.Record(9000000)
	buckets := mergeReadHistogramBuckets([]*Shard{a, b})
	var count int64
	for i, bucket := range buckets {
		if bucket.LowerNs != histogramValue(i) {
			t.Fatal("bucket boundary")
		}
		count += bucket.Count
	}
	if len(buckets) == 0 || count != 2 {
		t.Fatal("measured-window buckets", count)
	}
	r := TrialResult{}
	before, _ := json.Marshal(r)
	if strings.Contains(string(before), "read_latency_histogram") {
		t.Fatal("default output changed")
	}
	r.ReadLatencyHistogram = buckets
	after, _ := json.Marshal(r)
	if !strings.Contains(string(after), "read_latency_histogram") {
		t.Fatal("missing persisted histogram")
	}
	if len(mergeReadHistogramBuckets(nil)) == 0 {
		t.Fatal("empty window must remain explicit")
	}
	for _, block := range []string{"E1", "E2", "SMOKE", "X1", "X2", "X3", "X4"} {
		s := Spec{Block: block, PersistReadHistogram: true, WalSync: true, PublicationMode: "none", WriterConcurrency: 1, PacedWriteRatePerSec: 183}
		_, err := s.ResolveMode()
		if s.IsX() == (err != nil) {
			t.Fatalf("block %s: %v", block, err)
		}
	}
	s, _ := json.Marshal(Spec{})
	if strings.Contains(string(s), "persist_read_histogram") {
		t.Fatal("default spec changed")
	}
}
