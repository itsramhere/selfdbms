// Package harness2 is the Phase I7 (Windows-native) trial executor.
//
// It exists because the frozen harness in research/experiments/harness has three
// properties that Phase I7 must not inherit:
//
//   - one global mutex per read sample (LatencyRecorder) and another for flush
//     overlap accounting, both on the read hot path and both correlated with the
//     reader count (audit findings IA-1, IA-2);
//   - a single PRNG stream per worker, so paired arms stop issuing identical
//     requests as soon as their operation counts diverge (IA-8);
//   - a publication driver that injects 50-key write batches into the window
//     (AF-3) instead of publishing memtables frozen before it.
//
// Nothing here is pooled with the frozen campaigns' data.
package harness2

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"owndbms/lsmengine"

	"owndbms/research/experiments/harness"
)

// Block identifies an I7 experimental block.
type Block string

const (
	BlockX1    Block = "X1"
	BlockX2    Block = "X2"
	BlockX3    Block = "X3"
	BlockX4    Block = "X4"
	BlockE1    Block = "E1"
	BlockE2    Block = "E2"
	BlockE3    Block = "E3"
	BlockE4    Block = "E4"
	BlockE5    Block = "E5"
	BlockE7    Block = "E7"
	BlockD1    Block = "D1"
	BlockD2    Block = "D2"
	BlockD3    Block = "D3"
	BlockPF    Block = "PF"
	BlockSMOKE Block = "SMOKE"
)

// PublicationMode distinguishes "no events" from scheduled physical publications.
type PublicationMode string

const (
	PubNone     PublicationMode = "none"
	PubPhysical PublicationMode = "physical"
)

// Spec is the per-trial instruction emitted by run_i7.ps1 from runlist.csv.
// JSON names are a contract with the PowerShell driver and must not change
// without a schema version bump.
type Spec struct {
	Schema                 string  `json:"schema"`
	TrialID                string  `json:"trial_id"`
	Block                  string  `json:"block"`
	Cell                   string  `json:"cell"`
	Triplet                int     `json:"triplet"`
	Arm                    string  `json:"arm"`
	OrderPosition          int     `json:"order_position"`
	RunlistPosition        int     `json:"runlist_position"`
	Seed                   int64   `json:"seed"`
	WorkloadID             string  `json:"workload_id"`
	ReaderConcurrency      int     `json:"reader_concurrency"`
	WriterConcurrency      int     `json:"writer_concurrency"`
	MeasurementDurationMs  int     `json:"measurement_duration_ms"`
	WarmupDurationMs       int     `json:"warmup_duration_ms"`
	CooldownDurationMs     int     `json:"cooldown_duration_ms"`
	KeyspaceCardinality    int     `json:"keyspace_cardinality"`
	InitialSSTableCount    int     `json:"initial_sstable_count"`
	InitialKeysPerSSTable  int     `json:"initial_keys_per_sstable"`
	TargetReadHitRatio     float64 `json:"target_read_hit_ratio"`
	MemtableThresholdBytes int64   `json:"memtable_threshold_bytes"`
	FlushTriggerIntervalMs int     `json:"flush_trigger_interval_ms"`
	CompactionTriggerMs    int     `json:"compaction_trigger_interval_ms"`
	WalSync                bool    `json:"wal_sync"`
	ReadPath               string  `json:"read_path,omitempty"`
	ReadIODelayUs          int     `json:"read_io_delay_us,omitempty"`
	ReadDistribution       string  `json:"read_distribution"`
	DataDir                string  `json:"data_dir"`
	ExpectedReaderSetSize  int     `json:"expected_reader_set_size"`
	Notes                  string  `json:"notes,omitempty"`

	// Driver extensions (Phase I7 G2). Zero values mean "not applicable".
	PublicationEvents    int     `json:"publication_events,omitempty"`
	PublicationMode      string  `json:"publication_mode,omitempty"`
	OpenLoopRatePerSec   float64 `json:"open_loop_rate_per_sec,omitempty"`
	PacedWriteRatePerSec float64 `json:"paced_write_rate_per_sec,omitempty"`
	MaxLatenessMs        int     `json:"max_lateness_ms,omitempty"`

	// Engineering-preflight extension (Phase I7 R2). When true, the engine's
	// non-invasive synchronization instrumentation is attached for the whole
	// trial and the aggregate is reported in
	// TrialResult.Sync. Default off: the observer costs one nil check on
	// uninstrumented paths and the trial schema gains no field.
	InstrumentSync   bool `json:"instrument_sync,omitempty"`
	InlineFlushCheck bool `json:"inline_flush_check,omitempty"`
}

// Mode returns the driver mode implied by the block and cell.
type Mode int

