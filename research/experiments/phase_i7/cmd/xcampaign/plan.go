// Command xcampaign executes the fixed, paired X read-cost design sequentially.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"

	"owndbms/research/experiments/phase_i7/harness2"
)

const campaignID = "X-readcost-v1"

var permutations = [6][3]string{
	{"A", "AMIN", "B"}, {"A", "B", "AMIN"}, {"AMIN", "A", "B"},
	{"AMIN", "B", "A"}, {"B", "A", "AMIN"}, {"B", "AMIN", "A"},
}

type Cell struct {
	ID   string        `json:"id"`
	Spec harness2.Spec `json:"spec"`
}

type Plan struct {
	CampaignID   string          `json:"campaign_id"`
	MasterSeed   uint64          `json:"master_seed"`
	SeedEncoding string          `json:"seed_encoding"`
	Cells        []Cell          `json:"cells"`
	Trials       []harness2.Spec `json:"trials"`
}

func cells() []Cell {
	var out []Cell
	add := func(block, id string, readers, delay int, path string, mem int64, writers, events int) {
		s := harness2.Spec{
			Schema: "i7.trial-spec.v1", Block: block, Cell: id, WorkloadID: "X-readcost",
			ReaderConcurrency: readers, WriterConcurrency: writers,
			KeyspaceCardinality: 10000, InitialSSTableCount: 4, InitialKeysPerSSTable: 2500,
			ExpectedReaderSetSize: 4, TargetReadHitRatio: .95, ReadDistribution: "uniform",
			WarmupDurationMs: 2000, MeasurementDurationMs: 10000, CooldownDurationMs: 200,
			WalSync: true, InstrumentSync: true, ReadPath: path, ReadIODelayUs: delay,
			MemtableThresholdBytes: mem, PublicationEvents: events,
		}
		if events > 0 {
			s.PublicationMode = "physical"
		}
		out = append(out, Cell{id, s})
	}
	for _, r := range []int{4, 16} {
		for _, d := range []int{0, 20, 200, 2000} {
			add("X1", fmt.Sprintf("X1_r%02d_d%04d", r, d), r, d, "block", 1<<20, 1, 0)
		}
		add("X1", fmt.Sprintf("X1_r%02d_legacy", r), r, 0, "legacy_scan", 1<<20, 1, 0)
	}
	for _, d := range []int{0, 200, 2000} {
		add("X2", fmt.Sprintf("X2_r16_d%04d", d), 16, d, "block", 64<<20, 0, 20)
	}
	add("X2", "X2_r16_legacy", 16, 0, "legacy_scan", 64<<20, 0, 20)
	for _, r := range []int{1, 4, 16} {
		add("X3", fmt.Sprintf("X3_r%02d", r), r, 0, "block", 16<<10, 1, 0)
	}
	return out
}

// Seeds hash unambiguous UTF-8 decimal fields separated by '|'. The first eight
// bytes are big-endian, then reinterpreted as int64 for the harness PRNG contract.
func pairedSeed(master uint64, cell string, triplet int) int64 {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d", master, cell, triplet)))
	return int64(binary.BigEndian.Uint64(h[:8]))
}

func makePlan(master uint64) Plan {
	p := Plan{CampaignID: campaignID, MasterSeed: master, SeedEncoding: "SHA256(decimal master|cell_id|decimal triplet), first 8 bytes big-endian int64", Cells: cells()}
	appendTriplet := func(s harness2.Spec, cell string, k, index int) {
		seedTriplet := k
		if cell == "ANCHOR" {
			// Hold the request stream fixed across milestones so drift is not
			// confounded with a different stream; identity and order still use k.
			seedTriplet = 0
		}
		for pos, arm := range permutations[(k+index)%6] {
			trial := s
			trial.Cell = cell
			trial.Triplet = k
			trial.Seed = pairedSeed(master, cell, seedTriplet)
			trial.Arm = arm
			trial.OrderPosition = pos + 1
			trial.RunlistPosition = len(p.Trials) + 1
			trial.TrialID = fmt.Sprintf("%s_t%02d_%s", cell, k, arm)
			p.Trials = append(p.Trials, trial)
		}
	}
	appendTriplet(p.Cells[0].Spec, "ANCHOR", 0, 0)
	for k := 1; k <= 10; k++ {
		order := rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(len(p.Cells))
		for _, i := range order {
			appendTriplet(p.Cells[i].Spec, p.Cells[i].ID, k, i)
		}
		if k == 5 || k == 10 {
			appendTriplet(p.Cells[0].Spec, "ANCHOR", k, 0)
		}
	}
	return p
}

func jsonBytes(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
func digest(b []byte) string          { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func makeInterventionPlan(master uint64) Plan {
	p := Plan{CampaignID: "X-intervention-v1", MasterSeed: master,
		SeedEncoding: "SHA256(decimal master|cell_id|decimal triplet), first 8 bytes big-endian int64"}
	for _, c := range cells() {
		if c.Spec.Block != "X1" || c.Spec.ReadPath != "block" {
			continue
		}
		c.ID = fmt.Sprintf("I1_r%02d_d%04d", c.Spec.ReaderConcurrency, c.Spec.ReadIODelayUs)
		c.Spec.Cell = c.ID
		p.Cells = append(p.Cells, c)
	}
	for k := 1; k <= 5; k++ {
		order := rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(len(p.Cells))
		for _, i := range order {
			c := p.Cells[i]
			runs := rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(4)
			for pos, run := range runs {
				condition := []string{"second", "inline"}[run/2]
				s := c.Spec
				s.InlineFlushCheck = condition == "inline"
				s.Cell = c.ID + "_" + condition
				s.Triplet = k
				s.Seed = pairedSeed(master, c.ID, k)
				s.Arm = []string{"A", "AMIN"}[run%2]
				s.OrderPosition = pos + 1
				s.RunlistPosition = len(p.Trials) + 1
				s.TrialID = fmt.Sprintf("%s_t%02d_%s", s.Cell, k, s.Arm)
				p.Trials = append(p.Trials, s)
			}
		}
	}
	return p
}
