package runlist

// Tests for the execution ledger: every physical execution — pilot, drift
// anchor, retry, replacement, interrupted, aborted — counts against the
// strict global ceiling. All records are synthetic.

import (
	"strings"
	"testing"
)

func rec(event, trialID, kind, outcome string) ExecutionRecord {
	return ExecutionRecord{Event: event, TrialID: trialID, Kind: kind, Outcome: outcome}
}

// TestExecutionLedgerCountsEveryPhysicalRun walks the directive's full
// category list and asserts each contributes to the ceiling count.
func TestExecutionLedgerCountsEveryPhysicalRun(t *testing.T) {
	records := []ExecutionRecord{
		rec("STARTED", "p1", KindPilot, ""),
		rec("FINISHED", "p1", "", OutcomeSuccess),
		rec("STARTED", "a1", KindDriftAnchor, ""),
		rec("FINISHED", "a1", "", OutcomeSuccess),
		rec("STARTED", "c1", KindConfirmatory, ""),
		rec("FINISHED", "c1", "", OutcomeFailed),
		// interrupted: started, the process was killed, driver recorded it
		rec("STARTED", "i1", KindConfirmatory, ""),
		rec("FINISHED", "i1", "", OutcomeInterrupted),
		// aborted: started and aborted before any output
		rec("STARTED", "b1", KindConfirmatory, ""),
		rec("FINISHED", "b1", "", OutcomeAborted),
		// retry: re-execution of the interrupted trial
		rec("STARTED", "i1", KindRetry, ""),
		rec("FINISHED", "i1", "", OutcomeSuccess),
		// replacement: substitution for the failed trial
		rec("STARTED", "r1", KindReplacement, ""),
		rec("FINISHED", "r1", "", OutcomeSuccess),
		// timeout and invalid envelopes are outcomes of physical runs
		rec("STARTED", "t1", KindConfirmatory, ""),
		rec("FINISHED", "t1", "", OutcomeTimeout),
		rec("STARTED", "v1", KindConfirmatory, ""),
		rec("FINISHED", "v1", "", OutcomeInvalid),
		// unfinished: the driver process itself died (no FINISHED will ever appear)
		rec("STARTED", "u1", KindConfirmatory, ""),
	}
	rep := ValidateExecutionLedger(records)
	if !rep.OK {
		t.Fatalf("well-formed ledger rejected: %v", rep.Errors)
	}
	if rep.Started != 10 {
		t.Fatalf("started = %d, want 10 (every STARTED is a physical execution)", rep.Started)
	}
	if rep.Unfinished != 1 {
		t.Fatalf("unfinished = %d, want 1", rep.Unfinished)
	}
	for kind, want := range map[string]int{
		KindPilot: 1, KindDriftAnchor: 1, KindConfirmatory: 6,
		KindRetry: 1, KindReplacement: 1,
	} {
		if rep.ByKind[kind] != want {
			t.Fatalf("by_kind[%s] = %d, want %d", kind, rep.ByKind[kind], want)
		}
	}
	for outcome, want := range map[string]int{
		OutcomeSuccess: 4, OutcomeFailed: 1, OutcomeInterrupted: 1,
		OutcomeAborted: 1, OutcomeTimeout: 1, OutcomeInvalid: 1,
	} {
		if rep.ByOutcome[outcome] != want {
			t.Fatalf("by_outcome[%s] = %d, want %d", outcome, rep.ByOutcome[outcome], want)
		}
	}
	if rep.ReplRetry != 2 {
		t.Fatalf("repl+retry = %d, want 2", rep.ReplRetry)
	}
	if rep.Remaining != GlobalCeiling-10 {
		t.Fatalf("remaining = %d, want %d", rep.Remaining, GlobalCeiling-10)
	}
}

