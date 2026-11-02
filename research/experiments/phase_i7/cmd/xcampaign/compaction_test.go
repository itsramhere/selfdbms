package main

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"owndbms/research/experiments/phase_i7/harness2"
)

func TestFrozenInterventionHash(t *testing.T) {
	b, err := jsonBytes(makeInterventionPlan(20261103))
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(b); got != "2e44fcacb3e79dbddc079508cf663a5f794bd6c1f749567d115b2082adf83573" {
		t.Fatal(got)
	}
}

func TestCompactionPlan(t *testing.T) {
	const master = 20261104
	p := makeCompactionPlan(master)
	if p.CampaignID != "X-compaction-v1" || len(p.Cells) != 12 || len(p.Trials) != 360 {
		t.Fatal("dimensions")
	}
	if !reflect.DeepEqual(p, makeCompactionPlan(master)) || reflect.DeepEqual(p, makeCompactionPlan(master+1)) {
		t.Fatal("determinism")
	}
	ids := map[string]bool{}
	seeds := map[string]int64{}
	for k := 1; k <= 10; k++ {
		order := rand.New(rand.NewSource(master + int64(k))).Perm(12)
		for j, index := range order {
			for pos, arm := range permutations[(k+index)%6] {
				i := (k-1)*36 + j*3 + pos
				s := p.Trials[i]
				base := strings.TrimSuffix(strings.TrimSuffix(s.Cell, "_off"), "_on")
				key := fmt.Sprintf("%s/%d", base, k)
				if old, ok := seeds[key]; ok && old != s.Seed {
					t.Fatal("on/off pairing")
				}
				seeds[key] = s.Seed
				if ids[s.TrialID] || s.TrialID != fmt.Sprintf("%s_t%02d_%s", s.Cell, k, arm) {
					t.Fatal("identity")
				}
				ids[s.TrialID] = true
				if s.Cell != p.Cells[index].ID || s.Arm != arm || s.Triplet != k || s.OrderPosition != pos+1 || s.RunlistPosition != i+1 {
					t.Fatal("schedule")
				}
				trigger := 0
				if strings.HasSuffix(s.Cell, "_on") {
					trigger = 250
				}
				if s.CompactionTriggerMs != trigger || s.Seed != pairedSeed(master, base, k) {
					t.Fatal("treatment/seed")
				}
				if s.Block != "X3" || s.ReadPath != "block" || s.MemtableThresholdBytes != 16<<10 || s.WriterConcurrency != 1 || !s.WalSync || !s.InstrumentSync || s.WarmupDurationMs != 2000 || s.MeasurementDurationMs != 10000 || s.CooldownDurationMs != 200 {
					t.Fatal("common spec")
				}
				if _, err := s.ResolveMode(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	b, err := jsonBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(b); got != "5330cbddf01b0e55cbf1b13f1b92417e6f6521d315a0402f1c46136528b988a1" {
		t.Fatal(got)
	}
	t.Log("compaction plan SHA-256:", digest(b))
}

func TestCompactionErrorEnvelopeRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trial.json")
	s := makeCompactionPlan(20261104).Trials[0]
	r := harness2.TrialResult{TrialID: s.TrialID, Status: "SUCCESS",
		Operations:     harness2.OperationCounts{ReadOpsCompleted: 1},
		Timestamps:     harness2.TimestampInfo{MeasuredWindowNs: 1},
		LSMDiagnostics: harness2.Diagnostics{CompactionErrors: 1}}
	if err := writeJSON(path, map[string]any{"schema": "i7.trial.v1", "trial_id": s.TrialID, "status": "SUCCESS", "result": r}); err != nil {
		t.Fatal(err)
	}
	if got := resultStatus(path, s); got != "FAILED" {
		t.Fatal("engine fault accepted", got)
	}
}
