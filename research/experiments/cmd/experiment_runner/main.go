package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"owndbms/research/experiments/harness"
)

func getEnvironment(dataDir string) harness.EnvironmentInfo {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	fsType := "NTFS"
	if runtime.GOOS == "linux" {
		if strings.HasPrefix(dataDir, "/mnt/c") {
			fsType = "9P_drvfs (WARNING: Windows-backed)"
		} else {
			fsType = "ext4 (Native Linux)"
		}
	}

	return harness.EnvironmentInfo{
		OS:             runtime.GOOS,
		Kernel:         runtime.GOOS + "_" + runtime.GOARCH,
		CPUModel:       "AMD Ryzen 7 7445HS w/ Radeon 740M Graphics",
		PhysicalCores:  6,
		LogicalCores:   runtime.NumCPU(),
		TotalRAMBytes:  16 * 1024 * 1024 * 1024,
		FreeRAMBytes:   int64(m.Sys - m.Alloc),
		GoVersion:      runtime.Version(),
		GOMAXPROCS:     runtime.GOMAXPROCS(0),
		FilesystemType: fsType,
		DataDirPath:    dataDir,
	}
}

func main() {
	var (
		workloadFlag   = flag.String("workload", "WORKLOAD_A_READ_ONLY", "Workload ID (A, B, C, D, E or full name)")
		modeFlag       = flag.String("mode", "A", "Sync mode: A (ApproachA_RWMutexBaseline) or B (ApproachB_ReaderSnapshot)")
		readersFlag    = flag.Int("readers", 4, "Number of reader goroutines")
		writersFlag    = flag.Int("writers", -1, "Number of writer goroutines (-1 uses workload default)")
		durationFlag   = flag.Int("duration", 2000, "Measurement duration in milliseconds")
		warmupFlag     = flag.Int("warmup", 200, "Warm-up duration in milliseconds")
		dirFlag        = flag.String("dir", "", "Target database directory (empty uses temporary directory)")
		outputFlag     = flag.String("output", "", "Output JSON file path")
		smokeFlag      = flag.Bool("smoke", false, "Execute standard preliminary smoke validation suite")
		smokeOutputDir = flag.String("smoke-dir", "research/experiments/data/smoke", "Directory to store smoke test results")
		pilotFlag      = flag.Bool("pilot", false, "Execute official Phase I5 parameter pilot suite (10s duration, 1MB threshold, 500ms compaction)")
		pilotOutputDir = flag.String("pilot-dir", "research/experiments/data/pilot", "Directory to store pilot test results")
		gitRevFlag     = flag.String("git-rev", "0b0021ef3a16c918e19812d8c8322015755396bd", "Git revision identifier")
	)
	flag.Parse()

	if *smokeFlag {
		runSmokeSuite(*smokeOutputDir, *gitRevFlag)
		return
	}

	if *pilotFlag {
		runPilotSuite(*pilotOutputDir, *gitRevFlag)
		return
	}

	wID := parseWorkload(*workloadFlag)
	cfg := harness.DefaultTrialConfig(wID, *modeFlag, *readersFlag)
	cfg.MeasurementDurationMs = *durationFlag
	cfg.WarmupDurationMs = *warmupFlag
	if *writersFlag >= 0 {
		cfg.WriterConcurrency = *writersFlag
	} else {
		switch wID {
		case harness.WorkloadA_ReadOnly:
			cfg.WriterConcurrency = 0
			cfg.InitialSSTableCount = 4
		case harness.WorkloadB_MixedFlush, harness.WorkloadB1_NaturalWriteProgress:
			cfg.WriterConcurrency = 2
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 1024 * 1024 // 1MB natural threshold
		case harness.WorkloadB2_ControlledFlushInterference:
			cfg.WriterConcurrency = 0
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 64 * 1024 * 1024
			cfg.FlushTriggerIntervalMs = 2000
		case harness.WorkloadC_CompactionOverlap:
			cfg.WriterConcurrency = 1
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 64 * 1024 * 1024
			cfg.CompactionTriggerIntervalMs = 500
		case harness.WorkloadD_ReaderChurn:
			cfg.WriterConcurrency = 3
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 16 * 1024 // 16KB threshold to ensure continuous flush generation
			cfg.CompactionTriggerIntervalMs = 1500 // 1500ms compaction interval for repeated churn
		case harness.WorkloadE_NegativeControlMemtable:
			cfg.WriterConcurrency = 2
			cfg.InitialSSTableCount = 0
			cfg.MemtableThresholdBytes = 64 * 1024 * 1024
			cfg.TargetReadHitRatio = 1.0
			cfg.KeyspaceCardinality = 2000
		}
	}
	cfg.DataDir = *dirFlag

	env := getEnvironment(cfg.DataDir)
	fmt.Printf("[RUNNER] Starting trial %s on %s (%s)...\n", cfg.TrialID, env.OS, env.FilesystemType)
	res, err := harness.RunTrial(cfg, *gitRevFlag, env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Trial failed: %v\n", err)
		os.Exit(1)
	}

	p99Val := 0.0
	if res.LatencyRead.P99Nanos != nil && *res.LatencyRead.P99Nanos > 0 {
		p99Val = float64(*res.LatencyRead.P99Nanos) / 1000.0
	}
	fmt.Printf("[RUNNER] Trial completed: %d reads, %d writes in %dms (%.1f read ops/sec, p50: %.2f µs, p99: %.2f µs)\n",
		res.Operations.ReadOpsCompleted, res.Operations.WriteOpsCompleted,
		res.Timestamps.TotalDurationMs, res.Throughput.ReadOpsPerSec,
		float64(res.LatencyRead.P50Nanos)/1000.0, p99Val)
	fmt.Printf("[RUNNER] Measured LSM activity: Flushes=%d, Compactions=%d, SSTableReads=%d, MemtableReads=%d | CompOverlapMs=%d, ReadsInCompOverlap=%d | FlushOverlapMs=%d, ReadsInFlushOverlap=%d\n",
		res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions,
		res.LSMDiagnostics.MeasuredSSTablePointReads, res.LSMDiagnostics.MeasuredMemtablePointReads,
		res.LSMDiagnostics.CompactionActiveOverlapMs, res.LSMDiagnostics.ReadsDuringCompaction,
		res.LSMDiagnostics.FlushActiveOverlapMs, res.LSMDiagnostics.ReadsDuringFlush)

	if *outputFlag != "" {
		if err := harness.SaveResultJSON(res, *outputFlag); err != nil {
			fmt.Fprintf(os.Stderr, "[ERROR] Failed to save JSON: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[RUNNER] Saved result to %s\n", *outputFlag)
	}
}

func parseWorkload(raw string) harness.WorkloadID {
	switch strings.ToUpper(raw) {
	case "A", "WORKLOAD_A", "WORKLOAD_A_READ_ONLY":
		return harness.WorkloadA_ReadOnly
	case "B", "WORKLOAD_B", "WORKLOAD_B_MIXED_FLUSH":
		return harness.WorkloadB_MixedFlush
	case "B1", "WORKLOAD_B1", "WORKLOAD_B1_NATURAL_WRITE_PROGRESS":
		return harness.WorkloadB1_NaturalWriteProgress
	case "B2", "WORKLOAD_B2", "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE":
		return harness.WorkloadB2_ControlledFlushInterference
	case "C", "WORKLOAD_C", "WORKLOAD_C_COMPACTION_OVERLAP":
		return harness.WorkloadC_CompactionOverlap
	case "D", "WORKLOAD_D", "WORKLOAD_D_READER_CHURN":
		return harness.WorkloadD_ReaderChurn
	case "E", "WORKLOAD_E", "WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE":
		return harness.WorkloadE_NegativeControlMemtable
	default:
		return harness.WorkloadA_ReadOnly
	}
}

func runSmokeSuite(outDir string, gitRev string) {
	fmt.Println("================================================================================")
	fmt.Println("PRELIMINARY HARNESS VALIDATION — NOT OFFICIAL PERFORMANCE EVIDENCE.")
	fmt.Println("================================================================================")

	workloads := []harness.WorkloadID{
		harness.WorkloadA_ReadOnly,
		harness.WorkloadB1_NaturalWriteProgress,
		harness.WorkloadB2_ControlledFlushInterference,
		harness.WorkloadC_CompactionOverlap,
		harness.WorkloadD_ReaderChurn,
		harness.WorkloadE_NegativeControlMemtable,
	}

	modes := []string{"A", "B"}
	concurrency := 4
	durationMs := 1500
	warmupMs := 200

	_ = os.MkdirAll(outDir, 0755)

	for _, w := range workloads {
		for _, m := range modes {
			cfg := harness.DefaultTrialConfig(w, m, concurrency)
			cfg.MeasurementDurationMs = durationMs
			cfg.WarmupDurationMs = warmupMs

			switch w {
			case harness.WorkloadA_ReadOnly:
				cfg.WriterConcurrency = 0
				cfg.InitialSSTableCount = 4
			case harness.WorkloadB_MixedFlush, harness.WorkloadB1_NaturalWriteProgress:
				cfg.WriterConcurrency = 2
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 1024 * 1024
			case harness.WorkloadB2_ControlledFlushInterference:
				cfg.WriterConcurrency = 0
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 64 * 1024 * 1024
				cfg.FlushTriggerIntervalMs = 500
			case harness.WorkloadC_CompactionOverlap:
				cfg.WriterConcurrency = 1
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 64 * 1024 * 1024
				cfg.CompactionTriggerIntervalMs = 300
			case harness.WorkloadD_ReaderChurn:
				cfg.WriterConcurrency = 3
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 16 * 1024
				cfg.CompactionTriggerIntervalMs = 500
			case harness.WorkloadE_NegativeControlMemtable:
				cfg.WriterConcurrency = 2
				cfg.InitialSSTableCount = 0
				cfg.MemtableThresholdBytes = 64 * 1024 * 1024
				cfg.TargetReadHitRatio = 1.0
				cfg.KeyspaceCardinality = 2000
			}

			// Dedicated trial directory
			trialDir, err := os.MkdirTemp("", fmt.Sprintf("smoke_%s_%s_*", w, m))
			if err != nil {
				fmt.Printf("Failed to create trial dir: %v\n", err)
				continue
			}
			cfg.DataDir = trialDir

			env := getEnvironment(cfg.DataDir)
			fmt.Printf("[SMOKE] Running %s under Mode %s (duration %dms)...\n", w, m, durationMs)
			res, err := harness.RunTrial(cfg, gitRev, env)
			_ = os.RemoveAll(trialDir)

			if err != nil {
				fmt.Printf("[ERROR] Smoke trial failed: %v\n", err)
				continue
			}

			fileName := fmt.Sprintf("smoke_%s_%s.json", w, m)
			filePath := filepath.Join(outDir, fileName)
			_ = harness.SaveResultJSON(res, filePath)

			p50Us := float64(res.LatencyRead.P50Nanos) / 1000.0
			fmt.Printf("        -> Done: %d reads, %d writes | MeasFlushes: %d, MeasComp: %d | CompOverlapMs: %d, ReadsInComp: %d | FlushOverlapMs: %d, ReadsInFlush: %d | p50: %.2f µs\n",
				res.Operations.ReadOpsCompleted, res.Operations.WriteOpsCompleted,
				res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions,
				res.LSMDiagnostics.CompactionActiveOverlapMs, res.LSMDiagnostics.ReadsDuringCompaction,
				res.LSMDiagnostics.FlushActiveOverlapMs, res.LSMDiagnostics.ReadsDuringFlush,
				p50Us)
		}
	}
	fmt.Println("================================================================================")
	fmt.Printf("[SMOKE] Validation suite completed. Results archived in %s\n", outDir)
	fmt.Println("================================================================================")
}

func runPilotSuite(outDir string, gitRev string) {
	fmt.Println("================================================================================")
	fmt.Println("[PILOT] Executing Bounded Readiness Pilot Suite (10s duration)")
	fmt.Println("================================================================================")

	pilotConfigs := []struct {
		workload harness.WorkloadID
		name     string
		setup    func(cfg *harness.TrialConfig)
	}{
		{
			workload: harness.WorkloadB1_NaturalWriteProgress,
			name:     "Workload B1 (Natural Write Progress, 1MB Memtable Threshold)",
			setup: func(cfg *harness.TrialConfig) {
				cfg.ReaderConcurrency = 4
				cfg.WriterConcurrency = 2
				cfg.MeasurementDurationMs = 10000
				cfg.WarmupDurationMs = 500
				cfg.InitialSSTableCount = 4
				cfg.InitialKeysPerSSTable = 2500
				cfg.KeyspaceCardinality = 10000
				cfg.MemtableThresholdBytes = 1024 * 1024 // Official 1MB threshold
				cfg.TargetReadHitRatio = 0.95
				cfg.WalSync = true
			},
		},
		{
			workload: harness.WorkloadB2_ControlledFlushInterference,
			name:     "Workload B2 (Controlled Flush Interference, 2000ms Flush Trigger Interval)",
			setup: func(cfg *harness.TrialConfig) {
				cfg.ReaderConcurrency = 4
				cfg.WriterConcurrency = 0
				cfg.MeasurementDurationMs = 10000
				cfg.WarmupDurationMs = 500
				cfg.InitialSSTableCount = 4
				cfg.InitialKeysPerSSTable = 2500
				cfg.KeyspaceCardinality = 10000
				cfg.MemtableThresholdBytes = 64 * 1024 * 1024
				cfg.FlushTriggerIntervalMs = 2000
				cfg.TargetReadHitRatio = 0.95
				cfg.WalSync = true
			},
		},
		{
			workload: harness.WorkloadC_CompactionOverlap,
			name:     "Workload C (Compaction Overlap, 500ms Trigger Interval)",
			setup: func(cfg *harness.TrialConfig) {
				cfg.ReaderConcurrency = 4
				cfg.WriterConcurrency = 1
				cfg.MeasurementDurationMs = 10000
				cfg.WarmupDurationMs = 500
				cfg.InitialSSTableCount = 4
				cfg.InitialKeysPerSSTable = 2500
				cfg.KeyspaceCardinality = 10000
				cfg.MemtableThresholdBytes = 64 * 1024 * 1024
				cfg.CompactionTriggerIntervalMs = 500 // Official 500ms interval
				cfg.TargetReadHitRatio = 0.95
				cfg.WalSync = true
			},
		},
		{
			workload: harness.WorkloadD_ReaderChurn,
			name:     "Workload D (Reader Churn, 16KB Threshold, 1500ms Compaction)",
			setup: func(cfg *harness.TrialConfig) {
				cfg.ReaderConcurrency = 4
				cfg.WriterConcurrency = 3
				cfg.MeasurementDurationMs = 10000
				cfg.WarmupDurationMs = 500
				cfg.InitialSSTableCount = 4
				cfg.InitialKeysPerSSTable = 2500
				cfg.KeyspaceCardinality = 10000
				cfg.MemtableThresholdBytes = 16 * 1024 // 16KB threshold to ensure continuous flush generation
				cfg.CompactionTriggerIntervalMs = 1500 // 1500ms compaction interval for repeated churn
				cfg.TargetReadHitRatio = 0.95
				cfg.WalSync = true
			},
		},
	}

	modes := []string{"A", "B"}
	_ = os.MkdirAll(outDir, 0755)

	for _, pc := range pilotConfigs {
		for _, m := range modes {
			cfg := harness.DefaultTrialConfig(pc.workload, m, 4)
			pc.setup(&cfg)

			trialDir, err := os.MkdirTemp("", fmt.Sprintf("pilot_%s_%s_*", pc.workload, m))
			if err != nil {
				fmt.Printf("Failed to create trial dir: %v\n", err)
				continue
			}
			cfg.DataDir = trialDir

			env := getEnvironment(cfg.DataDir)
			fmt.Printf("[PILOT] Running %s under Mode %s on %s (%s)...\n", pc.name, m, env.OS, env.FilesystemType)
			t0 := time.Now()
			res, err := harness.RunTrial(cfg, gitRev, env)
			elapsed := time.Since(t0)
			_ = os.RemoveAll(trialDir)

			if err != nil {
				fmt.Printf("[ERROR] Pilot trial failed: %v\n", err)
				continue
			}

			fileName := fmt.Sprintf("pilot_%s_%s.json", pc.workload, m)
			filePath := filepath.Join(outDir, fileName)
			_ = harness.SaveResultJSON(res, filePath)

			p50Us := float64(res.LatencyRead.P50Nanos) / 1000.0
			p99Us := 0.0
			if res.LatencyRead.P99Nanos != nil {
				p99Us = float64(*res.LatencyRead.P99Nanos) / 1000.0
			}
			hitRate := 0.0
			if res.Operations.ReadOpsCompleted > 0 {
				hitRate = float64(res.Operations.ActualHitCount) / float64(res.Operations.ReadOpsCompleted) * 100.0
			}

			fmt.Printf("        -> Total Wall Time: %v | Measured Reads: %d (%.1f/s), Writes: %d (%.1f/s)\n",
				elapsed, res.Operations.ReadOpsCompleted, res.Throughput.ReadOpsPerSec,
				res.Operations.WriteOpsCompleted, res.Throughput.WriteOpsPerSec)
			fmt.Printf("        -> HitRate: %.1f%%, ValErrors: %d, Flushes: %d, Compactions: %d\n",
				hitRate, res.Operations.ValidationErrorCount,
				res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions)
			fmt.Printf("        -> CompOverlap: %dms, ReadsInComp: %d | FlushOverlap: %dms, ReadsInFlush: %d | p50: %.2f µs, p99: %.2f µs\n\n",
				res.LSMDiagnostics.CompactionActiveOverlapMs, res.LSMDiagnostics.ReadsDuringCompaction,
				res.LSMDiagnostics.FlushActiveOverlapMs, res.LSMDiagnostics.ReadsDuringFlush,
				p50Us, p99Us)
		}
	}
	fmt.Println("================================================================================")
	fmt.Printf("[PILOT] Suite completed. Results saved in %s\n", outDir)
	fmt.Println("================================================================================")
}
