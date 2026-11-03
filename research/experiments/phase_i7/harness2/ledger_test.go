package harness2

// Deterministic tests for the conserved transaction ledger and the
// synchronization-instrumentation collector (Phase I7 engineering preflight).

import (
	"strings"
	"testing"

	"owndbms/lsmengine"
	runlistpkg "owndbms/research/experiments/phase_i7/runlist"
)

func validScheduledLedger() RequestAccounting {
	// offered 100 = attempted 90 + abandoned 5 + queued 5
	// attempted 90 = in-window 70 + late 12 + failed 8
	// outstanding 20 = attempted 90 - in-window 70 - failed 8 == late 12? No:
	// outstanding must equal late for conservation, so use consistent numbers:
	// in-window 70, late 20, failed 0 -> attempted 90, outstanding 20.
	return NewScheduledAccounting(100, 90, 70, 20, 0, 5)
}

func TestLedgerValidateAcceptsConsistentScheduled(t *testing.T) {
	a := validScheduledLedger()
	if a.Queued != 5 || a.OutstandingAtClose != 20 {
		t.Fatalf("derived fields wrong: queued=%d outstanding=%d", a.Queued, a.OutstandingAtClose)
	}
	l := TransactionLedger{Reads: a, Writes: NewClosedLoopAccounting(0, 0, 0, 0)}
	if err := l.Validate(true, false); err != nil {
		t.Fatalf("consistent scheduled ledger rejected: %v", err)
	}
}

func TestLedgerValidateAcceptsConsistentClosedLoop(t *testing.T) {
	l := TransactionLedger{
		Reads:  NewClosedLoopAccounting(500, 480, 19, 1),
		Writes: NewClosedLoopAccounting(60, 55, 5, 0),
	}
	if err := l.Validate(false, false); err != nil {
		t.Fatalf("consistent closed-loop ledger rejected: %v", err)
	}
}

// TestLedgerValidateRejectsViolations is the error-path suite: each mutation
// breaks exactly one conservation identity and must be rejected.
func TestLedgerValidateRejectsViolations(t *testing.T) {
	mk := func() (TransactionLedger, bool, bool) {
		return TransactionLedger{
			Reads:  validScheduledLedger(),
			Writes: NewClosedLoopAccounting(10, 9, 1, 0),
		}, true, true
	}
	cases := map[string]func(l *TransactionLedger){
		"terminal conservation":     func(l *TransactionLedger) { l.Reads.CompletedLate++ },
		"arrival conservation":      func(l *TransactionLedger) { l.Reads.Offered++ },
		"closed-loop offer":         func(l *TransactionLedger) { l.Writes.Offered++ },
		"closed-loop nonzero queue": func(l *TransactionLedger) { l.Writes.Queued = 1 },
		"outstanding mismatch":      func(l *TransactionLedger) { l.Reads.OutstandingAtClose-- },
		"outstanding vs late": func(l *TransactionLedger) {
			l.Writes.OutstandingAtClose = l.Writes.CompletedLate + 1
			l.Writes.Failed--
			l.Writes.Attempted--
		},
		"negative count": func(l *TransactionLedger) { l.Reads.Failed = -1 },
	}
	for name, mutate := range cases {
		l, sr, sw := mk()
		mutate(&l)
		if err := l.Validate(sr, sw); err == nil {
			t.Fatalf("%s: expected Validate to reject", name)
		} else if !strings.Contains(err.Error(), "violated") && !strings.Contains(err.Error(), "want 0/0") && !strings.Contains(err.Error(), "negative") && !strings.Contains(err.Error(), "drain") {
			t.Fatalf("%s: unexpected error text %q", name, err.Error())
		}
	}
}

