package harness

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"owndbms/lsmengine"
)

// EnvironmentInfo captures machine and OS runtime characteristics.
type EnvironmentInfo struct {
	OS             string `json:"os"`
	Kernel         string `json:"kernel"`
	CPUModel       string `json:"cpu_model"`
	PhysicalCores  int    `json:"physical_cores"`
	LogicalCores   int    `json:"logical_cores"`
	TotalRAMBytes  int64  `json:"total_ram_bytes"`
	FreeRAMBytes   int64  `json:"free_ram_bytes"`
	GoVersion      string `json:"go_version"`
	GOMAXPROCS     int    `json:"gomaxprocs"`
	FilesystemType string `json:"filesystem_type"`
	DataDirPath    string `json:"data_dir_path"`
}

// TimestampInfo captures trial timing boundaries.
type TimestampInfo struct {
	StartISO              string `json:"start_iso"`
	FinishISO             string `json:"finish_iso"`
	WarmupDurationMs      int    `json:"warmup_duration_ms"`
	MeasurementDurationMs int    `json:"measurement_duration_ms"`
	TotalDurationMs       int    `json:"total_duration_ms"`
}

// OperationCounts tracks attempted and completed operations across all workers.
type OperationCounts struct {
	ReadOpsAttempted       int64 `json:"read_ops_attempted"`
	ReadOpsCompleted       int64 `json:"read_ops_completed"`
	ExpectedHitCount       int64 `json:"expected_hit_count"`
	ExpectedMissCount      int64 `json:"expected_miss_count"`
	ActualHitCount         int64 `json:"actual_hit_count"`
	ActualMissCount        int64 `json:"actual_miss_count"`
	ActualMemtableHitCount int64 `json:"actual_memtable_hit_count"`
	ActualSSTableHitCount  int64 `json:"actual_sstable_hit_count"`
	WriteOpsAttempted      int64 `json:"write_ops_attempted"`
	WriteOpsCompleted      int64 `json:"write_ops_completed"`
	ErrorOpsCount          int64 `json:"error_ops_count"`
	TimeoutOpsCount        int64 `json:"timeout_ops_count"`
	ValidationErrorCount   int64 `json:"validation_error_count"`
}

// ThroughputInfo captures completed operations per second.
type ThroughputInfo struct {
	ReadOpsPerSec  float64 `json:"read_ops_per_sec"`
	WriteOpsPerSec float64 `json:"write_ops_per_sec"`
	TotalOpsPerSec float64 `json:"total_ops_per_sec"`
}

// PhaseCounters records initial, final, and delta values of LSM counters across an experiment phase.
type PhaseCounters struct {
	StartFlushes       uint64 `json:"start_flushes"`
	EndFlushes         uint64 `json:"end_flushes"`
	DeltaFlushes       uint64 `json:"delta_flushes"`
	StartCompactions   uint64 `json:"start_compactions"`
	EndCompactions     uint64 `json:"end_compactions"`
	DeltaCompactions   uint64 `json:"delta_compactions"`
	StartSSTableReads  uint64 `json:"start_sstable_reads"`
	EndSSTableReads    uint64 `json:"end_sstable_reads"`
	DeltaSSTableReads  uint64 `json:"delta_sstable_reads"`
	StartMemtableReads uint64 `json:"start_memtable_reads"`
	EndMemtableReads   uint64 `json:"end_memtable_reads"`
	DeltaMemtableReads uint64 `json:"delta_memtable_reads"`
}

// PhaseMetrics captures phase-scoped counter accounting across all experiment phases.
type PhaseMetrics struct {
	FixtureSetup PhaseCounters `json:"fixture_setup"`
	Warmup       PhaseCounters `json:"warmup"`
	Measured     PhaseCounters `json:"measured"`
	Cooldown     PhaseCounters `json:"cooldown"`
	Shutdown     PhaseCounters `json:"shutdown"`
}

