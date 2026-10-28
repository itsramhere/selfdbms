# 03 — Reader Pacing and Coordinated-Omission Audit

**Status:** READ-ONLY AUDIT. No trials executed. All numbers below are either read from the frozen campaign JSON or are arithmetic over those numbers.
**Scope:** the reader driver in `research/experiments/harness/runner.go`, its metrics in `metrics.go` / `flush_tracker.go`, and the interpretability of the historical latency claims built on them.

> **Windows amendment (2026-10-28).** Every finding here was derived from the frozen **WSL2/ext4** records and applies unchanged to the Windows campaign, because the defects are in the *driver and metric definitions*, not in the OS: closed-loop load, `20·r` stall sampling, uncounted B2 writes, and the D work-composition shift are all platform-independent.
>
> Two platform-specific consequences:
> 1. On Windows the file cache makes AF-1 even stronger: the working set is ≈1.5 MB, so the read critical section is CPU-bound for the whole campaign. No "disk I/O" wording is permitted ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §13).
> 2. The historical `time.Now` granularity problem does **not** affect the measurements: the harness already takes latencies from QPC (100 ns). It *does* affect any new scheduler code (§[12](12_WINDOWS_EXECUTION_AMENDMENT.md) §4.2, [04](04_OFFERED_LOAD_METHODOLOGY.md)).

---

## 1. What the Reader Driver Actually Does

```go
// research/experiments/harness/runner.go:489-533 (abridged)
go func(seed int64) {
    sampler := NewKeySampler(seed, ...)
    <-startBarrier
    for {
        select {
        case <-stopMeasure: return
        default:
            key, expectHit := sampler.SampleKey()
            readAttempted.Add(1)
            readAdmitted.Add(1)          // unconditional: always equals attempted
            t0 := NowPrecision()
            val, err := man.Get(key, math.MaxUint64)
            readLatency.RecordNanos(SincePrecisionNanos(t0))
            if compactionActive.Load() { readsDuringCompaction.Add(1) }
            flushTracker.RecordPointRead()
            ...
        }
    }
}(seed)
```

Four properties follow directly:

1. **Closed loop, no think time.** Each of the `r` goroutines issues its next request immediately after the previous one returns. There is no inter-arrival process. The system is driven at whatever rate the system permits: *offered load is an outcome, not an input.*
2. **At most one outstanding request per goroutine.** A goroutine blocked inside `Get` cannot issue anything else.
3. **Latency is service latency, measured from actual issue.** `t0` is taken after the previous completion, not at an intended time.
4. **`readAdmitted` is a constant copy of `readAttempted`.** No admission decision exists, so the field carries no information. `TimeoutOpsCount` is never incremented.

This is the textbook precondition for **coordinated omission**.

---

## 2. Why Closed-Loop Beats Down the Tail

Consider a stall of duration `L` (a publication critical section, a WAL `fsync` convoy, a scheduler delay). Under this driver:

- the `r` active goroutines are all blocked in `Get`, so **exactly `r` samples** are affected, each of length ≈ `L`;
- the stall consumes `L` of wall-clock throughput, so `λ·L` requests that an open-loop client would have issued during the stall **are never issued at all**.

An open-loop generator offering rate λ would record ≈ `λ·L` affected requests. The closed loop records `r`. The **undercount factor is `λ·L / r`**.

Using the frozen Workload B2 A-arm numbers (`λ` = median read ops/s, `L` = median p95):

| r | λ (ops/s) | L = p95 (s) | λ·L (requests an open loop would delay) | recorded (`r`) | undercount |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 39.7 | 0.0452 | 1.8 | 1 | 1.8× |
| 2 | 72.6 | 0.1754 | 12.7 | 2 | 6.4× |
| 4 | 135.9 | 0.1810 | 24.6 | 4 | 6.2× |
| 8 | 195.8 | 0.2209 | 43.2 | 8 | 5.4× |
| 16 | 197.1 | 0.2987 | 58.9 | 16 | 3.7× |
| 32 | 249.3 | 0.3708 | 92.4 | 32 | 2.9× |

