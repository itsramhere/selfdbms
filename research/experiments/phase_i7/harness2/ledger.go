package harness2

// Structured transaction accounting (Phase I7 engineering preflight, R2
// authorization item 3).
//
// Every request in a trial moves through a single lifecycle:
//
//	offered ─┬─> queued        (scheduled but never attempted: the window
//	         │                 closed, or the lateness bound hit, first)
//	         ├─> abandoned     (dequeued past its lateness bound; never
//	         │                 issued to the engine)
//	         └─> attempted ─┬─> completed_in_window
//	                        ├─> completed_late  (outstanding at window
//	                        │                   close, finished in drain)
//	                        └─> failed          (engine error, incl. a
//	                            refused Begin)
//
// The ledger is conserved: the two partitions close exactly, for every
// terminal outcome, in every driver mode:
//
//	(C1) attempted == completed_in_window + completed_late + failed
//	(C2) offered    == attempted + abandoned + queued
//	(C3) outstanding_at_window_close == attempted - completed_in_window - failed
//	                                 == completed_late
//
// C3 says: every request issued but not completed inside the window was
// outstanding at close, and after the drain it has completed late. This holds
// by construction of the workers (each attempt reaches exactly one terminal
// state before the trial returns). Failures are counted as terminal at close;
// a post-close failure is trial-fatal and surfaced via the error path, so it
// cannot silently distort the ledger.
//
// In closed-loop modes there is no exogenous schedule: the request stream
// offers work to itself, so offered == attempted and abandoned == queued == 0.
// "Queued" is the category the frozen schema calls "unissued" (scheduled
// arrivals the window closed before claiming); both names are kept — the
// ledger uses Queued, OperationCounts keeps ReadOpsUnissued for schema
// compatibility.

import "fmt"

// RequestAccounting is the conserved lifecycle ledger for one operation class.
type RequestAccounting struct {
	Offered            int64 `json:"offered"`
	Attempted          int64 `json:"attempted"`
	CompletedInWindow  int64 `json:"completed_in_window"`
	CompletedLate      int64 `json:"completed_late"`
	Failed             int64 `json:"failed"`
	Abandoned          int64 `json:"abandoned"`
	Queued             int64 `json:"queued"`
	OutstandingAtClose int64 `json:"outstanding_at_window_close"`
}

// TransactionLedger carries the per-class ledgers of one trial.
type TransactionLedger struct {
	Reads  RequestAccounting `json:"reads"`
	Writes RequestAccounting `json:"writes"`
}

// NewScheduledAccounting derives a ledger entry for a class with an exogenous
// schedule (open-loop reads, paced writes). Queued is the remainder of the
// offered set after attempts and abandonments.
func NewScheduledAccounting(offered, attempted, completedInWindow, completedLate, failed, abandoned int64) RequestAccounting {
	return RequestAccounting{
		Offered:            offered,
		Attempted:          attempted,
		CompletedInWindow:  completedInWindow,
		CompletedLate:      completedLate,
		Failed:             failed,
		Abandoned:          abandoned,
		Queued:             offered - attempted - abandoned,
		OutstandingAtClose: attempted - completedInWindow - failed,
	}
}

// NewClosedLoopAccounting derives a ledger entry for a class without an
// exogenous schedule (closed-loop readers, unpaced writers). The stream offers
// exactly what it attempts.
func NewClosedLoopAccounting(attempted, completedInWindow, completedLate, failed int64) RequestAccounting {
	return RequestAccounting{
		Offered:            attempted,
		Attempted:          attempted,
		CompletedInWindow:  completedInWindow,
		CompletedLate:      completedLate,
		Failed:             failed,
		Abandoned:          0,
		Queued:             0,
		OutstandingAtClose: attempted - completedInWindow - failed,
	}
}

// Validate checks the conservation identities. scheduledReads / scheduledWrites
// select which class carries an exogenous schedule.
func (l TransactionLedger) Validate(scheduledReads, scheduledWrites bool) error {
	if err := validateAccounting(l.Reads, scheduledReads, "reads"); err != nil {
		return err
	}
	return validateAccounting(l.Writes, scheduledWrites, "writes")
}

func validateAccounting(a RequestAccounting, scheduled bool, class string) error {
	if a.Attempted != a.CompletedInWindow+a.CompletedLate+a.Failed {
		return fmt.Errorf("%s terminal conservation violated: attempted %d != in-window %d + late %d + failed %d",
			class, a.Attempted, a.CompletedInWindow, a.CompletedLate, a.Failed)
	}
	if scheduled {
		if a.Offered != a.Attempted+a.Abandoned+a.Queued {
			return fmt.Errorf("%s arrival conservation violated: offered %d != attempted %d + abandoned %d + queued %d",
				class, a.Offered, a.Attempted, a.Abandoned, a.Queued)
		}
	} else {
		if a.Offered != a.Attempted {
			return fmt.Errorf("%s closed-loop offer violated: offered %d != attempted %d", class, a.Offered, a.Attempted)
		}
		if a.Abandoned != 0 || a.Queued != 0 {
			return fmt.Errorf("%s closed-loop class has abandoned %d / queued %d, want 0/0", class, a.Abandoned, a.Queued)
		}
	}
	if want := a.Attempted - a.CompletedInWindow - a.Failed; a.OutstandingAtClose != want {
		return fmt.Errorf("%s outstanding accounting violated: recorded %d, want attempted - in-window - failed = %d",
			class, a.OutstandingAtClose, want)
	}
	if a.OutstandingAtClose != a.CompletedLate {
		return fmt.Errorf("%s outstanding-at-close %d != late completions %d (drain must close every outstanding request)",
			class, a.OutstandingAtClose, a.CompletedLate)
	}
	if a.Attempted < 0 || a.Offered < 0 || a.CompletedInWindow < 0 || a.CompletedLate < 0 ||
		a.Failed < 0 || a.Abandoned < 0 || a.Queued < 0 {
		return fmt.Errorf("%s ledger has a negative count: %+v", class, a)
	}
	return nil
}
