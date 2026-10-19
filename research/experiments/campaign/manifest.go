package campaign

import (
	"fmt"
	"strings"

	"owndbms/research/experiments/harness"
)

const (
	PlatformWindowsNTFS = "windows_ntfs"
	PlatformWSL2Ext4     = "wsl2_ext4"
)

// SupportedPlatforms returns the two canonical evaluation platforms.
var SupportedPlatforms = []string{PlatformWindowsNTFS, PlatformWSL2Ext4}

// SupportedWorkloads defines the 6 formal evaluation workloads.
var SupportedWorkloads = []harness.WorkloadID{
	harness.WorkloadA_ReadOnly,
	harness.WorkloadB1_NaturalWriteProgress,
	harness.WorkloadB2_ControlledFlushInterference,
	harness.WorkloadC_CompactionOverlap,
	harness.WorkloadD_ReaderChurn,
	harness.WorkloadE_NegativeControlMemtable,
}

// SupportedReaderConcurrencies defines the 8 concurrency levels required by the matrix.
var SupportedReaderConcurrencies = []int{1, 2, 4, 8, 12, 16, 24, 32}

// SupportedSyncModes defines the 2 synchronization modes under scientific comparison.
var SupportedSyncModes = []string{
	"ApproachA_RWMutexBaseline",
	"ApproachB_ReaderSnapshot",
}

const (
	TotalWorkloads     = 6
	TotalReaderLevels  = 8
	TotalSyncModes     = 2
	TotalPlatforms     = 2
	RepetitionsPerCell = 10

	TrialsPerPlatform = TotalWorkloads * TotalReaderLevels * TotalSyncModes * RepetitionsPerCell // 960
	TotalPlannedTrials = TrialsPerPlatform * TotalPlatforms                                        // 1,920
)

// PlannedTrial documents a single planned matrix trial with deterministic coordinates and parameters.
type PlannedTrial struct {
	Platform          string             `json:"platform"`
	CellID            string             `json:"cell_id"`
	RunID             string             `json:"run_id"`
	WorkloadID        harness.WorkloadID `json:"workload_id"`
	SyncMode          string             `json:"sync_mode"`
	ReaderConcurrency int                `json:"reader_concurrency"`
	Repetition        int                `json:"repetition"`
	Seed              int64              `json:"seed"`
	BaseConfig        harness.TrialConfig `json:"base_config"`
}

// ShortModeName returns "A" or "B" for compact identifiers.
func ShortModeName(mode string) string {
	if strings.Contains(mode, "ApproachA") || mode == "A" {
		return "A"
	}
	return "B"
}

// FormatCellID computes the canonical cell identifier: <platform>/<workload>/<mode>/r<readers>.
func FormatCellID(platform string, workload harness.WorkloadID, mode string, readers int) string {
	return fmt.Sprintf("%s/%s/%s/r%02d", platform, workload, ShortModeName(mode), readers)
}

// FormatRunID computes the canonical run identifier: <platform>_<workload>_<mode>_r<readers>_rep<rep>.
func FormatRunID(platform string, workload harness.WorkloadID, mode string, readers, rep int) string {
	return fmt.Sprintf("%s_%s_%s_r%02d_rep%02d", platform, workload, ShortModeName(mode), readers, rep)
}

// GeneratePlannedManifest returns the complete, deterministically ordered list of all 1,920 planned trials.
func GeneratePlannedManifest(durationMs, warmupMs int) []PlannedTrial {
	trials := make([]PlannedTrial, 0, TotalPlannedTrials)
	for _, platform := range SupportedPlatforms {
		trials = append(trials, GeneratePlatformManifest(platform, durationMs, warmupMs)...)
	}
	return trials
}

