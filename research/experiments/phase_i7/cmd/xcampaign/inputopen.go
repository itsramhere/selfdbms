package main

import (
	"fmt"
	"math/rand"
)

// An entry crosses two input-open paths for A/AMIN, with one default B.
func makeInputopenPlan(master uint64) Plan {
	p := Plan{CampaignID: "X-inputopen-v1", MasterSeed: master, SettleMs: settledPauseMs,
		SeedEncoding: "SHA256(decimal master|entry_id|decimal triplet), first 8 bytes big-endian int64"}
	var entries []string
	for _, r := range []int{4, 16} {
		for _, d := range []int{0, 200, 2000} {
			entry := fmt.Sprintf("O1_r%02d_d%04d", r, d)
			entries = append(entries, entry)
			for _, path := range []string{"default", "aligned"} {
				s := sourceCell(makeCompactionPlan(0).Cells, fmt.Sprintf("C1_r%02d_d%04d_on", r, d))
				s.Cell = entry + "_" + path
				s.CompactionOpenOutsideLock = path == "aligned"
				p.Cells = append(p.Cells, Cell{s.Cell, s})
			}
		}
	}
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(6) {
			for pos, run := range rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(5) {
				cell, arm := run/2, []string{"A", "AMIN"}[run%2]
				if run == 4 {
					cell, arm = 0, "B"
				}
				s := p.Cells[i*2+cell].Spec
				s.Triplet, s.Seed, s.Arm = k, pairedSeed(master, entries[i], k), arm
				s.OrderPosition, s.RunlistPosition = pos+1, len(p.Trials)+1
				s.TrialID = fmt.Sprintf("%s_t%02d_%s", s.Cell, k, s.Arm)
				p.Trials = append(p.Trials, s)
			}
		}
	}
	return p
}
