package main

import (
	"fmt"
	"math/rand"

	"owndbms/research/experiments/phase_i7/harness2"
)

func makeFollowupPlan(master uint64) Plan {
	p := Plan{CampaignID: "X-followup-v1", MasterSeed: master, SettleMs: settledPauseMs,
		SeedEncoding: "SHA256(decimal master|cell_id|decimal triplet), first 8 bytes big-endian int64"}
	base := sourceCell(makeSettledPlan(0).Cells, "S3_d0000_f80_closed")
	add := func(id string, s harness2.Spec) {
		s.Cell = id
		p.Cells = append(p.Cells, Cell{id, s})
	}
	for _, rate := range []int{183, 366, 549, 732} {
		s := base
		s.PacedWriteRatePerSec, s.MemtableThresholdBytes = float64(rate), 16<<20
		add(fmt.Sprintf("F1_w%04d_m16", rate), s)
	}
	add("F2_w0732_m01", base)
	s := p.Cells[3].Spec
	s.WalSync = false
	add("F3_w0732_m16_syncoff", s)
	s = sourceCell(cells(), "X1_r04_d0000")
	s.PersistReadHistogram = true
	add("F4_r04_d0000", s)
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(len(p.Cells)) {
			arms := []string{"AMIN", "B"}
			if (k+i)%2 != 0 {
				arms = []string{"B", "AMIN"}
			}
			if i == 6 {
				arms = permutations[(k+i)%6][:]
			}
			for pos, arm := range arms {
				s := p.Cells[i].Spec
				s.Triplet, s.Seed, s.Arm = k, pairedSeed(master, s.Cell, k), arm
				s.OrderPosition, s.RunlistPosition = pos+1, len(p.Trials)+1
				s.TrialID = fmt.Sprintf("%s_t%02d_%s", s.Cell, k, arm)
				p.Trials = append(p.Trials, s)
			}
		}
	}
	return p
}
