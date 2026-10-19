package harness

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"owndbms/lsmengine"
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

func TestLatencyRecorder_SingleSample(t *testing.T) {
	rec := NewLatencyRecorder(100)
	rec.RecordNanos(5000) // 5 µs

	stats := rec.Snapshot()
	if stats.Count != 1 {
		t.Fatalf("expected count 1, got %d", stats.Count)
	}
	if stats.MinNanos != 5000 || stats.MaxNanos != 5000 || stats.P50Nanos != 5000 {
		t.Fatalf("expected min=5000, max=5000, p50=5000, got min=%d, max=%d, p50=%d",
			stats.MinNanos, stats.MaxNanos, stats.P50Nanos)
	}
	if stats.P95Nanos != nil {
		t.Fatalf("expected p95 to be nil for sample size < 20, got %v", *stats.P95Nanos)
	}
	if stats.P99Nanos != nil {
		t.Fatalf("expected p99 to be nil for sample size < 100, got %v", *stats.P99Nanos)
	}
	if stats.P999Nanos != nil {
		t.Fatalf("expected p999 to be nil for sample size < 1000, got %v", *stats.P999Nanos)
	}
	if stats.ZeroCount != 0 {
		t.Fatalf("expected zero count 0, got %d", stats.ZeroCount)
	}
	if stats.OverflowCount != 0 {
		t.Fatalf("expected overflow count 0, got %d", stats.OverflowCount)
	}
}

func TestLatencyRecorder_PartiallyFilledBuffer(t *testing.T) {
	rec := NewLatencyRecorder(1000) // capacity 1000
	for i := 1; i <= 100; i++ {      // write only 100
		rec.RecordNanos(int64(i * 1000))
	}

	stats := rec.Snapshot()
	if stats.Count != 100 {
		t.Fatalf("expected count 100, got %d", stats.Count)
	}
	if stats.OverflowCount != 0 {
		t.Fatalf("expected overflow count 0, got %d", stats.OverflowCount)
	}
	if stats.MinNanos != 1000 {
		t.Fatalf("expected min 1000ns, got %d", stats.MinNanos)
	}
	if stats.MaxNanos != 100000 {
		t.Fatalf("expected max 100000ns, got %d", stats.MaxNanos)
	}
	if stats.P50Nanos != 50000 { // nearest rank 50
		t.Fatalf("expected p50 50000ns, got %d", stats.P50Nanos)
	}
	if stats.P95Nanos == nil || *stats.P95Nanos != 95000 { // nearest rank 95
		t.Fatalf("expected p95 95000ns, got %v", stats.P95Nanos)
	}
	if stats.P99Nanos == nil || *stats.P99Nanos != 99000 { // nearest rank 99
		t.Fatalf("expected p99 99000ns, got %v", stats.P99Nanos)
	}
	if stats.P999Nanos != nil {
		t.Fatalf("expected p999 to be nil when n < 1000, got %v", *stats.P999Nanos)
	}
}

func TestLatencyRecorder_CapacityOverflow(t *testing.T) {
	rec := NewLatencyRecorder(50)
	for i := 1; i <= 120; i++ {
		rec.RecordNanos(int64(i * 100))
	}

	stats := rec.Snapshot()
	if stats.Count != 50 {
		t.Fatalf("expected count capped at 50, got %d", stats.Count)
	}
	if stats.OverflowCount != 70 {
		t.Fatalf("expected overflow count 70, got %d", stats.OverflowCount)
	}
	if stats.MaxNanos != 5000 {
		t.Fatalf("expected max 5000ns (50th sample), got %d", stats.MaxNanos)
	}
}

func TestLatencyRecorder_ZeroSampleAccounting(t *testing.T) {
	rec := NewLatencyRecorder(100)
	rec.RecordNanos(0)
	rec.RecordNanos(100)
	rec.RecordNanos(0)
	rec.RecordNanos(200)

	stats := rec.Snapshot()
	if stats.Count != 4 {
		t.Fatalf("expected count 4, got %d", stats.Count)
	}
	if stats.ZeroCount != 2 {
		t.Fatalf("expected zero count 2, got %d", stats.ZeroCount)
	}
	if stats.MinNanos != 0 {
		t.Fatalf("expected min 0ns, got %d", stats.MinNanos)
	}
}

