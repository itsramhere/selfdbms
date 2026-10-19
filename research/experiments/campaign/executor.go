package campaign

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"owndbms/research/experiments/harness"
)

// ExecutionFilter allows targeting specific subsets of the campaign.
type ExecutionFilter struct {
	Platform          string
	WorkloadID        string
	SyncMode          string
	ReaderConcurrency int
	Repetition        int
	BatchSize         int
	SkipExisting      bool
}

// Matches returns true if the planned trial satisfies the filter criteria.
func (f *ExecutionFilter) Matches(pt *PlannedTrial) bool {
	if f.Platform != "" && pt.Platform != f.Platform {
		return false
	}
	if f.WorkloadID != "" && string(pt.WorkloadID) != f.WorkloadID && !stringsEqualFold(string(pt.WorkloadID), f.WorkloadID) {
		return false
	}
	if f.SyncMode != "" && pt.SyncMode != f.SyncMode && ShortModeName(pt.SyncMode) != ShortModeName(f.SyncMode) {
		return false
	}
	if f.ReaderConcurrency > 0 && pt.ReaderConcurrency != f.ReaderConcurrency {
		return false
	}
	if f.Repetition > 0 && pt.Repetition != f.Repetition {
		return false
	}
	return true
}

func stringsEqualFold(a, b string) bool {
	return len(a) == len(b) && (a == b || (len(a) > 0 && a[0] == b[0]))
}

// ExecuteCampaign executes planned trials matching the filter in bounded, restartable batches.
func ExecuteCampaign(manifest []PlannedTrial, filter ExecutionFilter, baseDataDir, gitRev string, env harness.EnvironmentInfo, logFn func(string)) (executed int, err error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	outDir := filepath.Join(baseDataDir, filter.Platform)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create platform output directory: %w", err)
	}

	for _, pt := range manifest {
		if !filter.Matches(&pt) {
			continue
		}

		targetFile := filepath.Join(outDir, fmt.Sprintf("%s.json", pt.RunID))
		if filter.SkipExisting {
			if data, err := os.ReadFile(targetFile); err == nil {
				var existing harness.TrialResult
				if err := json.Unmarshal(data, &existing); err == nil && existing.Status == "SUCCESS" {
					// Already executed and valid
					continue
				}
			}
		}

		// Check host disk health before each trial
		if err := harness.EnsurePrecisionTimer(); err != nil {
			return executed, fmt.Errorf("precision timer failed before trial %s: %w", pt.RunID, err)
		}

		trialDir, err := os.MkdirTemp("", fmt.Sprintf("campaign_%s_*", pt.RunID))
		if err != nil {
			return executed, fmt.Errorf("failed to create temporary data dir: %w", err)
		}

		cfg := pt.BaseConfig
		cfg.DataDir = trialDir
		tEnv := env
		tEnv.DataDirPath = trialDir

		logFn(fmt.Sprintf("[CAMPAIGN] [%d] Running %s (%s, r=%d, rep=%d)...", executed+1, pt.WorkloadID, pt.SyncMode, pt.ReaderConcurrency, pt.Repetition))
		t0 := time.Now()
		res, runErr := harness.RunTrial(cfg, gitRev, tEnv)
		elapsed := time.Since(t0)
		_ = os.RemoveAll(trialDir)

		if runErr != nil {
			logFn(fmt.Sprintf("[ERROR] Trial %s failed: %v", pt.RunID, runErr))
			errMsg := runErr.Error()
			failedRes := &harness.TrialResult{
				SchemaVersion: "1.0.0",
				TrialID:       pt.RunID,
				TrialIndex:    pt.Repetition - 1,
				WorkloadID:    string(pt.WorkloadID),
				SyncMode:      pt.SyncMode,
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
		} else {
			_ = harness.SaveResultJSON(res, targetFile)
			p50Us := float64(res.LatencyRead.P50Nanos) / 1000.0
			logFn(fmt.Sprintf("         -> Done in %v: %d reads (%.1f/s), p50=%.1fµs | MeasFlushes=%d, MeasComp=%d, OverlapReads=%d",
				elapsed, res.Operations.ReadOpsCompleted, res.Throughput.ReadOpsPerSec, p50Us,
				res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions,
				res.LSMDiagnostics.ReadsDuringFlush+res.LSMDiagnostics.ReadsDuringCompaction))
		}

		executed++
		if filter.BatchSize > 0 && executed >= filter.BatchSize {
			logFn(fmt.Sprintf("[CAMPAIGN] Batch limit reached (%d trials). Halting batch execution.", filter.BatchSize))
			break
		}
	}

	return executed, nil
}
