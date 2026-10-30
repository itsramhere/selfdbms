// Command i7trial executes exactly one I7 trial in its own OS process.
//
// Contract (see research/experiments/phase_i7/12_WINDOWS_EXECUTION_AMENDMENT.md):
//
//	i7trial -selftest                       -> platform + timer + affinity JSON, no trial
//	i7trial -smoke -mode A|AMIN|B -out F    -> tiny functional trial on the host FS
//	i7trial -spec S.json -out F -gate G4 -approval A.json
//
// Execution is refused unless an approval file authorizes the exact gate,
// protocol version, runner binary and block being executed. Block E7 is owned by
// the separate pebble_runner binary and is rejected here.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"owndbms/research/experiments/harness"
	"owndbms/research/experiments/phase_i7/harness2"
)

const (
	exitOK          = 0
	exitTrialFailed = 1
	exitUsage       = 2
	exitUnsupported = 3
	exitNotApproved = 4

	protocolVersion = "1.2.0-windows"
	campaignID      = "I7_STRENGTHENING_V1"
)

// supportedBlocks lists the blocks this runner dispatches. D3 (R1.2e §4.2)
// is the drift-anchor control block: closed-loop triplets on the C2-01
// zero-delay cell, spliced at campaign milestones. Its absence here was the
// D3 runner-dispatch defect corrected by the R2 engineering authorization —
// a runlist containing D3 rows previously failed closed with exitUnsupported.
var supportedBlocks = []string{"E1", "E2", "E3", "E4", "E5", "D3", "PF", "SMOKE", "X1", "X2", "X3", "ANCHOR"}
var separateRunnerBlocks = []string{"E7"}

// Approval is written by the operator only after external review authorizes the
// gate. The binding fields make the approval specific to one protocol version,
// one runlist, one binary and one permitted block set.
type Approval struct {
	CampaignID      string   `json:"campaign_id"`
	ProtocolVersion string   `json:"protocol_version"`
	Gate            string   `json:"gate"`
	Approved        bool     `json:"approved"`
	ApprovedBy      string   `json:"approved_by"`
	ApprovedAt      string   `json:"approved_at"`
	Scope           string   `json:"scope"`
	PlanSHA256      string   `json:"plan_sha256,omitempty"`
	RunlistSHA256   string   `json:"runlist_sha256"`
	RunnerSHA256    string   `json:"runner_sha256"`
	AllowedBlocks   []string `json:"allowed_blocks"`
	AllowedPhases   []string `json:"allowed_phases"`
	MaxTrials       int      `json:"max_trials"`
	AllowDirty      bool     `json:"allow_dirty"`
}