// GeneratePlatformManifest returns all 960 planned trials for a specific platform.
// Execution ordering strictly interleaves Approach A and Approach B for identical configurations
// across independent repetitions to prevent temporal bias or thermal confounding.
func GeneratePlatformManifest(platform string, durationMs, warmupMs int) []PlannedTrial {
	trials := make([]PlannedTrial, 0, TrialsPerPlatform)

	for rep := 1; rep <= RepetitionsPerCell; rep++ {
		for wIdx, wID := range SupportedWorkloads {
			for _, readers := range SupportedReaderConcurrencies {
				for mIdx, mode := range SupportedSyncModes {
					seed := int64(42000000 + (wIdx * 100000) + (readers * 1000) + (rep * 10) + mIdx)
					cfg := harness.DefaultTrialConfig(wID, mode, readers)
					cfg.TrialID = FormatRunID(platform, wID, mode, readers, rep)
					cfg.TrialIndex = rep - 1
					cfg.Seed = seed
					if durationMs > 0 {
						cfg.MeasurementDurationMs = durationMs
					}
					if warmupMs > 0 {
						cfg.WarmupDurationMs = warmupMs
					}

					// Workload-specific calibrated parameters
					switch wID {
					case harness.WorkloadA_ReadOnly:
						cfg.WriterConcurrency = 0
						cfg.InitialSSTableCount = 4
						cfg.InitialKeysPerSSTable = 2500
						cfg.KeyspaceCardinality = 10000
						cfg.TargetReadHitRatio = 0.95
						cfg.WalSync = true

					case harness.WorkloadB1_NaturalWriteProgress:
						cfg.WriterConcurrency = 2
						cfg.InitialSSTableCount = 4
						cfg.InitialKeysPerSSTable = 2500
						cfg.KeyspaceCardinality = 10000
						cfg.MemtableThresholdBytes = 1024 * 1024 // 1MB threshold
						cfg.TargetReadHitRatio = 0.95
						cfg.WalSync = true

					case harness.WorkloadB2_ControlledFlushInterference:
						cfg.WriterConcurrency = 0 // Coordinator-driven
						cfg.InitialSSTableCount = 4
						cfg.InitialKeysPerSSTable = 2500
						cfg.KeyspaceCardinality = 10000
						cfg.MemtableThresholdBytes = 64 * 1024 * 1024
						cfg.FlushTriggerIntervalMs = 500 // Synchronized flush every 500ms
						cfg.TargetReadHitRatio = 0.95
						cfg.WalSync = true

					case harness.WorkloadC_CompactionOverlap:
						cfg.WriterConcurrency = 1
						cfg.InitialSSTableCount = 4
						cfg.InitialKeysPerSSTable = 2500
						cfg.KeyspaceCardinality = 10000
						cfg.MemtableThresholdBytes = 64 * 1024 * 1024
						cfg.CompactionTriggerIntervalMs = 500 // Background compactor every 500ms
						cfg.TargetReadHitRatio = 0.95
						cfg.WalSync = true

					case harness.WorkloadD_ReaderChurn:
						cfg.WriterConcurrency = 3
						cfg.InitialSSTableCount = 4
						cfg.InitialKeysPerSSTable = 2500
						cfg.KeyspaceCardinality = 10000
						cfg.MemtableThresholdBytes = 16 * 1024 // 16KB threshold
						cfg.CompactionTriggerIntervalMs = 600 // Compaction every 600ms for repeated churn
						cfg.TargetReadHitRatio = 0.95
						cfg.WalSync = true

					case harness.WorkloadE_NegativeControlMemtable:
						cfg.WriterConcurrency = 2
						cfg.InitialSSTableCount = 0
						cfg.InitialKeysPerSSTable = 0
						cfg.KeyspaceCardinality = 2000
						cfg.MemtableThresholdBytes = 64 * 1024 * 1024
						cfg.TargetReadHitRatio = 1.0 // 100% memtable hit rate
						cfg.WalSync = true
					}

					pt := PlannedTrial{
						Platform:          platform,
						CellID:            FormatCellID(platform, wID, mode, readers),
						RunID:             cfg.TrialID,
						WorkloadID:        wID,
						SyncMode:          mode,
						ReaderConcurrency: readers,
						Repetition:        rep,
						Seed:              seed,
						BaseConfig:        cfg,
					}
					trials = append(trials, pt)
				}
			}
		}
	}

	return trials
}
