# 09 — Workload D Overhead Isolation and Profiling Plan

**Status:** DESIGN ONLY. No profiling run executed. **Windows-amended 2026-10-28.**
**Separation rule:** profiling runs (D1) and instrumented runs (D2) are **never** pooled with the primary performance trials (E1–E5). They use separate processes, separate builds, and separate output directories, and they appear in the manuscript only in the mechanism section.

> **Windows amendment.** The plan is unchanged in structure; four implementation notes.
> 1. `pprof` CPU/mutex/block/alloc profiles work identically on Windows; the same window hooks apply.
> 2. Window boundaries and any timing inside the profiler must use QPC, not `time.Now` (~404 µs steps).
> 3. Defender real-time scanning is enabled and cannot be disabled by protocol; scans can inflate `fsync` and `open` tails. Record `Get-MpComputerStatus` alongside each profile and report it, so a Defender-induced tail is not attributed to the engine.
> 4. Laptop thermal throttling is uncontrolled; record the CPU clock before/after each profile and discard a profile whose clock moved by more than 5 % (the pre-registered re-run rule, §7).
>
> The D1/D2 runners are **not implemented yet** (G2 gap, [12](12_WINDOWS_EXECUTION_AMENDMENT.md) §11); this document remains the plan they must implement.

---

## 1. Objective

The frozen Workload D result — Approach B reads 26–37 % fewer ops/s at `r ≤ 4` — is currently attributed to "snapshot descriptor allocation, atomic reference counting, and deferred reader retirement". The audit ([03](03_PACING_AUDIT.md) §5) shows this attribution is arithmetically impossible:

| Quantity | Magnitude |
| :--- | :--- |
| Observed gap at `r=1` | **+2.06 ms** median read latency (A 1.11 → B 3.17 ms) |
| Published microbenchmark cost of acquire/release | **42–68 ns** per op |
| Descriptor allocation frequency | per publication (≈ 28 flushes + 15 compactions / 10 s), not per read |
| Per-read snapshot/refcount work | Θ(1) — a few atomics |

So the task is not to "measure the overhead" but to **identify which term actually accounts for 2 ms**. The plan below is a decomposition with a closure test, not a fishing expedition.

---

## 2. Competing Hypotheses (fixed before profiling)

| ID | Hypothesis | Prediction if true |
| :--- | :--- | :--- |
| **H1** | Snapshot descriptor/refcount CPU cost per read | `Δt_meta` is a large fraction of Δ; allocs/read and GC time differ materially; the microbenchmark shows Θ(1) cost in the hundreds of ns |
| **H2** | Lock-occupancy sharing on `m.Mu` | B's writer lock occupancy is ≥ 1.5× A's; readers' `m.Mu` wait accounts for ≥ 50 % of Δ; median latency gap shrinks as `r` grows (observed: it does — 2.06 ms at `r=1`, ≈ 0 at `r≥16`) |
| **H3** | Unequal work composition | Equal-work pacing (E3) removes the gap; flush counts and write counts explain most of the between-arm difference |
| **H4** | Reader-set cardinality | `k` differs between arms; `records_scanned` per read explains ≥ 50 % of the scan-time difference |
| **H5** | Allocation/GC pressure | Alloc bytes/op and GC cycles differ; GC pause time explains the gap |
| **H6** | Storage behaviour | Device read bytes or fsync latency differ; unlikely given the 1.5 MB footprint (page-cache resident) |

H2 and H3 are the audit's working hypotheses. H1 is the manuscript's current claim. The plan is designed to separate all six.

---

## 3. Frozen Baseline to Reproduce

| `r` | A read / write / total ops/s | B read / write / total ops/s | A p50 | B p50 | A flushes | B flushes |
| ---: | :--- | :--- | ---: | ---: | ---: | ---: |
| 1 | 475.4 / 235.2 / 710.6 | 298.0 / 297.5 / 595.6 | 1.11 ms | 3.17 ms | 22 | 28 |
| 4 | 1,701.7 / 210.5 / 1,912.2 | 1,267.3 / 289.9 / 1,557.5 | 1.07 ms | 3.22 ms | 20 | 27 |

(Source: `research/experiments/data/campaign_wsl2_ext4_restart_v1/wsl2_ext4/*WORKLOAD_D_READER_CHURN_*.json`, medians of 10 reps.)

The profiling configuration must reproduce this **shape** (B slower on reads, faster on writes, ~27 % more flushes). If it does not, the profiles are describing a different phenomenon and must not be used to explain the frozen result.

---

## 4. D1 — Profile Runs (9 runs)

