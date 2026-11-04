# X read-cost campaign driver

`xcampaign` owns the fixed X-readcost-v1 design. It never invokes `run_i7.ps1`.
Build the runner with `go build -o i7trial.exe ./research/experiments/phase_i7/cmd/i7trial`.

Generate the plan and platform preflight without executing trials:

```powershell
go run ./research/experiments/phase_i7/cmd/xcampaign -runner .\i7trial.exe -out C:\scratch\xcampaign -dry-run
```

A dirty source tree requires `-allow-dirty`; the override and full git status are
recorded in `platform.json`. Preflight records HEAD, executable and plan hashes,
runner selftest JSON, active Windows power scheme and available disk space on
both output and data volumes (minimum 5 GB each). Linux records the power scheme
as unavailable; Windows requires `powercfg` to succeed. Platform probes run on
every invocation, including dry-run and resume. Dry-run writes the immutable
plan plus platform files and creates the scratch root, but launches no trials.

Execution requires `-approve approval.json` with the two printed hashes:

```json
{
  "campaign_id": "X-readcost-v1",
  "approved": true,
  "runner_sha256": "<printed runner_sha256>",
  "plan_sha256": "<printed plan_sha256>"
}
```

Approval is checked before execution preflight. The runner independently checks
the binary binding and the hash of `plan.json` beside the `specs/` directory.
Gate X accepts X1/X2/X3 and ANCHOR (mapped to X1); existing I7 gates retain their
checks. The driver emits anchors as block X1, cell ANCHOR, at triplets 0, 5 and
10. They use the X1_r04_d0000 treatment and all share the request seed
`pairedSeed(master_seed, "ANCHOR", 0)`, so drift is measured on the same stream.
Their trial IDs, milestone triplets and arm permutations remain distinct.

The fixed design has 17 cells, ten triplets per cell and nine anchor runs: 519
processes before retries. Round k randomizes cell order with a PRNG seeded by
`int64(master_seed + uint64(k))`. Within each triplet the arm order
is `permutations[(k + zero_based_cell_index) % 6]`. Request seeds use the first
eight SHA-256 bytes of `decimal(master)|cell_id|decimal(triplet)`, big-endian,
reinterpreted as int64 for the harness. The request seed encoding is persisted in the
plan; paths, timestamps and platform probes do not affect its hash.

All X specs require **wal_sync=true** (a missing/false bool fails validation).
Fixture WAL sync is disabled; warmup, pre-freeze and the measured window honor
wal_sync. Read latency uses fixed histograms with 128 subdivisions per power of
two (less than 0.8% bucket width) over 1 ns through 120 s. Zero durations remain
exact; larger values retain exact min/max/mean and saturate percentile buckets.
X sync observations are reset after fixture, pre-freeze and warmup, immediately
before the measurement window, and include post-window drain. Older blocks keep
their existing whole-trial sync scope. E/D recorders and their
sample sufficiency rules are unchanged. X records no per-read issue intervals.

`-limit N` selects the first N unfinished *trial identities*, with up to two
attempts per identity. A crash, timeout or non-SUCCESS output retries once, and
an exhausted second attempt is terminal on resume. Attempts use separate scratch
directories and output names. Interrupted STARTED records consume an attempt.
The append-only ledger syncs each record before proceeding; a partial append can
be recovered without discarding preceding records. Successful trials and
exhausted identities are skipped. Plan or runner changes refuse resume. A final
failure rate greater than 5% after 40 attempted identities stops the campaign
resumably (exactly 5% continues). `progress.json` and stdout report final trial
outcomes; process stderr/stdout are saved as attempt-specific `.log` files.

Defaults: master seed 20261030, scratch root `<out>/data`, timeout 120s. Execution
is sequential. Ctrl+C cancels the child process and records the interruption.
The full dry-run estimate is 519 x 14s = 2h1m6s. A three-trial smoke invocation
uses `-limit 3`; it consumes the first anchor triplet.

## SSTable-count design

`-design tablecount` selects `X-tablecount-v1`, with master seed 20261105 when
`-master-seed` is unset. It has 16 cells (4/16/64/256 initial tables, 4/16 readers,
0/1 writers), ten rounds and three arms: 480 trials with no anchors. It uses X1,
10240 keys split into disjoint initial ranges, no compaction, and the existing
gate X approval protocol. See [the preregistration](../../X_TABLECOUNT_PREREGISTRATION.md)
for predictions, analysis and freeze requirements. Generate it without trials:

