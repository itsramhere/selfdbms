# Phase I7.0 — Final Experimental Strengthening Protocol

**Campaign ID:** `I7_STRENGTHENING_V1`
**Protocol version:** `1.2.0-windows` (amended 2026-10-28)
**Status:** **G1 COMPLETE. G2 PARTIAL. NO PERFORMANCE TRIAL EXECUTED.**
**Prepared:** 2026-10-28
**Base revision at preparation:** `f1a561a364b4e6719b3867400370a5b6228cba33` (working tree *dirty*; see §8)
**Execution platform (amended):** **Windows 11 Home, NTFS, internal Samsung NVMe, Go 1.26.3 windows/amd64, PowerShell** — see [12_WINDOWS_EXECUTION_AMENDMENT.md](12_WINDOWS_EXECUTION_AMENDMENT.md)
**Superseded by the amendment:** the bare-metal Linux plan in [05_PLATFORM_VERIFICATION.md](05_PLATFORM_VERIFICATION.md) and the cost figures in [06_TRIAL_INVENTORY.md](06_TRIAL_INVENTORY.md) §9

> **Platform amendment.** The operator has no dedicated bare-metal Linux machine. All I7 execution is Windows-native; no Ubuntu, WSL2, Bash or POSIX tooling is required. The frozen WSL2/ext4 campaigns stay frozen and are never pooled with I7. I7 does **not** validate bare-metal Linux behaviour. Read [12](12_WINDOWS_EXECUTION_AMENDMENT.md) before any other document in this package.

---

## 1. Authorization Compliance

| Directive | Status |
| :--- | :--- |
| Phase I7.0 authorization (design + read-only audit) | **Complied in the previous turn.** Documents only. |
| Windows amendment: A-min G1 implementation | **Authorized and completed.** Evidence: [13_G1_REVIEW_EVIDENCE.md](13_G1_REVIEW_EVIDENCE.md). |
| Windows amendment: PowerShell automation (G2) | **Partial.** `run_i7.ps1` + `cmd/i7trial` validated; E2/E4/E3-equal/E7/D1/D2 drivers still open ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §11). |
| 960-trial campaign frozen | **Untouched.** `research/experiments/data/campaign_wsl2_ext4_restart_v1/` read only; SHA-256 of its manifest recorded. |
| 600-trial campaign frozen | **Untouched.** `research/experiments/data/targeted_validation_v1/` read only; SHA-256 of its manifest recorded. |
| Executables and manifests unchanged | **Untouched.** `experiment_runner.exe`, `experiment_runner_linux`, `targeted_runner`, both `manifest_checksums.json` were only hashed, never modified. |
| Do not execute benchmarks | **Complied.** No performance trial executed. Only 400 ms functional smokes on an 80-key fixture, labelled engineering validation. |
| Pebble: verify Windows compatibility | **Complied.** v1.1.5 builds CGO-free and passes a full lifecycle smoke test; still descriptive only. |

All numbers quoted in this package are either (a) read directly from the frozen campaign JSON, or (b) arithmetic over those numbers, and are labelled with their source file pattern.

---

## 2. What This Package Is

A single, bounded, prospective extension that answers five reviewer objections **without repeating the 1,560 historical trials**:

| # | Reviewer concern | Addressed in |
| :--- | :--- | :--- |
| R1 | Is a correct minimal synchronization-fix baseline (A-min) sufficient, or is snapshot versioning actually required? | [01](01_A_MIN_DESIGN.md), [06](06_TRIAL_INVENTORY.md) E1–E3, [10](10_CAUSAL_VS_E2E.md) |
| R2 | External reference engine (Pebble; Badger only if justified) | [02](02_COMPATIBILITY_MATRIX.md), E7 |
| R3 | Bare-metal Linux + NVMe, CPU affinity, randomized order, deterministic seeds, reproducible builds | [05](05_PLATFORM_VERIFICATION.md), [08](08_HARNESS_CHANGES.md) |
| R4 | Reader pacing / coordinated omission; open-loop offered load; Workload D overhead isolation | [03](03_PACING_AUDIT.md), [04](04_OFFERED_LOAD_METHODOLOGY.md), [09](09_WORKLOAD_D_PROFILING.md) |
| R5 | Separate end-to-end comparisons from causal mechanism tests | [10](10_CAUSAL_VS_E2E.md), [07](07_ANALYSIS_PLAN.md) |