**Reading:** every publication stall is under-represented in the sample by a factor of 3–6 for `r ≥ 2`. The *duration* of the stall is captured accurately by the samples that do get in; the *frequency* is not. Any statistic that depends on the fraction of affected requests — which is exactly what a percentile is — is therefore a function of the driver, not of the system.

Cross-check that the samples are in fact the stall population: `20` injected publications × `r` readers, divided by the total sample count, should straddle 5 % precisely where the reported p95 jumps:

| r | N (samples) | `20r / N` | p95 (A) | interpretation |
| ---: | ---: | ---: | ---: | :--- |
| 1 | 397 | 5.0 % | 45.2 ms | p95 sits *on* the stall boundary — maximally fragile |
| 2 | 726 | 5.5 % | 175.4 ms | p95 ≈ stall duration |
| 4 | 1,359 | 5.9 % | 181.0 ms | p95 ≈ stall duration |
| 8 | 1,958 | 8.2 % | 220.9 ms | p95 inside the stall population |
| 32 | 2,493 | 25.7 % | 370.8 ms | a quarter of all reads are stall samples |

This is why the reported p95 rises monotonically with `r` in the A arm: it is tracking `20r/N`, not a latency distribution. The 73–76 % "suppression" at `r = 2, 4` is best read as **a ratio of stall durations**, `L_B / L_A`, in the only two cells where the stall population happens to straddle the 95th percentile.

---

## 3. The Tail Is Also Contaminated by the Hit/Miss Mixture

Workload A is read-only with **no** publications and **no** writers, i.e. zero synchronization effects. Its frozen distribution at `r = 1`:

| statistic | value |
| :--- | ---: |
| p50 | 10.83 ms |
| p95 | 31.33 ms |
| p99 | 37.06 ms |
| throughput | 72.5 ops/s |

The p95/p50 ≈ 2.9 ratio is the **hit/miss mixture**: 5 % of reads are misses that scan all four tables, hits scan ~2. With a 5 % miss rate, p95 is structurally located at the boundary of the miss class. Consequences:

- Any p95 comparison at 5 % misses is dominated by the exact miss fraction and by the per-table scan cost — both of which are properties of the read path (AF-1), not of synchronization.
- A change in *achieved* write throughput (Workload D) changes the flush/compaction cadence, which changes the number of tables, which changes the miss cost, which moves p95. This is a real, measured path from "more writes" to "worse read p50/p95" that has nothing to do with snapshots.

**Correction used in I7:** event-conditioned latency (reads overlapping a publication, reported separately from the unconditional distribution) plus the miss class reported separately. See [04](04_OFFERED_LOAD_METHODOLOGY.md) §4.

---

## 4. Workload B2 Is Not a Controlled Publication Experiment

The B2 driver ([runner.go:604–646](../../experiments/harness/runner.go)) has three defects that interact:

### 4.1 It injects 1,000 uncounted `fsync`ed writes per trial

Every 500 ms it commits a 50-key transaction. `Transaction.Commit` holds `m.Mu.Lock()` and calls `Wal.Append` once per key, each of which does `File.Sync()` ([transaction.go:57–114](../../../lsmengine/transaction.go), [wal.go:28–69](../../../lsmengine/wal.go)). For the A arm this is an exclusive-lock hold of tens of milliseconds, repeated 20 times. Evidence from the frozen JSON: an `A r=4` B2 trial reports `write_ops_attempted = 0`, `publication_duration_ms = 450`, `reads_during_publication_count = 76`, `p95 = 171,982 µs`.

The measured "flush tail" therefore contains **three conflated stall sources**:

