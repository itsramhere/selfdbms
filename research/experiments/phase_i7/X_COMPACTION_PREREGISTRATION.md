# X-compaction-v1 preregistration

Status: PRE-DATA DRAFT. This document becomes FROZEN once committed before any
real trial. This change does not authorize or execute a trial. No earlier test,
margin, or result changes.

## 1. Purpose

Earlier campaigns ran zero compactions (679 of 679 trials), and X1 had at most
one flush per trial. The earlier A-min/B throughput equivalence therefore did
not exercise file retirement. This is a separate, pre-registered test of that
claim when compaction retires SSTables, including lifecycle costs.

## 2. Design and schedule

Campaign ID `X-compaction-v1`; master seed 20261104 unless explicitly supplied.
Reader concurrency r in {4, 16}; injected block-read delay d in {0, 200, 2000}
microseconds; compaction in {off, on}: 12 cells. Base ID `C1_r%02d_d%04d`;
full cell ID `<base>_<off|on>`. Arms A, AMIN, B; ten rounds k = 1..10;
360 trials, with no anchors. Trial ID `<cell>_t%02d_<arm>`; Triplet = k.

Every cell uses block X3 and the X3 common specification: block read path,
16 KiB memtable, one free writer, WAL sync on, synchronization instrumentation
on, uniform reads, 95% target hit ratio, keyspace 10,000, four initial SSTables
with 2,500 keys each, expected reader set size four, 2 s warmup, 10 s measurement
window and 200 ms cooldown. Fixture construction disables WAL sync outside the
treatment, as in X-readcost-v1. Warmup uses readers only.

On has `CompactionTriggerMs = 250`; off has zero. The trigger is a fixed timer,
not a size or level policy. Each compaction merges ALL active tables into one;
with at most one active table it returns without work. These are existing engine
behaviors. No-op calls do not count as compactions that did work.

Each round shuffles the twelve cells with
`rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(12)`. Within each cell,
arm order is `permutations[(k+index)%6]`, with the same six permutations and
original cell index rule as `makePlan`. Seeds are `pairedSeed(master, base, k)`:
SHA-256 of unambiguous UTF-8 decimal fields `master|base|k`, first eight bytes
big-endian reinterpreted as int64. Seeds are shared across all three arms AND
across off/on, so those comparisons are paired. Driver command:
`xcampaign -design compaction`; plan, runner hash, clean-tree check, resume ledger,
one retry, 5% abort and hash-bound approval use the existing X driver protocol.

## 3. Confirmatory predictions

**H-C (primary).** In each of the six on cells, A-min and B are equivalent on
write throughput W and read throughput R. Test as in X-readcost-v1 H2: TOST on
the paired mean of ln(AMIN/B), margin +/- ln(1/0.90), 90% paired Student t CI.
Apply Holm across the six on cells within each outcome (one W family and one R
family). A cell is `equivalent` if Holm-adjusted TOST p < 0.05,
`not_equivalent` if its 90% CI lies wholly outside the margin, and `inconclusive`
otherwise. Report per cell, without pooling. Percentile bootstrap sensitivity:
10,000 paired resamples, seed 20261104, at the same CI level.

The prediction is that all twelve primary tests come out equivalent. Any other
outcome will be reported as found and would qualify the earlier leasing claim.
If cells are not equivalent, the paper must say so.

Secondary: apply the same test in the six off cells as a same-session reference.
These are separate Holm families per outcome, separate from the primary families.

## 4. Descriptive reporting and implementation disclosure

No directional predictions. Report within each arm ln(on/off) for W, R, read
p99 and commit-wait p99, with 95% paired t CIs. Report A/AMIN and A/B (also
AMIN/B for completeness) per cell, with 95% paired t CIs. Pair on/off by base,
round and arm, requiring both triplets to be complete. No cells are pooled.

Report each arm's compaction start exclusive hold and publication hold, B's
snapshotMu publication hold and wait, retirement lag, retired-table high-water
mark, compaction wall time, compaction count and input count, and final SSTable
count. Each timing distribution reports count, mean, p99 and max; input count
reports mean and max. Provide the trial-level values and 95% t CIs across trial
summaries; comparisons use 95% paired t CIs on defined positive log-ratios.
Structural absence (e.g. B has no Mu input-open section, or no compaction work)
is NA, not a measured zero duration. Histogram percentiles use the existing X
histogram bucket approximation. High-water marks and event counts retain zeros.

