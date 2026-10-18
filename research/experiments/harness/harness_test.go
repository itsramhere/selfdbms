package harness

import (
	"os"
	"testing"
	"time"
)

func dummyEnv(dataDir string) EnvironmentInfo {
	return EnvironmentInfo{
		OS:             "test_os",
		Kernel:         "test_kernel",
		CPUModel:       "test_cpu",
		PhysicalCores:  4,
		LogicalCores:   8,
		TotalRAMBytes:  16 * 1024 * 1024 * 1024,
		FreeRAMBytes:   8 * 1024 * 1024 * 1024,
		GoVersion:      "go_test",
		GOMAXPROCS:     8,
		FilesystemType: "test_fs",
		DataDirPath:    dataDir,
	}
}

func TestLatencyRecorder_Statistics(t *testing.T) {
	rec := NewLatencyRecorder(100)
	for i := 1; i <= 100; i++ {
		rec.Record(time.Duration(i) * time.Microsecond)
	}

	stats := rec.Snapshot()
	if stats.Count != 100 {
		t.Fatalf("expected count 100, got %d", stats.Count)
	}
	if stats.MinNanos != 1000 {
		t.Fatalf("expected min 1000ns, got %d", stats.MinNanos)
	}
	if stats.MaxNanos != 100000 {
		t.Fatalf("expected max 100000ns, got %d", stats.MaxNanos)
	}
	if stats.P50Nanos != 51000 { // index 50: 51µs
		t.Fatalf("expected p50 51000ns, got %d", stats.P50Nanos)
	}
	if stats.P95Nanos != 96000 { // index 95: 96µs
		t.Fatalf("expected p95 96000ns, got %d", stats.P95Nanos)
	}
	if stats.P99Nanos != 100000 { // index 99: 100µs
		t.Fatalf("expected p99 100000ns, got %d", stats.P99Nanos)
	}
}

func TestHarness_WorkloadA_SmokeParity(t *testing.T) {
	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultTrialConfig(WorkloadA_ReadOnly, mode, 2)
			cfg.DataDir = dir
			cfg.InitialSSTableCount = 2
			cfg.InitialKeysPerSSTable = 200
			cfg.KeyspaceCardinality = 1000
			cfg.MeasurementDurationMs = 300
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.Operations.ReadOpsCompleted == 0 {
				t.Fatalf("expected non-zero reads completed, got 0")
			}
			if res.LSMDiagnostics.SSTablePointReads == 0 {
				t.Fatalf("expected real SSTable reads, got 0")
			}
		})
	}
}

func TestHarness_WorkloadB_MixedFlushSmoke(t *testing.T) {
	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultTrialConfig(WorkloadB_MixedFlush, mode, 2)
			cfg.DataDir = dir
			cfg.InitialSSTableCount = 2
			cfg.InitialKeysPerSSTable = 200
			cfg.WriterConcurrency = 2
			cfg.MemtableThresholdBytes = 16 * 1024 // 16KB threshold to quickly force flushes
			cfg.MeasurementDurationMs = 400
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.Operations.WriteOpsCompleted == 0 {
				t.Fatalf("expected non-zero writes, got 0")
			}
			if res.LSMDiagnostics.FlushesCompleted == 0 {
				t.Fatalf("expected flushes to occur under flush threshold, got 0")
			}
		})
	}
}

func TestHarness_WorkloadC_CompactionSmoke(t *testing.T) {
	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultTrialConfig(WorkloadC_CompactionOverlap, mode, 2)
			cfg.DataDir = dir
			cfg.InitialSSTableCount = 3
			cfg.InitialKeysPerSSTable = 100
			cfg.WriterConcurrency = 1
			cfg.CompactionTriggerIntervalMs = 150
			cfg.MeasurementDurationMs = 500
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.LSMDiagnostics.CompactionsCompleted == 0 {
				t.Fatalf("expected compactions to occur under trigger interval, got 0")
			}
		})
	}
}

func TestHarness_WorkloadE_NegativeControlSmoke(t *testing.T) {
	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultTrialConfig(WorkloadE_NegativeControlMemtable, mode, 2)
			cfg.DataDir = dir
			cfg.InitialSSTableCount = 0
			cfg.WriterConcurrency = 1
			cfg.MeasurementDurationMs = 300
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.LSMDiagnostics.MemtablePointReads == 0 {
				t.Fatalf("expected memtable reads, got 0")
			}
			if res.LSMDiagnostics.SSTablePointReads != 0 {
				t.Fatalf("expected 0 SSTable reads in negative control, got %d", res.LSMDiagnostics.SSTablePointReads)
			}
		})
	}
}

func TestSaveAndLoadResultJSON(t *testing.T) {
	dir := t.TempDir()
	filePath := dir + "/result.json"

	res := &TrialResult{
		SchemaVersion: "1.0.0",
		TrialID:       "test_save",
		Status:        "SUCCESS",
	}

	if err := SaveResultJSON(res, filePath); err != nil {
		t.Fatalf("SaveResultJSON failed: %v", err)
	}

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file not found after SaveResultJSON: %v", err)
	}
}