type RuntimeInfo struct {
	GoVersion   string `json:"go_version"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	GOMAXPROCS  int    `json:"gomaxprocs"`
	NumCPU      int    `json:"num_cpu"`
	AffinityHex string `json:"affinity_mask"`
	ExeSHA256   string `json:"exe_sha256"`
	ExePath     string `json:"exe_path"`
	Protocol    string `json:"protocol_version"`
}

type TimingInfo struct {
	StartedUTC  string `json:"started_utc"`
	FinishedUTC string `json:"finished_utc"`
	WallMs      int64  `json:"wall_ms"`
}

type Envelope struct {
	Schema         string                `json:"schema"`
	TrialID        string                `json:"trial_id"`
	Block          string                `json:"block"`
	Cell           string                `json:"cell"`
	Triplet        int                   `json:"triplet"`
	Arm            string                `json:"arm"`
	RunlistPos     int                   `json:"runlist_position"`
	OrderPosition  int                   `json:"order_position"`
	Spec           *harness2.Spec        `json:"spec,omitempty"`
	Runtime        RuntimeInfo           `json:"runtime"`
	Platform       *PlatformManifest     `json:"platform,omitempty"`
	Timing         TimingInfo            `json:"timing"`
	Status         string                `json:"status"`
	Error          string                `json:"error,omitempty"`
	Result         *harness2.TrialResult `json:"result,omitempty"`
	DataDirRemoved bool                  `json:"data_dir_removed"`
}

func main() {
	var (
		selftest = flag.Bool("selftest", false, "emit platform/timer/affinity JSON and exit")
		smoke    = flag.Bool("smoke", false, "run a tiny functional trial on the host filesystem")
		specPath = flag.String("spec", "", "trial spec JSON")
		outPath  = flag.String("out", "", "output JSON path")
		modeFlag = flag.String("mode", "", "smoke sync mode: A, AMIN or B")
		platform = flag.String("platform", "", "platform_manifest.json produced by run_i7.ps1")
		gate     = flag.String("gate", "", "gate being executed (G3, G4, G5, X)")
		approval = flag.String("approval", "", "approval JSON file")
		keepData = flag.Bool("keep-data", false, "keep the trial data directory")
		affinity = flag.String("affinity", "", "expected affinity mask, hex (recorded only)")
		gitRev   = flag.String("git-rev", "", "source revision recorded in the trial")
	)
	flag.Parse()

	rt := runtimeInfo()

	switch {
	case *selftest:
		emitSelftest(rt, *affinity)
		return
	case *smoke:
		runSmoke(rt, *modeFlag, *outPath, *platform, *gitRev, *keepData)
		return
	}

	if *specPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: i7trial -spec S.json -out F.json -gate G4 -approval A.json")
		os.Exit(exitUsage)
	}
	runSpec(rt, *specPath, *outPath, *platform, *gitRev, *gate, *approval, *keepData)
}

func runtimeInfo() RuntimeInfo {
	exe, _ := os.Executable()
	sum := ""
	if exe != "" {
		if b, err := os.ReadFile(exe); err == nil {
			h := sha256.Sum256(b)
			sum = hex.EncodeToString(h[:])
		}
	}
	mask, _ := processAffinityMask()
	return RuntimeInfo{
		GoVersion:   runtime.Version(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		GOMAXPROCS:  runtime.GOMAXPROCS(0),
		NumCPU:      runtime.NumCPU(),
		AffinityHex: fmt.Sprintf("0x%x", mask),
		ExeSHA256:   sum,
		ExePath:     exe,
		Protocol:    protocolVersion,
	}
}

func runSpec(rt RuntimeInfo, specPath, outPath, platformPath, gitRev, gate, approvalPath string, keepData bool) {
	env := &Envelope{Schema: "i7.trial.v1"}
	start := time.Now()
	env.Timing.StartedUTC = start.UTC().Format(time.RFC3339Nano)
	env.Runtime = rt

	fail := func(code int, msg string) {
		env.Status = "FAILED"
		env.Error = msg
		env.Timing.FinishedUTC = time.Now().UTC().Format(time.RFC3339Nano)
		env.Timing.WallMs = time.Since(start).Milliseconds()
		_ = writeJSONAtomic(outPath, env)
		fmt.Fprintf(os.Stderr, "[i7trial] %s\n", msg)
		os.Exit(code)
	}

	raw, err := os.ReadFile(specPath)
	if err != nil {
		fail(exitUsage, "read spec: "+err.Error())
	}
	var spec harness2.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fail(exitUsage, "parse spec: "+err.Error())
	}
	if gate == "X" && spec.Block == "ANCHOR" {
		spec.Block = "X1"
	}
	env.Spec = &spec
	env.TrialID, env.Block, env.Cell = spec.TrialID, spec.Block, spec.Cell
	env.Triplet, env.Arm = spec.Triplet, spec.Arm
	env.RunlistPos, env.OrderPosition = spec.RunlistPosition, spec.OrderPosition

	if spec.TrialID == "" {
		fail(exitUsage, "spec.trial_id is required")
	}
	for _, b := range separateRunnerBlocks {
		if strings.EqualFold(spec.Block, b) {
			fail(exitUnsupported, fmt.Sprintf("block %s is executed by pebble_runner.exe, not i7trial", b))
		}
	}
	if !containsFold(supportedBlocks, spec.Block) {
		fail(exitUnsupported, "unsupported block: "+spec.Block)
	}
	if _, err := spec.ResolveMode(); err != nil {
		fail(exitUsage, err.Error())
	}
	appr, err := checkApproval(approvalPath, gate, spec.Block)
	if err != nil {
		fail(exitNotApproved, err.Error())
	}
	if gate == "X" {
		// Campaign specs live directly below specs/. Bind the runner invocation to
		// the same immutable plan that the driver checked before launching it.
		plan, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(specPath)), "plan.json"))
		if err != nil {
			fail(exitNotApproved, "read X plan: "+err.Error())
		}
		sum := sha256.Sum256(plan)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), appr.PlanSHA256) {
			fail(exitNotApproved, "X plan_sha256 mismatch")
		}
	}

	plat := readPlatformManifest(platformPath)
	env.Platform = plat

	res, runErr := harness2.Run(spec, toEnvironment(plat), gitRev)
	if res != nil {
		env.Result = res
		env.Status = res.Status
		env.Error = res.Error
	} else {
		env.Status = "FAILED"
	}
	if runErr != nil {
		env.Status = "FAILED"
		if env.Error == "" {
			env.Error = runErr.Error()
		} else {
			env.Error += " | " + runErr.Error()
		}
	}

	if !keepData {
		if rmErr := os.RemoveAll(spec.DataDir); rmErr == nil {
			env.DataDirRemoved = true
		}
	}
	env.Timing.FinishedUTC = time.Now().UTC().Format(time.RFC3339Nano)
	env.Timing.WallMs = time.Since(start).Milliseconds()

	if err := validateEnvelope(env); err != nil {
		env.Status = "FAILED"
		env.Error = "envelope validation: " + err.Error()
	}
	if err := writeJSONAtomic(outPath, env); err != nil {
		fmt.Fprintf(os.Stderr, "[i7trial] write output: %v\n", err)
		os.Exit(exitTrialFailed)
	}
	if env.Status != "SUCCESS" {
		fmt.Fprintf(os.Stderr, "[i7trial] %s failed: %s\n", spec.TrialID, env.Error)
		os.Exit(exitTrialFailed)
	}
	os.Exit(exitOK)
}

// validateEnvelope is the fail-closed check the driver also performs, so a
// malformed record can never reach the ledger as a success.
func validateEnvelope(env *Envelope) error {
	if env.Schema != "i7.trial.v1" {
		return errors.New("schema mismatch")
	}
	if env.TrialID == "" || env.Block == "" || env.Arm == "" {
		return errors.New("missing identity fields")
	}
	if env.Status != "SUCCESS" {
		return nil // failures are recorded, not rejected
	}
	if env.Result == nil {
		return errors.New("SUCCESS without a result payload")
	}
	r := env.Result
	if r.TrialID != env.TrialID {
		return fmt.Errorf("result trial_id %q != envelope %q", r.TrialID, env.TrialID)
	}
	if r.Operations.ReadOpsCompleted == 0 {
		return errors.New("SUCCESS with zero completed reads")
	}
	if r.Timestamps.MeasuredWindowNs <= 0 {
		return errors.New("SUCCESS with a non-positive measured window")
	}
	if r.Operations.ValidationErrorCount != 0 {
		return errors.New("SUCCESS with validation errors")
	}
	return nil
}

func runSmoke(rt RuntimeInfo, arm, outPath, platformPath, gitRev string, keepData bool) {
	if outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: i7trial -smoke -mode A|AMIN|B -out F.json")
		os.Exit(exitUsage)
	}
	start := time.Now()
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("i7smoke_%s_%d", sanitize(arm), start.UnixNano()))
	spec := harness2.Spec{
		Schema:                 "i7.trial-spec.v1",
		TrialID:                "smoke_" + sanitize(arm),
		Block:                  "SMOKE",
		Cell:                   "smoke",
		Arm:                    arm,
		Seed:                   424242,
		WorkloadID:             "SMOKE",
		ReaderConcurrency:      2,
		WriterConcurrency:      1,
		MeasurementDurationMs:  400,
		WarmupDurationMs:       100,
		CooldownDurationMs:     50,
		KeyspaceCardinality:    80,
		InitialSSTableCount:    2,
		InitialKeysPerSSTable:  40,
		TargetReadHitRatio:     0.95,
		MemtableThresholdBytes: 64 << 20,
		DataDir:                dir,
		ExpectedReaderSetSize:  2,
	}
	env := &Envelope{Schema: "i7.smoke.v1", TrialID: spec.TrialID, Block: "SMOKE", Arm: arm, Runtime: rt}
	env.Timing.StartedUTC = start.UTC().Format(time.RFC3339Nano)
	plat := readPlatformManifest(platformPath)
	env.Platform = plat
	res, err := harness2.Run(spec, toEnvironment(plat), gitRev)
	if res != nil {
		env.Result = res
		env.Status = res.Status
		env.Error = res.Error
	} else {
		env.Status = "FAILED"
	}
	if err != nil {
		env.Status = "FAILED"
		if env.Error == "" {
			env.Error = err.Error()
		}
	}
	removeErr := os.RemoveAll(dir)
	env.DataDirRemoved = removeErr == nil
	if removeErr != nil {
		env.Status = "FAILED"
		env.Error = strings.TrimSpace(env.Error + " | RemoveAll: " + removeErr.Error())
	}
	env.Timing.FinishedUTC = time.Now().UTC().Format(time.RFC3339Nano)
	env.Timing.WallMs = time.Since(start).Milliseconds()
	if err := writeJSONAtomic(outPath, env); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitTrialFailed)
	}
	if env.Status != "SUCCESS" {
		fmt.Fprintf(os.Stderr, "[i7trial] smoke %s failed: %s\n", arm, env.Error)
		os.Exit(exitTrialFailed)
	}
	fmt.Printf("[i7trial] smoke %s OK: reads=%d writes=%d p50=%.1fus\n", arm,
		res.Operations.ReadOpsCompleted, res.Operations.WriteOpsCompleted,
		float64(res.LatencyRead.P50Nanos)/1000.0)
	os.Exit(exitOK)
}

func emitSelftest(rt RuntimeInfo, expectedAffinity string) {
	actual, _ := processAffinityMask()
	cores := maskCores(actual)
	out := map[string]any{
		"schema":  "i7.selftest.v1",
		"runtime": rt,
		"affinity": map[string]any{
			"expected":     orDefault(expectedAffinity, "unset"),
			"actual":       fmt.Sprintf("0x%x", actual),
			"actual_cores": cores,
			"match":        expectedAffinity == "" || strings.EqualFold(expectedAffinity, fmt.Sprintf("0x%x", actual)),
		},
		"timers":      probeTimers(),
		"schedule":    probeSchedule(5*time.Millisecond, 200),
		"supported":   supportedBlocks,
		"separate":    separateRunnerBlocks,
		"protocol":    protocolVersion,
		"cgo_enabled": os.Getenv("CGO_ENABLED"),
		"tmp_dir":     os.TempDir(),
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	os.Exit(exitOK)
}

func probeTimers() map[string]any {
	out := map[string]any{}
	out["qpc_freq_hz"] = harness.QPCFrequencyHz()

	const samples = 20000
	minQPC, lastQPC := int64(1<<62), int64(0)
	start := harness.NowPrecision()
	for i := 0; i < samples; i++ {
		el := harness.SincePrecisionNanos(start)
		if el > lastQPC {
			if d := el - lastQPC; d < minQPC {
				minQPC = d
			}
			lastQPC = el
		}
	}
	out["qpc_min_positive_delta_ns"] = minQPC
	out["qpc_burst_ns"] = harness.SincePrecisionNanos(start)

	t0 := time.Now()
	prev := t0
	distinct, minStep := 1, int64(1<<62)
	for time.Since(t0) < 50*time.Millisecond {
		now := time.Now()
		if d := now.Sub(prev).Nanoseconds(); d > 0 {
			if d < minStep {
				minStep = d
			}
			distinct++
			prev = now
		}
	}
	wallWindow := time.Since(t0)
	out["now_window_ns"] = wallWindow.Nanoseconds()
	out["now_distinct_values"] = distinct
	out["now_min_positive_delta_ns"] = minStep
	out["now_effective_hz"] = float64(distinct) / wallWindow.Seconds()

	const k = 200
	var sumSleep, maxErr int64
	for i := 0; i < k; i++ {
		s := time.Now()
		time.Sleep(time.Millisecond)
		el := time.Since(s).Nanoseconds()
		sumSleep += el
		e := el - int64(time.Millisecond)
		if e < 0 {
			e = -e
		}
		if e > maxErr {
			maxErr = e
		}
	}
	out["sleep1ms_mean_ns"] = sumSleep / k
	out["sleep1ms_max_abs_err_ns"] = maxErr
	return out
}

// probeSchedule uses QPC, never time.Now (see 12 §4.2).
func probeSchedule(interval time.Duration, n int) map[string]any {
	errs := make([]int64, 0, n)
	start := harness.NowPrecision()
	for i := 1; i <= n; i++ {
		target := int64(i) * interval.Nanoseconds()
		for {
			rem := target - harness.SincePrecisionNanos(start)
			if rem <= 0 {
				break
			}
			if rem > 2_000_000 {
				time.Sleep(time.Duration(rem) - 1500*time.Microsecond)
				continue
			}
			runtime.Gosched()
		}
		errs = append(errs, harness.SincePrecisionNanos(start)-target)
	}
	abs := make([]int64, len(errs))
	for i, e := range errs {
		if e < 0 {
			e = -e
		}
		abs[i] = e
	}
	sort.Slice(abs, func(i, j int) bool { return abs[i] < abs[j] })
	pct := func(p float64) int64 {
		if len(abs) == 0 {
			return 0
		}
		idx := int(float64(len(abs))*p) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(abs) {
			idx = len(abs) - 1
		}
		return abs[idx]
	}
	return map[string]any{
		"clock":          "QueryPerformanceCounter",
		"interval_us":    interval.Microseconds(),
		"n":              n,
		"p50_abs_err_us": float64(pct(0.50)) / 1000.0,
		"p95_abs_err_us": float64(pct(0.95)) / 1000.0,
		"p99_abs_err_us": float64(pct(0.99)) / 1000.0,
		"max_abs_err_us": float64(pct(1.0)) / 1000.0,
	}
}

type PlatformManifest struct {
	OS                      string `json:"os"`
	OSVersion               string `json:"os_version"`
	CPUModel                string `json:"cpu_model"`
	PhysicalCores           int    `json:"physical_cores"`
	LogicalCores            int    `json:"logical_cores"`
	TotalRAMBytes           int64  `json:"total_ram_bytes"`
	FreeRAMBytes            int64  `json:"free_ram_bytes"`
	Filesystem              string `json:"filesystem"`
	DiskModel               string `json:"disk_model"`
	DiskBusType             string `json:"disk_bus_type"`
	DiskMediaType           string `json:"disk_media_type"`
	VolumeFreeBytes         int64  `json:"volume_free_bytes"`
	AffinityRequested       string `json:"affinity_requested"`
	AffinityActual          string `json:"affinity_actual"`
	PowerScheme             string `json:"power_scheme"`
	DefenderRealtimeEnabled any    `json:"defender_realtime"`
	GoVersion               string `json:"go_version"`
	GitRevision             string `json:"git_revision"`
	GitDirty                bool   `json:"git_dirty"`
	WorktreeFingerprint     string `json:"worktree_fingerprint"`
}

func readPlatformManifest(path string) *PlatformManifest {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pm PlatformManifest
	if json.Unmarshal(b, &pm) != nil {
		return nil
	}
	return &pm
}

func toEnvironment(pm *PlatformManifest) harness.EnvironmentInfo {
	env := harness.EnvironmentInfo{
		OS:             runtime.GOOS,
		Kernel:         runtime.GOOS + "_" + runtime.GOARCH,
		CPUModel:       "unknown",
		PhysicalCores:  runtime.NumCPU(),
		LogicalCores:   runtime.NumCPU(),
		GoVersion:      runtime.Version(),
		GOMAXPROCS:     runtime.GOMAXPROCS(0),
		FilesystemType: "NTFS",
	}
	if pm == nil {
		return env
	}
	if pm.OS != "" {
		env.OS = pm.OS
	}
	if pm.OSVersion != "" {
		env.Kernel = pm.OSVersion
	}
	if pm.CPUModel != "" {
		env.CPUModel = pm.CPUModel
	}
	if pm.PhysicalCores > 0 {
		env.PhysicalCores = pm.PhysicalCores
	}
	if pm.LogicalCores > 0 {
		env.LogicalCores = pm.LogicalCores
	}
	if pm.TotalRAMBytes > 0 {
		env.TotalRAMBytes = pm.TotalRAMBytes
	}
	if pm.Filesystem != "" {
		env.FilesystemType = pm.Filesystem
	}
	return env
}

// checkApproval validates the gate plus the binding fields. Every binding field
// is mandatory: an approval that does not name the protocol version, the runner
// binary and the permitted blocks cannot authorize an execution.
func checkApproval(approvalPath, gate, block string) (*Approval, error) {
	if gate == "" {
		return nil, errors.New("no gate supplied; refusing to execute a trial")
	}
	if approvalPath == "" {
		return nil, errors.New("no approval file supplied; refusing to execute a trial")
	}
	b, err := os.ReadFile(approvalPath)
	if err != nil {
		return nil, fmt.Errorf("read approval: %w", err)
	}
	var a Approval
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("parse approval: %w", err)
	}
	if !a.Approved {
		return nil, errors.New("approval file does not set approved=true")
	}
	if gate == "X" {
		if a.CampaignID != "X-readcost-v1" {
			return nil, errors.New("X approval campaign_id mismatch")
		}
		if !containsFold([]string{"X1", "X2", "X3", "ANCHOR"}, block) {
			return nil, errors.New("block is outside X approval")
		}
		planHash, err := hex.DecodeString(a.PlanSHA256)
		if err != nil || len(planHash) != sha256.Size {
			return nil, errors.New("X approval requires a plan_sha256")
		}
		self := runtimeInfo().ExeSHA256
		if self == "" || !strings.EqualFold(a.RunnerSHA256, self) {
			return nil, errors.New("X approval runner_sha256 mismatch")
		}
		return &a, nil
	}
	if a.CampaignID != campaignID {
		return nil, fmt.Errorf("approval campaign_id = %q, expected %q", a.CampaignID, campaignID)
	}
	if a.ProtocolVersion != protocolVersion {
		return nil, fmt.Errorf("approval protocol_version = %q, expected %q", a.ProtocolVersion, protocolVersion)
	}
	if a.Gate != gate {
		return nil, fmt.Errorf("approval is for gate %q, requested %q", a.Gate, gate)
	}
	self := runtimeInfo().ExeSHA256
	if a.RunnerSHA256 == "" {
		return nil, errors.New("approval does not bind a runner_sha256")
	}
	if self != "" && !strings.EqualFold(a.RunnerSHA256, self) {
		return nil, fmt.Errorf("approval runner_sha256 %s does not match this binary %s", a.RunnerSHA256, self)
	}
	if len(a.AllowedBlocks) == 0 {
		return nil, errors.New("approval does not list allowed_blocks")
	}
	if !containsFold(a.AllowedBlocks, block) {
		return nil, fmt.Errorf("block %s is not in the approved set %v", block, a.AllowedBlocks)
	}
	return &a, nil
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