Disclosed before data: A and A-min open compaction input iterators while holding
Mu exclusively; B opens them outside all locks. This is an implementation
asymmetry, not inherent in leasing. It is measured here and not changed. A future
arm could remove it. A's readers hold Mu until their read completes, so retirement
waits for those readers; A-min leases and B snapshot pins can span retirement.

Retirement lag is per retired input table, from A-min's retired.Store(true), B's
MarkObsoleteLocked, or A's publication, until successful file removal. Failed
removals do not supply a lag observation. Instrumentation resets at window start
and includes the post-window drain, including shutdown cleanup, like other sync
statistics. Wall-time samples count successful CompactAll calls that did work;
wall time includes admission/serialization and synchronous cleanup, while B's
asynchronous cleanup is measured separately by retirement lag. Engine published
compaction counts include drain. The additional in-window count is successful
calls that did work and returned within the window; it is not a strict publication
timestamp count. No-op and error counts are distinguished; compaction_errors
excludes only engine-closed errors during stop. Final SSTable count is sampled
before shutdown, as in the existing harness.

## 5. Failures, exclusions and zero outcomes

Use X-readcost-v1 section 6: outcomes never fail a trial. Missed deadlines,
starvation and zero writes are data. Only engine/harness faults (ledger violation,
close/lease-drain error, validation error, crash or timeout) fail. Additionally,
a compaction error other than engine closure during stop is an engine fault.
Retry a failed trial once. After a second failure, drop the complete cell-round
triplet from paired tests and descriptive cell summaries; list every dropped
triplet and failed attempt, including the compaction error count. Successful
observations in dropped triplets remain visible in the trial-level output.

Abort if more than 5% fail after at least 40 attempted trials; report collected
data as found. No outcome-based exclusions. Use the existing X-readcost zero
rule: if any arm has W = 0 in a complete triplet, use W + 0.1 writes/s for that
unit's log comparisons and flag it. On/off W comparisons apply the adjustment to
both units when either has a zero. Other nonpositive/missing logarithms are
excluded only for the affected metric and explicitly counted. No pseudo-counts
are applied to lifecycle metrics. Missing trials require --allow-partial and are
marked partial; latest ledger attempts are authoritative, never stale artifacts.

## 6. Drift

No anchors. Random interleaving across rounds limits systematic order effects.
Report the round-trend of R per arm and cell descriptively, including round/R
values and slope of ln(R) against round where defined; no confirmatory drift test.

## 7. Host protocol

As X-readcost-v1 section 8: AC power; Windows power mode "Best performance";
no other user applications; Defender real-time protection left as found and
recorded. Record git revision, runner SHA-256, timer self-test, power scheme and
plan SHA-256 in platform.json. Real trials require a frozen clean tree and the
existing approval binding.

## 8. Analysis and deviations log

Analysis script: `research/experiments/analysis/x_compaction_analyze.py`.
CLI: `--data-dir --output-dir [--allow-partial]`. Outputs:
`x_compaction_results.json`, `x_compaction_tables.md`. Provenance includes plan
and ledger hashes, runner, revision, platform, failures, missing trials and
explicit dropped triplets. Statistical results are deterministic.

- Pre-freeze mechanical smoke (excluded from every analysis): on 2026-11-02, 15 trials ran from a dirty tree with an earlier runner build, to check mechanics: the first 9 plan rows through the driver (all compaction-off), then 6 compaction-on rows directly through the runner (C1_r04_d0000_on and C1_r16_d2000_on, round 1, three arms each). All 15 succeeded. The operator saw their per-trial values. No design value, prediction or margin was changed because of them. Output is kept in the git-ignored `X_READCOST/compaction_smoke` and `X_READCOST/compaction_smoke_on` folders. No other trial precedes the freeze.
- No other deviations are registered. This draft is to be frozen by a future
  commit before any real trial; preparing it makes no changes to paper.tex or paper/.
