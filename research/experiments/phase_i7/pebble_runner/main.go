// Command pebble_runner executes exactly ONE descriptive Pebble trial
// described by a JSON spec and writes exactly ONE JSON envelope.
//
// It is an external anchor for block E7 of the Windows-native benchmark. It is
// deliberately descriptive: it measures one configuration at a time and MUST
// NOT be used for paired comparisons.
//
// Usage:
//
//	pebble_runner.exe -spec <spec.json> -out <out.json> \
//	    -platform <platform_manifest.json> -git-rev <sha> \
//	    -gate G4 -approval <APPROVAL.json>
//
// Exit codes:
//
//	0 success
//	1 trial failed
//	2 usage error
//	3 unsupported
//	4 not approved
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	exitSuccess     = 0
	exitTrialFailed = 1
	exitUsageError  = 2
	exitUnsupported = 3
	exitNotApproved = 4

	schemaTrial = "i7.trial.v1"
	schemaSpec  = "i7.trial-spec.v1"

	blockE7        = "E7"
	armPebble      = "PEBBLE"
	cellY1         = "Y1"
	cellY2         = "Y2"
	cellY3         = "Y3"
	campaignID     = "I7_STRENGTHENING_V1"
	protocolV120   = "1.2.0-windows"
	pebbleVersion  = "v1.1.5"
	valueLen       = 128
	defaultHitRate = 0.95

	defaultMemTableSize = 64 << 20
	cacheSizeBytes      = 64 << 20
	y3MemTableSize      = 16384

	statusSuccess = "SUCCESS"
	statusFailed  = "FAILED"
)

// TrialSpec is the i7.trial-spec.v1 document consumed by the runner.
type TrialSpec struct {
	Schema                 string  `json:"schema"`
	TrialID                string  `json:"trial_id"`
	Block                  string  `json:"block"`
	Cell                   string  `json:"cell"`
	Arm                    string  `json:"arm"`
	Seed                   int64   `json:"seed"`
	ReaderConcurrency      int     `json:"reader_concurrency"`
	WriterConcurrency      int     `json:"writer_concurrency"`
	MeasurementDurationMS  int     `json:"measurement_duration_ms"`
	WarmupDurationMS       int     `json:"warmup_duration_ms"`
	CooldownDurationMS     int     `json:"cooldown_duration_ms"`
	KeyspaceCardinality    int     `json:"keyspace_cardinality"`
	InitialSSTableCount    int     `json:"initial_sstable_count"`
	InitialKeysPerSSTable  int     `json:"initial_keys_per_sstable"`
	TargetReadHitRatio     float64 `json:"target_read_hit_ratio"`
	MemTableThresholdBytes int64   `json:"memtable_threshold_bytes"`
	DataDir                string  `json:"data_dir"`
}

// hitRatio returns the configured read-hit ratio, defaulting to 0.95 when the
// spec leaves it unset.
func (s *TrialSpec) hitRatio() float64 {
	if s.TargetReadHitRatio <= 0 || s.TargetReadHitRatio > 1 {
		return defaultHitRate
	}
	return s.TargetReadHitRatio
}

// memTableSize returns the effective MemTableSize and where it came from.
func (s *TrialSpec) memTableSize() (uint64, string) {
	if s.Cell == cellY3 {
		return y3MemTableSize, "cell_y3_override"
	}
	if s.MemTableThresholdBytes > 0 {
		return uint64(s.MemTableThresholdBytes), "spec"
	}
	return defaultMemTableSize, "default"
}