## 3. What This Package Is Not

- It is **not** an implementation plan for production code. A-min is specified to the level required to review its correctness, not to merge it.
- It is **not** a re-analysis of the frozen campaigns. Those are used only as (i) priors for variance and (ii) audit targets.
- It does **not** authorize execution. Execution requires the gate sequence in §7 and explicit reviewer sign-off on the open decisions in §9.
- It does **not** propose changing the read path of the engine. §5 shows the read path is the dominant cost term, and I7 holds it constant by design.

---

## 4. The Central Scientific Reframing

The historical study compares two designs. Reviewers correctly note that a third design — **A-min**: keep Approach A's single `RWMutex`, but move the SSTable scan *outside* the read critical section with a per-reader lease — was never tested. A-min is ~40 lines, contains no version chain, no snapshot objects, no retirement worker.

If A-min reproduces most of Approach B's benefit, then the paper's current causal story ("snapshot pinning is what fixes reader–writer interference") is wrong, and the correct story is:

> **What matters is the duration of the serialized critical section, not the versioning mechanism.** A short read-side critical section restores writer progress; versioning only pays for itself when the reader must observe a *consistent multi-table version* across a changing table set, or when per-reader lease bookkeeping grows with reader-set cardinality.

That is a *better* paper, but it is a different claim, and it must be tested prospectively. I7 is designed to discriminate the two stories with a small, fully crossed three-arm ladder.

### 4.1 The three-arm ladder

| Arm | Read critical section | Reader-set lifetime | Publication cost |
| :--- | :--- | :--- | :--- |
| **A** (frozen) | `m.Mu.RLock()` held across the entire tiered lookup **including every SSTable scan** | Implicit (lock excludes mutation) | `m.Mu.Lock()`; must wait for all in-flight readers to drain |
| **A-min** (new) | `m.Mu.RLock()` held only for memory-tier lookup + reader-set capture + O(k) lease acquisition | Explicit per-reader `inflight` lease, freed by last releaser | `m.Mu.Lock()`; O(k) retirement bookkeeping; frees deferred to lease drain |
| **B** (frozen) | `m.Mu.RLock()` for memory tier + 2× `snapshotMu.Lock()` (pin/unpin) per disk read | Immutable `ReaderSnapshot` + refcounted `SharedReader` chain + async reclamation | `snapshotMu.Lock()`; allocates a new version + O(k) `Ref()` calls |

A → A-min is a **single-variable** causal contrast (one structural change: unlock before scan, plus the lease that makes it safe).
A-min → B is a **design-bundle** contrast (three changes at once) and is explicitly *not* treated as causal in [10](10_CAUSAL_VS_E2E.md).

---

## 5. Read-Only Audit Findings (already actionable, no new trials)

These were derived by reading the engine source and re-reading frozen JSON. They change what I7 must measure. Full detail in [03](03_PACING_AUDIT.md).

### AF-1 — The "disk tier" is not disk; the read critical section is a CPU-bound linear scan

`SSTableReader.Get` ([lsmengine/sstableReader.go:83](../../../lsmengine/sstableReader.go)) binary-searches a sparse index (one entry per 4 KiB block), then **sequentially scans every record from the located block to the end of the data region**, decoding with `binary.Read` (reflection) and allocating `make([]byte, klen)` and `make([]byte, vlen)` per record.

