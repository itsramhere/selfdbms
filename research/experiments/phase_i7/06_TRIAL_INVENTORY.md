# 06 — Complete Trial Inventory, Statistics, Cost, and Hard Limit

**Status:** PREREGISTERED INVENTORY. No performance trial has been executed. **Windows-amended 2026-10-28** (§9).
**Campaign:** `I7_STRENGTHENING_V1`
**Platform:** Windows 11 / NTFS / internal NVMe ([12](12_WINDOWS_EXECUTION_AMENDMENT.md))
**Planned trials:** **366**
**Hard ceiling:** **384** (366 + 18 invalid-only replacements)
**Reduction vs history:** 366 / 1,560 = **23.5 %**; 366 / 960 = 38 %.

> **Amendment.** The inventory, N per cell, exclusions, randomization, seeds and the hard limit are **unchanged**. Two things changed: the platform (§9 cost, revalidated on Windows), and the fact that only **144 of the 330 confirmatory rows** are executable by the current runner ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §5). The runlist is committed at 366 rows regardless, so the budget accounting does not move.

---

## 1. Design Principles

1. **One unit, three arms.** The experimental unit is a **triplet**: three trials sharing a seed, an arrival schedule and a key stream, one per arm `{A, A-min, B}`. Every block is a fully crossed triplet design. This gives paired contrasts with the same variance reduction the frozen campaigns relied on, extended to three arms.
2. **No cell exists without a decision rule.** Every cell in this document is tied to a pre-registered prediction in [07](07_ANALYSIS_PLAN.md). If a cell is not needed to decide a prediction, it is not run.
3. **Only five questions are asked.** (i) Does A-min capture B's benefit? (ii) Does the same hold for publication tail? (iii) Is Workload D's penalty snapshot-specific or a work/lock-sharing effect? (iv) What does the latency picture look like at matched offered load? (v) Does the effect scale with the length of the read critical section?
4. **Reuse, don't repeat.** The frozen campaigns already establish gross A-vs-B differences across 48 cells. I7 measures only the cells where the *new arm* changes the inference, plus the cells where the *measurement method* was shown to be invalid ([03](03_PACING_AUDIT.md)).
5. **Fixed N, no adaptation.** N per block is fixed below. There is no sequential sampling, no alpha spending, and no post-hoc N increase.

---

## 2. Block Definitions

Common to all blocks unless stated: keyspace 10,000 (Workload E-style excepted), 128-byte values, 19-byte keys, 95 % hit ratio, uniform key sampling, `wal_sync = true`, fresh data directory per trial, 10,000 ms measurement window, 2,000 ms warmup, 200 ms cooldown.

### E1 — Starvation decomposition (Workload B1 shape)

| Property | Value |
| :--- | :--- |
| Purpose | Decide whether A-min captures B's writer-progress advantage |
| Workload | 4 pre-built SSTables (2,500 keys each), **2 unpaced writers**, 1 MB memtable threshold, no forced flush, no compaction trigger |
| `r` | **1, 8** |
| Arms | A, A-min, B |
| Triplets/cell | **12** |
| Trials | 2 × 12 × 3 = **72** |
| Why these `r` | `r=1` is the low-concurrency anchor (frozen A-vs-B write ratio 10.8×); `r=8` is the peak of the frozen effect (18.5×). `r ≥ 24` is dominated by host scheduling in the frozen data and adds no discrimination. |
| Primary endpoints | completed writes; completed reads; reader-block share |
| Cannot answer | anything about publication cost (no forced publications) |

### E2 — Publication tail, publication-only driver (Workload B2 shape, redesigned)

| Property | Value |
| :--- | :--- |
| Purpose | Decide whether A-min captures B's publication-tail advantage, with the WAL-`fsync` convoy removed |
| Workload | 4 pre-built SSTables, **0 writers**, 64 MB memtable, **publication-only driver at 2 Hz** (H-07): 20 pre-frozen memtables released on an absolute schedule, injector never blocks and never commits |
| Publication modes | `none` (control: same fixture, no events) and `physical` (20 flush publications) |
| `r` | **4** (where the frozen suppression was largest and the event-count defect was absent) |
| Arms | A, A-min, B |
| Triplets/cell | **10** |
| Trials | 2 × 10 × 3 = **60** |
| Primary endpoints | event-conditioned p95/max response latency; publication hold; realized event count (= 20 required) |
| Why `r=4` only | At `r=1` the stall population is 5.0 % of samples (fragile, [03](03_PACING_AUDIT.md) §2); at `r ≥ 8` the frozen B2 was contaminated by the uncounted write batch. `r=4` is the only frozen cell whose tail was both measurable and publication-dominated. |
| Cannot answer | behaviour at high concurrency (not claimed) |

