package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"owndbms/research/experiments/phase_i7/harness2"
)

// Helper subprocesses exercise process isolation, retry, timeout and resume
// without invoking an engine or performing a campaign trial.
func TestMain(m *testing.M) {
	if mode := os.Getenv("XCAMPAIGN_TEST_HELPER"); mode != "" {
		args := map[string]string{}
		for i := 1; i+1 < len(os.Args); i += 2 {
			args[os.Args[i]] = os.Args[i+1]
		}
		b, err := os.ReadFile(args["-spec"])
		if err != nil {
			os.Exit(8)
		}
		var s harness2.Spec
		if json.Unmarshal(b, &s) != nil {
			os.Exit(8)
		}
		if mode == "timeout" {
			time.Sleep(time.Second)
			os.Exit(8)
		}
		if mode == "fail" || (mode == "retry" && strings.HasSuffix(args["-out"], "_a1.json")) {
			os.Exit(9)
		}
		r := harness2.TrialResult{TrialID: s.TrialID, Status: "SUCCESS", Operations: harness2.OperationCounts{ReadOpsCompleted: 1}, Timestamps: harness2.TimestampInfo{MeasuredWindowNs: 1}}
		if err = writeJSON(args["-out"], map[string]any{"schema": "i7.trial.v1", "trial_id": s.TrialID, "status": "SUCCESS", "result": r}); err != nil {
			os.Exit(8)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPlan(t *testing.T) {
	p := makePlan(20261030)
	if len(p.Cells) != 17 || len(p.Trials) != 519 {
		t.Fatalf("cells %d trials %d", len(p.Cells), len(p.Trials))
	}
	if !reflect.DeepEqual(p, makePlan(20261030)) {
		t.Fatal("plan is not deterministic")
	}
	if reflect.DeepEqual(p, makePlan(20261031)) {
		t.Fatal("master seed has no effect")
	}
	ids := map[string]bool{}
	trips := map[string]map[int]bool{}
	orders := map[string]map[[3]string]int{}
	seeds := map[int64]bool{}
	anchors := 0
	for i := 0; i < len(p.Trials); i += 3 {
		group := p.Trials[i : i+3]
		s := group[0]
		order := [3]string{}
		for j, g := range group {
			if g.Seed != s.Seed || g.Cell != s.Cell || g.Triplet != s.Triplet {
				t.Fatal("triplet pairing broken")
			}
			if ids[g.TrialID] {
				t.Fatal("duplicate id")
			}
			ids[g.TrialID] = true
			if g.RunlistPosition != i+j+1 || g.OrderPosition != j+1 {
				t.Fatal("positions broken")
			}
			if _, err := g.ResolveMode(); err != nil {
				t.Fatal(err)
			}
			order[j] = g.Arm
		}
		if s.Cell == "ANCHOR" {
			anchors += 3
			if s.Seed != pairedSeed(p.MasterSeed, "ANCHOR", 0) || s.Seed != p.Trials[0].Seed {
				t.Fatal("anchor request seed changed across milestones")
			}
			if i != 0 && i != 258 && i != 516 {
				t.Fatalf("anchor at %d", i)
			}
			if s.Block != "X1" || s.ReaderConcurrency != 4 || s.ReadIODelayUs != 0 {
				t.Fatal("anchor treatment differs")
			}
			for j, trial := range group {
				if trial.TrialID != fmt.Sprintf("ANCHOR_t%02d_%s", trial.Triplet, trial.Arm) || trial.Arm != permutations[trial.Triplet%6][j] {
					t.Fatal("anchor identity or permutation changed")
				}
			}
			continue
		}
		if seeds[s.Seed] || s.Seed == p.Trials[0].Seed {
			t.Fatal("non-anchor seed collision across triplets")
		}
		seeds[s.Seed] = true
		if trips[s.Cell] == nil {
			trips[s.Cell] = map[int]bool{}
			orders[s.Cell] = map[[3]string]int{}
		}
		trips[s.Cell][s.Triplet] = true
		orders[s.Cell][order]++
	}
	for k := 1; k <= 10; k++ {
		order := rand.New(rand.NewSource(int64(uint64(20261030) + uint64(k)))).Perm(len(p.Cells))
		actual := []string{}
		for _, trial := range p.Trials {
			if trial.Cell != "ANCHOR" && trial.Triplet == k && trial.OrderPosition == 1 {
				actual = append(actual, trial.Cell)
			}
		}
		for j, index := range order {
			if actual[j] != p.Cells[index].ID {
				t.Fatal("round shuffle is not seeded from master_seed + round")
			}
		}
	}
	if anchors != 9 {
		t.Fatal("anchor count")
	}
	for index, c := range p.Cells {
		if len(trips[c.ID]) != 10 {
			t.Fatalf("%s missing triplets", c.ID)
		}
		for _, perm := range permutations {
			if n := orders[c.ID][perm]; n < 1 || n > 2 {
				t.Fatalf("%s unbalanced %v = %d", c.ID, perm, n)
			}
		}
		for _, s := range p.Trials {
			if s.Cell != c.ID {
				continue
			}
			if s.Arm != permutations[(s.Triplet+index)%6][s.OrderPosition-1] {
				t.Fatal("arm assignment differs from design")
			}
		}
	}
}

func TestApprovalAndPlanResume(t *testing.T) {
	dir := t.TempDir()
	planBytes, _ := jsonBytes(makePlan(1))
	runnerHash := digest([]byte("runner"))
	planHash := digest(planBytes)
	if err := preparePlan(dir, planBytes, runnerHash); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "platform.json"), platform{CampaignID: campaignID, RunnerSHA256: runnerHash, PlanSHA256: planHash}); err != nil {
		t.Fatal(err)
	}
	if err := preparePlan(dir, planBytes, runnerHash); err != nil {
		t.Fatal(err)
	}
	if err := preparePlan(dir, append(planBytes, ' '), runnerHash); err == nil {
		t.Fatal("changed plan accepted")
	}
	if err := preparePlan(dir, planBytes, digest([]byte("other"))); err == nil {
		t.Fatal("changed runner accepted")
	}
	path := filepath.Join(dir, "approval.json")
	a := approval{campaignID, true, runnerHash, planHash}
	if err := writeJSON(path, a); err != nil {
		t.Fatal(err)
	}
	if err := checkApproval(path, runnerHash, planHash); err != nil {
		t.Fatal(err)
	}
	if err := checkApproval(path, runnerHash, strings.Repeat("0", 64)); err == nil {
		t.Fatal("plan mismatch accepted")
	}
	if err := checkApproval(path, strings.Repeat("0", 64), planHash); err == nil {
		t.Fatal("runner mismatch accepted")
	}
	if err := checkApproval("", runnerHash, planHash); err == nil {
		t.Fatal("missing approval accepted")
	}
	a.Approved = false
	_ = writeJSON(path, a)
	if err := checkApproval(path, runnerHash, planHash); err == nil {
		t.Fatal("unapproved accepted")
	}
	arbitrary := t.TempDir()
	_ = os.WriteFile(filepath.Join(arbitrary, "unrelated"), []byte("x"), 0644)
	if err := preparePlan(arbitrary, planBytes, runnerHash); err == nil {
		t.Fatal("arbitrary nonempty out accepted")
	}
}

func TestExecutionRetryResume(t *testing.T) {
	for _, mode := range []string{"success", "retry", "fail", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XCAMPAIGN_TEST_HELPER", mode)
			runner, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			o := options{runner: runner, out: dir, data: filepath.Join(dir, "data"), limit: 1, timeout: 5 * time.Second}
			if mode == "timeout" {
				o.timeout = 100 * time.Millisecond
			}
			p := makePlan(1)
			p.Trials = p.Trials[:2]
			if err = execute(context.Background(), o, p, platform{}, io.Discard); err != nil {
				t.Fatal(err)
			}
			latest, err := readLedger(filepath.Join(dir, "ledger.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			r := latest[p.Trials[0].TrialID]
			wantAttempt := 2
			if mode == "success" {
				wantAttempt = 1
			}
			if r.Attempt != wantAttempt {
				t.Fatalf("%+v", r)
			}
			wantStatus := "SUCCESS"
			if mode == "fail" {
				wantStatus = "FAILED"
			}
			if mode == "timeout" {
				wantStatus = "TIMEOUT"
			}
			if r.Status != wantStatus {
				t.Fatalf("%+v", r)
			}
			if todo := pending(p, latest); len(todo) != 1 || todo[0].TrialID != p.Trials[1].TrialID {
				t.Fatal("resume did not skip finished/exhausted trial")
			}
			// Resume uses only the next unfinished row, preserving the first attempt.
			if err = execute(context.Background(), o, p, platform{}, io.Discard); err != nil {
				t.Fatal(err)
			}
			again, err := readLedger(filepath.Join(dir, "ledger.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if again[p.Trials[0].TrialID] != r || len(pending(p, again)) != 0 {
				t.Fatal("resume replayed completed trial")
			}
		})
	}
}

func TestInterruptedAndFailureRate(t *testing.T) {
	p := makePlan(1)
	latest := map[string]record{}
	latest[p.Trials[0].TrialID] = record{Event: "STARTED", TrialID: p.Trials[0].TrialID, Attempt: 1, Status: "STARTED"}
	if todo := pending(p, latest); len(todo) != 519 {
		t.Fatal("interrupted first attempt not retried")
	}
	for i := 0; i < 40; i++ {
		status := "SUCCESS"
		if i < 2 {
			status = "FAILED"
		}
		latest[p.Trials[i].TrialID] = record{Attempt: 2, Status: status}
	}
	if failureLimit(p, latest) {
		t.Fatal("exactly 5% should not abort")
	}
	latest[p.Trials[2].TrialID] = record{Attempt: 2, Status: "FAILED"}
	if !failureLimit(p, latest) {
		t.Fatal("7.5% should abort")
	}
	delete(latest, p.Trials[39].TrialID)
	if failureLimit(p, latest) {
		t.Fatal("fewer than 40 should not abort")
	}
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	if err := os.WriteFile(path, []byte(`{"event":"STARTED","trial_id":"t","attempt":1,"status":"STARTED"}
{"event":"FIN`), 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if err = appendRecord(f, record{Event: "FINISHED", TrialID: "t", Attempt: 2, Status: "SUCCESS"}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got, err := readLedger(path)
	if err != nil || got["t"].Status != "SUCCESS" {
		t.Fatal(fmt.Sprint(got, err))
	}
}
