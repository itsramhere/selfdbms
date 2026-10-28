# 08 — Required Changes in the Benchmark Harness

**Status:** PARTIALLY IMPLEMENTED (updated 2026-10-28). `H-01`, `H-12`, `H-13`, `H-15`, `H-16`, `H-18`, `H-23` are implemented or superseded by `cmd/i7trial` + `run_i7.ps1`; `H-02`..`H-11`, `H-14`, `H-17`, `H-19`..`H-22` remain open. Per-item status is marked below.
**Platform note:** the harness is **Windows-native** ([12](12_WINDOWS_EXECUTION_AMENDMENT.md)). `H-14` (page-cache control) is withdrawn on Windows; `H-05`/`H-06` must use QPC, not `time.Now`.
**Governing constraint:** the frozen campaigns (960 + 600 trials) must remain reproducible. Therefore:

- **No file used by the frozen runners is rewritten.** Only one additive `case` was added to `harness.RunTrial`'s sync-mode switch (`H-01`); new behaviour lives in `research/experiments/phase_i7/`.
- The frozen executables (`experiment_runner.exe`, `experiment_runner_linux`, `targeted_runner`) and their manifests are untouched and were only hashed.
- The paper's reproducibility sentence should stay anchored to **commit `4a06217`** rather than "the current source tree", because HEAD now contains new code. The frozen binary was **not** rebuilt in this phase (that requires the Linux toolchain and a clean WSL environment; it is a G2/G3 task, not claimed as done).

---

## 0. Implementation status (2026-10-28)

| Item | Status |
| :--- | :--- |
| H-01 third arm | **DONE** — `SyncModeApproachAMin`, one additive `case` in the harness switch |
| H-02 sharded latency recorder | **DONE (G2)** — `harness2/metrics.go` per-worker shards, merged after the window; no read-path mutex |
| H-03 per-worker flush tracking | **DONE (G2)** — `harness2/events.go` records publication windows with a single mutex per *event*, not per read |
| H-04 honest op accounting | **DONE (G2)** — scheduled/issued/completed/abandoned/unissued in `harness2.OperationCounts` |
| H-05 open-loop scheduler | **DONE (G2)** — `harness2/clock.go` QPC schedule, pre-computed exponential arrivals, `W_max` abandonment |
| H-06 event log + event-conditioned latency | **DONE (G2)** — `harness2/events.go`, `block_ratio = max_response / hold` |
| H-07 publication-only driver | **DONE (G2)** — engine injection gate + pre-frozen memtables; zero writes in the window |
| H-08 equal-work paced writer | **DONE (G2)** — token-free absolute QPC schedule at `W_paced` (235/210 writes/s) |
| H-09 bulk fixture | OPEN (optional) |
| H-10 lock-occupancy instrumentation | OPEN — blocks D2 |
| H-11 counter-based key stream | **DONE (G2)** — `harness2/keystream.go`, SHA-256 over (seed, op index) |
| H-12 affinity | **DONE (Windows form)** — `ProcessorAffinity` on the launcher, inherited; recorded per trial |
| H-13 real environment manifest | **DONE** — `run_i7.ps1 -Check` probes CIM/drive/topology; no hardcoded values |
| H-14 page-cache control | **WITHDRAWN** — no Windows equivalent; all trials warm-cache |
| H-15 reproducible build + provenance | **DONE** — `-trimpath`, binary SHA-256, Go env, frozen-artifact hashes |
| H-16 runlist permutation + commitment | **DONE** — deterministic SHA-256 ordering, committed hash, runner refuses a mismatch; D3 anchored first/last |
| H-17 attempt/replacement bookkeeping | **DONE (G2)** — STARTED/FINISHED ledger, per-attempt archive, budget persisted in the manifest |
| H-18 trial-count ceiling | **DONE** — enforced before any launch |
| H-19 drift monitor | **DONE (G2)** — D3 anchored first/last; the package computes start/end medians and flags >15 % |
| H-20 new per-trial metrics | **DONE (G2)** — `k` proxies, events, response latency, accounting, data-dir removal |
| H-21 analysis tooling | OPEN |
| H-22 Pebble adapter | **DONE (G2)** — `pebble_runner` module, v1.1.5, CGO-free, QPC timing, verified |
| H-23 schema versioning | PARTIAL — `i7.trial.v1` / `i7.ledger.v1` envelopes exist; the frozen v1 reader is unchanged |
| H-24 runner CLI | **DONE** — `-Check`/`-Calibrate`/`-Preflight`/`-Campaign`/`-Resume`/`-Diagnostics`/`-Package`/`-DryRun` |
| **H-25** | **DONE** — `cmd/i7trial`: one trial per process, bound approval, QPC probes, affinity readback |
| **H-26** | **DONE** — `run_i7.ps1`: platform validation, runlist generation, per-trial launch, resume, packaging |
| **H-27** | **NEW (G2)** — `cmd/i7stub` + `tests/orchestration_tests.ps1`: 25 synthetic orchestration scenarios |

