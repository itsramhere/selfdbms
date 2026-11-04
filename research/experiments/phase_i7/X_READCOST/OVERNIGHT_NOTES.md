# Overnight notes (2026-11-02/03)

Working log for the morning summary. Git-ignored evidence folder.

## Step 1: X-compaction-v1 (finished 00:06 IST, 3 Nov)

Run: 360/360 first attempt, 0 retries, 0 dropped, 0 validation errors. Frozen at c342832.
Runner 3a05470d..., plan 5330cbdd..., ledger 10d34887...
Analysis: `analysis_compaction/`. Throughput and compaction counts rechecked independently from raw trials; they match.

### Pre-registered result
H-C: 12 of 12 primary tests equivalent (6 on cells, W and R), as predicted. All 12 secondary off tests equivalent too.
Closest to the margin: C1_r16_d0200_on W, 0.949, 90% t CI [0.909, 0.991], bootstrap [0.916, 0.986]. Inside the margin, but the CI excludes 1, so a small A-min deficit is possible there.

### Descriptive results (no prediction made; these are not confirmatory)
1. Commit tail differs even though throughput does not. A-min commit-wait p99 / B: 1.43 to 7.69 in all six on cells, every 95% CI excludes 1.
   Largest at d=2000: r4 4.45 [2.68, 7.39], r16 7.69 [6.19, 9.54]. Absolute values small: A-min 22 to 246 us, B 12 to 177 us, A 306 to 9221 us.
   Fits the disclosed asymmetry (A-min opens compaction inputs under Mu exclusive, hold mean 160 to 354 us; B outside locks). Cause not tested.
2. Coarse-lock write penalty survives compaction. W_A/W_AMIN on: r4 0.919, 0.681, 0.439; r16 0.851, 0.656, 0.393 (d=0, 200, 2000).
   Off: r4 0.813, 0.594, 0.532; r16 0.721, 0.582, 0.497.
3. A's read advantage reverses at 2 ms with compaction. R_A/R_AMIN at d=2000: on r4 0.912 [0.892, 0.933], r16 0.820 [0.806, 0.835]; off r4 1.154, r16 1.081.
   At d=0 and 200 A still reads more (1.24 to 1.49).
   Post-hoc reading, untested: in off cells A flushes less, so it carries fewer tables (final 8 vs 12 at 2 ms), so part of A's read advantage may be table count, not lock policy.
4. Compaction raises throughput in every arm and cell: W on/off 1.17 to 2.66, R on/off 1.31 to 2.86. Off cells accumulate tables (final up to 70); on cells stay near 1 to 3. So on/off measures read amplification relief as well as compaction cost; the two are not separated.
5. Lifecycle at d=2000: retirement lag mean A 0.74 to 0.76 ms, A-min 5.1 to 6.2 ms, B 2.9 to 3.8 ms. At d=0/200 B's p99 lag is larger (7 to 13 ms vs A-min 1.6 to 2.9 ms), since B's cleanup is asynchronous. Retired-table high-water similar in all arms (4 to 6).
6. Compaction count: timer-limited at d=0/200 (about 36 of 40 ticks); at d=2000 flush-limited (A 9 to 10, others 22; no-ops 29 to 30 vs 17). So A does not get "starved"; it simply has fewer flushes to compact. Do not claim starvation.

### Consequences for the paper
- Disclose: earlier campaigns had zero compactions; this campaign fills the gap for throughput.
- The leasing claim is now: throughput equivalent with compaction, commit tail not equal.
- The earlier A read advantage needs a table-count caveat.

## Step 2: X-tablecount-v1 (trials finished 02:08 IST)

Run: 480/480 first attempt, 0 retries, 0 dropped. Frozen at adf754b. Runner 050c3210..., plan 0bc0042b..., ledger 34927d8e...
Analysis: `analysis_tablecount/`. Key ratios rechecked from raw trials; they match.

### Pre-registered result
H-T1: decline detected in both read-only families (r4 L=295, Holm p 5.9e-9; r16 L=296, Holm p 1.6e-9). Not detected with a free writer (r4 p 0.96, r16 Holm p 0.87).
H-T2: every prediction matched. Equivalent at N=4 for all; not equivalent at N=256 read-only.
R_AMIN/R_B read-only, N = 4, 16, 64, 256:
- r4: 0.975, 0.956, 0.918 (equivalent, Holm p 0.04), 0.712 [0.699, 0.726] not equivalent
- r16: 0.989, 0.962, 0.851 [0.837, 0.866] not equivalent, 0.663 [0.660, 0.666] not equivalent
With a free writer all 16 R and W tests equivalent (0.943 to 1.045). Reads are then limited by the commit hold in all arms, which hides the leasing cost.

### Descriptive
- Read critical section mean, read-only: A-min r4 326 to 3370 ns, r16 393 to 8512 ns as N goes 4 to 256; B 256 to 877 ns, flat; A 10 to 41 us (the whole read).
- R_A/R_B read-only: 1.01 to 1.05. R_A/R_AMIN: up to 1.52 at N=256.
- Read p50 at N=256 r16: A-min 22.5 us, B 12.3 us.

