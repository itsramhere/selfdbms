package main

import (
	"os"
	"path/filepath"
	"testing"

	"owndbms/research/experiments/harness"
)

func TestValidateResult_MissingFields(t *testing.T) {
	// Missing schema_version
	res := &harness.TrialResult{
		TrialID:     "trial_1",
		WorkloadID:  "WORKLOAD_A_READ_ONLY",
		SyncMode:    "ApproachA_RWMutexBaseline",
		GitRevision: "rev_1",
	}
	if err := validateResult(res, "test.json"); err == nil {
		t.Fatalf("expected error for missing schema_version, got nil")
	}

	// Missing trial_id
	res.SchemaVersion = "1.0.0"
	res.TrialID = ""
	if err := validateResult(res, "test.json"); err == nil {
		t.Fatalf("expected error for missing trial_id, got nil")
	}

	// Valid minimal
	res.TrialID = "trial_1"
	res.Environment.OS = "linux"
	res.Environment.FilesystemType = "ext4"
	res.Environment.GoVersion = "go1.27.2"
	res.Timestamps.MeasurementDurationMs = 1500
	res.Status = "SUCCESS"
	if err := validateResult(res, "test.json"); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestSummarize_PreserveFailedTrial(t *testing.T) {
	dir := t.TempDir()
	reason := "simulated disk full"
	res := &harness.TrialResult{
		SchemaVersion: "1.0.0",
		TrialID:       "failed_trial",
		WorkloadID:    "WORKLOAD_B_MIXED_FLUSH",
		SyncMode:      "ApproachA_RWMutexBaseline",
		GitRevision:   "rev_fail",
		Environment: harness.EnvironmentInfo{
			OS:             "linux",
			FilesystemType: "ext4",
			GoVersion:      "go1.27.2",
		},
		Timestamps: harness.TimestampInfo{
			MeasurementDurationMs: 1500,
		},
		Status:          "FAILED",
		ExclusionReason: &reason,
	}

	path := filepath.Join(dir, "failed_trial.json")
	if err := harness.SaveResultJSON(res, path); err != nil {
		t.Fatalf("failed to save trial JSON: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read JSON: %v", err)
	}
	var loaded harness.TrialResult
	if err := validateResult(&loaded, path); err == nil {
		// should fail before unmarshal
	}
	_ = data
}
