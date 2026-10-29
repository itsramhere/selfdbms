# Phase I7 — R2 Engineering Checkpoint: Infrastructure and Correctness Verification

**DOCUMENT ID:** `I7_R2_ENGINEERING_CHECKPOINT.md`
**STATUS:** ENGINEERING CORRECTNESS VERIFICATION DELIVERED — **STOPPED FOR INDEPENDENT REVIEW**
**BRANCH:** `research/i7-engineering-preflight` (created from the frozen engine revision `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`)
**HEAD AT DELIVERY:** see §4 (final commit)
**PROVENANCE MANIFEST:** `I7_R2_PROVENANCE_MANIFEST.sha256`
**DATE:** 2026-10-29

---

## 0. Compliance summary

| Directive | Status |
| :--- | :--- |
| New research branch from frozen engine revision; G4 data/hashes/manifests preserved | **Done** — no historical dataset, hash, or manifest touched (§3) |
| D3 runner-dispatch correction implemented and unit-tested | **Done** (§5.1) |
| Structured transaction accounting (offered/attempted/in-window/late/failed/queued/outstanding), conserved | **Done** (§5.2) |
| Non-invasive instrumentation (read CS, writer wait, publication, holders) + documented limitations | **Done** (§6) |
| Deterministic tests for instrumentation and error paths; three architectures verified | **Done** (§7) |
| Run-list generation/validation with strict 400 ceiling, drift controls, replacement budget, synthetic records only | **Done** (§8) |
| EXP-3 metadata microbenchmark harness authored, not executed | **Done** (§5.4) |
| Only functional unit tests, build checks, race tests where supported | **Done** — race detector **unsupported on this host** (§7.3) |
| Statistical tasks: deliver R1.2e artifacts, identify log-ratio vs. absolute-estimand mismatch, no silent changes | **Done** (§9) |
| No pilot calibration, CPU-spin measurements, Go benchmark execution, throughput/latency campaigns, G4 resumption, G5 | **Respected** — zero executions |
| No production engine behavior modification to improve benchmark results | **Respected** — instrumentation is observer-gated and zero-cost by default (§6.1) |

**Note on inputs:** the referenced handoff document `I7_R1_2E_FINAL_HANDOFF_REVIEW.md` is **not present** in the repository (root, archives, or git history). This checkpoint was executed from `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md` (verified SHA-256 `37725BA4…7584`) plus the authorization text of the review decision. If the handoff review imposes additional constraints, this checkpoint must be re-audited against it.

---

## 1. Source diff

Engineering work is four commits on top of the preservation commit. Full diff vs. the pre-engineering state (`27a041e`): **30 files, +3076/−45**, plus the checkpoint artifacts.

| Commit | Content |
| :--- | :--- |
| `27a041e` | *(preservation)* R1.1/R1.2 external-review artifacts, revised manuscript, R1.2e statistical deliverables — committed unmodified so the engineering diff is reviewable in isolation |
| `a0d0a2e` | *(preservation addendum)* R1.1 reviewer-response paper artifacts |
| `3427642` | Engine instrumentation (`lsmengine/sync_instrumentation.go` + wiring), conserved transaction ledger + `SyncCollector` (`harness2/ledger.go`, `harness2/syncinstr.go`, `run.go`, `spec.go`, `events.go`), D3 dispatch (`cmd/i7trial/main.go`), and their tests |
| `067af86` | Run-list model (`phase_i7/runlist/`, `cmd/runlistctl/`) and EXP-3 metadata benchmark suite (`lsmengine/metadata_bench_test.go`) |

Key files (new): `lsmengine/sync_instrumentation.go`, `lsmengine/sync_instrumentation_test.go`, `lsmengine/metadata_bench_test.go`, `harness2/ledger.go`, `harness2/ledger_test.go`, `harness2/syncinstr.go`, `runlist/runlist.go`, `runlist/validate.go`, `runlist/runlist_test.go`, `cmd/runlistctl/main.go`, `cmd/i7trial/main_test.go`.
Key files (modified): `lsmengine/manager.go`, `snapshot.go`, `transaction.go`, `compaction.go`, `flush_observer.go`, `flush_publication_overlap_test.go`; `harness2/spec.go`, `run.go`, `events.go`, `clock.go`; `harness/flush_tracker.go`; `corrective_validation/t1_deterministic_sync_test.go`; `cmd/i7trial/main.go`.

