# X-readcost-v1 — Pre-registration

**Status:** DRAFT. Freeze (commit) before the first non-smoke trial runs. Any later change must be
listed in §9 with its date and reason.
**Supersedes for new data collection:** the R1.2e/R2 confirmatory protocol (never executed).
Historical Phase I (WSL2) and Phase I7 G4 (Windows, 60 trials) data are not pooled with this campaign.

## 1. Why this campaign existss

Every earlier measurement used an SSTable point-read path that scanned from the target block to the
end of the file, with about four unbuffered syscalls per record and no early stop. Measured on the
campaign host, one point read cost 0.3 ms for a key in the newest SSTable and about 50 ms for each
older 2,500-record table it had to scan (up to 156 ms), and about 500 ms under 8 concurrent readers in G4.
Approach A holds the manager read lock across that scan, so its read critical section was two to
three orders of magnitude longer than in a production LSM engine. The earlier results therefore
describe one extreme operating point.

The engine now has a correct read path: a per-table key-range check, one `ReadAt` per 4 KB block,
and early termination. The old path is kept as `legacy_scan`. An experiment-only per-block delay
`d` makes the read critical-section length a controlled variable. The delay is injected inside the
SSTable block read, so Approach A holds the manager lock across it and A-min/B do not.

## 2. Research questions

- **RQ1 (mechanism):** How does Approach A's penalty relative to A-min and B grow with read
  critical-section length? At what read cost does coarse locking start to starve writers and stall
  flush publication?
- **RQ2 (sufficiency):** At each read cost, does descriptor leasing (A-min) match immutable snapshots
  (B) on write throughput, read throughput and publication latency?
- **RQ3 (churn):** Under rapid reader-set churn (16 KiB memtable), what is the A-min vs B cost?
  Phase I reported that B was 26–37% slower than A at low concurrency. A-min was never tested.

## 3. Design (fixed in `cmd/xcampaign`)

Common to all cells: Windows 11 NTFS, NVMe SSD; keyspace 10,000 keys in 4 initial SSTables of 2,500
keys each (disjoint ranges); 128 B values; read hit ratio 0.95 uniform; WAL fsync on during the
window (off while building the fixture); 2 s warmup, 10 s measurement window, 200 ms cooldown;
synchronization instrumentation on in every arm.

| Block | Cells | Purpose |
|---|---|---|
| X1 | r ∈ {4, 16} × {d = 0, 20 µs, 200 µs, 2 ms, legacy_scan} = 10 | Dose–response, mixed read/write (1 free-running writer, 1 MiB memtable) |
| X2 | r = 16 × {d = 0, 200 µs, 2 ms, legacy_scan} = 4 | 20 pre-frozen memtables published at fixed times during the window (G4 E2 parameters, 0 writers) |
| X3 | r ∈ {1, 4, 16}, d = 0 = 3 | Reader-set churn (16 KiB memtable, 1 free-running writer) |

- **Unit:** a triplet is the three arms (A, AMIN, B) run on the same seed, so all three see identical
  request streams. There are 10 triplets per cell, giving 510 trials, plus 3 drift-anchor triplets
  (cell X1 r=4 d=0) at the start, after round 5 and at the end. Total: 519 trials. All three anchor
  triplets use the same seed, so anchor differences reflect drift rather than different request streams.
- **Realized dose** (single-goroutine point read on the campaign host, block path): about 9 µs at
  d = 0, 35 µs at 20 µs, 215 µs at 200 µs and 2.25 ms at 2 ms. Sub-millisecond delays are a
  CPU-yielding spin timed with QueryPerformanceCounter. The 2 ms delay blocks in the OS for all but
  the last 0.5 ms.
- **Instrumentation scope:** synchronization statistics are reset at the start of the measurement
  window, so they cover the window and the post-window publication drain, but not fixture
  construction or warmup.
- **Order:** 10 rounds. Each round runs every cell's next triplet in a random cell order. Arm order
  within a triplet cycles through all 6 permutations. Because of this interleaving, slow drift
  spreads across all cells rather than biasing any one of them.
- **N = 10 justification:** in G4 the standard deviation of the paired log-ratio ln(AMIN/B) across
  closed-loop triplets was about 0.06. With N = 10 the 90% CI half-width is about 1.83 × 0.06 / √10 ≈
  0.035, so a true difference near zero can be shown equivalent within the ±10% margin (§5). If
  realized dispersion is much larger, the affected cells will be reported as inconclusive, not
  re-run.

## 4. Outcomes

Per trial, from the i7 trial JSON:
- **W**: in-window completed writes per second (X1, X3).
- **R**: in-window completed reads per second (X1, X2, X3).
- **Read latency p50 / p99** (histogram-based, no sample drops).
- **Publication outcomes (X2):** in-window realized publications out of 20; per-publication
  writer lock-wait and hold time (p50/p99) from the synchronization instrumentation.
- **Measured dose:** mean and p99 read critical-section duration per arm, from the
  instrumentation. The analysis reports d as nominal and as measured.

## 5. Hypotheses and tests

All ratios are computed per triplet; the analysis unit is the triplet (N = 10 per cell).