### Microbenchmark (lifecycle_bench.txt; separate, non-campaign; 2 s, 3 counts, mean of counts)
- One read cycle, A-min: 27, 70, 248, 974 ns (1 goroutine); 58, 138, 482, 1767 ns (16). B: 21 ns and 88 ns at every N. 0 allocations.
- One flush publication: A-min 104 to 128 ns, 0 allocs. B 654, 718, 1035, 3200 ns, 3 allocs. So B moves the N-proportional cost from reads to publication.
- The campaign's per-table cost is larger than the benchmark's: about 13 ns/table (r4) and 32 ns/table (r16) in the read critical section vs about 3.8 ns/table. Plausibly cold cache lines on real tables spread through the heap; not tested.

### Consequences for the paper
- The earlier leasing claim holds only near four tables, or when commits limit reads. A-min takes per-table leases on every active table, so it does not scale with table count. This is a real qualification.
- All disjoint ranges: no overlapping-table search cost. Range checks are common to all arms.

## My mistake: the microbenchmark cost 3 hours
`-benchtime=2s -count=3` ran from 02:08 to 05:14 (11172 s). The publication benchmark rebuilds a manager with untimed setup per iteration, and Go scales iterations by timed duration only, so wall time ballooned. I should have fixed the iteration count. No results were harmed. Consequence: Step 3 was not run.

## Step 3: X-paced-v1 NOT run overnight
The campaign needed about 2 hours including the build and analysis, which would have left no time for the rewrite, so the rewrite went first. The design was then built and frozen, with 6 excluded smoke trials, but no real trial was run: 74 minutes would have overrun the night's window, and the operator had asked to be told before long campaigns.

## X-paced-v1 (morning of 3 Nov, after the operator said to run it)
Run: 360/360 first attempt, 0 retries, 0 dropped, 0 validation errors. Launched from clean 6833d7c (design frozen in 54572b2). Runner 00f97d04..., plan 384aace7..., ledger 4c0dc604...
Analysis: `analysis_paced/`. Page L and p, W and R ratios recomputed from raw trial files without the analysis script; they match.

### Pre-registered result
- H-P1 (primary) NOT supported below capacity. f=0.2: closed L=123, p 0.291, Holm 0.291; open L=128, p 0.047, Holm 0.093. f=0.8: closed L=139, Holm 1.4e-6; open L=138, Holm 1.0e-5, but all 10 rounds censored, so these are completion-only tail trends driven by A's backlog, not true p99 trends.
- H-P2: 6/6 equivalent, as predicted.
- H-P3: 4/6 as predicted. Failed: d=200 inconclusive, closed 0.896 [0.866, 0.927], open 0.920 [0.898, 0.942]. A did saturate (19 of 20 trials) but the ratio straddles the margin. My design error: earlier capacity 545.8 over rate 619 gives 0.882, right at the 0.90 margin; visible before the freeze.
- H-P4: W 12/12 equivalent. R 5/6; d=0, f=0.8 closed inconclusive, 1.072, 90% CI [0.910, 1.264], median 0.998, sd log 0.284. Dispersion, not a shift. R tracks backlog in all arms (log R vs log mean write response r = -0.82 to -0.91, post hoc).

### Descriptive
1. The latency penalty is in the median, not the p99 (no prediction on p50). Write response p50 A/AMIN at f=0.2: closed 1.093, 1.484, 2.681; open 1.007, 1.336, 2.453. Service p50 at f=0.8: closed 1.068, 1.562, 3.104; open 1.009, 1.416, 3.035.
2. Why p99 was the wrong metric: every arm in every cell has a max write service with median 53 to 74 ms (range 32 to 103), independent of d and arm. At f=0.2 a p99 is the 18th worst of about 1830 writes, and one such stall delays about 10 ticks, so the p99 flips between 2 ms and 260 ms. Per-round A/AMIN at d=0 closed spans 0.03 to 106. I chose the wrong primary metric.
3. Plain fsync probe (non-campaign; `fsync_probe.py`, `fsync_probe.txt`; six 10 s runs, no engine, no readers): max 12.8 to 36.5 ms, often clustered, many near 31.5 ms. So the host stalls without the engine. It does not explain the 53 to 74 ms band; cause untested.
4. Capacity replicates across campaigns. W_A closed at f=0.8: d=200 547.2 vs earlier 545.8; d=2000 111.0 vs 111.0. Open: 564.7, 115.4. Admission relation, out of sample: R_A closed 17560 vs 2rW 17511; 3557 vs 3552.
5. A's backlog p99 (6.04 s at d=2000) is set by the window: 10 s x (1 - 111/286) = 6.12 s. Unstable queue, not a steady-state latency.
6. A's read advantage vanishes at equal write rate. R_A/R_AMIN closed, f=0.2, 0 flushes: 0.967 [0.912, 1.025], 0.921 [0.889, 0.954], 0.720 [0.711, 0.730]. f=0.8: 0.776, 0.550, 0.642. Earlier A read advantage comes with A writing less.
7. f=0.8 is not a safe operating point for any arm: AMIN and B write p99 tens to hundreds of ms (65 to 453 ms) with medians 1 to 3 ms. Trials above 0.95 achieved can still leave up to 360 ticks unissued (21 of 319 unflagged trials exceed 86, the figure I first wrote, which was a per-cell mean), so the flag understates censoring.

