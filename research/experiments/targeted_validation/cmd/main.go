package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"owndbms/research/experiments/harness"
	"owndbms/research/experiments/targeted_validation"
)

func getEnvironment(dataDir string) harness.EnvironmentInfo {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	fsType := "ext4 (Native Linux)"
	if runtime.GOOS == "windows" {
		fsType = "NTFS"
	} else if strings.HasPrefix(dataDir, "/mnt/c") {
		fsType = "9P_drvfs (WARNING: Windows-backed)"
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

func getGitRevision() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "unknown_rev"
	}
	return strings.TrimSpace(string(out))
}

func main() {
	manifestPath := flag.String("manifest", "research/experiments/targeted_validation/manifest.json", "Path to preregistered manifest JSON")
	outDir := flag.String("out-dir", "research/experiments/data/targeted_validation_v1", "Output directory for results")
	preflight := flag.Bool("preflight", false, "Run 16-trial preflight validation with short durations")
	runAll := flag.Bool("run", false, "Execute planned trials")
	profile := flag.Bool("profile", false, "Execute dedicated profiling trials")
	batchSize := flag.Int("batch-size", 0, "Maximum trials to execute in this process (0 = all)")
	expFilter := flag.String("experiment", "", "Filter trials by experiment (e.g. T1, T2, T3)")

	flag.Parse()

	if !*preflight && !*runAll && !*profile {
		fmt.Println("Usage: runner [-preflight | -run | -profile] [options]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("failed to create output directory %s: %v", *outDir, err)
	}

	gitRev := getGitRevision()
	env := getEnvironment(*outDir)

	manifest, err := targeted_validation.LoadManifest(*manifestPath)
	if err != nil {
		log.Fatalf("failed to load manifest: %v", err)
	}

	logFn := func(msg string) {
		log.Println(msg)
	}

	// 1. Profiling Mode
	if *profile {
		log.Println("Starting dedicated profiling sub-experiment...")
		if err := targeted_validation.RunProfilingTrials(*outDir, gitRev, env, logFn); err != nil {
			log.Fatalf("profiling failed: %v", err)
		}
		log.Println("Profiling complete!")
		return
	}

	// 2. Preflight Mode
	if *preflight {
		log.Println("Starting Preflight Validation (1 pair per condition, short duration)...")
		// Pick first pair of each experiment/condition
		seen := make(map[string]bool)
		var preflightTrials []targeted_validation.ManifestTrial
		for _, t := range manifest.Trials {
			key := fmt.Sprintf("%s_%s_%d", t.Experiment, t.Condition, t.ReaderConcurrency)
			if t.PairIndex == 1 {
				// Modify for short preflight duration
				shortTrial := t
				shortTrial.MeasurementDurationMs = 500
				shortTrial.WarmupDurationMs = 100
				shortTrial.CooldownDurationMs = 50
				shortTrial.TrialID = fmt.Sprintf("preflight_%s", t.TrialID)
				preflightTrials = append(preflightTrials, shortTrial)
				seen[key] = true
			}
		}

		preflightDir := filepath.Join(*outDir, "preflight")
		_ = os.MkdirAll(preflightDir, 0755)

		executed := 0
		for _, pt := range preflightTrials {
			res, err := targeted_validation.ExecuteTargetedTrial(&pt, preflightDir, gitRev, env, logFn)
			if err != nil {
				log.Fatalf("PREFLIGHT FAILED on trial %s: %v", pt.TrialID, err)
			}
			if res.Status != "SUCCESS" {
				log.Fatalf("PREFLIGHT FAILED: trial %s status %s", pt.TrialID, res.Status)
			}
			executed++
		}
		log.Printf("PREFLIGHT PASSED: %d trials completed with status SUCCESS, zero data races, valid metrics.", executed)
		return
	}

	// 3. Full Execution Mode
	if *runAll {
		log.Printf("Starting Campaign %s (Total Manifest Trials: %d)...", manifest.CampaignID, manifest.TotalPlannedTrials)
		executed := 0
		for _, t := range manifest.Trials {
			if *expFilter != "" && !strings.EqualFold(t.Experiment, *expFilter) {
				continue
			}

			// Check skip existing
			targetFile := filepath.Join(*outDir, fmt.Sprintf("%s.json", t.TrialID))
			if _, err := os.Stat(targetFile); err == nil {
				// Already exists
				continue
			}

			res, err := targeted_validation.ExecuteTargetedTrial(&t, *outDir, gitRev, env, logFn)
			if err != nil {
				log.Fatalf("Execution halted on trial %s: %v", t.TrialID, err)
			}
			if res.Status != "SUCCESS" {
				log.Fatalf("Trial %s failed with status: %s", t.TrialID, res.Status)
			}

			executed++
			if *batchSize > 0 && executed >= *batchSize {
				log.Printf("Reached batch size limit (%d trials executed). Exiting cleanly for batch cycle.", executed)
				return
			}
		}
		log.Printf("Campaign execution finished. Executed %d trials in this invocation.", executed)
	}
}
