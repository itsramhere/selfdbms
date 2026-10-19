package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"owndbms/research/experiments/harness"
)

// CellKey groups trials by platform, workload, reader concurrency, and mode.
type CellKey struct {
	Platform          string
	WorkloadID        string
	ReaderConcurrency int
	SyncMode          string
}

// CellAggregate summarizes repetitions of a specific experimental cell.
type CellAggregate struct {
	Key                  CellKey
	RepCount             int
	ThroughputMean       float64
	ThroughputMedian     float64
	ThroughputIQR        float64
	LatencyP50MeanUs     float64
	LatencyP50MedianUs   float64
	LatencyP95MedianUs   float64
	LatencyP99MedianUs   float64
	ValidationErrors     int64
	MeasuredFlushes      float64
	MeasuredCompactions  float64
	CompactionOverlapMs  float64
	CompactionReads      float64
	FlushOverlapMs       float64
	FlushReads           float64
	ReadsDuringPub       float64
	ReadsDuringQueue     float64
}

// ComparisonRow compares Approach A and Approach B for identical platform, workload, and concurrency.
type ComparisonRow struct {
	Platform          string
	WorkloadID        string
	ReaderConcurrency int
	ThroughputA       float64
	ThroughputB       float64
	ThroughputSpeedup float64
	LatencyP50A       float64
	LatencyP50B       float64
	LatencyP50Ratio   float64
	LatencyP99A       float64
	LatencyP99B       float64
	CompOverlapMsA    float64
	CompOverlapMsB    float64
	FlushOverlapMsA   float64
	FlushOverlapMsB   float64
}

// LoadAllResults reads and unmarshals all trial JSON files under baseDataDir.
func LoadAllResults(baseDataDir string) ([]*harness.TrialResult, error) {
	var results []*harness.TrialResult

	err := filepath.Walk(baseDataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".json") && !strings.HasPrefix(info.Name(), "preflight_") {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			var res harness.TrialResult
			if err := json.Unmarshal(data, &res); err == nil && res.Status == "SUCCESS" {
				results = append(results, &res)
			}
		}
		return nil
	})

	return results, err
}

// ComputeCellAggregates groups results by cell key and calculates robust statistics.
func ComputeCellAggregates(results []*harness.TrialResult) map[CellKey]*CellAggregate {
	groups := make(map[CellKey][]*harness.TrialResult)
	for _, r := range results {
		k := CellKey{
			Platform:          r.Environment.OS + "_" + strings.ToLower(r.Environment.FilesystemType[:4]),
			WorkloadID:        r.WorkloadID,
			ReaderConcurrency: r.Config.ReaderConcurrency,
			SyncMode:          r.SyncMode,
		}
		if strings.Contains(r.Environment.FilesystemType, "NTFS") {
			k.Platform = "windows_ntfs"
		} else if strings.Contains(r.Environment.FilesystemType, "ext4") {
			k.Platform = "wsl2_ext4"
		}
		groups[k] = append(groups[k], r)
	}

	aggregates := make(map[CellKey]*CellAggregate)
	for k, list := range groups {
		if len(list) == 0 {
			continue
		}
		n := len(list)
		tputs := make([]float64, n)
		p50s := make([]float64, n)
		p95s := make([]float64, n)
		p99s := make([]float64, n)

		var tputSum, p50Sum, flushesSum, compSum, compOverSum, compRdsSum, flshOverSum, flshRdsSum, pubRdsSum, queueRdsSum float64
		var valErrs int64

		for i, r := range list {
			tput := r.Throughput.ReadOpsPerSec
			tputs[i] = tput
			tputSum += tput

			p50 := float64(r.LatencyRead.P50Nanos) / 1000.0
			p50s[i] = p50
			p50Sum += p50

			if r.LatencyRead.P95Nanos != nil {
				p95s[i] = float64(*r.LatencyRead.P95Nanos) / 1000.0
			}
			if r.LatencyRead.P99Nanos != nil {
				p99s[i] = float64(*r.LatencyRead.P99Nanos) / 1000.0
			}

			valErrs += r.Operations.ValidationErrorCount
			flushesSum += float64(r.LSMDiagnostics.MeasuredFlushes)
			compSum += float64(r.LSMDiagnostics.MeasuredCompactions)
			compOverSum += float64(r.LSMDiagnostics.CompactionActiveOverlapMs)
			compRdsSum += float64(r.LSMDiagnostics.ReadsDuringCompaction)
			flshOverSum += float64(r.LSMDiagnostics.FlushActiveOverlapMs)
			flshRdsSum += float64(r.LSMDiagnostics.ReadsDuringFlush)
			pubRdsSum += float64(r.LSMDiagnostics.ReadsDuringPublication)
			queueRdsSum += float64(r.LSMDiagnostics.ReadsDuringQueueWait)
		}

		sort.Float64s(tputs)
		sort.Float64s(p50s)
		sort.Float64s(p95s)
		sort.Float64s(p99s)

		median := func(s []float64) float64 {
			if len(s) == 0 {
				return 0
			}
			mid := len(s) / 2
			if len(s)%2 == 0 {
				return (s[mid-1] + s[mid]) / 2.0
			}
			return s[mid]
		}

		iqr := func(s []float64) float64 {
			if len(s) < 4 {
				return 0
			}
			q1 := s[len(s)/4]
			q3 := s[len(s)*3/4]
			return q3 - q1
		}

		aggregates[k] = &CellAggregate{
			Key:                 k,
			RepCount:            n,
			ThroughputMean:      tputSum / float64(n),
			ThroughputMedian:    median(tputs),
			ThroughputIQR:       iqr(tputs),
			LatencyP50MeanUs:    p50Sum / float64(n),
			LatencyP50MedianUs:  median(p50s),
			LatencyP95MedianUs:  median(p95s),
			LatencyP99MedianUs:  median(p99s),
			ValidationErrors:    valErrs,
			MeasuredFlushes:     flushesSum / float64(n),
			MeasuredCompactions: compSum / float64(n),
			CompactionOverlapMs: compOverSum / float64(n),
			CompactionReads:     compRdsSum / float64(n),
			FlushOverlapMs:      flshOverSum / float64(n),
			FlushReads:          flshRdsSum / float64(n),
			ReadsDuringPub:      pubRdsSum / float64(n),
			ReadsDuringQueue:    queueRdsSum / float64(n),
		}
	}

	return aggregates
}

