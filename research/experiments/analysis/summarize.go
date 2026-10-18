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

func main() {
	dirFlag := flag.String("dir", "research/experiments/data/smoke", "Directory containing result JSON files")
	formatFlag := flag.String("format", "markdown", "Output format: markdown or text")
	flag.Parse()

	files, err := os.ReadDir(*dirFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading directory %s: %v\n", *dirFlag, err)
		os.Exit(1)
	}

	var results []*harness.TrialResult
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
			path := filepath.Join(*dirFlag, f.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var res harness.TrialResult
			if err := json.Unmarshal(data, &res); err != nil {
				continue
			}
			results = append(results, &res)
		}
	}

	if len(results) == 0 {
		fmt.Printf("No valid result JSON files found in %s\n", *dirFlag)
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
	fmt.Println("| Workload | Mode | Readers | Read Ops/s | p50 (µs) | p95 (µs) | p99 (µs) | Flushes | Compactions | SSTable Reads | Memtable Reads | Status |")
	fmt.Println("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |")
	for _, r := range results {
		p50Us := float64(r.LatencyRead.P50Nanos) / 1000.0
		p95Us := float64(r.LatencyRead.P95Nanos) / 1000.0
		p99Us := float64(r.LatencyRead.P99Nanos) / 1000.0
		modeShort := "A"
		if strings.Contains(r.SyncMode, "ApproachB") {
			modeShort = "B"
		}
		workloadShort := strings.TrimPrefix(r.WorkloadID, "WORKLOAD_")
		fmt.Printf("| %s | %s | %d | %.1f | %.2f | %.2f | %.2f | %d | %d | %d | %d | %s |\n",
			workloadShort, modeShort, r.Config.ReaderConcurrency,
			r.Throughput.ReadOpsPerSec, p50Us, p95Us, p99Us,
			r.LSMDiagnostics.FlushesCompleted, r.LSMDiagnostics.CompactionsCompleted,
			r.LSMDiagnostics.SSTablePointReads, r.LSMDiagnostics.MemtablePointReads,
			r.Status)
	}
	fmt.Println()
}

func printTextTable(results []*harness.TrialResult) {
	fmt.Printf("%-24s %-6s %-7s %-12s %-10s %-10s %-10s %-8s %-8s %-10s\n",
		"Workload", "Mode", "Readers", "Read Ops/s", "p50 (µs)", "p95 (µs)", "p99 (µs)", "Flush", "Compact", "Status")
	fmt.Println(strings.Repeat("-", 100))
	for _, r := range results {
		p50Us := float64(r.LatencyRead.P50Nanos) / 1000.0
		p95Us := float64(r.LatencyRead.P95Nanos) / 1000.0
		p99Us := float64(r.LatencyRead.P99Nanos) / 1000.0
		modeShort := "A"
		if strings.Contains(r.SyncMode, "ApproachB") {
			modeShort = "B"
		}
		workloadShort := strings.TrimPrefix(r.WorkloadID, "WORKLOAD_")
		fmt.Printf("%-24s %-6s %-7d %-12.1f %-10.2f %-10.2f %-10.2f %-8d %-8d %-10s\n",
			workloadShort, modeShort, r.Config.ReaderConcurrency,
			r.Throughput.ReadOpsPerSec, p50Us, p95Us, p99Us,
			r.LSMDiagnostics.FlushesCompleted, r.LSMDiagnostics.CompactionsCompleted,
			r.Status)
	}
}
