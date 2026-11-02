package main

// Unit tests for the i7trial dispatch surface (Phase I7 engineering preflight).
// These tests cover block dispatch and envelope validation only; they never
// execute a trial.

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	want := []string{"E1", "E2", "E3", "E4", "E5", "D3", "PF", "SMOKE", "X1", "X2", "X3", "ANCHOR"}
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

// TestCheckApprovalBindings covers the approval-binding refusals at the
// runner level (the driver-level scenarios of the G2 orchestration suite
// became revocation assertions when the legacy execution authority was
// removed): campaign, protocol, gate, runner hash, and block-set bindings
// must all match or execution is refused.
func TestCheckApprovalBindings(t *testing.T) {
	selfHash := runtimeInfo().ExeSHA256
	if selfHash == "" {
		t.Skip("runner self-hash unavailable in this environment")
	}

	write := func(t *testing.T, mutate func(a *Approval)) string {
		t.Helper()
		a := Approval{
			CampaignID:      campaignID,
			ProtocolVersion: protocolVersion,
			Gate:            "G4",
			Approved:        true,
			ApprovedBy:      "unit-test",
			ApprovedAt:      "2026-10-29T00:00:00Z",
			Scope:           "SYNTHETIC - unit test only",
			RunlistSHA256:   "0f6076500a56a05b4a1c1b5b8b1c9b5f4f3e2d1c0b9a8a7b6c5d4e3f2a1b0c9d",
			RunnerSHA256:    selfHash,
			AllowedBlocks:   []string{"D3", "E1"},
			AllowedPhases:   []string{"confirmatory", "drift_anchor"},
			MaxTrials:       400,
			AllowDirty:      true,
		}
		if mutate != nil {
			mutate(&a)
		}
		path := filepath.Join(t.TempDir(), "APPROVAL.json")
		b, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("marshal approval: %v", err)
		}
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatalf("write approval: %v", err)
		}
		return path
	}

	// The fully valid approval must pass for an approved block.
	if _, err := checkApproval(write(t, nil), "G4", "D3"); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}

	cases := map[string]func(a *Approval){
		"not approved":       func(a *Approval) { a.Approved = false },
		"wrong campaign":     func(a *Approval) { a.CampaignID = "OTHER" },
		"wrong protocol":     func(a *Approval) { a.ProtocolVersion = "0.0.1" },
		"wrong gate":         func(a *Approval) { a.Gate = "G3" },
		"wrong runner hash":  func(a *Approval) { a.RunnerSHA256 = strings.Repeat("0", 64) },
		"empty runner hash":  func(a *Approval) { a.RunnerSHA256 = "" },
		"empty block set":    func(a *Approval) { a.AllowedBlocks = nil },
		"block not approved": func(a *Approval) { a.AllowedBlocks = []string{"E1"} },
	}
	for name, mutate := range cases {
		if _, err := checkApproval(write(t, mutate), "G4", "D3"); err == nil {
			t.Fatalf("%s: expected checkApproval to refuse", name)
		}
	}

	// Missing gate or approval file refuse outright.
	if _, err := checkApproval("", "G4", "D3"); err == nil {
		t.Fatal("missing gate accepted")
	}
	if _, err := checkApproval(filepath.Join(t.TempDir(), "missing.json"), "G4", "D3"); err == nil {
		t.Fatal("missing approval file accepted")
	}
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

func TestXApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approval.json")
	a := Approval{CampaignID: "X-readcost-v1", Approved: true, RunnerSHA256: runtimeInfo().ExeSHA256, PlanSHA256: strings.Repeat("a", 64)}
	for _, campaign := range []string{"X-readcost-v1", "X-intervention-v1", "X-compaction-v1", "X-tablecount-v1"} {
		a.CampaignID = campaign
		b, _ := json.Marshal(a)
		if err := os.WriteFile(path, b, 0644); err != nil {
			t.Fatal(err)
		}
		for _, block := range []string{"X1", "X2", "X3", "ANCHOR"} {
			if _, err := checkApproval(path, "X", block); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := checkApproval(path, "X", "E2"); err == nil {
		t.Fatal("X approval authorized E2")
	}
	if _, err := checkApproval(path, "G4", "X1"); err == nil {
		t.Fatal("X approval authorized G4")
	}
	a.PlanSHA256 = ""
	b, _ := json.Marshal(a)
	_ = os.WriteFile(path, b, 0644)
	if _, err := checkApproval(path, "X", "X1"); err == nil {
		t.Fatal("missing plan hash accepted")
	}
	a.PlanSHA256 = strings.Repeat("a", 64)
	a.CampaignID = "X-other-v1"
	b, _ = json.Marshal(a)
	_ = os.WriteFile(path, b, 0644)
	if _, err := checkApproval(path, "X", "X1"); err == nil {
		t.Fatal("unknown X campaign accepted")
	}
}
