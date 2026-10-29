// Command runlistctl generates and validates Phase I7 R1.2e campaign run
// lists. It is engineering infrastructure for the preflight: it writes and
// checks structural run lists only, executes nothing, and enforces the
// strict global execution ceiling (400 runs), the milestone drift anchors,
// and the Rule S-1 replacement budget.
//
// Usage:
//
//	runlistctl generate -out runlist.csv [-catalog cat.json] [-sizing s.json] [-seed 20261101]
//	runlistctl validate -runlist runlist.csv [-catalog cat.json] [-report report.json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"owndbms/research/experiments/phase_i7/runlist"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "generate":
		generate(os.Args[2:])
	case "validate":
		validate(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: runlistctl generate|validate [flags] (see -h)")
	os.Exit(2)
}

func fs(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	return fs
}

func generate(args []string) {
	f := fs("generate")
	out := f.String("out", "runlist.csv", "output CSV path")
	catalogPath := f.String("catalog", "", "optional cell catalog JSON (default: the R1.2e inventory)")
	sizingPath := f.String("sizing", "", "optional per-cell confirmatory N JSON (default: all cells at N=8)")
	seed := f.String("seed", "", "master seed override")
	_ = f.Parse(args)

	catalog := runlist.DefaultCatalog()
	if *catalogPath != "" {
		b, err := os.ReadFile(*catalogPath)
		if err != nil {
			fatal("read catalog: %v", err)
		}
		if err := json.Unmarshal(b, &catalog); err != nil {
			fatal("parse catalog: %v", err)
		}
	}
	if *seed != "" {
		catalog.MasterSeed = *seed
	}
	sizing := runlist.Sizing{}
	if *sizingPath != "" {
		b, err := os.ReadFile(*sizingPath)
		if err != nil {
			fatal("read sizing: %v", err)
		}
		if err := json.Unmarshal(b, &sizing); err != nil {
			fatal("parse sizing: %v", err)
		}
	}

	rows, err := runlist.Generate(catalog, sizing)
	if err != nil {
		fatal("generate: %v", err)
	}
	clamped := catalog.ClampSizing(sizing)
	if dir := filepath.Dir(*out); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal("mkdir: %v", err)
		}
	}
	file, err := os.Create(*out)
	if err != nil {
		fatal("create output: %v", err)
	}
	defer file.Close()
	if err := runlist.WriteCSV(file, rows); err != nil {
		fatal("write: %v", err)
	}
	plan := catalog.PlannedRuns(clamped)
	fmt.Printf("generated %d rows (planned model %d runs, worst case %d, ceiling %d) -> %s\n",
		len(rows), plan, plan+runlist.ReplacementBudget, runlist.GlobalCeiling, *out)
}

func validate(args []string) {
	f := fs("validate")
	runlistPath := f.String("runlist", "runlist.csv", "run list CSV to validate")
	catalogPath := f.String("catalog", "", "optional cell catalog JSON (default: the R1.2e inventory)")
	reportPath := f.String("report", "", "optional JSON report path")
	_ = f.Parse(args)

	catalog := runlist.DefaultCatalog()
	if *catalogPath != "" {
		b, err := os.ReadFile(*catalogPath)
		if err != nil {
			fatal("read catalog: %v", err)
		}
		if err := json.Unmarshal(b, &catalog); err != nil {
			fatal("parse catalog: %v", err)
		}
	}
	file, err := os.Open(*runlistPath)
	if err != nil {
		fatal("open run list: %v", err)
	}
	defer file.Close()
	rows, err := runlist.ReadCSV(file)
	if err != nil {
		fatal("parse run list: %v", err)
	}
	rep := runlist.Validate(rows, catalog)
	if *reportPath != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*reportPath, append(b, '\n'), 0o644); err != nil {
			fatal("write report: %v", err)
		}
	}
	for _, e := range rep.Errors {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", e)
	}
	fmt.Printf("validated %d rows: planned %d (anchors %d), replacements %d, worst case %d, buffer %d, ceiling %d -> %s\n",
		rep.Total, rep.Planned, rep.D3, rep.Repl, rep.Worst, rep.Buffer, runlist.GlobalCeiling, verdict(rep))
	if !rep.OK {
		os.Exit(1)
	}
}

func verdict(rep *runlist.Findings) string {
	if rep.OK {
		return "PASS"
	}
	return "FAIL"
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "runlistctl: "+format+"\n", args...)
	os.Exit(1)
}