Status detail and raw logs: [I7_G2_REVIEW_PACKET.md](I7_G2_REVIEW_PACKET.md).

---

## 1. Change List

Each item: **what / why / where / risk / frozen-data impact / verification.** Priority: **P0** = required for a valid study, **P1** = required for the paper's claims, **P2** = quality/cost.

### H-01 — Third arm (`SyncModeApproachAMin`) — P0

| | |
| :--- | :--- |
| What | New `SyncModeApproachAMin` value; harness accepts `"ApproachAMin_Lease"`; manifest/CSV/JSON carry it |
| Why | E1–E5 are three-arm triplets |
| Where | `lsmengine` (mode enum, dispatch), `runner.go` mode switch, schema |
| Risk | A third code path in the engine; gated by the G1 correctness suite |
| Frozen impact | None: the enum is additive and A/B dispatch is unchanged |
| Verify | `T-13` differential oracle; mode appears in every record |

### H-02 — Per-worker sharded latency recording — P0

| | |
| :--- | :--- |
| What | Replace the single `LatencyRecorder` (global `sync.Mutex` per sample) with one pre-allocated recorder per worker; merge and compute percentiles offline. If a recorder exceeds capacity, keep a uniform reservoir (seeded) and report `samples_dropped`. |
| Why | IA-1: a global mutex on the read loop is correlated with `r` and with throughput — i.e. with the treatment |
| Where | `harness2/latency.go` (new); `metrics.go` untouched |
| Risk | Percentiles must be computed over the merged multiset, not over per-worker percentiles |
| Frozen impact | None |
| Verify | Unit test: merged percentile of 3 shards == single-recorder percentile on the same sample stream; allocation count per record = 0 |

### H-03 — Per-worker flush/overlap tracking — P0

| | |
| :--- | :--- |
| What | Replace `FlushTracker.RecordPointRead()`'s mutex-protected read with per-worker counters and lock-free publication flags (`atomic.Bool` already); merge at the end |
| Why | IA-2: second global mutex on the read loop |
| Where | `harness2/flush_tracker.go`; keep the `FlushObserver` interface identical |
| Risk | The "reads during publication" counter must stay defined the same way (a read completing between `OnFlushPublishBegin` and `OnFlushPublishEnd`) so the Claim-13 audit remains valid |
| Frozen impact | None |
| Verify | Re-run the `TestMetricSemantics_ApproachA_ReadsDuringPublicationAudit` unit test against the new tracker |

### H-04 — Honest operation accounting — P0

