package campaign

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"owndbms/research/experiments/harness"
)

func TestCampaignManifest_Exact1920Trials(t *testing.T) {
	manifest := GeneratePlannedManifest(2000, 200)

	if len(manifest) != TotalPlannedTrials {
		t.Fatalf("expected exactly %d total planned trials, got %d", TotalPlannedTrials, len(manifest))
	}
	if TotalPlannedTrials != 1920 {
		t.Fatalf("expected TotalPlannedTrials == 1920, got %d", TotalPlannedTrials)
	}

	winManifest := GeneratePlatformManifest(PlatformWindowsNTFS, 2000, 200)
	if len(winManifest) != TrialsPerPlatform {
		t.Fatalf("expected %d trials for Windows NTFS, got %d", TrialsPerPlatform, len(winManifest))
	}
	if TrialsPerPlatform != 960 {
		t.Fatalf("expected TrialsPerPlatform == 960, got %d", TrialsPerPlatform)
	}

	wslManifest := GeneratePlatformManifest(PlatformWSL2Ext4, 2000, 200)
	if len(wslManifest) != TrialsPerPlatform {
		t.Fatalf("expected %d trials for WSL2 ext4, got %d", TrialsPerPlatform, len(wslManifest))
	}

	// Verify workload counts
	workloadCounts := make(map[harness.WorkloadID]int)
	readerCounts := make(map[int]int)
	modeCounts := make(map[string]int)
	cellIDs := make(map[string]int)
	runIDs := make(map[string]int)

	for _, pt := range manifest {
		workloadCounts[pt.WorkloadID]++
		readerCounts[pt.ReaderConcurrency]++
		modeCounts[pt.SyncMode]++
		cellIDs[pt.CellID]++
		runIDs[pt.RunID]++

		if pt.RunID == "" || pt.CellID == "" {
			t.Fatalf("empty RunID or CellID for %+v", pt)
		}
	}

	// Every workload: 8 readers * 2 modes * 2 platforms * 10 reps = 320 trials
	for _, w := range SupportedWorkloads {
		if count := workloadCounts[w]; count != 320 {
			t.Fatalf("expected 320 trials for workload %s, got %d", w, count)
		}
	}

	// 8 reader concurrencies: 1, 2, 4, 8, 12, 16, 24, 32
	expectedReaders := []int{1, 2, 4, 8, 12, 16, 24, 32}
	if len(readerCounts) != 8 {
		t.Fatalf("expected 8 reader concurrency levels, got %d", len(readerCounts))
	}
	for _, r := range expectedReaders {
		// Each reader level: 6 workloads * 2 modes * 2 platforms * 10 reps = 240 trials
		if count := readerCounts[r]; count != 240 {
			t.Fatalf("expected 240 trials for reader concurrency %d, got %d", r, count)
		}
	}

	// 2 modes: 6 workloads * 8 readers * 2 platforms * 10 reps = 960 trials
	for _, m := range SupportedSyncModes {
		if count := modeCounts[m]; count != 960 {
			t.Fatalf("expected 960 trials for mode %s, got %d", m, count)
		}
	}

	// Exactly 192 cells (6 * 8 * 2 * 2 = 192), each having 10 repetitions
	if len(cellIDs) != 192 {
		t.Fatalf("expected 192 unique cell IDs, got %d", len(cellIDs))
	}
	for cell, count := range cellIDs {
		if count != 10 {
			t.Fatalf("expected 10 repetitions for cell %s, got %d", cell, count)
		}
	}

	// Every RunID must be globally unique
	if len(runIDs) != 1920 {
		t.Fatalf("expected 1920 unique run IDs, got %d", len(runIDs))
	}
}

func TestCampaignAuditor_EmptyAndSimulatedResults(t *testing.T) {
	tmpDir := t.TempDir()
	manifest := GeneratePlatformManifest(PlatformWindowsNTFS, 2000, 200)[:10] // Take 10 trials

	// Audit empty directory
	report, err := AuditCampaign(tmpDir, manifest)
	if err != nil {
		t.Fatalf("audit failed on empty dir: %v", err)
	}
	if report.TotalPlanned != 10 {
		t.Fatalf("expected 10 planned, got %d", report.TotalPlanned)
	}
	if report.TotalMissing != 10 {
		t.Fatalf("expected 10 missing, got %d", report.TotalMissing)
	}
	if report.TotalValidated != 0 {
		t.Fatalf("expected 0 validated, got %d", report.TotalValidated)
	}

	// Create 1 valid result and 1 failed result
	validPt := manifest[0]
	failedPt := manifest[1]

	validRes := harness.TrialResult{
		SchemaVersion: "1.0.0",
		TrialID:       validPt.RunID,
		WorkloadID:    string(validPt.WorkloadID),
		SyncMode:      validPt.SyncMode,
		Status:        "SUCCESS",
		Operations: harness.OperationCounts{
			ReadOpsCompleted:     100,
			ValidationErrorCount: 0,
		},
	}
	validBytes, _ := json.Marshal(validRes)
	validFile := filepath.Join(tmpDir, PlatformWindowsNTFS, validPt.RunID+".json")
	_ = os.MkdirAll(filepath.Dir(validFile), 0755)
	_ = os.WriteFile(validFile, validBytes, 0644)

	failReason := "simulated disk full error"
	failedRes := harness.TrialResult{
		SchemaVersion: "1.0.0",
		TrialID:       failedPt.RunID,
		WorkloadID:    string(failedPt.WorkloadID),
		SyncMode:      failedPt.SyncMode,
		Status:        "FAILED",
		FailureReason: &failReason,
		Operations: harness.OperationCounts{
			ReadOpsCompleted:     0,
			ValidationErrorCount: 0,
		},
	}
	failedBytes, _ := json.Marshal(failedRes)
	failedFile := filepath.Join(tmpDir, PlatformWindowsNTFS, failedPt.RunID+".json")
	_ = os.WriteFile(failedFile, failedBytes, 0644)

	// Re-audit
	report2, err := AuditCampaign(tmpDir, manifest)
	if err != nil {
		t.Fatalf("audit failed on populated dir: %v", err)
	}
	if report2.TotalPlanned != 10 {
		t.Fatalf("expected 10 planned, got %d", report2.TotalPlanned)
	}
	if report2.TotalExecuted != 2 {
		t.Fatalf("expected 2 executed, got %d", report2.TotalExecuted)
	}
	if report2.TotalValidated != 1 {
		t.Fatalf("expected 1 validated, got %d", report2.TotalValidated)
	}
	if report2.TotalFailed != 1 {
		t.Fatalf("expected 1 failed, got %d", report2.TotalFailed)
	}
	if report2.TotalMissing != 8 {
		t.Fatalf("expected 8 missing, got %d", report2.TotalMissing)
	}
	if len(report2.FailedTrials) != 1 {
		t.Fatalf("expected 1 failed trial summary, got %d", len(report2.FailedTrials))
	}
	if report2.FailedTrials[0].FailureReason != failReason {
		t.Fatalf("expected failure reason %q, got %q", failReason, report2.FailedTrials[0].FailureReason)
	}

	reportStr := report2.String()
	if reportStr == "" {
		t.Fatalf("expected non-empty string output from report")
	}
}