## 2. Statistical deliverables (delivered unchanged, reproduced)

| Artifact | SHA-256 (verified) | Reproduction |
| :--- | :--- | :--- |
| `research/experiments/analysis/simulate_power_r1_2e.py` | `DBCD469E9B6860E573F039716FAF2EDC92329B86D5D2351379534C49A4D387E3` | matches R1.2e §7 manifest |
| `research/experiments/analysis/r1_2e_power_simulation_results.json` | `4DCE74545F427752232FD1360BD2EB3E1F8F212D78B2058F7646AFC1902E37D1` | **byte-identical re-run** on this host (Python 3.13.7, numpy 2.5.1, scipy 1.18.0): hash before == hash after == manifest |
| `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md` | `37725BA47AE712CCDBDF8CC6DC7497FA780D5C419A8831CF54876F5BC2E27584` | committed on this branch |

## 3. Frozen-state preservation

No file under any historical G4/G3 results directory, no hash, and no manifest was modified. Verified by `git diff f4cce4b HEAD --stat` scope: the only pre-`I7` paths touched by engineering commits are `lsmengine/` and `research/experiments/{harness,phase_i7}` sources — no dataset, ledger, calibration, or provenance file. Historical branches (`main`, `research/i6.9-corrective-preflight`, `research/approach-b-snapshots`, `research/reader-compactor-sync`) are untouched; the branch was created at `f4cce4b`.

## 4. Git state at delivery

```text
branch: research/i7-engineering-preflight (from f4cce4b5faa80924c7ef945139ef1a65ccb2b32a)
27a041e docs(i7-r1): preserve R1.x external-review artifacts ...
a0d0a2e docs(i7-r1): addendum — R1.1 reviewer-response paper artifacts ...
3427642 feat(i7-preflight): non-invasive sync instrumentation, conserved transaction ledger, and D3 dispatch
067af86 feat(i7-preflight): R1.2e run-list model and EXP-3 metadata microbenchmark suite
f34b3a1 docs(i7-r2): deliver R2 engineering checkpoint, estimand mismatch notice, run-list validation artifacts
```

The checkpoint text itself and `I7_R2_PROVENANCE_MANIFEST.sha256` land in the terminal commit(s) of this branch; the manifest hashes every delivered artifact and is committed last by construction, so its own digest lives in the git history. The git chain is the authoritative record.

## 5. Implemented infrastructure

### 5.1 D3 runner-dispatch correction
`cmd/i7trial/main.go`: `"D3"` added to `supportedBlocks` (previously absent — a runlist containing drift-anchor rows failed closed with `exitUnsupported`). D3 resolves to `ModeClosedLoop` (already supported in `harness2.ResolveMode`). `main_test.go` locks the dispatch surface: `TestD3BlockIsDispatchable`, `TestSupportedBlocksComplete` (E7 remains pebble_runner-owned), and envelope-validation error paths. **Scope note:** D3 anchor rows execute the C2-01 zero-delay cell as plain closed-loop trials and are dispatchable today; the EXP-2 causal cells (C2-02…C2-06) are **not** dispatchable yet because reader-delay spec fields do not exist — see §10 (open item O-1).

### 5.2 Structured transaction accounting (`harness2/ledger.go`)
Lifecycle: `offered → {queued, abandoned, attempted → {completed_in_window, completed_late, failed}}`. Conservation identities checked at the end of every trial, failing the trial on violation:
- **C1 terminal:** `attempted == in_window + late + failed`
- **C2 arrival:** `offered == attempted + abandoned + queued` (closed-loop classes: `offered == attempted`, `abandoned == queued == 0`)
- **C3 outstanding:** `outstanding_at_window_close == attempted − in_window − failed == completed_late` (every request outstanding at close finishes late in the drain; post-close failures are trial-fatal and cannot distort the ledger)
"Queued" is the category the frozen schema calls *unissued*; `OperationCounts` keeps `ReadOpsUnissued` for schema compatibility while the new `TrialResult.transaction_ledger` uses `queued`. `TestRunLedgerConservedAllModes` verifies conservation and cross-agreement with the legacy counters in all four driver modes (closed-loop, open-loop, paced, publication-only); `TestLedgerValidateRejectsViolations` covers every broken identity (error paths).

