package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFollowupPlan(t *testing.T) {
	const master = 20261108
	p := makeFollowupPlan(master)
	if p.CampaignID != "X-followup-v1" || p.SettleMs != 20000 || len(p.Cells) != 7 || len(p.Trials) != 150 {
		t.Fatal("dimensions/pause")
	}
	if !reflect.DeepEqual(p, makeFollowupPlan(master)) || reflect.DeepEqual(p, makeFollowupPlan(master+1)) {
		t.Fatal("determinism/seed sensitivity")
	}
	ids := []string{"F1_w0183_m16", "F1_w0366_m16", "F1_w0549_m16", "F1_w0732_m16", "F2_w0732_m01", "F3_w0732_m16_syncoff", "F4_r04_d0000"}
	for i, c := range p.Cells {
		want := sourceCell(makeSettledPlan(0).Cells, "S3_d0000_f80_closed")
		if i < 4 {
			want.PacedWriteRatePerSec = float64([]int{183, 366, 549, 732}[i])
		}
		if i < 4 || i == 5 {
			want.MemtableThresholdBytes = 16 << 20
		}
		if i == 5 {
			want.WalSync = false
		}
		if i == 6 {
			want = sourceCell(cells(), "X1_r04_d0000")
			want.PersistReadHistogram = true
		}
		want.Cell = ids[i]
		if c.ID != ids[i] || !reflect.DeepEqual(c.Spec, want) {
			t.Fatalf("cell %d: %+v", i, c)
		}
		if _, err := c.Spec.ResolveMode(); err != nil {
			t.Fatal(err)
		}
	}
	row := 0
	for k := 1; k <= 10; k++ {
		for _, i := range rand.New(rand.NewSource(master + int64(k))).Perm(7) {
			arms := []string{"AMIN", "B"}
			if (k+i)%2 != 0 {
				arms = []string{"B", "AMIN"}
			}
			if i == 6 {
				arms = permutations[(k+i)%6][:]
			}
			for pos, arm := range arms {
				want := p.Cells[i].Spec
				want.Triplet, want.Seed, want.Arm = k, pairedSeed(master, want.Cell, k), arm
				want.OrderPosition, want.RunlistPosition = pos+1, row+1
				want.TrialID = fmt.Sprintf("%s_t%02d_%s", want.Cell, k, arm)
				if !reflect.DeepEqual(p.Trials[row], want) {
					t.Fatalf("row %d", row)
				}
				row++
			}
		}
	}
}

func TestExistingDesignFrozenPlans(t *testing.T) {
	for _, c := range []struct {
		name, directory, sha string
		plan                 Plan
	}{
		{"readcost", "campaign", "68ad96f878c19bcb24c4c7f68e39e8f371420274c76c483f7dab54618c088136", makePlan(20261030)},
		{"intervention", "intervention", "2e44fcacb3e79dbddc079508cf663a5f794bd6c1f749567d115b2082adf83573", makeInterventionPlan(20261103)},
		{"compaction", "compaction", "5330cbddf01b0e55cbf1b13f1b92417e6f6521d315a0402f1c46136528b988a1", makeCompactionPlan(20261104)},
		{"tablecount", "tablecount", "0bc0042b8b8ad5ba517c911e9ec9a7dab00ec1b1f0c78876fc26be969eddc048", makeTablecountPlan(20261105)},
		{"paced", "paced", "384aace771c82b3e16eb0e1cb5363678cfff075a420ce8fcd70045ad82cb90e8", makePacedPlan(20261106)},
		{"settled", "settled", "7c868047870d72cf2d66100aade809f01549ee3a69f68357a5994b34a6c9c777", makeSettledPlan(20261107)},
		{"locks", "locks", "3323c32d053be80ca92b543509f474f1e2107e24e093b3e77c64fdd353c89159", makeLocksPlan(20261109)},
		{"followup", "followup", "394675b73698ef4afeed5af747b59970bc57cec03c66bc9c7642322ff2e04f51", makeFollowupPlan(20261108)},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := jsonBytes(c.plan)
			if err != nil {
				t.Fatal(err)
			}
			if got := digest(b); got != c.sha {
				t.Fatalf("frozen hash changed: %s", got)
			}
			// Local campaign artifacts are git-ignored; the frozen digest above
			// remains enforced on hosts without those artifacts.
			path := filepath.Join("..", "..", "X_READCOST", c.directory, "plan.json")
			old, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if digest(old) != digest(b) {
				t.Fatalf("differs from %s", path)
			}
		})
	}
}