const (
	ModeClosedLoop Mode = iota
	ModePublicationOnly
	ModePacedWrites
	ModeOpenLoop
	ModeOpenReadsPacedWrites
)

// ResolveMode classifies a spec. An unsupported combination returns an error so
// the runner fails closed instead of silently running a different experiment.
func (s Spec) ResolveMode() (Mode, error) {
	if _, err := s.ResolveReadPath(); err != nil {
		return 0, err
	}
	// X requires true rather than relying on a missing JSON bool's zero value.
	if s.IsX() && !s.WalSync {
		return 0, errf("X blocks require explicit wal_sync=true")
	}
	switch Block(s.Block) {
	case BlockE2, BlockX2:
		if s.PublicationMode != string(PubPhysical) && s.PublicationMode != string(PubNone) {
			return 0, errf("%s requires publication_mode none|physical, got %q", s.Block, s.PublicationMode)
		}
		return ModePublicationOnly, nil
	case BlockX4:
		if math.IsNaN(s.PacedWriteRatePerSec) || s.PacedWriteRatePerSec <= 0 || s.PacedWriteRatePerSec > maxPacedWriteRatePerSec {
			return 0, errf("X4 requires finite paced_write_rate_per_sec in (0, %g]", maxPacedWriteRatePerSec)
		}
		if s.WriterConcurrency != 1 {
			return 0, errf("X4 requires writer_concurrency == 1")
		}
		if math.IsNaN(s.OpenLoopRatePerSec) || math.IsInf(s.OpenLoopRatePerSec, 0) || s.OpenLoopRatePerSec < 0 {
			return 0, errf("X4 requires finite open_loop_rate_per_sec >= 0")
		}
		if s.OpenLoopRatePerSec > 0 {
			return ModeOpenReadsPacedWrites, nil
		}
		return ModePacedWrites, nil
	case BlockE4:
		if s.OpenLoopRatePerSec <= 0 {
			return 0, errf("E4 requires open_loop_rate_per_sec > 0 (set from the G3 calibration)")
		}
		return ModeOpenLoop, nil
	case BlockE3:
		if len(s.Cell) >= 6 && s.Cell[:6] == "equal_" {
			if s.PacedWriteRatePerSec <= 0 {
				return 0, errf("E3 equal-work requires paced_write_rate_per_sec > 0 (W_paced)")
			}
			if s.PacedWriteRatePerSec > maxPacedWriteRatePerSec {
				return 0, errf("E3 paced_write_rate_per_sec %g exceeds the sanity bound %g", s.PacedWriteRatePerSec, maxPacedWriteRatePerSec)
			}
			return ModePacedWrites, nil
		}
		return ModeClosedLoop, nil
	case BlockX1, BlockX3, BlockE1, BlockE5, BlockD1, BlockD2, BlockD3, BlockPF, BlockSMOKE:
		return ModeClosedLoop, nil
	case BlockE7:
		return 0, errf("E7 is executed by the separate pebble_runner binary")
	}
	return 0, errf("unknown block %q", s.Block)
}

func (s Spec) IsX() bool {
	return s.Block == string(BlockX1) || s.Block == string(BlockX2) || s.Block == string(BlockX3) || s.Block == string(BlockX4)
}

// ResolveReadPath fails closed, including duration overflow before conversion.
func (s Spec) ResolveReadPath() (lsmengine.ReadPathMode, error) {
	if s.ReadIODelayUs < 0 || int64(s.ReadIODelayUs) > math.MaxInt64/int64(time.Microsecond) {
		return 0, errf("invalid read_io_delay_us %d", s.ReadIODelayUs)
	}
	switch s.ReadPath {
	case "", "block":
		return lsmengine.ReadPathBlock, nil
	case "legacy_scan":
		if s.ReadIODelayUs != 0 {
			return 0, errf("legacy_scan requires read_io_delay_us=0")
		}
		return lsmengine.ReadPathLegacyScan, nil
	default:
		return 0, errf("unknown read_path %q", s.ReadPath)
	}
}

// MarshalJSON keeps old specs byte-compatible while making X zero-dose settings
// explicit in the serialized config, where omission would obscure treatment.
func (s Spec) MarshalJSON() ([]byte, error) {
	type plain Spec
	if !s.IsX() {
		return json.Marshal(plain(s))
	}
	path := s.ReadPath
	if path == "" {
		path = "block"
	}
	return json.Marshal(struct {
		plain
		ReadPath      string `json:"read_path"`
		ReadIODelayUs int    `json:"read_io_delay_us"`
	}{plain(s), path, s.ReadIODelayUs})
}

// Environment is the harness-compatible environment record.
type Environment = harness.EnvironmentInfo

