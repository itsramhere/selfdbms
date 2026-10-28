package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lookupPath walks a decoded JSON object and fails if any segment is missing or
// null.
func lookupPath(t *testing.T, root map[string]any, path string) any {
	t.Helper()
	var cur any = root
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %q: %q is not a JSON object", path, part)
		}
		next, ok := obj[part]
		if !ok {
			t.Fatalf("required envelope path %q is missing (at %q)", path, part)
		}
		if next == nil {
			t.Fatalf("required envelope path %q is null", path)
		}
		cur = next
	}
	return cur
}

func sampleEnvelope() *Envelope {
	return &Envelope{
		Schema:  schemaTrial,
		TrialID: "I7-E7-Y1-SELFTEST",
		Block:   blockE7,
		Cell:    cellY1,
		Arm:     armPebble,
		Runtime: RuntimeInfo{
			GoVersion:     "go1.26.3",
			GOS:           "windows",
			GOOS:          "windows",
			GOARCH:        "amd64",
			GOMAXPROCS:    8,
			NumCPU:        8,
			PebbleVersion: pebbleVersion,
			ExeSHA256:     "deadbeef",
		},
		Timing: TimingInfo{
			StartedUTC:  "2026-10-28T10:00:00Z",
			FinishedUTC: "2026-10-28T10:00:01Z",
			WallMS:      1000,
		},
		PebbleOptions: PebbleOptionsInfo{
			MemTableSizeBytes:              64 << 20,
			MemTableSizeSource:             "spec",
			CacheSizeBytes:                 cacheSizeBytes,
			CacheKind:                      "pebble.NewCache (DB block cache)",
			DisableAutomaticCompactions:    true,
			AutomaticCompactionsControlled: true,
			AutomaticCompactionsNote:       "test",
			Overrides:                      []string{"disable_automatic_compactions=true"},
		},
		Status:         statusSuccess,
		Error:          "",
		DataDirRemoved: true,
		Result: TrialResult{
			Operations: Operations{
				ReadOpsAttempted:     1000,
				ReadOpsCompleted:     1000,
				WriteOpsAttempted:    100,
				WriteOpsCompleted:    100,
				ErrorOpsCount:        0,
				ValidationErrorCount: 0,
			},
			Throughput: Throughput{
				ReadOpsPerSec:  1000,
				WriteOpsPerSec: 100,
				TotalOpsPerSec: 1100,
			},
			LatencyReadNanos: LatencyReadNanos{
				SampleCount:   1000,
				P50:           1000,
				P95:           2000,
				P99:           3000,
				P999:          4000,
				P95Supported:  true,
				P99Supported:  true,
				P999Supported: true,
			},
			LSMDiagnostics: LSMDiagnostics{
				MeasuredFlushes:       4,
				MeasuredCompactions:   2,
				FinalSSTables:         6,
				PebbleMetricsSnapshot: "flush.count=4 compact.count=2",
			},
			MeasurementMS: 400,
		},
		Provenance: Provenance{GitRev: "abc123", Gate: "G4"},
	}
}