| | |
| :--- | :--- |
| What | Remove `readAdmitted` (or redefine as "issued after a scheduling decision"); remove the dead `TimeoutOpsCount`; add `intended`, `issued`, `completed`, `abandoned`; record `window_ns` and `drain_ns` separately; define throughput over `window_ns` |
| Why | IA-3/IA-5: `admitted == attempted` always; the denominator silently includes drain |
| Where | `harness2/runner.go`; schema v2 |
| Risk | Breaks schema v1 consumers; keep both versions readable in the analysis layer |
| Frozen impact | None (new schema version) |
| Verify | Assert `admitted == attempted` no longer trivially true in a unit test with a saturated queue |

### H-05 — Open-loop scheduler — P0

| | |
| :--- | :--- |
| What | Pre-computed exponential arrival schedule (seeded), per-worker index claim, absolute-time sleep, `W_max = 250 ms` abandonment, full per-request record (`t_j`, `a_j`, `c_j`), schedule serialized + hashed |
| Why | E4; [04](04_OFFERED_LOAD_METHODOLOGY.md) |
| Where | `harness2/openloop.go` |
| Risk | A bug here silently changes the offered load; gated by a self-test that measures the realized inter-arrival distribution against the theoretical one (KS test, p > 0.01) before use |
| Frozen impact | None |
| Verify | Self-test on a null workload (no engine call): achieved rate within 1 % of `R`, KS test passes |

### H-06 — Publication event log and event-conditioned latency — P0

| | |
| :--- | :--- |
| What | For each publication, record `[begin, end]` (engine-measured, from `OnFlushPublishBegin`/`End`), the memtable id, and whether the build or the publication dominated. After the trial, tag every read whose `[a_j, c_j]` intersects `[begin − 10 ms, end + 10 ms]` and report the conditional distribution and `block_ratio = cond_max / hold` |
| Why | P2; the metric that is independent of injection count and `r` |
| Where | `harness2/events.go`, engine observer (already exists) |
| Risk | The post-hoc tagging must use the *same* clock domain as the read timestamps (monotonic ns) — assert both come from `NowPrecision` |
| Frozen impact | None |
| Verify | Deterministic barrier test: a synthetic 20 ms publication must produce `block_ratio ≈ 1` under A and ≈ 0 under A-min/B |

### H-07 — Publication-only driver — P0

| | |
| :--- | :--- |
| What | Replace the 50-key write-batch coordinator with: (a) a pre-freeze phase before the window that fills and queues `N_events` frozen memtables via `TriggerFlush` **before** measurement, and (b) a non-blocking injector that releases one queued memtable per scheduled absolute time, never calling `WaitForFlushCompletion` inside the schedule |
| Why | AF-3: B2 injects 1,000 uncounted `fsync`s and drops publications when the arm is slow |
| Where | `lsmengine` needs a small test-scoped seam: `FreezeActiveMemtableForTest()` and `ReleasePreFrozenForTest()` (or an existing equivalent); `harness2/publication.go` |
| Risk | Must not alter production flush semantics; the seams are test-only and must be listed in the diff audit |
| Frozen impact | None (new methods; existing paths untouched) |
| Verify | Realized publications = 20 in every arm; zero writes recorded; `measured_flushes == 20`; the injector's own lateness distribution is reported |
| Note | This is the single most important harness fix: without it, E2 repeats the B2 confound. |

### H-08 — Equal-work paced writer — P0

| | |
| :--- | :--- |
| What | A writer mode that attempts writes on an absolute-time token bucket at `W_paced` writes/s (token bucket, not `time.Sleep` per op, to avoid drift), records `attempted` and `completed`, and never spins when behind |
| Why | E3; removes the composition confound |
| Where | `harness2/writers.go` |
| Risk | With 1 writer, `W_paced ≈ 210–235/s` requires ≈ 4.3–4.8 ms per write; if a write takes longer, the bucket accumulates and the writer runs at capacity anyway → the manipulation check (MC-3) will catch it |
| Frozen impact | None |
| Verify | MC-3: realized writes within 15 % of target and within 10 % across arms |

### H-09 — Bulk fixture loader (optional, OD-4) — P2

