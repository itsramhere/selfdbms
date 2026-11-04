# X-memtable-v1 preregistration

Status: PRE-DATA. FROZEN once committed before any real trial. No engine behaviour, earlier
campaign statistic or frozen earlier plan changes. Earlier results stand as they ran.

## 1. Purpose

X-followup-v1 rejected its primary prediction. Across write rates, the slope of log R on log free
share was 0.768 and 0.779, below the band [0.80, 1.25]. Within a write rate, an exploratory pooled
slope was about 1.14. Reads per unit of free time rose with write rate, as did the share of reads
served from the memtable (about 0.08 at 183 writes/s and 0.29 at 732).

The explanation under test: a read served from the memtable skips SSTable reads, so a higher write
rate makes the average read cheaper. Reads then equal a capacity set by memtable share, times free
share. The rival: fsync costs reads something beyond its hold at low write rates.

Both ideas came from X-followup-v1 data. This campaign tests them on new data with fresh seeds, by
changing memtable share without changing the write rate, the commit stream or the memtable's size.

## 2. Design

Campaign ID `X-memtable-v1`; default master seed 20261112; driver `-design memtable`. Plan field
`settle_ms` = 20000. All other protocol follows the existing driver: plan persistence, runner hash,
clean-tree check, resume ledger, one retry, hash-bound gate X approval, 5% abort after 40 attempts.

All cells use `F1_w0183_m16` of X-followup-v1 except for three factors: r = 16, one paced writer,
closed readers, d = 0, keyspace 10000, four initial tables of 2500 keys, hit ratio 0.95, uniform
reads, 16 MiB memtable, 2 s warmup, 10 s window, 200 ms cooldown, no compaction, `diag_trace` on.

| Factor | Levels |
| --- | --- |
| Write rate | 183, 366, 549, 732 writes/s |
| Write key domain | `same`: writes use read keys, as in X-followup-v1. `shadow`: the same key with the suffix `x`, which sorts next to the read key and is never read |
| WAL sync | `on`, `off` |

Sixteen cells, ID `M_w<rate>_<domain>_<sync>`. One arm, B (Snapshot), ten rounds: 160 trials.

One arm because the mechanism under test concerns read cost, and Lease and Snapshot reads were
within about 2% at these rates. Snapshot has no per-table lease cost to separate out. Results apply
to Snapshot; extending them to Lease rests on X-followup-v1's read ratios, not on this campaign.

Each round k = 1..10 shuffles the cells with `rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(16)`.
Seed is `pairedSeed(master, cellID, k)`. Trial ID `<cell>_t%02d_B`.

## 3. Definitions

Hold share and free share as X-followup-v1 section 3. Memtable share per trial:
`measured_memtable_point_reads` / `read_ops_completed`. The memtable is empty during warmup, so this
counts window reads. Capacity per round, rate and domain: C = R_off / free_off, from that round's
sync-off cell. Fit ratio per round, rate and domain: Q = R_on / (C x free_on). The equivalence classes
are as X-paced-v1 section 3, with margin +/- ln(1/0.90).

## 4. Confirmatory predictions

**H-M1 (primary).** Per round, take the OLS slope of log R on log free share across the four rates,
in the sync-on cells of a domain. (a) For `shadow`, the mean slope over rounds has a 90% t CI wholly
inside [0.80, 1.25]. (b) The mean per-round difference, `shadow` minus `same`, has a 90% t CI wholly
above 0. Supported only if both hold.

**H-M2.** In the `same` sync-off cells, reads rise with write rate. (a) One-sided Page's L, ten
rounds as blocks, cells ordered 183, 366, 549, 732, predicted ranks 1, 2, 3, 4, alpha 0.05,
`page_trend_test(method='auto')`. (b) R at 183 over R at 732, paired by round, has a 90% t CI on the
log ratio wholly below ln(0.90). Supported only if both hold. The rival predicts a ratio near 1. The
straight-line fit to X-followup-v1 gives about 0.77; this is a guide and not the test.

**H-M3.** In the `shadow` sync-off cells, reads do not depend on write rate. R at 183 over R at 732,
paired by round, is `equivalent`.

**H-M4.** Hold share plus matched capacity accounts for reads. Q is `equivalent` to 1 at all eight
combinations of rate and domain. One Holm family of eight. Supported only if all eight are
`equivalent`. Each is also reported on its own.

**Protocol check H-X.** (a) No trial has a measured flush. (b) Mean memtable share is under 0.001 in
every `shadow` cell. (c) In `same` cells, memtable share rises with rate. If (b) or (c) fails the
manipulation did not work and H-M1 to H-M3 are not interpreted. A flush is reported and the trial is
kept.

## 5. Use in the paper

The paper states only what these tests support.

- The memtable explanation is claimed only if H-M1, H-M2, H-M3 and H-X all hold.
- The statement that reads equal capacity times free share is claimed only if H-M4 also holds, or for
  the rates and domains where it holds, named as such.
- If they do not hold, the paper reports X-followup-v1's failed prediction as a failed prediction,
  states the monotone trend it did support, and proposes no mechanism for the slope.

Descriptive, no prediction: R, W, hold and free share, capacity and memtable share per cell; Q per
cell; slope for `same` beside X-followup-v1's values; pooled within-rate slope per domain; `shadow`
over `same` capacity at each rate; hold per commit and its phases per cell; largest fsync and count at
or above 25 ms per sync-on cell. Spread of log R per cell. A figure of capacity against memtable share.

## 6. Failures, drift, host protocol

Failure, zero and missing-data rules are as X-paced-v1 section 5. Failed attempts are retried once;
a second failure drops that complete round from the tests that need it and it is listed. No
outcome-based exclusions. AC power; no other user applications; no Codex, ChatGPT or WSL VM process at
the start; Windows power mode and Defender as found and recorded. Record revision, runner SHA-256,
timer self-test, power scheme and plan SHA-256 in platform.json. Any interaction or background process
during the run is recorded as a deviation.

Limits: one host, one writer, one arm, injected-delay engine with d = 0, Windows page cache. The shadow
key is one byte longer. Sync-off capacity excludes any cost that fsync imposes on reads apart from its
hold; H-M4 is what would show such a cost.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_memtable_analyze.py`, CLI `--data-dir --output-dir
[--allow-partial]`. Deterministic outputs `x_memtable_results.json` and `x_memtable_tables.md` with
provenance, plan and ledger hashes, runner, revision, platform, exclusions, failures, missing units and
every prediction above. Bootstrap sensitivity uses 10000 paired resamples, seed 20261112.

- Mechanical smoke, excluded from every analysis: to be run after the freeze, first trials of the plan
  only, reading status, trace presence, cleanup and gaps, not outcomes. Recorded here if run.
- Plan SHA-256 (indented JSON bytes without trailing newline): `8560df67fb5fae4f70f9a93ed8451e5ffa70983b049a3cfe318efd4fa8f5cad9`.
- Unit tests, synthetic fixtures and the driver dry-run are mechanical verification, not campaign
  observations.