// TestRunLedgerConservedAllModes executes mini trials in all four driver
// modes and asserts the emitted ledger is conserved and agrees with the
// legacy OperationCounts.
func TestRunLedgerConservedAllModes(t *testing.T) {
	cases := []struct {
		name   string
		spec   Spec
		schedR bool
		schedW bool
	}{
		{"closed_loop", func() Spec {
			s := miniSpec(t, "E1", "r01", "A")
			s.WriterConcurrency = 1
			return s
		}(), false, false},
		{"open_loop", func() Spec {
			s := miniSpec(t, "E4", "ol", "A")
			s.OpenLoopRatePerSec = 500
			s.MaxLatenessMs = 20
			return s
		}(), true, false},
		{"paced_writes", func() Spec {
			s := miniSpec(t, "E3", "equal_r01", "A")
			s.PacedWriteRatePerSec = 200
			s.WriterConcurrency = 1
			return s
		}(), false, true},
		{"publication_only", func() Spec {
			s := miniSpec(t, "E2", "pub", "A")
			s.PublicationMode = string(PubNone)
			s.WriterConcurrency = 1
			return s
		}(), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(tc.spec, testEnv(), "test")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.Status != "SUCCESS" {
				t.Fatalf("trial failed: %s", res.Error)
			}
			if err := res.Ledger.Validate(tc.schedR, tc.schedW); err != nil {
				t.Fatalf("ledger not conserved: %v", err)
			}
			// Cross-check against the legacy counters.
			if res.Ledger.Reads.Offered != res.Operations.ReadOpsScheduled {
				t.Fatalf("ledger offered %d != scheduled %d", res.Ledger.Reads.Offered, res.Operations.ReadOpsScheduled)
			}
			if res.Ledger.Reads.CompletedInWindow+res.Ledger.Reads.CompletedLate != res.Operations.ReadOpsCompleted {
				t.Fatalf("ledger completions != ReadOpsCompleted")
			}
			if res.Ledger.Writes.CompletedInWindow+res.Ledger.Writes.CompletedLate != res.Operations.WriteOpsCompleted {
				t.Fatalf("ledger write completions != WriteOpsCompleted")
			}
			if res.Ledger.Reads.Failed+res.Ledger.Writes.Failed != res.Operations.ErrorOpsCount {
				t.Fatalf("ledger failures %d != ErrorOpsCount %d",
					res.Ledger.Reads.Failed+res.Ledger.Writes.Failed, res.Operations.ErrorOpsCount)
			}
		})
	}
}

// TestRunSyncInstrumentationAttachAndAggregate runs one instrumented trial and
// asserts the collector observed reads, a commit wait, and fixture/publication
// work, with mode and site names intact.
func TestRunSyncInstrumentationAttachAndAggregate(t *testing.T) {
	spec := miniSpec(t, "E1", "r01", "AMIN")
	spec.WriterConcurrency = 1
	spec.InstrumentSync = true
	res, err := Run(spec, testEnv(), "test")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("trial failed: %s", res.Error)
	}
	if res.Sync == nil {
		t.Fatal("instrumented trial produced no sync summary")
	}
	readMode := lsmengine.SyncModeApproachAMin.String()
	cs, ok := res.Sync.ReadCriticalSectionNanos[readMode]
	if !ok || cs.Count == 0 {
		t.Fatalf("no read critical sections recorded for %s: %+v", readMode, res.Sync.ReadCriticalSectionNanos)
	}
	if cs.MaxNanos < cs.MinNanos {
		t.Fatalf("read CS stats inconsistent: min %d > max %d", cs.MinNanos, cs.MaxNanos)
	}
	w, ok := res.Sync.WriterLockWaitNanos[lsmengine.LockSiteCommit.String()]
	if !ok || w.Count == 0 {
		t.Fatalf("no commit waits recorded: %+v", res.Sync.WriterLockWaitNanos)
	}
	// Fixture construction flushes at least one publication; it is inside the
	// collector's trial-lifecycle scope by design.
	p, ok := res.Sync.PublicationNanos[lsmengine.LockSiteFlushPublish.String()]
	if !ok || p.Count == 0 {
		t.Fatalf("no publications recorded: %+v", res.Sync.PublicationNanos)
	}
}

// TestRunLedgerConservationUnderOpenLoopOverload forces the cancellation
// paths of the open-loop driver: an offered rate far above service capacity
// with a tight lateness bound W_max produces abandoned claims (cancellation)
// and queued arrivals (window closes before claiming). The conservation
// identities must hold for whatever outcome mix materializes.
func TestRunLedgerConservationUnderOpenLoopOverload(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive open-loop scenario; -short skips real-time-window tests (see acceptance race evidence)")
	}
	spec := miniSpec(t, "E4", "ol_overload", "A")
	spec.ReadPath = "legacy_scan" // Exercise the full scans assumed by this overload fixture.
	// Platform-independent cancellation: 1 us arrival period against
	// full-miss reads (cost >> period) guarantees the reader falls behind
	// and cancels arrivals past the lateness bound on every OS.
	spec.OpenLoopRatePerSec = 1_000_000
	spec.TargetReadHitRatio = 0.0
	spec.MaxLatenessMs = 2
	res, err := Run(spec, testEnv(), "test")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("trial failed: %s", res.Error)
	}
	l := res.Ledger.Reads
	if err := res.Ledger.Validate(true, false); err != nil {
		t.Fatalf("conservation violated under cancellation: %v", err)
	}
	if l.Offered != l.Attempted+l.Abandoned+l.Queued {
		t.Fatalf("arrival partition broken: %+v", l)
	}
	if l.Abandoned == 0 {
		t.Fatalf("overload scenario produced no cancellations; the abandonment path was not exercised: %+v", l)
	}
	// The offered set is exactly the schedule.
	if l.Offered != res.Operations.ReadOpsScheduled {
		t.Fatalf("offered %d != scheduled %d", l.Offered, res.Operations.ReadOpsScheduled)
	}
}

