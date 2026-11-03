package harness2

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestX4ModesAndValidation(t *testing.T) {
	s := Spec{Block: "X4", WriterConcurrency: 1, WalSync: true, PacedWriteRatePerSec: 183}
	for _, open := range []float64{0, 12500} {
		s.OpenLoopRatePerSec = open
		want := ModePacedWrites
		if open > 0 {
			want = ModeOpenReadsPacedWrites
		}
		if got, err := s.ResolveMode(); err != nil || got != want || !s.IsX() {
			t.Fatalf("mode: %v %v", got, err)
		}
	}
	for _, mutate := range []func(*Spec){
		func(s *Spec) { s.WalSync = false },
		func(s *Spec) { s.WriterConcurrency = 0 },
		func(s *Spec) { s.WriterConcurrency = 2 },
		func(s *Spec) { s.PacedWriteRatePerSec = 0 },
		func(s *Spec) { s.PacedWriteRatePerSec = math.NaN() },
		func(s *Spec) { s.PacedWriteRatePerSec = math.Inf(1) },
		func(s *Spec) { s.OpenLoopRatePerSec = -1 },
		func(s *Spec) { s.OpenLoopRatePerSec = math.Inf(1) },
		func(s *Spec) { s.OpenLoopRatePerSec = math.NaN() },
	} {
		bad := s
		mutate(&bad)
		if _, err := bad.ResolveMode(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

// Older blocks retain their existing classification even when X4 rate fields
// are present: E3's equal_ prefix alone selects pacing, E4 alone selects open
// reads and still never selects the combined writer mode.
func TestX4PreservesEveryOlderBlockMode(t *testing.T) {
	for _, block := range []string{"X1", "X2", "X3", "E1", "E2", "E3", "E4", "E5", "D1", "D2", "D3", "PF", "SMOKE", "E7"} {
		for _, cell := range []string{"ordinary", "equal_work"} {
			s := Spec{Block: block, Cell: cell, WalSync: true, PublicationMode: "none", OpenLoopRatePerSec: 500, PacedWriteRatePerSec: 100}
			want := ModeClosedLoop
			switch block {
			case "E2", "X2":
				want = ModePublicationOnly
			case "E4":
				want = ModeOpenLoop
			case "E3":
				if cell == "equal_work" {
					want = ModePacedWrites
				}
			}
			got, err := s.ResolveMode()
			if block == "E7" {
				if err == nil {
					t.Fatal("E7 must reject")
				}
				continue
			}
			if err != nil || got != want {
				t.Fatalf("%s %s: %v %v", block, cell, got, err)
			}
		}
	}
	for _, block := range []string{"E3", "E4"} {
		s := Spec{Block: block, Cell: "equal_work"}
		if _, err := s.ResolveMode(); err == nil {
			t.Fatalf("%s missing rate accepted", block)
		}
	}
	b, err := json.Marshal(TrialResult{})
	if err != nil || strings.Contains(string(b), "paced_load") {
		t.Fatal("old result shape changed")
	}
	if NewHistogramShard().Paced != nil {
		t.Fatal("old X shard gained X4 observers")
	}
}

func TestPacedHistogramsAndScheduledShortfall(t *testing.T) {
	s := NewHistogramShard()
	s.Paced = &pacedHistograms{}
	for i := 0; i < 100; i++ {
		s.Paced.recordWrite(30, 10, 50, 100)
		s.Paced.recordWrite(90, 70, 110, 100)
		s.RecordService(12)
		s.Paced.readResponse.Record(45)
	}
	s.observeLateness(1500)
	s.observeLateness(1000)
	l := TransactionLedger{
		Reads:  NewScheduledAccounting(250, 200, 100, 100, 0, 20),
		Writes: NewScheduledAccounting(300, 200, 100, 100, 0, 0),
	}
	if err := l.Validate(true, true); err != nil {
		t.Fatal(err)
	}
	r := mergePaced([]*Shard{s}, l, true)
	if r.WriteTicksOffered != 300 || r.WriteTicksUnissued != 100 || r.ReadArrivalsAbandoned != 20 || r.MaxReadLatenessNs != 1500 {
		t.Fatalf("%+v", r)
	}
	if r.WriteService.P50Nanos != 20 || *r.WriteResponse.P99Nanos != 40 || r.WriteResponseLate.P50Nanos != 40 || r.WriteServiceLate.Count != 100 {
		t.Fatal("write timing mixed")
	}
	if r.ReadResponse.Count != 100 || *r.ReadResponse.P99Nanos != 45 || MergeHistograms([]*Shard{s}, false).Count != 100 {
		t.Fatal("read timing mixed")
	}
	if len(s.Resp) != 0 || len(s.Samples) != 0 {
		t.Fatal("X4 must have bounded histogram storage")
	}
	r = mergePaced([]*Shard{s}, l, false)
	if r.ReadResponse != nil || r.ReadResponseLate != nil {
		t.Fatal("closed reads have no response schedule")
	}
}

func TestX4AbandonmentAndWindowStopWithoutIssuing(t *testing.T) {
	sh := NewHistogramShard()
	sh.Paced = &pacedHistograms{}
	var claim int64
	stop := make(chan struct{})
	// Past-due arrivals must be abandoned without touching a manager. Their
	// lateness must still be visible, even if every arrival is abandoned.
	sched := []Arrival{{IntendedNs: -1_000_000_000}, {IntendedNs: -2_000_000_000}}
	openLoopWorker(nil, sh, sched, Now(), stop, &claim, 1, 10_000_000_000, Spec{Block: "X4"}, nil)
	if sh.Abandoned != 2 || sh.ReadsAttempted != 0 || sh.MaxLatenessNs < 2_000_000_000 {
		t.Fatalf("abandonment: %+v", sh)
	}
	// A worker that wakes after the window must not issue a request, even
	// before the stop channel has been closed by the window controller.
	claim = 0
	openLoopWorker(nil, sh, []Arrival{{IntendedNs: 0}}, Now(), stop, &claim, 10_000_000_000, -1, Spec{Block: "X4"}, nil)
	close(stop)
	pacedWriter(nil, sh, Now(), stop, 10_000_000_000, Spec{Block: "X4", PacedWriteRatePerSec: 100}, nil)
	if sh.ReadsAttempted != 0 || sh.WritesAttempted != 0 {
		t.Fatal("worker issued after window/stop")
	}
}
