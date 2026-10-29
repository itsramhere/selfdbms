# R2 Acceptance Package — Independent Audit Guide

**PACKAGE:** `I7_R2_ACCEPTANCE_REVIEW.zip`
**HOW TO AUDIT:** every claim below is re-derivable from the files in this ZIP plus a clone of the branch. Nothing relies on the agent's summary.

## 0. What this package contains

```
MANIFEST.sha256                  manifest of every file in this ZIP (with sizes)
AUDIT_GUIDE.md                   this document
I7_R2_ENGINEERING_CHECKPOINT.md  R2 baseline checkpoint (blocker closure builds on it)
I7_R2_ACCEPTANCE_ADDENDUM.md     blocker 1-5 closure matrix and verification matrix
I7_R2_PROVENANCE_MANIFEST.sha256 provenance manifest for the R2 engineering checkpoint
R1_2E_ESTIMAND_SCALE_MISMATCH_NOTICE.md   log-ratio vs. absolute-estimand analysis
R2_STATISTICAL_DECISION_PREPARATION.md    Task-3 recommendation (absolute estimand, pilot sizing)
GIT_REVISION.txt                 branch, HEAD, engineering diff base, commit chain
SOURCE_TREE.sha256               SHA-256 of every tracked source file at HEAD (via git show)
diff/r2_engineering.diff         full engineering diff vs. the preservation commit 27a041e
diff/r2_engineering.diffstat.txt diffstat of the same
_sources/                        the instrumentation, ledger, run-list, and runner files
test_evidence/TEST_COMMANDS_AND_OUTPUTS.txt   commands + raw outputs + exit codes (Windows)
test_evidence/race_verification_wsl2.log      verbatim race-enabled run (WSL2) + notes
test_evidence/capture_windows_evidence.sh     the script that generated the evidence file
test_evidence/orchestration_tests_run3.log    driver orchestration suite, 27/27, exit 0
test_evidence/orchestration_results.json      machine-readable scenario results
runlist_validation/              synthetic run lists (N=8, N=10) + validator reports
```

## 1. Verify the revision and the source tree

```
git clone <repo> && git checkout <HEAD from GIT_REVISION.txt>
sha256sum -c SOURCE_TREE.sha256          # every tracked file matches HEAD
```
`SOURCE_TREE.sha256` was produced by `git show HEAD:<file> | sha256sum` per tracked file, so it verifies the COMMITTED tree independent of any working-tree state.

## 2. Verify the engineering diff

```
git diff 27a041e HEAD --stat             # compare with diff/r2_engineering.diffstat.txt
git diff 27a041e HEAD                    # compare with diff/r2_engineering.diff
```
The diff base `27a041e` is the R1.x preservation commit; the frozen engine revision below it is `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a` (never modified).

## 3. Re-run the verification (Windows host)

```
go build ./... && go vet ./...
go test ./lsmengine/ ./research/experiments/phase_i7/harness2/ ./research/experiments/phase_i7/runlist/ ./research/experiments/phase_i7/cmd/i7trial/ ./research/experiments/harness/ ./research/experiments/corrective_validation/ -count=1
go run ./research/experiments/phase_i7/cmd/runlistctl generate -out rl.csv -seed 20261101
go run ./research/experiments/phase_i7/cmd/runlistctl validate -runlist rl.csv -report v.json
go run ./research/experiments/phase_i7/cmd/runlistctl ceiling -ledger <any STARTED/FINISHED jsonl>
powershell -File research/experiments/phase_i7/run_i7.ps1 -Root <tmp> -Campaign   # must exit 5
powershell -File research/experiments/phase_i7/tests/orchestration_tests.ps1      # 27/27, exit 0
```
Expected results are exactly the ones recorded in `test_evidence/TEST_COMMANDS_AND_OUTPUTS.txt` (verbatim, with exit codes) and `orchestration_tests_run3.log`.

## 4. Re-run the race-enabled verification (WSL2 or any CGO-equipped Linux)

```
wsl -e bash -c "cd ~/i7race && CGO_ENABLED=1 go test -short ./lsmengine/ ./research/experiments/phase_i7/harness2/ ./research/experiments/phase_i7/runlist/ ./research/experiments/harness/ ./research/experiments/corrective_validation/ -count=1 -race"
```
Windows cannot run `-race` (no C toolchain — probed live by the driver's `-Check`, which reports `race detector NOT available`). Expected: all packages `ok` (see `race_verification_wsl2.log` pass 1). The log's NOTES section documents the WSL2 worker-starvation artifact that affects three real-time-window scenario tests; those tests' full assertions pass on Windows and their conservation identities are outcome-mix-independent.

## 5. Verify the ceiling model by hand

From `runlist/runlist.go` constants: 12 cells × N∈[8,10] × 3 arms + 4 D3 anchor triplets × 3 = 300..372 planned; + Rule S-1 budget 16 → worst case 388 ≤ 400 strict ceiling; ≥ 12 unallocated. The execution ledger (`runlist/execution_ledger.go`) counts every STARTED process regardless of outcome; the breach demo in the evidence file shows 407 executions refused with exit 1.

## 6. Verify the statistical artifacts independently

`R2_STATISTICAL_DECISION_PREPARATION.md` changes no registered value: the margin (25.0 / 26.5 writes/s), the confirmatory bounds (N ∈ [8,10]), and the df = N−2 adjustment are carried over unchanged from `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md`; the recommendation is to size from per-cell pilot estimates of σ_ΔẆ on the absolute scale. `simulate_power_r1_2e.py` reproduces its results JSON byte-identically (hash in the evidence file). Decision items D-1…D-6 remain open for the reviewer; the protocol is explicitly **not** frozen.

## 7. Known limitations (stated, not hidden)

- The two review decision documents named by the directive (`I7_R2_INDEPENDENT_REVIEW_DECISION.md`, `I7_R1_2E_FINAL_HANDOFF_REVIEW.md`) are not present in the repository; this package was produced from the directive text. Reviewers should attach them independently.
- The legacy driver execution path is REVOKED (exit 5), not rebuilt; rebuilding it around runlistctl is the pilot gate's work (addendum §2).
- No performance measurement of any kind was executed: the EXP-3 benchmark suite is compile-verified only (`go test -run '^$' -bench '^$'`).
