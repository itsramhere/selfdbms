// Command i7stub is a deterministic stand-in for i7trial used ONLY by the
// orchestration tests (run_i7.ps1 -TestMode). It never touches the engine.
//
// Behaviour is selected by the I7_STUB_MODE environment variable:
//
//	ok      write a valid SUCCESS envelope
//	fail    write a valid FAILED envelope, exit 1
//	crash   write nothing, exit 2
//	hang    sleep 120 s (the driver must kill it and record TIMEOUT)
//	slowok  sleep 2 s, then write a valid SUCCESS envelope
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

type envelope struct {
	Schema   string `json:"schema"`
	TrialID  string `json:"trial_id"`
	Block    string `json:"block"`
	Cell     string `json:"cell"`
	Arm      string `json:"arm"`
	Timing   struct {
		StartedUTC  string `json:"started_utc"`
		FinishedUTC string `json:"finished_utc"`
		WallMs      int64  `json:"wall_ms"`
	} `json:"timing"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Result struct {
		SchemaVersion string `json:"schema_version"`
		TrialID       string `json:"trial_id"`
		Block         string `json:"block"`
		Cell          string `json:"cell"`
		Arm           string `json:"arm"`
		Timestamps    struct {
			MeasuredWindowNs int64 `json:"measured_window_ns"`
		} `json:"timestamps"`
		Operations struct {
			ReadOpsCompleted int64 `json:"read_ops_completed"`
			WriteOpsCompleted int64 `json:"write_ops_completed"`
			ValidationErrorCount int64 `json:"validation_error_count"`
		} `json:"operations"`
	} `json:"result"`
	DataDirRemoved bool `json:"data_dir_removed"`
}

type spec struct {
	TrialID string `json:"trial_id"`
	Block   string `json:"block"`
	Cell    string `json:"cell"`
	Arm     string `json:"arm"`
	DataDir string `json:"data_dir"`
}

func main() {
	specPath := flag.String("spec", "", "trial spec")
	outPath := flag.String("out", "", "output envelope")
	_ = flag.String("gate", "", "ignored")
	_ = flag.String("approval", "", "ignored")
	_ = flag.String("platform", "", "ignored")
	_ = flag.String("git-rev", "", "ignored")
	flag.Parse()

	if *specPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "i7stub: -spec and -out are required")
		os.Exit(2)
	}
	mode := os.Getenv("I7_STUB_MODE")
	if mode == "" {
		mode = "ok"
	}
	switch mode {
	case "invalid":
		// Write a syntactically valid envelope with status SUCCESS but no result
		// payload. The driver must classify this as INVALID, never as success.
		raw, rerr := os.ReadFile(*specPath)
		if rerr != nil {
			os.Exit(2)
		}
		var inv struct {
			TrialID string `json:"trial_id"`
		}
		_ = json.Unmarshal(raw, &inv)
		out := map[string]any{"schema": "i7.trial.v1", "trial_id": inv.TrialID, "block": "D3", "arm": "A", "status": "SUCCESS"}
		b, _ := json.MarshalIndent(out, "", "  ")
		_ = os.WriteFile(*outPath, b, 0o644)
		os.Exit(0)
	case "crash":
		os.Exit(2)
	case "hang":
		time.Sleep(120 * time.Second)
		os.Exit(1)
	case "slowok":
		time.Sleep(2 * time.Second)
	}

	raw, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "i7stub: %v\n", err)
		os.Exit(2)
	}
	var s spec
	if err := json.Unmarshal(raw, &s); err != nil {
		fmt.Fprintf(os.Stderr, "i7stub: %v\n", err)
		os.Exit(2)
	}

	var env envelope
	env.Schema = "i7.trial.v1"
	env.TrialID = s.TrialID
	env.Block = s.Block
	env.Cell = s.Cell
	env.Arm = s.Arm
	env.Timing.StartedUTC = time.Now().UTC().Format(time.RFC3339Nano)
	env.Timing.FinishedUTC = env.Timing.StartedUTC
	if mode == "fail" {
		env.Status = "FAILED"
		env.Error = "stub failure requested"
	} else {
		env.Status = "SUCCESS"
		env.Result.SchemaVersion = "2.0.0"
		env.Result.TrialID = s.TrialID
		env.Result.Block = s.Block
		env.Result.Cell = s.Cell
		env.Result.Arm = s.Arm
		env.Result.Timestamps.MeasuredWindowNs = 1000000
		env.Result.Operations.ReadOpsCompleted = 10
		env.Result.Operations.WriteOpsCompleted = 1
	}
	env.DataDirRemoved = true
	if s.DataDir != "" {
		_ = os.RemoveAll(s.DataDir)
	}

	b, _ := json.MarshalIndent(env, "", "  ")
	if err := os.WriteFile(*outPath, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "i7stub: %v\n", err)
		os.Exit(2)
	}
	if env.Status != "SUCCESS" {
		os.Exit(1)
	}
	os.Exit(0)
}
