package main

import (
	"fmt"
	"math/rand"
)

var lockPolicies = []string{"go", "phasefair", "readerpref"}

func makeLocksPlan(master uint64) Plan {
	p := Plan{CampaignID: "X-locks-v1", MasterSeed: master, SettleMs: settledPauseMs,
		SeedEncoding: "SHA256(decimal master|entry_id|decimal triplet), first 8 bytes big-endian int64"}
	var entries []string
	for _, r := range []int{4, 16} {
		for _, d := range []int{0, 20, 200, 2000} {
			entry := fmt.Sprintf("L1_r%02d_d%04d", r, d)
			entries = append(entries, entry)
			for _, policy := range lockPolicies {
				s := sourceCell(cells(), fmt.Sprintf("X1_r%02d_d%04d", r, d))
				s.Cell = entry + "_" + policy
				if policy != "go" { // Default sync.RWMutex is absent from the JSON.
					s.LockPolicy = policy
				}
				p.Cells = append(p.Cells, Cell{s.Cell, s})
			}
		}
	}
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(len(entries)) {
			for pos, run := range rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(6) {
				s := p.Cells[i*3+run/2].Spec
				s.Triplet, s.Seed, s.Arm = k, pairedSeed(master, entries[i], k), []string{"A", "AMIN"}[run%2]
				s.OrderPosition, s.RunlistPosition = pos+1, len(p.Trials)+1
				s.TrialID = fmt.Sprintf("%s_t%02d_%s", s.Cell, k, s.Arm)
				p.Trials = append(p.Trials, s)
			}
		}
	}
	return p
}