### E3 — Churn: free-work vs equal-work (Workload D shape)

| Property | Value |
| :--- | :--- |
| Purpose | Decide whether the D "penalty" is snapshot-specific or a work-composition / lock-sharing effect |
| Workload | 4 pre-built SSTables, **16 KiB** memtable, compaction trigger every 1,500 ms, `r ∈ {1, 4}` |
| Regimes | **free**: 2 unpaced writers (historical D). **equal-work**: 1 writer paced at `W_paced` writes/s by an absolute-time token bucket, so both arms attempt identical write work; the attempt count is recorded and the realized count is a covariate. |
| Arms | A, A-min, B |
| Triplets/cell | **8** |
| Trials | 2 `r` × 2 regimes × 8 × 3 = **96** |
| Primary endpoints | read throughput at matched attempted write work; total-op throughput; median read latency; flush count; write count |
| `W_paced` | **fixed at G2 from frozen data only**, before G4: `W_paced = median A write ops/s` from the frozen D trials, rounded to the nearest 10 → **235** at `r=1`, **210** at `r=4` (§7). Identical for every arm and regime. Rationale: a 16 KiB memtable holds ≈ 100 keys, so 210–235 writes/s × 10 s ≈ 2,100–2,350 keys reproduces the frozen 22–28 flushes per trial while keeping the writer well inside both arms' capacity. |
| Cannot answer | which of the three A-min↔B differences causes any residual gap (that is D1/D2) |

### E4 — Open-loop offered load

| Property | Value |
| :--- | :--- |
| Purpose | Measure latency at *matched, controlled* offered load without coordinated omission |
| Workload | 4 pre-built SSTables, 0 writers, publication-only driver at 2 Hz, **16 workers** |
| Offered rates | `0.30 · C_ref`, `0.60 · C_ref`, `0.90 · C_ref` (absolute, identical across arms) |
| Arrival process | pre-computed exponential schedule, identical across the three arms of a triplet ([04](04_OFFERED_LOAD_METHODOLOGY.md) §2) |
| Arms | A, A-min, B |
| Triplets/cell | **6** |
| Trials | 3 rates × 6 × 3 = **54** |
| `C_ref` | measured once per platform in the calibration block (9 short runs, not trials), `min` over arms |
| Primary endpoints | response-latency p99 and p99.9; abandon rate; achieved/offered ratio |
| Cannot answer | absolute capacity claims beyond this platform |

### E5 — Read-critical-section dose-response

| Property | Value |
| :--- | :--- |
| Purpose | Test the *critical-section-duration* mechanism directly, at zero fixture cost |
| Workload | 4 pre-built SSTables (2,500 keys each, 10,000 keys), 2 unpaced writers, 1 MB memtable, `r = 8` |
| Manipulation | **`TargetReadHitRatio = 0.0`** — every sampled key lies outside the populated range (`SampleKey` returns `cardinality + rnd(10000) + 1`, [workloads.go:72–77](../../experiments/harness/workloads.go)), so every read scans **all four tables in full** (the binary search finds no block ≥ key, `targetOffset = 0`) |
| Dose achieved | ≈ 10,000 records scanned per read vs ≈ 3,600 at the 95 %-hit baseline — a **≈ 2.8× longer read critical section for A**, with an *identical fixture* and an identical working set |
| Arms | A, A-min, B |
| Triplets/cell | **6** |
| Trials | 6 × 3 = **18** |
| Primary endpoints | write progress vs E1 `r=8`; measured `T_readCS` (D2); median read latency |
| Why this manipulation | Changing table count or table size changes both `k` and the scan length, and inflates the fixture quadratically (AF-7). Changing the hit ratio changes **only the amount of work inside the read critical section**, which is the hypothesised causal quantity. It is the cheapest possible intervention on the mechanism. |
| Cost | Same fixture as E1 (≈ 32 s); ≈ 45 s per trial; 14 min total |
| Cannot answer | whether the effect saturates above a 2.8× critical section (the D2 microbenchmark sweep covers the curve) |
| Note | With 0 % hits, `sstablePointReads` is not incremented (the counter only counts hits), so `records_scanned` (H-20) is the required work metric for this block. |

