// Package runlist implements the Phase I7 R1.2e campaign run-list model:
// generation, validation, and the strict global execution ceiling.
//
// Scope guard: this package is engineering infrastructure only. It generates
// and validates structural run lists (12 confirmatory cells across EXP-1/2/4,
// four D3 drift-anchor triplets at pre-registered milestones, a bounded
// replacement budget) from SYNTHETIC records. It never executes trials and
// never touches engine behavior. Per R1.2e §2, EXP-3 contributes zero disk
// runs (its metadata microbenchmarks are Go testing.B suites, not campaign
// rows), so any EXP-3 row in a run list is a validation error.
//
// Pre-registered budget model (R1.2e §5):
//
//	12 cells × N∈[8,10] triplets × 3 arms   = 288..360 planned runs
//	4 D3 anchor triplets × 3 arms           =  12 runs
//	planned total                           = 300..372
//	replacement budget (Rule S-1)           =  16 runs
//	absolute worst case                     =  388 ≤ 400 ceiling
//	guaranteed unallocated buffer           ≥   12 runs
package runlist

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Pre-registered budget constants (R1.2e §5).
const (
	// GlobalCeiling is the strict, absolute upper bound on executed trials
	// across pilot, adaptive, and replacement attempts.
	GlobalCeiling = 400
	// ReplacementBudget is the maximum replacement attempts (Rule S-1).
	ReplacementBudget = 16
	// MaxPlannedRuns is the worst-case planned model size: 12 cells × 10
	// triplets × 3 arms + 12 anchor runs. Enforcing this preserves at least
	// 12 unallocated runs under every adaptive expansion.
	MaxPlannedRuns = 372
	// MaxWorstCaseRuns = MaxPlannedRuns + ReplacementBudget = 388.
	MaxWorstCaseRuns = MaxPlannedRuns + ReplacementBudget
	// MinTripletsPerCell / MaxTripletsPerCell bound the confirmatory N per
	// cell (R1.2e §1.3: N = min(10, max(8, N_req))).
	MinTripletsPerCell = 8
	MaxTripletsPerCell = 10
	// AnchorTriplets is the number of milestone drift-anchor triplets.
	AnchorTriplets = 4
)

// Arms are the three synchronization architectures under comparison.
var Arms = []string{"A", "AMIN", "B"}

// ArmPerms is the balanced Latin-square family of arm orderings.
var ArmPerms = [6][3]string{
	{"A", "AMIN", "B"}, {"A", "B", "AMIN"},
	{"AMIN", "A", "B"}, {"AMIN", "B", "A"},
	{"B", "A", "AMIN"}, {"B", "AMIN", "A"},
}

// Stage names.
const (
	StageEXP1 = "EXP-1"
	StageEXP2 = "EXP-2"
	StageEXP3 = "EXP-3"
	StageEXP4 = "EXP-4"
)

// Phases.
const (
	PhaseConfirmatory = "confirmatory"
	PhaseDriftAnchor  = "drift_anchor"
	PhaseReplacement  = "replacement"
)

// Cell is one experimental cell of the campaign catalog.
type Cell struct {
	ID                string `json:"id"`
	Stage             string `json:"stage"`
	Order             int    `json:"order"` // position within its stage
	WorkloadID        string `json:"workload_id,omitempty"`
	ReaderConcurrency int    `json:"reader_concurrency,omitempty"`
	Note              string `json:"note,omitempty"`
}

// Catalog is the structural campaign inventory. Workload parameters that are
// still protocol decisions (delays, rates, window sizes) are deliberately not
// modeled here; they bind at the pilot-authorization gate.
type Catalog struct {
	MasterSeed string `json:"master_seed"`
	Cells      []Cell `json:"cells"`
}