// TimestampInfo mirrors the frozen schema for downstream tooling.
type TimestampInfo struct {
	StartISO              string `json:"start_iso"`
	FinishISO             string `json:"finish_iso"`
	WarmupDurationMs      int    `json:"warmup_duration_ms"`
	MeasurementDurationMs int    `json:"measurement_duration_ms"`
	MeasuredWindowNs      int64  `json:"measured_window_ns"`
}

// OperationCounts extends the frozen counters with the scheduled/issued/
// abandoned/unissued accounting required for open-loop measurement, and
// distinguishes completions in-window from late completions during drain.
type OperationCounts struct {
	ReadOpsScheduled          int64            `json:"read_ops_scheduled"`
	ReadOpsIssued             int64            `json:"read_ops_issued"`
	ReadOpsCompletedInWindow  int64            `json:"read_ops_completed_in_window"`
	ReadOpsCompletedLate      int64            `json:"read_ops_completed_late"`
	ReadOpsCompleted          int64            `json:"read_ops_completed"`
	ReadOpsAbandoned          int64            `json:"read_ops_abandoned"`
	ReadOpsUnissued           int64            `json:"read_ops_unissued"`
	ExpectedHitCount          int64            `json:"expected_hit_count"`
	ExpectedMissCount         int64            `json:"expected_miss_count"`
	ActualHitCount            int64            `json:"actual_hit_count"`
	ActualMissCount           int64            `json:"actual_miss_count"`
	ActualMemtableHitCount    int64            `json:"actual_memtable_hit_count"`
	ActualSSTableHitCount     int64            `json:"actual_sstable_hit_count"`
	WriteOpsAttempted         int64            `json:"write_ops_attempted"`
	WriteOpsCompletedInWindow int64            `json:"write_ops_completed_in_window"`
	WriteOpsCompletedLate     int64            `json:"write_ops_completed_late"`
	WriteOpsCompleted         int64            `json:"write_ops_completed"`
	ErrorOpsCount             int64            `json:"error_ops_count"`
	ValidationErrorCount      int64            `json:"validation_error_count"`
	WorkerSeeds               map[string]int64 `json:"worker_seeds,omitempty"`
}

// ThroughputInfo mirrors the frozen schema.
type ThroughputInfo struct {
	ReadOpsPerSec  float64 `json:"read_ops_per_sec"`
	WriteOpsPerSec float64 `json:"write_ops_per_sec"`
	TotalOpsPerSec float64 `json:"total_ops_per_sec"`
}

// Diagnostics carries engine activity plus the accounting needed to keep
// per-trial work visible (audit AF-4: composition shifts must not be hidden).
type Diagnostics struct {
	CompactionNoOps                   uint64  `json:"compaction_no_ops,omitempty"`
	CompactionErrors                  uint64  `json:"compaction_errors,omitempty"`
	CompactionSuccesses               uint64  `json:"compaction_successes,omitempty"`
	CompactionsInWindow               *uint64 `json:"compactions_in_window,omitempty"`
	InitialSSTables                   int     `json:"initial_sstables"`
	FinalSSTables                     int     `json:"final_sstables"`
	InitialFrozenMemtables            int     `json:"initial_frozen_memtables"`
	FinalFrozenMemtables              int     `json:"final_frozen_memtables"`
	PublicationEventsTarget           int     `json:"publication_events_target"`
	PublicationEventsReleased         int     `json:"publication_events_released"`
	PublicationEventsRealizedInWindow int     `json:"publication_events_realized_in_window"`
	PublicationEventsRealizedLate     int     `json:"publication_events_realized_late"`
	PublicationEventsRealized         uint64  `json:"publication_events_realized"`
	TopologyNote                      string  `json:"topology_note,omitempty"`
	MeasuredFlushes                   uint64  `json:"measured_flushes"`
	MeasuredCompactions               uint64  `json:"measured_compactions"`
	MeasuredSSTableReads              uint64  `json:"measured_sstable_point_reads"`
	MeasuredMemtableReads             uint64  `json:"measured_memtable_point_reads"`
	SnapshotSeq                       uint64  `json:"snapshot_seq"`
	ActiveSnapshotPins                int64   `json:"active_snapshot_pins"`
	RetiredSnapshots                  int     `json:"retired_snapshots_count"`
	PendingCleanupCount               int     `json:"pending_cleanup_count"`
	LivePhysicalReaders               int     `json:"live_physical_readers"`
	// New in I7.
	PendingFlushesAtEnd  int   `json:"pending_flushes_at_end"`
	MaxOutstandingLeases int32 `json:"max_outstanding_leases"`
	DataDirRemoved       bool  `json:"data_dir_removed"`
}

