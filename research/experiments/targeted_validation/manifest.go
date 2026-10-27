package targeted_validation

import (
	"encoding/json"
	"fmt"
	"os"
)

// ManifestTrial represents a single planned trial in the preregistered manifest.
type ManifestTrial struct {
	CampaignID                  string  `json:"campaign_id"`
	Experiment                  string  `json:"experiment"`
	TrialID                     string  `json:"trial_id"`
	TrialIndex                  int     `json:"trial_index"`
	WorkloadID                  string  `json:"workload_id"`
	Condition                   string  `json:"condition"`
	SyncMode                    string  `json:"sync_mode"`
	ReaderConcurrency           int     `json:"reader_concurrency"`
	WriterConcurrency           int     `json:"writer_concurrency"`
	PairIndex                   int     `json:"pair_index"`
	OrderInPair                 int     `json:"order_in_pair"`
	Seed                        int64   `json:"seed"`
	MeasurementDurationMs       int     `json:"measurement_duration_ms"`
	WarmupDurationMs            int     `json:"warmup_duration_ms"`
	CooldownDurationMs          int     `json:"cooldown_duration_ms"`
	ShamFlush                   bool    `json:"sham_flush"`
	FlushTriggerIntervalMs      int     `json:"flush_trigger_interval_ms"`
	CompactionTriggerIntervalMs int     `json:"compaction_trigger_interval_ms,omitempty"`
	MemtableThresholdBytes      int     `json:"memtable_threshold_bytes"`
	InitialSSTableCount         int     `json:"initial_sstable_count"`
	InitialKeysPerSSTable       int     `json:"initial_keys_per_sstable"`
	KeyspaceCardinality         int     `json:"keyspace_cardinality"`
	TargetReadHitRatio          float64 `json:"target_read_hit_ratio"`
	WalSync                     bool    `json:"wal_sync"`
}

// Manifest represents the top-level preregistered manifest.
type Manifest struct {
	SchemaVersion       string                 `json:"schema_version"`
	CampaignID          string                 `json:"campaign_id"`
	TotalPlannedTrials  int                    `json:"total_planned_trials"`
	ExperimentBreakdown map[string]int         `json:"experiment_breakdown"`
	Trials              []ManifestTrial        `json:"trials"`
}

// LoadManifest reads and parses the preregistered manifest.json from disk.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest file %s: %w", path, err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest JSON: %w", err)
	}

	if len(m.Trials) != m.TotalPlannedTrials {
		return nil, fmt.Errorf("manifest integrity error: expected %d trials, got %d", m.TotalPlannedTrials, len(m.Trials))
	}

	return &m, nil
}
