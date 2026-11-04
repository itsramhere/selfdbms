package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"owndbms/research/experiments/phase_i7/harness2"
)

type options struct {
	runner, out, data, approval string
	design                      string
	master                      uint64
	dry, allowDirty             bool
	limit                       int
	timeout                     time.Duration
}

type approval struct {
	CampaignID   string `json:"campaign_id"`
	Approved     bool   `json:"approved"`
	RunnerSHA256 string `json:"runner_sha256"`
	PlanSHA256   string `json:"plan_sha256"`
}

type platform struct {
	RunnerPath    string          `json:"runner_path"`
	DataRoot      string          `json:"data_root"`
	TrialTimeout  string          `json:"trial_timeout"`
	CampaignID    string          `json:"campaign_id"`
	GitHEAD       string          `json:"git_head"`
	GitStatus     string          `json:"git_status"`
	Dirty         bool            `json:"git_dirty"`
	AllowDirty    bool            `json:"allow_dirty"`
	RunnerSHA256  string          `json:"runner_sha256"`
	PlanSHA256    string          `json:"plan_sha256"`
	Selftest      json.RawMessage `json:"selftest"`
	PowerScheme   string          `json:"power_scheme"`
	FreeDiskBytes uint64          `json:"free_disk_bytes"`
	CheckedUTC    string          `json:"checked_utc"`
	GOOS          string          `json:"goos"`
	GOARCH        string          `json:"goarch"`
}

func main() {
	var o options
	flag.StringVar(&o.runner, "runner", "", "path to i7trial executable (required)")
	flag.StringVar(&o.out, "out", "", "empty results root or existing X campaign (required)")
	flag.StringVar(&o.data, "data", "", "scratch data root (default <out>/data)")
	flag.StringVar(&o.design, "design", "readcost", "campaign design: readcost|intervention|compaction|tablecount|paced|settled|followup")
	flag.Uint64Var(&o.master, "master-seed", 20261030, "paired design master seed")
	flag.BoolVar(&o.dry, "dry-run", false, "print and persist plan and preflight; launch no trials")
	flag.IntVar(&o.limit, "limit", 0, "execute at most N unfinished trials (0 = all)")
	flag.StringVar(&o.approval, "approve", "", "approval JSON bound to runner and plan hashes")
	flag.DurationVar(&o.timeout, "trial-timeout", 120*time.Second, "per-process timeout")
	flag.BoolVar(&o.allowDirty, "allow-dirty", false, "permit and record a dirty worktree")
	flag.Parse()
	masterSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "master-seed" {
			masterSet = true
		}
	})
	if !masterSet && o.design == "intervention" {
		o.master = 20261103
	}
	if !masterSet && o.design == "compaction" {
		o.master = 20261104
	}
	if !masterSet && o.design == "tablecount" {
		o.master = 20261105
	}
	if !masterSet && o.design == "paced" {
		o.master = 20261106
	}
	if !masterSet && o.design == "settled" {
		o.master = 20261107
	}
	if !masterSet && o.design == "followup" {
		o.master = 20261108
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, o, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "xcampaign:", err)
		os.Exit(1)
	}
}