Consequence, measured on the frozen data: in Workload A at `r=1`, `p50 = 10.83 ms` and `p95 = 31.33 ms` per *point* read, throughput 72.5 ops/s, on a working set of ≈1.5 MB that is entirely page-cache resident after warmup (`wsl2_ext4_WORKLOAD_A_READ_ONLY_A_r01_rep01.json`). The p95 ≈ 3× p50 is the hit/miss mixture: a hit scans ~2 tables, a miss scans all 4.

**Therefore:**
- The phrase "holds `RLock` across blocking POSIX disk I/O" is not supported for these workloads. The lock is held across ~10–31 ms of user-space scanning and allocation.
- The *direction* of every historical effect is still valid (a 10 ms critical section will starve writers far worse than a 10 µs one).
- The *magnitude* (3.3–18.5× write progress) is an **upper bound** that scales with the engine's read-path inefficiency, not a general prediction for production LSM engines. The paper must say so.

### AF-2 — The p95 tail metric is a sampling-structure artifact (coordinated omission, quantified)

Readers are closed-loop: `for { sample; Get; record; }` with no pacing and no think time ([runner.go:493–533](../../experiments/harness/runner.go)). Each reader therefore contributes **at most one sample per stall**, regardless of stall length.

In Workload B2 the number of injected publications is 20. The number of samples that can observe a stall is therefore `20 × r`. With `N = readOpsPerSec × 10s`:

| r | N (A) | stall-capable samples `20r` | share | reported p95 (A) |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 397 | 20 | 5.0 % | 45.2 ms |
| 2 | 726 | 40 | 5.5 % | 175.4 ms |
| 4 | 1,359 | 80 | 5.9 % | 181.0 ms |
| 8 | 1,958 | 160 | 8.2 % | 220.9 ms |
| 16 | 1,971 | 320 | 16.2 % | 298.7 ms |
| 32 | 2,493 | 640 | 25.7 % | 370.8 ms |

`p95` therefore does not measure a latency distribution. It sits at the stall boundary and grows monotonically with `r` because the *denominator of stall samples* grows. The 73–76 % "suppression" at `r=2,4` is, strictly, a **ratio of stall durations** — readable only in the two cells where the `20r` stall population happens to straddle the 95th percentile; outside that window the metric is either undefined (`r=1`) or dominated by a different stall source (`r≥8`).

### AF-3 — Workload B2 injects uncounted writes and drops publications

The B2 coordinator ([runner.go:604–646](../../experiments/harness/runner.go)) commits a **50-key transaction** every 500 ms and only then calls `TriggerFlush()` + `WaitForFlushCompletion()`. `Transaction.Commit` holds `m.Mu.Lock()` for the whole batch, doing **50 sequential WAL `fsync`s** ([transaction.go:57–114](../../../lsmengine/transaction.go), [wal.go:28–69](../../../lsmengine/wal.go)). Those writes are not counted in `write_ops_*` because the coordinator bypasses the counters.

Verified in the frozen JSON: a B2 `A r=4` trial reports `write_ops_attempted = 0` while carrying 1,000 `fsync`ed writes, `publication_duration_ms = 450`, and `reads_during_publication_count = 76`.

Because the coordinator blocks until the flush completes, the *realized* publication count is treatment-dependent:

| r | A realized publications (of 20) | B realized |
| ---: | ---: | ---: |
| 1–16 | 20 | 20 |
| 24 | 19 | 20 |
| 32 | **16** | 20 |

(`lsm_diagnostics.flushes_completed` − 4 fixture flushes; e.g. A r=32 total = 20.)

A tail-latency comparison in which one arm receives 20 stall events and the other 16 is not a comparison of latency; it is a comparison of exposure.

### AF-4 — Workload D's "reading throughput penalty" is largely a work-composition shift

Frozen medians (`*WORKLOAD_D_READER_CHURN_*`):

