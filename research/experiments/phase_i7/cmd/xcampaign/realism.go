package main

import (
	"fmt"
	"math/rand"
)

func makeRealismPlan(master uint64) Plan {
	p := Plan{CampaignID: "X-realism-v1", MasterSeed: master, SettleMs: settledPauseMs,
		SeedEncoding: "SHA256(decimal master|D_r{04,16}|decimal triplet), first 8 bytes big-endian int64"}
	for _, w := range []int{1, 2, 4} {
		for _, r := range []int{4, 16} {
			s := sourceCell(cells(), fmt.Sprintf("X1_r%02d_d0000", r))
			s.Cell, s.WriterConcurrency, s.ReadPath = fmt.Sprintf("D1_r%02d_w%d_direct", r, w), w, "direct"
			p.Cells = append(p.Cells, Cell{s.Cell, s})
		}
	}
	for _, r := range []int{4, 16} {
		s := sourceCell(cells(), fmt.Sprintf("X1_r%02d_d0000", r))
		s.Cell = fmt.Sprintf("D2_r%02d_w1_buffered", r)
		p.Cells = append(p.Cells, Cell{s.Cell, s})
	}
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(8) {
			for pos, arm := range permutations[(k+i)%6] {
				s := p.Cells[i].Spec
				s.Triplet, s.Seed, s.Arm = k, pairedSeed(master, fmt.Sprintf("D_r%02d", s.ReaderConcurrency), k), arm
				s.OrderPosition, s.RunlistPosition = pos+1, len(p.Trials)+1
				s.TrialID = fmt.Sprintf("%s_t%02d_%s", s.Cell, k, arm)
				p.Trials = append(p.Trials, s)
			}
		}
	}
	return p
}
