# X-followup-v1 preregistration

Status: PRE-DATA. FROZEN once committed before any real trial. No engine behaviour,
earlier campaign statistic, margin or frozen earlier plan changes. Earlier results stand
as they ran. This campaign is reported alongside them.

## 1. Purpose

Two post hoc findings need a test on new data.

1. In X-settled-v1 and an exploratory check, reads tracked the time left outside the
   writer's exclusive hold (log R against log free share: r 0.905 and 0.965, slope 1.11 and
   1.16). Hold share was only observed there, at one write rate. This campaign sets it by
   treatment, using write rate.
2. The paper infers from percentiles that CoarseLock read latency at low delay mixes a fast
   read with one blocked behind a commit. Trials stored no read histograms, so this was never
   checked. This campaign stores them.

Both hypotheses arose from earlier data; these are new-data tests with fresh seeds, not
independent of how the hypotheses were generated.

## 2. Design

Campaign ID `X-followup-v1`; default master seed 20261108; driver `-design followup`.
Plan field `settle_ms` = 20000. All other protocol follows the existing driver: plan
persistence, runner hash, clean-tree check, resume ledger, one retry, hash-bound gate X
approval, 5% abort after 40 attempts. Arm labels are code names: A = CoarseLock, AMIN = Lease,
B = Snapshot.

### Paced cells (block X4)

As `S3_d0000_f80_closed` of X-settled-v1: r = 16, one paced writer, closed readers, d = 0,
keyspace 10000, four initial tables of 2500 keys, ExpectedReaderSetSize 4, hit ratio 0.95,
uniform reads, 2 s warmup, 10 s window, 200 ms cooldown, no compaction, MaxLatenessMs 1000,
sync instrumentation on, `diag_trace` on. Arms AMIN and B only, since CoarseLock's own wait
would change what hold share means.

| Cell | writes/s | Memtable | WAL sync |
| --- | --- | --- | --- |
| `F1_w0183_m16` | 183 | 16 MiB | on |
| `F1_w0366_m16` | 366 | 16 MiB | on |
| `F1_w0549_m16` | 549 | 16 MiB | on |
| `F1_w0732_m16` | 732 | 16 MiB | on |
| `F2_w0732_m01` | 732 | 1 MiB | on |
| `F3_w0732_m16_syncoff` | 732 | 16 MiB | off |

A 16 MiB memtable keeps these windows free of flushes, so write rate does not also change the
flush count, which was a known confound (S1 cells had 0 flushes and S3 about 1). F2 keeps the
old threshold to show what the flush adds. F3 gives read capacity with the fsync hold removed;
the campaign harness allows WAL sync off only with `diag_trace`.
Six cells, two arms, ten rounds: 120 trials.

### Mixed cell (block X1)

`F4_r04_d0000`: the specification of first-campaign `X1_r04_d0000`, with
`persist_read_histogram` on. Arms A, AMIN, B, ten rounds: 30 trials. Total 150 trials.

### Order and seeds

Each round k = 1..10 shuffles the seven cells with
`rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(7)`. Three-arm cells use
`permutations[(k+i)%6]`, with i the cell's list index. Two-arm cells run AMIN first when
(k+i) is even and B first otherwise. Seed is `pairedSeed(master, cellID, k)`, shared across arms.
Trial ID `<cell>_t%02d_<arm>`.

## 3. Definitions

Hold share per trial: (`validate` + `wal_write` + `wal_sync` + `apply`) phase means times
commit count, over the measured window. Free share = 1 - hold share. The equivalence classes
are as X-paced-v1 section 3: `equivalent` means Holm-adjusted TOST p < 0.05, `not_equivalent` means
the 90% paired Student t CI on the log ratio lies wholly outside +/- ln(1/0.90), otherwise
`inconclusive`.

