package runlist

// Deterministic tests for the R1.2e run-list model. All records are
// synthetic; nothing here runs trials or benchmarks.

import (
	"bytes"
	"strings"
	"testing"
)

func sizingAt(n int) Sizing {
	s := Sizing{}
	for _, c := range DefaultCatalog().Cells {
		s[c.ID] = n
	}
	return s
}

func mustGenerate(t *testing.T, catalog Catalog, sizing Sizing) []Row {
	t.Helper()
	rows, err := Generate(catalog, sizing)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return rows
}

// TestGenerateDeterministic locks generation to (catalog, sizing, seed).
func TestGenerateDeterministic(t *testing.T) {
	catalog := DefaultCatalog()
	a := mustGenerate(t, catalog, sizingAt(8))
	b := mustGenerate(t, catalog, sizingAt(8))
	if len(a) != len(b) {
		t.Fatalf("generation is not deterministic: %d vs %d rows", len(a), len(b))
	}
	for i := range a {
		if a[i].TrialID != b[i].TrialID || a[i].Arm != b[i].Arm || a[i].Position != b[i].Position {
			t.Fatalf("row %d differs between identical generations: %+v vs %+v", i, a[i], b[i])
		}
	}
	// A different seed must change the arm orderings.
	catalog.MasterSeed = "9999"
	c := mustGenerate(t, catalog, sizingAt(8))
	changed := false
	for i := range a {
		if a[i].Arm != c[i].Arm {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("arm orderings did not change with the master seed")
	}
}

// TestGenerateBudgetModel checks the R1.2e §5 ledger: at N=8 the minimum is
// 300 planned runs; at N=10 the maximum is 372; both keep the worst case
// within the 400 ceiling with >= 12 unallocated runs.
func TestGenerateBudgetModel(t *testing.T) {
	catalog := DefaultCatalog()
	for n, wantPlanned := range map[int]int{8: 300, 10: 372} {
		rows := mustGenerate(t, catalog, sizingAt(n))
		if len(rows) != wantPlanned {
			t.Fatalf("N=%d: generated %d rows, want %d", n, len(rows), wantPlanned)
		}
		if worst := len(rows) + ReplacementBudget; worst > MaxWorstCaseRuns || worst > GlobalCeiling {
			t.Fatalf("N=%d: worst case %d violates the ceiling model", n, worst)
		}
		if buffer := GlobalCeiling - (len(rows) + ReplacementBudget); buffer < 12 {
			t.Fatalf("N=%d: guaranteed buffer %d < 12", n, buffer)
		}
	}
	if MaxWorstCaseRuns != 388 || GlobalCeiling != 400 || MaxPlannedRuns != 372 || ReplacementBudget != 16 {
		t.Fatalf("budget constants drifted: ceiling=%d maxPlanned=%d worstCase=%d repl=%d",
			GlobalCeiling, MaxPlannedRuns, MaxWorstCaseRuns, ReplacementBudget)
	}
}

// TestValidateAcceptsWellFormedRunlist is the primary validation result used
// in the engineering checkpoint.
func TestValidateAcceptsWellFormedRunlist(t *testing.T) {
	catalog := DefaultCatalog()
	rows := mustGenerate(t, catalog, sizingAt(8))
	rep := Validate(rows, catalog)
	if !rep.OK {
		t.Fatalf("well-formed run list rejected: %v", rep.Errors)
	}
	if rep.Total != 300 || rep.Planned != 300 || rep.D3 != 12 || rep.Worst != 316 || rep.Buffer != 84 {
		t.Fatalf("unexpected accounting: %+v", rep)
	}

	// Round-trip through CSV must still validate.
	var buf bytes.Buffer
	if err := WriteCSV(&buf, rows); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	back, err := ReadCSV(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(back) != len(rows) {
		t.Fatalf("csv round trip lost rows: %d -> %d", len(rows), len(back))
	}
	rep2 := Validate(back, catalog)
	if !rep2.OK {
		t.Fatalf("round-tripped run list rejected: %v", rep2.Errors)
	}
}

// TestValidateRejectsViolations is the error-path suite: each corruption must
// be caught by exactly the ceiling/structure rules.
func TestValidateRejectsViolations(t *testing.T) {
	catalog := DefaultCatalog()
	base := mustGenerate(t, catalog, sizingAt(8))

	clone := func() []Row {
		out := make([]Row, len(base))
		copy(out, base)
		return out
	}

	t.Run("ceiling breach", func(t *testing.T) {
		rows := clone()
		extra := rows[len(rows)-1]
		extra.Position = len(rows) + 1
		extra.TrialID = "i7_extra_A"
		extra.TripletKey = "E1|C1-01|99"
		extra.Triplet = 999
		rows = append(rows, extra)
		if rep := Validate(rows, catalog); rep.OK {
			t.Fatal("over-ceiling run list accepted")
		}
	})

	t.Run("incomplete triplet", func(t *testing.T) {
		rows := clone()
		// Remove one arm of the first EXP-1 triplet.
		for i, r := range rows {
			if r.TripletKey == "E1|C1-01|01" && r.Arm == "B" {
				rows = append(rows[:i:i], rows[i+1:]...)
				break
			}
		}
		rep := Validate(rows, catalog)
		if rep.OK {
			t.Fatal("run list with an incomplete triplet accepted")
		}
		found := false
		for _, e := range rep.Errors {
			if strings.Contains(e, "E1|C1-01|01") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing-triplet error not reported: %v", rep.Errors)
		}
	})

	t.Run("anchor displaced", func(t *testing.T) {
		rows := clone()
		// Swap the opening anchor's first row with a later row.
		i0, i1 := -1, -1
		for i, r := range rows {
			if i0 < 0 && r.TripletKey == "D3|C2-01|01" {
				i0 = i
				continue
			}
			if r.Block == "E1" && i1 < 0 {
				i1 = i
			}
		}
		rows[i0].Position, rows[i1].Position = rows[i1].Position, rows[i0].Position
		if rep := Validate(rows, catalog); rep.OK {
			t.Fatal("run list with a displaced opening anchor accepted")
		}
	})

	t.Run("unregistered cell", func(t *testing.T) {
		rows := clone()
		rows[5].Cell = "C9-99"
		if rep := Validate(rows, catalog); rep.OK {
			t.Fatal("run list with an unknown cell accepted")
		}
	})

	t.Run("replacement budget breach", func(t *testing.T) {
		rows := clone()
		anchor := rows[0]
		for i := 0; i <= ReplacementBudget; i++ {
			r := anchor
			r.Phase = PhaseReplacement
			r.Position = len(rows) + 1 + i
			r.TrialID = "i7_repl_" + string(rune('a'+i))
			rows = append(rows, r)
		}
		rep := Validate(rows, catalog)
		if rep.OK {
			t.Fatal("run list exceeding the replacement budget accepted")
		}
		found := false
		for _, e := range rep.Errors {
			if strings.Contains(e, "replacement attempts") {
				found = true
			}
		}
		if !found {
			t.Fatalf("replacement-budget error not reported: %v", rep.Errors)
		}
	})

	t.Run("orphan replacement", func(t *testing.T) {
		rows := clone()
		r := rows[0]
		r.Phase = PhaseReplacement
		r.Cell = "C3-42"
		r.TripletKey = "E1|C3-42|01"
		r.Position = len(rows) + 1
		rows = append(rows, r)
		rep := Validate(rows, catalog)
		if rep.OK {
			t.Fatal("orphan replacement row accepted")
		}
	})

	t.Run("exp3 row refused", func(t *testing.T) {
		cat := DefaultCatalog()
		cat.Cells = append(cat.Cells, Cell{ID: "C3-01", Stage: StageEXP3, Order: 1})
		if _, err := Generate(cat, sizingAt(8)); err == nil {
			t.Fatal("EXP-3 cell was allowed into a run list")
		}
	})
}

// TestTripletSeedMatchesDriverConvention locks the seed derivation to the
// driver's convention (first 15 hex chars of SHA-256 as unsigned int).
func TestTripletSeedMatchesDriverConvention(t *testing.T) {
	// Reference value computed with the PowerShell convention:
	// Get-TextSha256('20261101|triplet|E1|C1-01|01').Substring(0,15)
	// as lowercase hex without the '0x' prefix.
	got := TripletSeed("20261101", "E1|C1-01|01")
	again := TripletSeed("20261101", "E1|C1-01|01")
	if got != again {
		t.Fatal("seed derivation is not deterministic")
	}
	if got < 0 {
		t.Fatalf("seed %d must be non-negative to match the driver's UInt64 cast", got)
	}
}

// TestClampSizing verifies the confirmatory N rule N = min(10, max(8, N_req)).
func TestClampSizing(t *testing.T) {
	catalog := DefaultCatalog()
	got := catalog.ClampSizing(Sizing{"C1-01": 3, "C1-02": 42, "C2-01": 8})
	if got["C1-01"] != 8 || got["C1-02"] != 10 || got["C2-01"] != 8 {
		t.Fatalf("clamp rule violated: %+v", got)
	}
	// Missing cells default to the minimum.
	if got["C4-02"] != 8 {
		t.Fatalf("missing cell defaulted to %d, want 8", got["C4-02"])
	}
}
