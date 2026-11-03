# X-settled-v1 preregistration

Status: PRE-DATA. FROZEN once committed before any real trial. No engine behaviour,
earlier campaign statistic, margin, or frozen earlier plan changes. Earlier
pre-registered results stand as they ran. This campaign is reported alongside them,
never in place of them.

## 1. Purpose

A post-hoc, exploratory diagnosis after X-paced-v1 found that the write-service
stall of about 55 ms sat inside the WAL fsync and followed the immediately preceding
trial: in nine pairs the largest fsync had median 8.0 ms after a 30 s pause and 54.9 ms
straight after a trial. All five earlier campaigns started each trial about 0.04 s
after the last. This campaign pauses before every trial and asks:

1. Does the descriptive below-capacity median write penalty hold as a tested trend?
2. Does a clearly higher offered rate at 200 us separate CoarseLock from Lease, which
   X-paced-v1's design could not do?
3. Is the inconclusive Lease/Snapshot read comparison at d = 0, f = 0.8 resolved?
4. Does a direct measurement of commit phases show two lock waits under CoarseLock?
5. Do key cells from each earlier campaign change when trials are paused?

The earlier values below have been seen. Questions 1 to 4 are new-data tests of
patterns found on earlier data, using fresh seeds. They are not independent of how the
hypotheses were generated.

## 2. Design

Campaign ID `X-settled-v1`; default master seed 20261107; driver `-design settled`.
Plan field `settle_ms` = 20000. The driver waits this long before every
attempt, including the first and retries. Its value comes from a non-campaign
calibration (section 7). All other protocol follows the existing driver: plan
persistence, runner hash, clean-tree check, resume ledger, one retry, hash-bound
gate X approval, 5% abort after 40 attempts.

Arm labels are code names: A = CoarseLock, AMIN = Lease, B = Snapshot.

### Paced cells (block X4)

As X-paced-v1: r = 16, one paced writer, keyspace 10000, four initial tables of 2500
keys, ExpectedReaderSetSize 4, 1 MiB memtable, WAL sync on, sync instrumentation on, hit
ratio 0.95, uniform reads, 2 s warmup, 10 s window, 200 ms cooldown, no compaction,
MaxLatenessMs 1000. One addition: `diag_trace` is on, which adds commit-phase timing, a
1 ms heartbeat goroutine and per-reader slowest reads. This separates these trials from
X-paced-v1.

| Cell | d (us) | writes/s | reads | Open reads/s |
| --- | --- | --- | --- | --- |
| `S1_d0000_f20_closed` | 0 | 183 | closed | |
| `S1_d0000_f20_open` | 0 | 183 | open | 12500 |
| `S1_d0200_f20_closed` | 200 | 155 | closed | |
| `S1_d0200_f20_open` | 200 | 155 | open | 8000 |
| `S1_d2000_f20_closed` | 2000 | 72 | closed | |
| `S1_d2000_f20_open` | 2000 | 72 | open | 2850 |
| `S2_d0200_w700_closed` | 200 | 700 | closed | |
| `S2_d0200_w700_open` | 200 | 700 | open | 8000 |
| `S3_d0000_f80_closed` | 0 | 732 | closed | |

The S2 rate is fixed before data: earlier CoarseLock capacity 545.8 over 700 is 0.78, well
below the 0.90 margin; earlier Lease capacity 773.6 stays above the offered rate.
Nine cells, three arms, ten rounds: 270 trials.

### Sentinel cells

Specs are copied from the earlier plans by their generator functions, byte for byte
except Cell, Seed, Triplet, Arm, order fields and TrialID. `diag_trace` is off.

| Cell | Source | Original cell | Runs |
| --- | --- | --- | --- |
| `R_X1_r16_d0020` | X-readcost-v1 | `X1_r16_d0020` | 3 arms x 10 rounds |
| `R_X1_r16_d2000` | X-readcost-v1 | `X1_r16_d2000` | 3 arms x 10 rounds |
| `R_X2_r16_d0200` | X-readcost-v1 | `X2_r16_d0200` | 3 arms x 10 rounds |
| `R_X3_r16` | X-readcost-v1 | `X3_r16` | 3 arms x 10 rounds |
| `R_C1_r16_d0200_on` | X-compaction-v1 | `C1_r16_d0200_on` | 3 arms x 10 rounds |
| `R_T1_n256_r16_w0` | X-tablecount-v1 | `T1_n256_r16_w0` | 3 arms x 10 rounds |
| `R_I1_r16_d0200_{second,inline}` | X-intervention-v1 | `I1_r16_d0200_{second,inline}` | 2 conditions x arms A, AMIN x rounds 1 to 5 |

