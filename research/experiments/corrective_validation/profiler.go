package corrective_validation

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"owndbms/research/experiments/harness"
	"owndbms/research/experiments/provenance"
)

// ProfileSummary captures resource accounting strictly scoped to the measurement window.
type ProfileSummary struct {
	Mode                   string    `json:"mode"`
	GitRevision            string    `json:"git_revision"`
	WorkloadID             string    `json:"workload_id"`
	ReaderConcurrency      int       `json:"reader_concurrency"`
	WriterConcurrency      int       `json:"writer_concurrency"`
	MeasurementDurationMs  int       `json:"measurement_duration_ms"`
	WindowStartISO         string    `json:"window_start_iso"`
	WindowEndISO           string    `json:"window_end_iso"`
	AllocatedBytesWindow   uint64    `json:"allocated_bytes_window"`
	MallocsCountWindow     uint64    `json:"mallocs_count_window"`
	FreesCountWindow       uint64    `json:"frees_count_window"`
	RetainedHeapBytes      uint64    `json:"retained_heap_bytes"`
	GCCyclesWindow         uint32    `json:"gc_cycles_window"`
	CPUProfilePath         string    `json:"cpu_profile_path"`
	MutexProfilePath       string    `json:"mutex_profile_path"`
	BlockProfilePath       string    `json:"block_profile_path"`
	AllocsProfilePath      string    `json:"allocs_profile_path"`
	HeapProfilePath        string    `json:"heap_profile_path"`
	CompletedReadOps       int64     `json:"completed_read_ops"`
	CompletedWriteOps      int64     `json:"completed_write_ops"`
	ReadThroughputOpsSec   float64   `json:"read_throughput_ops_sec"`
	P95ReadLatencyUs       float64   `json:"p95_read_latency_us"`
	CapturedAt             time.Time `json:"captured_at"`
}