| r | A read / write / **total** ops/s | B read / write / **total** ops/s | read ratio | write ratio | **total ratio** |
| ---: | :--- | :--- | ---: | ---: | ---: |
| 1 | 475.4 / 235.2 / **710.6** | 298.0 / 297.5 / **595.6** | 0.63 | 1.26 | **0.84** |
| 2 | 904.8 / 224.0 / **1,128.8** | 598.9 / 288.1 / **887.1** | 0.66 | 1.29 | **0.79** |
| 4 | 1,701.7 / 210.5 / **1,912.2** | 1,267.3 / 289.9 / **1,557.5** | 0.74 | 1.38 | **0.81** |

The "26–37 % throughput penalty" is a 26–37 % *read* penalty in a fixed-duration trial in which B also completed 26–38 % more writes. On total completed operations the gap is 16–21 %. B also performed 27 % more flushes (28 vs 22 measured at `r=1`), i.e. more background work.

### AF-5 — Workload D's mechanism cannot be descriptor allocation (arithmetic)

At `r=1`, B's median read latency is **3.17 ms** vs A's **1.11 ms** (+2.06 ms). The entire snapshot bookkeeping per read is two `snapshotMu` acquire/release pairs plus one pin increment — tens of nanoseconds uncontended, a few hundred under contention, against a 2–3 ms scan. Descriptor allocation happens *per publication* (≈28 flushes + 15 compactions in 10 s), not per read. The 2 ms/read gap is therefore **waiting**, not CPU.

The consistent explanation is mutual-exclusion sharing: at `r=1` the `m.Mu` mutex is the contended resource. A's reader holds `RLock` for ~2 ms per read, so writers mostly lose the lock and complete 22 flushes worth of work. B's reader holds `RLock` for microseconds, so writers win the lock ~1.3× more often and hold it for the whole commit (tiered conflict check + `fsync`); each commit blocks a B reader in turn. Both arms are "fair", they allocate the same lock differently. **This is a hypothesis, not a result** — it is the primary target of E3 + the D profiling block.

### AF-6 — The read path is the same in every arm, so arm contrasts are still valid

`SSTableReader.Get` is shared by A, A-min and B. The scan cost is a constant additive term in every arm. Arm contrasts therefore isolate the synchronization term; absolute throughput does not describe the engine.

### AF-7 — Fixture construction, not the measurement window, dominates campaign cost

Measured `fixture_manifest.initialization_duration_ms` (frozen, WSL2 ext4, medians): Workload A/B1 31.7 s, B2 30.7 s, C 42.0 s, D 40.5 s, E 9.2 s. Each fixture commits 2,500 keys per table, one at a time, each commit performing a full tiered `latestValueLocked` conflict check plus a WAL `fsync`. Cost is `O(tables × keys²)` in scan work.

The previously circulated estimate "280 trials × 6.2 s ≈ 29 min" undercounted the real per-trial cost by roughly 7×. All cost figures in [06](06_TRIAL_INVENTORY.md) use the measured fixture cost.

### AF-8 — Instrumentation serializes the read path being measured

Every read takes two global mutexes *after* timing stops but *before* the next read starts: `LatencyRecorder.mu` ([metrics.go:56–70](../../experiments/harness/metrics.go)) and `FlushTracker.mu` ([flush_tracker.go:64–78](../../experiments/harness/flush_tracker.go)). At `r=32` on a 6-core host this is a measurement-path bottleneck whose cost is a function of `r` and of throughput — i.e. it is correlated with the treatment. Separately, `readAdmitted` is incremented unconditionally on the line after `readAttempted`, so the schema's "admitted" is identically equal to "attempted" and carries no information.

---

## 6. Study at a Glance

