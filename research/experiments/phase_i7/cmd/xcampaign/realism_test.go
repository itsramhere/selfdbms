package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestRealismPlan(t *testing.T) {
	const master = 20261111
	p := makeRealismPlan(master)
	if p.CampaignID != "X-realism-v1" || p.SettleMs != 20000 || len(p.Cells) != 8 || len(p.Trials) != 240 {
		t.Fatal("dimensions/pause")
	}
	if !reflect.DeepEqual(p, makeRealismPlan(master)) || reflect.DeepEqual(p, makeRealismPlan(master+1)) {
		t.Fatal("determinism/seed sensitivity")
	}
	ids := []string{"D1_r04_w1_direct", "D1_r16_w1_direct", "D1_r04_w2_direct", "D1_r16_w2_direct", "D1_r04_w4_direct", "D1_r16_w4_direct", "D2_r04_w1_buffered", "D2_r16_w1_buffered"}
	for i, c := range p.Cells {
		if c.ID != ids[i] {
			t.Fatal("cell order")
		}
		want := sourceCell(cells(), fmt.Sprintf("X1_r%02d_d0000", c.Spec.ReaderConcurrency))
		want.Cell = c.ID
		if i < 6 {
			want.ReadPath = "direct"
			want.WriterConcurrency = []int{1, 2, 4}[i/2]
		}
		if !reflect.DeepEqual(c.Spec, want) {
			t.Fatalf("treatment %s", c.ID)
		}
	}
	b, err := jsonBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(b); got != "e41320390c819d25fe74132d242e555e5831e063f71cba2d51eae0fc1d34681a" {
		t.Fatalf("realism hash changed: %s", got)
	}
	row, seen := 0, map[string]bool{}
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(master + int64(k))).Perm(8) {
			for pos, arm := range permutations[(k+i)%6] {
				want := p.Cells[i].Spec
				want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, fmt.Sprintf("D_r%02d", want.ReaderConcurrency), k), arm
				want.OrderPosition, want.RunlistPosition = pos+1, row+1
				want.TrialID = fmt.Sprintf("%s_t%02d_%s", want.Cell, k, arm)
				if seen[want.TrialID] || !reflect.DeepEqual(want, p.Trials[row]) {
					t.Fatalf("schedule %d", row)
				}
				if _, err := want.ResolveMode(); err != nil {
					t.Fatal(err)
				}
				seen[want.TrialID] = true
				row++
			}
		}
	}
}
