package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestInputopenPlan(t *testing.T) {
	const master = 20261110
	p := makeInputopenPlan(master)
	if p.CampaignID != "X-inputopen-v1" || p.SettleMs != 20000 || len(p.Cells) != 12 || len(p.Trials) != 300 {
		t.Fatal("dimensions/pause")
	}
	if !reflect.DeepEqual(p, makeInputopenPlan(master)) || reflect.DeepEqual(p, makeInputopenPlan(master+1)) {
		t.Fatal("determinism/seed sensitivity")
	}
	var entries []string
	for _, r := range []int{4, 16} {
		for _, d := range []int{0, 200, 2000} {
			entry := fmt.Sprintf("O1_r%02d_d%04d", r, d)
			entries = append(entries, entry)
			for j, path := range []string{"default", "aligned"} {
				want := sourceCell(makeCompactionPlan(0).Cells, fmt.Sprintf("C1_r%02d_d%04d_on", r, d))
				want.Cell, want.CompactionOpenOutsideLock = entry+"_"+path, j == 1
				c := p.Cells[(len(entries)-1)*2+j]
				if c.ID != want.Cell || !reflect.DeepEqual(c.Spec, want) {
					t.Fatalf("cell %s", c.ID)
				}
				b, _ := json.Marshal(c.Spec)
				var fields map[string]any
				_ = json.Unmarshal(b, &fields)
				if j == 0 && fields["compaction_open_outside_lock"] != nil {
					t.Fatal("default must be omitted")
				}
				if _, err := c.Spec.ResolveMode(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	row, seen := 0, map[string]bool{}
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(master + int64(k))).Perm(6) {
			for pos, run := range rand.New(rand.NewSource(master*1000 + int64(k)*100 + int64(i))).Perm(5) {
				cell, arm := run/2, []string{"A", "AMIN"}[run%2]
				if run == 4 {
					cell, arm = 0, "B"
				}
				want := p.Cells[i*2+cell].Spec
				want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, entries[i], k), arm
				want.OrderPosition, want.RunlistPosition = pos+1, row+1
				want.TrialID = fmt.Sprintf("%s_t%02d_%s", want.Cell, k, arm)
				if seen[want.TrialID] || !reflect.DeepEqual(want, p.Trials[row]) {
					t.Fatalf("schedule row %d", row)
				}
				seen[want.TrialID] = true
				row++
			}
		}
	}
	if row != 300 || len(entries) != 6 {
		t.Fatal("entry/trial count")
	}
	b, err := jsonBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := digest(b); got != "5b685feb2327ff8d9b1e72b4bc0637570f0fe0014690ec9e214e84f163893a98" {
		t.Fatalf("inputopen plan hash changed: %s", got)
	}
}