### 5.3 Run-list model (`phase_i7/runlist` + `cmd/runlistctl`)
Generation per R1.2e §4.2: 12 confirmatory cells (EXP-1 ×4, EXP-2 ×6, EXP-4 ×2) with per-cell confirmatory triplets clamped to [8,10]; balanced Latin-square arm ordering permuted by a SHA-256-derived PRNG keyed on the master seed; four D3 drift-anchor triplets spliced at the pre-registered milestones (D3-01 opening; D3-02 immediately post-EXP-1; D3-03 immediately after C2-01…C2-03; D3-04 closing). Validation enforces: strict 400-run global ceiling; planned ≤ 372; worst case ≤ 388 (guaranteeing ≥ 12 unallocated runs); Rule S-1 replacement budget ≤ 16 with orphan detection; triplet integrity (exactly 3 arms, balanced permutation, single cell); anchor identity and milestone placement; per-cell triplet bounds; unknown-cell and EXP-3-row rejection (EXP-3 contributes zero disk rows). **Synthetic records only** — the package and CLI execute nothing.

### 5.4 EXP-3 metadata microbenchmark harness (`lsmengine/metadata_bench_test.go`)
Authored `testing.B` suite for in-memory metadata cardinality profiling: memtable point reads (hit/miss) at 10³/10⁵/10⁶; A-min read critical section; B snapshot pin/release; frozen-memtable scan depth (1/4/16, installed without flushes so no SSTable exists); the active-holder counter pair cost (with/without); concurrent-reader contention (r ∈ {1,4}) per architecture. **Compile-verified only** (`go test -run '^$' -bench '^$'` passes); **not executed** — benchmark execution is a performance measurement reserved for the next gate.

## 6. Instrumentation semantics (`lsmengine/sync_instrumentation.go`)

### 6.1 Non-invasiveness contract
- `SyncObserver` is nil unless attached via `SetSyncObserver(obs, clock)`; every instrumented site costs exactly one nil check. `SetSyncObserver` **fails closed** on a nil clock: durations can never silently degrade to a coarse wall clock. The harness injects the QPC clock (`harness2/clock.go::qpcElapsedNanos`), placing engine timestamps on the same monotonic timeline as the trial window.
- The single exception is `activeRLockHolders` (atomic.Int32), maintained **unconditionally** on every `Manager.Mu` read per the pre-registered R1.2e §3.2 sketch — one uncontended atomic add per read-side lock/unlock pair. Its marginal cost is itself benchmarkable (`BenchmarkHolderCounterPair`) without executing it now.
- All read paths (A `Get`, A-min `getApproachAMin`, B `getApproachB`, plus diagnostic RLock holders) enter/exit exclusively through `rLock` / `rUnlock` / `rUnlockObserved`; direct `Mu.RUnlock()` calls no longer exist on read paths, so the counter cannot drift.

### 6.2 What is observed
- **Read critical-section duration** per architecture: A = the entire read; A-min = the memory-tier probe + lease-capture window (SSTable scan runs unlocked); B = memory probe + snapshot pin (disk scan runs unlocked).
- **Writer lock-acquisition wait** at `Transaction.Commit`, the flush publication, and the A/A-min compaction publication: interval from the pre-lock arrival sample to completed acquisition (includes the uncontended fast path, so waits have a timer-overhead floor of roughly two clock reads).
- **Publication duration:** the strict exclusive hold of the flush/compaction publication section (measured inside the lock, immediately before release). Distinct from the harness `EventRecord.HoldNs`, which spans observer-begin (pre-lock) to observer-end (in-lock) and therefore includes the lock wait.
- **Active lock holders:** sampled immediately before each publication `Mu.Lock()` and delivered through `FlushObserver.OnFlushPublishBegin(activeRLockHolders int)` (interface updated; all four implementations — harness `FlushTracker`, harness2 `EventRecorder`, and two test observers — updated). Harness records land in `EventRecord.active_readers_at_arrival`. When a `SyncObserver` is attached, wait/publication observations carry the same arrival sample.

