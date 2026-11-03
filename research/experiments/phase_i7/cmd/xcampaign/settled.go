package main

import (
	"fmt"
	"math/rand"

	"owndbms/research/experiments/phase_i7/harness2"
)

const settledPauseMs int64 = 20000

func sourceCell(source []Cell, id string) harness2.Spec {
	for _, c := range source {
		if c.ID == id {
			return c.Spec
		}
	}
	panic("missing settled source cell " + id)
}

func makeSettledPlan(master uint64) Plan {
	p := Plan{CampaignID: "X-settled-v1", MasterSeed: master, SettleMs: settledPauseMs,
		SeedEncoding: "SHA256(decimal master|entry_id|decimal triplet), first 8 bytes big-endian int64"}
	add := func(id string, spec harness2.Spec) {
		spec.Cell = id
		p.Cells = append(p.Cells, Cell{id, spec})
	}
	paced := makePacedPlan(0).Cells
	for _, d := range []int{0, 200, 2000} {
		for _, reads := range []string{"closed", "open"} {
			s := sourceCell(paced, fmt.Sprintf("P1_d%04d_f20_%s", d, reads))
			s.DiagTrace = true
			add(fmt.Sprintf("S1_d%04d_f20_%s", d, reads), s)
		}
	}
	for _, reads := range []string{"closed", "open"} {
		s := sourceCell(paced, "P1_d0200_f20_"+reads)
		s.PacedWriteRatePerSec, s.DiagTrace = 700, true
		add("S2_d0200_w700_"+reads, s)
	}
	s := sourceCell(paced, "P1_d0000_f80_closed")
	s.DiagTrace = true
	add("S3_d0000_f80_closed", s)
	for _, id := range []string{"X1_r16_d0020", "X1_r16_d2000", "X2_r16_d0200", "X3_r16"} {
		add("R_"+id, sourceCell(cells(), id))
	}
	add("R_C1_r16_d0200_on", sourceCell(makeCompactionPlan(0).Cells, "C1_r16_d0200_on"))
	add("R_T1_n256_r16_w0", sourceCell(makeTablecountPlan(0).Cells, "T1_n256_r16_w0"))
	for _, condition := range []string{"second", "inline"} {
		s := sourceCell(makeInterventionPlan(0).Cells, "I1_r16_d0200")
		s.InlineFlushCheck = condition == "inline"
		add("R_I1_r16_d0200_"+condition, s)
	}
	for k := 1; k <= 10; k++ {
		n := 15
		if k <= 5 {
			n = 16
		}
		for _, i := range rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(n) {
			entry := p.Cells[i].ID
			appendRun := func(spec harness2.Spec, arm string, pos int) {
				spec.Triplet, spec.Seed, spec.Arm = k, pairedSeed(master, entry, k), arm
				spec.OrderPosition, spec.RunlistPosition = pos+1, len(p.Trials)+1
				spec.TrialID = fmt.Sprintf("%s_t%02d_%s", spec.Cell, k, arm)
				p.Trials = append(p.Trials, spec)
			}
			if i == 15 {
				entry = "R_I1_r16_d0200"
				order := rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(4)
				for pos, run := range order {
					appendRun(p.Cells[15+run/2].Spec, []string{"A", "AMIN"}[run%2], pos)
				}
			} else {
				for pos, arm := range permutations[(k+i)%6] {
					appendRun(p.Cells[i].Spec, arm, pos)
				}
			}
		}
	}
	return p
}