## Stall diagnosis (3 Nov, after X-paced-v1). Exploratory, not pre-registered, not campaign evidence.
Evidence: `stall_diag/{runs,long,settle}`, scripts and the Codex brief in `stall_diag/scripts`. Binary `stalldiag.exe` sha256 471157ae..., built from the then-uncommitted working tree on top of af351c4. All runs reuse frozen X4 specs; diag_trace adds tracing, a 1 ms heartbeat and per-reader slowest reads, so these are not identical to campaign trials.

### Where the stall is
- 36 runs (`runs`). In all 27 fsync-on runs the largest commit's time was inside the fsync call (`File.Sync`). Median largest commit 67.4 ms. 0 of 27 were the first commit. No heartbeat gap overlapped it; the largest GC pause in any run was 3.0 ms.
- fsync off (9 runs, d=0, f=0.2): median largest commit 4.9 ms, largest 13.0 ms, all in the WAL write.
- Frozen campaign records agree: first-hold lock wait never reached half the write max (0/360); read max / write max is 1.01 to 1.03 by arm, so readers wait behind the exclusive hold; A at d=2000 had a longest critical section of 9.33 ms against about 64 ms, which rules out a process-wide freeze in those 40 trials.

### What triggers the large stall: the previous trial
- Nine pairs (`settle`, d=0, f=0.2, closed), same spec: 30 s pause then trial ("settled"), then the same spec straight after its cleanup ("back to back"). Largest fsync: settled median 8.0 ms, range 5.7 to 17.0; back to back median 54.9 ms, range 45.7 to 73.0. Larger in 9/9 pairs, ranges do not overlap. Back-to-back stall came 3.1 to 8.5 s into the window.
- Frozen paced campaign: every trial started 0.04 s (median, max 0.13 s) after the previous one, so all 360 were back to back. Median write max does not depend on the previous arm: A 58.9, AMIN 60.8, B 59.0 ms.
- Bare fsync started straight after an engine trial (`long`) had one large stall at 5.4 to 9.5 s (86 to 101 ms). Same timing after the previous trial, no engine in the stalled process.
- Engine 60 s runs that followed a bare run had a largest fsync of 11.4 to 28 ms, except one burst.
- NOT shown: what in the previous trial causes it. Deleting a six-file directory on its own did not reproduce the band (`settle/B_*`: max 31 to 32 ms with delete, 31 to 48 ms without, 4 runs each). Candidates such as heavy fsync on the deleted WAL, process exit, Defender, NTFS or the device are untested. A pause shorter than 30 s is untested.

### Pause calibration (`pause`, before freezing X-settled-v1)
36 trials, d=0, f=0.2, closed, six trials at each pause level in a shuffled order. Largest fsync: pause 0: median 60.4 ms, range 53.7 to 74.0; 5 s: 19.6, 7.0 to 46.8; 10 s: 7.5, 5.5 to 54.3; 15 s: 20.4, 3.3 to 35.9; 20 s: 31.5, 7.8 to 36.0; 30 s: 31.3, 5.6 to 36.1. So a pause of 15 s or more removed the band above about 45 ms in 18/18 trials. It did not remove the 31.5 ms bursts: 11 of those 18 trials had at least three fsyncs of 25 ms or longer, whereas the earlier nine settled trials had none. That earlier 8 ms median therefore overstates what a pause achieves; the difference between the two sets is unexplained. X-settled-v1 uses 20 s. 15 and 20 s were indistinguishable here, but other cells leave more files behind than this one.

### A second, separate effect: 31.5 ms bursts
Clusters of consecutive fsyncs near 31.5 ms, sometimes larger, for up to several seconds. Seen in bare probes with no delete and no engine (35 s runs after a 25 s pause), and in 1 of 3 engine 60 s runs. The cause is untested. Back to back pairs had 2 of 9 bursts, settled 0 of 9; too few to call. A 15.625 ms timer-tick cause has no support for the per-trial maxima (distance to a multiple: median 4.3 ms vs 3.9 ms uniform).