// TestEnvelopeJSON asserts the serialized envelope exposes every contract
// field. It marshals a sample envelope and walks the decoded JSON.
func TestEnvelopeJSON(t *testing.T) {
	data, err := envelopeJSON(sampleEnvelope())
	if err != nil {
		t.Fatalf("envelopeJSON: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("envelope is not valid JSON: %v", err)
	}

	required := []string{
		"schema", "trial_id", "block", "cell", "arm", "status", "error", "data_dir_removed",
		"runtime.go_version", "runtime.gos", "runtime.goos", "runtime.goarch",
		"runtime.gomaxprocs", "runtime.num_cpu", "runtime.pebble_version", "runtime.exe_sha256",
		"timing.started_utc", "timing.finished_utc", "timing.wall_ms",
		"pebble_options.memtable_size_bytes", "pebble_options.memtable_size_source",
		"pebble_options.cache_size_bytes", "pebble_options.cache_kind",
		"pebble_options.disable_automatic_compactions",
		"pebble_options.automatic_compactions_controlled",
		"pebble_options.automatic_compactions_note",
		"result.operations.read_ops_attempted", "result.operations.read_ops_completed",
		"result.operations.write_ops_attempted", "result.operations.write_ops_completed",
		"result.operations.error_ops_count", "result.operations.validation_error_count",
		"result.throughput.read_ops_per_sec", "result.throughput.write_ops_per_sec",
		"result.throughput.total_ops_per_sec",
		"result.latency_read_nanos.sample_count", "result.latency_read_nanos.p50",
		"result.latency_read_nanos.p95", "result.latency_read_nanos.p99",
		"result.latency_read_nanos.p999", "result.latency_read_nanos.p95_supported",
		"result.latency_read_nanos.p99_supported", "result.latency_read_nanos.p999_supported",
		"result.lsm_diagnostics.measured_flushes",
		"result.lsm_diagnostics.measured_compactions",
		"result.lsm_diagnostics.final_sstables",
		"result.lsm_diagnostics.pebble_metrics_snapshot",
	}
	for _, path := range required {
		lookupPath(t, root, path)
	}

	if got := root["schema"]; got != schemaTrial {
		t.Fatalf("schema = %v, want %q", got, schemaTrial)
	}
	if got := root["block"]; got != blockE7 {
		t.Fatalf("block = %v, want %q", got, blockE7)
	}
	if got := root["status"]; got != statusSuccess {
		t.Fatalf("status = %v, want %q", got, statusSuccess)
	}
	if got := lookupPath(t, root, "result.latency_read_nanos.p95_supported"); got != true {
		t.Fatalf("p95_supported = %v, want true (must serialize even when boolean)", got)
	}
	// The booleans must still be present when false, not omitted.
	env := sampleEnvelope()
	env.Result.LatencyReadNanos = LatencyReadNanos{SampleCount: 5, P50: 42}
	data, err = envelopeJSON(env)
	if err != nil {
		t.Fatalf("envelopeJSON (unsupported percentiles): %v", err)
	}
	root = nil
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, path := range []string{
		"result.latency_read_nanos.p95_supported",
		"result.latency_read_nanos.p99_supported",
		"result.latency_read_nanos.p999_supported",
	} {
		if got := lookupPath(t, root, path); got != false {
			t.Fatalf("%s = %v, want false", path, got)
		}
	}
}

// TestAbsErr verifies the 5%-absent key generator never returns a key inside
// the populated keyspace.
func TestAbsErr(t *testing.T) {
	for _, cardinality := range []int{1, 2, 200, 10000} {
		r := rand.New(rand.NewSource(int64(cardinality)*7919 + 13))
		for i := 0; i < 20000; i++ {
			idx := absentKeyIndex(r, cardinality)
			if idx < cardinality {
				t.Fatalf("cardinality %d: absent index %d is inside [0,%d)", cardinality, idx, cardinality)
			}
			if idx <= cardinality {
				t.Fatalf("cardinality %d: absent index %d must be >= %d", cardinality, idx, cardinality+1)
			}

			key := string(absentKey(r, cardinality))
			if !strings.HasPrefix(key, "user_key_") {
				t.Fatalf("absent key %q does not use the user_key_ prefix", key)
			}
			var parsed int
			if _, err := fmt.Sscanf(key, "user_key_%d", &parsed); err != nil {
				t.Fatalf("cardinality %d: cannot parse absent key %q: %v", cardinality, key, err)
			}
			if parsed < cardinality {
				t.Fatalf("cardinality %d: absent key %q has index %d inside the keyspace", cardinality, key, parsed)
			}
		}
	}
}

// TestApprovalParser is a table test for the approval gate.
func TestApprovalParser(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	good := `{"campaign_id":"I7_STRENGTHENING_V1","gate":"G4","approved":true,` +
		`"protocol_version":"1.2.0-windows","approved_by":"selftest"}`

	tests := []struct {
		name    string
		path    string
		gate    string
		wantErr bool
	}{
		{
			name:    "missing file",
			path:    filepath.Join(dir, "does-not-exist.json"),
			gate:    "G4",
			wantErr: true,
		},
		{
			name:    "approved=false",
			path:    write("not-approved.json", `{"campaign_id":"I7_STRENGTHENING_V1","gate":"G4","approved":false,"protocol_version":"1.2.0-windows"}`),
			gate:    "G4",
			wantErr: true,
		},
		{
			name:    "wrong gate",
			path:    write("wrong-gate.json", `{"campaign_id":"I7_STRENGTHENING_V1","gate":"G3","approved":true,"protocol_version":"1.2.0-windows"}`),
			gate:    "G4",
			wantErr: true,
		},
		{
			name:    "wrong campaign",
			path:    write("wrong-campaign.json", `{"campaign_id":"SOMETHING_ELSE","gate":"G4","approved":true,"protocol_version":"1.2.0-windows"}`),
			gate:    "G4",
			wantErr: true,
		},
		{
			name:    "wrong protocol version",
			path:    write("wrong-protocol.json", `{"campaign_id":"I7_STRENGTHENING_V1","gate":"G4","approved":true,"protocol_version":"1.0.0"}`),
			gate:    "G4",
			wantErr: true,
		},
		{
			name:    "malformed json",
			path:    write("malformed.json", `{not json`),
			gate:    "G4",
			wantErr: true,
		},
		{
			name:    "correct",
			path:    write("good.json", good),
			gate:    "G4",
			wantErr: false,
		},
		{
			name:    "protocol omitted is tolerated",
			path:    write("no-protocol.json", `{"campaign_id":"I7_STRENGTHENING_V1","gate":"G4","approved":true}`),
			gate:    "G4",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := approvalCheck(tc.path, tc.gate)
			if tc.wantErr && err == nil {
				t.Fatalf("approvalCheck(%q, %q) = nil, want error", tc.path, tc.gate)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("approvalCheck(%q, %q) = %v, want nil", tc.path, tc.gate, err)
			}
		})
	}
}

