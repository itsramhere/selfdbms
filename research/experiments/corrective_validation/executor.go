package corrective_validation

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"owndbms/research/experiments/harness"
	"owndbms/research/experiments/provenance"
)

var csvLock sync.Mutex

// CSVHeaders returns standard column headers for corrective experiment results.
func CSVHeaders() []string {
	return []string{
		"trial_id",
		"campaign_id",
		"experiment",
		"workload_id",
		"condition",
		"sync_mode",
		"reader_concurrency",
		"writer_concurrency",
		"pair_index",
		"order_in_pair",
		"pair_seed",
		"effective_seed",
		"git_commit",
		"status",
		"read_ops_attempted",
		"read_ops_admitted",
		"read_ops_completed",
		"read_ops_per_sec",
		"write_ops_attempted",
		"write_ops_admitted",
		"write_ops_completed",
		"write_ops_per_sec",
		"latency_read_p50_us",
		"latency_read_p95_us",
		"latency_read_p99_us",
		"latency_read_mean_us",
		"measured_flushes",
		"measured_compactions",
		"reads_during_flush",
		"reads_during_publication",
		"flush_overlap_ms",
	}
}

// TrialToCSVRow converts a completed TrialResult into a flat row.
func TrialToCSVRow(mt *ManifestTrial, res *harness.TrialResult) []string {
	p50 := float64(res.LatencyRead.P50Nanos) / 1000.0
	p95 := 0.0
	if res.LatencyRead.P95Nanos != nil {
		p95 = float64(*res.LatencyRead.P95Nanos) / 1000.0
	}
	p99 := 0.0
	if res.LatencyRead.P99Nanos != nil {
		p99 = float64(*res.LatencyRead.P99Nanos) / 1000.0
	}
	mean := res.LatencyRead.MeanNanos / 1000.0

	return []string{
		mt.TrialID,
		mt.CampaignID,
		mt.Experiment,
		mt.WorkloadID,
		mt.Condition,
		mt.SyncMode,
		strconv.Itoa(mt.ReaderConcurrency),
		strconv.Itoa(mt.WriterConcurrency),
		strconv.Itoa(mt.PairIndex),
		strconv.Itoa(mt.OrderInPair),
		strconv.FormatInt(mt.PairSeed, 10),
		strconv.FormatInt(mt.Seed, 10),
		res.GitRevision,
		res.Status,
		strconv.FormatInt(res.Operations.ReadOpsAttempted, 10),
		strconv.FormatInt(res.Operations.ReadOpsAdmitted, 10),
		strconv.FormatInt(res.Operations.ReadOpsCompleted, 10),
		fmt.Sprintf("%.2f", res.Throughput.ReadOpsPerSec),
		strconv.FormatInt(res.Operations.WriteOpsAttempted, 10),
		strconv.FormatInt(res.Operations.WriteOpsAdmitted, 10),
		strconv.FormatInt(res.Operations.WriteOpsCompleted, 10),
		fmt.Sprintf("%.2f", res.Throughput.WriteOpsPerSec),
		fmt.Sprintf("%.2f", p50),
		fmt.Sprintf("%.2f", p95),
		fmt.Sprintf("%.2f", p99),
		fmt.Sprintf("%.2f", mean),
		strconv.FormatUint(res.LSMDiagnostics.MeasuredFlushes, 10),
		strconv.FormatUint(res.LSMDiagnostics.MeasuredCompactions, 10),
		strconv.FormatInt(res.LSMDiagnostics.ReadsDuringFlush, 10),
		strconv.FormatInt(res.LSMDiagnostics.ReadsDuringPublication, 10),
		strconv.FormatInt(res.LSMDiagnostics.FlushActiveOverlapMs, 10),
	}
}

// AppendCSVRow safely writes a CSV row with header management.
func AppendCSVRow(csvPath string, row []string) error {
	csvLock.Lock()
	defer csvLock.Unlock()

	needHeader := false
	if _, err := os.Stat(csvPath); os.IsNotExist(err) {
		needHeader = true
	}

	f, err := os.OpenFile(csvPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open csv %s: %w", csvPath, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if needHeader {
		if err := w.Write(CSVHeaders()); err != nil {
			return fmt.Errorf("write csv header: %w", err)
		}
	}
	return w.Write(row)
}

