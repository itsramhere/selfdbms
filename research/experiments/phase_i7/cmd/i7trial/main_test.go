package main

// Unit tests for the i7trial dispatch surface (Phase I7 engineering preflight).
// These tests cover block dispatch and envelope validation only; they never
// execute a trial.

import (
	"strings"
	"testing"

	"owndbms/research/experiments/phase_i7/harness2"
)

// TestD3BlockIsDispatchable is the regression test for the D3
// runner-dispatch correction (R1.2e §6 item 1): a drift-anchor row in the
// runlist must be accepted by this runner, resolved to the closed-loop mode,
// and never mistaken for a separate-runner block.
func TestD3BlockIsDispatchable(t *testing.T) {
	if !containsFold(supportedBlocks, "D3") {
		t.Fatal("D3 missing from supportedBlocks: runlists with drift anchors would be rejected")
	}
	for _, b := range separateRunnerBlocks {
		if strings.EqualFold(b, "D3") {
			t.Fatal("D3 must not be dispatched to the separate pebble_runner")
		}
	}
	spec := harness2.Spec{
		Schema:  "i7.trial-spec.v1",
		TrialID: "d3_anchor",
		Block:   "D3",
		Cell:    "C2-01",
		Arm:     "A",
	}
	mode, err := spec.ResolveMode()
	if err != nil {
		t.Fatalf("D3 spec must resolve: %v", err)
	}
	if mode != harness2.ModeClosedLoop {
		t.Fatalf("D3 must resolve to closed-loop mode, got %v", mode)
	}
}

// TestSupportedBlocksComplete locks the full dispatch surface.
func TestSupportedBlocksComplete(t *testing.T) {
	want := []string{"E1", "E2", "E3", "E4", "E5", "D3", "PF", "SMOKE"}
	if len(supportedBlocks) != len(want) {
		t.Fatalf("supportedBlocks = %v, want %v", supportedBlocks, want)
	}
	for _, b := range want {
		if !containsFold(supportedBlocks, b) {
			t.Fatalf("supportedBlocks missing %q", b)
		}
	}
	// E7 is owned by pebble_runner and must stay rejected here.
	if containsFold(supportedBlocks, "E7") {
		t.Fatal("E7 must not be dispatchable by i7trial")
	}
}

func validEnvelope() *Envelope {
	e := &Envelope{
		Schema:  "i7.trial.v1",
		TrialID: "t1",
		Block:   "D3",
		Arm:     "A",
		Status:  "SUCCESS",
		Result: &harness2.TrialResult{
			TrialID: "t1",
			Operations: harness2.OperationCounts{
				ReadOpsCompleted: 10,
			},
		},
	}
	e.Result.Timestamps.MeasuredWindowNs = 1
	return e
}

// TestValidateEnvelopeErrorPaths covers the fail-closed envelope checks that
// keep malformed records out of the ledger.
func TestValidateEnvelopeErrorPaths(t *testing.T) {
	valid := validEnvelope()
	if err := validateEnvelope(valid); err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}

	cases := map[string]func(e *Envelope){
		"schema mismatch":     func(e *Envelope) { e.Schema = "other" },
		"missing identity":    func(e *Envelope) { e.TrialID = "" },
		"missing result":      func(e *Envelope) { e.Result = nil },
		"result id mismatch":  func(e *Envelope) { e.Result.TrialID = "other" },
		"zero completed rows": func(e *Envelope) { e.Result.Operations.ReadOpsCompleted = 0 },
		"zero window":         func(e *Envelope) { e.Result.Timestamps.MeasuredWindowNs = 0 },
		"validation errors":   func(e *Envelope) { e.Result.Operations.ValidationErrorCount = 1 },
	}
	for name, mutate := range cases {
		e := validEnvelope()
		mutate(e)
		if err := validateEnvelope(e); err == nil {
			t.Fatalf("%s: expected validateEnvelope to fail", name)
		}
	}

	// Failures are recorded, not rejected.
	failed := validEnvelope()
	failed.Status = "FAILED"
	failed.Result = nil
	if err := validateEnvelope(failed); err != nil {
		t.Fatalf("FAILED envelope must be recorded, not rejected: %v", err)
	}
}
