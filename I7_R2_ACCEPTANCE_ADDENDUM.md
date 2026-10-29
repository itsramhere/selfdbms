# I7 R2 Engineering Acceptance Addendum — Blocker Closure and Verification

**DOCUMENT ID:** `I7_R2_ACCEPTANCE_ADDENDUM.md`
**STATUS:** DELIVERED — **STOPPED FOR INDEPENDENT REVIEW**
**BASELINE:** `I7_R2_ENGINEERING_CHECKPOINT.md` (unchanged; this addendum extends it)
**DATE:** 2026-10-29
**REVIEW INPUTS:** the R2 acceptance directive lists two review documents — `I7_R2_INDEPENDENT_REVIEW_DECISION.md` and `I7_R1_2E_FINAL_HANDOFF_REVIEW.md`. **Neither file exists in the repository, the review archives, or the git history.** The work below was executed from the directive text itself (R2 provisionally accepted; pilot and confirmatory experiments unauthorized; three tasks; execution restrictions). Reviewers must attach the decision documents independently; the acceptance ZIP therefore cannot contain their verbatim text.

---

## 1. Blocker closure summary

| # | Blocker | Resolution | Evidence |
| :--- | :--- | :--- | :--- |
| 1 | Reconcile `run_i7.ps1` with the validated run-list generator; remove conflicting legacy execution authority or fail closed | **Legacy execution authority revoked, fail-closed unconditionally.** Every mode that can launch a trial process (`-Calibrate`, `-Preflight`, `-Campaign`, `-Resume`, `-Diagnostics`) exits with code 5 before any approval evaluation, runner resolution, or process launch. No flag, approval file, or `-TestMode` bypass exists. `-Check`, `-DryRun` (spec/run-list inspection; launches nothing), `-WriteApprovalTemplate`, and `-Package` remain available. Ceiling constants updated to the R1.2e model (400 / 16) and the campaign manifest records `model_authority = legacy_inventory_366_REVOKED_by_R1_2e_execution_fails_closed`. | `run_i7.ps1::Assert-ExecutionRevoked`; orchestration scenarios S4–S10 |
| 2 | Ceiling covers every physical run (pilot, drift controls, retries, replacements, interrupted, aborted) | **Execution ledger** (`runlist/execution_ledger.go`): every STARTED record is a physical execution and counts against the 400 ceiling regardless of outcome (SUCCESS / FAILED / TIMEOUT / INVALID / INTERRUPTED / ABORTED, or STARTED-without-FINISHED after a driver crash). Rule S-1 bounds replacement+retry attempts at 16. `runlistctl ceiling` verifies a driver-style JSONL ledger; tests cover every category named by the directive. | `TestExecutionLedgerCountsEveryPhysicalRun`, `TestExecutionLedgerCeilingBreach`, `TestExecutionLedgerRuleS1Budget`, `TestExecutionLedgerIntegrity`, `TestExecutionLedgerWorstCaseComposition`, `TestExecutionLedgerCountsInterruptedProcess` |
| 3 | Race-enabled verification in a supported environment | Windows: **unavailable** — the race detector needs CGO and no C toolchain exists on the host (`CGO_ENABLED=0`, no `gcc`; also probed live by the driver's `-Check`). WSL2 (Ubuntu 24.04, WSL version 2): **available and used** — Go 1.27.3 + gcc 13.3. **Pass 1: the complete suite for every Linux-buildable package passes under `-race`** (`lsmengine`, `harness2`, `runlist`, `harness`, `corrective_validation` — exit 0), covering the engine read paths, the instrumentation hot paths, and the publication paths. `cmd/i7trial` is Windows-native (QPC/affinity build tags): verified by a `GOOS=windows` cross-compile from Linux; its unit tests run on Windows. Two real-time-window scenario tests and one late-completion test skip under `-short` in WSL2 because of a documented WSL2 scheduling artifact (a lone open-loop worker goroutine can be starved for the whole window — probe evidence in the race log); their full assertions pass on Windows and their conservation identities are outcome-mix-independent. | `test_evidence/race_verification_wsl2.log` (captured verbatim, incl. the artifact notes) |
| 4 | Instrumentation review: data races, observer-induced behavior changes, coverage, holder-count accuracy | (a) **Races:** the full engine + harness suite passes `-race` in WSL2, including the instrumentation hot paths. (b) **Behavior change:** `TestObserverTransparencyNoBehaviorChange` runs an identical 20-operation sequence per architecture with and without observers attached and asserts byte-identical observable behavior while verifying the observer was actually exercised. (c) **Coverage:** `TestHolderCounterDrainsAfterEveryOperation` asserts the holder counter drains to zero after every operation type that can take a read lock (Get hit/miss, LatestValue, FlushErr, ActiveSSTableCount, TriggerFlush, WaitForFlushCompletion, CompactAll, AcquireSnapshot/Release, Close) on all three architectures — a leaked RUnlock conversion would fail it. (d) **Count accuracy:** `TestHolderCounterExactUnderParkedReaders` asserts exact counting in the quiescent case; the pre-registered one-sided overcount bias (increment before RLock, decrement after RUnlock) is documented in the baseline checkpoint §6.3 and remains a statistical decision (D-2), not silently absorbed. Uncovered writer sites (TriggerFlush, checkAndTriggerFlush, SetMemtableThresholdBytes, Close, Approach B compaction's snapshotMu swap) remain explicitly documented as out of scope. | `lsmengine/sync_observer_audit_test.go`; WSL2 race log |
| 5 | Transaction-accounting conservation through cancellation, timeout, process interruption, late completion | Deterministic harness2 trials: open-loop overload (rate 100k/s, W_max = 2 ms) forces cancellations and queued arrivals — conservation holds and the abandonment path is provably exercised; a 25 ms window forces late completions — the outstanding-at-close identity holds exactly; process interruption is modeled by the execution ledger's unfinished STARTED records (counted). Cancellation (W_max abandonment), timeout (window close), and lateness identities are conserved by construction and verified. | `TestRunLedgerConservationUnderOpenLoopOverload`, `TestRunLedgerConservationUnderLateCompletion`, `TestExecutionLedgerCountsInterruptedProcess` |

Approval-binding coverage lost by the S4 revocation rewrite was restored at the correct layer: `TestCheckApprovalBindings` (cmd/i7trial) exercises campaign, protocol, gate, runner-hash, and block-set binding refusals directly against `checkApproval`.

## 2. What was NOT done (scope boundaries respected)

- No performance benchmark, EXP-3 microbenchmark execution, CPU-spin measurement, pilot calibration, confirmatory trial, or G4 resumption. The EXP-3 suite remains authored-only (`go test -run '^$' -bench '^$'` compile check only).
- The driver's executing path was **revoked**, not rebuilt: the R1.2e-runlist-driven execution redesign (mode→phase mapping, spec construction from the 12-cell catalog, runlistctl as the embedded generator) is the pilot gate's engineering work. The orchestration suite's stub-runner execution scenarios were converted to revocation assertions; their underlying semantics (approval binding, envelope validation, ledger conservation) are covered by Go unit tests and will be re-covered at the driver level post-rebuild.
- No historical evidence was modified; frozen datasets, hashes, and manifests untouched (verified by diff scope).

## 3. Verification matrix

All commands, raw outputs, and exit codes are captured in `test_evidence/TEST_COMMANDS_AND_OUTPUTS.txt` (generated mechanically; not paraphrased).

| Check | Environment | Result |
| :--- | :--- | :--- |
| `go build ./...` + `go vet ./...` | Windows (Go 1.26.3) | PASS (exit 0) |
| Full unit suite, 6 packages | Windows | PASS (exit 0) |
| Orchestration suite (`orchestration_tests.ps1`) | Windows PowerShell | **PASS (exit 0; 27/27 scenarios incl. S11 runlistctl authority)** |
| Full unit suite with `-race` (5 packages) | WSL2 Ubuntu 24.04 (Go 1.27.3, gcc 13.3) | **PASS (exit 0)** |
| `i7trial` `GOOS=windows` cross-compile from WSL2 | WSL2 | PASS |
| R1.2e statistical artifacts | Python 3.13.7 / numpy 2.5.1 / scipy 1.18.0 | byte-identical reproduction (hash `4DCE7454…E37D1`) |
| Run-list model N=8 / N=10 validation | runlistctl | PASS (316 / 388 worst case ≤ 400; buffer 84 / 12) |
| Execution-ledger ceiling (7-run mixed-kind ledger; 407-run breach) | runlistctl ceiling | PASS / **refused (exit 1)** |
| Legacy execution authority (-Campaign, -Calibrate) | run_i7.ps1 via -File | **exit 5 both modes, refusal precedes approval evaluation** |

## 4. Statistical decision preparation

`R2_STATISTICAL_DECISION_PREPARATION.md` (included in the ZIP) provides the concise recommendation: **retain the absolute write-rate primary estimand**, size it from per-cell pilot observations of $\hat\sigma_{\Delta\dot W}$ in writes/s (one-sided paired Student-$t$, $df = N-2$, margin unchanged at 25.0/26.5 writes/s), retain the pilot (internal, first 4 triplets per cell), treat the ceiling as a hard epistemic bound (failure to reject under capped power is pre-registered inconclusive), and route decisions D-1…D-6 to the reviewer. No margin or sample size was changed and the protocol is explicitly **not** declared frozen.