### E7 — Pebble external anchor (unpaired, descriptive)

| Property | Value |
| :--- | :--- |
| Purpose | External plausibility of the *shape* of the results; not a performance race |
| Shapes | **Y1** read-only 4 tables, `r=8`; **Y2** mixed 2 sync writers, `r=8`; **Y3** churn 16 KiB memtable, `r=8` |
| Reps/shape | **8** |
| Trials | 3 × 8 = **24** |
| Endpoints | completed ops, latency distribution, flush/compaction counts, table count |
| Reporting | descriptive only; **no hypothesis test**; no ratio-based claim ([02](02_COMPATIBILITY_MATRIX.md) §4–5) |
| Cannot answer | anything causal; anything about durability; anything about A vs B |

### Preflight (engineering only)

4 representative triplets — one each for E1 (`r=8`), E2 (`physical`), E3 (`equal-work`, `r=4`), E4 (`0.60 C_ref`) — executed at **1,500 ms** window and **300 ms** warmup.
**12 trials.** Purpose: wiring, provenance, scheduling, recording, driver correctness, per-trial cost measurement.
**Policy:** preflight observations are never reported as findings and never enter the analysis.

### D1 — Workload-D profiling (separate processes)

3 arms × 3 repetitions, 5 s window, CPU/mutex/block/allocs/heap/exec-trace profiles + differential `MemStats`. **9 runs.** Excluded from all performance inference ([09](09_WORKLOAD_D_PROFILING.md)).

### D2 — Instrumented mechanism runs (separate build)

3 arms × 5 repetitions with build-tag lock-occupancy instrumentation (m.Mu hold by writer/reader, `snapshotMu` wait, WAL `fsync`, records scanned, per-read lease count), plus a microbenchmark sweep over `k ∈ {1,2,4,8,16,32}` that costs no trials. **15 runs.** Excluded from performance inference.

### D3 — Platform drift reference

One reference triplet (E1 `r=8`) at the **start** and one at the **end** of G4: 2 × 3 = **6 trials**. Used only for the drift check and as a time covariate.

---

## 3. Totals

| Block | Cells | N/cell | Trials |
| :--- | ---: | ---: | ---: |
| E1 Starvation decomposition | 2 `r` × 3 arms = 6 | 12 | 72 |
| E2 Publication tail | 2 modes × 3 arms = 6 | 10 | 60 |
| E3 Churn work-regime | 2 `r` × 2 regimes × 3 arms = 12 | 8 | 96 |
| E4 Open-loop load | 3 rates × 3 arms = 9 | 6 | 54 |
| E5 Read-critical-section dose-response | 3 arms | 6 | 18 |
| E7 Pebble anchor | 3 shapes | 8 | 24 |
| Preflight | 4 triplets | 1 | 12 |
| D1 Profiling | 3 arms | 3 | 9 |
| D2 Instrumented | 3 arms | 5 | 15 |
| D3 Drift | 2 time points × 3 arms | — | 6 |
| **Total planned** | | | **366** |
| Replacement budget (invalid only) | | | **+18** |
| **Hard ceiling** | | | **384** |

---

## 4. Statistical Justification

### 4.1 Estimand and variance

The primary analysis is on **paired log-ratios** within a triplet: `d_t = ln(x_{t,arm1}) − ln(x_{t,arm2})`. With `σ` = SD of `d_t`, the 95 % CI half-width is `1.96 σ / √N` and the detectable effect at 80 % power (two-sided α = 0.05) is `2.802 σ / √N`. All are reported as ratios `exp(·)`.

### 4.2 Sensitivity table (ratio units, ±%)