| Property | Value |
| :--- | :--- |
| Arms | A, A-min, B |
| Repetitions | 3 each |
| Workload | Workload D shape: 4 SSTables, 10k keys, 95 % hit, **16 KiB** memtable, compaction trigger 1,500 ms, **2 unpaced writers**, `r = 4` |
| Window | 5,000 ms measurement, 1,000 ms warmup, 200 ms cooldown |
| Process | **one arm per OS process** (pprof state must not cross arms) |
| Profiling window | exactly `[OnMeasurementStart, OnMeasurementEnd]` — fixture, warmup and teardown excluded (existing hooks) |
| Profiles | CPU, mutex (`SetMutexProfileFraction(1)`), block (`SetBlockProfileRate(1)`), allocs, heap (no forced GC), goroutine, plus `runtime/trace` for 2 of 3 reps |
| Labels | `pprof.Do` labels per goroutine role: `reader`, `writer`, `flusher`, `compactor`, `cleanup` |
| Differential `MemStats` | `TotalAlloc`, `Mallocs`, `Frees`, `NumGC`, `PauseTotalNs` within the window |
| Overhead | recorded; profiles are never used for throughput claims |

### 4.1 Questions each profile answers

| Profile | Question |
| :--- | :--- |
| CPU (labeled) | What fraction of on-CPU time is `SSTableReader.Get` / `binary.Read` / `mallocgc` / `sync.(*Mutex).Lock` / `runtime.futex` in each arm? Is B's reader CPU higher than A's, or is it the same with more waiting? |
| Mutex | Which mutex has the largest delay, and how does delay per acquisition differ between `m.Mu`, `snapshotMu`, `wal.mu`? |
| Block | How much time do reader goroutines spend blocked (not runnable) and on what? |
| Allocs | Bytes and objects per read; does B allocate per read at all? |
| Heap | Retained memory attributable to snapshots/retired readers |
| Goroutine | Goroutine count and stacks at window end (cleanup worker, flush worker) |
| `runtime/trace` | Goroutine blocking events with timestamps → can be cross-correlated with publication windows |

### 4.2 Profile-derived quantities

| Symbol | Definition | Source |
| :--- | :--- | :--- |
| `t_cpu_read` | on-CPU ns per completed read | CPU profile / labels ÷ reads |
| `t_block_read` | off-CPU ns per read, split by blocking reason | block profile + trace |
| `t_meta` | ns per read in acquire/release/lease/refcount functions | CPU profile, function-level |
| `t_wal` | ns per write in `Wal.Append` (incl. `fsync`) | CPU + block profiles |
| `hold_writer` | Σ time `m.Mu` is held in write mode / window | trace + instrumented run |
| `hold_reader` | Σ `RLock` hold time / window | instrumented run (D2) |
| `alloc_read` | bytes allocated per read | allocs profile ÷ reads |
| `gc_time` | GC pause + assist time / window | MemStats / trace |

---

## 5. D2 — Instrumented Runs (15 runs) and Microbenchmarks

### 5.1 Instrumented build (`-tags i7instr`)

Per-operation counters, no pprof:

| Counter | Purpose |
| :--- | :--- |
| `reader_mu_wait_ns` | time a reader blocks acquiring `m.Mu.RLock` (H2) |
| `reader_mu_hold_ns` | time `RLock` is held (A: includes scan; A-min/B: excludes) |
| `writer_mu_wait_ns`, `writer_mu_hold_ns` | the writer side of the same mutex |
| `snapshotmu_wait_ns` | B's pin/unpin contention |
| `wal_mu_wait_ns`, `wal_fsync_ns` | commit path |
| `lease_count` | A-min's Θ(k) cost per read |
| `records_scanned` | scan length per read |
| `k_at_read` | reader-set cardinality at capture |
| `flushqueue_wait_ns` | admission path |

15 runs = 3 arms × 5 reps, same workload as D1, `r = 4` (and `r = 1` for 2 of the 5 reps, because the frozen gap is largest at `r=1`).

### 5.2 Microbenchmark sweep (no trials)

| Benchmark | What it measures |
| :--- | :--- |
| `BenchmarkReadMeta/{A,AMIN,B}/k={1,2,4,8,16,32}` | per-read metadata cost with a **stubbed** table (no scan): isolates `t_meta` and A-min's Θ(k) |
| `BenchmarkTableScan_k` | per-table scan cost for 2,500 records (the `t_scan` constant) |
| `BenchmarkCommitCriticalSection` | `m.Mu` write hold per commit, excluding `fsync`; then with `fsync` |
| `BenchmarkSnapshotPublish/k={4,8,16}` | publication cost per arm (B allocates a version; A-min marks k tables retired) |
| `BenchmarkKeyStream` | H-11 generator throughput |

These provide the constants used by the model in §6, and they cost no trials (12 microbenchmarks, capped).

---

## 6. Decomposition Model and Closure Test

For each arm, model the median read latency as:

```
t_read(arm) = t_lockwait(arm)          // m.Mu.RLock wait (H2)
            + t_meta(arm)              // acquire/pin/lease/refcount (H1)
            + S(arm) · c_scan          // records scanned × per-record cost (H4)
            + t_gc(arm)                // allocation + GC assist (H5)
            + t_other(arm)             // scheduler, syscalls, cache (residual)
```

