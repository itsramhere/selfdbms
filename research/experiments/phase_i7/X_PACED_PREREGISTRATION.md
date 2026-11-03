# X-paced-v1 preregistration

Status: PRE-DATA DRAFT. FROZEN once committed before any real trial. Preparing
this campaign authorizes no real trial. A real trial requires separate approval.
No engine code, earlier campaign statistics, margin, or frozen plan changes.

## 1. Purpose

Test the coarse-lock result below saturation. Earlier X campaigns used a
saturated closed-loop writer, so a coarse-lock penalty appeared as lost write
throughput. A paced writer tests whether, at a load the system can carry, the
throughput penalty disappears and becomes a latency penalty; above A's capacity,
A is predicted to fall behind. Closed-loop and open-loop readers are separate
conditions. Service time is completion minus issue; response time is completion
minus the intended absolute arrival time, retaining queueing delay and correcting
coordinated omission for issued requests. Unissued arrivals are reported, not
imputed as observed latency.

## 2. Design and frozen rates

Campaign ID `X-paced-v1`; default master seed 20261106; driver `-design paced`.
Block X4, block read path, r = 16, one paced writer, keyspace 10000, four initial
disjoint SSTables of 2500 keys each, ExpectedReaderSetSize 4, 1 MiB memtable,
WAL sync on, synchronization instrumentation on, target hit ratio 0.95, uniform
reads, 2 s reader-only warmup, 10 s measurement window, 200 ms cooldown, and no
compaction. Fixture WAL sync is disabled outside the measured treatment, as in
earlier X campaigns. Writer flushes remain possible and their counts are reported.

Factors: d in {0, 200, 2000} microseconds, write fraction f in {0.2, 0.8}, reads
in {closed, open}. Twelve cells, enumerated d, then f, then reads in those orders.
Cell ID `P1_d%04d_f%02d_%s`, with f = 20 or 80 and reads = closed or open.
Absolute rates are frozen before data, derived from X-readcost-v1 A-min at
r = 16: W = 914.8, 773.6, 357.5 writes/s and R = 25123.5, 16022.3, 5693.9
reads/s, in ascending d order. No rate is estimated from this campaign's data.

| d (us) | f=0.2 writes/s | f=0.8 writes/s | open reads/s |
| --- | --- | --- | --- |
| 0 | 183 | 732 | 12500 |
| 200 | 155 | 619 | 8000 |
| 2000 | 72 | 286 | 2850 |

Open read rates are half of earlier closed-loop A-min capacity, rounded as in
the table. Earlier X1 A capacity at r = 16 was W = 856.7, 545.8, 111.0.
Thus f = 0.8 is above A's earlier saturated capacity at d = 200 and 2000 and
below it at d = 0; f = 0.2 is below it everywhere. Capacity may differ under
open-loop reads. MaxLatenessMs is 1000. Go scheduling and sleep accuracy limit
how well high open-loop rates are met; report lateness and abandoned arrivals.

Arms A, AMIN, B; ten rounds k = 1..10; 360 trials; no anchors. Each round
shuffles the cells with
`rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(12)`.
Arm order is `permutations[(k+index)%6]` using the original cell index. The six
permutations are A/AMIN/B, A/B/AMIN, AMIN/A/B, AMIN/B/A, B/A/AMIN, B/AMIN/A.
Seed is `pairedSeed(master, cellID, k)`: SHA-256 of UTF-8 decimal fields
`master|cellID|k`, first eight bytes big-endian reinterpreted as int64. Each
cell-round shares its seed across arms. Trial ID `<cell>_t%02d_<arm>`; Triplet = k.

X4 uses the existing deterministic BuildSchedule for open reads. Writes have
absolute intended targets `i * periodNs`, starting i = 0, where
`periodNs = int64(1e9 / rate)`. Offered ticks have targets strictly before the
window end: `ceil(windowNs / periodNs)`; integer rounding can add one tick
relative to rate times duration. Once the window ends, remaining ticks are
unissued. Both reader and writer request ledgers conserve offered, attempted,
in-window, late, failed, abandoned and queued work. Late completions have separate
histograms and never enter in-window latency or throughput. Max read lateness is
the maximum observed issue/dequeue lateness, including abandoned arrivals; ticks
or arrivals never visited have no measured lateness.

