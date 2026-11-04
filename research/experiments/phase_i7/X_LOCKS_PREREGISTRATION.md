# X-locks-v1 preregistration

Status: PRE-DATA. FROZEN once committed before any real trial. The engine's default lock, earlier
campaign statistics, margins and frozen earlier plans do not change. Earlier results stand as they
ran. This campaign is reported alongside them.

## 1. Purpose

The paper's core claim is admission coupling: with a continually waiting writer, CoarseLock reads
follow R = k r W, because Go's `RWMutex` blocks new readers once a writer waits and releases queued
readers on writer unlock. If that is the cause, the coupling should depend on the admission policy.
This campaign changes the policy and predicts what happens to q = R / (r W).

Go's lock behaves much like a phase-fair lock: at most one writer runs between reader phases. So the
phase-fair prediction is that nothing important changes, and the reader-preferring prediction is that
the coupling breaks. The hypotheses were generated from earlier data; these are new-data tests with
fresh seeds, not independent of how the hypotheses arose.

## 2. Design

Campaign ID `X-locks-v1`; default master seed 20261109; driver `-design locks`. Plan field `settle_ms`
= 20000. All other protocol follows the existing driver. Arm labels: A = CoarseLock, AMIN = Lease.
Snapshot is omitted, because Lease already holds the short read lock the question needs.

### Lock policies, spec field `lock_policy`

- `go`: `sync.RWMutex`, the engine default. Absent from the JSON when default.
- `phasefair`: writers wait for in-flight readers and block new readers. When a writer releases,
  every reader waiting at that moment is admitted before the next writer. Writers are served in
  arrival order. At most one writer runs between reader phases.
- `readerpref`: a new reader enters unless a writer currently holds the lock, even if writers are
  waiting. A writer enters only when no reader or writer holds it.

Both new policies are built from `sync.Mutex` and `sync.Cond`, so they carry overhead Go's lock does
not. This is disclosed, and it is why equivalence with Go is not predicted for CoarseLock.

### Cells

The specifications of first-campaign `X1_r{04,16}_d{0000,0020,0200,2000}`: a free-running writer,
1 MiB memtable, four initial tables, 2 s warmup, 10 s window, 200 ms cooldown, no compaction, WAL sync on,
sync instrumentation on, `diag_trace` off. Each crossed with the three policies: cell ID
`L1_r04_d0000_go` and so on. Eight settings, three policies, two arms, ten rounds: 480 trials.

### Order and seeds

An entry is one setting's six runs in one round. Each round k = 1..10 shuffles the eight entries with
`rand.New(rand.NewSource(int64(master + uint64(k)))).Perm(8)`. Within an entry the six runs are
shuffled with `rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(i))).Perm(6)`, with i the
entry's list index. Seed is `pairedSeed(master, entryID, k)`, shared across policies and arms. Trial
ID `<cell>_t%02d_<arm>`.

## 3. Definitions

q = R / (r W) per trial, in-window. Equivalence classes are as X-paced-v1 section 3, margin
+/- ln(1/0.90).

Writer starvation is a possible result here, so zero is pre-specified. A trial with zero in-window
commits is a valid observation, not a failure, in this campaign only. Its q counts as above any
bound, and its W counts as 0.1 writes/s in log ratios. The count of such trials is reported per cell.
A t CI is undefined when any trial in a setting has zero commits. H-L2 is then met in that setting if every finite q in it is above 2.10, and H-L1 is not met in that setting. This is the frozen rule.

## 4. Confirmatory predictions

**H-L1 (primary).** Under `phasefair`, CoarseLock's arithmetic mean q over rounds, with a 95% t CI,
lies wholly inside [1.90, 2.10] in all eight settings. Supported only if all eight hold.

**H-L2.** Under `readerpref`, CoarseLock's mean q has a 95% t CI wholly above 2.10 in all eight settings.
Supported only if all eight hold.

**H-L3.** Under `readerpref`, W_CL relative to `go` in the same round has a 95% paired log-t CI wholly
below 1 in all eight settings. Supported only if all eight hold.

**H-L4.** Lease write throughput is `equivalent` between `go` and `phasefair` in all eight settings
(one Holm family of eight), and between `go` and `readerpref` at d >= 200 us (one Holm family of four).
Lease does not hold the manager lock across lookups, so policy should matter little. Below 200 us
under `readerpref` is descriptive, because sixteen readers taking a short lock very often could still
delay a writer.

## 5. Descriptive reporting, no prediction

CoarseLock/Lease S and read ratios under each policy, with 95% CIs. W_CL and R_CL for `phasefair`
relative to `go`, with the overhead caveat. q under Lease per policy. First-acquisition commit wait.
Zero-commit counts. Read and write p50 and p99.

## 6. Failures, drift, host protocol

Failure and missing-data rules are as X-paced-v1 section 5, except for the zero-commit rule above.
Failed attempts are retried once; a second failure drops that complete entry-round unit from paired
tests and it is listed. No outcome-based exclusions. AC power; no other user applications; Windows power mode and Defender as found and recorded. The operator and the assistant leave the machine alone
during the run, and any interaction or background process is recorded as a deviation.

Limits: one host, one writer, one Go runtime. The new locks are simple implementations, not tuned ones,
and they do not reproduce published phase-fair algorithms. The manager lock is now called through an interface in every design and policy, which adds a dynamic call that the earlier campaigns did not have; the `go` cells of this campaign include it. The first-campaign specifications do not
pause in their originals; this campaign does, so comparison with first-campaign values is descriptive.

## 7. Analysis and deviations log

Analysis: `research/experiments/analysis/x_locks_analyze.py`, CLI `--data-dir --output-dir
[--allow-partial]`. Deterministic outputs `x_locks_results.json` and `x_locks_tables.md` with
provenance, plan and ledger hashes, runner, revision, platform, exclusions, failures, missing units and
every prediction above. Bootstrap sensitivity uses 10000 paired resamples, seed 20261109.

- Mechanical smoke, excluded from every analysis: to be run after the freeze, first trials of the plan
  only, reading status, cleanup and gaps, not outcomes. Recorded here if run.
- Plan SHA-256 (indented JSON bytes without trailing newline): `3323c32d053be80ca92b543509f474f1e2107e24e093b3e77c64fdd353c89159`
- Unit tests, synthetic fixtures and the driver dry-run are mechanical verification, not campaign
  observations.