180 + 20 = 200 trials. Total 470.

### Order and seeds

There are 17 cells. The two intervention conditions form one entry, `R_I1_r16_d0200`, so
the list above has 16 entries, in the listed order, with that entry last. Each round
k = 1..10 shuffles its entries with
`rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(n)`, where n = 16 for k <= 5
and n = 15, without the intervention entry, for k > 5. The intervention entry's four
runs are ordered with
`rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(4)`, as in the
intervention plan. Other entries use `permutations[(k+i)%6]` with i the entry's list index.
Seed is `pairedSeed(master, entryID, k)`, shared across arms and, for the intervention,
conditions. Trial ID `<cell>_t%02d_<arm>`.

## 3. Definitions

Write response, write service, saturated, censoring and the equivalence classes are as
X-paced-v1 sections 1 and 3. `equivalent` means Holm-adjusted TOST p < 0.05; `not_equivalent`
means the 90% paired Student t CI on the log ratio lies wholly outside +/- ln(1/0.90);
otherwise `inconclusive`. Bootstrap sensitivity uses 10000 paired resamples, seed 20261107.

## 4. Confirmatory predictions

**H-M1 (primary).** For each read mode, the A/AMIN write response p50 ratio at the S1
cells rises with d. One-sided Page's L, n = 10 rounds as blocks, d ordered 0, 200, 2000,
predicted ranks 1, 2, 3, alpha 0.05. Two tests, Holm across them. `page_trend_test(method='auto')`,
as in X-paced-v1.

**H-M2.** At d = 200 and d = 2000 in both read modes, the A/AMIN write response p50 ratio is
`not_equivalent` with A higher. Four tests, one Holm family. d = 0 has no prediction, since
earlier values of 1.093 and 1.007 sit at or inside the margin; report it descriptively.

**H-M3 (mechanism).** At `S1_d2000_f20_closed`, both of A's lock_wait p50 and flush_check p50
lie in [1, 3] ms, and both of AMIN's lie below 0.5 ms. The summary is the arithmetic mean of
per-trial p50s over complete rounds, with the 95% t CI wholly inside the band. Supported only
if all four hold. Open cells and other doses are descriptive.

**H-W.** At both S2 cells, W_A/W_AMIN is `not_equivalent` with A lower. Two tests, one Holm family.

**H-R.** At `S3_d0000_f80_closed`, R_AMIN/R_B and W_AMIN/W_B are `equivalent`. Two tests, one Holm
family. Report the same comparison at S1 cells descriptively.

**H-S (protocol check).** In every S1 cell and arm, the median over rounds of the trial's largest
WAL fsync is below 40 ms. X-paced-v1 had no direct fsync measure; its largest write service had
cell medians of 53 to 74 ms. The threshold sits above the bursts near 31.5 ms, which the calibration
showed a pause does not remove. Those bursts are not excluded and are reported.

### Sentinel rule (H-C)

For each metric below, compute the mean paired log ratio over complete rounds in the new and in
the original campaign, and a 90% Welch CI on new minus original.

- `unchanged`: CI wholly inside +/- margin.
- `changed`: CI wholly outside +/- margin.
- `inconclusive`: otherwise.

The margin is ln(1/0.90) for throughput metrics and ln(1.5) for tail metrics. Rounds are 5 for the
intervention and 10 otherwise, as in each original.

| Cell | Throughput metrics | Tail metrics |
| --- | --- | --- |
| `R_X1_r16_d0020` | W_A/W_AMIN, R_A/R_AMIN, W_AMIN/W_B, R_AMIN/R_B | |
| `R_X1_r16_d2000` | W_A/W_AMIN, R_A/R_AMIN, W_AMIN/W_B, R_AMIN/R_B | |
| `R_X2_r16_d0200` | R_AMIN/R_B | publication lock-wait p99 AMIN/B |
| `R_X3_r16` | W_AMIN/W_B, R_AMIN/R_B | |
| `R_C1_r16_d0200_on` | W_AMIN/W_B, R_AMIN/R_B | commit-wait p99 AMIN/B |
| `R_T1_n256_r16_w0` | R_AMIN/R_B | |
| `R_I1_r16_d0200` | W_inline/W_second and R_inline/R_second in A and in AMIN | read p99 inline/second in A and in AMIN |