- **H1 (dose–response, RQ1):** Approach A's write starvation increases with d. For each r in X1, the
  test statistic is the per-triplet ordering of S = W_A / W_AMIN across d ∈ {0, 20, 200, 2000 µs}.
  The test is a one-sided Page's L trend test, with triplet index as block and d level as treatment,
  for a decreasing trend at α = 0.05. legacy_scan is reported descriptively as the historical
  operating point, not in the trend test. Because the test is rank-based, W = 0 is handled.
- **H2 (A-min ≈ B, RQ2):** For each X1 and X2 cell and each of W (X1 only), R, and X2 publication
  lock-wait p99, the equivalence test is TOST on the mean paired log-ratio ln(AMIN/B). The margin is
  ±ln(1/0.90), i.e. a ratio in [0.90, 1.11], tested with a 90% paired-t CI. Holm–Bonferroni
  correction is applied across cells within each outcome. Results are reported per cell as
  equivalent, not equivalent, or inconclusive (CI straddles a margin). Cells are never pooled across
  blocks or d levels.
- **H3 (churn, RQ3):** In X3, ln(AMIN/B) for R and W, reported with a two-sided 95% paired-t CI. No
  directional prediction.
- **Descriptive:** all three pairwise ratios (A/AMIN, A/B, AMIN/B) with 95% CIs for every cell.
  Publication in-window counts are reported as distributions.
- **Zeros:** if any arm has W = 0 in a triplet, its log-ratios use W + 0.1 writes/s, and the cell is
  flagged. H1 is rank-based and unaffected.
- **Sensitivity:** percentile bootstrap (10,000 resamples, fixed seed) for every CI above.

## 6. Exclusions and failures

- Treatment outcomes never fail a trial. Missed publication deadlines, starvation and zero writes are
  data.
- A trial fails only on an engine or harness fault: ledger violation, close/lease-drain error,
  validation error, crash or timeout. A failed trial is retried once. If the retry also fails, the
  triplet is reported as incomplete and dropped from the paired tests of that cell. Every such case is
  listed.
- The campaign aborts if more than 5% of trials fail (after at least 40 have run). The data collected
  up to that point is reported as is.
- There are no outcome-based exclusions.

## 7. Drift control

Anchor triplets run at the start, middle and end. Drift is measured as D = R_AMIN(end) / R_AMIN(start)
for the anchor cell. If |ln D| > 0.10, this is reported as a threat to validity. The interleaved order
(§3) limits the bias drift can introduce into within-triplet comparisons.

## 8. Host protocol

AC power; Windows power mode "Best performance"; no other user applications; Defender real-time
protection left as found and recorded (it cannot be changed reliably and was on in G4). The driver
records the git revision, runner SHA-256, timer self-test and power scheme in `platform.json`.

## 9. Pilot and deviations log

- **2026-10-30, mechanical smoke run (excluded from all analyses).** The first 21 rows of the plan
  were executed from a dirty working tree to check mechanics. Plan hash: 68ad96f8…8136. The run
  covered the anchor, both X1 legacy cells, X1 r=16 at d ∈ {0, 20 µs, 2 ms} and X2 legacy. All 21
  trials returned SUCCESS. Wall time was 12.5–17.6 s per trial, for an expected campaign duration of
  about 1 h 55 min. No design value was changed because of these data.
- **Change to the old E blocks:** fixture construction now runs without WAL fsync in all blocks.
  E blocks are not re-run in this campaign, so no X outcome is affected.
- **2026-10-31, aborted first start (discarded).** The campaign was started from commit `afdfb67` and
  stopped by the operator after 38 of 519 trials (all SUCCESS; the 39th was interrupted) because
  continuous mains power could not be guaranteed. The reason was unrelated to the results. All partial
  output was deleted without being analysed. The only values seen were the driver's per-trial
  status lines. The full plan is re-run from the start with the same plan and seeds.
- **2026-11-02, analysis rules fixed before any campaign data exists.** These make §5 precise and do not
  change any test or margin:
  - A cell is `equivalent` if its Holm-adjusted TOST p is below 0.05, `not_equivalent` if its 90% CI lies
    wholly outside the margin, and `inconclusive` otherwise.
  - The Holm families for H2 are W (10 X1 cells), R (10 X1 and 4 X2 cells) and publication lock-wait p99
    (4 X2 cells).
  - H1 uses triplets complete across all four delay levels, ordered d = 0, 20, 200, 2000 µs, with predicted
    ranks 4, 3, 2, 1.
  - A zero in an outcome other than W is excluded for that triplet and reported.
  - The percentile bootstrap uses seed 20261030.
  - The analysis is implemented in `research/experiments/analysis/x_readcost_analyze.py`.


### 2026-11-02, post-data notes (no test or margin changed)

- Campaign completed 2026-11-02 from `c53ee5b`: 519/519 first-attempt success.
- Post-data finding: commit conflict validation performs delayed SSTable block reads under the exclusive lock in all arms, so section 1's statement that A-min and B do not hold the lock across the delay applies to readers only.
- Post-data finding: two exclusive acquisitions per commit and R = 2rW under A; exploratory.
- The optional WAL-sync-off run was not performed; the harness rejects `wal_sync=false` for X blocks.
- No test, margin, or exclusion was changed.
