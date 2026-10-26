package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"owndbms/research/experiments/campaign"
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

func detectPlatform() string {
	if runtime.GOOS == "windows" {
		return campaign.PlatformWindowsNTFS
	}
	return campaign.PlatformWSL2Ext4
}

func main() {
	var (
		workloadFlag      = flag.String("workload", "WORKLOAD_A_READ_ONLY", "Workload ID (A, B, B1, B2, C, D, E or full name)")
		modeFlag          = flag.String("mode", "A", "Sync mode: A (ApproachA_RWMutexBaseline) or B (ApproachB_ReaderSnapshot)")
		readersFlag       = flag.Int("readers", 4, "Number of reader goroutines")
		writersFlag       = flag.Int("writers", -1, "Number of writer goroutines (-1 uses workload default)")
		durationFlag      = flag.Int("duration", 2000, "Measurement duration in milliseconds")
		warmupFlag        = flag.Int("warmup", 200, "Warm-up duration in milliseconds")
		dirFlag           = flag.String("dir", "", "Target database directory (empty uses temporary directory)")
		outputFlag        = flag.String("output", "", "Output JSON file path")
		smokeFlag         = flag.Bool("smoke", false, "Execute standard preliminary smoke validation suite")
		smokeOutputDir    = flag.String("smoke-dir", "research/experiments/data/smoke", "Directory to store smoke test results")
		pilotFlag         = flag.Bool("pilot", false, "Execute official Phase I5 parameter pilot suite")
		pilotOutputDir    = flag.String("pilot-dir", "research/experiments/data/pilot", "Directory to store pilot test results")
		preflightFlag     = flag.Bool("preflight", false, "Execute mandatory I5.1.4 Workload B2 diagnostic preflight")
		preflightDir      = flag.String("preflight-dir", "research/experiments/data/preflight", "Directory to store preflight test results")
		campaignFlag      = flag.Bool("campaign", false, "Execute Phase I5 campaign trials in bounded batches")
		campaignDir       = flag.String("campaign-dir", "research/experiments/data/campaign", "Base directory for campaign results")
		auditFlag         = flag.Bool("audit", false, "Execute machine-checked campaign inventory auditor")
		platformFlag      = flag.String("platform", "", "Platform identifier (windows_ntfs or wsl2_ext4, empty auto-detects)")
		batchSizeFlag     = flag.Int("batch-size", 0, "Number of trials to execute in this batch (0 = all matching)")
		resumeFlag        = flag.Bool("resume", true, "Skip trials that already completed successfully")
		repFlag           = flag.Int("rep", 0, "Specific repetition to execute (0 = all)")
		gitRevFlag        = flag.String("git-rev", "8900c817b1915e24dbf5087334bd054604926f7b", "Git revision identifier")
	)
	flag.Parse()

	targetPlatform := *platformFlag
	if targetPlatform == "" {
		targetPlatform = detectPlatform()
	}

	if *preflightFlag {
		runPreflightSuite(*preflightDir, targetPlatform, *gitRevFlag)
		return
	}

	if *auditFlag {
		runAuditor(*campaignDir, targetPlatform, *durationFlag, *warmupFlag)
		return
	}

	if *campaignFlag {
		wFilter := ""
		mFilter := ""
		rFilter := 0
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "workload" {
				wFilter = *workloadFlag
			}
			if f.Name == "mode" {
				mFilter = *modeFlag
			}
			if f.Name == "readers" {
				rFilter = *readersFlag
			}
		})
		runCampaignSuite(*campaignDir, targetPlatform, *gitRevFlag, *batchSizeFlag, *resumeFlag, wFilter, mFilter, rFilter, *repFlag, *durationFlag, *warmupFlag)
		return
	}

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
			cfg.MemtableThresholdBytes = 1024 * 1024
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
			cfg.MemtableThresholdBytes = 16 * 1024
			cfg.CompactionTriggerIntervalMs = 1500
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

