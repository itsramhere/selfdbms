package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"owndbms/research/experiments/harness"
)

func validateResult(res *harness.TrialResult, filePath string) error {
	if res.SchemaVersion != "1.0.0" {
		return fmt.Errorf("%s: invalid or missing schema_version: %q", filePath, res.SchemaVersion)
	}
	if res.TrialID == "" {
		return fmt.Errorf("%s: missing trial_id", filePath)
	}
	if res.WorkloadID == "" {
		return fmt.Errorf("%s: missing workload_id", filePath)
	}
	if res.SyncMode == "" {
		return fmt.Errorf("%s: missing sync_mode", filePath)
	}
	if res.GitRevision == "" {
		return fmt.Errorf("%s: missing git_revision", filePath)
	}
	if res.Environment.OS == "" {
		return fmt.Errorf("%s: missing environment.os", filePath)
	}
	if res.Environment.FilesystemType == "" {
		return fmt.Errorf("%s: missing environment.filesystem_type", filePath)
	}
	if res.Environment.GoVersion == "" {
		return fmt.Errorf("%s: missing environment.go_version", filePath)
	}
	if res.Timestamps.MeasurementDurationMs <= 0 {
		return fmt.Errorf("%s: invalid measurement_duration_ms: %d", filePath, res.Timestamps.MeasurementDurationMs)
	}
	if res.Status == "" {
		return fmt.Errorf("%s: missing status", filePath)
	}
	return nil
}

func main() {
	dirFlag := flag.String("dir", "research/experiments/data/smoke", "Directory containing result JSON files")
	formatFlag := flag.String("format", "markdown", "Output format: markdown or text")
	strictFlag := flag.Bool("strict", true, "Fail immediately on any malformed or invalid JSON file")
	csvFlag := flag.String("csv", "", "Output CSV file path to export all results")
	compareFlag := flag.Bool("compare", false, "Print A/B comparison table across cells")
	recurseFlag := flag.Bool("recurse", false, "Scan directory and subdirectories recursively")
	flag.Parse()

	var results []*harness.TrialResult
	var err error
	if *recurseFlag {
		results, err = LoadAllResults(*dirFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading results from %s: %v\n", *dirFlag, err)
			os.Exit(1)
		}
	} else {
		files, err := os.ReadDir(*dirFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading directory %s: %v\n", *dirFlag, err)
			os.Exit(1)
		}

		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
				path := filepath.Join(*dirFlag, f.Name())
				data, err := os.ReadFile(path)
				if err != nil {
					if *strictFlag {
						fmt.Fprintf(os.Stderr, "Error reading file %s: %v\n", path, err)
						os.Exit(1)
					}
					continue
				}
				var res harness.TrialResult
				if err := json.Unmarshal(data, &res); err != nil {
					if *strictFlag {
						fmt.Fprintf(os.Stderr, "JSON unmarshal error in %s: %v\n", path, err)
						os.Exit(1)
					}
					continue
				}
				if err := validateResult(&res, path); err != nil {
					if *strictFlag {
						fmt.Fprintf(os.Stderr, "Schema validation error: %v\n", err)
						os.Exit(1)
					}
					continue
				}
				results = append(results, &res)
			}
		}
	}

	if len(results) == 0 {
		fmt.Printf("No valid result JSON files found in %s\n", *dirFlag)
		return
	}

	if *csvFlag != "" {
		if err := ExportResultsCSV(results, *csvFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to export CSV to %s: %v\n", *csvFlag, err)
			os.Exit(1)
		}
		fmt.Printf("Exported %d results to %s\n", len(results), *csvFlag)
	}

	if *compareFlag {
		aggs := ComputeCellAggregates(results)
		rows := ComputeComparisons(aggs)
		fmt.Println("### Empirical A/B Synchronization Comparison Matrix")
		fmt.Println(FormatComparisonMarkdown(rows))
		return
	}

	// Sort by WorkloadID, then SyncMode
	sort.Slice(results, func(i, j int) bool {
		if results[i].WorkloadID != results[j].WorkloadID {
			return results[i].WorkloadID < results[j].WorkloadID
		}
		return results[i].SyncMode < results[j].SyncMode
	})

	if *formatFlag == "markdown" {
		printMarkdownTable(results)
	} else {
		printTextTable(results)
	}
}

