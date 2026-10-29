package runlist

// Run-list validation: structural integrity, the strict global execution
// ceiling, drift-anchor milestone placement, and replacement-attempt budget
// accounting. All inputs are synthetic records; nothing here executes trials.

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Findings is a validation report.
type Findings struct {
	OK      bool     `json:"ok"`
	Total   int      `json:"total_rows"`
	Planned int      `json:"planned_runs"`
	D3      int      `json:"drift_anchor_runs"`
	Repl    int      `json:"replacement_runs"`
	Worst   int      `json:"worst_case_total"`
	Buffer  int      `json:"guaranteed_buffer"`
	Errors  []string `json:"errors,omitempty"`
}

func (f *Findings) errf(format string, args ...any) {
	f.Errors = append(f.Errors, fmt.Sprintf(format, args...))
	f.OK = false
}

// Validate checks rows against the pre-registered R1.2e campaign model and
// the strict global execution ceiling. The expected cell inventory defaults
// to DefaultCatalog; pass a different catalog for what-if checks.
func Validate(rows []Row, catalog Catalog) *Findings {
	f := &Findings{OK: true}
	if len(rows) == 0 {
		f.errf("run list is empty")
		f.Total = 0
		return f
	}
	f.Total = len(rows)

	// --- ceiling accounting ---------------------------------------------
	planned, anchors, repl := 0, 0, 0
	for _, r := range rows {
		switch r.Phase {
		case PhaseConfirmatory:
			planned++
		case PhaseDriftAnchor:
			anchors++
		case PhaseReplacement:
			repl++
		default:
			f.errf("row %d (%s): unknown phase %q", r.Position, r.TrialID, r.Phase)
		}
	}
	f.Planned = planned + anchors
	f.D3 = anchors
	f.Repl = repl
	// The worst case reserves the entire Rule S-1 budget whether or not
	// replacement rows are present yet (R1.2e §5 reconciliation).
	f.Worst = f.Planned + ReplacementBudget
	f.Buffer = GlobalCeiling - f.Worst

	if f.Total > GlobalCeiling {
		f.errf("ceiling violated: %d total rows exceed the strict global execution ceiling of %d", f.Total, GlobalCeiling)
	}
	if f.Planned > MaxPlannedRuns {
		f.errf("planned model size %d exceeds the pre-registered maximum %d", f.Planned, MaxPlannedRuns)
	}
	if f.Worst > MaxWorstCaseRuns {
		f.errf("worst-case executions %d exceed the pre-registered worst-case bound %d (buffer must stay >= %d)",
			f.Worst, MaxWorstCaseRuns, GlobalCeiling-MaxWorstCaseRuns)
	}
	if repl > ReplacementBudget {
		f.errf("replacement attempts %d exceed the Rule S-1 budget %d", repl, ReplacementBudget)
	}

	// --- positions -------------------------------------------------------
	positions := make([]int, 0, len(rows))
	for _, r := range rows {
		positions = append(positions, r.Position)
	}
	sort.Ints(positions)
	for i, p := range positions {
		if p != i+1 {
			f.errf("runlist_position sequence broken: expected %d at rank %d, found %d", i+1, i, p)
			break
		}
	}

	// --- triplet integrity (planned rows only) ---------------------------
	type tinfo struct {
		rows   []Row
		phases map[string]int
	}
	triplets := map[string]*tinfo{}
	for _, r := range rows {
		if r.Phase == PhaseReplacement {
			continue
		}
		t, ok := triplets[r.TripletKey]
		if !ok {
			t = &tinfo{phases: map[string]int{}}
			triplets[r.TripletKey] = t
		}
		t.rows = append(t.rows, r)
		t.phases[r.Phase]++
	}
	for key, t := range triplets {
		if len(t.rows) != len(Arms) {
			f.errf("triplet %s has %d rows, want %d (one per arm)", key, len(t.rows), len(Arms))
			continue
		}
		seen := map[string]bool{}
		orders := map[int]string{}
		cell := ""
		for _, r := range t.rows {
			if seen[r.Arm] {
				f.errf("triplet %s: arm %s appears twice", key, r.Arm)
			}
			seen[r.Arm] = true
			if _, ok := armsSet[r.Arm]; !ok {
				f.errf("triplet %s: unknown arm %q", key, r.Arm)
			}
			orders[r.OrderInTriplet] = r.Arm
			if cell == "" {
				cell = r.Cell
			} else if cell != r.Cell {
				f.errf("triplet %s: mixed cells %s / %s", key, cell, r.Cell)
			}
			if len(t.phases) != 1 {
				f.errf("triplet %s: mixed phases within triplet", key)
			}
		}
		if len(orders) == len(Arms) {
			var perm [3]string
			for o := 1; o <= 3; o++ {
				perm[o-1] = orders[o]
			}
			if !isBalancedPerm(perm) {
				f.errf("triplet %s: arm order %v is not one of the balanced Latin-square permutations", key, perm)
			}
		}
	}

	// --- drift anchors ---------------------------------------------------
	var anchorKeys []string
	for key, t := range triplets {
		if t.rows[0].Block == "D3" {
			anchorKeys = append(anchorKeys, key)
			for _, r := range t.rows {
				if r.Cell != AnchorCell {
					f.errf("anchor %s runs cell %s, want %s", key, r.Cell, AnchorCell)
				}
			}
		}
	}
	sort.Strings(anchorKeys)
	if len(anchorKeys) != AnchorTriplets {
		f.errf("expected %d drift-anchor triplets, found %d", AnchorTriplets, len(anchorKeys))
	} else {
		want := []string{"D3|C2-01|01", "D3|C2-01|02", "D3|C2-01|03", "D3|C2-01|04"}
		for i, k := range anchorKeys {
			if k != want[i] {
				f.errf("anchor triplet %d is %s, want %s", i+1, k, want[i])
			}
		}
	}

	// Milestone placement: D3-01 at positions 1..3; D3-02 immediately after
	// the last EXP-1 row; D3-03 immediately after the C2-03 cell; D3-04 as
	// the final 3 rows.
	for _, r := range rows {
		if r.Phase == PhaseReplacement {
			continue // replacements are budget-checked, not milestone-checked
		}
		switch r.TripletKey {
		case "D3|C2-01|01":
			if r.Position < 1 || r.Position > 3 {
				f.errf("opening anchor D3-01 must occupy positions 1..3, found position %d", r.Position)
			}
		case "D3|C2-01|04":
			if r.Position <= len(rows)-3 {
				f.errf("closing anchor D3-04 must be the final 3 rows, found position %d of %d", r.Position, len(rows))
			}
		}
	}
	pos := func(key string) int {
		best := 1 << 30
		for _, r := range rows {
			if r.TripletKey == key && r.Position < best {
				best = r.Position
			}
		}
		return best
	}
	lastE1 := lastPosOfBlock(rows, "E1")
	firstC203 := pos("E2C|C2-01|")
	if lastE1 > 0 {
		if d := pos("D3|C2-01|02"); d != lastE1+1 {
			f.errf("anchor D3-02 must immediately follow EXP-1 (expected position %d, found %d)", lastE1+1, d)
		}
	}
	c203End := lastPosOfCell(rows, "C2-03")
	if c203End > 0 {
		if d := pos("D3|C2-01|03"); d != c203End+1 {
			f.errf("anchor D3-03 must immediately follow C2-03 (expected position %d, found %d)", c203End+1, d)
		}
	}
	_ = firstC203

	// --- stage cell coverage and per-cell triplet counts ------------------
	expected := catalog
	if len(expected.Cells) == 0 {
		expected = DefaultCatalog()
	}
	counts := map[string]int{} // cell -> confirmatory triplets
	for key, t := range triplets {
		if t.rows[0].Phase != PhaseConfirmatory {
			continue
		}
		if strings.HasPrefix(t.rows[0].Block, "E") {
			counts[t.rows[0].Cell]++
			_ = key
		}
	}
	for _, cell := range expected.Cells {
		if cell.Stage == StageEXP3 {
			f.errf("catalog cell %s: EXP-3 contributes zero disk runs", cell.ID)
			continue
		}
		n := counts[cell.ID]
		if n == 0 {
			f.errf("cell %s (%s) has no confirmatory rows", cell.ID, cell.Stage)
			continue
		}
		if n < MinTripletsPerCell || n > MaxTripletsPerCell {
			f.errf("cell %s has %d confirmatory triplets, outside the pre-registered [%d,%d] bound",
				cell.ID, n, MinTripletsPerCell, MaxTripletsPerCell)
		}
		delete(counts, cell.ID)
	}
	for cell := range counts {
		f.errf("run list contains confirmatory rows for unknown cell %s", cell)
	}

	// --- replacement rows -------------------------------------------------
	known := map[string]bool{} // cell|triplet|arm of planned rows
	for _, r := range rows {
		if r.Phase != PhaseReplacement {
			known[r.Cell+"|"+strconv.Itoa(r.Triplet)+"|"+r.Arm] = true
		}
	}
	for _, r := range rows {
		if r.Phase != PhaseReplacement {
			continue
		}
		if !known[r.Cell+"|"+strconv.Itoa(r.Triplet)+"|"+r.Arm] {
			f.errf("replacement row %s references cell=%s triplet=%d arm=%s which has no planned row",
				r.TrialID, r.Cell, r.Triplet, r.Arm)
		}
	}
	return f
}

