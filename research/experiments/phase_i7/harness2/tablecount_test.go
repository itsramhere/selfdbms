package harness2

import (
	"fmt"
	"testing"
)

// Short synthetic unit windows exercise fixture construction and the zero-writer
// path. These are not campaign trials and do not estimate campaign outcomes.
func TestTablecountReadOnlyFixture(t *testing.T) {
	for _, n := range []int{4, 16, 64, 256} {
		for _, arm := range []string{"A", "AMIN", "B"} {
			t.Run(fmt.Sprintf("n%d/%s", n, arm), func(t *testing.T) {
				s := miniSpec(t, "X1", "tablecount_unit", arm)
				s.WalSync, s.InstrumentSync = true, true
				s.ReadPath, s.ReadDistribution = "block", "uniform"
				s.WriterConcurrency, s.WarmupDurationMs, s.MeasurementDurationMs = 0, 0, 10
				s.KeyspaceCardinality, s.InitialSSTableCount = 10240, n
				s.InitialKeysPerSSTable, s.ExpectedReaderSetSize = 10240/n, n
				s.MemtableThresholdBytes = 1 << 20
				r, err := Run(s, testEnv(), "synthetic")
				if err != nil {
					t.Fatal(err)
				}
				if r.Status != "SUCCESS" || r.Operations.ReadOpsCompleted == 0 || r.Operations.WriteOpsCompleted != 0 ||
					r.FixtureManifest.ActualSSTableCount != n || r.FixtureManifest.CommittedKeys != 10240 ||
					r.LSMDiagnostics.InitialSSTables != n || r.LSMDiagnostics.FinalSSTables != n {
					t.Fatalf("fixture/read-only path: %+v", r)
				}
			})
		}
	}
}