func printMarkdownTable(results []*harness.TrialResult) {
	fmt.Println("### Preliminary Experiment Smoke Validation Summary")
	fmt.Println("*Note: PRELIMINARY HARNESS VALIDATION — NOT OFFICIAL PERFORMANCE EVIDENCE.*")
	fmt.Println()
	fmt.Println("| Workload | Mode | Readers | Read Ops/s | p50 (µs) | p95 (µs) | p99 (µs) | HitRate% | ValErrs | Meas Flushes | Meas Compact | SST Reads | Mem Reads | CompOverlap (ms) | CompOverlap Rds | FlushOverlap (ms) | FlushOverlap Rds | Status |")
	fmt.Println("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |")
	for _, r := range results {
		p50Us := float64(r.LatencyRead.P50Nanos) / 1000.0
		p95Str := "n/a"
		if r.LatencyRead.P95Nanos != nil {
			p95Str = fmt.Sprintf("%.2f", float64(*r.LatencyRead.P95Nanos)/1000.0)
		}
		p99Str := "n/a"
		if r.LatencyRead.P99Nanos != nil {
			p99Str = fmt.Sprintf("%.2f", float64(*r.LatencyRead.P99Nanos)/1000.0)
		}
		modeShort := "A"
		if strings.Contains(r.SyncMode, "ApproachB") {
			modeShort = "B"
		}
		workloadShort := strings.TrimPrefix(r.WorkloadID, "WORKLOAD_")

		actualHitRate := 0.0
		if r.Operations.ReadOpsCompleted > 0 {
			actualHitRate = float64(r.Operations.ActualHitCount) / float64(r.Operations.ReadOpsCompleted) * 100.0
		}

		statusStr := r.Status
		if r.ExclusionReason != nil && *r.ExclusionReason != "" {
			statusStr = fmt.Sprintf("%s (%s)", r.Status, *r.ExclusionReason)
		}

		fmt.Printf("| %s | %s | %d | %.1f | %.2f | %s | %s | %.1f%% | %d | %d | %d | %d | %d | %d | %d | %d | %d | %s |\n",
			workloadShort, modeShort, r.Config.ReaderConcurrency,
			r.Throughput.ReadOpsPerSec, p50Us, p95Str, p99Str,
			actualHitRate, r.Operations.ValidationErrorCount,
			r.LSMDiagnostics.MeasuredFlushes, r.LSMDiagnostics.MeasuredCompactions,
			r.LSMDiagnostics.MeasuredSSTablePointReads, r.LSMDiagnostics.MeasuredMemtablePointReads,
			r.LSMDiagnostics.CompactionActiveOverlapMs, r.LSMDiagnostics.ReadsDuringCompaction,
			r.LSMDiagnostics.FlushActiveOverlapMs, r.LSMDiagnostics.ReadsDuringFlush,
			statusStr)
	}
	fmt.Println()
}

func printTextTable(results []*harness.TrialResult) {
	fmt.Printf("%-28s %-6s %-7s %-12s %-10s %-10s %-8s %-8s %-10s %-10s %-12s %-12s %-12s %-12s %-10s\n",
		"Workload", "Mode", "Readers", "Read Ops/s", "p50 (µs)", "p99 (µs)", "HitRate%", "ValErrs", "MeasFlush", "MeasCmp", "CompOverMs", "CompOverRds", "FlshOverMs", "FlshOverRds", "Status")
	fmt.Println(strings.Repeat("-", 175))
	for _, r := range results {
		p50Us := float64(r.LatencyRead.P50Nanos) / 1000.0
		p99Str := "n/a"
		if r.LatencyRead.P99Nanos != nil {
			p99Str = fmt.Sprintf("%.2f", float64(*r.LatencyRead.P99Nanos)/1000.0)
		}
		modeShort := "A"
		if strings.Contains(r.SyncMode, "ApproachB") {
			modeShort = "B"
		}
		workloadShort := strings.TrimPrefix(r.WorkloadID, "WORKLOAD_")
		actualHitRate := 0.0
		if r.Operations.ReadOpsCompleted > 0 {
			actualHitRate = float64(r.Operations.ActualHitCount) / float64(r.Operations.ReadOpsCompleted) * 100.0
		}
		fmt.Printf("%-28s %-6s %-7d %-12.1f %-10.2f %-10s %-8.1f%% %-8d %-10d %-10d %-12d %-12d %-12d %-12d %-10s\n",
			workloadShort, modeShort, r.Config.ReaderConcurrency,
			r.Throughput.ReadOpsPerSec, p50Us, p99Str,
			actualHitRate, r.Operations.ValidationErrorCount,
			r.LSMDiagnostics.MeasuredFlushes, r.LSMDiagnostics.MeasuredCompactions,
			r.LSMDiagnostics.CompactionActiveOverlapMs, r.LSMDiagnostics.ReadsDuringCompaction,
			r.LSMDiagnostics.FlushActiveOverlapMs, r.LSMDiagnostics.ReadsDuringFlush,
			r.Status)
	}
	fmt.Println()
}
