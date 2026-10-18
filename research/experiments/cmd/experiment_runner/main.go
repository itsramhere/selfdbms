package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
		gitRevFlag     = flag.String("git-rev", "0b0021ef3a16c918e19812d8c8322015755396bd", "Git revision identifier")
	)
	flag.Parse()

	if *smokeFlag {
		runSmokeSuite(*smokeOutputDir, *gitRevFlag)
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
		case harness.WorkloadB_MixedFlush:
			cfg.WriterConcurrency = 2
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 256 // 256B threshold to force measured flushes
		case harness.WorkloadC_CompactionOverlap:
			cfg.WriterConcurrency = 1
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 32 * 1024
			cfg.CompactionTriggerIntervalMs = 300
		case harness.WorkloadD_ReaderChurn:
			cfg.WriterConcurrency = 3
			cfg.InitialSSTableCount = 4
			cfg.MemtableThresholdBytes = 32 * 1024
			cfg.CompactionTriggerIntervalMs = 300
		case harness.WorkloadE_NegativeControlMemtable:
			cfg.WriterConcurrency = 2
			cfg.InitialSSTableCount = 0
			cfg.MemtableThresholdBytes = 64 * 1024 * 1024
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
	if res.LatencyRead.P99Nanos > 0 {
		p99Val = float64(res.LatencyRead.P99Nanos) / 1000.0
	}
	fmt.Printf("[RUNNER] Trial completed: %d reads, %d writes in %dms (%.1f read ops/sec, p50: %.2f µs, p99: %.2f µs)\n",
		res.Operations.ReadOpsCompleted, res.Operations.WriteOpsCompleted,
		res.Timestamps.TotalDurationMs, res.Throughput.ReadOpsPerSec,
		float64(res.LatencyRead.P50Nanos)/1000.0, p99Val)
	fmt.Printf("[RUNNER] Measured LSM activity: Flushes=%d, Compactions=%d, SSTableReads=%d, MemtableReads=%d | OverlapMs=%d, ReadsInOverlap=%d\n",
		res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions,
		res.LSMDiagnostics.MeasuredSSTablePointReads, res.LSMDiagnostics.MeasuredMemtablePointReads,
		res.LSMDiagnostics.CompactionActiveOverlapMs, res.LSMDiagnostics.ReadsDuringCompaction)

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
		harness.WorkloadB_MixedFlush,
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
			case harness.WorkloadB_MixedFlush:
				cfg.WriterConcurrency = 2
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 256 // 256B threshold so 2 writes trigger a measured flush even under extreme RWMutex contention
			case harness.WorkloadC_CompactionOverlap:
				cfg.WriterConcurrency = 1
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 32 * 1024
				cfg.CompactionTriggerIntervalMs = 300
			case harness.WorkloadD_ReaderChurn:
				cfg.WriterConcurrency = 3
				cfg.InitialSSTableCount = 4
				cfg.MemtableThresholdBytes = 32 * 1024
				cfg.CompactionTriggerIntervalMs = 300
			case harness.WorkloadE_NegativeControlMemtable:
				cfg.WriterConcurrency = 2
				cfg.InitialSSTableCount = 0
				cfg.MemtableThresholdBytes = 64 * 1024 * 1024
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
			fmt.Printf("        -> Done: %d reads, %d writes | MeasuredFlushes: %d, MeasuredCompactions: %d | SSTableReads: %d, MemtableReads: %d | OverlapMs: %d, ReadsInOverlap: %d | p50: %.2f µs\n",
				res.Operations.ReadOpsCompleted, res.Operations.WriteOpsCompleted,
				res.LSMDiagnostics.MeasuredFlushes, res.LSMDiagnostics.MeasuredCompactions,
				res.LSMDiagnostics.MeasuredSSTablePointReads, res.LSMDiagnostics.MeasuredMemtablePointReads,
				res.LSMDiagnostics.CompactionActiveOverlapMs, res.LSMDiagnostics.ReadsDuringCompaction,
				p50Us)
		}
	}
	fmt.Println("================================================================================")
	fmt.Printf("[SMOKE] Validation suite completed. Results archived in %s\n", outDir)
	fmt.Println("================================================================================")
}
