package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"owndbms/research/experiments/phase_i7/harness2"
)

// Resolve existing parents too, so a symlink cannot disguise campaign evidence.
func checkOutput(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolved := abs
	for parent := abs; ; parent = filepath.Dir(parent) {
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			rel, err := filepath.Rel(parent, abs)
			if err != nil {
				return err
			}
			resolved = filepath.Join(real, rel)
			break
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	for _, candidate := range []string{abs, resolved} {
		parts := strings.Split(filepath.ToSlash(candidate), "/")
		for i, part := range parts {
			if strings.EqualFold(part, "X_READCOST") {
				for j := i + 2; j < len(parts)-1; j++ {
					if strings.EqualFold(parts[j], "trials") {
						return errors.New("diagnostic output must not be inside X_READCOST/*/trials")
					}
				}
			}
		}
	}
	return nil
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("stalldiag", flag.ContinueOnError)
	flags.SetOutput(stderr)
	specPath := flags.String("spec", "", "bare spec or trial record JSON")
	out := flags.String("out", "", "diagnostic result JSON")
	data := flags.String("data-dir", "", "override data directory (removed afterwards)")
	arm := flags.String("arm", "", "override arm: A|AMIN|B")
	wal := flags.String("wal-sync", "", "override WAL sync: true|false")
	trialID := flags.String("trial-id", "", "override trial ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *specPath == "" || *out == "" || flags.NArg() != 0 {
		return errors.New("usage: stalldiag -spec S.json -out F.json [-data-dir D] [-arm A|AMIN|B] [-wal-sync true|false] [-trial-id ID]")
	}
	if err := checkOutput(*out); err != nil {
		return err
	}
	b, err := os.ReadFile(*specPath)
	if err != nil {
		return err
	}
	var record struct {
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(b, &record); err != nil {
		return err
	}
	if len(record.Spec) != 0 {
		b = record.Spec
	}
	var spec harness2.Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return err
	}
	if spec.Block != "X4" {
		return errors.New("stalldiag requires an existing X4 spec")
	}
	spec.DiagTrace = true
	if *data != "" {
		spec.DataDir = *data
	}
	if *arm != "" {
		if *arm != "A" && *arm != "AMIN" && *arm != "B" {
			return errors.New("arm must be A, AMIN or B")
		}
		spec.Arm = *arm
	}
	if *wal != "" {
		if *wal != "true" && *wal != "false" {
			return errors.New("wal-sync must be true or false")
		}
		spec.WalSync, _ = strconv.ParseBool(*wal)
	}
	if *trialID != "" {
		spec.TrialID = *trialID
	}
	if _, err := spec.ResolveMode(); err != nil {
		return err
	}
	if spec.DataDir == "" {
		spec.DataDir, err = os.MkdirTemp("", "stalldiag-")
		if err != nil {
			return err
		}
	}
	dataAbs, err := filepath.Abs(spec.DataDir)
	if err != nil {
		return err
	}
	outAbs, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(dataAbs, outAbs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("diagnostic output must be outside the data directory, which is removed afterwards")
	}
	if err := os.MkdirAll(spec.DataDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(spec.DataDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("data dir %s is not empty", spec.DataDir)
	}
	defer os.RemoveAll(spec.DataDir)
	env := harness2.Environment{OS: runtime.GOOS, GoVersion: runtime.Version(), LogicalCores: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0)}
	result, runErr := harness2.Run(spec, env, "")
	if result != nil {
		payload := struct {
			Diagnostic bool `json:"diagnostic"`
			*harness2.TrialResult
		}{true, result}
		b, err = json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			return err
		}
		if runErr == nil && result.Status != "SUCCESS" {
			runErr = errors.New(result.Error)
		}
	}
	return runErr
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