func runPreflightSuite(baseDir, platform, gitRev string) {
	fmt.Println("================================================================================")
	fmt.Printf("[PREFLIGHT] MANDATORY I5.1.4 WORKLOAD B2 DIAGNOSTIC PREFLIGHT (%s)\n", platform)
	fmt.Println("================================================================================")

	outDir := filepath.Join(baseDir, platform)
	_ = os.MkdirAll(outDir, 0755)

	modes := []string{"ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"}
	allPassed := true

	for _, mode := range modes {
		modeShort := campaign.ShortModeName(mode)
		fmt.Printf("\n--- Executing Workload B2 Diagnostic Preflight (Mode: %s) ---\n", modeShort)

		cfg := harness.DefaultTrialConfig(harness.WorkloadB2_ControlledFlushInterference, mode, 4)
		cfg.MeasurementDurationMs = 10000 // 10s measurement duration
		cfg.WarmupDurationMs = 500
		cfg.InitialSSTableCount = 4
		cfg.InitialKeysPerSSTable = 2500
		cfg.KeyspaceCardinality = 10000
		cfg.MemtableThresholdBytes = 64 * 1024 * 1024
		cfg.FlushTriggerIntervalMs = 1000 // Trigger flush every 1s (target ~8-10 flushes)
		cfg.TargetReadHitRatio = 0.95
		cfg.WalSync = true
		cfg.TrialID = fmt.Sprintf("preflight_B2_%s_%s", platform, modeShort)

		trialDir, err := os.MkdirTemp("", fmt.Sprintf("preflight_b2_%s_*", modeShort))
		if err != nil {
			fmt.Printf("[FAIL] Failed to create temp dir: %v\n", err)
			allPassed = false
			continue
		}
		cfg.DataDir = trialDir

		env := getEnvironment(cfg.DataDir)
		t0 := time.Now()
		res, err := harness.RunTrial(cfg, gitRev, env)
		elapsed := time.Since(t0)
		_ = os.RemoveAll(trialDir)

		if err != nil {
			fmt.Printf("[FAIL] RunTrial returned error: %v\n", err)
			allPassed = false
			continue
		}

		targetFile := filepath.Join(outDir, fmt.Sprintf("preflight_B2_%s.json", modeShort))
		if err := harness.SaveResultJSON(res, targetFile); err != nil {
			fmt.Printf("[WARN] Failed to save JSON: %v\n", err)
		} else {
			fmt.Printf("Result archived at: %s\n", targetFile)
		}

		// Verification checks
		c1WorkerObserved := res.LSMDiagnostics.StartedFlushes > 0 && res.LSMDiagnostics.FlushesCompleted > 0
		c2PhysDerived := res.LSMDiagnostics.PhysicalFlushDurationMs > 0
		c3OverlapRecorded := res.LSMDiagnostics.ReadsDuringFlush > 0
		c4Reconciled := (res.LSMDiagnostics.FlushesCompleted >= uint64(res.LSMDiagnostics.StartedFlushes)) &&
			(res.LSMDiagnostics.StartedFlushes == res.LSMDiagnostics.CompletedFlushes)
		c5ZeroValErrors := res.Operations.ValidationErrorCount == 0
		c6CleanShutdown := res.Status == "SUCCESS"

		passStr := func(p bool) string {
			if p {
				return "PASS"
			}
			return "FAIL"
		}

		fmt.Println("Diagnostic Criteria Checklist:")
		fmt.Printf("  1. Worker start/complete observed:              [%s] (started=%d, completed=%d)\n",
			passStr(c1WorkerObserved), res.LSMDiagnostics.StartedFlushes, res.LSMDiagnostics.CompletedFlushes)
		fmt.Printf("  2. Physical flush duration derived from worker: [%s] (%d ms physical, %d ms queue wait)\n",
			passStr(c2PhysDerived), res.LSMDiagnostics.PhysicalFlushDurationMs, res.LSMDiagnostics.QueueWaitDurationMs)
		fmt.Printf("  3. Reader overlap during active flush recorded: [%s] (%d reads during physical flush, %d in queue wait)\n",
			passStr(c3OverlapRecorded), res.LSMDiagnostics.ReadsDuringFlush, res.LSMDiagnostics.ReadsDuringQueueWait)
		fmt.Printf("  4. Flush completed counts reconciled:           [%s] (flushes_completed=%d == started=%d == completed=%d)\n",
			passStr(c4Reconciled), res.LSMDiagnostics.FlushesCompleted, res.LSMDiagnostics.StartedFlushes, res.LSMDiagnostics.CompletedFlushes)
		fmt.Printf("  5. Validation errors == 0:                      [%s] (%d errors)\n",
			passStr(c5ZeroValErrors), res.Operations.ValidationErrorCount)
		fmt.Printf("  6. Clean shutdown:                              [%s] (status=%s, elapsed=%v)\n",
			passStr(c6CleanShutdown), res.Status, elapsed)

		modePassed := c1WorkerObserved && c2PhysDerived && c3OverlapRecorded && c4Reconciled && c5ZeroValErrors && c6CleanShutdown
		if !modePassed {
			allPassed = false
			fmt.Printf(">>> MODE %s FAILED MANDATORY PREFLIGHT CRITERIA <<<\n", modeShort)
		} else {
			fmt.Printf(">>> MODE %s PASSED ALL MANDATORY PREFLIGHT CRITERIA <<<\n", modeShort)
		}
	}

	fmt.Println("================================================================================")
	if allPassed {
		fmt.Printf("[PREFLIGHT] WORKLOAD B2 PREFLIGHT FULLY PASSED ON %s.\n", platform)
	} else {
		fmt.Printf("[PREFLIGHT] WORKLOAD B2 PREFLIGHT FAILED ON %s.\n", platform)
		os.Exit(1)
	}
	fmt.Println("================================================================================")
}