### 6.3 Timestamp and synchronization limitations (as required by the authorization)
1. **One-sided holder bias.** Per the pre-registered sketch, the counter increments **before** `Mu.RLock()` and decrements **after** `Mu.RUnlock()`. At any instant `counter ≥ true holders`: readers that have incremented but not yet acquired, and readers that have released the RWMutex but not yet decremented, are still counted. The sample can overcount by up to (in-flight acquisitions + in-flight releases); it can never undercount. Statistical consequence for EXP-2: the sampled $k$ is a noisy, upward-biased regressor of the true blocking set; the TOST equivalence bounds on $\beta$ must absorb this attenuation bias — flagged as statistical decision D-2 (§9.2), not silently absorbed.
2. **No linearization with the lock.** Between a writer's `Load()` of the counter and its `Mu.Lock()`, the holder set may change arbitrarily. The protocol's semantics is "sampled at arrival", not "the set that blocked the writer".
3. **Scope.** `activeRLockHolders` covers `Manager.Mu` only. Approach B's `snapshotMu` pin count and A-min's per-table lease counts are separate mechanisms and are not part of the holder sample. Approach B compaction publishes through `snapshotMu` and is deliberately not instrumented as a publication site.
4. **Uncovered writer sites.** `TriggerFlush`, `checkAndTriggerFlush`, `SetMemtableThresholdBytes`, `Close`, and the compaction admission section take `Mu.Lock()` without wait observation (documented coverage boundary; the three primary contention sites are covered).
5. **Clock.** Durations require the injected monotonic clock (fail-closed otherwise). QPC-derived engine timestamps are monotonic on one timeline with the harness window; absolute epoch alignment is not attempted and not needed. Callbacks run on hot-path goroutines and must be non-blocking; the harness collector (`harness2/syncinstr.go`) is lock-free (atomics + CAS min/max) and allocation-free.
6. **Aggregation scope.** The collector aggregates the whole trial lifecycle, including fixture construction and the post-window drain; it reports count/mean/min/max and the mean arrival holder sample, not online percentiles (a bounded buffer would drop or lock).

## 7. Tests executed and outcomes

### 7.1 Functional unit tests (all pass, `-count=1`)
| Package | Result | Notes |
| :--- | :--- | :--- |
| `owndbms/lsmengine` | **ok** (24.1s) | Full engine suite: A / A-min / B correctness, MVCC, compaction concurrency, cleanup coordination, flush shutdown, lifetime guarantees — i.e., the three architectures retain their existing correctness and lifetime guarantees on top of the instrumentation |
| `…/phase_i7/harness2` | **ok** (8.4s) | Existing harness suite + new ledger/instrumentation tests |
| `…/phase_i7/runlist` | **ok** (0.9s) | Generation determinism, budget model, validation accept/reject paths |
| `…/phase_i7/cmd/i7trial` | **ok** (0.9s) | D3 dispatch, dispatch surface, envelope error paths |
| `…/experiments/harness` | **ok** (68.5s) | Frozen harness incl. updated `FlushTracker` |
| `…/experiments/corrective_validation` | **ok** (15.3s) | Updated deterministic publication observer |

225 test functions report `--- PASS` across the four packages directly touched by the engineering commits; the balance are the pre-existing engine/harness suites.

New deterministic tests added (highlights): `TestActiveRLockHoldersAllModes` (holders tracked to 4 and back to 0 in all three architectures), `TestReadCriticalSectionObservedAllModes` and `TestWriterWaitAndPublicationObservedAllModes` (exact synthetic-clock durations, no wall-clock assertions), `TestPublicationArrivalSampleCountsBlockedReaders` (the R1.2e §3.2 arrival sample with a parked reader — staged via `beforeFlushPublishHook` so the reader holds `Mu` only when the flush worker arrives at publication), `TestCompactionPublicationObserved`, `TestHolderCounterNeverNegative`, `TestSetSyncObserverRequiresClock`, `TestRunLedgerConservedAllModes`, `TestLedgerValidateRejectsViolations`, `TestValidateAcceptsWellFormedRunlist`, `TestValidateRejectsViolations` (7 corruptions), `TestD3BlockIsDispatchable`.

### 7.2 Build checks
`go build ./...`, `go vet ./...` clean; `go test -run '^$' -bench '^$'` compiles the EXP-3 benchmark suite without executing it.

