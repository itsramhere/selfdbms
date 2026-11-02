package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"owndbms/research/experiments/phase_i7/harness2"
)

func TestTablecountPlan(t *testing.T) {
	const master = 20261105
	p := makeTablecountPlan(master)
	if p.CampaignID != "X-tablecount-v1" || len(p.Cells) != 16 || len(p.Trials) != 480 {
		t.Fatal("dimensions")
	}
	if !reflect.DeepEqual(p, makeTablecountPlan(master)) || reflect.DeepEqual(p, makeTablecountPlan(master+1)) {
		t.Fatal("determinism")
	}
	for i, c := range p.Cells {
		n, r, w := []int{4, 16, 64, 256}[i/4], []int{4, 16}[(i/2)%2], i%2
		want := harness2.Spec{
			Schema: "i7.trial-spec.v1", Block: "X1", Cell: fmt.Sprintf("T1_n%03d_r%02d_w%d", n, r, w), WorkloadID: "X-tablecount",
			ReaderConcurrency: r, WriterConcurrency: w, KeyspaceCardinality: 10240,
			InitialSSTableCount: n, InitialKeysPerSSTable: 10240 / n, ExpectedReaderSetSize: n,
			TargetReadHitRatio: .95, ReadDistribution: "uniform", WarmupDurationMs: 2000,
			MeasurementDurationMs: 10000, CooldownDurationMs: 200, WalSync: true,
			InstrumentSync: true, ReadPath: "block", MemtableThresholdBytes: 1 << 20,
		}
		if c.ID != want.Cell || !reflect.DeepEqual(c.Spec, want) {
			t.Fatalf("cell %d: %+v", i, c)
		}
		if mode, err := c.Spec.ResolveMode(); err != nil || mode != harness2.ModeClosedLoop {
			t.Fatalf("cell %s: %v %v", c.ID, mode, err)
		}
	}
	ids := map[string]bool{}
	for k := 1; k <= 10; k++ {
		order := rand.New(rand.NewSource(master + int64(k))).Perm(16)
		for j, index := range order {
			for pos, arm := range permutations[(k+index)%6] {
				i := (k-1)*48 + j*3 + pos
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
	if got := digest(b); got != "0bc0042b8b8ad5ba517c911e9ec9a7dab00ec1b1f0c78876fc26be969eddc048" {
		t.Fatal(got)
	}
	t.Log("tablecount plan SHA-256:", digest(b))
}