| | |
| :--- | :--- |
| What | Build the 4 (or 8) fixture SSTables by streaming sorted internal keys directly into `SSTableBuilder`, assigning the same sequence numbers the transactional fixture would assign, with no per-key conflict check and no per-key `fsync` |
| Why | AF-7: fixture construction is 31–42 s/trial, 80 % of campaign wall clock, and is `O(keys²)` |
| Where | `harness2/fixture.go` |
| Risk | If the produced SSTables differ from the frozen fixture in any byte, the arm-internal comparability is preserved but comparability with history is weakened. **Equivalence proof required:** build the same fixture both ways and `sha256sum` the `.sst` files; they must match. If they do not, do not use H-09. |
| Frozen impact | None to the frozen data; affects only I7 fixtures |
| Verify | Byte-identical SSTables + identical readback for all sample keys + identical table count; recorded in `fixture_provenance.json` |

### H-10 — Lock-occupancy instrumentation (build tag) — P1

| | |
| :--- | :--- |
| What | Behind `-tags i7instr`: measure per-operation time blocked on `m.Mu` (read and write), `snapshotMu`, `Wal.mu`, `flushQueueMu`; total hold time by writer/reader; `fsync` time; records scanned per read; leases acquired per read. Export as counters, not profiles. |
| Why | D2; AF-5's hypothesis is about *lock occupancy*, which no current metric records |
| Where | `lsmengine` under the build tag only; `harness2/instrument.go` |
| Risk | Instrumentation costs 50–200 % overhead → a separate binary, never used for performance numbers |
| Frozen impact | None: tag off by default; released binary identical |
| Verify | `go build` without the tag produces a byte-identical binary to the pre-change build (assert in CI) |

### H-11 — Counter-based deterministic key stream — P1

| | |
| :--- | :--- |
| What | Replace the per-worker `rand` stream with `KeyIndex(opIndex) = SHA256(triplet_seed ‖ "key" ‖ opIndex) mod keyspace`, so request `j` uses the same key in every arm of a triplet |
| Why | IA-8; makes the paired design actually paired per operation |
| Where | `harness2/keystream.go` |
| Risk | SHA-256 per op costs ~300 ns — significant against a 10 ms read but not free. Mitigation: precompute a block of 4,096 indices per worker at a time (one hash per block, counter-derived offsets), amortizing to < 1 ns/op |
| Frozen impact | None |
| Verify | Unit test: the first 10⁶ indices are identical across arms for the same triplet seed; throughput of the generator > 50 M indices/s |

### H-12 — CPU affinity and `GOMAXPROCS` recording — P0

| | |
| :--- | :--- |
| What | `taskset` at launch; record `sched_getaffinity`, `runtime.GOMAXPROCS(0)`, `runtime.NumCPU()`; assert the mask matches the manifest |
| Why | R3, [05](05_PLATFORM_VERIFICATION.md) §4 |
| Where | `harness2/platform.go` |
| Risk | None |
| Frozen impact | None |
| Verify | Mask recorded in every trial; mismatch = hard failure |

### H-13 — Real environment manifest — P0

| | |
| :--- | :--- |
| What | Replace the hardcoded CPU/RAM/cores in `corrective_validation/cmd/main.go:17-31` (and the equivalent in the new runner) with parsed `/proc` + `/sys` values; fail closed on missing fields |
| Why | The current values would be *false* on the new platform |
| Where | `harness2/platform.go`; new runner only |
| Risk | Parsing fragility across distros; mitigate with `lsblk -J`/`lscpu` JSON where available and explicit fallbacks |
| Frozen impact | None (new runner) |
| Verify | Run on the target host; manifest complete; a deliberately missing field aborts |

### H-14 — Page-cache condition control — P1

