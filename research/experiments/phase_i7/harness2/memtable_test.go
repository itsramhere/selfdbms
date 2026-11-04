package harness2

import "testing"

func TestWriteKeyDomainValidation(t *testing.T) {
	s := Spec{Block: "X4", WriterConcurrency: 1, WalSync: true, PacedWriteRatePerSec: 183}
	for _, domain := range []string{"", "shadow"} {
		for _, open := range []float64{0, 500} {
			s.WriteKeyDomain, s.OpenLoopRatePerSec = domain, open
			if _, err := s.ResolveMode(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.WriteKeyDomain = "same" },
		func(s *Spec) { s.WriteKeyDomain = "other" },
		func(s *Spec) { s.Block = "X1" },
		func(s *Spec) { s.Block, s.Cell = "E3", "equal_work" },
		func(s *Spec) { s.PacedWriteRatePerSec = 0 },
		func(s *Spec) { s.WriterConcurrency = 0 },
	} {
		bad := s
		mutate(&bad)
		if _, err := bad.ResolveMode(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

// Unit fixtures only: a tiny keyspace and hit-ratio-one readers make the
// same-key control encounter committed keys quickly, without campaign trials.
func TestPacedShadowMemtableReads(t *testing.T) {
	for _, domain := range []string{"", "shadow"} {
		t.Run("domain="+domain, func(t *testing.T) {
			s := miniSpec(t, "X4", "unit_memtable", "B")
			s.WalSync, s.DiagTrace, s.InstrumentSync = false, true, true
			s.WriteKeyDomain, s.PacedWriteRatePerSec = domain, 200
			s.KeyspaceCardinality, s.InitialSSTableCount, s.InitialKeysPerSSTable = 4, 1, 4
			s.ExpectedReaderSetSize, s.TargetReadHitRatio = 1, 1
			s.WarmupDurationMs, s.MeasurementDurationMs = 0, 300
			r, err := Run(s, testEnv(), "unit-test")
			if err != nil || r.Status != "SUCCESS" {
				t.Fatalf("unit fixture: %v %+v", err, r)
			}
			if r.Operations.WriteOpsCompletedInWindow == 0 || r.Operations.ReadOpsCompleted == 0 || r.LSMDiagnostics.MeasuredFlushes != 0 {
				t.Fatalf("missing work or unexpected flush: %+v %+v", r.Operations, r.LSMDiagnostics)
			}
			if (r.LSMDiagnostics.MeasuredMemtableReads == 0) != (domain == "shadow") {
				t.Fatalf("domain %q: memtable reads=%d", domain, r.LSMDiagnostics.MeasuredMemtableReads)
			}
		})
	}
}
