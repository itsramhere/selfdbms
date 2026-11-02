package main

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFrozenReadcostHash(t *testing.T) {
	b, err := jsonBytes(makePlan(20261030))
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(b); got != "68ad96f878c19bcb24c4c7f68e39e8f371420274c76c483f7dab54618c088136" {
		t.Fatal(got)
	}
}

func TestInterventionPlan(t *testing.T) {
	p := makeInterventionPlan(20261103)
	if p.CampaignID != "X-intervention-v1" || len(p.Cells) != 8 || len(p.Trials) != 160 {
		t.Fatal("bad dimensions")
	}
	if !reflect.DeepEqual(p, makeInterventionPlan(20261103)) || reflect.DeepEqual(p, makeInterventionPlan(20261104)) {
		t.Fatal("bad determinism")
	}
	ids := map[string]bool{}
	groups := map[string]bool{}
	for i := 0; i < len(p.Trials); i += 4 {
		group := p.Trials[i : i+4]
		seen := map[string]bool{}
		base := strings.TrimSuffix(strings.TrimSuffix(group[0].Cell, "_second"), "_inline")
		k := i/32 + 1
		cellIndex := rand.New(rand.NewSource(int64(20261103 + k))).Perm(8)[(i/4)%8]
		if base != p.Cells[cellIndex].ID || groups[fmt.Sprintf("%s/%d", base, k)] {
			t.Fatal("cell order or duplicate cell-round", base, k)
		}
		groups[fmt.Sprintf("%s/%d", base, k)] = true
		order := rand.New(rand.NewSource(20261103*1000 + int64(k)*100 + int64(cellIndex))).Perm(4)
		for pos, s := range group {
			condition := "second"
			if s.InlineFlushCheck {
				condition = "inline"
			}
			if s.Cell != base+"_"+condition || (s.Arm != "A" && s.Arm != "AMIN") {
				t.Fatal(s)
			}
			if condition != []string{"second", "inline"}[order[pos]/2] || s.Arm != []string{"A", "AMIN"}[order[pos]%2] {
				t.Fatal("condition/arm shuffle", s)
			}
			if ids[s.TrialID] || seen[condition+s.Arm] {
				t.Fatal("duplicate", s.TrialID)
			}
			ids[s.TrialID], seen[condition+s.Arm] = true, true
			if s.Seed != pairedSeed(20261103, base, s.Triplet) || s.Seed != group[0].Seed || s.Triplet != i/32+1 {
				t.Fatal("pairing", s)
			}
			if s.OrderPosition != pos+1 || s.RunlistPosition != i+pos+1 || s.TrialID != fmt.Sprintf("%s_t%02d_%s", s.Cell, s.Triplet, s.Arm) {
				t.Fatal("positions", s)
			}
			if s.Block != "X1" || s.ReadPath != "block" || s.MemtableThresholdBytes != 1<<20 || s.WriterConcurrency != 1 {
				t.Fatal("config", s)
			}
		}
	}
	t.Logf("intervention plan SHA-256: %s", func() string { b, _ := jsonBytes(p); return digest(b) }())
}

func TestInterventionApprovalAndResumeBinding(t *testing.T) {
	dir := t.TempDir()
	p := makeInterventionPlan(20261103)
	b, _ := jsonBytes(p)
	runnerHash, planHash := digest([]byte("runner")), digest(b)
	if err := preparePlan(dir, b, runnerHash); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "platform.json"), platform{CampaignID: p.CampaignID, RunnerSHA256: runnerHash, PlanSHA256: planHash}); err != nil {
		t.Fatal(err)
	}
	if err := preparePlan(dir, b, runnerHash); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "approval.json")
	if err := writeJSON(path, approval{p.CampaignID, true, runnerHash, planHash}); err != nil {
		t.Fatal(err)
	}
	if err := checkApproval(path, runnerHash, planHash, p.CampaignID); err != nil {
		t.Fatal(err)
	}
	if err := checkApproval(path, runnerHash, planHash, campaignID); err == nil {
		t.Fatal("intervention approval authorized readcost")
	}
	if err := writeJSON(filepath.Join(dir, "platform.json"), platform{CampaignID: campaignID, RunnerSHA256: runnerHash, PlanSHA256: planHash}); err != nil {
		t.Fatal(err)
	}
	if err := preparePlan(dir, b, runnerHash); err == nil {
		t.Fatal("wrong campaign accepted on resume")
	}
}