### 7.3 Race tests — not supported on this host
`go test -race` requires CGO and a C toolchain (`gcc`); this Windows host has neither configured (`CGO_ENABLED=0`, no `gcc` in PATH). The race run therefore could **not** be executed here and is recorded as an **open verification item (O-2, §10)**: the same suite must be run with `-race` on a toolchain-equipped host before any pilot authorization. The deterministic engine tests still exercise concurrent interleavings (parked-reader publication, 4-reader holder counting, concurrent read/write drain), but that is not a substitute for the race detector.

## 8. Run-list validation results (synthetic records only)

Artifacts committed under `research/experiments/phase_i7/runlist_validation/`:

| Run list | Rows | Planned (anchors) | Worst case (planned + S-1 budget 16) | Buffer | Verdict |
| :--- | ---: | ---: | ---: | ---: | :--- |
| `runlist_N8.csv` / `validation_N8.json` | 300 | 300 (12) | 316 | 84 | **PASS** |
| `runlist_N10.csv` / `validation_N10.json` | 372 | 372 (12) | 388 | 12 | **PASS** |

Both bounds reproduce the R1.2e §5 ledger exactly (minimum 316 = 288+12+16; maximum 388 = 360+12+16 ≤ 400 ceiling). Fail-closed behavior demonstrated: a synthetic list augmented with a 17th replacement row is rejected with `replacement attempts 17 exceed the Rule S-1 budget 16` plus misplaced-replacement findings, exit code 1.

## 9. Statistical tasks

### 9.1 Delivered for independent inspection
The three R1.2e artifacts (§2) are committed on this branch with verified hashes, and the results JSON was reproduced byte-identically on this host. Nothing in them was altered.

### 9.2 Identified mismatch — log-ratio sizing vs. absolute write-rate estimand
**The sample-size factor 884.14 (with margin $\ln 0.90$ and dispersions $\sigma_d \in [0.06,0.25]$) is derived on the log-ratio scale, but R1.2e §2.3 declares the primary confirmatory estimand to be the absolute paired write-rate difference $\Delta\dot W$ with margin 25.0/26.5 writes/s. These are different tests; the sizing mathematics do not transfer** (the absolute-scale variance depends on the baseline level and its covariance with the log-ratio; the §1.4 power figures 95.6/58.7/30.8/18.2% are log-ratio figures and must not be cited as power for the primary estimand; the $N\in[8,10]$ clamp is statistically unquantified on the absolute scale pending pilot estimates of $\sigma_{\Delta\dot W}$). Full analysis with affected/unaffected component table and non-binding decision options: **`R1_2E_ESTIMAND_SCALE_MISMATCH_NOTICE.md`**.

**Compliance:** no margin was changed, no confirmatory sample size was altered, no claim of validated adaptive inference was made, and no frozen dataset was touched.

## 10. Open items for the next authorization gate

- **O-1 (engineering):** EXP-2 causal cells (C2-02…C2-06) require reader-delay spec fields (`T_in`/`T_out`, incl. the C2-06 outside-lock CPU spin) and a dispatch extension (`E2C` block label is reserved by the run-list model; rows are emitted `runner_support=preflight_pending` and fail closed if executed today). Design exists (R1.2e §3.1); implementation is ready to author at the next gate.
- **O-2 (verification):** `-race` suite run on a CGO-equipped host (§7.3).
- **O-3 (integration):** `run_i7.ps1` constants predate R1.2e (`PlannedTrials=366, HardCeiling=384, ReplacementCap=18`); the driver must be reconciled to consume the `runlist` package's model (planned 300–372, ceiling 400, budget 16) before any campaign. Deliberately not patched in place in this checkpoint to avoid an untestable PowerShell change.
- **O-4 (statistical):** the estimand-scale decision (D-1) and the holder-sample bias treatment for the EXP-2 TOST (D-2) — both in `R1_2E_ESTIMAND_SCALE_MISMATCH_NOTICE.md`; the pilot gate is the earliest point they can be resolved with data.
- **O-5 (execution):** EXP-3 benchmarks authored, compile-verified, not executed; pilot calibration, CPU-spin measurements, throughput/latency campaigns, G4 resumption, and G5 remain excluded.

---

**STATUS: ENGINEERING CHECKPOINT COMPLETE. WORK STOPPED AFTER CORRECTNESS VERIFICATION.**
No performance measurement was executed anywhere in this checkpoint. The next authorization gate decides whether controlled pilot calibration may begin.