where:

- `t_lockwait` and `t_meta` come from D2;
- `S(arm)` = measured mean `records_scanned` per read (new metric, H-20);
- `c_scan` from `BenchmarkTableScan_k`;
- `t_gc` from the alloc profile and MemStats;
- `t_other` is the residual.

**Closure test.** The reported quantity is

```
Δ_obs  = t_read(B) − t_read(A)
Δ_pred = Σ_terms (term_B − term_A)
closure = Δ_pred / Δ_obs        // target: 0.8 – 1.2
```

with a bootstrap CI over the 5 instrumented repetitions per arm. **If closure is outside [0.5, 2.0], the decomposition is declared failed** and the mechanism claim is withdrawn rather than replaced by speculation. This is the guard against a plausible-sounding story.

### 6.1 Hypothesis decision table (fixed before profiling)

| Observation | Conclusion |
| :--- | :--- |
| `Δt_meta / Δ_obs < 0.1` | **H1 rejected**: descriptor allocation cannot explain the gap; the manuscript's current attribution is withdrawn |
| `hold_writer(B) ≥ 1.5 · hold_writer(A)` **and** `Δ(t_lockwait) / Δ_obs ≥ 0.5` | **H2 supported**: the gap is lock-occupancy sharing |
| Equal-work E3 removes the gap **and** `Δ(writes)` explains `Δ(flush)` | **H3 supported**: composition, not mechanism |
| `Δ(S · c_scan) / Δ_obs ≥ 0.5` with `k_B > k_A` | **H4 supported**: reader-set size |
| `Δ(t_gc) / Δ_obs ≥ 0.3` | **H5 supported** |
| `read_bytes` or `fsync` latency differs by arm | **H6 supported** |
| closure outside [0.5, 2.0] | **inconclusive**: report the residual and stop attributing |

Multiple hypotheses may hold simultaneously; the report gives the fraction of Δ attributable to each, with intervals. No single hypothesis is declared "the cause" without closure.

---

## 7. Controls

| Control | Implementation |
| :--- | :--- |
| Profiling overhead | measured by comparing D1 throughput against the uninstrumented E3 cell at the same config; reported; no performance claim from D1 |
| Instrumentation overhead | D2 binary is separate; its throughput is reported but not used for inference |
| Cross-arm contamination | one arm per process; fresh data directory; no pprof state reuse |
| Order effects | D1/D2 run in randomized order (same runlist machinery) |
| Thermal drift | `MHZ` recorded before/after each run; runs > 5 % apart are re-run once |
| Fixture differences | the D fixture is built identically to E3; `k`, flush count and write count recorded so the "same phenomenon" check (§3) can be made |
| Page cache | warm (same as E3); `read_bytes` recorded to confirm |

---

## 8. Deliverables

1. **Table D-1** — per arm: reads, writes, p50/p95, `t_lockwait`, `t_meta`, `S`, `c_scan`, `t_gc`, residual, closure.
2. **Table D-2** — mutex profile: mutex name, acquisitions, total delay, mean delay, per arm.
3. **Table D-3** — alloc profile: bytes/op, objects/op, GC cycles and pause time, per arm.
4. **Table D-4** — microbenchmark sweep: `t_meta` vs `k`, per arm; `t_scan` per table; commit hold; publication cost.
5. **Figure D-1** — stacked bar decomposition of `Δ_obs` by term, per arm pair, with closure ratio annotated.
6. **Figure D-2** — `t_meta` vs `k` curves: the Θ(k) vs Θ(1) prediction.
7. **Statement** — which hypothesis survives, with the fraction of Δ and the closure value, or an explicit "unexplained".

---

## 9. Cost

| Item | Runs | Per run | Cost |
| :--- | ---: | ---: | ---: |
| D1 profiles | 9 | ≈ 50 s | 8 min |
| D2 instrumented | 15 | ≈ 55 s | 14 min |
| Microbenchmarks | 12 benchmarks | ≈ 30–60 s | 10 min |
| Repeat for drift | ≤ 3 | 55 s | 3 min |
| **Total** | | | **≈ 35 min** |

Included in the campaign budget ([06](06_TRIAL_INVENTORY.md) §9), not additive to it beyond the 24 diagnostic trials already allocated.

---

## 10. What This Plan Deliberately Does Not Do

- It does not attempt to *prove* the absence of snapshot overhead. It measures the term and compares it to the gap; if the term is small, the claim fails on its own terms.
- It does not add a fourth "B without snapshots" arm. That would require implementing a new design; A-min already supplies the structural contrast.
- It does not profile E1/E2. The publication path is already measured by event-conditioned latency (H-06), which is cheaper and more directly interpretable than a profile.
- It does not use pprof percentages as causal evidence. The profiles supply terms for a model whose closure is tested.
