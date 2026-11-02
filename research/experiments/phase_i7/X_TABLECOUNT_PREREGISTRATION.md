# X-tablecount-v1 preregistration

Status: PRE-DATA DRAFT. FROZEN once committed before any real trial. No real
trial is authorized or executed by preparing this campaign. No engine behavior,
earlier analysis statistics, margin, or frozen plan changes.

## 1. Purpose and scope

Test whether A-min's earlier equivalence with B holds as the active SSTable
count grows. A-min captures one atomic lease per active table inside the manager
read lock and releases every lease afterwards; B pins one immutable reader set.
Earlier cells had about four tables, except churn cells that accumulated about
70. This factor has not previously been varied directly. A free writer's 1 ms
commit hold can mask read cost, so read-only cells are included.

X-compaction-v1 motivates this design: read amplification from accumulated
overlapping tables moved throughput by up to 2.9x there. See the separate
[X-compaction campaign](X_COMPACTION_PREREGISTRATION.md) and its analysis
artifacts. This campaign deliberately excludes that effect: initial tables have
disjoint key ranges, so a lookup reads blocks from one table; overlapping levels
are out of scope. A miss outside all ranges reads no table blocks.

Implementation disclosure: `scanLeasedTables` and its equivalents walk tables
newest to oldest and `lookupBlock` rejects a table by a min/max key comparison,
so every arm still pays up to N in-memory range comparisons per read. That search
cost is common to all arms but runs under the read lock only in A, and it is not
separated from lifecycle cost in campaign throughput; the microbenchmark isolates
lifecycle cost. Tables hold fewer keys as N grows because the keyspace is fixed.
Memtable hits can bypass the SSTable path in writer cells. No compaction is
scheduled. A writer can still flush; report initial and final table counts to
expose deviations from the initial N, without excluding observations by outcome.

## 2. Design and schedule

Campaign ID `X-tablecount-v1`; default master seed 20261105. N in {4, 16, 64,
256}, reader concurrency r in {4, 16}, writer concurrency w in {0, 1}: sixteen
cells. Cell ID `T1_n%03d_r%02d_w%d`, enumerated N, then r, then w in those orders.
Arms A, AMIN, B; ten rounds k = 1..10; 480 trials; no anchors.

Every cell uses block X1, block read path, d = 0, keyspace 10240,
`InitialSSTableCount = N`, `InitialKeysPerSSTable = 10240/N`,
`ExpectedReaderSetSize = N`, 1 MiB memtable, WAL sync on, synchronization
instrumentation on, 0.95 target hit ratio, uniform reads, 2 s warmup, 10 s
measurement window, 200 ms cooldown, and zero compaction interval. Initial
keys/table are respectively 2560, 640, 160, 40. Fixture WAL sync is disabled
outside the treatment, as in the earlier X campaigns. Warmup uses readers only.
`ExpectedReaderSetSize` is spec metadata in harness2, not a fixed-four validator;
the fixture verifies the requested actual count and boundary keys. Synthetic unit
tests exercise all four counts, 10240 keys and zero writers in each arm, without
weakening validation for earlier blocks.

Each round shuffles the sixteen cells using
`rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(16)`.
Arm order is `permutations[(k+index)%6]` using the original cell index and these
six permutations: A/AMIN/B, A/B/AMIN, AMIN/A/B, AMIN/B/A, B/A/AMIN, B/AMIN/A.
Seed is `pairedSeed(master, cellID, k)`: SHA-256 of UTF-8 decimal fields
`master|cellID|k`, first eight bytes big-endian reinterpreted as int64. The seed
is shared across the three arms of a cell-round, not across different N cells.
Trial ID `<cell>_t%02d_<arm>`; Triplet = k.

Driver: `xcampaign -design tablecount`. Plan persistence, runner hash, clean-tree
check, resume ledger, retry, abort rule and hash-bound gate X approval follow the
existing driver protocol. No engine behavior changes are part of this campaign.
Default plan SHA-256 (indented JSON bytes, no trailing newline):
`0bc0042b8b8ad5ba517c911e9ec9a7dab00ec1b1f0c78876fc26be969eddc048`.

## 3. Confirmatory predictions

**H-T1 (primary).** For each (r, w), R_AMIN/R_B decreases with N. One-sided
Page's L trend test, n = 10 rounds as blocks, N ordered 4, 16, 64, 256, predicted
ranks 4, 3, 2, 1, alpha 0.05. Four tests, with Holm across them. Use the existing
X-readcost Page helper and its `method='auto'` handling, including average ranks
for ties. A round contributes only if every N has a complete triplet and defined
positive AMIN/B R ratio. Report actual block counts and rounds if any are lost.

**H-T2.** Equivalence as in X-readcost-v1 H2, for R in all sixteen cells and W in
the eight w = 1 cells. Separate Holm families, one of sixteen R tests and one of
eight W tests. TOST on paired mean ln(AMIN/B), margin +/- ln(1/0.90), 90% paired
Student t CI. `equivalent` means Holm-adjusted TOST p < 0.05;
`not_equivalent` means the 90% CI lies wholly outside the margin; otherwise
`inconclusive`. Untestable hypotheses remain in the planned Holm family as p=1.
Percentile bootstrap sensitivity uses 10000 paired resamples, seed 20261105,
at the same CI level. No pooling across cells.

Prediction: equivalent at N = 4 in every (r, w) for each applicable outcome;
not equivalent at N = 256 in the read-only cells for R. Other outcomes are
reported as found. If no decline exists, the paper must say leasing's per-table
cost was not detectable here. Partial or inconclusive evidence does not establish
absence of cost. W is not applicable when w = 0.