// TestPercentile checks nearest-rank correctness and the sample-count support
// rules (p95 >= 20, p99 >= 100, p999 >= 1000 samples).
func TestPercentile(t *testing.T) {
	sorted := make([]int64, 1000)
	for i := range sorted {
		sorted[i] = int64(i + 1)
	}
	cases := []struct {
		permille int
		want     int64
	}{
		{500, 500},
		{950, 950},
		{990, 990},
		{999, 999},
	}
	for _, tc := range cases {
		if got := percentile(sorted, tc.permille); got != tc.want {
			t.Fatalf("percentile(1..1000, %d permille) = %d, want %d", tc.permille, got, tc.want)
		}
	}

	// Empty input.
	if got := summarizeLatency(nil); got.SampleCount != 0 || got.P50 != 0 ||
		got.P95Supported || got.P99Supported || got.P999Supported ||
		got.P95 != 0 || got.P99 != 0 || got.P999 != 0 {
		t.Fatalf("summarizeLatency(nil) = %+v, want zero value with unsupported percentiles", got)
	}

	// Unsorted input: p50 of 5 values is the 3rd smallest.
	got := summarizeLatency([]int64{5, 1, 4, 2, 3})
	if got.SampleCount != 5 || got.P50 != 3 {
		t.Fatalf("summarizeLatency unsorted = %+v, want SampleCount=5 P50=3", got)
	}
	if got.P95Supported || got.P99Supported || got.P999Supported {
		t.Fatalf("small sample unexpectedly supports high percentiles: %+v", got)
	}

	// 19 samples: p95 unsupported and reported as zero.
	mk := func(n int) []int64 {
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(i + 1)
		}
		return out
	}
	got = summarizeLatency(mk(19))
	if got.P95Supported || got.P95 != 0 {
		t.Fatalf("n=19: p95 = %d supported=%v, want 0/false", got.P95, got.P95Supported)
	}
	if got.P50 != 10 {
		t.Fatalf("n=19: p50 = %d, want 10", got.P50)
	}

	// 20 samples: p95 supported, nearest-rank ceil(0.95*20)=19 -> 19.
	got = summarizeLatency(mk(20))
	if !got.P95Supported || got.P95 != 19 {
		t.Fatalf("n=20: p95 = %d supported=%v, want 19/true", got.P95, got.P95Supported)
	}

	// 100 samples: p99 supported, ceil(0.99*100)=99 -> 99.
	got = summarizeLatency(mk(100))
	if !got.P99Supported || got.P99 != 99 {
		t.Fatalf("n=100: p99 = %d supported=%v, want 99/true", got.P99, got.P99Supported)
	}
	if got.P999Supported || got.P999 != 0 {
		t.Fatalf("n=100: p999 = %d supported=%v, want 0/false", got.P999, got.P999Supported)
	}

	// 999 samples: p999 unsupported.
	got = summarizeLatency(mk(999))
	if got.P999Supported || got.P999 != 0 {
		t.Fatalf("n=999: p999 = %d supported=%v, want 0/false", got.P999, got.P999Supported)
	}

	// 1000 samples: p999 supported, ceil(0.999*1000)=999 -> 999.
	got = summarizeLatency(mk(1000))
	if !got.P999Supported || got.P999 != 999 {
		t.Fatalf("n=1000: p999 = %d supported=%v, want 999/true", got.P999, got.P999Supported)
	}
}
