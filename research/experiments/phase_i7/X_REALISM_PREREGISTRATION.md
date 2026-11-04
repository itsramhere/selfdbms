# X-realism-v1 preregistration

Status: PRE-DATA. FROZEN once committed before any real trial. The engine's default path, earlier
campaign statistics, margins and frozen earlier plans do not change. Earlier results stand as they
ran. This campaign is reported alongside them.

## 1. Purpose

Every earlier campaign controls read cost with injected delay over a dataset likely resident in the OS
page cache, and has at most one writer. This campaign asks whether the main results survive real
device reads and more than one writer. It replaces injected delay with direct, unbuffered reads, and it
varies writer count. Direct reads make the small dataset hit the device on every block read, so a dataset larger
than RAM is not needed for this question and is not attempted. Each trial builds its own fixture, which
rules out a larger-than-RAM fixture at this trial count.

The hypotheses arose from earlier data; these are new-data tests with fresh seeds.

## 2. Design

Campaign ID `X-realism-v1`; default master seed 20261111; driver `-design realism`. Plan field `settle_ms`
= 20000. All other protocol follows the existing driver. Arm labels: A = CoarseLock, AMIN = Lease, B =
Snapshot.

### The treatment, spec field `read_path` = `direct`

Point-read data blocks are read with `FILE_FLAG_NO_BUFFERING` on Windows and `O_DIRECT` on Linux, with
aligned offsets, lengths and buffers. This applies to every block read, including writer conflict
validation. The SSTable is synced before a direct reader opens it, so the device holds what the cached
writer wrote. Open-time metadata, compaction and the default `block` path are unchanged. Injected delay is 0.

### Cells

Mixed-shaped as first-campaign `X1_r{04,16}_d0000`: free-running writers, 1 MiB memtable, four initial
tables, 2 s warmup, 10 s window, 200 ms cooldown, no compaction, WAL sync on, sync instrumentation on,
`diag_trace` off, closed readers. Writers use the harness's existing per-writer stride. It separates request indices, not keys, so two writers can pick the same key and a write-write conflict abort is possible, though rare over 10000 keys. W counts successful commits only.

| Cells | Readers | Writers | Read path |
| --- | --- | --- | --- |
| `D1_r04_w1_direct`, `D1_r16_w1_direct` | 4, 16 | 1 | direct |
| `D1_r04_w2_direct`, `D1_r16_w2_direct` | 4, 16 | 2 | direct |
| `D1_r04_w4_direct`, `D1_r16_w4_direct` | 4, 16 | 4 | direct |
| `D2_r04_w1_buffered`, `D2_r16_w1_buffered` | 4, 16 | 1 | block |

Eight cells, three arms, ten rounds: 240 trials.

### Order and seeds

Each round k = 1..10 shuffles the eight cells with `rand.New(rand.NewSource(int64(master +
uint64(k)))).Perm(8)`. Within a cell, arms follow `permutations[(k+i)%6]`, with i the cell's list index.
Seed is `pairedSeed(master, cellID, k)`, shared across arms, plus `pairedSeed(master, "D_" + reader setting,
k)` shared across writer counts and paths of one reader setting, where that lets the writer-count and path
comparisons be paired. If the harness has one seed field, the second form is the one used.

### Linux replication

If a native Linux host with ext4 or xfs becomes available, the same plan, plan hash and predictions run again on it, in a separate output directory, as a replication. Not running it is not a deviation. WSL2 is a rehearsal only, because its disk is virtual, and it counts
for nothing. The platform file records the kernel, file system and device. A replication is reported beside the
Windows result and never pooled with it.

## 3. Definitions

q = R / (r W), with W all writers' in-window commits per second. Hold is CoarseLock's trial mean read
critical section. S = W_A / W_AMIN. Equivalence classes are as X-paced-v1 section 3, margin +/- ln(1/0.90).

## 4. Confirmatory predictions

**H-D1 (primary).** Real device reads produce the same pattern as injected delay. In both `D1_r*_w1_direct`
cells, S has a 95% paired log-t CI wholly below 1, and CoarseLock's mean q has a 95% t CI wholly inside [1.90,
2.10]. Supported only if all four hold.

**H-D2.** Admission coupling holds with several writers: in the four `D1_r*_w{2,4}_direct` cells,
CoarseLock's mean q has a 95% t CI wholly inside [1.80, 2.20]. Every commit still takes two exclusive
acquisitions, and each still admits queued readers. The band is wider than H-D1's because writers can now follow each other. Supported only if all four hold.

**H-D3.** R_AMIN/R_B and W_AMIN/W_B are `equivalent` in all six `D1` cells, with separate Holm families
of six.

**H-D4.** With fsync under the exclusive lock, more writers do not add commit throughput. For Lease,
W_w4/W_w1 and W_w2/W_w1 are `equivalent` in both reader settings, one Holm family of four, paired by round.

**Protocol check H-X.** Conflict aborts are counted per trial and reported. An abort is not a failure, and the trial is kept. If more than 1% of attempted commits in a cell abort, the paper says so beside that cell's results.

## 5. Descriptive reporting, no prediction

Hold under direct and buffered reads, against the earlier doses, labelled as across-campaign. Read p50, p95
and p99 by cell. S and read ratios at w = 2 and 4. Direct/buffered ratios of R and W per arm. Commit wait. Flush
counts. The Linux results beside the Windows ones when they exist.

## 6. Failures, drift, host protocol

Failure, zero and missing-data rules are as X-paced-v1 section 5. Failed attempts are retried once; a
second failure drops that complete cell-round unit from paired tests and it is listed. No outcome-based
exclusions. AC power; no other user applications; Windows power mode and Defender as found and recorded.
The operator and the assistant leave the machine alone during the run, and any interaction or background
process is recorded as a deviation.

Limits: a small dataset and uniform reads, so device behaviour under direct reads is not a production cache
model. No block cache exists, so every block read goes to the device. One laptop SSD on Windows, unless the replication runs. Bursts near 31.5 ms are uncontrolled. Group commit is absent here; X-commitpath-v1 tests it.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_realism_analyze.py`, CLI `--data-dir --output-dir
[--allow-partial] [--replication-dir]`. Deterministic outputs `x_realism_results.json` and
`x_realism_tables.md` with provenance, plan and ledger hashes, runner, revision, platform, exclusions,
failures, missing units and every prediction above. Bootstrap sensitivity uses 10000 paired resamples, seed
20261111.

- Mechanical smoke, excluded from every analysis: to be run after the freeze, first trials of the plan
  only, reading status, cleanup and gaps, not outcomes. Recorded here if run.
- Plan SHA-256 (indented JSON bytes without trailing newline): `e41320390c819d25fe74132d242e555e5831e063f71cba2d51eae0fc1d34681a`
- Unit tests, synthetic fixtures, a WSL2 rehearsal and the driver dry-run are mechanical verification, not
  campaign observations.
