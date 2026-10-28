# 07 — Preregistered Analysis Plan and Stopping Rule

**Status:** PREREGISTRATION. Frozen before G4. No outcome data exist. **Windows-amended 2026-10-28: no changes to estimands, margins, tests or multiplicity.**
**Companion:** [06](06_TRIAL_INVENTORY.md) (inventory), [04](04_OFFERED_LOAD_METHODOLOGY.md) (metrics), [10](10_CAUSAL_VS_E2E.md) (claim types).

> **Windows amendment.** The analysis plan is platform-agnostic and is **unchanged**. Three operational notes:
> 1. Every cell is a Windows/NTFS cell; no pooling with the frozen WSL2 campaigns, and no cross-platform contrast ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §1.1).
> 2. The stopping rules gain nothing new, but S-6 (drift) now also covers thermal throttling on a laptop: the D3 start/end triplets are the instrument, and an arm-independent >15 % shift still aborts.
> 3. E4's response latencies are only valid if the open-loop driver uses QPC ([04](04_OFFERED_LOAD_METHODOLOGY.md)); a `time.Now`-based implementation invalidates P4 by construction and must be rejected at review.

---

## 1. Principles

1. **Estimands before data.** Every contrast below is named, paired, and assigned a decision rule before any G4 trial runs.
2. **Paired by construction.** Arms within a triplet share seed, arrival schedule and key stream. The analysis never pools unpaired trials as if they were paired.
3. **Effect sizes with intervals, always.** No decision is reported as a bare p-value. Every table cell carries a point estimate and a 95 % interval.
4. **No outcome-based stopping or adaptation.** N, margins and tests are fixed. Precision is reported after the fact.
5. **Exploratory is labelled.** Any analysis not listed in §4 is exploratory and is presented as such, with no α control and no manuscript claim beyond "observed".
6. **Mechanical exclusions only.** §8 of [06](06_TRIAL_INVENTORY.md), applied by a script that cannot read the outcome of the trial it excludes.

---

## 2. Data Preparation (in this order, before any test)

1. Load all trial JSONs and the campaign manifest; verify `runlist` hash, `provenance`, `platform_manifest`.
2. Apply exclusion rules X-1…X-12 mechanically; write `exclusions.csv` with IDs and reasons; publish before analysis.
3. Build triplets: keep a triplet only if all three arms are valid. Count dropped triplets per cell.
4. For each trial compute derived metrics: total ops/s, `k` (mean/max reader-set size), `records_scanned`, `stall_time_share`, event-conditioned statistics, `achieved/offered`, `abandon_rate`.
5. Verify the manipulation checks of §3.6.
6. Only then compute contrasts.

No trial is dropped after step 2. No arm is dropped. If a cell has fewer than the planned triplets, it is reported with its actual N.

---

## 3. Estimands

Notation: for triplet `t` and quantity `X` (e.g. completed writes), `d_t = ln X_{t,a1} − ln X_{t,a2}`. The reported effect is `exp(median_t d_t)` (ratio of geometric means) with a BCa bootstrap 95 % CI over triplets (10,000 resamples). Secondary summaries use the ratio of medians.

### 3.1 P1 — Does A-min capture B's writer-progress advantage? (E1, `r=8` primary; `r=1` secondary)

| ID | Estimand | Decision rule |
| :--- | :--- | :--- |
| **P1a** | `R_w(A-min / A)` = ratio of completed write operations | **Supported** if the 95 % CI lower bound > 1 and the point estimate ≥ 3. **Refuted** if the CI upper bound < 1.5. Otherwise **inconclusive**. (Superiority, two-sided α=0.05.) |
| **P1b** | Capture fraction `CF_w = (W_amin − W_A) / (W_B − W_A)` per triplet, only defined when `W_B − W_A > 0.2·W_A` | **Supported** if `median CF_w ≥ 0.7` and the CI lower bound ≥ 0.5. **Refuted** if `median CF_w ≤ 0.3`. Otherwise inconclusive. Triplets with undefined CF are reported as counts. |
| **P1c** | `R_w(A-min / B)` | **Non-inferiority supported** if the 95 % CI lower bound > **0.80** (one-sided α=0.05). Otherwise A-min is materially worse than B on write progress. |
| **P1d** | `R_r(A-min / A)` and `R_r(A-min / B)` for read throughput | Secondary. **Supported** if `R_r(A-min/A) ≥ 1.5` and the NI bound `R_r(A-min/B) > 0.90` holds. |

Interpretation map: P1b ≥ 0.7 with P1c holding ⇒ *versioning is not required for writer progress*; the manuscript's causal claim must be rewritten. P1a failing ⇒ *versioning matters*; the existing claim survives and A-min is reported as the null control.