| | |
| :--- | :--- |
| What | Optional `unix.Fadvise(fd, 0, 0, FADV_DONTNEED)` over each SSTable at measurement start; record `read_bytes` from `/proc/self/io` before/after |
| Why | Makes "cold" meaningful without touching the global page cache |
| Where | `harness2/cachecond.go` (Linux build tag) |
| Risk | Hint may be a no-op on busy pages; the `read_bytes` delta decides |
| Frozen impact | None |
| Verify | Cold run: `read_bytes` delta ≈ SSTable bytes; warm run: ≈ 0 |

### H-15 — Reproducible build and provenance extensions — P0

| | |
| :--- | :--- |
| What | `-trimpath -buildvcs=true`, `CGO_ENABLED=0`, double-build hash equality, `GOAMD64`, `GOTOOLCHAIN`, module hash; store all in `build_manifest.json`; refuse to run on mismatch |
| Why | R3 |
| Where | build script + `provenance` extension |
| Risk | `-buildvcs` requires a `.git` in the build tree; the clean clone provides it |
| Frozen impact | None |
| Verify | Two clean clones → identical SHA-256; `file` reports static |

### H-16 — Run-list permutation, commitment and resume — P0

| | |
| :--- | :--- |
| What | Generate `runlist.csv`, shuffle per §5 of [06](06_TRIAL_INVENTORY.md), commit its SHA-256; runner refuses a differing list; append-only JSONL ledger with the runlist position; resumable after a crash without re-running completed trials |
| Why | R3 (randomized ordering) and integrity |
| Where | `harness2/runlist.go`, `harness2/ledger.go` |
| Risk | A partial trial after a crash must be marked `attempt` and re-run, and the ledger must record it |
| Frozen impact | None |
| Verify | Kill -9 mid-trial; resume; ledger shows exactly one completed record per trial |

### H-17 — Attempt/replacement bookkeeping — P0

| | |
| :--- | :--- |
| What | `attempt` number, `replacement_reason`, `excluded`, `exclusion_code`; enforced replacement whitelist |
| Why | [06](06_TRIAL_INVENTORY.md) §8 |
| Where | schema v2 + executor |
| Risk | None |
| Verify | Attempting a replacement with a non-whitelisted code fails |

### H-18 — Trial-count ceiling enforcement — P0

| | |
| :--- | :--- |
| What | Runner counts completed attempts and refuses to start beyond 384 |
| Why | The hard limit must be enforced in code, not by discipline |
| Where | `harness2/ledger.go` |
| Risk | None |
| Verify | Unit test with a tiny ceiling |

### H-19 — Drift monitor (D3) — P0

| | |
| :--- | :--- |
| What | Reference triplet at start and end; record `MHZ` per CPU before/after each block; analysis includes trial position as a covariate |
| Why | S-6 |
| Where | `harness2/drift.go` |
| Risk | None |
| Verify | Drift triplet produces the expected schema and is excluded from confirmatory analysis |

### H-20 — New metrics in every record — P0

| | |
| :--- | :--- |
| What | `k_mean`, `k_max`, `leases_per_read`, `records_scanned`, `stall_count`, `stall_time_share`, `max_stall`, `abandoned`, `intended`, `achieved_offered_ratio`, `event_count`, `schedule_hash`, `read_bytes_delta`, `gcd_or_cleanup_errors`, `fd_count_start/end` |
| Why | The audit shows composition variables must be visible in the table, not inferred |
| Where | schema v2 |
| Risk | Schema churn; version it and keep a reader for v1 |
| Frozen impact | None |
| Verify | Schema validation on 1 synthetic record per block |

### H-21 — Analysis tooling — P1

| | |
| :--- | :--- |
| What | Loader → exclusions → triplets → paired log-ratios → BCa bootstrap → Wilcoxon/sign → TOST/NI → Holm → tables; deterministic (seeded bootstrap); emits every table and figure from one command |
| Why | Reproducible analysis; prevents hand-editing |
| Where | `research/experiments/phase_i7/analysis/` |
| Risk | Two implementations of a bootstrap that disagree → pin the seed and publish the resample indices hash |
| Verify | Runs on synthetic data with a known answer |