// RunIsolatedProfileTrial runs an isolated, measurement-window-bounded profiling trial.
// To guarantee zero cross-contamination of pprof/mutex state, Approach A and Approach B
// MUST run in separate OS process invocations.
func RunIsolatedProfileTrial(mode string, outDir string, prov *provenance.ProvenanceRecord, logFn func(string)) (*ProfileSummary, error) {
	if logFn == nil {
		logFn = func(string) {}
	}

	profileDir := filepath.Join(outDir, "profiles")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return nil, fmt.Errorf("create profile directory: %w", err)
	}

	shortName := "A"
	syncMode := "ApproachA_RWMutexBaseline"
	if mode == "B" || mode == "ApproachB_ReaderSnapshot" || mode == "ApproachB" {
		shortName = "B"
		syncMode = "ApproachB_ReaderSnapshot"
	}

	trialDir, err := os.MkdirTemp("", fmt.Sprintf("profile_%s_*", shortName))
	if err != nil {
		return nil, fmt.Errorf("create temp profile data dir: %w", err)
	}
	defer os.RemoveAll(trialDir)

	cpuPath := filepath.Join(profileDir, fmt.Sprintf("cpu_mode%s.pprof", shortName))
	mutexPath := filepath.Join(profileDir, fmt.Sprintf("mutex_mode%s.pprof", shortName))
	blockPath := filepath.Join(profileDir, fmt.Sprintf("block_mode%s.pprof", shortName))
	allocsPath := filepath.Join(profileDir, fmt.Sprintf("allocs_mode%s.pprof", shortName))
	heapPath := filepath.Join(profileDir, fmt.Sprintf("heap_mode%s.pprof", shortName))

	cfg := harness.TrialConfig{
		SchemaVersion:               "1.0.0",
		TrialID:                     fmt.Sprintf("profile_corrective_mode%s", shortName),
		TrialIndex:                  999,
		WorkloadID:                  harness.WorkloadD_ReaderChurn,
		SyncMode:                    syncMode,
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

	var (
		cpuFile        *os.File
		memStatsBefore runtime.MemStats
		memStatsAfter  runtime.MemStats
		windowStartISO string
		windowEndISO   string
	)

	// Configure measurement-window hooks
	cfg.Hooks = &harness.TrialHooks{
		OnMeasurementStart: func() {
			logFn(fmt.Sprintf("[PROFILER-%s] Measurement window STARTED. Activating CPU, Mutex, and Block profiling...", shortName))
			windowStartISO = time.Now().UTC().Format(time.RFC3339Nano)

			// 1. Snapshot MemStats right at window start
			runtime.ReadMemStats(&memStatsBefore)

			// 2. Enable contention profilers
			runtime.SetMutexProfileFraction(1)
			runtime.SetBlockProfileRate(1)

			// 3. Start CPU profiling
			var err error
			cpuFile, err = os.Create(cpuPath)
			if err != nil {
				log.Printf("[PROFILER-%s] ERROR: failed to create cpu profile file: %v", shortName, err)
				return
			}
			if err := pprof.StartCPUProfile(cpuFile); err != nil {
				log.Printf("[PROFILER-%s] ERROR: failed to start CPU profile: %v", shortName, err)
			}
		},
		OnMeasurementEnd: func() {
			// 1. Stop CPU profiling promptly
			pprof.StopCPUProfile()
			if cpuFile != nil {
				cpuFile.Close()
			}

			// 2. Snapshot MemStats right at window close
			runtime.ReadMemStats(&memStatsAfter)
			windowEndISO = time.Now().UTC().Format(time.RFC3339Nano)
			logFn(fmt.Sprintf("[PROFILER-%s] Measurement window ENDED. Writing Mutex, Block, Allocs, and Heap profiles...", shortName))

			// 3. Write Mutex profile
			if mf, err := os.Create(mutexPath); err == nil {
				if mp := pprof.Lookup("mutex"); mp != nil {
					_ = mp.WriteTo(mf, 0)
				}
				mf.Close()
			}
			runtime.SetMutexProfileFraction(0) // disable

			// 4. Write Block profile
			if bf, err := os.Create(blockPath); err == nil {
				if bp := pprof.Lookup("block"); bp != nil {
					_ = bp.WriteTo(bf, 0)
				}
				bf.Close()
			}
			runtime.SetBlockProfileRate(0) // disable

			// 5. Write Allocs profile (total allocated objects and space since start)
			if af, err := os.Create(allocsPath); err == nil {
				if ap := pprof.Lookup("allocs"); ap != nil {
					_ = ap.WriteTo(af, 0)
				}
				af.Close()
			}

			// 6. Write Heap profile WITHOUT forced GC (preserves live memory snapshot at end of window)
			if hf, err := os.Create(heapPath); err == nil {
				_ = pprof.WriteHeapProfile(hf)
				hf.Close()
			}
		},
	}

	env := harness.EnvironmentInfo{
		OS:             prov.Platform,
		Kernel:         prov.Platform + "_" + prov.Architecture,
		CPUModel:       "AMD Ryzen 7 7445HS w/ Radeon 740M Graphics",
		PhysicalCores:  6,
		LogicalCores:   runtime.NumCPU(),
		TotalRAMBytes:  16 * 1024 * 1024 * 1024,
		FreeRAMBytes:   8 * 1024 * 1024 * 1024,
		GoVersion:      prov.GoVersion,
		GOMAXPROCS:     runtime.GOMAXPROCS(0),
		FilesystemType: prov.FilesystemType,
		DataDirPath:    trialDir,
	}

	res, err := harness.RunTrial(cfg, prov.GitCommit, env)
	if err != nil {
		return nil, fmt.Errorf("run profiling trial failed: %w", err)
	}

	p95Us := 0.0
	if res.LatencyRead.P95Nanos != nil {
		p95Us = float64(*res.LatencyRead.P95Nanos) / 1000.0
	}

	allocBytes := uint64(0)
	if memStatsAfter.TotalAlloc >= memStatsBefore.TotalAlloc {
		allocBytes = memStatsAfter.TotalAlloc - memStatsBefore.TotalAlloc
	}
	mallocs := uint64(0)
	if memStatsAfter.Mallocs >= memStatsBefore.Mallocs {
		mallocs = memStatsAfter.Mallocs - memStatsBefore.Mallocs
	}
	frees := uint64(0)
	if memStatsAfter.Frees >= memStatsBefore.Frees {
		frees = memStatsAfter.Frees - memStatsBefore.Frees
	}
	gcCycles := uint32(0)
	if memStatsAfter.NumGC >= memStatsBefore.NumGC {
		gcCycles = memStatsAfter.NumGC - memStatsBefore.NumGC
	}

	summary := &ProfileSummary{
		Mode:                  syncMode,
		GitRevision:           prov.GitCommit,
		WorkloadID:            string(cfg.WorkloadID),
		ReaderConcurrency:     cfg.ReaderConcurrency,
		WriterConcurrency:     cfg.WriterConcurrency,
		MeasurementDurationMs: cfg.MeasurementDurationMs,
		WindowStartISO:        windowStartISO,
		WindowEndISO:          windowEndISO,
		AllocatedBytesWindow:  allocBytes,
		MallocsCountWindow:    mallocs,
		FreesCountWindow:      frees,
		RetainedHeapBytes:     memStatsAfter.HeapAlloc,
		GCCyclesWindow:        gcCycles,
		CPUProfilePath:        cpuPath,
		MutexProfilePath:      mutexPath,
		BlockProfilePath:      blockPath,
		AllocsProfilePath:     allocsPath,
		HeapProfilePath:       heapPath,
		CompletedReadOps:      res.Operations.ReadOpsCompleted,
		CompletedWriteOps:     res.Operations.WriteOpsCompleted,
		ReadThroughputOpsSec:  res.Throughput.ReadOpsPerSec,
		P95ReadLatencyUs:      p95Us,
		CapturedAt:            time.Now().UTC(),
	}

	// Write summary JSON
	summaryPath := filepath.Join(profileDir, fmt.Sprintf("summary_mode%s.json", shortName))
	data, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(summaryPath, data, 0644)

	logFn(fmt.Sprintf("[PROFILER-%s] Summary saved to %s", shortName, summaryPath))
	return summary, nil
}