// LSMDiagnostics tracks internal LSM-tree counters, phase metrics, and snapshot health.
type LSMDiagnostics struct {
	InitialSSTables           int          `json:"initial_sstables"`
	FinalSSTables             int          `json:"final_sstables"`
	FlushesCompleted          uint64       `json:"flushes_completed"`
	CompactionsCompleted      uint64       `json:"compactions_completed"`
	SSTablePointReads         uint64       `json:"sstable_point_reads"`
	MemtablePointReads        uint64       `json:"memtable_point_reads"`
	MeasuredFlushes           uint64       `json:"measured_flushes"`
	MeasuredCompactions       uint64       `json:"measured_compactions"`
	MeasuredSSTablePointReads uint64       `json:"measured_sstable_point_reads"`
	MeasuredMemtablePointReads uint64      `json:"measured_memtable_point_reads"`
	SnapshotSeq               uint64       `json:"snapshot_seq"`
	ActiveSnapshotPins        int64        `json:"active_snapshot_pins"`
	RetiredSnapshots          int          `json:"retired_snapshots_count"`
	PendingCleanupCount       int          `json:"pending_cleanup_count"`
	LivePhysicalReaders       int          `json:"live_physical_readers"`
	CompactionActiveOverlapMs int64        `json:"compaction_active_overlap_ms"`
	ReadsDuringCompaction     int64        `json:"reads_during_compaction_count"`
	PhaseMetrics              PhaseMetrics `json:"phase_metrics"`
}

// HostDiagnostics records memory and GC metrics during the trial.
type HostDiagnostics struct {
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64 `json:"heap_sys_bytes"`
	NumGC          uint32 `json:"num_gc"`
	GCPauseTotalNs uint64 `json:"gc_pause_total_ns"`
}

// TrialConfig specifies parameters for an individual experimental run.
type TrialConfig struct {
	SchemaVersion               string     `json:"schema_version"`
	TrialID                     string     `json:"trial_id"`
	TrialIndex                  int        `json:"trial_index"`
	WorkloadID                  WorkloadID `json:"workload_id"`
	SyncMode                    string     `json:"sync_mode"`
	Seed                        int64      `json:"seed"`
	ReaderConcurrency           int        `json:"reader_concurrency"`
	WriterConcurrency           int        `json:"writer_concurrency"`
	MeasurementDurationMs       int        `json:"measurement_duration_ms"`
	WarmupDurationMs            int        `json:"warmup_duration_ms"`
	CooldownDurationMs          int        `json:"cooldown_duration_ms"`
	KeySizeBytes                int        `json:"key_size_bytes"`
	ValSizeBytes                int        `json:"val_size_bytes"`
	KeyspaceCardinality         int        `json:"keyspace_cardinality"`
	InitialSSTableCount         int        `json:"initial_sstable_count"`
	InitialKeysPerSSTable       int        `json:"initial_keys_per_sstable"`
	ReadDistribution            string     `json:"read_distribution"`
	ZipfS                       float64    `json:"zipf_s"`
	TargetReadHitRatio          float64    `json:"target_read_hit_ratio"`
	MemtableThresholdBytes      int        `json:"memtable_threshold_bytes"`
	DataDir                     string     `json:"data_dir"`
	WalSync                     bool       `json:"wal_sync"`
	CompactionTriggerIntervalMs int        `json:"compaction_trigger_interval_ms"`
}

// DefaultTrialConfig returns a robust default configuration for testing and smoke verification.
func DefaultTrialConfig(workload WorkloadID, mode string, readers int) TrialConfig {
	return TrialConfig{
		SchemaVersion:               "1.0.0",
		TrialID:                     fmt.Sprintf("trial_%s_%s_r%d_%d", workload, mode, readers, time.Now().UnixNano()),
		TrialIndex:                  0,
		WorkloadID:                  workload,
		SyncMode:                    mode,
		Seed:                        42,
		ReaderConcurrency:           readers,
		WriterConcurrency:           0,
		MeasurementDurationMs:       2000,
		WarmupDurationMs:            200,
		CooldownDurationMs:          100,
		KeySizeBytes:                19,
		ValSizeBytes:                128,
		KeyspaceCardinality:         10000,
		InitialSSTableCount:         4,
		InitialKeysPerSSTable:       2500,
		ReadDistribution:            "uniform",
		ZipfS:                       0.99,
		TargetReadHitRatio:          0.95,
		MemtableThresholdBytes:      64 * 1024 * 1024,
		DataDir:                     "",
		WalSync:                     true,
		CompactionTriggerIntervalMs: 0,
	}
}

