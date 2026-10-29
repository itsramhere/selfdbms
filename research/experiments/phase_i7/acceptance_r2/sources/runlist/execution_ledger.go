package runlist

// Execution ledger: the record of every PHYSICAL execution attempt of the
// campaign, regardless of outcome or kind (R2 review directive, blocker 2).
//
// Rule: the strict global ceiling (R1.2e: 400 total runs across all pilot,
// adaptive, and replacement attempts) counts processes, not successes. A run
// that started and crashed, was killed by a timeout, was aborted before
// writing output, or finished with an invalid envelope has physically
// executed and consumes budget exactly like a success. The driver's duty is
// to append a STARTED record before launching each trial process and a
// FINISHED record when it observes the outcome; ValidateExecutionLedger then
// enforces:
//
//	R1 total started (physical executions)      <= GlobalCeiling (400)
//	R2 replacements + retries (Rule S-1 budget) <= ReplacementBudget (16)
//	R3 every FINISHED references an unfinished STARTED of the same trial
//	R4 no trial is finished twice
//	R5 kinds and outcomes are from the closed vocabularies below
//
// Unfinished STARTED records (crash, kill, power loss) are counted: the
// budget was consumed even though no outcome was observed. Pilot attempts
// are the internal-pilot observations retained in the confirmatory sample
// (R1.2e §1.2), so they are part of the planned model, but the ledger still
// records them under kind=pilot so the ceiling report shows every category
// the review directive names: pilot attempts, drift controls, retries,
// replacements, interrupted executions, and aborted attempts.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Execution kinds (closed vocabulary).
const (
	KindConfirmatory = "confirmatory"
	KindDriftAnchor  = "drift_anchor"
	KindPilot        = "pilot"
	KindReplacement  = "replacement"
	KindRetry        = "retry"
)

// Execution outcomes for FINISHED records (closed vocabulary).
const (
	OutcomeSuccess     = "SUCCESS"
	OutcomeFailed      = "FAILED"
	OutcomeTimeout     = "TIMEOUT"
	OutcomeInvalid     = "INVALID"
	OutcomeInterrupted = "INTERRUPTED"
	OutcomeAborted     = "ABORTED"
)

var executionKinds = map[string]bool{
	KindConfirmatory: true, KindDriftAnchor: true, KindPilot: true,
	KindReplacement: true, KindRetry: true,
}

var executionOutcomes = map[string]bool{
	OutcomeSuccess: true, OutcomeFailed: true, OutcomeTimeout: true,
	OutcomeInvalid: true, OutcomeInterrupted: true, OutcomeAborted: true,
}

// ExecutionRecord is one JSONL line of the campaign execution ledger.
type ExecutionRecord struct {
	Event    string `json:"event"` // STARTED | FINISHED
	TrialID  string `json:"trial_id"`
	Kind     string `json:"kind,omitempty"`   // required on STARTED
	Outcome  string `json:"status,omitempty"` // required on FINISHED
	TS       string `json:"ts,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"` // optional diagnostic
	Note     string `json:"note,omitempty"`
}

// ExecutionFindings is the ceiling verification report.
type ExecutionFindings struct {
	OK         bool           `json:"ok"`
	Started    int            `json:"started"`
	Finished   int            `json:"finished"`
	Unfinished int            `json:"unfinished"` // started, never finished: still budget-consuming
	ByKind     map[string]int `json:"by_kind"`
	ByOutcome  map[string]int `json:"by_outcome"`
	Ceiling    int            `json:"ceiling"`
	Remaining  int            `json:"remaining_under_ceiling"`
	ReplRetry  int            `json:"replacement_and_retry_attempts"`
	Errors     []string       `json:"errors,omitempty"`
}

func (f *ExecutionFindings) errf(format string, args ...any) {
	f.Errors = append(f.Errors, fmt.Sprintf(format, args...))
	f.OK = false
}

// ValidateExecutionLedger applies R1–R5.
func ValidateExecutionLedger(records []ExecutionRecord) *ExecutionFindings {
	f := &ExecutionFindings{
		OK:        true,
		ByKind:    map[string]int{},
		ByOutcome: map[string]int{},
		Ceiling:   GlobalCeiling,
	}
	type open struct{ kind string }
	opened := map[string]open{} // trial_id -> open STARTED (at most one at a time)

	for i, rec := range records {
		switch rec.Event {
		case "STARTED":
			if rec.TrialID == "" {
				f.errf("record %d: STARTED without trial_id", i)
				continue
			}
			if !executionKinds[rec.Kind] {
				f.errf("record %d (%s): unknown execution kind %q", i, rec.TrialID, rec.Kind)
			}
			if _, already := opened[rec.TrialID]; already {
				f.errf("record %d (%s): second STARTED while the previous execution is unfinished", i, rec.TrialID)
			}
			opened[rec.TrialID] = open{kind: rec.Kind}
			f.Started++
			f.ByKind[rec.Kind]++
			if rec.Kind == KindReplacement || rec.Kind == KindRetry {
				f.ReplRetry++
			}
		case "FINISHED":
			if rec.TrialID == "" {
				f.errf("record %d: FINISHED without trial_id", i)
				continue
			}
			o, ok := opened[rec.TrialID]
			if !ok {
				f.errf("record %d (%s): FINISHED without a matching open STARTED", i, rec.TrialID)
				continue
			}
			if !executionOutcomes[rec.Outcome] {
				f.errf("record %d (%s): unknown outcome %q", i, rec.TrialID, rec.Outcome)
			}
			delete(opened, rec.TrialID)
			f.Finished++
			f.ByOutcome[rec.Outcome]++
			_ = o
		default:
			f.errf("record %d: unknown event %q (want STARTED|FINISHED)", i, rec.Event)
		}
	}
	f.Unfinished = len(opened)

	// R1: every physical execution — success, failure, timeout, invalid,
	// interrupted, aborted, or unobserved — is a STARTED record.
	if f.Started > GlobalCeiling {
		f.errf("ceiling violated: %d physical executions exceed the strict global ceiling of %d", f.Started, GlobalCeiling)
	}
	// R2: the Rule S-1 budget bounds deliberate re-executions. An interrupted
	// or aborted attempt is re-run as kind=retry; both kinds draw on the same
	// 16-run budget that R1.2e §5 reserves.
	if f.ReplRetry > ReplacementBudget {
		f.errf("Rule S-1 budget violated: %d replacement/retry attempts exceed the reserved budget of %d", f.ReplRetry, ReplacementBudget)
	}
	f.Remaining = GlobalCeiling - f.Started
	return f
}

// ParseExecutionLedgerJSONL reads a driver-style JSONL ledger. Blank lines
// are skipped.
func ParseExecutionLedgerJSONL(r io.Reader) ([]ExecutionRecord, error) {
	var out []ExecutionRecord
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var rec ExecutionRecord
		if err := json.Unmarshal([]byte(text), &rec); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line, err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}