// ComputeComparisons aligns Approach A and Approach B for identical conditions.
func ComputeComparisons(aggs map[CellKey]*CellAggregate) []ComparisonRow {
	var rows []ComparisonRow
	matched := make(map[string]bool)

	for kA, aggA := range aggs {
		if !strings.Contains(kA.SyncMode, "ApproachA") {
			continue
		}
		kB := CellKey{
			Platform:          kA.Platform,
			WorkloadID:        kA.WorkloadID,
			ReaderConcurrency: kA.ReaderConcurrency,
			SyncMode:          "ApproachB_ReaderSnapshot",
		}
		aggB, ok := aggs[kB]
		if !ok {
			continue
		}
		rowKey := fmt.Sprintf("%s_%s_%d", kA.Platform, kA.WorkloadID, kA.ReaderConcurrency)
		if matched[rowKey] {
			continue
		}
		matched[rowKey] = true

		speedup := 0.0
		if aggA.ThroughputMedian > 0 {
			speedup = aggB.ThroughputMedian / aggA.ThroughputMedian
		}
		latRatio := 0.0
		if aggA.LatencyP50MedianUs > 0 {
			latRatio = aggB.LatencyP50MedianUs / aggA.LatencyP50MedianUs
		}

		rows = append(rows, ComparisonRow{
			Platform:          kA.Platform,
			WorkloadID:        kA.WorkloadID,
			ReaderConcurrency: kA.ReaderConcurrency,
			ThroughputA:       aggA.ThroughputMedian,
			ThroughputB:       aggB.ThroughputMedian,
			ThroughputSpeedup: speedup,
			LatencyP50A:       aggA.LatencyP50MedianUs,
			LatencyP50B:       aggB.LatencyP50MedianUs,
			LatencyP50Ratio:   latRatio,
			LatencyP99A:       aggA.LatencyP99MedianUs,
			LatencyP99B:       aggB.LatencyP99MedianUs,
			CompOverlapMsA:    aggA.CompactionOverlapMs,
			CompOverlapMsB:    aggB.CompactionOverlapMs,
			FlushOverlapMsA:   aggA.FlushOverlapMs,
			FlushOverlapMsB:   aggB.FlushOverlapMs,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Platform != rows[j].Platform {
			return rows[i].Platform < rows[j].Platform
		}
		if rows[i].WorkloadID != rows[j].WorkloadID {
			return rows[i].WorkloadID < rows[j].WorkloadID
		}
		return rows[i].ReaderConcurrency < rows[j].ReaderConcurrency
	})

	return rows
}

// ExportResultsCSV serializes raw trial records into results.csv.
func ExportResultsCSV(results []*harness.TrialResult, targetFile string) error {
	f, err := os.Create(targetFile)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{
		"trial_id", "platform", "workload_id", "sync_mode", "reader_concurrency", "writer_concurrency",
		"repetition", "measurement_duration_ms", "read_ops_completed", "write_ops_completed",
		"read_ops_per_sec", "write_ops_per_sec", "latency_read_p50_us", "latency_read_p95_us", "latency_read_p99_us",
		"validation_error_count", "measured_flushes", "measured_compactions",
		"compaction_active_overlap_ms", "reads_during_compaction_count",
		"flush_active_overlap_ms", "reads_during_flush_count", "reads_during_publication_count", "reads_during_queue_wait_count",
		"status",
	}
	if err := w.Write(header); err != nil {
		return err
	}

	for _, r := range results {
		plat := "windows_ntfs"
		if strings.Contains(r.Environment.FilesystemType, "ext4") {
			plat = "wsl2_ext4"
		}
		p95Str := ""
		if r.LatencyRead.P95Nanos != nil {
			p95Str = fmt.Sprintf("%.2f", float64(*r.LatencyRead.P95Nanos)/1000.0)
		}
		p99Str := ""
		if r.LatencyRead.P99Nanos != nil {
			p99Str = fmt.Sprintf("%.2f", float64(*r.LatencyRead.P99Nanos)/1000.0)
		}

		row := []string{
			r.TrialID,
			plat,
			r.WorkloadID,
			r.SyncMode,
			strconv.Itoa(r.Config.ReaderConcurrency),
			strconv.Itoa(r.Config.WriterConcurrency),
			strconv.Itoa(r.TrialIndex + 1),
			strconv.Itoa(r.Timestamps.MeasurementDurationMs),
			strconv.FormatInt(r.Operations.ReadOpsCompleted, 10),
			strconv.FormatInt(r.Operations.WriteOpsCompleted, 10),
			fmt.Sprintf("%.2f", r.Throughput.ReadOpsPerSec),
			fmt.Sprintf("%.2f", r.Throughput.WriteOpsPerSec),
			fmt.Sprintf("%.2f", float64(r.LatencyRead.P50Nanos)/1000.0),
			p95Str,
			p99Str,
			strconv.FormatInt(r.Operations.ValidationErrorCount, 10),
			strconv.FormatUint(r.LSMDiagnostics.MeasuredFlushes, 10),
			strconv.FormatUint(r.LSMDiagnostics.MeasuredCompactions, 10),
			strconv.FormatInt(r.LSMDiagnostics.CompactionActiveOverlapMs, 10),
			strconv.FormatInt(r.LSMDiagnostics.ReadsDuringCompaction, 10),
			strconv.FormatInt(r.LSMDiagnostics.FlushActiveOverlapMs, 10),
			strconv.FormatInt(r.LSMDiagnostics.ReadsDuringFlush, 10),
			strconv.FormatInt(r.LSMDiagnostics.ReadsDuringPublication, 10),
			strconv.FormatInt(r.LSMDiagnostics.ReadsDuringQueueWait, 10),
			r.Status,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}

	return nil
}

// FormatComparisonMarkdown generates a structured Markdown table comparing A vs B across cells.
func FormatComparisonMarkdown(rows []ComparisonRow) string {
	var sb strings.Builder
	sb.WriteString("| Platform | Workload | Readers | Throughput A (ops/s) | Throughput B (ops/s) | Speedup (B/A) | Latency p50 A (µs) | Latency p50 B (µs) | Latency p99 A (µs) | Latency p99 B (µs) |\n")
	sb.WriteString("| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |\n")

	for _, r := range rows {
		wShort := strings.TrimPrefix(r.WorkloadID, "WORKLOAD_")
		sb.WriteString(fmt.Sprintf("| %-12s | %-26s | %2d | %10.1f | %10.1f | %6.2fx | %10.1f | %10.1f | %10.1f | %10.1f |\n",
			r.Platform, wShort, r.ReaderConcurrency,
			r.ThroughputA, r.ThroughputB, r.ThroughputSpeedup,
			r.LatencyP50A, r.LatencyP50B, r.LatencyP99A, r.LatencyP99B))
	}

	return sb.String()
}
