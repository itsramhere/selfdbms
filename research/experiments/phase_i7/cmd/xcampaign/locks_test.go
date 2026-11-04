package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"

	"owndbms/research/experiments/phase_i7/harness2"
)

func TestLocksPlan(t *testing.T) {
	const master = 20261109
	p := makeLocksPlan(master)
	if p.CampaignID != "X-locks-v1" || p.SettleMs != 20000 || len(p.Cells) != 24 || len(p.Trials) != 480 {
		t.Fatal("dimensions/pause")
	}
	if !reflect.DeepEqual(p, makeLocksPlan(master)) || reflect.DeepEqual(p, makeLocksPlan(master+1)) {
		t.Fatal("determinism/seed sensitivity")
	}
	var entries []string
	for _, r := range []int{4, 16} {
		for _, d := range []int{0, 20, 200, 2000} {
			entry := fmt.Sprintf("L1_r%02d_d%04d", r, d)
			entries = append(entries, entry)
			for j, policy := range []string{"go", "phasefair", "readerpref"} {
				c := p.Cells[(len(entries)-1)*3+j]
				want := sourceCell(cells(), fmt.Sprintf("X1_r%02d_d%04d", r, d))
				want.Cell = entry + "_" + policy
				if j > 0 {
					want.LockPolicy = policy
				}
				if c.ID != want.Cell || !reflect.DeepEqual(c.Spec, want) {
					t.Fatalf("cell %s", c.ID)
				}
				b, _ := json.Marshal(c.Spec)
				var fields map[string]any
				_ = json.Unmarshal(b, &fields)
				if j == 0 && fields["lock_policy"] != nil {
					t.Fatal("default must be omitted")
				}
				if _, err := c.Spec.ResolveMode(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	seen := map[string]bool{}
	row := 0
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(master + int64(k))).Perm(8) {
			for pos, run := range rand.New(rand.NewSource(master*1000 + int64(k)*100 + int64(i))).Perm(6) {
				want := p.Cells[i*3+run/2].Spec
				want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, entries[i], k), []string{"A", "AMIN"}[run%2]
				want.OrderPosition, want.RunlistPosition = pos+1, row+1
				want.TrialID = fmt.Sprintf("%s_t%02d_%s", want.Cell, k, want.Arm)
				if !want.IsLocksCell() || seen[want.TrialID] || !reflect.DeepEqual(want, p.Trials[row]) {
					t.Fatalf("schedule row %d: %+v", row, p.Trials[row])
				}
				seen[want.TrialID] = true
				row++
			}
		}
	}
}

func TestLocksZeroCommitsAreAnOutcome(t *testing.T) {
	s := makeLocksPlan(20261109).Trials[0]
	r := harness2.TrialResult{TrialID: s.TrialID, Status: "SUCCESS",
		Operations: harness2.OperationCounts{ReadOpsCompleted: 10},
		Timestamps: harness2.TimestampInfo{MeasuredWindowNs: 1}}
	path := filepath.Join(t.TempDir(), "trial.json")
	write := func() {
		t.Helper()
		if err := writeJSON(path, map[string]any{"schema": "i7.trial.v1", "trial_id": s.TrialID, "status": "SUCCESS", "result": r}); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if resultStatus(path, s) != "SUCCESS" {
		t.Fatal("zero commits rejected")
	}
	r.Operations.ValidationErrorCount = 1
	write()
	if resultStatus(path, s) != "FAILED" {
		t.Fatal("engine fault accepted")
	}
}