// EventRecord is one scheduled or observed publication.
type EventRecord struct {
	Index        int   `json:"index"`
	ReleasedNs   int64 `json:"released_ns"`
	BeginNs      int64 `json:"begin_ns"`
	EndNs        int64 `json:"end_ns"`
	HoldNs       int64 `json:"hold_ns"`
	BuildNs      int64 `json:"build_ns"`
	MemtableKeys int   `json:"memtable_keys"`
	InWindow     bool  `json:"in_window"`
	// ActiveReadersAtArrival is the engine's sampled count of active
	// Manager.Mu read holders taken immediately before the publication Lock
	// attempt (R1.2e §3.2). The sample is one-sided: it can overcount the
	// true blocking set (readers mid-acquisition or mid-release are still
	// counted) and is not linearized with the Lock. See the checkpoint
	// document for the full timestamp and synchronization limitations.
	ActiveReadersAtArrival int `json:"active_readers_at_arrival"`
}

// EventConditioned summarises reads whose execution overlapped an event window.
type EventConditioned struct {
	EventIndex    int     `json:"event_index"`
	OverlapReads  int     `json:"overlap_reads"`
	MaxResponseNs int64   `json:"max_response_ns"`
	P50ResponseNs int64   `json:"p50_response_ns"`
	P95ResponseNs *int64  `json:"p95_response_ns"`
	HoldNs        int64   `json:"hold_ns"`
	BlockRatio    float64 `json:"block_ratio"`
}

// LatencyStats mirrors the frozen schema, including the sample-sufficiency rules.
type LatencyStats struct {
	Count     int64   `json:"sample_count"`
	MinNanos  int64   `json:"min"`
	MaxNanos  int64   `json:"max"`
	MeanNanos float64 `json:"mean"`
	P50Nanos  int64   `json:"p50"`
	P95Nanos  *int64  `json:"p95"`
	P99Nanos  *int64  `json:"p99"`
	P999Nanos *int64  `json:"p999"`
}

// TrialResult is the per-trial envelope consumed by the analysis layer.
type TrialResult struct {
	FixtureWALSync      *bool           `json:"fixture_wal_sync,omitempty"`
	SchemaVersion       string          `json:"schema_version"`
	TrialID             string          `json:"trial_id"`
	TrialIndex          int             `json:"trial_index"`
	WorkloadID          string          `json:"workload_id"`
	Block               string          `json:"block"`
	Cell                string          `json:"cell"`
	Arm                 string          `json:"arm"`
	SyncMode            string          `json:"sync_mode"`
	GitRevision         string          `json:"git_revision"`
	Environment         Environment     `json:"environment"`
	Config              Spec            `json:"config"`
	Timestamps          TimestampInfo   `json:"timestamps"`
	Operations          OperationCounts `json:"operations"`
	Throughput          ThroughputInfo  `json:"throughput"`
	LatencyRead         LatencyStats    `json:"latency_read_nanos"`
	LatencyResponse     LatencyStats    `json:"latency_response_nanos"`
	LatencyReadLate     LatencyStats    `json:"latency_read_late_nanos,omitempty"`
	LatencyResponseLate LatencyStats    `json:"latency_response_late_nanos,omitempty"`
	LSMDiagnostics      Diagnostics     `json:"lsm_diagnostics"`
	// X4 adds optional scheduled-load observations; older result bytes omit it.
	PacedLoad *PacedLoadOutcome `json:"paced_load,omitempty"`

	// Ledger is the conserved request-lifecycle accounting (offered /
	// attempted / in-window / late / failed / queued / outstanding).
	Ledger TransactionLedger `json:"transaction_ledger"`
	// Sync carries the aggregated engine synchronization instrumentation when
	// spec.instrument_sync was set; nil otherwise.
	Sync            *SyncInstrumentationSummary `json:"sync_instrumentation,omitempty"`
	Events          []EventRecord               `json:"publication_events,omitempty"`
	EventsLate      []EventRecord               `json:"publication_events_late,omitempty"`
	EventCond       []EventConditioned          `json:"event_conditioned,omitempty"`
	FixtureManifest harness.FixtureManifest     `json:"fixture_manifest"`
	Status          string                      `json:"status"`
	Error           string                      `json:"error,omitempty"`
}

// helperError keeps error construction in one place.
func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// maxPacedWriteRatePerSec is a fail-closed sanity bound: a paced rate beyond
// this would make the tick period sub-nanosecond and the paced writer loop
// unbounded. Real cells are two to three orders of magnitude below it.
const maxPacedWriteRatePerSec = 1e7

// defaultWindow is only used by tests that omit durations.
const defaultWindow = 400 * time.Millisecond