// DefaultCatalog returns the R1.2e §3.1/§5 inventory: EXP-1 (4 cells),
// EXP-2 (6 cells), EXP-4 (2 cells). EXP-3 contributes no disk rows.
func DefaultCatalog() Catalog {
	return Catalog{
		MasterSeed: "20261101",
		Cells: []Cell{
			{ID: "C1-01", Stage: StageEXP1, Order: 1},
			{ID: "C1-02", Stage: StageEXP1, Order: 2},
			{ID: "C1-03", Stage: StageEXP1, Order: 3},
			{ID: "C1-04", Stage: StageEXP1, Order: 4},
			{ID: "C2-01", Stage: StageEXP2, Order: 1, Note: "zero-delay baseline control"},
			{ID: "C2-02", Stage: StageEXP2, Order: 2, Note: "renewal delay sweep point 1"},
			{ID: "C2-03", Stage: StageEXP2, Order: 3, Note: "renewal delay sweep point 2"},
			{ID: "C2-04", Stage: StageEXP2, Order: 4, Note: "renewal delay sweep point 3"},
			{ID: "C2-05", Stage: StageEXP2, Order: 5, ReaderConcurrency: 2, Note: "concurrency scaling control (r=2)"},
			{ID: "C2-06", Stage: StageEXP2, Order: 6, Note: "CPU-load-matched negative control"},
			{ID: "C4-01", Stage: StageEXP4, Order: 1},
			{ID: "C4-02", Stage: StageEXP4, Order: 2},
		},
	}
}

// AnchorCell is the cell the drift anchors execute (C2-01 zero-delay).
const AnchorCell = "C2-01"

// RunlistColumns mirrors the frozen runlist.csv schema of run_i7.ps1.
var RunlistColumns = []string{
	"runlist_position", "trial_id", "block", "cell", "phase", "triplet", "triplet_key", "arm",
	"order_in_triplet", "workload_id", "reader_concurrency", "writer_concurrency",
	"measurement_duration_ms", "warmup_duration_ms", "cooldown_duration_ms",
	"keyspace_cardinality", "initial_sstable_count", "initial_keys_per_sstable",
	"target_read_hit_ratio", "memtable_threshold_bytes", "flush_trigger_interval_ms",
	"compaction_trigger_interval_ms", "wal_sync", "read_distribution",
	"expected_reader_set_size", "publication_events", "publication_mode",
	"paced_write_rate_per_sec", "open_loop_level", "runner_support", "note",
}

// Row is one run-list record.
type Row struct {
	Position              int
	TrialID               string
	Block                 string
	Cell                  string
	Phase                 string
	Triplet               int
	TripletKey            string
	Arm                   string
	OrderInTriplet        int
	WorkloadID            string
	ReaderConcurrency     int
	ExpectedReaderSetSize int
	RunnerSupport         string
	Note                  string
	// Raw keeps any workload parameters supplied by the catalog so a
	// round-trip preserves them byte-for-byte.
	Raw map[string]string
}

// TripletSeed derives the shared per-triplet request-stream seed from the
// master seed and the triplet key, matching the driver's convention
// (first 15 hex characters of SHA-256 interpreted as unsigned 60-bit int).
func TripletSeed(masterSeed, tripletKey string) int64 {
	sum := sha256.Sum256([]byte(masterSeed + "|triplet|" + tripletKey))
	v, err := strconv.ParseUint(hex.EncodeToString(sum[:])[:15], 16, 64)
	if err != nil {
		panic("runlist: seed derivation cannot fail on hex input: " + err.Error())
	}
	return int64(v)
}

// permIndexFor deterministically selects one of the six balanced
// Latin-square orderings for a triplet (R1.2e §4.2 rule 2).
func permIndexFor(masterSeed, tripletKey string) int {
	sum := sha256.Sum256([]byte(masterSeed + "|perm|" + tripletKey))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(len(ArmPerms)))
}

// Sizing maps cell ID -> confirmatory triplet count N.
type Sizing map[string]int

// ClampSizing returns a copy of s with every value clamped to [8,10] and
// missing cells defaulted to 8 (the minimum pre-registered confirmatory N).
func (c Catalog) ClampSizing(s Sizing) Sizing {
	out := Sizing{}
	for _, cell := range c.Cells {
		n, ok := s[cell.ID]
		if !ok {
			n = MinTripletsPerCell
		}
		if n < MinTripletsPerCell {
			n = MinTripletsPerCell
		}
		if n > MaxTripletsPerCell {
			n = MaxTripletsPerCell
		}
		out[cell.ID] = n
	}
	return out
}