Read latency regions for F4, using persisted histogram buckets: fast below 200 us, valley from
200 us up to 500 us, blocked at or above 500 us. The bounds come from the first campaign's stored
percentiles (CoarseLock p50 45 us, p95 1619 us), which have been seen. The histograms have not.
A histogram bucket belongs to the region containing its lower bound. Buckets are under 0.8% wide, so this moves a sample by less than that across a bound. This is the frozen rule, and H-F4 is decided by it.

## 4. Confirmatory predictions

**H-F1 (primary).** Per round and arm, take the OLS slope of log R on log free share across the
four F1 cells. For each of AMIN and B, the arithmetic mean slope over rounds has a 90% t CI wholly
inside [0.80, 1.25]. Supported only if both hold. The band is a judgment: proportional reads give
1, and the earlier post hoc slopes of 1.11 and 1.16 lie inside it. Both must hold, so no
multiplicity adjustment applies. The treatment here is the write rate; free share is measured.

**H-F2.** For each of AMIN and B, reads rise as write rate falls. One-sided Page's L, ten rounds as
blocks, cells ordered 732, 549, 366, 183, predicted ranks 1, 2, 3, 4, alpha 0.05, Holm across the
two tests, `page_trend_test(method='auto')` as in X-paced-v1.

**H-F3.** R_AMIN/R_B is `equivalent` at `F1_w0183_m16`, `F1_w0366_m16` and `F1_w0549_m16`. Three
tests, one Holm family. `F1_w0732_m16` has no prediction, because this comparison was inconclusive
twice at that rate with a 1 MiB memtable; report it descriptively.

**H-F4.** In F4, for CoarseLock (A), the arithmetic mean over trials of the fast share and of the
blocked share are each at least 0.20, and of the valley share at most 0.10, each with a 95% t CI
wholly on the predicted side. Supported only if all three hold. Lease, Snapshot and all other
regions are descriptive.

**Protocol check H-X.** No F1 or F3 trial has a measured flush. A flush is reported and the
trial is kept; H-F1 is then reported with and without that round, with the frozen form being the
one including it.

## 5. Descriptive reporting, no prediction

Hold and free share per paced cell and arm; F4 has no `diag_trace`, so it has no phase data. R against R_off x free share per F1 cell, using each arm's
F3 median, and whether the earlier undershoot (mean log 0.14 and 0.19) is constant across rate.
F2 against `F1_w0732_m16`: read ratio, hold share and flush count. Largest fsync and count of fsyncs
at or above 25 ms by paced cell, since this also bears on burst rate, without a flush. Spread of log R by
cell. Full histograms for F4 in all arms, with a CDF figure.

## 6. Failures, drift, host protocol

Failure, zero and missing-data rules are as X-paced-v1 section 5. Failed attempts are retried once;
a second failure drops that complete cell-round unit from paired tests and it is listed. No
outcome-based exclusions. AC power; no other user applications; Windows power mode and Defender as
found and recorded. Record revision, runner SHA-256, timer self-test, power scheme and plan SHA-256 in
platform.json. The operator and the assistant leave the machine alone during the run, and any
interaction or background process is recorded as a deviation.

Limits: one host, one writer, injected-delay engine, no read delay in the paced cells. Bursts near
31.5 ms remain uncontrolled. A larger memtable changes how many reads hit memory, so F1 is not
identical to S3; F2 reports that difference.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_followup_analyze.py`, CLI `--data-dir --output-dir
[--allow-partial]`. Deterministic outputs `x_followup_results.json` and `x_followup_tables.md` with
provenance, plan and ledger hashes, runner, revision, platform, exclusions, failures, missing units
and every prediction above. Bootstrap sensitivity uses 10000 paired resamples, seed 20261108.

- Mechanical smoke, excluded from every analysis: to be run after the freeze, first trials of the
  plan only, reading status, trace presence, cleanup and gaps, not outcomes. Recorded here if run.
- Plan SHA-256 (indented JSON bytes without trailing newline): `394675b73698ef4afeed5af747b59970bc57cec03c66bc9c7642322ff2e04f51`.
- Unit tests, synthetic fixtures and the driver dry-run are mechanical verification, not campaign
  observations.
