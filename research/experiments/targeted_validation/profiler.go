package targeted_validation

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"owndbms/research/experiments/harness"
)

// RunProfilingTrials executes standalone, dedicated CPU and memory profiling runs
// for both Approach A and Approach B under Workload D (controlled churn, r=4).
func RunProfilingTrials(outDir string, gitRev string, env harness.EnvironmentInfo, logFn func(string)) error {
	if logFn == nil {
		logFn = func(string) {}
	}

	profileDir := filepath.Join(outDir, "profiles")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	modes := []struct {
		name      string
		syncMode  string
		shortName string
	}{
		{"Approach A Baseline", "ApproachA_RWMutexBaseline", "A"},
		{"Approach B Snapshot", "ApproachB_ReaderSnapshot", "B"},
	}

	for _, m := range modes {
		logFn(fmt.Sprintf("[PROFILER] Starting profiling run for %s (Workload D, r=4, 5s)...", m.name))

		trialDir, err := os.MkdirTemp("", fmt.Sprintf("profile_%s_*", m.shortName))
		if err != nil {
			return fmt.Errorf("temp dir for profiling: %w", err)
		}

		cfg := harness.TrialConfig{
			SchemaVersion:               "1.0.0",
			TrialID:                     fmt.Sprintf("profile_D_r04_mode%s", m.shortName),
			TrialIndex:                  999,
			WorkloadID:                  harness.WorkloadD_ReaderChurn,
			SyncMode:                    m.syncMode,
			Seed:                        2026102799,
			ReaderConcurrency:           4,
			WriterConcurrency:           3,
			MeasurementDurationMs:       5000,
			WarmupDurationMs:            1000,
			CooldownDurationMs:          200,
			KeySizeBytes:                19,
			ValSizeBytes:                128,
			KeyspaceCardinality:         10000,
			InitialSSTableCount:         4,
			InitialKeysPerSSTable:       2500,
			ReadDistribution:            "uniform",
			ZipfS:                       0.99,
			TargetReadHitRatio:          0.95,
			MemtableThresholdBytes:      16 * 1024,
			CompactionTriggerIntervalMs: 600,
			DataDir:                     trialDir,
			WalSync:                     true,
		}

		tEnv := env
		tEnv.DataDirPath = trialDir

		cpuPath := filepath.Join(profileDir, fmt.Sprintf("cpu_mode%s.pprof", m.shortName))
		cpuFile, err := os.Create(cpuPath)
		if err != nil {
			_ = os.RemoveAll(trialDir)
			return fmt.Errorf("create cpu profile file: %w", err)
		}

		// Start CPU profiling and mutex contention tracking
		runtime.SetMutexProfileFraction(5)
		if err := pprof.StartCPUProfile(cpuFile); err != nil {
			cpuFile.Close()
			_ = os.RemoveAll(trialDir)
			return fmt.Errorf("start cpu profile: %w", err)
		}

		// Run trial
		t0 := time.Now()
		res, runErr := harness.RunTrial(cfg, gitRev, tEnv)
		pprof.StopCPUProfile()
		cpuFile.Close()

		if runErr != nil {
			_ = os.RemoveAll(trialDir)
			return fmt.Errorf("profiling trial failed: %w", runErr)
		}

		// Mutex contention profile
		mutexPath := filepath.Join(profileDir, fmt.Sprintf("mutex_mode%s.pprof", m.shortName))
		if mf, err := os.Create(mutexPath); err == nil {
			_ = pprof.Lookup("mutex").WriteTo(mf, 0)
			mf.Close()
		}

		// Heap profile
		heapPath := filepath.Join(profileDir, fmt.Sprintf("heap_mode%s.pprof", m.shortName))
		heapFile, err := os.Create(heapPath)
		if err == nil {
			runtime.GC()
			_ = pprof.WriteHeapProfile(heapFile)
			heapFile.Close()
		}

		_ = os.RemoveAll(trialDir)

		logFn(fmt.Sprintf("[PROFILER] Completed %s: elapsed=%v, status=%s, tput=%.1f ops/s, cpu_profile=%s",
			m.name, time.Since(t0), res.Status, res.Throughput.ReadOpsPerSec, cpuPath))
	}

	return nil
}