// PlannedRuns returns the planned execution count for a sizing (12 cells × N
// × 3 arms + 12 anchor runs).
func (c Catalog) PlannedRuns(s Sizing) int {
	total := AnchorTriplets * len(Arms)
	for _, cell := range c.Cells {
		total += s[cell.ID] * len(Arms)
	}
	return total
}

// Generate builds the milestone-anchored run list (R1.2e §4.2):
//
//	D3-01 (opening)  -> positions 1..3
//	EXP-1 cells      -> then D3-02 (post-EXP-1 milestone)
//	EXP-2 C2-01..03  -> then D3-03 (EXP-2 midpoint milestone)
//	EXP-2 C2-04..06
//	EXP-4 cells      -> then D3-04 (closing baseline, final 3 rows)
//
// Rows carry only structural fields; workload parameters bind later. The
// result is deterministic in (catalog, sizing, master seed).
func Generate(catalog Catalog, sizing Sizing) ([]Row, error) {
	if len(catalog.Cells) == 0 {
		return nil, fmt.Errorf("catalog has no cells")
	}
	if catalog.MasterSeed == "" {
		return nil, fmt.Errorf("catalog.master_seed is required")
	}
	for _, cell := range catalog.Cells {
		if cell.Stage == StageEXP3 {
			return nil, fmt.Errorf("cell %s: EXP-3 contributes zero disk runs; EXP-3 cells must not appear in a run list", cell.ID)
		}
	}
	clamped := catalog.ClampSizing(sizing)
	if planned := catalog.PlannedRuns(clamped); planned > MaxPlannedRuns {
		return nil, fmt.Errorf("planned runs %d exceeds the pre-registered maximum %d", planned, MaxPlannedRuns)
	}

	stages := []struct {
		stage       string
		anchorAfter int // emit D3 anchor #n after this many cells of the stage; -1 = none
		anchorNo    int
	}{
		{StageEXP1, -1, 2}, // anchor D3-02 emitted after the whole stage
		{StageEXP2, 3, 3},  // anchor D3-03 after C2-01..C2-03
		{StageEXP4, -1, 4}, // anchor D3-04 after the whole stage
	}

	// Ordered cell lists per stage.
	byStage := map[string][]Cell{}
	for _, cell := range catalog.Cells {
		byStage[cell.Stage] = append(byStage[cell.Stage], cell)
	}
	for s := range byStage {
		sort.Slice(byStage[s], func(i, j int) bool { return byStage[s][i].Order < byStage[s][j].Order })
	}

	var rows []Row
	triplet := 0

	// D3-01: opening baseline triplet.
	rows = append(rows, anchorTriplet(catalog, &triplet, 1, "opening baseline")...)

	cellOrdinal := 0
	for _, st := range stages {
		cells := byStage[st.stage]
		if len(cells) == 0 {
			return nil, fmt.Errorf("stage %s has no cells", st.stage)
		}
		for i, cell := range cells {
			cellOrdinal++
			n := clamped[cell.ID]
			for t := 1; t <= n; t++ {
				triplet++
				key := fmt.Sprintf("%s|%s|%02d", stageBlock(st.stage), cell.ID, t)
				perm := ArmPerms[permIndexFor(catalog.MasterSeed, key)]
				for o, arm := range perm {
					rows = append(rows, Row{
						Block:                 stageBlock(st.stage),
						Cell:                  cell.ID,
						Phase:                 PhaseConfirmatory,
						Triplet:               triplet,
						TripletKey:            key,
						Arm:                   arm,
						OrderInTriplet:        o + 1,
						WorkloadID:            cell.WorkloadID,
						ReaderConcurrency:     cell.ReaderConcurrency,
						ExpectedReaderSetSize: cell.ReaderConcurrency,
						RunnerSupport:         runnerSupportFor(st.stage),
						Note:                  cell.Note,
					})
				}
			}
			// Milestone anchors inside a stage.
			if st.anchorAfter == i+1 {
				rows = append(rows, anchorTriplet(catalog, &triplet, st.anchorNo, "milestone anchor after "+cell.ID)...)
			}
		}
		// Stage-boundary anchor (D3-02 after EXP-1, D3-04 after EXP-4).
		if st.anchorAfter == -1 {
			rows = append(rows, anchorTriplet(catalog, &triplet, st.anchorNo, stageAnchorLabel(st.stage))...)
		}
	}

	// Consecutive positions after splicing.
	for i := range rows {
		rows[i].Position = i + 1
		if rows[i].TrialID == "" {
			rows[i].TrialID = fmt.Sprintf("i7_%s_%s_%03d_%s", strings.ToLower(rows[i].Block), strings.ToLower(rows[i].Cell), rows[i].Triplet, strings.ToLower(rows[i].Arm))
		}
	}
	if len(rows) > MaxPlannedRuns {
		return nil, fmt.Errorf("generated %d rows exceeds the pre-registered maximum %d", len(rows), MaxPlannedRuns)
	}
	return rows, nil
}

