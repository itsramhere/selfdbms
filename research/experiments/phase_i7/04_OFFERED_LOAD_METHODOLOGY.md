# 04 — Offered-Load Design and Latency Measurement Methodology

**Status:** DESIGN ONLY. No runs. **Windows-amended 2026-10-28.**
**Applies to:** E4 (open-loop) and, as a reporting layer, to E1/E2/E3/E5.
**Motivation:** [03](03_PACING_AUDIT.md) shows the historical driver is closed-loop with at most one outstanding request per reader, which suppresses the frequency of stalled requests by 3–6× and makes p95 a function of `r` and of the injection schedule.

> **Windows amendment.** The design is unchanged; one implementation rule is now mandatory.
>
> - **Clock:** all intended/actual timestamps and all wakeups must come from **QueryPerformanceCounter** (`harness.NowPrecision` / `SincePrecisionNanos`). `time.Now` has a ~404 µs minimum step on the target host and would quantise `q_j` to zero for most requests, silently hiding the very queueing delay this document exists to measure.
> - **Sleep:** `time.Sleep` overshoots a 1 ms request by ≈0.5 ms on average (mean 1.536 ms, max abs error 0.948 ms), so the last ~1.5 ms of every wait must be a spin.
> - **Validated:** with that discipline, 200 wakeups at 5 ms spacing achieved p50 **0.2 µs**, p95 **9.1 µs**, p99 **65.7 µs**, max **552.7 µs** on the target host ([13](13_G1_REVIEW_EVIDENCE.md) §5).
> - **No change** to the offered-load levels, the `C_ref` calibration rule, the abandonment rule, or the censoring policy.


---

## 1. Definitions (fixed for the whole study)

Let a trial have a measurement window `[T0, T1)` of 10 s, a pre-registered arrival schedule, and a set of reader workers.

| Symbol | Definition |
| :--- | :--- |
| `t_j` | **intended issue time** of request `j` (from the pre-computed schedule) |
| `a_j` | **actual issue time** (the instant the worker begins the `Get` call) |
| `c_j` | **completion time** |
| **service latency** | `s_j = c_j − a_j` — what the historical harness measures |
| **queueing delay** | `q_j = a_j − t_j` (≥ 0; how late the worker was) |
| **response latency** | `y_j = c_j − t_j = q_j + s_j` — the coordinated-omission-correct measure |
| **offered rate** `R` | scheduled requests per second (an input) |
| **achieved rate** | completed requests / window (an output) |
| **abandoned** | request with `a_j > t_j + W_max`, `W_max = 250 ms`; counted, not silently dropped |
| **censored** | an abandoned request's response latency is known only to exceed `W_max` |

`y_j` is the primary latency estimand for E4. `s_j` is retained for comparability with the frozen campaign and for decomposition (`y − s = q`).

---

## 2. Open-Loop Driver

### 2.1 Pre-computed arrival schedule

Arrivals are generated **before** the trial, from a seeded exponential process:

```
t_0 = 0
t_j = t_{j-1} + Exp(rate = R)          // deterministic from the trial seed
key_j = KeyStream(seed, j)             // counter-based, same for every arm (H-11)
```

The schedule is an array `[(t_j, key_j)]`, length `N ≈ R × 10 s`. Storing it means:

- **no generator goroutine** (no extra scheduling noise, no channel),
- **identical arrival times and identical keys in every arm** — a genuinely matched open-loop design,
- the schedule is serialised in the trial JSON, so the latency analysis can be re-run offline,
- the schedule's SHA-256 is recorded in the trial record.

### 2.2 Worker protocol

```
idx := schedule.next()                  // atomic fetch-add, no contention beyond one counter
if idx >= len(schedule) { return }
t, key := schedule[idx]
sleep_until(t)                          // monotonic; if t is past, proceed immediately
a := now()
if a-t > W_max { record abandoned; continue }
v, err := man.Get(key, MaxUint64)
c := now()
record{idx, t, a, c, err}
```

`schedule.next()` is the only shared counter; per-worker records are appended to per-worker buffers (no shared mutex), merged at the end (H-02). Workers that fall behind do not accumulate a backlog: they abandon and move on. This bounds memory and makes overload explicit.

### 2.3 Why not a bounded channel

A buffered channel places a queue *inside* the measurement instrument, mixes channel contention into `q_j`, and makes the backlog state opaque. The index scheme has exact `t_j`, no queue, and a transparent `abandoned` count.

