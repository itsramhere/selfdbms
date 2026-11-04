package main

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestMemtablePlan(t *testing.T) {
	const master = 20261112
	p := makeMemtablePlan(master)
	if p.CampaignID != "X-memtable-v1" || p.SettleMs != 20000 || len(p.Cells) != 16 || len(p.Trials) != 160 {
		t.Fatal("dimensions/pause")
	}
	if !reflect.DeepEqual(p, makeMemtablePlan(master)) || reflect.DeepEqual(p, makeMemtablePlan(master+1)) {
		t.Fatal("determinism/seed sensitivity")
	}
	i := 0
	for _, rate := range []int{183, 366, 549, 732} {
		for _, domain := range []string{"same", "shadow"} {
			for _, sync := range []string{"on", "off"} {
				want := sourceCell(makeFollowupPlan(0).Cells, "F1_w0183_m16")
				want.Cell = fmt.Sprintf("M_w%04d_%s_%s", rate, domain, sync)
				want.PacedWriteRatePerSec, want.WalSync = float64(rate), sync == "on"
				if domain == "shadow" {
					want.WriteKeyDomain = "shadow"
				}
				if p.Cells[i].ID != want.Cell || !reflect.DeepEqual(p.Cells[i].Spec, want) {
					t.Fatalf("cell %d: %+v", i, p.Cells[i])
				}
				if _, err := want.ResolveMode(); err != nil {
					t.Fatal(err)
				}
				i++
			}
		}
	}
	row := 0
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(master + int64(k))).Perm(16) {
			want := p.Cells[i].Spec
			want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, want.Cell, k), "B"
			want.OrderPosition, want.RunlistPosition = 1, row+1
			want.TrialID = fmt.Sprintf("%s_t%02d_B", want.Cell, k)
			if !reflect.DeepEqual(p.Trials[row], want) {
				t.Fatalf("row %d", row)
			}
			row++
		}
	}
	b, err := jsonBytes(p)
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = "8560df67fb5fae4f70f9a93ed8451e5ffa70983b049a3cfe318efd4fa8f5cad9"
	if got := digest(b); got != wantHash {
		t.Fatalf("memtable plan hash changed: %s", got)
	}
}
