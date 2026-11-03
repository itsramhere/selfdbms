package main

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"testing"

	"owndbms/research/experiments/phase_i7/harness2"
)

func TestPacedPlan(t *testing.T) {
	const master = 20261106
	p := makePacedPlan(master)
	if p.CampaignID != "X-paced-v1" || len(p.Cells) != 12 || len(p.Trials) != 360 {
		t.Fatal("dimensions")
	}
	if !reflect.DeepEqual(p, makePacedPlan(master)) || reflect.DeepEqual(p, makePacedPlan(master+1)) {
		t.Fatal("determinism")
	}
	for i, c := range p.Cells {
		d, f, readMode := []int{0, 200, 2000}[i/4], []int{20, 80}[(i/2)%2], []string{"closed", "open"}[i%2]
		want := harness2.Spec{
			Schema: "i7.trial-spec.v1", Block: "X4", Cell: fmt.Sprintf("P1_d%04d_f%02d_%s", d, f, readMode), WorkloadID: "X-paced",
			ReaderConcurrency: 16, WriterConcurrency: 1, KeyspaceCardinality: 10000,
			InitialSSTableCount: 4, InitialKeysPerSSTable: 2500, ExpectedReaderSetSize: 4,
			TargetReadHitRatio: .95, ReadDistribution: "uniform", WarmupDurationMs: 2000,
			MeasurementDurationMs: 10000, CooldownDurationMs: 200, WalSync: true,
			InstrumentSync: true, ReadPath: "block", MemtableThresholdBytes: 1 << 20, ReadIODelayUs: d,
			PacedWriteRatePerSec: [3][2]float64{{183, 732}, {155, 619}, {72, 286}}[i/4][(i/2)%2], MaxLatenessMs: 1000,
		}
		wantMode := harness2.ModePacedWrites
		if readMode == "open" {
			want.OpenLoopRatePerSec = []float64{12500, 8000, 2850}[i/4]
			wantMode = harness2.ModeOpenReadsPacedWrites
		}
		if c.ID != want.Cell || !reflect.DeepEqual(c.Spec, want) {
			t.Fatalf("cell %d: %+v", i, c)
		}
		if mode, err := c.Spec.ResolveMode(); err != nil || mode != wantMode {
			t.Fatalf("cell %s: %v %v", c.ID, mode, err)
		}
	}
	ids := map[string]bool{}
	for k := 1; k <= 10; k++ {
		order := rand.New(rand.NewSource(master + int64(k))).Perm(12)
		for j, index := range order {
			for pos, arm := range permutations[(k+index)%6] {
				i := (k-1)*36 + j*3 + pos
				s := p.Trials[i]
				want := p.Cells[index].Spec
				want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, want.Cell, k), arm
				want.OrderPosition, want.RunlistPosition = pos+1, i+1
				want.TrialID = fmt.Sprintf("%s_t%02d_%s", want.Cell, k, arm)
				if ids[s.TrialID] || !reflect.DeepEqual(s, want) {
					t.Fatalf("schedule row %d: %+v", i, s)
				}
				ids[s.TrialID] = true
			}
		}
	}
	b, err := jsonBytes(p)
	if err != nil {
		t.Fatal(err)
	}

	if got := digest(b); got != "384aace771c82b3e16eb0e1cb5363678cfff075a420ce8fcd70045ad82cb90e8" {
		t.Fatal(got)
	}
	t.Log("paced plan SHA-256:", digest(b))
}

func TestPacedZeroReadsAreAnOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trial.json")
	s := makePacedPlan(20261106).Trials[0]
	r := harness2.TrialResult{TrialID: s.TrialID, Block: "X4", Status: "SUCCESS",
		Timestamps: harness2.TimestampInfo{MeasuredWindowNs: 1}}
	if err := writeJSON(path, map[string]any{"schema": "i7.trial.v1", "trial_id": s.TrialID, "status": "SUCCESS", "result": r}); err != nil {
		t.Fatal(err)
	}
	if got := resultStatus(path, s); got != "SUCCESS" {
		t.Fatal("total abandonment rejected", got)
	}
	s.Block = "X1"
	if got := resultStatus(path, s); got != "FAILED" {
		t.Fatal("older block zero-read rule changed", got)
	}
}