| N | 80 %-power detection, σ=0.05 | σ=0.10 | σ=0.20 | σ=0.30 | 95 % CI half-width, σ=0.10 | σ=0.20 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 6 | 5.7 % | 11.4 % | 22.9 % | 34.3 % | ±8.0 % | ±16.0 % |
| 8 | 4.9 % | 9.9 % | 19.8 % | 29.7 % | ±6.9 % | ±13.9 % |
| 10 | 4.4 % | 8.9 % | 17.7 % | 26.6 % | ±6.2 % | ±12.4 % |
| 12 | 4.0 % | 8.1 % | 16.2 % | 24.3 % | ±5.7 % | ±11.3 % |

### 4.3 Why each N

| Block | N | Justification |
| :--- | ---: | :--- |
| E1 | 12 | Two decisions of different kinds. (a) *Superiority* A → A-min: expected ratio ≥ 3, so even σ = 0.5 is detectable at N = 6. (b) *Bound* A-min vs B: with σ = 0.20 (conservative for two fast arms) the CI is ±11.3 %, inside the pre-registered ±20 % bound with margin. |
| E2 | 10 | The estimand is a ratio of event-conditioned latencies over 20 events per trial; the within-trial median of 20 holds is stable, so σ ≈ 0.10–0.15 is expected. N = 10 gives ±6.2–9.3 %, inside the ±15 % margin. |
| E3 | 8 | Precision claim: read-throughput ratio at matched work within ±10 %. σ = 0.10 → ±6.9 % (pass); σ = 0.20 → ±13.9 % (inconclusive but reported). N = 8 is the smallest N that survives the optimistic case with margin. The paired free-vs-equal-work interaction has a much larger expected effect and is powered regardless. |
| E4 | 6 | Very large expected effect (p99 ratio ≤ 0.5), and both sides of the comparison come from the *same* arrival schedule, which removes schedule variance. σ = 0.30 still detects 34 %. |
| E5 | 6 | Directional/dose claim with a large manipulated change (≈ 2.8× more records scanned per read at 0 % hits). The quantitative dose evidence comes from regressing write progress on the *measured* `T_readCS` and from the D2 microbenchmark sweep, not from trial count. |
| E7 | 8 | Descriptive; 8 is enough for a stable median and IQR ([00](00_README.md) §5). No test is performed. |

Historical variance used as a prior: the frozen B1 writer-ratio CI at `r=4` is `[15.2, 28.1]`, i.e. a log half-width of 0.307 → `σ ≈ 0.5` for the A-vs-B *stall-dominated* contrast. That variance is **not** applicable to A-min-vs-B (both arms are expected to be fast); σ = 0.2 is used as the conservative planning value. If the realized σ is 0.5, the A-min-vs-B contrast will be reported as inconclusive with its CI and the *superiority* contrasts (A → A-min) will still be decisive. **No N is increased after data are seen.**

### 4.4 Multiplicity

Four confirmatory predictions (P1–P4 in [07](07_ANALYSIS_PLAN.md)) are Holm-corrected at α = 0.05 across the four. Within a prediction, its pre-declared cells are Holm-corrected as a family. Everything else is labelled **secondary/exploratory** and reported without α control. No cell is promoted from exploratory to confirmatory after inspection.

---

## 5. Randomization, Ordering, and Commitment

1. **Full run list generated deterministically** from the manifest generator with master seed `S0 = 20261101`, before any execution.
2. **Stratification:** the list is grouped by `(block, cell)`; cells are interleaved so that no more than **12** consecutive trials belong to the same cell and no arm appears more than **3** times consecutively. A greedy schedule is generated and validated by a checker.
3. **Within-triplet order:** the 6 permutations of `{A, A-min, B}` are assigned so that, within a cell, each permutation appears as evenly as possible (N=6 → once each; N=12 → twice each; N=8/10 → balanced to within one).
4. **Commitment:** `runlist.csv` (columns: `trial_id, block, cell, triplet, arm, order_position, seed, schedule_hash`) and its SHA-256 are committed in `campaign_manifest.json` **before G4**. The runner refuses to start if the file's hash differs, and each trial record embeds its `runlist` position and the expected order.
5. **Realized-order audit:** the analysis reports the realized order distribution and tests for an order effect (trial position as a covariate) per block.

---

## 6. Seeds and Determinism