func stageBlock(stage string) string {
	switch stage {
	case StageEXP1:
		return "E1"
	case StageEXP2:
		// "E2C" is the dispatch label for the EXP-2 causal cells. It is
		// deliberately NOT the existing E2 publication block and is not
		// dispatchable by i7trial yet: the causal cells need reader-delay
		// spec fields that the R2 authorization does not add. Rows are
		// emitted with runner_support=preflight_pending and any attempt to
		// execute them today fails closed.
		return "E2C"
	case StageEXP4:
		return "E4"
	}
	return stage
}

// runnerSupportFor marks which blocks the current runner can dispatch today:
// E1, E4 and D3 are dispatchable; the E2C causal cells are not (see
// stageBlock).
func runnerSupportFor(stage string) string {
	if stage == StageEXP2 {
		return "preflight_pending"
	}
	return "i7trial"
}

func stageAnchorLabel(stage string) string {
	switch stage {
	case StageEXP1:
		return "post-EXP-1 milestone"
	case StageEXP4:
		return "closing baseline"
	}
	return "milestone"
}

// anchorTriplet emits one drift-anchor triplet (block D3, cell C2-01).
func anchorTriplet(catalog Catalog, triplet *int, anchorNo int, label string) []Row {
	*triplet++
	key := fmt.Sprintf("D3|%s|%02d", AnchorCell, anchorNo)
	perm := ArmPerms[permIndexFor(catalog.MasterSeed, key)]
	out := make([]Row, 0, len(Arms))
	for o, arm := range perm {
		out = append(out, Row{
			Block:          "D3",
			Cell:           AnchorCell,
			Phase:          PhaseDriftAnchor,
			Triplet:        *triplet,
			TripletKey:     key,
			Arm:            arm,
			OrderInTriplet: o + 1,
			RunnerSupport:  "i7trial",
			Note:           fmt.Sprintf("D3-%02d drift anchor (%s)", anchorNo, label),
		})
	}
	return out
}

// WriteCSV serializes rows in the frozen runlist.csv schema.
func WriteCSV(w io.Writer, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(RunlistColumns); err != nil {
		return err
	}
	for _, r := range rows {
		rec := r.record()
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// record maps the row onto the schema columns.
func (r Row) record() []string {
	get := func(k, def string) string {
		if r.Raw != nil {
			if v, ok := r.Raw[k]; ok {
				return v
			}
		}
		return def
	}
	out := make([]string, len(RunlistColumns))
	for i, col := range RunlistColumns {
		switch col {
		case "runlist_position":
			out[i] = strconv.Itoa(r.Position)
		case "trial_id":
			out[i] = r.TrialID
		case "block":
			out[i] = r.Block
		case "cell":
			out[i] = r.Cell
		case "phase":
			out[i] = r.Phase
		case "triplet":
			out[i] = strconv.Itoa(r.Triplet)
		case "triplet_key":
			out[i] = r.TripletKey
		case "arm":
			out[i] = r.Arm
		case "order_in_triplet":
			out[i] = strconv.Itoa(r.OrderInTriplet)
		case "workload_id":
			out[i] = r.WorkloadID
		case "reader_concurrency":
			out[i] = get(col, intOrEmpty(r.ReaderConcurrency))
		case "expected_reader_set_size":
			out[i] = get(col, intOrEmpty(r.ExpectedReaderSetSize))
		case "runner_support":
			out[i] = r.RunnerSupport
		case "note":
			out[i] = r.Note
		default:
			out[i] = get(col, "")
		}
	}
	return out
}

func intOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}
