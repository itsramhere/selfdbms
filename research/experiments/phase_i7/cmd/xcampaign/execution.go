package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"owndbms/research/experiments/phase_i7/harness2"
)

type record struct {
	Event    string `json:"event"`
	TrialID  string `json:"trial_id"`
	Attempt  int    `json:"attempt"`
	StartUTC string `json:"start_utc"`
	EndUTC   string `json:"end_utc,omitempty"`
	ExitCode int    `json:"exit_code"`
	Status   string `json:"status"`
	WallMs   int64  `json:"wall_ms"`
}

// A STARTED record consumes its attempt even if the driver dies before FINISHED.
// Attempt-specific data directories prevent an interrupted run contaminating a
// retry. A torn final JSON line is ignored; preceding complete records survive.
func readLedger(path string) (map[string]record, error) {
	out := map[string]record{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var r record
		if json.Unmarshal(line, &r) != nil {
			// Only an incomplete object prefix can be an interrupted append.
			// Valid JSON with an incompatible shape is a corrupt ledger.
			if !json.Valid(line) && bytes.HasPrefix(line, []byte(`{"event":`)) {
				continue
			}
			return nil, errors.New("malformed ledger record")
		}
		if r.TrialID == "" || r.Attempt < 1 || r.Attempt > 2 || (r.Event != "STARTED" && r.Event != "FINISHED") {
			return nil, errors.New("invalid ledger record")
		}
		out[r.TrialID] = r
	}
	return out, scanner.Err()
}

func appendRecord(f *os.File, r record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	// Normal JSONL has exactly one object per line. Only an interrupted final
	// append needs a separator before the next complete recovery record.
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err = f.ReadAt(last[:], info.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			b = append([]byte{'\n'}, b...)
		}
	}
	b = append(b, '\n')
	if _, err = f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func pending(plan Plan, latest map[string]record) []harness2.Spec {
	var out []harness2.Spec
	for _, s := range plan.Trials {
		r, ok := latest[s.TrialID]
		if ok && (r.Status == "SUCCESS" || r.Attempt >= 2) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func counts(plan Plan, latest map[string]record) (success, failed, attempted int) {
	for _, s := range plan.Trials {
		r, ok := latest[s.TrialID]
		if !ok {
			continue
		}
		attempted++
		if r.Status == "SUCCESS" {
			success++
		} else {
			failed++
		}
	}
	return
}

func failureLimit(plan Plan, latest map[string]record) bool {
	_, failed, attempted := counts(plan, latest)
	return attempted >= 40 && failed*20 > attempted
}

func execute(ctx context.Context, o options, plan Plan, p platform, w io.Writer) error {
	for _, name := range []string{"specs", "trials"} {
		if err := os.MkdirAll(filepath.Join(o.out, name), 0755); err != nil {
			return err
		}
	}
	ledgerPath := filepath.Join(o.out, "ledger.jsonl")
	latest, err := readLedger(ledgerPath)
	if err != nil {
		return err
	}
	if failureLimit(plan, latest) {
		return errors.New("failure rate exceeds 5% after at least 40 attempted trials; campaign stopped resumably")
	}
	ledger, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer ledger.Close()
	todo := pending(plan, latest)
	if o.limit > 0 && len(todo) > o.limit {
		todo = todo[:o.limit]
	}
	started := time.Now()
	executed := 0
	for _, s := range todo {
		if err = ctx.Err(); err != nil {
			return err
		}
		first := latest[s.TrialID].Attempt + 1
		var final record
		var wall time.Duration
		for attempt := first; attempt <= 2; attempt++ {
			if err = ctx.Err(); err != nil {
				return err
			}
			spec := s
			spec.DataDir = filepath.Join(o.data, fmt.Sprintf("%s_a%d", s.TrialID, attempt))
			specPath := filepath.Join(o.out, "specs", fmt.Sprintf("%s_a%d.json", s.TrialID, attempt))
			outPath := filepath.Join(o.out, "trials", fmt.Sprintf("%s_a%d.json", s.TrialID, attempt))
			if err = writeJSON(specPath, spec); err != nil {
				return err
			}
			t0 := time.Now()
			r := record{Event: "STARTED", TrialID: s.TrialID, Attempt: attempt, StartUTC: t0.UTC().Format(time.RFC3339Nano), Status: "STARTED", ExitCode: -1}
			if err = appendRecord(ledger, r); err != nil {
				return err
			}
			latest[s.TrialID] = r
			trialCtx, cancel := context.WithTimeout(ctx, o.timeout)
			cmd := exec.CommandContext(trialCtx, o.runner, "-spec", specPath, "-out", outPath, "-platform", filepath.Join(o.out, "platform_manifest.json"), "-gate", "X", "-approval", o.approval, "-git-rev", p.GitHEAD)
			log, logErr := os.Create(filepath.Join(o.out, "trials", fmt.Sprintf("%s_a%d.log", s.TrialID, attempt)))
			if logErr != nil {
				cancel()
				return logErr
			}
			cmd.Stdout, cmd.Stderr = log, log
			runErr := cmd.Run()
			_ = log.Close()
			status := resultStatus(outPath, s)
			if runErr != nil {
				status = "FAILED"
			}
			if errors.Is(trialCtx.Err(), context.DeadlineExceeded) {
				status = "TIMEOUT"
			}
			if ctx.Err() != nil {
				status = "INTERRUPTED"
			}
			cancel()
			exit := -1
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
			}
			elapsed := time.Since(t0)
			wall += elapsed
			r.Event = "FINISHED"
			r.EndUTC = time.Now().UTC().Format(time.RFC3339Nano)
			r.ExitCode = exit
			r.Status = status
			r.WallMs = elapsed.Milliseconds()
			if err = appendRecord(ledger, r); err != nil {
				return err
			}
			latest[s.TrialID] = r
			final = r
			if err = ctx.Err(); err != nil {
				return err
			}
			if status == "SUCCESS" {
				break
			}
		}
		executed++
		success, failed, attempted := counts(plan, latest)
		remaining := len(pending(plan, latest))
		eta := time.Duration(float64(time.Since(started)) / float64(executed) * float64(remaining)).Round(time.Second)
		progress := map[string]any{"campaign_id": campaignID, "updated_utc": time.Now().UTC().Format(time.RFC3339Nano), "total": len(plan.Trials), "attempted": attempted, "success": success, "failed": failed, "remaining": remaining, "last": final, "eta_seconds": eta.Seconds()}
		if err = writeJSON(filepath.Join(o.out, "progress.json"), progress); err != nil {
			return err
		}
		fmt.Fprintf(w, "[%d/%d] %s t%02d %s %s %.1fs, ETA %s\n", s.RunlistPosition, len(plan.Trials), s.Cell, s.Triplet, s.Arm, final.Status, wall.Seconds(), eta)
		if failureLimit(plan, latest) {
			return errors.New("failure rate exceeds 5% after at least 40 attempted trials; campaign stopped resumably")
		}
	}
	return nil
}