---

## 3. Reference Capacity and Chosen Rates

### 3.1 Calibration (not a trial, not an inference)

`C_ref` is measured once per platform, after G2, in a dedicated calibration block:

- condition: read-only, 4 tables, 10k keys, 95 % hit, **closed-loop**, `r = 16`, 5 s window, 3 repetitions **per arm**;
- `C_ref = min over arms of median(achieved read ops/s)`;
- 9 short runs ≈ 9 × 50 s ≈ 8 min (fixture-bound); recorded as `calibration`, excluded from all confirmatory analysis;
- `C_ref` and its derivation are committed before the first E4 trial. The rule is mechanical (a minimum), so it cannot be tuned to an outcome.

Rationale for the minimum: at `0.9 · C_ref` no arm is above 90 % of its own capacity, so any queue growth is attributable to the *publication events*, not to a standing overload.

### 3.2 Offered-load levels

| Level | Rate | Purpose |
| :--- | :--- | :--- |
| **L1** | `0.30 · C_ref` | Below the knee: publication stalls should be visible but the queue should be empty almost always. Establishes per-event stall duration cleanly. |
| **L2** | `0.60 · C_ref` | Moderate utilisation: queueing begins to interact with stalls. |
| **L3** | `0.90 · C_ref` | Near capacity: exposes whether short critical sections delay the onset of overload. If an arm cannot sustain 0.9 C_ref it will abandon requests — an outcome, reported. |

Three levels are the minimum that identifies a knee. With only two points the "knee" is not identifiable; with four the marginal information does not justify another 18 trials.

If `C_ref` is dominated by the read path's 10 ms scans, note that these levels are **absolute request rates**, identical across arms; they are not "fractions of each arm's capacity". That is deliberate: it makes the comparison "same offered load, different response" rather than "each arm at 90 % of itself".

### 3.3 Expected sample sizes (E4)

| Level | R (if `C_ref ≈ 500/s`) | Requests | Abandoned (expected) |
| :--- | ---: | ---: | :--- |
| L1 | 150 /s | 1,500 | ~0 |
| L2 | 300 /s | 3,000 | ~0 |
| L3 | 450 /s | 4,500 | 0 in B; possibly > 0 in A |