func checkApproval(path, runnerHash, planHash, expectedCampaign string) error {
	if path == "" {
		return errors.New("-approve is required for execution")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var a approval
	if err = json.Unmarshal(b, &a); err != nil {
		return err
	}
	if !a.Approved || a.CampaignID != expectedCampaign {
		return fmt.Errorf("approval must authorize %s", expectedCampaign)
	}
	if !strings.EqualFold(a.RunnerSHA256, runnerHash) || !strings.EqualFold(a.PlanSHA256, planHash) {
		return errors.New("approval runner_sha256 or plan_sha256 mismatch")
	}
	return nil
}

// preparePlan refuses arbitrary nonempty directories and verifies persisted bytes
// before overwriting anything. Identical JSON bytes are the approval boundary.
func preparePlan(out string, b []byte, runnerHash string) error {
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	path := filepath.Join(out, "plan.json")
	old, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		entries, e := os.ReadDir(out)
		if e != nil {
			return e
		}
		if len(entries) != 0 {
			return errors.New("-out must be empty or an existing X campaign")
		}
		return os.WriteFile(path, b, 0644)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(old, b) {
		return errors.New("stored plan.json differs from generated plan; refusing resume")
	}
	oldPlatform, err := os.ReadFile(filepath.Join(out, "platform.json"))
	if errors.Is(err, os.ErrNotExist) {
		// A process can stop between writing its plan and its first preflight.
		entries, e := os.ReadDir(out)
		if e != nil {
			return e
		}
		if len(entries) == 1 {
			return nil
		}
	}
	if err != nil {
		return err
	}
	var p platform
	if err = json.Unmarshal(oldPlatform, &p); err != nil {
		return err
	}
	var plan Plan
	if err = json.Unmarshal(b, &plan); err != nil {
		return err
	}
	if p.CampaignID != plan.CampaignID || p.RunnerSHA256 != runnerHash || p.PlanSHA256 != digest(b) {
		return errors.New("stored runner or plan hash differs; refusing resume")
	}
	return nil
}

func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(bounded, name, args...).CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

func preflight(ctx context.Context, o options, runnerHash, planHash, selectedCampaign string) (platform, error) {
	p := platform{RunnerPath: o.runner, DataRoot: o.data, TrialTimeout: o.timeout.String(), CampaignID: selectedCampaign, RunnerSHA256: runnerHash, PlanSHA256: planHash, AllowDirty: o.allowDirty, CheckedUTC: time.Now().UTC().Format(time.RFC3339Nano), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	var err error
	if p.GitHEAD, err = commandOutput(ctx, "git", "rev-parse", "HEAD"); err != nil {
		return p, err
	}
	if p.GitStatus, err = commandOutput(ctx, "git", "status", "--porcelain"); err != nil {
		return p, err
	}
	p.Dirty = p.GitStatus != ""
	if p.Dirty && !o.allowDirty {
		return p, errors.New("git tree is dirty; use -allow-dirty to record and override")
	}
	self, err := commandOutput(ctx, o.runner, "-selftest")
	if err != nil {
		return p, err
	}
	if !json.Valid([]byte(self)) {
		return p, errors.New("runner -selftest did not emit valid JSON")
	}
	var st struct {
		Schema    string   `json:"schema"`
		Supported []string `json:"supported"`
	}
	if err = json.Unmarshal([]byte(self), &st); err != nil || st.Schema != "i7.selftest.v1" {
		return p, errors.New("runner -selftest schema mismatch")
	}
	requiredBlocks := []string{"X1", "X2", "X3"}
	if selectedCampaign == "X-paced-v1" || selectedCampaign == "X-settled-v1" || selectedCampaign == "X-followup-v1" {
		requiredBlocks = append(requiredBlocks, "X4")
	}
	for _, required := range requiredBlocks {
		found := false
		for _, b := range st.Supported {
			if b == required {
				found = true
			}
		}
		if !found {
			return p, fmt.Errorf("runner selftest does not support %s", required)
		}
	}
	p.Selftest = json.RawMessage(self)
	p.PowerScheme, err = commandOutput(ctx, "powercfg", "/getactivescheme")
	if err != nil {
		if runtime.GOOS == "windows" {
			return p, err
		}
		p.PowerScheme = "unavailable on " + runtime.GOOS
	}
	if err = os.MkdirAll(o.data, 0755); err != nil {
		return p, err
	}
	// Both output and scratch volumes must have room when they differ.
	for _, path := range []string{o.out, o.data} {
		free, e := freeDisk(path)
		if e != nil {
			return p, e
		}
		if p.FreeDiskBytes == 0 || free < p.FreeDiskBytes {
			p.FreeDiskBytes = free
		}
		if free < 5_000_000_000 {
			return p, fmt.Errorf("%s: free disk %d bytes < 5 GB", path, free)
		}
	}
	return p, nil
}

func writeJSON(path string, v any) error {
	b, err := jsonBytes(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func run(ctx context.Context, o options, w io.Writer) error {
	if o.runner == "" || o.out == "" {
		return errors.New("-runner and -out are required")
	}
	if o.limit < 0 || o.timeout <= 0 {
		return errors.New("limit must be >=0 and trial-timeout >0")
	}
	var err error
	if o.runner, err = filepath.Abs(o.runner); err != nil {
		return err
	}
	if o.out, err = filepath.Abs(o.out); err != nil {
		return err
	}
	if o.data == "" {
		o.data = filepath.Join(o.out, "data")
	}
	if o.data, err = filepath.Abs(o.data); err != nil {
		return err
	}
	runnerBytes, err := os.ReadFile(o.runner)
	if err != nil {
		return err
	}
	runnerHash := digest(runnerBytes)
	var plan Plan
	switch o.design {
	case "", "readcost":
		plan = makePlan(o.master)
	case "intervention":
		plan = makeInterventionPlan(o.master)
	case "compaction":
		plan = makeCompactionPlan(o.master)
	case "followup":
		plan = makeFollowupPlan(o.master)
	case "settled":
		plan = makeSettledPlan(o.master)
	case "paced":
		plan = makePacedPlan(o.master)
	case "tablecount":
		plan = makeTablecountPlan(o.master)
	default:
		return fmt.Errorf("unknown design %q; expected readcost|intervention|compaction|tablecount|paced|settled|followup", o.design)
	}
	b, err := jsonBytes(plan)
	if err != nil {
		return err
	}
	planHash := digest(b)
	if !o.dry {
		if err = checkApproval(o.approval, runnerHash, planHash, plan.CampaignID); err != nil {
			return err
		}
	}
	if err = preparePlan(o.out, b, runnerHash); err != nil {
		return err
	}
	p, err := preflight(ctx, o, runnerHash, planHash, plan.CampaignID)
	if err != nil {
		fmt.Fprintf(w, "Preflight FAILED: %v\n", err)
		return err
	}
	if err = writeJSON(filepath.Join(o.out, "platform.json"), p); err != nil {
		return err
	}
	manifest := map[string]any{"os": runtime.GOOS, "os_version": runtime.GOOS + "_" + runtime.GOARCH, "logical_cores": runtime.NumCPU(), "go_version": runtime.Version(), "git_revision": p.GitHEAD, "git_dirty": p.Dirty, "power_scheme": p.PowerScheme, "volume_free_bytes": p.FreeDiskBytes}
	if err = writeJSON(filepath.Join(o.out, "platform_manifest.json"), manifest); err != nil {
		return err
	}
	fmt.Fprintf(w, "Preflight PASS: HEAD=%s dirty=%v allow_dirty=%v free_disk=%d\nPower: %s\nSelftest: PASS (stored in platform.json)\nrunner_sha256=%s\nplan_sha256=%s\n", p.GitHEAD, p.Dirty, p.AllowDirty, p.FreeDiskBytes, p.PowerScheme, runnerHash, planHash)
	if o.dry {
		fmt.Fprintln(w, "Cell                 Block Readers Path         Delay(us) Memtable Writers Events Compact(ms) Tables Keys/table")
		for _, c := range plan.Cells {
			s := c.Spec
			fmt.Fprintf(w, "%-20s %-5s %7d %-12s %9d %8d %7d %6d %11d %6d %10d\n", c.ID, s.Block, s.ReaderConcurrency, s.ReadPath, s.ReadIODelayUs, s.MemtableThresholdBytes, s.WriterConcurrency, s.PublicationEvents, s.CompactionTriggerMs, s.InitialSSTableCount, s.InitialKeysPerSSTable)
		}
		if plan.CampaignID == "X-followup-v1" {
			eta := time.Duration(len(plan.Trials)) * (time.Duration(plan.SettleMs)*time.Millisecond + 12500*time.Millisecond)
			fmt.Fprintf(w, "Total: %d cells; %d trials; pause %d ms before every attempt; ETA at 12.5s/trial plus pause: %s (budget about 85 minutes)\n", len(plan.Cells), len(plan.Trials), plan.SettleMs, eta)
		} else if plan.CampaignID == "X-settled-v1" {
			eta := time.Duration(len(plan.Trials)) * (time.Duration(plan.SettleMs)*time.Millisecond + 12500*time.Millisecond)
			fmt.Fprintf(w, "Total: %d runs; pause %d ms before every attempt; ETA at 12.5s/trial plus pause: %s\n", len(plan.Trials), plan.SettleMs, eta)
		} else if plan.CampaignID == "X-intervention-v1" {
			fmt.Fprintf(w, "Total: %d cells x 5 rounds x 2 conditions x 2 arms = %d runs\n", len(plan.Cells), len(plan.Trials))
		} else if plan.CampaignID == "X-compaction-v1" || plan.CampaignID == "X-tablecount-v1" || plan.CampaignID == "X-paced-v1" {
			fmt.Fprintf(w, "Total: %d cells x 10 rounds x 3 arms = %d runs (no anchors)\n", len(plan.Cells), len(plan.Trials))
		} else {
			fmt.Fprintf(w, "Total: %d cells x 10 triplets x 3 arms + 9 anchor runs = %d runs\nETA at 14s/trial: %s\nFirst 20 schedule rows:\n", len(plan.Cells), len(plan.Trials), (time.Duration(len(plan.Trials)) * 14 * time.Second).String())
		}
		for _, s := range plan.Trials[:20] {
			fmt.Fprintf(w, "[%03d/%d] %s t%02d %-4s seed=%d\n", s.RunlistPosition, len(plan.Trials), s.Cell, s.Triplet, s.Arm, s.Seed)
		}
		return nil
	}
	return execute(ctx, o, plan, p, w)
}

// The envelope is checked as well as the exit code: a stale or malformed output
// must never turn a process failure into a completed trial in the resume ledger.
func resultStatus(path string, s harness2.Spec) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "FAILED"
	}
	var e struct {
		Schema  string                `json:"schema"`
		TrialID string                `json:"trial_id"`
		Status  string                `json:"status"`
		Result  *harness2.TrialResult `json:"result"`
	}
	if json.Unmarshal(b, &e) != nil || e.Schema != "i7.trial.v1" || e.TrialID != s.TrialID || e.Status != "SUCCESS" || e.Result == nil {
		return "FAILED"
	}
	r := e.Result
	if r.Status != "SUCCESS" || r.TrialID != s.TrialID || (r.Operations.ReadOpsCompleted == 0 && s.Block != string(harness2.BlockX4)) || r.Operations.ValidationErrorCount != 0 || r.Timestamps.MeasuredWindowNs <= 0 || (s.IsX() && r.LSMDiagnostics.CompactionErrors > 0) {
		return "FAILED"
	}
	return "SUCCESS"
}
