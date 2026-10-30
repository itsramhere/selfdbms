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