// TestRunLedgerConservationUnderLateCompletion forces completions after the
// measured window: a short window with continuous closed-loop readers leaves
// requests outstanding at close, which must appear as completed_late and
// satisfy the outstanding identity exactly.
func TestRunLedgerConservationUnderLateCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("timing-sensitive late-completion scenario; -short skips real-time-window tests (see acceptance race evidence)")
	}
	spec := miniSpec(t, "E1", "r01_late", "A")
	spec.MeasurementDurationMs = 25
	spec.WriterConcurrency = 1
	res, err := Run(spec, testEnv(), "test")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("trial failed: %s", res.Error)
	}
	l := res.Ledger.Reads
	if err := res.Ledger.Validate(false, false); err != nil {
		t.Fatalf("conservation violated under late completion: %v", err)
	}
	if l.CompletedInWindow == 0 {
		t.Fatalf("scenario produced no in-window completions: %+v", l)
	}
	if l.OutstandingAtClose != l.CompletedLate {
		t.Fatalf("outstanding at close %d != late completions %d", l.OutstandingAtClose, l.CompletedLate)
	}
	// The throughput denominator counts only in-window completions.
	if res.Operations.ReadOpsCompletedInWindow != l.CompletedInWindow {
		t.Fatalf("OperationCounts in-window %d != ledger %d", res.Operations.ReadOpsCompletedInWindow, l.CompletedInWindow)
	}
}

// TestExecutionLedgerCountsInterruptedProcess ties process interruption into
// the ceiling: a driver that dies after launching trials leaves STARTED
// records without FINISHED; those executions consumed budget.
func TestExecutionLedgerCountsInterruptedProcess(t *testing.T) {
	var records []runlistpkg.ExecutionRecord
	var ids []string
	for i := 0; i < 5; i++ {
		id := string(rune('A'+i)) + "_trial"
		ids = append(ids, id)
		records = append(records, runlistpkg.ExecutionRecord{
			Event: "STARTED", TrialID: id, Kind: runlistpkg.KindConfirmatory,
		})
	}
	for i := 0; i < 3; i++ {
		records = append(records, runlistpkg.ExecutionRecord{
			Event: "FINISHED", TrialID: ids[i], Outcome: runlistpkg.OutcomeSuccess,
		})
	}
	rep := runlistpkg.ValidateExecutionLedger(records)
	if !rep.OK {
		t.Fatalf("interrupted ledger rejected: %v", rep.Errors)
	}
	if rep.Started != 5 || rep.Finished != 3 || rep.Unfinished != 2 {
		t.Fatalf("started=%d finished=%d unfinished=%d, want 5/3/2", rep.Started, rep.Finished, rep.Unfinished)
	}
	if rep.Remaining != runlistpkg.GlobalCeiling-5 {
		t.Fatalf("remaining %d, want %d", rep.Remaining, runlistpkg.GlobalCeiling-5)
	}
}

// TestSyncCollectorDropsUnknownSlots guards the enum mapping.
func TestSyncCollectorDropsUnknownSlots(t *testing.T) {
	c := NewSyncCollector()
	c.OnReadCriticalSection(lsmengine.SyncMode(99), 5)
	c.OnWriterLockWait(lsmengine.LockSite(99), 5, 1)
	c.OnPublication(lsmengine.LockSite(99), 5, 1)
	s := c.Summary()
	if len(s.ReadCriticalSectionNanos)+len(s.WriterLockWaitNanos)+len(s.PublicationNanos) != 0 {
		t.Fatalf("unknown slots were recorded: %+v", s)
	}
}
