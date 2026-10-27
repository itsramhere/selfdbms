package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"owndbms/research/experiments/corrective_validation"
	"owndbms/research/experiments/harness"
	"owndbms/research/experiments/provenance"
)

func getEnvironment(outDir string, prov *provenance.ProvenanceRecord) harness.EnvironmentInfo {
	return harness.EnvironmentInfo{
		OS:             runtime.GOOS,
		Kernel:         runtime.GOOS + "_" + runtime.GOARCH,
		CPUModel:       "AMD Ryzen 7 7445HS w/ Radeon 740M Graphics",
		PhysicalCores:  6,
		LogicalCores:   runtime.NumCPU(),
		TotalRAMBytes:  16 * 1024 * 1024 * 1024,
		FreeRAMBytes:   8 * 1024 * 1024 * 1024,
		GoVersion:      runtime.Version(),
		GOMAXPROCS:     runtime.GOMAXPROCS(0),
		FilesystemType: prov.FilesystemType,
		DataDirPath:    outDir,
	}
}

func main() {
	manifestPath := flag.String("manifest", "research/experiments/corrective_validation/manifest.json", "Path to manifest JSON")
	outDir := flag.String("out-dir", "research/experiments/data/corrective_validation_v1", "Output directory for results")
	preflight := flag.Bool("preflight", false, "Execute bounded preflight validation suite (max 24 trials)")
	profile := flag.Bool("profile", false, "Execute isolated profiling trial")
	profileMode := flag.String("profile-mode", "A", "Profiling mode: A (ApproachA_RWMutexBaseline) or B (ApproachB_ReaderSnapshot)")
	validateOnly := flag.Bool("validate-provenance", false, "Verify provenance and exit")

	flag.Parse()

	if !*preflight && !*profile && !*validateOnly {
		fmt.Println("Usage: runner [-preflight | -profile -profile-mode=[A|B] | -validate-provenance] [options]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// 1. Provenance Verification
	execDir, _ := os.Getwd()
	prov, err := provenance.CaptureProvenance(execDir, "1.1.0", "I6_CORRECTIVE_MECHANISMS_V1", false)
	if err != nil {
		log.Fatalf("FATAL: Provenance validation failed: %v", err)
	}

	if *validateOnly {
		fmt.Printf("=== PROVENANCE VALIDATION PASSED ===\n")
		fmt.Printf("Git Commit:      %s\n", prov.GitCommit)
		fmt.Printf("Git Branch:      %s\n", prov.GitBranch)
		fmt.Printf("Source Clean:    %v\n", prov.SourceClean)
		fmt.Printf("Source Tree SHA: %s\n", prov.SourceTreeHash)
		fmt.Printf("Binary SHA-256:  %s\n", prov.BinarySHA256)
		fmt.Printf("Compiler:        %s %s\n", prov.Compiler, prov.GoVersion)
		fmt.Printf("Platform:        %s/%s\n", prov.Platform, prov.Architecture)
		fmt.Printf("Filesystem:      %s\n", prov.FilesystemType)
		return
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("failed to create output directory %s: %v", *outDir, err)
	}

	// Save provenance manifest alongside results
	provManifestPath := filepath.Join(*outDir, "build_manifest.json")
	if err := prov.WriteManifest(provManifestPath); err != nil {
		log.Printf("Warning: failed to write build manifest: %v", err)
	}

	logFn := func(msg string) {
		log.Println(msg)
	}

	// 2. Profiling Mode (Separate Process per mode)
	if *profile {
		modeStr := strings.ToUpper(*profileMode)
		logFn(fmt.Sprintf("Starting isolated profiling trial for Mode %s...", modeStr))
		summary, err := corrective_validation.RunIsolatedProfileTrial(modeStr, *outDir, prov, logFn)
		if err != nil {
			log.Fatalf("Profiling failed: %v", err)
		}
		logFn(fmt.Sprintf("Profiling complete for Mode %s. Summary: p95=%.2fus, allocs=%d bytes",
			modeStr, summary.P95ReadLatencyUs, summary.AllocatedBytesWindow))
		return
	}

	// 3. Preflight Mode (Strictly Bounded to max 24 trials)
	if *preflight {
		logFn("Starting Bounded Preflight Validation (Authorized limit: <= 24 trials)...")

		manifest, err := corrective_validation.LoadManifest(*manifestPath)
		if err != nil {
			log.Fatalf("Failed to load manifest: %v", err)
		}

		// Select only pair 1 from each distinct condition
		var preflightTrials []corrective_validation.ManifestTrial
		seenCells := make(map[string]bool)

		for _, trial := range manifest.Trials {
			if trial.PairIndex == 1 {
				cellKey := fmt.Sprintf("%s_%s_%s_r%d", trial.Experiment, trial.WorkloadID, trial.Condition, trial.ReaderConcurrency)
				// Include both trials of pair 1
				preflightTrials = append(preflightTrials, trial)
				seenCells[cellKey] = true
			}
		}

		if len(preflightTrials) > 24 {
			log.Fatalf("AUTHORIZATION VIOLATION: Preflight trial count %d exceeds authorized bound of 24 trials!", len(preflightTrials))
		}

		logFn(fmt.Sprintf("Identified %d diagnostic trials across %d experimental cells (Bound <= 24: OK)",
			len(preflightTrials), len(seenCells)))

		env := getEnvironment(*outDir, prov)
		successCount := 0

		for idx, pt := range preflightTrials {
			// Configure short durations for engineering verification
			trialCopy := pt
			trialCopy.MeasurementDurationMs = 1500
			trialCopy.WarmupDurationMs = 300
			trialCopy.CooldownDurationMs = 100

			logFn(fmt.Sprintf("[%d/%d] Running %s...", idx+1, len(preflightTrials), trialCopy.TrialID))
			res, err := corrective_validation.ExecuteCorrectiveTrial(&trialCopy, *outDir, prov, env, logFn)
			if err != nil {
				log.Fatalf("Preflight trial %s failed: %v", trialCopy.TrialID, err)
			}
			if res.Status != "SUCCESS" {
				log.Fatalf("Preflight trial %s did not succeed: %s", trialCopy.TrialID, res.Status)
			}
			successCount++
		}

		logFn(fmt.Sprintf("=== PREFLIGHT VALIDATION PASSED: %d/%d trials completed successfully ===",
			successCount, len(preflightTrials)))
		return
	}
}