Default plan SHA-256 (indented JSON bytes without trailing newline):
`384aace771c82b3e16eb0e1cb5363678cfff075a420ce8fcd70045ad82cb90e8`.
Plan persistence, runner hash, clean-tree check, resume ledger, retry, abort rule,
and hash-bound gate X approval follow the existing driver protocol.

## 3. Definitions and confirmatory predictions

Achieved fraction = write completions in window / offered ticks. A trial is
`saturated` if achieved fraction < 0.95. Write response p99 in saturated trials
is censored from below because ticks never issued have no latency. Report it
flagged; never treat it as a true p99. The primary test retains defined observed
completion p99s and identifies every round with a censored component; when such
rounds occur its trend concerns completion-only tails, not uncensored p99s.
This disclosure is necessary because overload is an anticipated outcome, not an
exclusion criterion. Ratios with a censored component need not bound a true
ratio in either direction.

**H-P1 (primary).** For each (f, reads), the ratio A/AMIN of write response p99
rises with d. One-sided Page's L, n = 10 rounds as blocks, d ordered 0, 200,
2000, predicted ranks 1, 2, 3, alpha 0.05. Four tests, Holm across them. Use
scipy's `page_trend_test(method='auto')`, with average ranks for ties, matching
the existing Page helper's convention. A round contributes only if every d has
a complete triplet and a defined positive A/AMIN ratio. Report actual block
counts, contributing rounds and censored rounds. Untestable hypotheses stay
in the planned Holm family as p = 1.

**H-P2 (below capacity).** At f = 0.2, W_A/W_AMIN is equivalent in all six
cells. TOST as X-readcost H2, margin +/- ln(1/0.90), 90% paired Student t CI,
one six-cell Holm family. Below capacity throughput is predicted to show no
coarse-lock penalty. This predicts achieved rate; equivalence does not imply
equal latency.