Also report the publication in-window counts per arm, descriptively.

**Commitment.** A rerun of that campaign, with both reported, follows from a `changed` result on any
metric, or from a contradicted classification. A classification is contradicted only for a metric with a
published equivalence class in the original: the original's published class was `equivalent` and the new
unadjusted 90% paired t CI lies wholly outside +/- ln(1/0.90), or the published class was
`not_equivalent` and that CI lies wholly inside. These checks are not Holm-adjusted, because each is a
trigger for further work and not a claim; the new CI is reported beside the published class. `inconclusive` triggers no rerun
and is reported as not ruling out a change. The comparison confounds the pause with the date,
the rebuilt binary and fresh seeds, so `unchanged` is evidence about the combined difference.

No pooling across cells, doses, read modes or arms.

## 5. Descriptive reporting, no prediction

All X-paced-v1 section 4 quantities for X4 cells. For every X4 trial: largest fsync, count of fsyncs at
or above 10 and 25 ms, their time as a share of the window, the commit-phase p50s, heartbeat gaps at or
above 10 ms and GC pauses. Mean achieved gap between trials from the ledger. A/AMIN ratios of write
response p95 and p99, and the standard deviation of each log ratio. Log reads against write excess,
(mean - p50) x count, across trials at S3. Comparison of S1 and S3 ratios with X-paced-v1, labelled as
across-campaign and descriptive.

## 6. Failures, drift, host protocol

Failure, zero and missing-data rules are as X-paced-v1 section 5. Failed attempts are retried once; a
second failure drops that complete cell-round unit from paired tests and it is listed. No outcome-based
exclusions. No anchors. Report reads by round, cell and arm. AC power; no other user applications;
Windows power mode as found and recorded; Defender left as found and recorded. Record revision, runner
SHA-256, timer self-test, power scheme and plan SHA-256 in platform.json. The operator is away from the
machine during the run and did not check its state.

Limits: one host, one writer; the rates and earlier values are not calibrated from this campaign. The
pause was chosen on one setting, d = 0, f = 0.2, closed, on a separate calibration. The trigger for the
carry-over within a trial is unknown, and bursts near 31.5 ms are unexplained and not controlled.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_settled_analyze.py`, CLI
`--data-dir --output-dir --readcost-dir --compaction-dir --tablecount-dir --intervention-dir --paced-dir [--allow-partial]`. Deterministic
outputs `x_settled_results.json` and `x_settled_tables.md` with provenance, plan and ledger hashes,
runner, revision, platform, exclusions, failures, missing units, every prediction above, and the sentinel table.
Existing helpers are imported without altering earlier statistics.

- Calibration before the freeze (non-campaign, seen by the operator's assistant before the freeze; evidence in `X_READCOST/stall_diag/pause`): 36 trials at d = 0, f = 0.2, closed, six at each pause. Largest fsync median and range in ms: 0 s 60.4 (53.7 to 74.0); 5 s 19.6 (7.0 to 46.8); 10 s 7.5 (5.5 to 54.3); 15 s 20.4 (3.3 to 35.9); 20 s 31.5 (7.8 to 36.0); 30 s 31.3 (5.6 to 36.1). Pauses of 15 s or more removed the band above about 45 ms in 18/18 trials, not the 31.5 ms bursts, which appeared in 11 of those 18. 20 s was chosen over 15 s as a margin for cells that leave more files behind. H-S was set at 40 ms after seeing this; the first draft said 25 ms. No trial of this campaign preceded this change.
- Mechanical smoke (excluded from every analysis): on 2026-11-03, before the freeze, the first 6 trials of the plan ran from a dirty tree with an earlier runner build, to check mechanics: `S1_d0000_f20_open` and `R_X1_r16_d0020`, round 1, three arms each. All 6 succeeded with gaps of 20.0 s. Only their status, block, trace presence, cleanup and gaps were read, not their outcomes. Output is in the git-ignored `X_READCOST/settled_smoke` folder.
- Plan SHA-256 (indented JSON bytes without trailing newline): `7c868047870d72cf2d66100aade809f01549ee3a69f68357a5994b34a6c9c777`.
- Unit tests, synthetic fixtures and the driver dry-run are mechanical verification, not campaign observations.