// validate rejects unusable specs. Field-specific validation happens after the
// block/arm checks so that unsupported blocks keep exit code 3.
func (s *TrialSpec) validate() error {
	if strings.TrimSpace(s.TrialID) == "" {
		return errors.New("trial_id must be non-empty")
	}
	switch s.Cell {
	case cellY1, cellY2, cellY3:
	default:
		return fmt.Errorf("cell %q is not one of %q, %q, %q", s.Cell, cellY1, cellY2, cellY3)
	}
	if s.ReaderConcurrency < 1 {
		return fmt.Errorf("reader_concurrency must be >= 1, got %d", s.ReaderConcurrency)
	}
	if s.WriterConcurrency < 0 {
		return fmt.Errorf("writer_concurrency must be >= 0, got %d", s.WriterConcurrency)
	}
	if s.MeasurementDurationMS <= 0 {
		return fmt.Errorf("measurement_duration_ms must be > 0, got %d", s.MeasurementDurationMS)
	}
	if s.WarmupDurationMS < 0 {
		return fmt.Errorf("warmup_duration_ms must be >= 0, got %d", s.WarmupDurationMS)
	}
	if s.CooldownDurationMS < 0 {
		return fmt.Errorf("cooldown_duration_ms must be >= 0, got %d", s.CooldownDurationMS)
	}
	if s.KeyspaceCardinality < 1 {
		return fmt.Errorf("keyspace_cardinality must be >= 1, got %d", s.KeyspaceCardinality)
	}
	if s.InitialSSTableCount < 1 {
		return fmt.Errorf("initial_sstable_count must be >= 1, got %d", s.InitialSSTableCount)
	}
	if s.InitialKeysPerSSTable < 1 {
		return fmt.Errorf("initial_keys_per_sstable must be >= 1, got %d", s.InitialKeysPerSSTable)
	}
	if s.InitialSSTableCount > math.MaxInt/s.InitialKeysPerSSTable {
		return errors.New("initial_sstable_count * initial_keys_per_sstable overflows int")
	}
	if s.InitialSSTableCount*s.InitialKeysPerSSTable < s.KeyspaceCardinality {
		return fmt.Errorf("fixture geometry cannot hold keyspace: %d sstables * %d keys < %d keys",
			s.InitialSSTableCount, s.InitialKeysPerSSTable, s.KeyspaceCardinality)
	}
	if s.MemTableThresholdBytes < 0 {
		return fmt.Errorf("memtable_threshold_bytes must be >= 0, got %d", s.MemTableThresholdBytes)
	}
	if s.TargetReadHitRatio < 0 || s.TargetReadHitRatio > 1 {
		return fmt.Errorf("target_read_hit_ratio must be in [0,1], got %g", s.TargetReadHitRatio)
	}
	if strings.TrimSpace(s.DataDir) == "" {
		return errors.New("data_dir must be non-empty")
	}
	return nil
}

// approvalDoc is the synthetic approval file schema used by the benchmark.
type approvalDoc struct {
	CampaignID      string `json:"campaign_id"`
	Gate            string `json:"gate"`
	Approved        bool   `json:"approved"`
	ProtocolVersion string `json:"protocol_version"`
	ApprovedBy      string `json:"approved_by"`
}

func loadApproval(path string) (*approvalDoc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read approval %q: %w", path, err)
	}
	var a approvalDoc
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parse approval %q: %w", path, err)
	}
	return &a, nil
}

// satisfies enforces every approval predicate. A missing protocol_version is
// tolerated; a present but different one is not.
func (a *approvalDoc) satisfies(gate string) error {
	if a.CampaignID != campaignID {
		return fmt.Errorf("approval campaign_id %q != required %q", a.CampaignID, campaignID)
	}
	if !a.Approved {
		return errors.New("approval approved is not true")
	}
	if a.Gate != gate {
		return fmt.Errorf("approval gate %q != -gate %q", a.Gate, gate)
	}
	if a.ProtocolVersion != "" && a.ProtocolVersion != protocolV120 {
		return fmt.Errorf("approval protocol_version %q != required %q", a.ProtocolVersion, protocolV120)
	}
	return nil
}

// approvalCheck is the combined load-and-validate helper used by tests.
func approvalCheck(path, gate string) error {
	a, err := loadApproval(path)
	if err != nil {
		return err
	}
	return a.satisfies(gate)
}

// prepareDataDir creates the directory if needed and requires it to be empty.
func prepareDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data_dir %q: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read data_dir %q: %w", dir, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("data_dir %q must be empty; found %d entries", dir, len(entries))
	}
	return nil
}