// TrialResult captures complete outputs and diagnostics from an individual run.
type TrialResult struct {
	SchemaVersion   string          `json:"schema_version"`
	TrialID         string          `json:"trial_id"`
	TrialIndex      int             `json:"trial_index"`
	WorkloadID      string          `json:"workload_id"`
	SyncMode        string          `json:"sync_mode"`
	GitRevision     string          `json:"git_revision"`
	Environment     EnvironmentInfo `json:"environment"`
	Config          TrialConfig     `json:"config"`
	Timestamps      TimestampInfo   `json:"timestamps"`
	Operations      OperationCounts `json:"operations"`
	Throughput      ThroughputInfo  `json:"throughput"`
	LatencyRead     LatencyStats    `json:"latency_read_nanos"`
	LatencyWrite    LatencyStats    `json:"latency_write_nanos"`
	LSMDiagnostics  LSMDiagnostics  `json:"lsm_diagnostics"`
	HostDiagnostics HostDiagnostics `json:"host_diagnostics"`
	Status          string          `json:"status"`
	ExclusionReason *string         `json:"exclusion_reason"`
}

func captureLSMCounts(man *lsmengine.Manager) (flushes, compactions, sstReads, memReads uint64) {
	if man == nil {
		return 0, 0, 0, 0
	}
	return man.FlushesCompleted(), man.CompactionsCompleted(), man.SSTablePointReads(), man.MemtablePointReads()
}

func makePhaseCounters(sFlushes, sCompactions, sSstReads, sMemReads, eFlushes, eCompactions, eSstReads, eMemReads uint64) PhaseCounters {
	return PhaseCounters{
		StartFlushes:       sFlushes,
		EndFlushes:         eFlushes,
		DeltaFlushes:       eFlushes - sFlushes,
		StartCompactions:   sCompactions,
		EndCompactions:     eCompactions,
		DeltaCompactions:   eCompactions - sCompactions,
		StartSSTableReads:  sSstReads,
		EndSSTableReads:    eSstReads,
		DeltaSSTableReads:  eSstReads - sSstReads,
		StartMemtableReads: sMemReads,
		EndMemtableReads:   eMemReads,
		DeltaMemtableReads: eMemReads - sMemReads,
	}
}