### Consequences for the claims
- Medians are safe. A/AMIN write response p50 log-ratio sd at f=0.2 is 0.01 to 0.04, against 0.14 to 1.69 for p95 and 0.29 to 2.95 for p99. Replay of the traced commits (18 fsync-on runs at f=0.2) leaves p50 unchanged within 0.03 ms when stalls of 25 ms or more are removed, and drops p99 from 1.2 to 12 ms to 1.1 to 1.3 at d=0.
- H-P1 stays as reported. The stall explains why p99 was unstable, it does not rescue the prediction.
- The inconclusive H-P4 read cell (d=0, f=0.8, closed): trial reads track write excess, (mean - p50) x n, r = -0.90 on log reads, excess 0.61 to 2.04 s of the window. Post hoc. Paired-round ratios follow the excess difference.
- Intervention read p99: max 17.0 ms over 160 trials, below the 31.5 ms burst level and the 55 ms band. Closed readers can only have 16 reads blocked per stall, and the smallest 1% of reads in a trial is 74. Those stalls don't set it.
- Compaction commit-wait p99: the metric is the wait for the first lock, which excludes the single writer's own fsync. The fsync stall can't enter it directly. Max 16.2 ms, median 0.11 ms.
- Publication has no writer (X2 writer_concurrency 0), so no WAL fsync in the window.
- Paced open read response p99 at f=0.2: readers queue behind the exclusive hold. The wide CIs at the two lower doses (1.98 [0.89, 4.37], 1.15 [0.27, 4.86]) are consistent with the stall; replay wasn't done for reads.
- fsyncs of 25 ms or longer take a median 0.63 to 0.94% of the window per cell, small against the +/-10% margin. Burst trials are the exception: 31.6% in one d=0, f=0.8 run.

### Direct measurements the paper said were missing
- Sync-off (paper said not run because the harness rejects it; the diagnostic path allows it): at d=0, f=0.2, commit p50 Lease 1.114 -> 0.083 ms, Snapshot 1.106 -> 0.080, CoarseLock 1.229 -> 0.167. The 1 ms floor is the fsync.
- The second wait (paper said unmeasured): at d=2000, f=0.2, CoarseLock commit p50 8.06 ms, lock wait 2.015, validate 2.015, fsync 0.946, flush check 2.114; Lease 3.00 ms, lock wait 0.000, validate 2.015, fsync 0.913, flush check 0.030. So the median penalty is two lock waits of about one delay each, on top of the delayed validation all designs pay. Three runs per arm, below capacity, exploratory.

## X-settled-v1 (3 Nov, 13:51 to 18:06 IST). Pre-registered, frozen at 54b2c67.
470/470 succeeded on first attempt, none dropped. Gaps between trials 20.003 to 20.030 s. Analysis outputs rerun byte-identical. `analysis_settled/verify/` recomputes the numbers below from raw trial files without the analysis module; every ratio, CI and Page statistic matched.

### Pre-registered result
| Prediction | Result | Numbers |
| --- | --- | --- |
| H-M1 (primary), median penalty rises with d | Supported, both modes | Page's L = 140 (the maximum), p = 1.65e-8 exact, Holm 3.3e-8. Every round ranked 0 < 200 < 2000 |
| H-M2, not_equivalent, A higher | Supported, 4/4 | d=200 closed 1.431 [1.398, 1.464], open 1.332 [1.325, 1.340]; d=2000 closed 2.663 [2.640, 2.687], open 2.414 [2.372, 2.456] |
| H-M3, two lock waits | Supported, all four bands | A lock wait 1.952 ms [1.911, 1.993], flush check 2.105 [2.099, 2.112]; Lease 0.0003 and 0.027 |
| H-W, 700 writes/s at 200 us | Supported, 2/2 | W_A/W_AMIN closed 0.792 [0.775, 0.808], open 0.840 [0.818, 0.863]. Mean W: A 554 and 584, Lease 700 and 695, Snapshot 689 and 699 |
| H-R, S3 Lease/Snapshot | FAILED: W equivalent, R inconclusive | W 1.001 [0.994, 1.008]. R 0.995 [0.846, 1.171], Holm p = 0.142, sd of log 0.280 |
| H-S, median largest fsync below 40 ms | Supported, 18/18 | Cell medians 6.9 to 31.5 ms |
| H-C sentinels (22 metrics) | 17 unchanged, 5 inconclusive, 0 changed | 0 of 9 published classes contradicted. No rerun triggered |

The five inconclusive sentinels: X2 publication lock-wait p99 AMIN/B (1.60 to 0.74, CI on the change very wide); C1 commit-wait p99 AMIN/B (1.43 to 2.40); I1 W inline/second in A (1.27 to 1.23), R inline/second in A (0.64 to 0.62), W inline/second in Lease (0.995 to 1.04). The three I1 cells have 5 rounds and miss the margin by 0.011 or less.

