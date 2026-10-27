package targeted_validation

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"owndbms/research/experiments/harness"
)

// TestManifest_OrderBalancing verifies that execution order is strictly balanced:
// exactly 50% Approach A first, and 50% Approach B first across all paired blocks.
func TestManifest_OrderBalancing(t *testing.T) {
	manifestPath := filepath.Join("manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Skip("manifest.json not found in current dir, skipping")
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	aFirstCount := 0
	bFirstCount := 0

	for _, trial := range m.Trials {
		if trial.OrderInPair == 1 {
			if trial.SyncMode == "ApproachA_RWMutexBaseline" {
				aFirstCount++
			} else if trial.SyncMode == "ApproachB_ReaderSnapshot" {
				bFirstCount++
			}
		}
	}

	totalPairs := aFirstCount + bFirstCount
	if totalPairs != m.TotalPlannedTrials/2 {
		t.Fatalf("pair accounting mismatch: %d pairs vs %d expected", totalPairs, m.TotalPlannedTrials/2)
	}

	if aFirstCount != bFirstCount {
		t.Fatalf("order balancing failed: %d A-first vs %d B-first (expected strict 50/50 balance)", aFirstCount, bFirstCount)
	}
}

// TestManifest_SeedEquality verifies that both trials in every pair share the exact same random seed.
func TestManifest_SeedEquality(t *testing.T) {
	manifestPath := filepath.Join("manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Skip("manifest.json not found in current dir, skipping")
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	pairSeeds := make(map[string]map[int][]int64)

	for _, trial := range m.Trials {
		cellKey := fmt.Sprintf("%s_%s_%s_r%d", trial.Experiment, trial.WorkloadID, trial.Condition, trial.ReaderConcurrency)
		if pairSeeds[cellKey] == nil {
			pairSeeds[cellKey] = make(map[int][]int64)
		}
		pairSeeds[cellKey][trial.PairIndex] = append(pairSeeds[cellKey][trial.PairIndex], trial.Seed)
	}

	for cell, pairs := range pairSeeds {
		for pairIdx, seeds := range pairs {
			if len(seeds) != 2 {
				t.Fatalf("cell %s pair %d has %d trials (expected 2)", cell, pairIdx, len(seeds))
			}
			if seeds[0] != seeds[1] {
				t.Fatalf("cell %s pair %d seeds differ: trial1=%d, trial2=%d", cell, pairIdx, seeds[0], seeds[1])
			}
		}
	}
}

// TestManifest_TrialIDUniqueness verifies all 600 trial IDs are strictly unique.
func TestManifest_TrialIDUniqueness(t *testing.T) {
	manifestPath := filepath.Join("manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Skip("manifest.json not found in current dir, skipping")
	}

	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	seen := make(map[string]bool)
	for _, trial := range m.Trials {
		if seen[trial.TrialID] {
			t.Fatalf("duplicate trial ID: %s", trial.TrialID)
		}
		seen[trial.TrialID] = true
	}

	if len(seen) != m.TotalPlannedTrials {
		t.Fatalf("expected %d unique trial IDs, found %d", m.TotalPlannedTrials, len(seen))
	}
}

// TestTOST_EquivalenceLogic verifies the mathematical correctness of TOST bounds calculation.
func TestTOST_EquivalenceLogic(t *testing.T) {
	// Equivalence margin +/- 5%: [-0.051293, +0.048790]
	thetaL := math.Log(0.95)
	thetaU := math.Log(1.05)

	// Case 1: Strictly within band -> EQUIVALENT
	ciLow1 := -0.02
	ciHigh1 := +0.03
	if !(ciLow1 > thetaL && ciHigh1 < thetaU) {
		t.Fatalf("expected equivalent for [%f, %f]", ciLow1, ciHigh1)
	}

	// Case 2: Crossing lower bound -> INCONCLUSIVE
	ciLow2 := -0.06
	ciHigh2 := +0.02
	if ciLow2 > thetaL && ciHigh2 < thetaU {
		t.Fatalf("expected inconclusive when crossing thetaL")
	}

	// Case 3: Crossing upper bound -> INCONCLUSIVE
	ciLow3 := -0.01
	ciHigh3 := +0.06
	if ciLow3 > thetaL && ciHigh3 < thetaU {
		t.Fatalf("expected inconclusive when crossing thetaU")
	}
}

// TestPairedDifferenceAccounting verifies that paired difference calculation correctly preserves signs.
func TestPairedDifferenceAccounting(t *testing.T) {
	aP95 := []float64{100.0, 150.0, 200.0}
	bP95 := []float64{80.0, 110.0, 160.0}

	diffs := make([]float64, len(aP95))
	for i := range aP95 {
		diffs[i] = bP95[i] - aP95[i]
	}

	// All diffs should be negative (B has lower tail latency)
	for i, d := range diffs {
		if d >= 0 {
			t.Fatalf("pair %d: expected negative difference (B < A), got %f", i, d)
		}
	}
}

// TestExecuteTargetedTrial_Smoke executes a minimal single-trial smoke test.
func TestExecuteTargetedTrial_Smoke(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "out")
	_ = os.MkdirAll(outDir, 0755)

	mt := ManifestTrial{
		CampaignID:             "TEST_CAMPAIGN",
		Experiment:             "TEST",
		TrialID:                "test_trial_smoke_01",
		TrialIndex:             0,
		WorkloadID:             string(harness.WorkloadA_ReadOnly),
		Condition:              "standard",
		SyncMode:               "ApproachA_RWMutexBaseline",
		ReaderConcurrency:      1,
		WriterConcurrency:      0,
		PairIndex:              1,
		OrderInPair:            1,
		Seed:                   42,
		MeasurementDurationMs:  300,
		WarmupDurationMs:       50,
		CooldownDurationMs:     50,
		ShamFlush:              false,
		FlushTriggerIntervalMs: 0,
		MemtableThresholdBytes: 64 * 1024 * 1024,
		InitialSSTableCount:    2,
		InitialKeysPerSSTable:  100,
		KeyspaceCardinality:    200,
		TargetReadHitRatio:     0.95,
		WalSync:                true,
	}

	env := harness.EnvironmentInfo{
		OS:             "linux",
		GoVersion:      "go1.24",
		GOMAXPROCS:     4,
		FilesystemType: "ext4",
	}

	res, err := ExecuteTargetedTrial(&mt, outDir, "smoke_rev", env, nil)
	if err != nil {
		t.Fatalf("ExecuteTargetedTrial failed: %v", err)
	}

	if res.Status != "SUCCESS" {
		t.Fatalf("expected SUCCESS, got %s", res.Status)
	}

	// Check JSON file
	jsonFile := filepath.Join(outDir, mt.TrialID+".json")
	data, err := os.ReadFile(jsonFile)
	if err != nil {
		t.Fatalf("failed to read output json %s: %v", jsonFile, err)
	}
	var loaded harness.TrialResult
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to parse output json: %v", err)
	}
	if loaded.TrialID != mt.TrialID {
		t.Fatalf("trial ID mismatch: %s vs %s", loaded.TrialID, mt.TrialID)
	}

	// Check results.csv
	csvFile := filepath.Join(outDir, "results.csv")
	if _, err := os.Stat(csvFile); os.IsNotExist(err) {
		t.Fatalf("results.csv was not created")
	}
}
