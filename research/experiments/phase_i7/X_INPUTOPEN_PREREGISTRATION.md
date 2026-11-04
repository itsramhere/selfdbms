# X-inputopen-v1 preregistration

Status: PRE-DATA. FROZEN once committed before any real trial. The engine's default path, earlier
campaign statistics, margins and frozen earlier plans do not change. Earlier results stand as they ran.
This campaign is reported alongside them.

## 1. Purpose

In X-compaction-v1, Lease's commit-wait p99 was 1.43 to 7.69 times Snapshot's across six on cells.
CoarseLock and Lease open compaction input files while holding the exclusive lock, and Snapshot opens them
outside all locks. That asymmetry was disclosed before data and never tested, so the tail gap could not
be attributed to it or to leasing. This campaign removes the asymmetry. The hypothesis was generated from
earlier data; these are new-data tests with fresh seeds.

## 2. Design

Campaign ID `X-inputopen-v1`; default master seed 20261110; driver `-design inputopen`. Plan field
`settle_ms` = 20000. All other protocol follows the existing driver. Arm labels: A = CoarseLock, AMIN =
Lease, B = Snapshot.

### The treatment, spec field `compaction_open_outside_lock`

When true, for A and AMIN, `CompactAll` copies the input file names under the exclusive lock, releases it,
and only then opens the input iterators. `compactionMu` still serializes compactions, so inputs cannot
be retired between the copy and the open; flushes only append. B already opens outside all locks, and the
flag does nothing there. Default false, with the existing path unchanged.

### Cells

The six on cells of X-compaction-v1, `C1_r{04,16}_d{0000,0200,2000}_on`, including its 250 ms timer and
merge-all policy. An entry is one setting in one round, with five runs: A and AMIN each with the flag off
and on, and B. Cell IDs `O1_r04_d0000_default`, `O1_r04_d0000_aligned`. B runs under the `default` cell ID.
Six settings, five runs, ten rounds: 300 trials.

### Order and seeds

Each round k = 1..10 shuffles the six entries with `rand.New(rand.NewSource(int64(master +
uint64(k)))).Perm(6)`. Within an entry, the five runs are shuffled with `rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(5)`, with i the entry's list index. Seed is `pairedSeed(master, entryID, k)`, shared across runs. Trial ID `<cell>_t%02d_<arm>`.

## 3. Definitions

Commit-wait p99 is the trial's `sync_instrumentation.writer_lock_wait_nanos.commit.p99_nanos`, as in
X-compaction-v1. It is the wait for the first exclusive acquisition, so it excludes the writer's own fsync.
Throughput equivalence classes are as X-paced-v1 section 3, margin +/- ln(1/0.90). For tails the margin
is +/- ln(1.5), as in the X-settled-v1 sentinels, with the same three classes on the 90% paired t CI and
Holm-adjusted TOST.

## 4. Confirmatory predictions

**H-O1 (primary).** Opening inputs outside the lock lowers Lease's commit-wait p99. In each of the six
settings, the paired ratio of aligned to default has a 95% log-t CI wholly below 1. Supported only if all
six hold.

**H-O2.** With inputs aligned, the gap to Snapshot closes: Lease aligned / Snapshot commit-wait p99 is
`equivalent` at the tail margin in all six settings, one Holm family. If H-O1 holds and H-O2 fails, the
asymmetry explains part of the gap and not all of it, and the paper says so.

**H-O3.** Aligning does not cost throughput: Lease aligned / Lease default is `equivalent` on writes
and on reads in all six settings, with separate Holm families of six.

**H-O4.** Lease aligned / Snapshot is `equivalent` on writes and on reads in all six settings, with
separate Holm families of six. This repeats H-C of X-compaction-v1 on the aligned path.

## 5. Descriptive reporting, no prediction

CoarseLock default and aligned: commit-wait p99 and throughput ratios, with 95% CIs. Lease default /
Snapshot commit-wait p99, against X-compaction-v1's 1.43 to 7.69, labelled as across-campaign. Compaction
start hold, compaction count, retirement lag and retired-table high water by design and path. Commit-wait
p50 and max.

## 6. Failures, drift, host protocol

Failure, zero and missing-data rules are as X-paced-v1 section 5. Failed attempts are retried once; a
second failure drops that complete entry-round unit from paired tests and it is listed. No outcome-based
exclusions. AC power; no other user applications; Windows power mode and Defender as found and recorded.
The operator and the assistant leave the machine alone during the run, and any interaction or background process is recorded as a deviation.

Limits: one host, one writer, a merge-all timer policy, not a production compaction scheduler. Bursts near
31.5 ms are uncontrolled, though the first-acquisition wait excludes the writer's own fsync. The manager
lock's interface call is present here and absent in X-compaction-v1, so across-campaign comparison is descriptive.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_inputopen_analyze.py`, CLI `--data-dir --output-dir
[--allow-partial]`. Deterministic outputs `x_inputopen_results.json` and `x_inputopen_tables.md` with provenance, plan and ledger hashes, runner, revision, platform, exclusions, failures, missing units and every prediction above. Bootstrap sensitivity uses 10000 paired resamples, seed 20261110.

- Mechanical smoke, excluded from every analysis: to be run after the freeze, first trials of the plan
  only, reading status, cleanup and gaps, not outcomes. Recorded here if run.
- Plan SHA-256 (indented JSON bytes without trailing newline): `5b685feb2327ff8d9b1e72b4bc0637570f0fe0014690ec9e214e84f163893a98`
- Unit tests, synthetic fixtures and the driver dry-run are mechanical verification, not campaign
  observations.