```powershell
go run ./research/experiments/phase_i7/cmd/xcampaign -design tablecount -runner .\i7trial.exe -out C:\scratch\tablecount -dry-run -allow-dirty
```

Analysis: `research/experiments/analysis/x_tablecount_analyze.py --data-dir <campaign>
--output-dir <analysis> [--allow-partial]`. Optional `lifecycle_bench.txt` contains
separate Go microbenchmark output; one-iteration rows are compile checks only.

`-design followup` selects **X-followup-v1**, default master seed 20261108:
six X4 cells with AMIN/B and one X1 mixed cell with A/AMIN/B, ten rounds,
150 trials, and a 20-second pause before every attempt (budget about 85 minutes).
F1 varies paced writes with a 16 MiB memtable; F2 retains 1 MiB; F3 disables
WAL sync under `diag_trace`; F4 persists the existing measured-read histogram
with `persist_read_histogram`. This flag is X-only and adds no read-path work;
false omits both new JSON fields. See the [preregistration](../../X_FOLLOWUP_PREREGISTRATION.md)
and [run instructions](../../PHASE3_RUN_INSTRUCTIONS.md) for preflight, approval,
separate smoke, full execution and analysis. Gate X permits X1/X4 for this design.

`-design locks` selects **X-locks-v1**, default master seed 20261109: eight
first-campaign X1 block-read settings (4/16 readers, 0/20/200/2000 us) crossed
with go/phasefair/readerpref, A/AMIN only, ten rounds: 24 cells and 480 trials.
Each round shuffles eight entries; each entry shuffles its six runs and shares
one request seed across policies and arms. Go's default omits `lock_policy`.
A 20-second pause precedes every attempt; budget about 4.5 hours. Gate X permits
X1 only. Zero commits remain observations; only zero W becomes 0.1 writes/s in
log ratios. See the [preregistration](../../X_LOCKS_PREREGISTRATION.md) and
[run instructions](../../PHASE3_RUN_INSTRUCTIONS.md) for the frozen zero-commit
q decision rule, interface-call overhead, approval and host protocol. Analysis is
`research/experiments/analysis/x_locks_analyze.py --data-dir <campaign>
--output-dir <analysis> [--allow-partial]`, producing JSON, Markdown and q/S
figures. No trial or smoke was run during implementation.

`-design inputopen` selects **X-inputopen-v1**, default master seed 20261110:
six X-compaction on settings, A/AMIN crossed with default/aligned input opening
plus B under default, ten rounds and 300 trials. Each five-run entry-round shares
one seed, with the registered independent entry/run permutations and a 20-second
pause before every attempt. It retains X3's merge-all 250 ms timer. Default specs
omit `compaction_open_outside_lock`; aligned specs set it true. The runner's X
approval gate permits only X3 for this campaign. Budget about three hours. Use
`-dry-run` to prepare without trials; follow
[PHASE3_RUN_INSTRUCTIONS.md](../../PHASE3_RUN_INSTRUCTIONS.md#x-inputopen-v1)
for freeze, hash-bound approval, execution and deterministic analysis. No trial
or smoke was run during implementation; earlier frozen plans remain unchanged.


`-design realism` selects **X-realism-v1**, default master seed 20261111:
eight X1 cells (4/16 readers; direct reads with 1/2/4 writers, buffered controls
with one writer), three arms, ten rounds, 240 trials, 20-second settling pauses;
budget about 2.3 hours. The single harness seed uses D_r04/D_r16 per round across
writer counts/paths. Gate X permits X1 only. Direct readers sync the SSTable,
open NO_BUFFERING/O_DIRECT handles, pool aligned 4096-byte buffers and fail at
open/probe without fallback. The amended protocol permits conflicts: the
existing stride partitions indices, while hashed keys can collide across writers.
Aborts are counted per trial and retained; W counts successful commits only.
H-X reports per-cell abort shares and flags strictly more than 1% for disclosure
beside the cell results. Native Linux replication is optional; not running it is
not a deviation. See the [protocol](../../X_REALISM_PREREGISTRATION.md)
and [run instructions](../../PHASE3_RUN_INSTRUCTIONS.md#x-realism-v1), including
native Linux ext4/xfs replication, Linux kernel/filesystem/device recording,
separate Linux runner approval, WSL2 rehearsal-only status and deterministic
`x_realism_analyze.py --replication-dir` side-by-side reporting without pooling.
No trial, smoke or commit was performed during implementation.