func runCampaignSuite(baseDir, platform, gitRev string, batchSize int, resume bool, workloadFilter, modeFilter string, readersFilter, repFilter, durationMs, warmupMs int) {
	manifest := campaign.GeneratePlatformManifest(platform, durationMs, warmupMs)
	filter := campaign.ExecutionFilter{
		Platform:          platform,
		WorkloadID:        workloadFilter,
		SyncMode:          modeFilter,
		ReaderConcurrency: readersFilter,
		Repetition:        repFilter,
		BatchSize:         batchSize,
		SkipExisting:      resume,
	}

	env := getEnvironment(filepath.Join(baseDir, platform))
	fmt.Printf("[CAMPAIGN] Starting campaign execution for platform %s (Manifest: %d total trials)\n", platform, len(manifest))
	executed, err := campaign.ExecuteCampaign(manifest, filter, baseDir, gitRev, env, func(msg string) {
		fmt.Println(msg)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Campaign execution error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[CAMPAIGN] Batch complete. Executed %d trials.\n", executed)
}

func runAuditor(baseDir, platform string, durationMs, warmupMs int) {
	var manifest []campaign.PlannedTrial
	if platform != "" && platform != "all" {
		manifest = campaign.GeneratePlatformManifest(platform, durationMs, warmupMs)
	} else {
		manifest = campaign.GeneratePlannedManifest(durationMs, warmupMs)
	}

	report, err := campaign.AuditCampaign(baseDir, manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ERROR] Audit failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(report.String())
	if report.TotalFailed > 0 {
		os.Exit(1)
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
				cfg.MemtableThresholdBytes = 1024 * 1024
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
				cfg.CompactionTriggerIntervalMs = 500
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
				cfg.MemtableThresholdBytes = 16 * 1024
				cfg.CompactionTriggerIntervalMs = 1500
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