func removeDataDir(dir string) (bool, error) {
	if dir == "" {
		return false, errors.New("cannot remove empty data_dir path")
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	if _, err := os.Stat(dir); err == nil {
		return false, fmt.Errorf("data_dir %q still exists after RemoveAll", dir)
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

// userKey is the canonical key encoding for the benchmark.
func userKey(idx int) []byte {
	return fmt.Appendf(nil, "user_key_%010d", idx)
}

// userValue is the canonical 128-byte value for a key index.
func userValue(idx int) []byte {
	buf := make([]byte, 0, valueLen)
	return appendUserValue(buf, idx)
}

func appendUserValue(dst []byte, idx int) []byte {
	dst = dst[:0]
	dst = fmt.Appendf(dst, "val_%010d_", idx)
	for len(dst) < valueLen {
		dst = append(dst, 'x')
	}
	return dst
}

func appendUserKey(dst []byte, idx int) []byte {
	return fmt.Appendf(dst[:0], "user_key_%010d", idx)
}

// absentKeyIndex returns an index outside [0, keyspaceCardinality). The window
// is large enough that the absent-key distribution is effectively uniform and
// cannot collide with populated keys.
func absentKeyIndex(r *rand.Rand, keyspaceCardinality int) int {
	const absentWindow = 1 << 20
	return keyspaceCardinality + 1 + r.Intn(absentWindow)
}

func absentKey(r *rand.Rand, keyspaceCardinality int) []byte {
	return userKey(absentKeyIndex(r, keyspaceCardinality))
}

// derivedSeed derives an independent, deterministic per-goroutine PRNG seed
// from the trial seed, the role name, and the goroutine index.
func derivedSeed(seed int64, role string, index int) int64 {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%d", seed, role, index)
	sum := h.Sum(nil)
	return int64(binary.LittleEndian.Uint64(sum[:8]))
}

// percentile implements the nearest-rank method on an ascending slice.
// permille is the percentile in parts per thousand: 500 = p50, 950 = p95,
// 990 = p99, 999 = p999 (the 99.9th percentile). Integer arithmetic keeps the
// rank exact for every sample count.
func percentile(sorted []int64, permille int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int((int64(permille)*int64(len(sorted)) + 999) / 1000)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func summarizeLatency(samples []int64) LatencyReadNanos {
	out := LatencyReadNanos{SampleCount: len(samples)}
	if len(samples) == 0 {
		return out
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	out.P50 = percentile(sorted, 500)
	if len(sorted) >= 20 {
		out.P95 = percentile(sorted, 950)
		out.P95Supported = true
	}
	if len(sorted) >= 100 {
		out.P99 = percentile(sorted, 990)
		out.P99Supported = true
	}
	if len(sorted) >= 1000 {
		out.P999 = percentile(sorted, 999)
		out.P999Supported = true
	}
	return out
}

// RuntimeInfo records the toolchain and binary identity.
type RuntimeInfo struct {
	GoVersion     string `json:"go_version"`
	GOS           string `json:"gos"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	GOMAXPROCS    int    `json:"gomaxprocs"`
	NumCPU        int    `json:"num_cpu"`
	PebbleVersion string `json:"pebble_version"`
	ExeSHA256     string `json:"exe_sha256"`
}

type TimingInfo struct {
	StartedUTC  string `json:"started_utc"`
	FinishedUTC string `json:"finished_utc"`
	WallMS      int64  `json:"wall_ms"`
	CooldownMS  int    `json:"cooldown_ms"`
}

// PebbleOptionsInfo reports exactly what this run configured.
type PebbleOptionsInfo struct {
	MemTableSizeBytes              uint64   `json:"memtable_size_bytes"`
	MemTableSizeSource             string   `json:"memtable_size_source"`
	CacheSizeBytes                 int64    `json:"cache_size_bytes"`
	CacheKind                      string   `json:"cache_kind"`
	DisableAutomaticCompactions    bool     `json:"disable_automatic_compactions"`
	AutomaticCompactionsControlled bool     `json:"automatic_compactions_controlled"`
	AutomaticCompactionsNote       string   `json:"automatic_compactions_note"`
	Overrides                      []string `json:"overrides"`
}

type Operations struct {
	ReadOpsAttempted     int64 `json:"read_ops_attempted"`
	ReadOpsCompleted     int64 `json:"read_ops_completed"`
	WriteOpsAttempted    int64 `json:"write_ops_attempted"`
	WriteOpsCompleted    int64 `json:"write_ops_completed"`
	ErrorOpsCount        int64 `json:"error_ops_count"`
	ValidationErrorCount int64 `json:"validation_error_count"`
}

type Throughput struct {
	ReadOpsPerSec  float64 `json:"read_ops_per_sec"`
	WriteOpsPerSec float64 `json:"write_ops_per_sec"`
	TotalOpsPerSec float64 `json:"total_ops_per_sec"`
}

type LatencyReadNanos struct {
	SampleCount          int    `json:"sample_count"`
	P50                  int64  `json:"p50"`
	P95                  int64  `json:"p95"`
	P99                  int64  `json:"p99"`
	P999                 int64  `json:"p999"`
	P95Supported         bool   `json:"p95_supported"`
	P99Supported         bool   `json:"p99_supported"`
	P999Supported        bool   `json:"p999_supported"`
	SamplesDropped       int64  `json:"samples_dropped"`
	SampleCapacity       int    `json:"sample_capacity_per_reader"`
	SamplingNote         string `json:"sampling_note"`
	ClockSource          string `json:"clock_source"`
	ClockResolutionNanos int64  `json:"clock_resolution_nanos"`
}

type LSMDiagnostics struct {
	MeasuredFlushes       int64  `json:"measured_flushes"`
	MeasuredCompactions   int64  `json:"measured_compactions"`
	FinalSSTables         int    `json:"final_sstables"`
	FixtureSSTables       int    `json:"fixture_sstables"`
	BackingTableCount     int    `json:"backing_table_count"`
	PebbleMetricsSnapshot string `json:"pebble_metrics_snapshot"`
}

type TrialResult struct {
	Operations       Operations       `json:"operations"`
	Throughput       Throughput       `json:"throughput"`
	LatencyReadNanos LatencyReadNanos `json:"latency_read_nanos"`
	LSMDiagnostics   LSMDiagnostics   `json:"lsm_diagnostics"`
	ReadMisses       int64            `json:"read_misses"`
	Warmup           WarmupStats      `json:"warmup"`
	MeasurementMS    int64            `json:"measurement_window_ms"`
}

type WarmupStats struct {
	Attempted  int64 `json:"attempted"`
	Completed  int64 `json:"completed"`
	Errors     int64 `json:"errors"`
	DurationMS int64 `json:"duration_ms"`
}

type Provenance struct {
	GitRev                 string `json:"git_rev"`
	Gate                   string `json:"gate"`
	ApprovalPath           string `json:"approval_path,omitempty"`
	ApprovedBy             string `json:"approved_by,omitempty"`
	CampaignID             string `json:"campaign_id,omitempty"`
	ApprovalProtocol       string `json:"approval_protocol_version,omitempty"`
	PlatformManifestPath   string `json:"platform_manifest_path,omitempty"`
	PlatformManifestSHA256 string `json:"platform_manifest_sha256,omitempty"`
}

// Envelope is the i7.trial.v1 document written for every executed trial.
type Envelope struct {
	Schema         string            `json:"schema"`
	TrialID        string            `json:"trial_id"`
	Block          string            `json:"block"`
	Cell           string            `json:"cell"`
	Arm            string            `json:"arm"`
	Runtime        RuntimeInfo       `json:"runtime"`
	Timing         TimingInfo        `json:"timing"`
	PebbleOptions  PebbleOptionsInfo `json:"pebble_options"`
	Status         string            `json:"status"`
	Error          string            `json:"error"`
	DataDirRemoved bool              `json:"data_dir_removed"`
	Result         TrialResult       `json:"result"`
	Provenance     Provenance        `json:"provenance"`
	Spec           *TrialSpec        `json:"spec,omitempty"`
}

// envelopeJSON marshals an envelope; kept as a named helper so tests can
// assert the serialized contract without opening Pebble.
func envelopeJSON(env *Envelope) ([]byte, error) {
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// writeEnvelope writes atomically: temp file in the destination directory,
// sync, close, then rename over the final path.
func writeEnvelope(path string, env *Envelope) error {
	data, err := envelopeJSON(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".i7-trial-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp envelope in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp envelope: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp envelope: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp envelope: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename envelope into place: %w", err)
	}
	return nil
}

func executableSHA256() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func pebbleVersionString() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == "github.com/cockroachdb/pebble" {
				return dep.Version
			}
		}
	}
	return pebbleVersion
}

func newRuntimeInfo() RuntimeInfo {
	return RuntimeInfo{
		GoVersion:     runtime.Version(),
		GOS:           runtime.GOOS,
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		GOMAXPROCS:    runtime.GOMAXPROCS(0),
		NumCPU:        runtime.NumCPU(),
		PebbleVersion: pebbleVersionString(),
		ExeSHA256:     executableSHA256(),
	}
}

// pebbleOptionsFor builds the exact options used for the cell. The caller owns
// the returned cache reference and must call Unref after the DB is closed.
func pebbleOptionsFor(spec *TrialSpec) (*pebble.Options, PebbleOptionsInfo, *pebble.Cache) {
	cache := pebble.NewCache(cacheSizeBytes)
	opts := &pebble.Options{Cache: cache}
	size, source := spec.memTableSize()
	opts.MemTableSize = size

	info := PebbleOptionsInfo{
		MemTableSizeBytes:              size,
		MemTableSizeSource:             source,
		CacheSizeBytes:                 cacheSizeBytes,
		CacheKind:                      "pebble.NewCache (DB block cache)",
		AutomaticCompactionsControlled: true,
		Overrides:                      []string{},
	}
	switch spec.Cell {
	case cellY1, cellY2:
		// v1.1.5 has Options.DisableAutomaticCompactions, so Y1/Y2 really do
		// run without automatic compactions.
		opts.DisableAutomaticCompactions = true
		info.DisableAutomaticCompactions = true
		info.AutomaticCompactionsNote = "automatic compactions disabled via Options.DisableAutomaticCompactions (supported in pebble v1.1.5)"
		info.Overrides = append(info.Overrides, "disable_automatic_compactions=true")
	case cellY3:
		opts.DisableAutomaticCompactions = false
		info.DisableAutomaticCompactions = false
		info.AutomaticCompactionsNote = "automatic compactions left enabled; MemTableSize forced to 16384 bytes"
		info.Overrides = append(info.Overrides, "memtable_size_bytes=16384")
	}
	if source == "spec" {
		info.Overrides = append(info.Overrides, "memtable_size_bytes_from_spec")
	}
	return opts, info, cache
}

func metricsSnapshot(m *pebble.Metrics) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "flush.count=%d compact.count=%d table.backing=%d table.obsolete=%d levels=",
		m.Flush.Count, m.Compact.Count, m.Table.BackingTableCount, m.Table.ObsoleteCount)
	for i := 0; i < len(m.Levels); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "L%d:%d", i, m.Levels[i].NumFiles)
	}
	fmt.Fprintf(&b, " memtable.count=%d uptime_ms=%d", m.MemTable.Count, m.Uptime.Milliseconds())
	return b.String()
}

// sstableCount sums the per-level file counts. Table.BackingTableCount is not
// usable here: on a fresh v1.1.5 DB it reported 0 even while L0 held 2 files.
func sstableCount(m *pebble.Metrics) int {
	if m == nil {
		return 0
	}
	var total int64
	for i := 0; i < len(m.Levels); i++ {
		total += m.Levels[i].NumFiles
	}
	return int(total)
}

// opCounter accumulates one goroutine's operations; merging happens after the
// measurement window, never on the hot path.
type opCounter struct {
	attempted int64
	completed int64
	errors    int64
	firstErr  string
}

func (c *opCounter) setErr(format string, args ...any) {
	if c.firstErr == "" {
		c.firstErr = fmt.Sprintf(format, args...)
	}
}

func mergeOpCounter(dst, src *opCounter) {
	dst.attempted += src.attempted
	dst.completed += src.completed
	dst.errors += src.errors
	if dst.firstErr == "" {
		dst.firstErr = src.firstErr
	}
}

// writeFixture populates the keyspace in initial_sstable_count flush-delimited
// batches, using Sync:true for every write.
func writeFixture(db *pebble.DB, spec *TrialSpec) error {
	wo := &pebble.WriteOptions{Sync: true}
	written := 0
	for batch := 0; batch < spec.InitialSSTableCount && written < spec.KeyspaceCardinality; batch++ {
		n := spec.InitialKeysPerSSTable
		if remaining := spec.KeyspaceCardinality - written; n > remaining {
			n = remaining
		}
		for j := 0; j < n; j++ {
			idx := written + j
			if err := db.Set(userKey(idx), userValue(idx), wo); err != nil {
				return fmt.Errorf("fixture batch %d set key %d: %w", batch, idx, err)
			}
		}
		written += n
		if err := db.Flush(); err != nil {
			return fmt.Errorf("fixture batch %d flush: %w", batch, err)
		}
	}
	if written != spec.KeyspaceCardinality {
		return fmt.Errorf("fixture wrote %d keys, need %d", written, spec.KeyspaceCardinality)
	}
	return nil
}

// verifyFixture reads three sample keys and compares values byte-for-byte.
func verifyFixture(db *pebble.DB, spec *TrialSpec) error {
	samples := []int{0, spec.KeyspaceCardinality / 2, spec.KeyspaceCardinality - 1}
	seen := make(map[int]bool, len(samples))
	for _, idx := range samples {
		if idx < 0 || idx >= spec.KeyspaceCardinality || seen[idx] {
			continue
		}
		seen[idx] = true
		got, closer, err := db.Get(userKey(idx))
		if err != nil {
			return fmt.Errorf("fixture sample key %d unreadable: %w", idx, err)
		}
		want := userValue(idx)
		mismatch := !bytes.Equal(got, want)
		closeErr := closer.Close()
		if mismatch {
			return fmt.Errorf("fixture sample key %d value mismatch: got %d bytes, want %d bytes",
				idx, len(got), len(want))
		}
		if closeErr != nil {
			return fmt.Errorf("fixture sample key %d closer: %w", idx, closeErr)
		}
	}
	return nil
}

func latencyCapacity(durationMS int) int {
	capacity := durationMS * 500
	if capacity < 4096 {
		capacity = 4096
	}
	if capacity > 1<<20 {
		capacity = 1 << 20
	}
	return capacity
}

// runWarmup performs unrecorded reads for the warmup duration.
func runWarmup(db *pebble.DB, spec *TrialSpec) (opCounter, time.Duration) {
	var total opCounter
	start := time.Now()
	if spec.WarmupDurationMS <= 0 {
		return total, 0
	}
	deadline := start.Add(time.Duration(spec.WarmupDurationMS) * time.Millisecond)
	runs := make([]*opCounter, spec.ReaderConcurrency)
	var wg sync.WaitGroup
	for i := 0; i < spec.ReaderConcurrency; i++ {
		runs[i] = &opCounter{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run := runs[i]
			r := rand.New(rand.NewSource(derivedSeed(spec.Seed, "warmup-reader", i)))
			kbuf := make([]byte, 0, 32)
			for time.Now().Before(deadline) {
				idx := r.Intn(spec.KeyspaceCardinality)
				kbuf = appendUserKey(kbuf, idx)
				run.attempted++
				_, closer, err := db.Get(kbuf)
				if err != nil {
					run.errors++
					run.setErr("warmup get key %d: %v", idx, err)
					continue
				}
				run.completed++
				if err := closer.Close(); err != nil {
					run.errors++
					run.setErr("warmup closer key %d: %v", idx, err)
				}
			}
		}(i)
	}
	wg.Wait()
	for _, run := range runs {
		mergeOpCounter(&total, run)
	}
	return total, time.Since(start)
}

type readerRun struct {
	opCounter
	misses  int64
	samples []int64
	dropped int64
}

func (r *readerRun) record(ns int64) {
	if len(r.samples) < cap(r.samples) {
		r.samples = append(r.samples, ns)
	} else {
		r.dropped++
	}
}

type writerRun struct {
	opCounter
}

// runMeasurement runs readers and writers concurrently until the shared
// deadline. Each goroutine owns its counters and sample slice; no mutex is
// taken on the hot path.
func runMeasurement(db *pebble.DB, spec *TrialSpec, deadline time.Time) ([]*readerRun, []*writerRun) {
	hitRatio := spec.hitRatio()
	readers := make([]*readerRun, spec.ReaderConcurrency)
	writers := make([]*writerRun, spec.WriterConcurrency)
	var wg sync.WaitGroup

	for i := 0; i < spec.ReaderConcurrency; i++ {
		readers[i] = &readerRun{samples: make([]int64, 0, latencyCapacity(spec.MeasurementDurationMS))}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run := readers[i]
			r := rand.New(rand.NewSource(derivedSeed(spec.Seed, "measure-reader", i)))
			kbuf := make([]byte, 0, 32)
			for !time.Now().After(deadline) {
				absent := r.Float64() >= hitRatio
				var idx int
				if absent {
					idx = absentKeyIndex(r, spec.KeyspaceCardinality)
				} else {
					idx = r.Intn(spec.KeyspaceCardinality)
				}
				kbuf = appendUserKey(kbuf, idx)
				run.attempted++
				start := clockStart()
				_, closer, err := db.Get(kbuf)
				elapsed := clockElapsedNanos(start)
				switch {
				case err == nil:
					if absent {
						run.errors++
						run.setErr("absent key %d unexpectedly present", idx)
					} else {
						run.completed++
						run.record(elapsed)
					}
					if closeErr := closer.Close(); closeErr != nil {
						run.errors++
						run.setErr("read closer key %d: %v", idx, closeErr)
					}
				case errors.Is(err, pebble.ErrNotFound):
					if absent {
						run.completed++
						run.misses++
						run.record(elapsed)
					} else {
						run.errors++
						run.setErr("existing key %d not found", idx)
					}
				default:
					run.errors++
					run.setErr("read key %d: %v", idx, err)
				}
			}
		}(i)
	}

	for i := 0; i < spec.WriterConcurrency; i++ {
		writers[i] = &writerRun{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run := writers[i]
			r := rand.New(rand.NewSource(derivedSeed(spec.Seed, "measure-writer", i)))
			kbuf := make([]byte, 0, 32)
			vbuf := make([]byte, 0, valueLen)
			wo := &pebble.WriteOptions{Sync: true}
			for !time.Now().After(deadline) {
				idx := r.Intn(spec.KeyspaceCardinality)
				kbuf = appendUserKey(kbuf, idx)
				vbuf = appendUserValue(vbuf, idx)
				run.attempted++
				if err := db.Set(kbuf, vbuf, wo); err != nil {
					run.errors++
					run.setErr("write key %d: %v", idx, err)
					continue
				}
				run.completed++
			}
		}(i)
	}

	wg.Wait()
	return readers, writers
}

func firstOpError(readers []*readerRun, writers []*writerRun) string {
	for _, r := range readers {
		if r.firstErr != "" {
			return r.firstErr
		}
	}
	for _, w := range writers {
		if w.firstErr != "" {
			return w.firstErr
		}
	}
	return ""
}

// runTrial executes the whole trial lifecycle and always returns an envelope;
// the envelope's Status says whether the trial succeeded.
func runTrial(spec *TrialSpec, prov Provenance) (env *Envelope) {
	started := time.Now().UTC()
	env = &Envelope{
		Schema:  schemaTrial,
		TrialID: spec.TrialID,
		Block:   spec.Block,
		Cell:    spec.Cell,
		Arm:     spec.Arm,
		Runtime: newRuntimeInfo(),
		Timing: TimingInfo{
			StartedUTC: started.Format(time.RFC3339Nano),
			CooldownMS: spec.CooldownDurationMS,
		},
		Status:     statusSuccess,
		Provenance: prov,
		Spec:       spec,
	}
	fail := func(stage string, err error) {
		if env.Error == "" {
			env.Error = stage + ": " + err.Error()
		}
		env.Status = statusFailed
	}

	// Registered first, so it runs last and sees the final status.
	defer func() {
		env.Timing.FinishedUTC = time.Now().UTC().Format(time.RFC3339Nano)
		env.Timing.WallMS = time.Since(started).Milliseconds()
	}()

	// Removal runs after DB close and cache release (defers are LIFO below).
	dirRemoved := false
	defer func() {
		if dirRemoved {
			return
		}
		ok, err := removeDataDir(spec.DataDir)
		env.DataDirRemoved = ok
		if err != nil {
			fail("remove_data_dir", err)
		}
	}()

	opts, optionsInfo, cache := pebbleOptionsFor(spec)
	defer cache.Unref()
	env.PebbleOptions = optionsInfo

	db, err := pebble.Open(spec.DataDir, opts)
	if err != nil {
		fail("open_db", err)
		return env
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close()
		}
	}()

	if err := writeFixture(db, spec); err != nil {
		fail("fixture", err)
		return env
	}
	if err := verifyFixture(db, spec); err != nil {
		env.Result.Operations.ValidationErrorCount++
		fail("fixture_validation", err)
		return env
	}
	fixtureSSTables := 0
	if m := db.Metrics(); m != nil {
		fixtureSSTables = sstableCount(m)
	}

	warm, warmElapsed := runWarmup(db, spec)
	env.Result.Warmup = WarmupStats{
		Attempted:  warm.attempted,
		Completed:  warm.completed,
		Errors:     warm.errors,
		DurationMS: warmElapsed.Milliseconds(),
	}
	if warm.errors > 0 {
		fail("warmup", errors.New(warm.firstErr))
		return env
	}

	measureStart := time.Now()
	deadline := measureStart.Add(time.Duration(spec.MeasurementDurationMS) * time.Millisecond)
	readers, writers := runMeasurement(db, spec, deadline)
	measureElapsed := time.Since(measureStart)

	var ops Operations
	var merged []int64
	var dropped int64
	for _, r := range readers {
		ops.ReadOpsAttempted += r.attempted
		ops.ReadOpsCompleted += r.completed
		ops.ErrorOpsCount += r.errors
		env.Result.ReadMisses += r.misses
		dropped += r.dropped
		merged = append(merged, r.samples...)
	}
	for _, w := range writers {
		ops.WriteOpsAttempted += w.attempted
		ops.WriteOpsCompleted += w.completed
		ops.ErrorOpsCount += w.errors
	}
	env.Result.Operations = ops
	env.Result.MeasurementMS = measureElapsed.Milliseconds()

	elapsedSecs := measureElapsed.Seconds()
	if elapsedSecs > 0 {
		env.Result.Throughput.ReadOpsPerSec = float64(ops.ReadOpsCompleted) / elapsedSecs
		env.Result.Throughput.WriteOpsPerSec = float64(ops.WriteOpsCompleted) / elapsedSecs
		env.Result.Throughput.TotalOpsPerSec = float64(ops.ReadOpsCompleted+ops.WriteOpsCompleted) / elapsedSecs
	}
	env.Result.LatencyReadNanos = summarizeLatency(merged)
	env.Result.LatencyReadNanos.SamplesDropped = dropped
	env.Result.LatencyReadNanos.SampleCapacity = latencyCapacity(spec.MeasurementDurationMS)
	env.Result.LatencyReadNanos.SamplingNote = "per-goroutine preallocated slices; merged after the window"
	env.Result.LatencyReadNanos.ClockSource = clockSource()
	env.Result.LatencyReadNanos.ClockResolutionNanos = clockResolutionNanos()

	if spec.CooldownDurationMS > 0 {
		time.Sleep(time.Duration(spec.CooldownDurationMS) * time.Millisecond)
	}

	// Metrics must be read before Close.
	metrics := db.Metrics()
	if metrics != nil {
		env.Result.LSMDiagnostics = LSMDiagnostics{
			MeasuredFlushes:       metrics.Flush.Count,
			MeasuredCompactions:   metrics.Compact.Count,
			FinalSSTables:         sstableCount(metrics),
			FixtureSSTables:       fixtureSSTables,
			BackingTableCount:     int(metrics.Table.BackingTableCount),
			PebbleMetricsSnapshot: metricsSnapshot(metrics),
		}
	}

	if err := db.Close(); err != nil {
		fail("close_db", err)
	}
	closed = true

	if !dirRemoved {
		ok, removeErr := removeDataDir(spec.DataDir)
		dirRemoved = true
		env.DataDirRemoved = ok
		if removeErr != nil {
			fail("remove_data_dir", removeErr)
		}
	}

	if ops.ReadOpsAttempted == 0 {
		fail("measurement", errors.New("no read operations were attempted"))
	}
	if ops.ErrorOpsCount > 0 {
		fail("measurement", fmt.Errorf("%d operation errors; first: %s", ops.ErrorOpsCount, firstOpError(readers, writers)))
	}
	return env
}

// parseFlags parses the CLI and returns exit code 2 on any usage error.
func parseFlags(args []string, stderr io.Writer) (specPath, outPath, platformPath, gitRev, gate, approvalPath string, code int, ok bool) {
	fs := flag.NewFlagSet("pebble_runner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&specPath, "spec", "", "path to the i7.trial-spec.v1 JSON spec (required)")
	fs.StringVar(&outPath, "out", "", "path to write the i7.trial.v1 JSON envelope (required)")
	fs.StringVar(&platformPath, "platform", "", "path to the platform manifest JSON (optional, hashed)")
	fs.StringVar(&gitRev, "git-rev", "", "git revision recorded in provenance (optional)")
	fs.StringVar(&gate, "gate", "", "approval gate to enforce, e.g. G4 (required)")
	fs.StringVar(&approvalPath, "approval", "", "path to the approval JSON (required)")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: pebble_runner.exe -spec <spec.json> -out <out.json> "+
			"-platform <platform_manifest.json> -git-rev <sha> -gate G4 -approval <APPROVAL.json>\n")
	}
	if err := fs.Parse(args); err != nil {
		return "", "", "", "", "", "", exitUsageError, false
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "usage error: unexpected positional arguments: %s\n", strings.Join(fs.Args(), " "))
		return "", "", "", "", "", "", exitUsageError, false
	}
	if specPath == "" || outPath == "" || gate == "" || approvalPath == "" {
		fmt.Fprintln(stderr, "usage error: -spec, -out, -gate and -approval are required")
		return "", "", "", "", "", "", exitUsageError, false
	}
	return specPath, outPath, platformPath, gitRev, gate, approvalPath, exitSuccess, true
}

func readPlatformManifestSHA(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read platform manifest %q: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	specPath, outPath, platformPath, gitRev, gate, approvalPath, code, ok := parseFlags(args, stderr)
	if !ok {
		return code
	}

	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(stderr, "usage error: read spec %q: %v\n", specPath, err)
		return exitUsageError
	}
	var spec TrialSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintf(stderr, "usage error: parse spec %q: %v\n", specPath, err)
		return exitUsageError
	}
	if spec.Block != blockE7 {
		fmt.Fprintf(stderr, "unsupported: block %q is not %q\n", spec.Block, blockE7)
		return exitUnsupported
	}
	if spec.Arm != armPebble {
		fmt.Fprintf(stderr, "usage error: arm %q is not %q\n", spec.Arm, armPebble)
		return exitUsageError
	}
	if err := spec.validate(); err != nil {
		fmt.Fprintf(stderr, "usage error: %v\n", err)
		return exitUsageError
	}

	approval, err := loadApproval(approvalPath)
	if err != nil {
		fmt.Fprintf(stderr, "not approved: %v\n", err)
		return exitNotApproved
	}
	if err := approval.satisfies(gate); err != nil {
		fmt.Fprintf(stderr, "not approved: %v\n", err)
		return exitNotApproved
	}

	platformSHA, err := readPlatformManifestSHA(platformPath)
	if err != nil {
		fmt.Fprintf(stderr, "usage error: %v\n", err)
		return exitUsageError
	}

	if err := prepareDataDir(spec.DataDir); err != nil {
		fmt.Fprintf(stderr, "usage error: %v\n", err)
		return exitUsageError
	}

	prov := Provenance{
		GitRev:                 gitRev,
		Gate:                   gate,
		ApprovalPath:           approvalPath,
		ApprovedBy:             approval.ApprovedBy,
		CampaignID:             approval.CampaignID,
		ApprovalProtocol:       approval.ProtocolVersion,
		PlatformManifestPath:   platformPath,
		PlatformManifestSHA256: platformSHA,
	}
	env := runTrial(&spec, prov)
	if err := writeEnvelope(outPath, env); err != nil {
		fmt.Fprintf(stderr, "failed to write envelope %q: %v\n", outPath, err)
		return exitTrialFailed
	}

	fmt.Fprintf(stdout, "trial=%s cell=%s arm=%s status=%s reads=%d/%d writes=%d/%d errors=%d p50_ns=%d\n",
		env.TrialID, env.Cell, env.Arm, env.Status,
		env.Result.Operations.ReadOpsCompleted, env.Result.Operations.ReadOpsAttempted,
		env.Result.Operations.WriteOpsCompleted, env.Result.Operations.WriteOpsAttempted,
		env.Result.Operations.ErrorOpsCount, env.Result.LatencyReadNanos.P50)
	if env.Status == statusSuccess {
		return exitSuccess
	}
	return exitTrialFailed
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}