### 3.2 P2 — Does A-min capture B's publication-tail advantage? (E2, `r=4`)

| ID | Estimand | Decision rule |
| :--- | :--- | :--- |
| **P2a** | `L(A-min / A)` = ratio of **event-conditioned p95** response latency | **Supported** if the CI upper bound < 0.5 (A-min at least halves the blocking). |
| **P2b** | `L(A-min / B)` | **Non-inferiority supported** if the CI upper bound < **1.15**. |
| **P2c** | `block_ratio = cond_max / hold` per event | Descriptive; expected ≈ 1 for A, ≈ 0 for A-min/B. Reported with per-arm medians and IQR. |
| **P2d** | `stall_time_share` in the `physical` mode | Secondary; same directional expectation as P2a. |

Auxiliary requirement: P2 is only evaluated if **realized publication count = 20** in every included trial (X-6). This is the defect that invalidated the frozen B2 comparison ([03](03_PACING_AUDIT.md) §4.2).

### 3.3 P3 — Is Workload D's penalty snapshot-specific? (E3)

| ID | Estimand | Decision rule |
| :--- | :--- | :--- |
| **MC-3** | Manipulation check: completed writes in the equal-work regime differ across arms by ≤ 10 % and are within 15 % of `W_paced × 10 s` | If it fails, **P3 is void** (the equal-work manipulation did not hold) and only the free regime is reported. |
| **P3a** | Free regime, `r=4`: `R_r(B/A)` = ratio of read throughput | **Replication supported** if the CI upper bound < 0.95 (penalty reproduced on the new platform). |
| **P3b** | Equal-work regime, `r=4`: `R_r(B/A)` | **No-penalty supported** if the CI lower bound > **0.90**. **Penalty persists** if the CI upper bound < 0.95. Otherwise inconclusive. |
| **P3c** | Regime interaction: `Δ = ln R_r(free) − ln R_r(equal)` | **Penalty is regime-dependent** if the bootstrap 95 % CI for Δ excludes 0 and |Δ| ≥ 0.5 · |ln R_r(free)| (i.e. the work-matching removes at least half the observed penalty). |
| **P3d** | Total-op throughput ratio `R_tot(B/A)` in the free regime | Descriptive, reported alongside `R_r` to expose the composition shift (frozen: 0.84/0.79/0.81 vs read ratios 0.63/0.66/0.74). |
| **P3e** | Median read latency ratio in the free regime at `r=1` | Secondary; supports the lock-sharing hypothesis if the A-min arm tracks B rather than A. |

Auxiliary: `k`, flush count and realized write count are reported for every E3 trial, because the frozen comparison differed on all three ([03](03_PACING_AUDIT.md) §5).

### 3.4 P4 — What does the latency picture look like at matched offered load? (E4)

| ID | Estimand | Decision rule |
| :--- | :--- | :--- |
| **MC-4** | `achieved/offered ≥ 0.98` at L1/L2; abandon rate ≤ 2 % at L1/L2 | Else that cell is excluded (X-7/X-8). |
| **P4a** | At L2 (`0.60·C_ref`): `Y99(A-min/A)` and `Y99(B/A)`, response latency | **Supported** if both CI upper bounds < 0.5. |
| **P4b** | At L3 (`0.90·C_ref`): abandon rate per arm | Descriptive; tests whether A reaches overload earlier. Directional expectation: A > A-min ≈ B. Reported without α (exploratory). |
| **P4c** | Coordinated-omission inflation: ratio of the closed-loop E2 p95 ratio to the E4 response-p99 ratio, at the closest matching condition | Descriptive; no test. Reported as "the closed-loop estimate understates / overstates the load-controlled effect by ×k". |
| **P4d** | `q_mean` per arm at L1 | Validity: if `q_mean > 1 ms` at L1 the calibration was wrong; the cell is reported but not used for the knee. |

### 3.5 P5 — Does the effect scale with the read critical section? (E5 vs E1 `r=8`)

E5 manipulates **only** the work inside the read critical section: `TargetReadHitRatio = 0` makes every read scan all four tables (≈ 10,000 records) instead of ≈ 3,600 at 95 % hits, with an identical fixture. E5 and E1 are different trials, so this block is **unpaired and supporting**, never confirmatory.