// ExecuteCorrectiveTrial runs a single manifest trial under strict provenance and isolation.
func ExecuteCorrectiveTrial(mt *ManifestTrial, outDir string, prov *provenance.ProvenanceRecord, env harness.EnvironmentInfo, logFn func(string)) (*harness.TrialResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	// Provenance Preflight Check: Reject unknown_rev
	if prov == nil || prov.GitCommit == "" || prov.GitCommit == "unknown_rev" {
		return nil, fmt.Errorf("provenance violation: invalid or missing Git commit; 'unknown_rev' is forbidden")
	}

	trialDir, err := os.MkdirTemp("", fmt.Sprintf("trial_%s_*", mt.TrialID))
	if err != nil {
		return nil, fmt.Errorf("create temp trial dir: %w", err)
	}
	defer os.RemoveAll(trialDir)

	cfg := harness.TrialConfig{
		SchemaVersion:               "1.0.0",
		TrialID:                     mt.TrialID,
		TrialIndex:                  mt.TrialIndex,
		WorkloadID:                  harness.WorkloadID(mt.WorkloadID),
		SyncMode:                    mt.SyncMode,
		Seed:                        mt.PairSeed, // CRITICAL: Use PairSeed directly for worker seed derivation
		ReaderConcurrency:           mt.ReaderConcurrency,
		WriterConcurrency:           mt.WriterConcurrency,
		MeasurementDurationMs:       mt.MeasurementDurationMs,
		WarmupDurationMs:            mt.WarmupDurationMs,
		CooldownDurationMs:          mt.CooldownDurationMs,
		KeySizeBytes:                19,
		ValSizeBytes:                128,
		KeyspaceCardinality:         mt.KeyspaceCardinality,
		InitialSSTableCount:         mt.InitialSSTableCount,
		InitialKeysPerSSTable:       mt.InitialKeysPerSSTable,
		ReadDistribution:            "uniform",
		ZipfS:                       0.99,
		TargetReadHitRatio:          mt.TargetReadHitRatio,
		MemtableThresholdBytes:      mt.MemtableThresholdBytes,
		CompactionTriggerIntervalMs: mt.CompactionTriggerIntervalMs,
		FlushTriggerIntervalMs:      mt.FlushTriggerIntervalMs,
		ShamFlush:                   mt.ShamFlush,
		DataDir:                     trialDir,
		WalSync:                     mt.WalSync,
	}

	tEnv := env
	tEnv.DataDirPath = trialDir
	tEnv.FilesystemType = prov.FilesystemType

	logFn(fmt.Sprintf("Executing trial %s (Pair %d, Order %d, Seed %d)...", mt.TrialID, mt.PairIndex, mt.OrderInPair, mt.PairSeed))

	res, err := harness.RunTrial(cfg, prov.GitCommit, tEnv)
	if err != nil {
		return nil, fmt.Errorf("trial execution error for %s: %w", mt.TrialID, err)
	}

	// Save individual JSON
	jsonPath := filepath.Join(outDir, mt.TrialID+".json")
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal trial result JSON: %w", err)
	}
	if err := os.WriteFile(jsonPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write trial result file %s: %w", jsonPath, err)
	}

	// Append to results.csv
	csvPath := filepath.Join(outDir, "results.csv")
	row := TrialToCSVRow(mt, res)
	if err := AppendCSVRow(csvPath, row); err != nil {
		return nil, fmt.Errorf("append to results.csv: %w", err)
	}

	logFn(fmt.Sprintf("Trial %s completed: %s (Reads: %d, Writes: %d, Read p95: %.2f us)",
		mt.TrialID, res.Status, res.Operations.ReadOpsCompleted, res.Operations.WriteOpsCompleted,
		float64(res.LatencyRead.P50Nanos)/1000.0))

	return res, nil
}