func TestLatencyRecorder_P999SampleThreshold(t *testing.T) {
	rec := NewLatencyRecorder(2000)
	for i := 1; i <= 1000; i++ {
		rec.RecordNanos(int64(i * 10))
	}

	stats := rec.Snapshot()
	if stats.Count != 1000 {
		t.Fatalf("expected count 1000, got %d", stats.Count)
	}
	if stats.P999Nanos == nil {
		t.Fatalf("expected non-nil p999 for sample size 1000")
	}
	if *stats.P999Nanos != 9990 { // rank 999
		t.Fatalf("expected p999 9990ns, got %d", *stats.P999Nanos)
	}
}

func TestPhaseCounters_Accounting(t *testing.T) {
	pc := makePhaseCounters(10, 2, 50, 100, 15, 5, 80, 250)
	if pc.StartFlushes != 10 || pc.EndFlushes != 15 || pc.DeltaFlushes != 5 {
		t.Fatalf("flush accounting mismatch: %+v", pc)
	}
	if pc.StartCompactions != 2 || pc.EndCompactions != 5 || pc.DeltaCompactions != 3 {
		t.Fatalf("compaction accounting mismatch: %+v", pc)
	}
	if pc.StartSSTableReads != 50 || pc.EndSSTableReads != 80 || pc.DeltaSSTableReads != 30 {
		t.Fatalf("sstable read accounting mismatch: %+v", pc)
	}
	if pc.StartMemtableReads != 100 || pc.EndMemtableReads != 250 || pc.DeltaMemtableReads != 150 {
		t.Fatalf("memtable read accounting mismatch: %+v", pc)
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
			if res.LSMDiagnostics.MeasuredSSTablePointReads == 0 {
				t.Fatalf("expected real measured SSTable reads, got 0")
			}
			if res.LSMDiagnostics.MeasuredFlushes != 0 {
				t.Fatalf("expected 0 measured flushes in read-only, got %d", res.LSMDiagnostics.MeasuredFlushes)
			}
			if res.LSMDiagnostics.MeasuredCompactions != 0 {
				t.Fatalf("expected 0 measured compactions in read-only, got %d", res.LSMDiagnostics.MeasuredCompactions)
			}
			if res.Operations.WriteOpsCompleted != 0 {
				t.Fatalf("expected 0 measured writes in read-only, got %d", res.Operations.WriteOpsCompleted)
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
			cfg.KeyspaceCardinality = 400
			cfg.WriterConcurrency = 2
			cfg.MemtableThresholdBytes = 512 // 512B threshold to quickly force flushes within short test
			cfg.MeasurementDurationMs = 800
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
			if res.LSMDiagnostics.MeasuredFlushes == 0 {
				t.Fatalf("expected measured flushes to be >= 1 under 512B threshold, got 0")
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
			cfg.KeyspaceCardinality = 300
			cfg.WriterConcurrency = 1
			cfg.MemtableThresholdBytes = 16 * 1024
			cfg.CompactionTriggerIntervalMs = 100
			cfg.MeasurementDurationMs = 500
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.LSMDiagnostics.MeasuredCompactions == 0 {
				t.Fatalf("expected measured compactions to occur, got 0")
			}
			if res.LSMDiagnostics.CompactionActiveOverlapMs <= 0 {
				t.Fatalf("expected positive compaction overlap duration, got %d", res.LSMDiagnostics.CompactionActiveOverlapMs)
			}
			if res.LSMDiagnostics.ReadsDuringCompaction <= 0 {
				t.Fatalf("expected reads during active compaction, got %d", res.LSMDiagnostics.ReadsDuringCompaction)
			}
		})
	}
}