p99.9 requires ≥ 1,000 samples (the harness's existing rule) and is available at all three levels. At L1 it rests on ≈ 1.5 exceedances (`0.001 × 1,500`), so it is reported with an explicit `p999_exceedances` count and flagged as unstable rather than treated as an estimate.

### 3.4 Publication driver in E4

Publications are injected by the **publication-only driver** (H-07) at a fixed 2 Hz (20 events in 10 s), scheduled by absolute time and never blocking the injector's own schedule:

- at `T0 + 0.5k` s, the injector releases a pre-frozen memtable to the flush worker;
- the injector does **not** wait for completion and does **not** commit any writes;
- realized publications are counted; if fewer than 20 occur (because the flush worker is backlogged), the trial is flagged and the count is reported.

This isolates the publication critical section from the WAL `fsync` convoy that contaminated B2 ([03](03_PACING_AUDIT.md) §4).

---

## 4. Latency Metrics Reported for Every Trial

### 4.1 Unconditional

| Metric | Definition | Notes |
| :--- | :--- | :--- |
| `n_completed`, `n_abandoned`, `abandon_rate` | counts | |
| `s_p50, s_p90, s_p99, s_p99.9` | percentiles of **service** latency | comparable with the frozen campaign |
| `y_p50, y_p90, y_p99, y_p99.9` | percentiles of **response** latency | primary for E4 |
| `q_mean, q_p95` | queueing delay | demonstrates whether the arm is capacity-limited |
| `max_service` | max observed | required to interpret `W_max` censoring |
| `achieved_rate / R` | offered-load adherence | must be ≥ 0.98 at L1/L2 for the cell to be interpretable as "at load R" |
| hit/miss split | from `expectedHit` | because the miss class costs ~3× a hit ([03](03_PACING_AUDIT.md) §3) |
| `miss_p50, miss_p95` | latency by outcome class | separates the mixture from the tail |

### 4.2 Stall-event statistics (sampling-structure independent)

For every trial, whether closed- or open-loop:

| Metric | Definition |
| :--- | :--- |
| `stall_threshold` | `10 × median(service)` for that trial |
| `n_stalls` | number of *episodes* (consecutive samples above threshold, per worker-lane) above threshold |
| `stall_time_share` | Σ over episodes of `(episode_end − episode_start)` / window — the fraction of wall time during which at least one reader was stalled |
| `max_stall` | longest episode |
| `lost_capacity` | `1 − achieved_rate / (achieved_rate at matched no-publication control)` |

`stall_time_share` is the honest replacement for "p95": it is a property of the system and the injection schedule, not of the number of workers.

### 4.3 Event-conditioned latency

For each publication event `e` with engine-observed window `[b_e, r_e]` (`OnFlushPublishBegin` → `OnFlushPublishEnd`, which in Approach A spans the reader-drain wait because the hook fires *before* `m.Mu.Lock`):

| Metric | Definition |
| :--- | :--- |
| `cond_count_e` | reads whose `[a_j, c_j]` intersects `[b_e − δ, r_e + δ]`, δ = 10 ms |
| `cond_p50_e, cond_p95_e, cond_max_e` | their response latencies |
| `hold_e` | `r_e − b_e` (engine-measured publication hold) |
| `excess_e` | `cond_max_e − median(service)` |
| `block_ratio_e` | `cond_max_e / hold_e` — ≈ 1 for A (arrivals wait out the hold), ≈ 0 for A-min/B |

Aggregated across events: median and IQR of `hold_e`, `cond_max_e`, `block_ratio_e`. This is the **mechanism** metric; `stall_time_share` is the **exposure** metric; `y_p99` is the **load-controlled** metric. All three are reported; none is called "tail latency" without its qualifier.

---

## 5. Clock, Overhead, and Memory

| Concern | Decision |
| :--- | :--- |
| Clock source | `CLOCK_MONOTONIC` via Go's monotonic `time.Now` (two calls/op ≈ 40–50 ns) |
| Overflow | existing split arithmetic retained (`timer_common.go`) |
| Recorder | per-worker pre-allocated `[]int64` of capacity `ceil(1.25 × R × duration)`; no growth in the window; `samples_dropped` reported if exceeded |
| Memory | 4,500 requests × 4 int64 ≈ 144 KiB per trial — negligible |
| Percentiles | nearest-rank on the full sample; no histogram approximation |
| Censoring | abandoned requests are counted and reported; `y_p99.9` for a trial with `abandon_rate > 0` is reported as a **lower bound** |
| Affinity | all workers share the process affinity mask ([05](05_PLATFORM_VERIFICATION.md) §4); no per-worker pinning |

---

## 6. Validity Checks (fail-closed, evaluated before analysis)

| ID | Check | Action on failure |
| :--- | :--- | :--- |
| V-1 | `achieved_rate / R ≥ 0.98` at L1/L2 | mark trial `UNDER_DELIVERED`; excluded from latency inference, retained for throughput |
| V-2 | `abandon_rate ≤ 0.02` at L1/L2 | same |
| V-3 | realized publications = 20 in E4/E2 | mark `EVENT_COUNT_MISMATCH`; excluded (this is the B2 defect being fixed) |
| V-4 | `q_mean < 1 ms` at L1 | flags an over-capacity calibration; cell reported but not used for the knee |
| V-5 | sample count ≥ 1,000 | else p99.9 = `null` |
| V-6 | schedule hash matches the pre-registered hash | abort trial |
| V-7 | `max_service < W_max` | else `W_max` is censoring the distribution; report and flag |

---

## 7. Relationship to the Closed-Loop Blocks

E1/E2/E3/E5 remain closed-loop, because their question is *"at fixed concurrency, how much work does each arm complete and how long does a publication block a reader?"* — a comparison of achieved work, not of latency at load. They adopt from this document:

- stall-event statistics (§4.2),
- event-conditioned latency (§4.3),
- per-worker sharded recording and the removal of `readAdmitted`,
- explicit reporting of `k`, flush counts, write counts, and `records_scanned`.

The manuscript will then be able to say, for each claim, which of the three metric families supports it:

| Claim family | Metric family | Block |
| :--- | :--- | :--- |
| "B/A-min complete more work at fixed concurrency" | achieved throughput (closed loop) | E1, E3, E5 |
| "A publication blocks an arriving reader for X ms; B/A-min do not" | event-conditioned latency + hold | E2 |
| "At a matched offered load, response latency differs by Y" | response latency (open loop) | E4 |
| "The mechanism is the critical-section duration" | dose-response in `k` + instrumented lock occupancy | E5, D2 |