| Source | Present in A | Present in B | Counted as writes? |
| :--- | :--- | :--- | :--- |
| 50-key write batch under `m.Mu.Lock` (50 × `fsync`) | yes | yes | **no** |
| Flush publication critical section (`m.Mu.Lock` + reader drain) | yes | no (uses `snapshotMu`) | no |
| Physical SSTable build + `fsync` (outside the lock) | yes | yes | no |

### 4.2 Its schedule is self-clocked, so the treatment changes the dose

`TriggerFlush()` is followed by `WaitForFlushCompletion()` in the same goroutine that owns the 500 ms ticker. When a flush takes longer, the next tick is deferred and `time.Ticker` may drop ticks. Realized publications therefore differ by arm:

| r | A realized (of 20) | B realized |
| ---: | ---: | ---: |
| 1–16 | 20 | 20 |
| 24 | 19 | 20 |
| 32 | **16** | 20 |

(`lsm_diagnostics.flushes_completed` minus the 4 fixture flushes.)

A 20 % shortfall in injected stall events in the A arm inflates whatever "exposure" statistic is compared and works *in favour* of B at `r = 32`. This is a treatment-correlated schedule, i.e. a confound, and it is invisible in the aggregate table.

### 4.3 Its "writer-free" label is false

Because the coordinator's writes bypass the counters, the schema reports `write_ops_completed = 0` for B2. Any downstream analysis that treats B2 as read-only (for example, "no writers, so `m.Mu.Lock` contention comes only from publication") is reasoning from a false premise.

---

## 5. Workload D: A Composition Shift Reported as a Throughput Penalty

Frozen medians (`*WORKLOAD_D_READER_CHURN_*`, 10 reps each). See AF-4/AF-5 in [00](00_README.md).

| r | A read | A write | A **total** | B read | B write | B **total** | read ratio | write ratio | **total ratio** | A flushes | B flushes |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 475.4 | 235.2 | **710.6** | 298.0 | 297.5 | **595.6** | 0.63 | 1.26 | **0.84** | 22 | 28 |
| 2 | 904.8 | 224.0 | **1,128.8** | 598.9 | 288.1 | **887.1** | 0.66 | 1.29 | **0.79** | 21 | 27 |
| 4 | 1,701.7 | 210.5 | **1,912.2** | 1,267.3 | 289.9 | **1,557.5** | 0.74 | 1.38 | **0.81** | 20 | 27 |

Three facts that must be stated in the manuscript:

1. The arms perform **different amounts of work**. B completes 26–38 % more writes and 27 % more flushes; the accumulated frozen-memtable/handoff work is correspondingly larger. Read throughput is therefore not measured at equal work.
2. On **total** completed operations the gap is 16–21 %, not 26–37 %.
3. The **median read latency** difference at `r = 1` is +2.06 ms (A 1.11 ms → B 3.17 ms), and it converges at `r ≥ 16` (A 0.74 → B 0.66 ms). A 2 ms median gap cannot be produced by per-read metadata of tens of nanoseconds (§AF-5). It is a *waiting* effect: at low concurrency, whoever holds `m.Mu` more gets more throughput, and B's readers hold it for microseconds, so writers win it more often and readers queue behind the writer's commit critical section (tiered conflict check + `fsync`).

### 5.1 The historical attribution is unsupported

The manuscript attributes the D gap to "snapshot descriptor allocation, atomic reference counting, and deferred reader retirement". Under the frozen numbers:

- descriptor allocation is per publication (≈ 28 flushes + 15 compactions), not per read;
- the published microbenchmark cost of `AcquireSnapshot`/`ReleaseSnapshot` is 42–68 ns;
- the observed gap is ≈ 1.25 ms per read at `r = 1`, i.e. **four orders of magnitude larger**.

The attribution must be withdrawn or replaced with the lock-sharing hypothesis, and E3/E5/D1/D2 exist to discriminate between them.

---

## 6. Instrumentation Artifacts on the Measured Path