var armsSet = map[string]bool{"A": true, "AMIN": true, "B": true}

func isBalancedPerm(p [3]string) bool {
	for _, want := range ArmPerms {
		if p == want {
			return true
		}
	}
	return false
}

func lastPosOfBlock(rows []Row, block string) int {
	best := 0
	for _, r := range rows {
		if r.Block == block && r.Phase == PhaseConfirmatory && r.Position > best {
			best = r.Position
		}
	}
	return best
}

func lastPosOfCell(rows []Row, cell string) int {
	best := 0
	for _, r := range rows {
		if r.Cell == cell && r.Phase == PhaseConfirmatory && r.Position > best {
			best = r.Position
		}
	}
	return best
}

// ReadCSV parses a runlist.csv in the frozen schema.
func ReadCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	idx := map[string]int{}
	for i, col := range header {
		idx[strings.TrimSpace(col)] = i
	}
	for _, col := range []string{"trial_id", "block", "cell", "phase", "triplet", "triplet_key", "arm", "order_in_triplet", "runlist_position"} {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("run list is missing required column %q", col)
		}
	}
	var rows []Row
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line++
		get := func(col string) string {
			if i, ok := idx[col]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		atoi := func(col string) int {
			v, _ := strconv.Atoi(get(col))
			return v
		}
		raw := map[string]string{}
		for _, col := range RunlistColumns {
			if i, ok := idx[col]; ok && i < len(rec) {
				raw[col] = strings.TrimSpace(rec[i])
			}
		}
		rows = append(rows, Row{
			Position:       atoi("runlist_position"),
			TrialID:        get("trial_id"),
			Block:          get("block"),
			Cell:           get("cell"),
			Phase:          get("phase"),
			Triplet:        atoi("triplet"),
			TripletKey:     get("triplet_key"),
			Arm:            get("arm"),
			OrderInTriplet: atoi("order_in_triplet"),
			WorkloadID:     get("workload_id"),
			Raw:            raw,
		})
	}
	return rows, nil
}