### H-22 — Pebble adapter — P1

| | |
| :--- | :--- |
| What | Separate module/binary implementing Y1–Y3 through Pebble's API, sharing only the key stream, schedule and recorder contract |
| Why | E7 |
| Where | `research/experiments/phase_i7/cmd/pebble_runner` + `go.mod` in a submodule (so the main module's dependency graph is unchanged) |
| Risk | Dependency weight; keep it out of the main `go.mod` |
| Frozen impact | None |
| Verify | Same schema; `sync:true` on all writes; recorded version + hash |

### H-23 — Schema versioning — P1

| | |
| :--- | :--- |
| What | `schema_version: "2.0.0"`; additive fields only where possible; a documented mapping from v1 (frozen) to v2 (I7) for any joined table |
| Why | The frozen records must stay readable |
| Where | `research/experiments/schemas/` |
| Risk | Silent field renaming breaks comparability |
| Verify | Loader reads 1,560 frozen v1 records and all I7 v2 records |

### H-24 — Runner CLI — P2

| | |
| :--- | :--- |
| What | `-block`, `-preflight`, `-calibrate`, `-diagnostic`, `-resume`, `-max-trials`, `-dry-run`, `-runlist`, `-out-dir` |
| Why | Operability and the hard limit |
| Where | new `cmd/runner` |
| Verify | `-dry-run` emits the full list with zero engine calls |

---

## 2. Explicitly Unchanged

| Component | Rule |
| :--- | :--- |
| `lsmengine/transaction.go`, `wal.go`, `sstableBuilder.go`, `sstableReader.go` scan logic, `memtable.go`, `keys.go` | **No change** (the read path is the controlled constant, AF-6) |
| `lsmengine/snapshot.go` (Approach B) | **No change** |
| Approach A `Get` body | **No change** |
| Flush/compaction algorithms | **No change** |
| Frozen data directories | **Read-only**; hash before and after |
| Frozen executables | **Not modified, not re-used** for I7 data |
| `harness/runner.go`, `harness/metrics.go`, `harness/flush_tracker.go` | **No edits** (new code lives in `harness2`) |
| WAL format, SSTable format | **No change** |

---

## 3. Change Risk Register

| Risk | Mitigation |
| :--- | :--- |
| The new harness silently changes the workload | Every block runs a **manipulation check** ([07](07_ANALYSIS_PLAN.md) §3.6) before its prediction is evaluated; a failed check voids the prediction, not the campaign. |
| The open-loop scheduler is wrong | Self-test against a null workload before E4; KS test on inter-arrival times. |
| The publication-only driver changes flush semantics | Test-only seams; production paths untouched; diff audit lists them. |
| Per-worker sharding changes percentile computation | Equivalence unit test on a fixed sample stream. |
| New instrumentation leaks into the performance binary | Build-tag CI check: binary without the tag is byte-identical to the pre-change build. |
| Schema v2 breaks frozen analysis | Loader round-trip test over all 1,560 frozen records. |
| The bulk fixture produces different SSTables | Byte-equality proof; otherwise H-09 is abandoned. |

---

## 4. Implementation Order (G2)

1. H-15/H-13/H-12 (build + platform manifest) — everything else depends on a trustworthy environment record.
2. H-23/H-17/H-18/H-16 (schema, ledger, runlist, ceiling).
3. H-02/H-03/H-04/H-11/H-20 (measurement core + metric additions).
4. H-01 (third arm wiring; the arm itself lands at G1).
5. H-06/H-07/H-08 (events, publication driver, paced writer).
6. H-05/H-14 (open loop, cache condition).
7. H-10 (instrumented build, separate).
8. H-09 (bulk fixture, optional).
9. H-21/H-22 (analysis, Pebble).
10. H-19/H-24 (drift, CLI).

Each item lands with its unit test. No item is merged without the verification listed above.