| ID | Estimand | Decision rule |
| :--- | :--- | :--- |
| **MC-5** | Manipulation check: `records_scanned(E5) / records_scanned(E1 r=8) ≥ 2.0`, and realised hit rate = 0 | If it fails, P5 is void |
| **P5a** | `W_A(hit=0) / W_A(hit=0.95)` for write progress | **Dose-response supported** if A's CI upper bound < 0.80 **and** the corresponding intervals for A-min and B contain 1.0 within ±10 % |
| **P5b** | Regression of write progress on the *measured* `T_readCS` across the 6 cells (E1/E5 × 3 arms) | Descriptive: negative slope for A, ≈ 0 for A-min/B. Reported with the fitted slope and CI. |
| **P5c** | `leases_per_read` (A-min) and `t_meta` vs `k` (microbench, D2) | Component cost, not an effect. Quantifies A-min's Θ(k) term. |

Because the primary dose evidence is the regression on the *measured* critical-section duration (P5b) rather than on the nominal manipulation, a modest effect in P5a does not weaken the mechanism claim; a missing effect in P5b does.

### 3.6 Global manipulation checks (must pass before any P is interpreted)

| Check | Expected | Blocks |
| :--- | :--- | :--- |
| Fixture build profile is the intended one | every "fixture" phase has the recorded profile | all |
| Realized arm-order distribution is balanced | each permutation within ±1 of target | all |
| `k` matches the block's fixture | 4 | E1–E5 |
| `records_scanned / read` matches the hit ratio | ≈ 3.6 k (95 % hit) / ≈ 10 k (0 % hit) | E1, E5 |
| Realized publications = 20 | exact | E2, E4 |
| Write counts in equal-work regime ≈ `W_paced × 10 s` | ±15 % | E3 |
| Arrival schedule hash matches | exact | E4 |
| `read_bytes` delta indicates the intended cache condition | cold: ≈ file size; warm: ≈ 0 | E2 diagnostic |

A failed check voids the affected prediction and is reported prominently; it does not void the campaign.

---

## 4. Statistical Procedures

| Procedure | Use |
| :--- | :--- |
| **Paired log-ratio + BCa bootstrap** (10,000 resamples, per-triplet resampling) | primary effect and CI for every paired contrast |
| **Wilcoxon signed-rank** (exact for N ≤ 12, normal approximation above) | paired test; used only as a secondary confirmation, never as the sole evidence |
| **Paired sign test** | robustness when the ratio distribution is heavy-tailed |
| **TOST** with pre-declared margins | equivalence claims (P1c, P2b, P3b) |
| **Non-inferiority test** (one-sided) | "A-min is not materially worse than B" |
| **Bootstrap interaction test** (difference of log ratios across regimes/blocks) | P3c, P5a |
| **Spearman ρ** | microbenchmark dose-response (`k` vs per-read cost), descriptive |
| **Linear model with trial position** | drift / order-effect check, per block |
| **Kaplan–Meier / lower-bound reporting** | response latency with abandoned (censored) requests in E4 |

Margins (fixed, OD-5):

| Contrast | Margin |
| :--- | :--- |
| Write progress, A-min vs B (P1c) | NI margin **0.80** |
| Read throughput, A-min vs B (P1d) | NI margin **0.90** |
| Latency, A-min vs B (P2b) | NI margin **1.15** on the latency ratio |
| Read throughput, equal-work B vs A (P3b) | NI margin **0.90** |
| Event-conditioned latency, A-min vs A (P2a) | superiority threshold **0.50** |

Margins are fixed before data. If a reviewer wishes different margins, that must be decided before G4 and recorded as a protocol amendment.

---

## 5. Multiplicity

| Family | Members | Correction |
| :--- | :--- | :--- |
| Confirmatory family 1 | P1a, P1b, P1c (E1 `r=8`) | Holm, α = 0.05 |
| Confirmatory family 2 | P2a, P2b (E2) | Holm, α = 0.05 |
| Confirmatory family 3 | P3a, P3b, P3c (E3 `r=4`) | Holm, α = 0.05 |
| Confirmatory family 4 | P4a (E4 L2, both contrasts) | Holm, α = 0.05 |
| **Across families** | the four family-level decisions | Holm, α = 0.05 |
| Secondary | P1d, P2c, P2d, P3d, P3e, P4b, P4c, P4d, P5a–P5c | no α control; labelled exploratory |
| Diagnostic | D1/D2 decomposition | model-based, no hypothesis test |

This is conservative: correcting across families means a single strong result may not survive the top-level Holm if all four families are tested. That is the price of pre-registering four questions; the manuscript will report raw and corrected p-values for every member so a reader can judge.

---

## 6. Precision Reporting

For every cell, the analysis reports:

- N (triplets included / planned), dropped triplets and reasons;
- point estimate and 95 % CI for every contrast;
- the realized `σ̂` of the paired log-ratio and the resulting minimum detectable ratio (so a reader can see whether an "inconclusive" result is uninformative or merely imprecise);
- the manipulation checks of §3.6;
- the composition covariates: `k`, writes, flushes, total ops.