| Block | Purpose | Cells | N/cell | Trials |
| :--- | :--- | :--- | ---: | ---: |
| **E1** | Starvation decomposition (A vs A-min vs B), 2 writers | 2 `r` × 3 arms | 12 | 72 |
| **E2** | Publication tail, publication-only driver | 2 pub modes × 3 arms | 10 | 60 |
| **E3** | Churn: free-work vs equal-work | 2 `r` × 2 regimes × 3 arms | 8 | 96 |
| **E4** | Open-loop offered load (3 absolute rates) | 3 rates × 3 arms | 6 | 54 |
| **E5** | Read-critical-section dose-response (all-miss) | 3 arms | 6 | 18 |
| **E7** | Pebble external anchor (unpaired, descriptive) | 3 workload shapes | 8 | 24 |
| **Preflight** | Engineering validation only (never reported) | 4 triplets | 1 | 12 |
| **D1** | Workload-D profiling (separate processes) | 3 arms × 3 reps | — | 9 |
| **D2** | Instrumented mechanism runs (separate build) | 3 arms × 5 | — | 15 |
| **D3** | Platform-drift reference triplets (start/end) | 2 × 3 arms | — | 6 |
| **Total** | | | | **366** |

- **Hard ceiling: 384 trials** (366 planned + 18 invalid-only replacements, ≤ 4.9 %).
- **Reduction vs history:** 366 / 1,560 = **23.5 %**; 366 / 960 = 38 %; 366 is 131 % of the unexecuted corrective campaign but with a strictly larger inferential scope (adds A-min, open-loop, external anchor, k-dose, profiling).
- **Execution cost (Windows, amended):** **≈3.4 h** at 27 s/trial (13.2 s NTFS fixture measured from the archived Windows campaign + 12.2 s window/warmup + teardown). Linux estimate superseded. Derivation: [12](12_WINDOWS_EXECUTION_AMENDMENT.md) §8 and [06](06_TRIAL_INVENTORY.md) §9.
- **Currently runnable:** **144 confirmatory rows + 12 preflight** of 366; the remaining 186 need the E2/E4/E3-equal/E7/D1/D2 drivers ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §5, §11).

---

## 7. Gate Sequence (no gate may be skipped)

```
G0  Reviewer sign-off on this package + open decisions OD-1..OD-8        [done for design]
 │
G1  A-min implementation + correctness gate                             [COMPLETE - 13]
 │    - 7 deterministic/empirical tests (01 §9)                          [PASS]
 │    - observational-equivalence oracle A vs A-min (203 ops)            [PASS]
 │    - allocation parity (109 vs 109 allocs/read)                       [PASS]
 │    - NTFS handle-lifetime + shutdown drain tests                      [PASS]
 │    - race detector                                                    [UNAVAILABLE - cgo/C compiler]
 │
G2  Windows harness + PowerShell automation                             [PARTIAL - 12 §5/§11]
 │    - run_i7.ps1 -Check / -DryRun / gate refusals / -Package           [PASS]
 │    - cmd/i7trial closed-loop trials (E1, E3-free, E5, D3, PF)          [READY]
 │    - E2 / E4 / E3-equal / E7 / D1 / D2 drivers                         [OPEN]
 │
G3  Preflight: 12 trials (4 triplets), 1.5 s window                     [NOT AUTHORIZED]
 │    - variance calibration, fixture cost, runlist hash publication
 │
G4  Confirmatory: E1–E5 (300) + E7 (24) + D3 (6)                        [NOT AUTHORIZED]
 │
G5  Diagnostics: D1 (9) + D2 (15) — separate builds, never pooled        [NOT AUTHORIZED]
 │
G6  Preregistered analysis (07) → manuscript revision
```

**Futility stop:** if G1 fails, stop and publish the A-min design without empirical claims. (It did not fail.)
**No outcome-based early stopping at G4.** See [07](07_ANALYSIS_PLAN.md) §8.

---

## 8. Provenance Prerequisites Before G2

The working tree at preparation is dirty (`paper.tex` modified; `paper.aux`, `paper.log`, `paper.pdf`, `paper/figures/` untracked). I7 requires:

1. A clean commit containing exactly: this package, the A-min implementation, harness v2, and the manifest generator.
2. `git status --porcelain` empty at campaign start and end; the campaign aborts otherwise (already implemented in `research/experiments/provenance`).
3. `-trimpath` reproducible build verified by double-build SHA-256 equality (05 §6).
4. The ordered trial list and its SHA-256 committed **before** the first trial (08 H-16).
5. No writes anywhere under the two frozen data directories; verified by re-hashing both `manifest_checksums.json` before and after.

---

## 9. Open Decisions Requiring Reviewer Sign-Off

| ID | Decision | Default proposed | Why it matters |
| :--- | :--- | :--- | :--- |
| **OD-1** | Retire the unexecuted `I6_CORRECTIVE_MECHANISMS_V1` campaign in favour of I7 | **Retire** | Two simultaneously-open preregistrations on overlapping questions is a research-integrity hazard. I7 subsumes T1/T3 and adds A-min. The I6 *design work* (provenance, seed derivation, window profiling) is carried forward, not discarded. |
| **OD-2** | Include Pebble at all | **Include, anchored** | Absolute ratios are uninformative (AF-1), but a mature reference bounds the claim "this is a synchronization phenomenon, not an engine artifact" and gives reviewers a familiar number. Framed descriptively, no hypothesis test. |
| **OD-3** | Include Badger | **Exclude** | Mismatched durability semantics (value log + `SyncWrites`), no equivalent of the A arm, and the added architectural distance buys no discrimination for R1–R4. Rationale in [02](02_COMPATIBILITY_MATRIX.md) §6. |
| **OD-4** | Bulk-fixture loader (H-09) | **Approve** | Cuts per-trial cost by removing the `O(keys²)` conflict-check loop. On Windows the fixture is 13.2 s of a 27 s trial, so the payoff is ~2×, not ~3×. Equivalence proof (byte-identical fixture SSTables) still required. |
| **OD-5** | Equivalence margins | ±10 % for read throughput A-min vs B; ±20 % bound for write progress A-min vs B; ±15 % for latency ratios | Margins must be fixed before data. Rationale and power table in [07](07_ANALYSIS_PLAN.md) §5. |
| **OD-6** | Cold-read condition via `posix_fadvise(DONTNEED)` | **WITHDRAWN (Windows).** No equivalent without `O_DIRECT` or admin cache clearing | All Windows measurements are warm-cache; stated in scope ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §4.7). |
| **OD-7** | Report A-min's Θ(k) lease cost honestly | **Yes: report `k` and per-read lease count in every trial** | A-min is not a free lunch: its per-read metadata cost is Θ(k) while B's is Θ(1). E5 and D2 exist to measure exactly this. |
| **OD-8** | Accept the Windows-only platform scope | **Accept** | Every I7 claim is scoped to Windows 11 / NTFS / this device. No bare-metal Linux, WSL2 or enterprise-NVMe claim may be derived from I7. |

---

## 10. Falsification Summary — What Would Change the Paper

| Observation (G4) | Consequence for the manuscript |
| :--- | :--- |
| A-min write progress ≈ B (capture fraction ≥ 0.7) | **Rewrite the causal claim**: starvation is caused by critical-section length, not versioning. B's contribution narrows to consistent multi-table snapshots + bounded publication cost. |
| A-min write progress ≈ A | Versioning *is* required; strengthen the existing claim, and report A-min as the null control that failed. |
| A-min publication tail ≈ B | Same reframing for the B2 claim; the 73–76 % figure is re-scoped as "any short critical section suffices". |
| Equal-work churn shows no B penalty | Replace the "descriptor overhead" mechanism with the lock-sharing explanation (AF-5); Workload D's negative finding becomes a *scheduling* result. |
| Open-loop response latency shows smaller gaps than closed-loop p95 | Quantify the coordinated-omission inflation and report both; the abstract's tail numbers become "stall exposure under a fixed injection schedule". |
| Pebble is 2–3 orders of magnitude faster | Confirm AF-1 publicly; keep Pebble descriptive and move the read-path limitation into Threats to Validity. |
| k-dose shows A degrades monotonically with k while A-min/B do not | Direct causal support for the critical-section-length mechanism; strongest possible answer to R1. |

