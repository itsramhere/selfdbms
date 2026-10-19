package campaign

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"owndbms/research/experiments/harness"
)

// PlatformAudit summarizes campaign status for a single platform.
type PlatformAudit struct {
	Platform  string `json:"platform"`
	Planned   int    `json:"planned"`
	Executed  int    `json:"executed"`
	Validated int    `json:"validated"`
	Failed    int    `json:"failed"`
	Missing   int    `json:"missing"`
}

// WorkloadAudit summarizes campaign status for a specific workload.
type WorkloadAudit struct {
	WorkloadID string `json:"workload_id"`
	Planned    int    `json:"planned"`
	Executed   int    `json:"executed"`
	Validated  int    `json:"validated"`
	Failed     int    `json:"failed"`
	Missing    int    `json:"missing"`
}

// FailedTrialSummary details an unvalidated or failed trial execution.
type FailedTrialSummary struct {
	RunID         string `json:"run_id"`
	CellID        string `json:"cell_id"`
	FilePath      string `json:"file_path"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
}

// AuditReport provides complete campaign completeness and validation statistics.
type AuditReport struct {
	TotalPlanned   int                            `json:"total_planned"`
	TotalExecuted  int                            `json:"total_executed"`
	TotalValidated int                            `json:"total_validated"`
	TotalFailed    int                            `json:"total_failed"`
	TotalMissing   int                            `json:"total_missing"`
	PerPlatform    map[string]*PlatformAudit      `json:"per_platform"`
	PerWorkload    map[string]*WorkloadAudit      `json:"per_workload"`
	FailedTrials   []FailedTrialSummary           `json:"failed_trials,omitempty"`
}

// AuditCampaign scans data directories against the planned manifest.
func AuditCampaign(baseDataDir string, manifest []PlannedTrial) (*AuditReport, error) {
	report := &AuditReport{
		TotalPlanned: len(manifest),
		PerPlatform:  make(map[string]*PlatformAudit),
		PerWorkload:  make(map[string]*WorkloadAudit),
		FailedTrials: make([]FailedTrialSummary, 0),
	}

	for _, pt := range manifest {
		// Initialize platform entry
		if _, ok := report.PerPlatform[pt.Platform]; !ok {
			report.PerPlatform[pt.Platform] = &PlatformAudit{Platform: pt.Platform}
		}
		pAudit := report.PerPlatform[pt.Platform]
		pAudit.Planned++

		// Initialize workload entry
		wKey := string(pt.WorkloadID)
		if _, ok := report.PerWorkload[wKey]; !ok {
			report.PerWorkload[wKey] = &WorkloadAudit{WorkloadID: wKey}
		}
		wAudit := report.PerWorkload[wKey]
		wAudit.Planned++

		// Find result file
		targetName := fmt.Sprintf("%s.json", pt.RunID)
		targetPath := filepath.Join(baseDataDir, pt.Platform, targetName)
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			// Check direct path
			altPath := filepath.Join(baseDataDir, targetName)
			if _, err2 := os.Stat(altPath); err2 == nil {
				targetPath = altPath
			}
		}

		rawBytes, err := os.ReadFile(targetPath)
		if err != nil {
			report.TotalMissing++
			pAudit.Missing++
			wAudit.Missing++
			continue
		}

		report.TotalExecuted++
		pAudit.Executed++
		wAudit.Executed++

		var res harness.TrialResult
		if err := json.Unmarshal(rawBytes, &res); err != nil {
			report.TotalFailed++
			pAudit.Failed++
			wAudit.Failed++
			report.FailedTrials = append(report.FailedTrials, FailedTrialSummary{
				RunID:         pt.RunID,
				CellID:        pt.CellID,
				FilePath:      targetPath,
				Status:        "JSON_CORRUPT",
				FailureReason: fmt.Sprintf("failed to parse JSON: %v", err),
			})
			continue
		}

		// Check status
		if res.Status != "SUCCESS" {
			report.TotalFailed++
			pAudit.Failed++
			wAudit.Failed++
			reason := "unspecified failure"
			if res.FailureReason != nil && *res.FailureReason != "" {
				reason = *res.FailureReason
			} else if res.ExclusionReason != nil && *res.ExclusionReason != "" {
				reason = *res.ExclusionReason
			}
			report.FailedTrials = append(report.FailedTrials, FailedTrialSummary{
				RunID:         pt.RunID,
				CellID:        pt.CellID,
				FilePath:      targetPath,
				Status:        res.Status,
				FailureReason: reason,
			})
			continue
		}

		// Validate operational integrity
		if res.Operations.ValidationErrorCount > 0 {
			report.TotalFailed++
			pAudit.Failed++
			wAudit.Failed++
			report.FailedTrials = append(report.FailedTrials, FailedTrialSummary{
				RunID:         pt.RunID,
				CellID:        pt.CellID,
				FilePath:      targetPath,
				Status:        "VALIDATION_ERROR",
				FailureReason: fmt.Sprintf("observed %d validation errors during trial", res.Operations.ValidationErrorCount),
			})
			continue
		}

		// Reconcile flush counts for flush-active workloads
		if pt.WorkloadID == harness.WorkloadB2_ControlledFlushInterference {
			flushesComp := res.LSMDiagnostics.FlushesCompleted
			startFlushes := uint64(res.LSMDiagnostics.StartedFlushes)
			if flushesComp < startFlushes {
				report.TotalFailed++
				pAudit.Failed++
				wAudit.Failed++
				report.FailedTrials = append(report.FailedTrials, FailedTrialSummary{
					RunID:         pt.RunID,
					CellID:        pt.CellID,
					FilePath:      targetPath,
					Status:        "FLUSH_DISCREPANCY",
					FailureReason: fmt.Sprintf("completed flushes (%d) < started flushes (%d)", flushesComp, startFlushes),
				})
				continue
			}
		}

		report.TotalValidated++
		pAudit.Validated++
		wAudit.Validated++
	}

	return report, nil
}

// String renders a readable text and Markdown summary table of the audit report.
func (ar *AuditReport) String() string {
	var sb strings.Builder
	sb.WriteString("================================================================================\n")
	sb.WriteString("CAMPAIGN COMPLETENESS & INTEGRITY AUDIT REPORT\n")
	sb.WriteString("================================================================================\n")
	sb.WriteString(fmt.Sprintf("Total Planned Trials:   %d\n", ar.TotalPlanned))
	sb.WriteString(fmt.Sprintf("Total Executed Trials:  %d\n", ar.TotalExecuted))
	sb.WriteString(fmt.Sprintf("Total Validated Trials: %d\n", ar.TotalValidated))
	sb.WriteString(fmt.Sprintf("Total Failed Trials:    %d\n", ar.TotalFailed))
	sb.WriteString(fmt.Sprintf("Total Missing Trials:   %d\n", ar.TotalMissing))
	sb.WriteString("--------------------------------------------------------------------------------\n")
	sb.WriteString("PLATFORM BREAKDOWN:\n")
	sb.WriteString("| Platform | Planned | Executed | Validated | Failed | Missing | Completion |\n")
	sb.WriteString("| :--- | :---: | :---: | :---: | :---: | :---: | :---: |\n")

	pKeys := make([]string, 0, len(ar.PerPlatform))
	for k := range ar.PerPlatform {
		pKeys = append(pKeys, k)
	}
	sort.Strings(pKeys)
	for _, k := range pKeys {
		p := ar.PerPlatform[k]
		pct := 0.0
		if p.Planned > 0 {
			pct = float64(p.Validated) / float64(p.Planned) * 100.0
		}
		sb.WriteString(fmt.Sprintf("| %-12s | %7d | %8d | %9d | %6d | %7d | %9.1f%% |\n",
			p.Platform, p.Planned, p.Executed, p.Validated, p.Failed, p.Missing, pct))
	}

	sb.WriteString("--------------------------------------------------------------------------------\n")
	sb.WriteString("WORKLOAD BREAKDOWN:\n")
	sb.WriteString("| Workload ID | Planned | Executed | Validated | Failed | Missing | Completion |\n")
	sb.WriteString("| :--- | :---: | :---: | :---: | :---: | :---: | :---: |\n")

	wKeys := make([]string, 0, len(ar.PerWorkload))
	for k := range ar.PerWorkload {
		wKeys = append(wKeys, k)
	}
	sort.Strings(wKeys)
	for _, k := range wKeys {
		w := ar.PerWorkload[k]
		pct := 0.0
		if w.Planned > 0 {
			pct = float64(w.Validated) / float64(w.Planned) * 100.0
		}
		sb.WriteString(fmt.Sprintf("| %-35s | %5d | %6d | %7d | %5d | %6d | %9.1f%% |\n",
			w.WorkloadID, w.Planned, w.Executed, w.Validated, w.Failed, w.Missing, pct))
	}

	if len(ar.FailedTrials) > 0 {
		sb.WriteString("--------------------------------------------------------------------------------\n")
		sb.WriteString(fmt.Sprintf("FAILED TRIALS DETAILS (%d failed):\n", len(ar.FailedTrials)))
		for i, ft := range ar.FailedTrials {
			if i >= 10 {
				sb.WriteString(fmt.Sprintf("... and %d more failed trials.\n", len(ar.FailedTrials)-10))
				break
			}
			sb.WriteString(fmt.Sprintf("  - [%s] %s: %s (path: %s)\n", ft.Status, ft.RunID, ft.FailureReason, ft.FilePath))
		}
	}
	sb.WriteString("================================================================================\n")
	return sb.String()
}