| Quantity | Derivation |
| :--- | :--- |
| Master seed `S0` | `20261101` (committed) |
| Triplet seed | `int64(SHA256(S0 ‖ block ‖ cell_id ‖ triplet_index)[0:8]) & (1<<63−1)` |
| Worker seed | `SHA256(triplet_seed ‖ role ‖ worker_index ‖ purpose)` (existing `DeriveWorkerSeed`, unchanged) |
| Per-op key | `SHA256(triplet_seed ‖ "key" ‖ op_index) mod keyspace` — **counter-based**, so request `j` uses the same key in every arm of the triplet (H-11) |
| E4 arrival schedule | `t_j = t_{j−1} + Exp(rate)` using `SHA256(triplet_seed ‖ "sched" ‖ rate)` as the stream; identical in all three arms |
| E3 paced-writer schedule | absolute-time token bucket at `W_paced`; the *attempt* times are identical across arms |

No seed depends on the trial index or on the arm. Trial-index-dependent derivation was the I6.9 defect and must not reappear.

---

## 7. Two Values Fixed at G2 from Frozen Data Only

Both are computed before G4, published, and identical for all arms.

| Value | Rule | Input |
| :--- | :--- | :--- |
| `W_paced` (E3 equal-work) | `round_to_10(median over frozen D trials of A write ops/s)` separately for `r=1` and `r=4` | frozen `*WORKLOAD_D_READER_CHURN_A_r{01,04}_rep*.json` → **235** (`r=1`), **210** (`r=4`) |
| `C_ref` (E4) | `min over arms of median closed-loop read ops/s` at `r=16`, 5 s, 3 reps, on the I7 platform | calibration block (9 runs, not trials) |

Neither value depends on an I7 outcome. `C_ref` depends on arm capacities, but the rule is a mechanical minimum and the calibration data are published alongside.

---

## 8. Exclusion Criteria (mechanical only)

A trial is excluded from inference — never from the record — if **any** of:

| # | Criterion | Field |
| :--- | :--- | :--- |
| X-1 | `status != SUCCESS` | `status` |
| X-2 | provenance invalid (`unknown_rev`, dirty tree, hash mismatch) | `git_revision`, manifest |
| X-3 | `validation_error_count > 0` | `operations` |
| X-4 | `fixture_manifest.validation_readback_status != "PASSED"` or table count mismatch | `fixture_manifest` |
| X-5 | platform manifest mismatch (fs type, device, affinity, GOMAXPROCS) | `environment` |
| X-6 | realized publication count ≠ 20 (E2, E4) | `lsm_diagnostics.measured_flushes` |
| X-7 | `achieved/offered < 0.98` at L1/L2, or `< 0.95` at L3 | `throughput`, schedule |
| X-8 | abandon rate > 0.02 at L1/L2 | `operations` |
| X-9 | `latency_read_nanos.sample_count < 100` (p95 unsupported) | `latency_read_nanos` |
| X-10 | fd-count growth > 8 over the trial, or nonzero `pending_cleanup_count` at end (leak) | new diagnostics |
| X-11 | `stall_time_share` undefined because no publication event occurred in a `physical` cell | derived |
| X-12 | trial wall clock > 300 s (host interference / pathological fixture) | timestamps |

Rules:
- The exclusion script reads **only** the fields listed; it never inspects the outcome magnitude of the trial being excluded.
- The excluded-trial list is published with reasons before the confirmatory analysis runs.
- **Replacement** is permitted only for X-1, X-2, X-5, X-10, X-12 (mechanical failures), with a new `attempt` number and the same `trial_id` prefix. Replacement is **not** permitted for X-6…X-9, X-11 (those indicate a real treatment-related deviation and must be reported as such).
- If one arm of a triplet is excluded, that triplet is dropped from paired contrasts; its other two trials are retained for descriptive tables only.
- Replacement budget: **18**. When exhausted, the ceiling is reached and analysis proceeds on what exists.

---

## 9. Execution Cost

### 9.1 Measured per-trial cost (Windows/NTFS — governing figures)

Fixture cost measured from the archived Windows NTFS campaign (`research/experiments/data/campaign/windows_ntfs/`, 314 trials): A 13.24 s (n=64), B1 13.18 s (n=58), B2 12.64 s (n=48), C 12.83 s (n=48), D 13.16 s (n=48), E 2.32 s (n=48). Same root cause as Linux (per-key tiered conflict check + `fsync`), but the host NVMe is ~2.4× faster than the WSL2 virtual disk at it.

