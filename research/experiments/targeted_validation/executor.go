package targeted_validation

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"owndbms/research/experiments/harness"
)

var csvMutex sync.Mutex

// CSVHeader returns the standard column headers for the targeted results CSV.
func CSVHeader() []string {
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
		"seed",
		"status",
		"read_ops_completed",
		"read_ops_per_sec",
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

// ResultToCSVRow converts a trial execution result to a flat CSV row.
func ResultToCSVRow(mt *ManifestTrial, res *harness.TrialResult) []string {
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
		strconv.FormatInt(mt.Seed, 10),
		res.Status,
		strconv.FormatInt(res.Operations.ReadOpsCompleted, 10),
		fmt.Sprintf("%.2f", res.Throughput.ReadOpsPerSec),
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

// AppendCSVRow safely appends a single row to results.csv with header initialization.
func AppendCSVRow(csvPath string, row []string) error {
	csvMutex.Lock()
	defer csvMutex.Unlock()

	needHeader := false
	if _, err := os.Stat(csvPath); os.IsNotExist(err) {
		needHeader = true
	}

	f, err := os.OpenFile(csvPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open csv %s: %w", csvPath, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if needHeader {
		if err := w.Write(CSVHeader()); err != nil {
			return fmt.Errorf("write csv header: %w", err)
		}
	}
	if err := w.Write(row); err != nil {
		return fmt.Errorf("write csv row: %w", err)
	}
	w.Flush()
	return w.Error()
}

// ExecuteTargetedTrial executes an individual trial from the manifest.
func ExecuteTargetedTrial(mt *ManifestTrial, outDir, gitRev string, env harness.EnvironmentInfo, logFn func(string)) (*harness.TrialResult, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	targetFile := filepath.Join(outDir, fmt.Sprintf("%s.json", mt.TrialID))
	csvPath := filepath.Join(outDir, "results.csv")

	// Skip existing if already completed successfully
	if data, err := os.ReadFile(targetFile); err == nil {
		var existing harness.TrialResult
		if err := json.Unmarshal(data, &existing); err == nil && existing.Status == "SUCCESS" {
			logFn(fmt.Sprintf("[SKIP] %s already completed successfully", mt.TrialID))
			return &existing, nil
		}
	}

	if err := harness.EnsurePrecisionTimer(); err != nil {
		return nil, fmt.Errorf("precision timer check failed: %w", err)
	}

	trialDir, err := os.MkdirTemp("", fmt.Sprintf("target_%s_*", mt.TrialID))
	if err != nil {
		return nil, fmt.Errorf("create temp data dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(trialDir)
	}()

	cfg := harness.TrialConfig{
		SchemaVersion:               "1.0.0",
		TrialID:                     mt.TrialID,
		TrialIndex:                  mt.TrialIndex,
		WorkloadID:                  harness.WorkloadID(mt.WorkloadID),
		SyncMode:                    mt.SyncMode,
		Seed:                        mt.Seed,
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
		DataDir:                     trialDir,
		WalSync:                     mt.WalSync,
		CompactionTriggerIntervalMs: mt.CompactionTriggerIntervalMs,
		FlushTriggerIntervalMs:      mt.FlushTriggerIntervalMs,
		ShamFlush:                   mt.ShamFlush,
	}

	tEnv := env
	tEnv.DataDirPath = trialDir

	t0 := time.Now()
	res, runErr := harness.RunTrial(cfg, gitRev, tEnv)
	elapsed := time.Since(t0)

	if runErr != nil {
		errMsg := runErr.Error()
		logFn(fmt.Sprintf("[ERROR] %s failed: %v", mt.TrialID, runErr))
		failedRes := &harness.TrialResult{
			SchemaVersion: "1.0.0",
			TrialID:       mt.TrialID,
			TrialIndex:    mt.TrialIndex,
			WorkloadID:    mt.WorkloadID,
			SyncMode:      mt.SyncMode,
			GitRevision:   gitRev,
			Environment:   tEnv,
			Config:        cfg,
			Timestamps: harness.TimestampInfo{
				StartISO:              t0.UTC().Format(time.RFC3339Nano),
				FinishISO:             time.Now().UTC().Format(time.RFC3339Nano),
				TotalDurationMs:       int(elapsed.Milliseconds()),
				MeasurementDurationMs: cfg.MeasurementDurationMs,
				WarmupDurationMs:      cfg.WarmupDurationMs,
			},
			Status:        "FAILED",
			FailureReason: &errMsg,
		}
		_ = harness.SaveResultJSON(failedRes, targetFile)
		return nil, runErr
	}

	if err := harness.SaveResultJSON(res, targetFile); err != nil {
		return nil, fmt.Errorf("save trial JSON %s: %w", targetFile, err)
	}

	row := ResultToCSVRow(mt, res)
	if err := AppendCSVRow(csvPath, row); err != nil {
		return nil, fmt.Errorf("append CSV row: %w", err)
	}

	p95 := 0.0
	if res.LatencyRead.P95Nanos != nil {
		p95 = float64(*res.LatencyRead.P95Nanos) / 1000.0
	}
	logFn(fmt.Sprintf("[DONE] %s: status=%s, tput=%.1f ops/s, p95=%.1f µs, flushes=%d, compactions=%d",
		mt.TrialID, res.Status, res.Throughput.ReadOpsPerSec, p95,
		res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions))

	return res, nil
}