// TestExecutionLedgerCeilingBreach: 401 physical executions violate the
// strict global ceiling.
func TestExecutionLedgerCeilingBreach(t *testing.T) {
	var records []ExecutionRecord
	for i := 0; i < GlobalCeiling+1; i++ {
		tid := "t" + string(rune('A'+i%26)) + string(rune('a'+i/26%26)) + string(rune('0'+i/676%26))
		records = append(records, rec("STARTED", tid, KindConfirmatory, ""))
		records = append(records, rec("FINISHED", tid, "", OutcomeSuccess))
	}
	rep := ValidateExecutionLedger(records)
	if rep.OK {
		t.Fatal("ceiling breach accepted")
	}
	found := false
	for _, e := range rep.Errors {
		if strings.Contains(e, "ceiling violated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("ceiling error not reported: %v", rep.Errors)
	}
}

// TestExecutionLedgerRuleS1Budget: more than 16 replacement/retry attempts
// violate the pre-registered budget even when under the absolute ceiling.
func TestExecutionLedgerRuleS1Budget(t *testing.T) {
	var records []ExecutionRecord
	for i := 0; i < ReplacementBudget+1; i++ {
		tid := "r" + string(rune('A'+i%26)) + string(rune('a'+i/26%26))
		records = append(records, rec("STARTED", tid, KindRetry, ""))
		records = append(records, rec("FINISHED", tid, "", OutcomeSuccess))
	}
	rep := ValidateExecutionLedger(records)
	if rep.OK {
		t.Fatal("Rule S-1 budget breach accepted")
	}
	found := false
	for _, e := range rep.Errors {
		if strings.Contains(e, "Rule S-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("budget error not reported: %v", rep.Errors)
	}
}

// TestExecutionLedgerIntegrity: finished-without-started, double finish,
// unknown kinds/outcomes/events, and a second concurrent STARTED are all
// rejected.
func TestExecutionLedgerIntegrity(t *testing.T) {
	cases := map[string][]ExecutionRecord{
		"finished without started": {rec("FINISHED", "x", "", OutcomeSuccess)},
		"double finish": {
			rec("STARTED", "x", KindConfirmatory, ""),
			rec("FINISHED", "x", "", OutcomeSuccess),
			rec("FINISHED", "x", "", OutcomeSuccess),
		},
		"unknown kind":     {rec("STARTED", "x", "mystery", "")},
		"unknown outcome":  {rec("STARTED", "x", KindConfirmatory, ""), rec("FINISHED", "x", "", "SUCCEEDED?")},
		"unknown event":    {rec("PAUSED", "x", "", "")},
		"missing trial id": {rec("STARTED", "", KindPilot, "")},
		"double started": {
			rec("STARTED", "x", KindConfirmatory, ""),
			rec("STARTED", "x", KindConfirmatory, ""),
		},
	}
	for name, records := range cases {
		if rep := ValidateExecutionLedger(records); rep.OK {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// TestExecutionLedgerWorstCaseComposition ties the run-list worst case to the
// ledger: 372 planned (confirmatory + anchors) + 16 replacements = 388 ≤ 400.
func TestExecutionLedgerWorstCaseComposition(t *testing.T) {
	var records []ExecutionRecord
	n := 0
	add := func(kind string, count int) {
		for i := 0; i < count; i++ {
			tid := kind + "_" + string(rune('A'+n%26)) + string(rune('a'+n/26%26)) + string(rune('0'+n/676%26))
			records = append(records, rec("STARTED", tid, kind, ""))
			records = append(records, rec("FINISHED", tid, "", OutcomeSuccess))
			n++
		}
	}
	add(KindConfirmatory, 360)
	add(KindDriftAnchor, 12)
	add(KindReplacement, 16)
	rep := ValidateExecutionLedger(records)
	if !rep.OK {
		t.Fatalf("worst-case composition rejected: %v", rep.Errors)
	}
	if rep.Started != 388 {
		t.Fatalf("worst case = %d, want 388", rep.Started)
	}
	// One more physical execution is still under the absolute ceiling but
	// would breach the S-1 budget if it were a replacement; a pilot attempt
	// is part of the planned model and simply consumes ceiling.
	records = append(records, rec("STARTED", "extra_pilot", KindPilot, ""))
	records = append(records, rec("FINISHED", "extra_pilot", "", OutcomeSuccess))
	if rep := ValidateExecutionLedger(records); !rep.OK {
		t.Fatalf("389th execution must remain ceiling-legal: %v", rep.Errors)
	}
}