Per-trial budget = fixture 13.2 s + warmup 2 s + window 10 s + cooldown 0.2 s + teardown ≈ 1.5 s ≈ **27 s**.

| Scope | Trials | Wall clock |
| :--- | ---: | ---: |
| Currently runnable rows (E1, E3-free, E5, D3) | 144 | **≈ 1.1 h** |
| Preflight | 12 | ≈ 0.1 h |
| Full confirmatory set | 330 | ≈ 2.5 h |
| Full inventory (366) | 366 | **≈ 2.7 h** |
| Plus `-Check`, retries, packaging | — | **≈ 3.4 h ± 1 h** |

Windows is **cheaper** than the Linux estimate despite being the same code path, because fixture construction dominates and NTFS-on-NVMe beats the WSL2 VHD. Verified against `run_i7.ps1 -DryRun -Campaign`, which prints the same 27 s basis for the selected rows.

### 9.1.1 Historical Linux figures (superseded, retained for the record)

`fixture_manifest.initialization_duration_ms` medians on WSL2 ext4: A/B1 **31.7 s**, B2 30.7 s, C 42.0 s, D 40.5 s, E 9.2 s. Per-block budget totalled **≈ 4.7 h** for 366 trials, **≈ 5.9 h** including overhead. This is why the earlier estimate was ~6 h; it no longer applies.

### 9.2 If the bulk-fixture path (H-09) is approved

Fixture cost drops to ≈ 2 s (one `O(n)` sorted write per table, no per-key conflict check), verified by byte-identical fixture SSTables and readback:

| Scenario | Campaign wall clock |
| :--- | ---: |
| Windows measured basis (no H-09) | **≈ 3.4 h** |
| With H-09 bulk fixture | **≈ 1.6 h** |

H-09 does not change the fixture *content* (same keys, same sequence numbers, same 4 tables, verified byte-for-byte), only how it is produced. It is recommended (OD-4) but not required, and its payoff on Windows is ~2× rather than the ~3× estimated for Linux.

### 9.3 Cost gate at G3 (Windows threshold)

The gate now triggers on a **G3 preflight measurement**, not on G2, because the Windows per-trial cost cannot be measured without executing a trial.

If preflight measures a per-trial cost above **45 s** (i.e. >1.65× the 27 s Windows basis), the following pre-registered reduction applies, in this order, without reference to any outcome:

1. drop D2 repetitions from 5 to 3 (−6 runs);
2. drop E3 from `r ∈ {1,4}` to `r = 4` only (−48 trials);
3. drop E7 from 3 shapes to 2 (−8 trials).

Only these reductions are permitted; no block may be expanded, and no reduction may be reversed after data are seen. The **366 / 384 limits are unchanged** by the platform amendment.

---

## 10. Hard Trial Limit — Statement

> **The campaign will execute at most 384 trials.** 366 are planned; at most 18 replacements are permitted, and only for the mechanical failure classes listed in §8. No additional cells, no additional repetitions, no additional seeds, and no re-runs of "unlucky" trials will be executed. If a scientific question is left underpowered by this limit, it is reported as underpowered with its confidence interval; it is not re-run.
>
> The limit is enforced in code: the runner reads `campaign_manifest.json`, counts completed `attempt` records, and refuses to start trial 385. The count includes preflight, diagnostics, calibration (counted separately), and replacements.

Calibration runs (9) and microbenchmarks are **not** trials; they are recorded in a separate ledger and are capped at 9 and 12 respectively.

---

## 11. Trial ID Scheme

```
i7_<block>_<cell>_<triplet>_<arm>[_a<attempt>]

block  ∈ {E1,E2,E3,E4,E5,E7,PF,D1,D2,D3}
cell   ∈ e.g. r08, pub_phys, free_r01, L2, hit0, Y1
triplet∈ t01..t12
arm    ∈ A | AMIN | B | PEBBLE | —
```

Example: `i7_E2_pub_phys_r04_t03_AMIN`.

Each record embeds: `trial_id`, `runlist_position`, `triplet_seed`, `schedule_hash`, `arm_order_position`, `attempt`, and the platform/provenance block.
