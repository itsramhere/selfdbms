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