### Descriptive (no prediction; across-campaign comparisons are confounded with date, binary, seeds and diag_trace)
- Median penalty at d=0: 1.080 [1.067, 1.092] closed, 1.015 [0.998, 1.032] open. Both inside the +/-10% band. No prediction was made.
- Commit phases (A lock wait / flush check p50, closed): d=0 0.075 / 0.074 ms; d=200 0.229 / 0.296; d=2000 1.952 / 2.105. Lease lock wait is 0.001 ms or less at all three. The two waits are 82 to 93% of the A minus Lease total p50 difference at d=200 and d=2000. p50s don't add, so that share is approximate.
- Lease/Snapshot write response p50: 0.983 to 1.003 in all six S1 cells.
- The stall band went. Largest write service, cell medians: paced 54 to 59 ms, settled 9.5 to 22 ms. Across the 270 paced-cell trials one largest fsync reached 45 ms: 81.3 ms, `S1_d0000_f20_open_t07_B`. One more reached 40.2 ms.
- Earlier CoarseLock tails at d=2000 were inflated by the carry-over. Cell-median write response p99: A closed 25.95 to 11.17 ms, open 34.41 to 11.70; Lease 5.95 to 5.10, 7.03 to 5.16. Geometric-mean p99 A/AMIN ratio 4.04 to 1.85 and 4.98 to 1.64. p95 ratios held: 3.01 to 3.03, 2.63 to 2.64. H-P1 stays as reported.
- Tails below d=2000 are still not usable. A/AMIN p99 log-ratio sd at d=0 is 3.16 closed, 2.05 open; d=200 1.23, 1.36. A pause doesn't fix this; the bursts below remain.
- Compaction commit-wait p99 medians: Lease 0.245 to 0.232 ms, Snapshot 0.177 to 0.104. The AMIN/B rise comes from Snapshot falling, by about 0.07 ms.
- S3 reads range 81,048 to 188,948/s across trials. Log reads against write excess r = -0.73 (-0.90 in X-paced-v1). Not tested.

### New, exploratory: the 31.5 ms bursts rise with write rate
Trials with any fsync of 25 ms or longer, and mean share of the window in them: 72 writes/s 6/60, 0.1%; 155/s 16/60, 0.5 to 0.9%; 183/s 28/60, 1.2 to 1.4%; 650 to 730/s 88/90, 5.2 to 6.9%. At the same d=200 it goes from 16/60 at 155/s to 59/60 at 700/s. At 72 writes/s fsyncs are 14 ms apart, so a host event lasting tens of milliseconds, unrelated to writes, would still be caught. That points to write volume, not to an independent periodic event. No intervention; the host cause is still untested. This also explains why the calibration, run at 183/s, did not warn about S3: the pause cannot fix H-R's cell, and the frozen limit said the pause was chosen on one setting. No effect by arm is claimed (any >=25 ms: A 42, Lease 45, Snapshot 51 of 90).

The first commit of the window has fsync median 6.6 ms (1.7 to 16.7, 180 S1 trials), about seven times the p50. In quiet trials it sets the largest fsync.

### Deviations and things not in the frozen text
- `hold_awake.py` (`analysis_settled/verify/`) ran during the campaign to stop the 3 h idle standby. It holds a per-process power request and spawned `tasklist` every 5 minutes, so about 50 short processes during the run. No settings changed. The prereg says no other user applications; this is an added background process, and the desktop app was also running.
- One read-only status check, stamped 16:00:37 IST (line counts, ledger and log tails), when the operator asked how long the run would take. It saw status and ETA only and fell inside the window of `S1_d2000_f20_closed_t05_B`. That trial is the largest of its group: fsync 38.3 ms and write response p99 39.85 ms, against 5.05 to 7.73 ms for the other nine rounds. The burst began at window second 3.1, about 5 s before that stamp, so the command itself did not start it; the app's activity around the question can't be ruled out. Round 1 of the same group had a 31.2 ms fsync with no check running. No prediction depends on this trial's tail: B is not in H-M1 to H-M3, and H-S uses the median (7.6 ms). It is kept, per the no-outcome-exclusion rule.
- The operator was out; it can't be confirmed the machine stayed untouched.

### Consequences for the paper (not yet edited)
- The median trend moves from descriptive to tested. H-P3's 200 us design flaw is repaired by H-W. The two-wait mechanism is measured, not assumed, at d=2000.
- H-R stays open: inconclusive twice, on fresh data, and the likely reason now has evidence.
- Sentinels: earlier results were not artefacts of back-to-back running, at these margins, with the confounds above. C1's AMIN/B tail ratio should be reported beside the original.
- All campaigns before this one ran back to back, about 0.04 s apart. That must be disclosed along with the stall diagnosis.

## Two fsync checks (3 Nov, late). Exploratory, not pre-registered, not campaign evidence.
Predictions written before any run. Evidence in `fsync_checks/`.

Offline fact that shaped the design: the S1 cells have 0 measured flushes and S2/S3 have about 1 per window, so a flush could be part of why bursts are near universal at 650 to 730 writes/s. The bare loop has no flush, no reader and no engine.

### Check 1: bare append+fsync loop
72, 155, 183 and 700 writes/s, 10 runs each, 140 bytes, fresh file, 2 s after creation, 10 s window, 20 s pause, shuffled within each block.
Prediction: runs with any fsync of 25 ms or longer rise with rate, at most 3/10 at 72 and at least 8/10 at 700. If that holds, the rise is below the engine. If the bare loop stays flat, something in the engine, most likely the flush or the readers, is needed for it.