**H-P3 (above A's capacity).** At f = 0.8, W_A/W_AMIN is classified
`not_equivalent` with A lower at d = 200 and 2000, and equivalent at d = 0,
in both read modes. Same test, separate six-cell Holm family.

**H-P4 (leasing).** W_AMIN/W_B is equivalent in all twelve cells, and
R_AMIN/R_B is equivalent in the six closed cells. Separate Holm families of
12 and 6 tests. There is no open-cell R equivalence test. Read response p99
and write response p99 AMIN/B are descriptive only, with no prediction, in
light of X-compaction-v1's commit tail difference.

For all equivalence tests, `equivalent` means Holm-adjusted TOST p < 0.05;
`not_equivalent` means the 90% paired t CI lies wholly outside the margin;
otherwise `inconclusive`. A lower means that CI lies below the lower margin.
Untestable hypotheses remain p = 1 in the planned family. Percentile bootstrap
sensitivity uses 10000 paired resamples, seed 20261106, at the same CI level.
No pooling across cells, doses, read modes, loads or arms.

## 4. Descriptive reporting, no prediction

Report per arm read response p50/p99 in open cells, read service p50/p99,
write response and service p50/p99, commit wait p50/p99, achieved fractions,
offered and unissued ticks, abandoned and unissued reads, maximum read lateness,
R in closed cells and A/AMIN read ratios, and flush counts. Include a saturated
trial count per cell and arm, for complete triplets and all successful trials.
Read response has no scheduled meaning in closed cells and is NA there.
Open read throughput is reported descriptively, with no R equivalence test.
Retain late histograms separately in trial output. Synchronization instrumentation
is reset at window start and includes drain through shutdown, as in earlier X.

Report per-arm arithmetic summaries and trial values, retaining zeros, with 95%
Student t CIs across complete triplets. Report 95% paired t CIs on positive log
ratios where defined; latency units are nanoseconds. Count and list undefined
metrics, missing/nonpositive log pairs and censored observations. No pseudo-count
on latency. AMIN/B read and write tails have no prediction. A trial with zero
writes, or with no defined p99, leaves that latency metric undefined for that
unit and is counted. Saturated response summaries and ratios are explicitly
labeled completion-only, censored from below where applicable, never true p99s.

## 5. Failures, zeros and missing data

As [X-tablecount sections 5-6](X_TABLECOUNT_PREREGISTRATION.md): outcomes never
fail a trial. Missed deadlines, abandonment, scheduled shortfall, starvation,
saturation and zero writes are data. Total read abandonment is also data;
read latencies are undefined when no read completes. Engine/harness faults (ledger violation,
operation error, close/lease-drain error, validation error, crash or timeout)
fail. No compaction is planned; compaction errors or successful compaction work
are rejected as faults or unplanned treatment. Retry a failed trial once; after
a second failure drop the complete cell-round triplet from paired tests and
cell summaries. List every failed attempt and dropped triplet. Successful
observations in dropped units remain visible in trial-level output. Abort if
more than 5% fail after at least 40 attempted trials; report data as found.
No outcome-based exclusions.

If any W = 0 in a complete triplet, use W + 0.1 writes/s for every arm's W log
comparison in that unit, flagging the adjustment. Per-arm values stay unchanged.
There are no latency pseudo-counts. Other missing/nonpositive logarithms are
excluded only for the affected metric, explicitly counted and listed. Missing
trials require `--allow-partial` and partial labeling. Latest ledger attempts
are authoritative; stale trial files cannot override failures.

## 6. Drift, host protocol and limits

No anchors. Random interleaving across rounds limits systematic order effects.
Report R by round, cell and arm, with the slope of ln(R) against round where
defined; no confirmatory drift test. AC power; Windows power mode "Best
performance"; no other user applications; Defender real-time protection left
as found and recorded. Record revision, runner SHA-256, timer self-test, power
scheme, and plan SHA-256 in platform.json. Real trials require the frozen clean
tree and the existing hash-bound approval.

Limits: one writer; r fixed at 16; rates derived from earlier closed-loop
A-min capacity, not calibrated to this campaign; open rates fixed at half of
that earlier capacity before data. Open-loop read capacity can differ. Go
scheduling/sleep accuracy constrains high rates, making lateness essential.
At f = 0.8 and d = 0, A is close to its earlier capacity, so that cell may be
sensitive. Censoring in saturated trials prevents interpreting observed write
response p99 as the true offered-work p99. In-window tails exclude late
completions, whose distributions are reported separately.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_paced_analyze.py`, CLI
`--data-dir --output-dir [--allow-partial]`. Deterministic outputs
`x_paced_results.json` and `x_paced_tables.md`, with provenance, plan/ledger
hashes, runner, revision, platform, exclusions, failures, missing/dropped units,
all four predictions and descriptive results above. Existing analysis helpers
are imported without altering earlier statistics. Bootstrap seed 20261106.

- Pre-freeze mechanical smoke (excluded from every analysis): on 2026-11-03, 6 trials ran directly through the runner from a dirty tree with an earlier runner build, to check mechanics: P1_d0000_f80_open and P1_d2000_f80_closed, round 1, three arms each. All 6 succeeded. The predictions in section 3 were written before these trials. The operator saw their per-trial values, including, in the single round, A completing 1049 of 2861 offered writes at d = 2000, f = 0.8 against 2860 for AMIN and B, and all three arms short of the 0.95 achieved fraction or close to it at d = 0, f = 0.8 with open reads (achieved fractions about 0.97, 0.95 and 0.98 for A, AMIN and B). No design value, prediction or margin was changed because of them. Output is kept in the git-ignored `X_READCOST/paced_smoke` folder. No other trial precedes the freeze.
- Unit tests, synthetic analysis fixtures and the driver dry-run are mechanical
  verification, not campaign observations.
- No other deviations. Preparation leaves paper.tex, paper/ and lsmengine/ untouched.