## 4. Descriptive reporting and microbenchmark

No predictions: A/AMIN, A/B and AMIN/B ratios; read p50/p99 per arm; read critical
section mean and p99 per arm (this is where lease cost appears under the read
lock); commit-wait p50/p99; publication waits when present; throughput per arm;
initial/final SSTable count. Per-arm arithmetic summaries retain zeros, report
trial values and 95% Student t CIs across complete triplets. Ratios use 95%
paired t CIs on positive log ratios and deterministic percentile bootstrap
sensitivity. Timing units are nanoseconds. Missing event histograms are NA,
not measured zero durations. Synchronization reset starts at the measured window
and includes drain through shutdown, as in the earlier campaigns.

For each (r, w) and applicable throughput, report the first tested N at which
the geometric mean AMIN/B ratio lies outside [0.90, 1/0.90], and separately the
first N classified `not_equivalent`. No interpolation or assertion of monotonic
crossings; report NA if none. This is descriptive, with no crossing prediction.

`lsmengine/lifecycle_bench_test.go` measures one capture/release cycle without
SSTable search at each N, for A-min and B, using `ReportAllocs` and `RunParallel`
at exactly 1 and 16 workers (`SetParallelism(1)` and GOMAXPROCS equal to workers,
restored afterwards). No observer, memory-tier lookup or I/O is included.
A-min's test helper mirrors its scratch-copy, leases and releases; B calls its
production acquire/release methods. Manager read-lock holder accounting remains
included in both. Scratch storage is primed outside timing; RunParallel worker
setup and occasional pool misses remain part of Go's aggregate measurement.

Publication benchmarks measure an uncontended flush publication from N to N+1
tables, with setup/reset outside timing and no file build or disk I/O. B creates
its shared-reader wrapper and calls the production publisher, including immediate
retirement of the unpinned old snapshot. A-min mirrors the three publication statements from `flushToDisk`
because no standalone A-min publisher exists; spare slice capacity represents
amortized append cost and excludes occasional slice growth. These are test-only
helpers; no engine changes. This is publication cost, not end-to-end flush cost.

Report benchmark ns/op and allocs/op as separate, non-campaign measurements;
never pool with throughput or use in confirmatory tests. The analysis optionally
reads standard Go output from `<data-dir>/lifecycle_bench.txt`, records its hash
and rows, and reports missing measurements explicitly. Rows with one iteration
are labeled compile checks only, with no performance inference. This preparation
permits only `-benchtime=1x` compile checks, not a real benchmark measurement.

## 5. Failures, zeros and missing data

As X-compaction-v1 section 5: outcomes never fail a trial. Missed deadlines,
starvation and zero writes are data. Engine/harness faults (ledger violation,
close/lease-drain error, validation error, crash or timeout) fail; compaction
errors other than engine closure during stop are faults. No compaction is
planned here, so successful compaction work is also rejected as an unplanned
treatment. Retry a failed trial once; after a second failure drop the complete
cell-round triplet from paired tests and descriptive cell summaries. List every
failed attempt and dropped triplet. Successful observations from dropped units
remain visible in trial-level output. Abort if more than 5% fail after at least
40 attempted trials; report collected data as found. No outcome-based exclusions.

For w = 1, any W = 0 in a complete triplet applies W + 0.1 writes/s to every
arm's W log comparisons for that unit, and is flagged. Per-arm values stay
unadjusted. W and commit wait are NA at w = 0, with no W tests or pseudo-count.
Other missing/nonpositive logarithms are excluded only for the affected metric,
explicitly counted and listed. Missing trials require `--allow-partial` and
partial labeling. Latest ledger attempts are authoritative, never stale files.

## 6. Drift and host protocol

As X-compaction-v1 sections 6 and 7: no anchors. Random interleaving across
rounds limits systematic order effects. Report R by round, cell and arm, and
slope of ln(R) against round where defined; no confirmatory drift test.

AC power; Windows power mode "Best performance"; no other user applications;
Defender real-time protection left as found and recorded. Record git revision,
runner SHA-256, timer self-test, power scheme and plan SHA-256 in platform.json.
Real trials require the frozen clean tree and existing hash-bound approval.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_tablecount_analyze.py`, CLI
`--data-dir --output-dir [--allow-partial]`. Outputs `x_tablecount_results.json`
and `x_tablecount_tables.md`, deterministic, with plan/ledger hashes, runner,
revision, platform, exclusions, failures and missing/dropped units. Reuse helpers
by import without altering earlier statistics. Bootstrap seed 20261105.

- Pre-freeze mechanical smoke (excluded from every analysis): on 2026-11-03, 6 trials ran directly through the runner from a dirty tree with an earlier runner build, to check mechanics: T1_n004_r16_w1 and T1_n256_r16_w0, round 1, three arms each. All 6 succeeded. The predictions in section 3 were written before these trials. The operator saw their per-trial values, including a single-round AMIN/B read ratio of about 0.66 at N = 256, r = 16, w = 0, which points the way H-T1 predicts. No design value, prediction or margin was changed because of them. Output is kept in the git-ignored `X_READCOST/tablecount_smoke` folder. No other trial precedes the freeze.
- Unit tests, synthetic analysis fixtures, one-iteration benchmark compile checks
  and a driver dry-run are mechanical verification, not campaign observations.
- No other deviations. This draft awaits a future freeze commit; no commit is
  made as part of this preparation, and paper.tex and paper/ are untouched.