`σ̂` is reported but **never** used to change N, margins, or tests.

---

## 7. Robustness (pre-declared, not fishing)

| Check | Purpose |
| :--- | :--- |
| Median-of-ratios instead of ratio-of-geometric-means | outlier sensitivity |
| Excluding the first triplet of each cell | start-up transients |
| Trial-position covariate | drift within the campaign |
| Recomputing with `r=1` as the primary E1 cell | concurrency sensitivity |
| Recomputing E3 with realized (not attempted) write counts as a covariate | residual work imbalance |
| Recomputing latency contrasts on the miss class only | removes the hit/miss mixture ([03](03_PACING_AUDIT.md) §3) |

All robustness results are reported in an appendix. None may replace the primary result.

---

## 8. Stopping Rule

### 8.1 No outcome-based stopping

There is **no** interim efficacy analysis, no alpha spending, and no early stop for a positive or negative result. The only stops are validity stops (§8.2), budget stops (§8.3), and the hard trial ceiling (384).

### 8.2 Validity stops (immediate)

| ID | Condition | Action |
| :--- | :--- | :--- |
| S-1 | Platform gate P-1…P-10 fails ([05](05_PLATFORM_VERIFICATION.md) §8) | stop before trial 1 |
| S-2 | Provenance invalid, dirty tree, or binary hash mismatch | abort campaign |
| S-3 | A-min correctness gate (G1) fails: any differential mismatch, race, leak, or shutdown deadlock | stop; publish the design without empirical claims |
| S-4 | Any data race detected in any test or trial | abort |
| S-5 | Leak indicator (X-10) in ≥ 2 trials of the same arm | stop the block; investigate; resume only with a documented fix and a new protocol version |
| S-6 | Platform drift: the end reference triplet differs from the start reference by > 15 % in median read throughput for **any** arm | stop; determine whether drift is arm-independent; if not, abort and re-run on a stable host |
| S-7 | Storage health: `media_errors` increases, or `fio` fsync p99 exceeds 20 ms | abort |
| S-8 | Any modification detected under the two frozen data directories, or any runner write outside its output directory | immediate abort and integrity report |
| S-9 | Validation error rate > 0 in any trial | that trial is excluded (X-3); if it recurs in > 5 trials, stop and investigate |

### 8.3 Budget stops

| ID | Condition | Action |
| :--- | :--- | :--- |
| B-1 | 384 trials executed | stop; analyze what exists |
| B-2 | Replacement budget (18) exhausted | stop replacing; continue planned trials |
| B-3 | Wall clock > 8 h from G4 start | stop; analyze what exists; report the truncation |
| B-4 | Disk space < 10 GiB | stop; free space; do not delete trial records |

### 8.4 Pre-registered reduction (cost gate, [06](06_TRIAL_INVENTORY.md) §9.3)

If preflight measures > 75 s per trial, apply the reduction ladder (D2 reps 5→3; E3 to `r=4` only; E7 to 2 shapes) in that order. The reductions are defined on non-outcome data (measured cost) and are applied identically to all arms.

### 8.5 What a truncated campaign means

If B-1/B-3 truncates a block, the affected prediction is reported as **underpowered** with its CI and the number of triplets actually obtained. Truncation is not a reason to relax a margin, change a test, or promote an exploratory analysis.

---

## 9. Deviations Policy

Any deviation from this plan is recorded in `deviations.md` with timestamp, reason, and whether it was decided before or after seeing any outcome. Deviations after unblinding that affect a confirmatory analysis void that analysis as confirmatory and demote it to exploratory. Protocol amendments require reviewer sign-off and a version bump in `campaign_manifest.json`.

---

## 10. What Counts as a Failed Study

A failed study is one that cannot answer P1–P4 with the planned precision **and** whose failure is attributable to the design rather than to an inconclusive result. Explicitly:

- **Not a failure:** A-min ≈ B (that is the informative outcome that reframes the paper).
- **Not a failure:** a cell is inconclusive with a CI that is reported honestly.
- **Failure:** the equal-work manipulation does not hold (MC-3) **and** no other block can separate the D hypothesis.
- **Failure:** the A-min implementation cannot pass the correctness gate, leaving R1 unanswered.
- **Failure:** platform drift or storage instability makes arm contrasts uninterpretable.

In the failure cases the manuscript reports the design and the reason, and the corresponding claim is removed or explicitly marked as unresolved. No claim is retained on the strength of the frozen campaigns alone if I7 was designed to test it and could not.