| ID | Artifact | Effect | Correction |
| :--- | :--- | :--- | :--- |
| IA-1 | `LatencyRecorder.mu` acquired per read sample | Global mutex on the read loop; contention grows with `r` and throughput → correlated with treatment | Shard per worker; merge offline (H-02) |
| IA-2 | `FlushTracker.mu` acquired per read (`RecordPointRead`) | Second global mutex per read; also re-reads three atomics | Per-worker counters, no mutex (H-03) |
| IA-3 | `readAdmitted` incremented unconditionally | Field is a constant; misleading schema | Remove or redefine as issued (H-04) |
| IA-4 | `LatencyRecorder` stops recording after 500 k samples (first-N, not a reservoir) | Silent first-window bias if exceeded; at 20 k ops/s × 10 s = 200 k it is not exceeded today, but the failure is invisible | Reservoir sample + `samples_dropped` (H-02) |
| IA-5 | Throughput denominator = wall time from barrier close to last worker exit | Deflated by ≤ one tail latency (up to 3.7 % in B2 A r=32) | Record and report both `window_ns` and `drain_ns` (H-04) |
| IA-6 | Engine counters (`sstablePointReads`, `memtablePointReads`) are atomics inside the timed region | Adds ~2 atomics to every measured read | Keep (identical across arms) but note; optionally disable in a diagnostic build |
| IA-7 | `startBarrier` releases all `r` goroutines at once | Start-up thundering herd; affects the first few ms only | Warmup absorbs it; document |
| IA-8 | Worker key streams diverge once op counts diverge | Paired arms read the same *distribution*, not the same sequence | Counter-based key stream (H-11) |

None of these artifacts change direction. They do change magnitude, and IA-1/IA-2 are correlated with `r`, which is the independent variable of the paper.

---

## 7. What the Historical Latency and Throughput Numbers Can Still Support

| Claim | Status after this audit |
| :--- | :--- |
| A's writer progress collapses as `r` grows; B's does not | **Supported.** Effect is large relative to every listed artifact, and is visible in totals, not just read ratios. |
| B's stall duration per publication is much shorter than A's | **Supported as a ratio of stall durations** at `r = 2, 4`; not as a load-controlled p95. |
| "73–76 % p95 suppression" as a general tail-latency result | **Re-scope.** Valid only at the two `r` values where the 20·r stall population straddles 5 % of samples; undefined outside that window. Must be reported with the injection schedule and `r`. |
| B1 throughput doubling | **Supported** (total ops 4.5× at `r=1`; 2.07× at `r=4`), though attribution to "no lock over I/O" is replaced by "shorter critical section" pending E1. |
| Workload D "26–37 % throughput penalty" | **Narrow to "read throughput"**; add total-op ratio (16–21 %) and write ratio (1.26–1.38×); note unequal work. |
| Workload D mechanism = descriptor lifecycle | **Unsupported.** Withdraw or restate as a hypothesis. |
| Workload A "parity" | Unchanged (already re-scoped to "CIs span parity", Claim 9). |

---

## 8. Required Corrections Carried into I7

1. Replace closed-loop-only measurement with a **dual-mode driver**: closed-loop (for achieved-throughput comparisons) and open-loop (for latency at matched offered load). [04](04_OFFERED_LOAD_METHODOLOGY.md).
2. Report **stall-event statistics** independent of sampling structure: stall count, stall-time share, max stall, and **event-conditioned latency** (reads overlapping a publication).
3. Rebuild B2 as a **publication-only** driver that injects no writes and whose schedule is not self-clocked (H-07).
4. Add an **equal-work** churn regime so read throughput is compared at matched write work (H-08).
5. Remove both global mutexes from the read loop (H-02, H-03).
6. Report `k` (reader-set cardinality), `records_scanned`, flush count, and write count for **every** trial, so composition shifts are visible in the table rather than inferred.
7. Never compare p95 across cells with different event counts or different `r` without stating the injected-event count.

All seven are enumerated with implementation detail in [08_HARNESS_CHANGES.md](08_HARNESS_CHANGES.md).