func TestHarness_WorkloadD_ReaderChurnSmoke(t *testing.T) {
	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultTrialConfig(WorkloadD_ReaderChurn, mode, 2)
			cfg.DataDir = dir
			cfg.InitialSSTableCount = 3
			cfg.InitialKeysPerSSTable = 100
			cfg.KeyspaceCardinality = 300
			cfg.WriterConcurrency = 3
			cfg.MemtableThresholdBytes = 512
			cfg.CompactionTriggerIntervalMs = 100
			cfg.MeasurementDurationMs = 800
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.LSMDiagnostics.MeasuredFlushes == 0 {
				t.Fatalf("expected measured flushes in churn, got 0")
			}
			if res.LSMDiagnostics.MeasuredCompactions == 0 {
				t.Fatalf("expected measured compactions in churn, got 0")
			}
			if res.LSMDiagnostics.ReadsDuringCompaction <= 0 {
				t.Fatalf("expected reads during compaction in churn, got %d", res.LSMDiagnostics.ReadsDuringCompaction)
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
			cfg.MemtableThresholdBytes = 64 * 1024 * 1024
			cfg.MeasurementDurationMs = 300
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.LSMDiagnostics.MeasuredMemtablePointReads == 0 {
				t.Fatalf("expected memtable reads, got 0")
			}
			if res.LSMDiagnostics.MeasuredSSTablePointReads != 0 {
				t.Fatalf("expected 0 SSTable reads in negative control, got %d", res.LSMDiagnostics.MeasuredSSTablePointReads)
			}
			// High-resolution timer check: p50 should be > 0 ns
			if res.LatencyRead.P50Nanos <= 0 {
				t.Fatalf("expected positive p50 under precision timer, got %d", res.LatencyRead.P50Nanos)
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

func TestSampleSufficiency_Rules(t *testing.T) {
	thresholds := []struct {
		n       int
		wantP95 bool
		wantP99 bool
		want999 bool
	}{
		{n: 1, wantP95: false, wantP99: false, want999: false},
		{n: 19, wantP95: false, wantP99: false, want999: false},
		{n: 20, wantP95: true, wantP99: false, want999: false},
		{n: 99, wantP95: true, wantP99: false, want999: false},
		{n: 100, wantP95: true, wantP99: true, want999: false},
		{n: 999, wantP95: true, wantP99: true, want999: false},
		{n: 1000, wantP95: true, wantP99: true, want999: true},
	}

	for _, tc := range thresholds {
		t.Run(fmt.Sprintf("N=%d", tc.n), func(t *testing.T) {
			rec := NewLatencyRecorder(tc.n + 10)
			for i := 1; i <= tc.n; i++ {
				rec.RecordNanos(int64(i * 100))
			}
			stats := rec.Snapshot()
			if stats.Count != int64(tc.n) {
				t.Fatalf("expected count %d, got %d", tc.n, stats.Count)
			}
			if stats.P50Nanos <= 0 {
				t.Fatalf("expected positive p50, got %d", stats.P50Nanos)
			}
			if (stats.P95Nanos != nil) != tc.wantP95 {
				t.Fatalf("N=%d: wantP95=%v, got non-nil=%v", tc.n, tc.wantP95, stats.P95Nanos != nil)
			}
			if (stats.P99Nanos != nil) != tc.wantP99 {
				t.Fatalf("N=%d: wantP99=%v, got non-nil=%v", tc.n, tc.wantP99, stats.P99Nanos != nil)
			}
			if (stats.P999Nanos != nil) != tc.want999 {
				t.Fatalf("N=%d: want999=%v, got non-nil=%v", tc.n, tc.want999, stats.P999Nanos != nil)
			}
		})
	}
}

func TestKeySampler_HitRatioConvergence(t *testing.T) {
	sampler := NewKeySampler(42, 10000, 0.95, "uniform", 0.99)
	n := 100000
	hits := 0
	for i := 0; i < n; i++ {
		_, hit := sampler.SampleKey()
		if hit {
			hits++
		}
	}
	hitRatio := float64(hits) / float64(n)
	if hitRatio < 0.945 || hitRatio > 0.955 {
		t.Fatalf("expected hit ratio near 0.95 (0.945-0.955), got %.4f (%d/%d)", hitRatio, hits, n)
	}
}

func TestKeySampler_IntendedMissesAbsent(t *testing.T) {
	dir := t.TempDir()
	manCfg := lsmengine.Config{
		Mode:                   lsmengine.SyncModeApproachA,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 64 * 1024 * 1024,
	}
	man, err := lsmengine.CreateManagerWithConfig(manCfg)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}
	defer man.Close()

	// Populate keys 0..999
	cardinality := 1000
	for i := 0; i < cardinality; i++ {
		tx := man.Begin()
		tx.Put(FormatKey(i), FormatVal(i, 64))
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit failed: %v", err)
		}
	}

	sampler := NewKeySampler(12345, cardinality, 0.50, "uniform", 0.99)
	for i := 0; i < 2000; i++ {
		key, expectHit := sampler.SampleKey()
		val, err := man.Get(key, math.MaxUint64)
		if expectHit {
			if err != nil {
				t.Fatalf("expected hit for %s, got error: %v", key, err)
			}
			if len(val) == 0 {
				t.Fatalf("expected non-empty val for %s", key)
			}
		} else {
			if !errors.Is(err, lsmengine.ErrNotFound) {
				t.Fatalf("expected ErrNotFound for absent key %s, got: val=%q err=%v", key, val, err)
			}
		}
	}
}

func TestWorkloadE_100PercentMemtableHits(t *testing.T) {
	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := DefaultTrialConfig(WorkloadE_NegativeControlMemtable, mode, 2)
			cfg.DataDir = dir
			cfg.InitialSSTableCount = 0
			cfg.KeyspaceCardinality = 2000
			cfg.TargetReadHitRatio = 1.0
			cfg.WriterConcurrency = 1
			cfg.MemtableThresholdBytes = 64 * 1024 * 1024
			cfg.MeasurementDurationMs = 400
			cfg.WarmupDurationMs = 50

			res, err := RunTrial(cfg, "test_rev", dummyEnv(dir))
			if err != nil {
				t.Fatalf("RunTrial failed for %s: %v", mode, err)
			}

			if res.Status != "SUCCESS" {
				t.Fatalf("expected SUCCESS, got %s", res.Status)
			}
			if res.Operations.ValidationErrorCount != 0 {
				t.Fatalf("expected 0 validation errors, got %d", res.Operations.ValidationErrorCount)
			}
			if res.Operations.ActualMissCount != 0 {
				t.Fatalf("expected 0 misses under 100%% target, got %d", res.Operations.ActualMissCount)
			}
			if res.Operations.ActualHitCount != res.Operations.ReadOpsCompleted {
				t.Fatalf("expected actual hits (%d) == read ops completed (%d)",
					res.Operations.ActualHitCount, res.Operations.ReadOpsCompleted)
			}
			if res.Operations.ActualMemtableHitCount != res.Operations.ReadOpsCompleted {
				t.Fatalf("expected memtable hits (%d) == read ops completed (%d)",
					res.Operations.ActualMemtableHitCount, res.Operations.ReadOpsCompleted)
			}
			if res.Operations.ActualSSTableHitCount != 0 {
				t.Fatalf("expected 0 SSTable hits, got %d", res.Operations.ActualSSTableHitCount)
			}
			if res.LSMDiagnostics.MeasuredSSTablePointReads != 0 {
				t.Fatalf("expected 0 measured SSTable reads, got %d", res.LSMDiagnostics.MeasuredSSTablePointReads)
			}
			if res.LSMDiagnostics.MeasuredMemtablePointReads != uint64(res.Operations.ReadOpsCompleted) {
				t.Fatalf("expected LSM memtable reads (%d) == read ops completed (%d)",
					res.LSMDiagnostics.MeasuredMemtablePointReads, res.Operations.ReadOpsCompleted)
			}
		})
	}
}

func TestTimerOverheadCharacterization(t *testing.T) {
	const n = 100000
	t0 := time.Now()
	for i := 0; i < n; i++ {
		start := NowPrecision()
		_ = SincePrecisionNanos(start)
	}
	elapsed := time.Since(t0)
	meanNanos := float64(elapsed.Nanoseconds()) / float64(n)
	t.Logf("Timer overhead characterization: N=%d, total=%v, mean=%.2f ns/call", n, elapsed, meanNanos)
}