// RunTrial executes a single controlled experiment trial under strict isolation.
func RunTrial(cfg TrialConfig, gitRev string, env EnvironmentInfo) (*TrialResult, error) {
	if cfg.DataDir == "" {
		tmp, err := os.MkdirTemp("", "selfdbms_trial_*")
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary data dir: %w", err)
		}
		cfg.DataDir = tmp
	}
	defer os.RemoveAll(cfg.DataDir)

	var lsmMode lsmengine.SyncMode
	switch cfg.SyncMode {
	case "ApproachA_RWMutexBaseline", "A":
		lsmMode = lsmengine.SyncModeApproachA
		cfg.SyncMode = "ApproachA_RWMutexBaseline"
	case "ApproachB_ReaderSnapshot", "B":
		lsmMode = lsmengine.SyncModeApproachB
		cfg.SyncMode = "ApproachB_ReaderSnapshot"
	default:
		return nil, fmt.Errorf("unsupported sync mode: %s", cfg.SyncMode)
	}

	manCfg := lsmengine.Config{
		Mode:                   lsmMode,
		WalPath:                filepath.Join(cfg.DataDir, "wal.log"),
		DataDir:                cfg.DataDir,
		MemtableThresholdBytes: 64 * 1024 * 1024, // Keep large during fixture setup to prevent premature flushes
	}

	man, err := lsmengine.CreateManagerWithConfig(manCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create manager: %w", err)
	}

	// Phase 1: Fixture Setup Phase
	f0, c0, s0, m0 := captureLSMCounts(man)
	baseSeed := cfg.Seed + int64(cfg.TrialIndex)*10007
	if cfg.InitialSSTableCount > 0 && cfg.WorkloadID != WorkloadE_NegativeControlMemtable {
		keysPerTable := cfg.InitialKeysPerSSTable
		if keysPerTable*cfg.InitialSSTableCount < cfg.KeyspaceCardinality {
			keysPerTable = (cfg.KeyspaceCardinality + cfg.InitialSSTableCount - 1) / cfg.InitialSSTableCount
		}
		currKey := 0
		for t := 0; t < cfg.InitialSSTableCount; t++ {
			targetFlushes := man.FlushesCompleted() + 1
			for k := 0; k < keysPerTable; k++ {
				if currKey >= cfg.KeyspaceCardinality && t == cfg.InitialSSTableCount-1 {
					break
				}
				keyIdx := currKey % cfg.KeyspaceCardinality
				keyStr := FormatKey(keyIdx)
				valStr := FormatVal(keyIdx, cfg.ValSizeBytes)
				tx := man.Begin()
				tx.Put(keyStr, valStr)
				if err := tx.Commit(); err != nil {
					man.Close()
					return nil, fmt.Errorf("fixture initialization commit failed: %w", err)
				}
				currKey++
			}
			if err := man.TriggerFlush(); err != nil {
				man.Close()
				return nil, fmt.Errorf("fixture flush trigger failed: %w", err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for man.FlushesCompleted() < targetFlushes && time.Now().Before(deadline) {
				time.Sleep(2 * time.Millisecond)
			}
			if man.FlushesCompleted() < targetFlushes {
				man.Close()
				return nil, fmt.Errorf("fixture flush timed out for table %d", t)
			}
		}
	} else if cfg.WorkloadID == WorkloadE_NegativeControlMemtable {
		// Populate active Memtable directly without flushing
		for k := 0; k < cfg.KeyspaceCardinality; k++ {
			keyStr := FormatKey(k)
			valStr := FormatVal(k, cfg.ValSizeBytes)
			tx := man.Begin()
			tx.Put(keyStr, valStr)
			if err := tx.Commit(); err != nil {
				man.Close()
				return nil, fmt.Errorf("workload E memtable population failed: %w", err)
			}
		}
	}
	f1, c1, s1, m1 := captureLSMCounts(man)
	phaseSetup := makePhaseCounters(f0, c0, s0, m0, f1, c1, s1, m1)
	initialSSTables := man.ActiveSSTableCount()

	// Apply trial memtable threshold dynamically for warmup and measurement phases
	man.SetMemtableThresholdBytes(cfg.MemtableThresholdBytes)

	// Phase 2: Warm-up Phase
	if cfg.WarmupDurationMs > 0 {
		warmupStop := make(chan struct{})
		var warmupWg sync.WaitGroup
		for r := 0; r < cfg.ReaderConcurrency; r++ {
			warmupWg.Add(1)
			wSeed := baseSeed + int64(r)*101 + 999
			go func(seed int64) {
				defer warmupWg.Done()
				warmupSampler := NewKeySampler(seed, cfg.KeyspaceCardinality, cfg.TargetReadHitRatio, cfg.ReadDistribution, cfg.ZipfS)
				for {
					select {
					case <-warmupStop:
						return
					default:
						k, _ := warmupSampler.SampleKey()
						_, _ = man.Get(k, math.MaxUint64)
					}
				}
			}(wSeed)
		}
		time.Sleep(time.Duration(cfg.WarmupDurationMs) * time.Millisecond)
		close(warmupStop)
		warmupWg.Wait()
	}
	f2, c2, s2, m2 := captureLSMCounts(man)
	phaseWarmup := makePhaseCounters(f1, c1, s1, m1, f2, c2, s2, m2)

	// Phase 3: Steady-State Measurement Phase
	readLatency := NewLatencyRecorder(500000)
	writeLatency := NewLatencyRecorder(100000)
	var (
		readAttempted          atomic.Int64
		readCompleted          atomic.Int64
		expectedHit            atomic.Int64
		expectedMiss           atomic.Int64
		actualHit              atomic.Int64
		actualMiss             atomic.Int64
		writeAttempted         atomic.Int64
		writeCompleted         atomic.Int64
		errorOps               atomic.Int64
		timeoutOps             atomic.Int64
		validationErrs         atomic.Int64
		compactionActive       atomic.Bool
		compactionOverlapNanos atomic.Int64
		readsDuringCompaction  atomic.Int64
		stopMeasure            = make(chan struct{})
		startBarrier           = make(chan struct{})
		workerWg               sync.WaitGroup
	)

	// Launch reader workers
	for r := 0; r < cfg.ReaderConcurrency; r++ {
		workerWg.Add(1)
		readerSeed := baseSeed + int64(r)*101 + 1
		go func(seed int64) {
			defer workerWg.Done()
			sampler := NewKeySampler(seed, cfg.KeyspaceCardinality, cfg.TargetReadHitRatio, cfg.ReadDistribution, cfg.ZipfS)
			<-startBarrier
			for {
				select {
				case <-stopMeasure:
					return
				default:
					key, expectHit := sampler.SampleKey()
					if expectHit {
						expectedHit.Add(1)
					} else {
						expectedMiss.Add(1)
					}
					readAttempted.Add(1)
					t0 := NowPrecision()
					val, err := man.Get(key, math.MaxUint64)
					durNanos := SincePrecisionNanos(t0)
					readLatency.RecordNanos(durNanos)

					if compactionActive.Load() {
						readsDuringCompaction.Add(1)
					}

					if err == nil {
						readCompleted.Add(1)
						actualHit.Add(1)
						if !expectHit || len(val) == 0 {
							validationErrs.Add(1)
						}
					} else if err == lsmengine.ErrNotFound {
						readCompleted.Add(1)
						actualMiss.Add(1)
						if expectHit {
							validationErrs.Add(1)
						}
					} else {
						errorOps.Add(1)
					}
				}
			}
		}(readerSeed)
	}

	// Launch writer workers if configured
	if cfg.WriterConcurrency > 0 {
		for w := 0; w < cfg.WriterConcurrency; w++ {
			workerWg.Add(1)
			writerSeed := baseSeed + int64(w)*503 + 2000
			go func(seed int64, workerID int) {
				defer workerWg.Done()
				rnd := rand.New(rand.NewSource(seed))
				<-startBarrier
				counter := 0
				for {
					select {
					case <-stopMeasure:
						return
					default:
						counter++
						keyIdx := rnd.Intn(cfg.KeyspaceCardinality)
						key := FormatKey(keyIdx)
						val := FormatVal(keyIdx, cfg.ValSizeBytes)
						writeAttempted.Add(1)
						t0 := NowPrecision()
						tx := man.Begin()
						tx.Put(key, val)
						err := tx.Commit()
						durNanos := SincePrecisionNanos(t0)
						writeLatency.RecordNanos(durNanos)
						if err == nil {
							writeCompleted.Add(1)
						} else {
							errorOps.Add(1)
						}
					}
				}
			}(writerSeed, w)
		}
	}

	// Launch background compactor if interval configured
	if cfg.CompactionTriggerIntervalMs > 0 {
		workerWg.Add(1)
		go func() {
			defer workerWg.Done()
			<-startBarrier
			ticker := time.NewTicker(time.Duration(cfg.CompactionTriggerIntervalMs) * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopMeasure:
					return
				case <-ticker.C:
					compactionActive.Store(true)
					cStart := NowPrecision()
					_ = man.CompactAll()
					cDurNanos := SincePrecisionNanos(cStart)
					compactionActive.Store(false)
					compactionOverlapNanos.Add(cDurNanos)
				}
			}
		}()
	}

	// Trigger measurement start
	startISO := time.Now().UTC().Format(time.RFC3339Nano)
	measureStart := time.Now()
	close(startBarrier)

	// Measure for configured duration
	time.Sleep(time.Duration(cfg.MeasurementDurationMs) * time.Millisecond)
	close(stopMeasure)
	workerWg.Wait()
	measureDuration := time.Since(measureStart)
	finishISO := time.Now().UTC().Format(time.RFC3339Nano)

	f3, c3, s3, m3 := captureLSMCounts(man)
	phaseMeasured := makePhaseCounters(f2, c2, s2, m2, f3, c3, s3, m3)

	// Phase 4: Cooldown Phase
	if cfg.CooldownDurationMs > 0 {
		time.Sleep(time.Duration(cfg.CooldownDurationMs) * time.Millisecond)
	}
	f4, c4, s4, m4 := captureLSMCounts(man)
	phaseCooldown := makePhaseCounters(f3, c3, s3, m3, f4, c4, s4, m4)

	// Extract pre-close diagnostics
	finalSSTables := man.ActiveSSTableCount()
	snapSeq := man.SnapshotSeq()
	activePins := man.ActiveSnapshotPins()
	retiredSnaps := man.RetiredSnapshotCount()
	pendingClean := man.PendingCleanupCount()
	livePhysical := man.LivePhysicalReaders()

	var mStats runtime.MemStats
	runtime.ReadMemStats(&mStats)

	// Phase 5: Shutdown Phase
	closeErr := man.Close()
	if closeErr != nil {
		errorOps.Add(1)
	}
	f5, c5, s5, m5 := captureLSMCounts(man)
	phaseShutdown := makePhaseCounters(f4, c4, s4, m4, f5, c5, s5, m5)

	secs := measureDuration.Seconds()
	readOps := readCompleted.Load()
	writeOps := writeCompleted.Load()

	status := "SUCCESS"
	var exclusionReason *string
	if closeErr != nil {
		status = "FAILED"
		msg := fmt.Sprintf("engine close failed: %v", closeErr)
		exclusionReason = &msg
	}

	result := &TrialResult{
		SchemaVersion: "1.0.0",
		TrialID:       cfg.TrialID,
		TrialIndex:    cfg.TrialIndex,
		WorkloadID:    string(cfg.WorkloadID),
		SyncMode:      cfg.SyncMode,
		GitRevision:   gitRev,
		Environment:   env,
		Config:        cfg,
		Timestamps: TimestampInfo{
			StartISO:              startISO,
			FinishISO:             finishISO,
			WarmupDurationMs:      cfg.WarmupDurationMs,
			MeasurementDurationMs: cfg.MeasurementDurationMs,
			TotalDurationMs:       int(measureDuration.Milliseconds()),
		},
		Operations: OperationCounts{
			ReadOpsAttempted:       readAttempted.Load(),
			ReadOpsCompleted:       readOps,
			ExpectedHitCount:       expectedHit.Load(),
			ExpectedMissCount:      expectedMiss.Load(),
			ActualHitCount:         actualHit.Load(),
			ActualMissCount:        actualMiss.Load(),
			ActualMemtableHitCount: int64(phaseMeasured.DeltaMemtableReads),
			ActualSSTableHitCount:  int64(phaseMeasured.DeltaSSTableReads),
			WriteOpsAttempted:      writeAttempted.Load(),
			WriteOpsCompleted:      writeOps,
			ErrorOpsCount:          errorOps.Load(),
			TimeoutOpsCount:        timeoutOps.Load(),
			ValidationErrorCount:   validationErrs.Load(),
		},
		Throughput: ThroughputInfo{
			ReadOpsPerSec:  float64(readOps) / secs,
			WriteOpsPerSec: float64(writeOps) / secs,
			TotalOpsPerSec: float64(readOps+writeOps) / secs,
		},
		LatencyRead:  readLatency.Snapshot(),
		LatencyWrite: writeLatency.Snapshot(),
		LSMDiagnostics: LSMDiagnostics{
			InitialSSTables:           initialSSTables,
			FinalSSTables:             finalSSTables,
			FlushesCompleted:          f5,
			CompactionsCompleted:      c5,
			SSTablePointReads:         s5,
			MemtablePointReads:        m5,
			MeasuredFlushes:           phaseMeasured.DeltaFlushes,
			MeasuredCompactions:       phaseMeasured.DeltaCompactions,
			MeasuredSSTablePointReads: phaseMeasured.DeltaSSTableReads,
			MeasuredMemtablePointReads: phaseMeasured.DeltaMemtableReads,
			SnapshotSeq:               snapSeq,
			ActiveSnapshotPins:        activePins,
			RetiredSnapshots:          retiredSnaps,
			PendingCleanupCount:       pendingClean,
			LivePhysicalReaders:       livePhysical,
			CompactionActiveOverlapMs: compactionOverlapNanos.Load() / 1_000_000,
			ReadsDuringCompaction:     readsDuringCompaction.Load(),
			PhaseMetrics: PhaseMetrics{
				FixtureSetup: phaseSetup,
				Warmup:       phaseWarmup,
				Measured:     phaseMeasured,
				Cooldown:     phaseCooldown,
				Shutdown:     phaseShutdown,
			},
		},
		HostDiagnostics: HostDiagnostics{
			HeapAllocBytes: mStats.HeapAlloc,
			HeapSysBytes:   mStats.HeapSys,
			NumGC:          mStats.NumGC,
			GCPauseTotalNs: mStats.PauseTotalNs,
		},
		Status:          status,
		ExclusionReason: exclusionReason,
	}

	return result, nil
}

// SaveResultJSON writes the trial result to disk formatted as indentation-spaced JSON.
func SaveResultJSON(res *TrialResult, targetFile string) error {
	if err := os.MkdirAll(filepath.Dir(targetFile), 0755); err != nil {
		return err
	}
	bytes, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(targetFile, bytes, 0644)
}