### Check 2: fsync on and off at S3 (d=0, f=0.8, closed)
Frozen X-settled-v1 specs, Lease and Snapshot, rounds 1 to 10, `stalldiag -wal-sync true|false`, 40 trials, 20 s pause, shuffled within each round. These specs already set `diag_trace`, so the fsync-on trials are the same configuration as the campaign, apart from the driver.
Prediction: with fsync off, the sd of log reads falls by at least half, and the Lease/Snapshot read ratio has a 90% CI inside +/-10%. With fsync on, the spread stays near 0.28 and the ratio stays inconclusive.
Limit: turning fsync off removes the ordinary 1 ms holds as well as the bursts, so a fall in spread implicates fsync, not bursts alone. The result can't rescue H-R, which stays failed.

### Results (run 4 Nov, 00:01 to 00:44 IST)
80/80 runs exited 0, all on AC, 20 s pauses. Tool built from f71e7e2, a clean tree apart from these notes and the new check scripts (binary sha256 D0E7BB68...; diagnostic results don't carry a git revision). A 3-run smoke test was excluded. The driver spawned no process but stalldiag and used no status checks. I stayed idle during the run, and the operator was at the machine in the same session at the start. Report: `fsync_checks/report/`. Raw recomputation: `fsync_checks/verify/`.

**Check 1: prediction NOT met.** Runs with any fsync >= 25 ms: 72/s 0/10, 155/s 2/10, 183/s 2/10, 700/s 5/10. The 700/s figure missed the predicted 8/10. Spearman of rate and burst count 0.40, p = 0.010.
- Five of those nine runs have a single slow first fsync (47.4 to 51.8 ms) and nothing else. Excluding first fsyncs: 0, 0, 1 and 3 of 10.
- The four runs with real 31 ms bursts are all among the first 10 of 40 runs (positions 3, 5, 6, 9). None in the last 30, though rates were shuffled. The first three 700/s runs burst; the next seven did not. So time or host state matters and rate alone does not settle it.
- Engine with fsync on at 732/s, straight after, had 17/20. Settled at 650 to 730/s had 88/90. Fraction of fsyncs >= 25 ms: bare 700/s 0.05%, settled 650 to 730/s 0.28%.
- Reading: engine-free fsync can show the 31 ms pattern, so the bursts aren't created by the engine's code. But the near-universal bursts at high rate aren't reproduced by rate alone. Flush, readers, data volume or the order (bare first, engine second, not interleaved) could each be the difference. Not separated.

**Check 2: prediction met.** Pooled sd of log reads, arm means removed: on 0.142, off 0.018, ratio 0.128 [0.086, 0.191] (F interval, assumes normality). Lease/Snapshot reads: on 1.052 [0.906, 1.222], inconclusive; off 0.994 [0.987, 1.000], equivalent. Writes equivalent in both: 1.011 [0.989, 1.034], 1.000 [1.000, 1.000]. Reads: on, Lease 108,009 to 173,846, Snapshot 104,776 to 156,826; off, Lease 437,665 to 454,848, Snapshot 427,814 to 460,161. Any fsync >= 25 ms: on 17/20, off 0/20. Largest write service with fsync off 5.0 to 6.5 ms. The fsync-on trials replicate settled (sd log R 0.159 and 0.123 against 0.137 and 0.194; settled Snapshot range 104,364 to 186,900).

### New, post hoc: read spread follows the writer's exclusive hold time
At 732 writes/s the writer holds the exclusive lock for about three quarters of the window, so readers share the rest and small changes in the hold are large changes in what is left.
- Hold share (validate + WAL write + fsync + apply, phase means x commits / window): fsync on 67.6 to 78.8%, median 72.3%; settled S3 67.4 to 79.2%, median 74.7%; fsync off 4.3%.
- log R vs log(1 - hold share): fsync on r = 0.965, slope 1.16, n = 20; frozen settled S3 r = 0.905, slope 1.11, n = 20. Slope near 1 is what proportional reads would give.
- Fsync-off capacity x free share predicts the right scale but undershoots: observed is higher by a mean 0.14 in log (on), 0.19 (settled). So it is an account of the shape, not a calibrated model.
- Frozen settled S3, paired by round: AMIN/B read ratio against the ratio of free shares r = 0.97. Raw 0.995 [0.846, 1.171], sd 0.280; adjusted for free share 1.003 [0.956, 1.052], sd 0.082. This check: raw 1.052 [0.906, 1.222], sd 0.258; adjusted 1.009 [0.972, 1.047], sd 0.064.
- This answers open item (1), read swing larger than bursts directly explain: the bursts take a median 4.9 to 8.3% of the window, but the free share is only about 25 to 30%, so the loss is proportionally much larger.
- Limits: post hoc on outcomes measured in the same trial, so it doesn't establish direction. Hold share vs burst share r = 0.94 in settled but 0.44 here, so bursts are not the whole of the variation in hold. H-R stays failed; the adjusted CI is not a test of it.

## Phase 2 (4 Nov). Exploratory, on existing data, no new trials.
Criteria written before running the analysis. The only look so far is the 8 non-legacy Mixed cells, seen while planning, so those are the development set and cannot count as evidence. No other cell has been computed.

### 2a: does the write penalty collapse onto hold / cycle?
x = h / T_L, with h CoarseLock's arithmetic mean trial read critical section and T_L Lease's mean per-trial commit cycle in the same cell and condition. T_CL is not used, because T_CL = 1/W_CL makes the axis partly circular with S. T_L is also in S, so some coupling remains.

Two models, no fitted parameters: M1, S = 1 / (1 + k h / T_L). M2, the same with CoarseLock's mean trial p95 critical section in place of the mean, because the writer may wait for a slow admitted reader and not an average one. k is exclusive acquisitions per commit: 2 on the default path, 1 for intervention inline cells.

Test set, none seen: intervention second and inline cells, compaction on and off cells, Churn cells, table-count cells with a writer. Legacy scan is reported separately and counts for nothing.

A model collapses only if, on the test set, all three hold:
1. Median |log S - log predicted| is at most ln(1/0.90).
2. The largest |log S - log predicted| is at most 2 ln(1/0.90).
3. Where a cell exists at both r = 4 and r = 16 with otherwise the same settings, the mean of (r=16 residual minus r=4 residual) lies within +/- ln(1/0.90).

If neither model passes, the paper says no collapse was found and does not offer a rule. If one passes, it is reported as exploratory, with the k = 1 cells called out because they vary k. Nothing here is a confirmatory test.

### 2b: latency distributions
The records hold no read-latency histograms, only min, mean, p50, p95, p99 and p999, so read CDFs are not possible. Quantile plot only for the cell where the paper infers bimodality (Mixed, r = 4, d = 0, CoarseLock and Lease). Commit-phase CDFs from the settled histograms, which are complete. The quantile plot can't confirm bimodality, and the paper must keep saying so.

### Result: no collapse, no rule
Neither model met the criteria on the 39 test cells. M1: median |residual| 0.031 (passes), largest 0.307 against a bound of 0.211 (fails), r=16 minus r=4 mean -0.042 over 19 pairs (passes). M2: median 0.106 (fails, by 0.0004), largest 0.448 (fails), reader difference 0.002 (passes). Spearman of x and S -0.906, description only. So the paper says no collapse was found and offers no rule. I rechecked four test cells from raw records, including the two largest misses, and they matched.

M1 was close in most cells: 32 of 39 fell inside the margin, and 6 of the 8 k = 1 cells. The two cells beyond the bound are the intervention's r = 16, d = 2 ms cells (-0.228 and -0.307), where writes fell further than the model says. Five of the seven misses are at 2 ms, where the delay changes from a spin to a sleep; the other two are r = 16, d = 200 us (second) and Churn r = 1. Not separated. Tempting to loosen criterion 2 after seeing this, but it stays as written.

Separate from the criteria: the settled flush-check CDF at d = 2 ms under CoarseLock falls in two steps, near one and near two delays, so that p50 hides a second cluster. Not predicted and not analysed. Read quantiles show p50 45 vs 808 us and similar p95 and p99, which is consistent with the inferred fast and blocked reads but does not establish modes. Mean above p95 in 0/10 trials per arm.

## X-followup-v1 execution log (4 Nov). Pre-registered, frozen at fc7998c.
- Built in a worktree of fc7998c, so the later lock, compaction and direct-read code is not in the runner. Runner sha256 6d065832..., plan sha256 394675b7..., matching the protocol.
- Mechanical smoke, excluded from analysis: first 7 trials in `followup_smoke`. Read status, trace and histogram presence, cleanup and ledger only, not outcomes. All succeeded.
- Aborted start, excluded from analysis and not analysed: the operator asked to stop and restart, because a ChatGPT process left behind from earlier Codex runs had to be killed during the run. I stopped it after 13 finished trials, with the 14th killed in flight. Only status lines were seen. Kept unread in `followup_aborted1`. The restart uses the same plan, seeds and runner in a fresh folder, so no trial from the aborted start counts.
- Before the restart: AC on, open windows Claude and Settings only, no Codex, ChatGPT or WSL VM process running; `wslservice` idle.
- Run: 150/150 trials SUCCESS, first attempt, no retries, no dropped traces, 12:22 to 13:43 local. Gaps 20.0 to 22.5 s. AC at the end, no leftover processes at the end. No chat from the operator during the restarted run. The assistant sent one status message right after starting it and ran nothing else.

## X-followup-v1 results (4 Nov). Analysis in `analysis_followup`. Key figures recomputed from raw trials and they match.

Confirmatory:
- H-F1 (primary) NOT supported. Mean per-round slope of log R on log free share: Lease 0.768 [0.731, 0.806], Snapshot 0.779 [0.745, 0.813], 90% t CI, band [0.80, 1.25]. Both intervals also exclude 1, so proportional reads are rejected too, not only the band.
- H-F2 supported. Page L = 299 for both arms, Holm p about 1e-12. Mean ranks 1.0, 2.1, 2.9, 4.0 for Lease, and the same for Snapshot.
- H-F3 NOT supported as a whole; all three were predicted equivalent. Lease/Snapshot reads: 183 w/s 0.992 [0.979, 1.006] equivalent, Holm p < 0.001; 366 w/s 0.992 [0.918, 1.073] equivalent, Holm p 0.048; 549 w/s 0.983 [0.871, 1.108] inconclusive, Holm p 0.106. No evidence of a difference; the 549 interval is simply too wide.
- H-F4 supported. CoarseLock fast 0.555 [0.552, 0.558], valley 0.0011 [0.0009, 0.0013], blocked 0.444 [0.441, 0.447], 95% t CI.
- H-X supported: no flush in any F1 or F3 trial.

Descriptive, no prediction:
| Cell | R Lease / Snapshot | Hold share | R/(R_off x free) |
| --- | --- | --- | --- |
| 183 w/s | 368195 / 370938 | 0.200 / 0.203 | 0.811 / 0.807 |
| 366 w/s | 310745 / 312896 | 0.395 / 0.399 | 0.906 / 0.903 |
| 549 w/s | 238343 / 242780 | 0.571 / 0.563 | 0.979 / 0.962 |
| 732 w/s | 168004 / 164336 | 0.718 / 0.728 | 1.050 / 1.047 |
| 732 w/s, 1 MiB | 151864 / 156635 | 0.729 / 0.723 | 0.987 / 0.981 |
| 732 w/s, sync off | 563435 / 569640 | 0.034 / 0.034 | 1.028 / 1.022 |
- The earlier undershoot is not constant across rate: R falls short of R_off x free by about 19% at 183 w/s and exceeds it by about 5% at 732 w/s.
- Fsync is 94 to 96% of the hold with sync on, about 1 ms per commit.
- Lease/Snapshot reads: 732 w/s 1.023 [0.839, 1.246] inconclusive, spread grows with rate; sync off 0.989 [0.981, 0.998], equivalent.
- Sync off at 732 w/s gives 3.41 [3.06, 3.81] and 3.53 [3.13, 3.98] times the reads, sync-on durability aside. This is not an estimate of what moving fsync outside the lock will give.
- 1 MiB against 16 MiB at 732 w/s: read ratio 0.915 [0.794, 1.054] and 0.968 [0.830, 1.129], one flush per trial against none, hold share about the same.
- F4, all arms: read latency is two-moded with an almost empty valley. Slow-mode median about 877 us (A), 856 us (Lease), 854 us (Snapshot), with a further step near 1.6 ms (about 5.5% of reads at or above 1.2 ms). Blocked share 0.444 (A), 0.554 (Lease), 0.554 (Snapshot). Reads relative to A: Lease 0.822 [0.789, 0.856], Snapshot 0.844 [0.821, 0.867], writes equal within about 3%. The histogram shows the shape only; F4 has no trace, so the link to the commit hold is inferred.

Exploratory, found after seeing the data, untested:
- Slope differs by source of variation. Within a write rate, where free share varies through fsync bursts, the pooled slope is 1.136 (se 0.070, Lease) and 1.137 (se 0.067, Snapshot), close to the earlier post hoc 1.11 and 1.16. Those earlier slopes came from a single write rate. Across write rates it is about 0.77. The 183 w/s cell alone is uninformative within-cell because its free share hardly varies.
- Candidate reason: read cost changes with write rate. Memtable reads per window read rise 0.084, 0.161, 0.239, 0.289 with rate, and R/free rises 459k, 513k, 554k, 591k. Time per read in free time fits a line on that share, r about -0.99 over four cell means. F3 lands about 4% below the line's capacity. The counter covers the whole trial including warmup while the denominator covers the window, so the share is approximate. Share and rate move together by construction here, so this data cannot separate them. Other rate-linked effects are not ruled out.
- Check that could separate them: sync off at 183, 366 and 549 w/s. If memory-served reads explain it, sync-off reads rise with rate, to about 460k at 183 w/s. If fsync has its own extra cost at low rates, they stay near 570k. Not run, not preregistered. Superseded by X-memtable-v1, which also changes memtable share at a fixed write rate.

## X-memtable-v1 execution log (4 Nov). Pre-registered, frozen at 62e4e42.
- Runner sha256 db809a76..., plan sha256 8560df67..., built in a worktree of 62e4e42.
- Mechanical smoke, excluded from analysis: first 4 trials in `memtable_smoke`. Read status, trace presence, cleanup and ledger only, not outcomes. The analyzer's exit code was checked and its output discarded unread. All succeeded.
- Aborted start, excluded from analysis and not analysed: stopped at the operator's request because they needed the laptop. One trial had finished. Kept unread in `memtable_aborted1`. Not a result of the data. The campaign has not been run; a later run uses the same plan, seeds and runner in a fresh `memtable` folder, so nothing from this start counts.