---

## 11. Document Index

| File | Contents |
| :--- | :--- |
| [01_A_MIN_DESIGN.md](01_A_MIN_DESIGN.md) | Correctness-preserving A-min design, handle lifetime, reclamation, invariants, proofs, test plan |
| [02_COMPATIBILITY_MATRIX.md](02_COMPATIBILITY_MATRIX.md) | Operation and durability compatibility matrix for A, A-min, B, Pebble, Badger |
| [03_PACING_AUDIT.md](03_PACING_AUDIT.md) | Reader pacing and coordinated-omission audit with frozen-data evidence |
| [04_OFFERED_LOAD_METHODOLOGY.md](04_OFFERED_LOAD_METHODOLOGY.md) | Offered-load levels, open-loop scheduler, latency definitions, event-conditioned metrics |
| [05_PLATFORM_VERIFICATION.md](05_PLATFORM_VERIFICATION.md) | Hardware/storage verification without partition modification; affinity; reproducible builds |
| [06_TRIAL_INVENTORY.md](06_TRIAL_INVENTORY.md) | Complete inventory, statistical justification, execution cost, hard trial limit |
| [07_ANALYSIS_PLAN.md](07_ANALYSIS_PLAN.md) | Preregistered estimands, tests, multiplicity, power, decision rules, stopping rule |
| [08_HARNESS_CHANGES.md](08_HARNESS_CHANGES.md) | Enumerated harness changes with rationale and frozen-data impact |
| [09_WORKLOAD_D_PROFILING.md](09_WORKLOAD_D_PROFILING.md) | Workload-D profiling plan, separated from primary trials |
| [10_CAUSAL_VS_E2E.md](10_CAUSAL_VS_E2E.md) | Explicit end-to-end vs causal-mechanism separation and the assumptions of each |
| [12_WINDOWS_EXECUTION_AMENDMENT.md](12_WINDOWS_EXECUTION_AMENDMENT.md) | **Windows amendment:** errata, NTFS/timer/scheduling semantics, gates, cost, runner contract, approval workflow |
| [13_G1_REVIEW_EVIDENCE.md](13_G1_REVIEW_EVIDENCE.md) | **G1 evidence:** A-min tests, allocation parity, race-detector determination, NTFS validation, timing, Pebble |
| [run_i7.ps1](run_i7.ps1) | Campaign driver: `-Check`, `-Preflight`, `-Campaign`, `-Resume`, `-Package`, `-DryRun` |
| [cmd/i7trial/main.go](cmd/i7trial/main.go) | One-trial-per-process runner with approval gate, platform handshake, QPC probes |
| [pebble_probe/FINDINGS.md](pebble_probe/FINDINGS.md) | Pebble v1.1.5 Windows compatibility transcript |

### Reading order for a reviewer

1. [12](12_WINDOWS_EXECUTION_AMENDMENT.md) — what changed and why (10 minutes).
2. [13](13_G1_REVIEW_EVIDENCE.md) — what was actually built, run and measured.
3. [01](01_A_MIN_DESIGN.md) — the A-min design and its proofs (unchanged except §3).
4. [03](03_PACING_AUDIT.md) + [04](04_OFFERED_LOAD_METHODOLOGY.md) — why the old tail metric is not usable and what replaces it.
5. [06](06_TRIAL_INVENTORY.md) + [07](07_ANALYSIS_PLAN.md) — the inventory and the preregistered analysis.
6. [10](10_CAUSAL_VS_E2E.md) — which claim may be stated how.
