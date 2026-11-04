package main

import (
	"fmt"
	"math/rand"
)

func makeMemtablePlan(master uint64) Plan {
	p := Plan{CampaignID: "X-memtable-v1", MasterSeed: master, SettleMs: settledPauseMs,
		SeedEncoding: "SHA256(decimal master|cell_id|decimal triplet), first 8 bytes big-endian int64"}
	base := sourceCell(makeFollowupPlan(0).Cells, "F1_w0183_m16")
	for _, rate := range []int{183, 366, 549, 732} {
		for _, domain := range []string{"same", "shadow"} {
			for _, sync := range []string{"on", "off"} {
				s := base
				s.Cell = fmt.Sprintf("M_w%04d_%s_%s", rate, domain, sync)
				s.PacedWriteRatePerSec, s.WalSync = float64(rate), sync == "on"
				if domain == "shadow" {
					s.WriteKeyDomain = "shadow"
				}
				p.Cells = append(p.Cells, Cell{s.Cell, s})
			}
		}
	}
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(len(p.Cells)) {
			s := p.Cells[i].Spec
			s.Triplet, s.Seed, s.Arm = k, pairedSeed(master, s.Cell, k), "B"
			s.OrderPosition, s.RunlistPosition = 1, len(p.Trials)+1
			s.TrialID = fmt.Sprintf("%s_t%02d_B", s.Cell, k)
			p.Trials = append(p.Trials, s)
		}
	}
	return p
}
