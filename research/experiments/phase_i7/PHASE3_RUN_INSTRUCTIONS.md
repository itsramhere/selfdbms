# Phase 3 run instructions

## X-locks-v1

Protocol: [X_LOCKS_PREREGISTRATION.md](X_LOCKS_PREREGISTRATION.md).
Run from the repository root in PowerShell. Preparation and dry-run authorize
no trial. No trial or smoke was run during implementation. Execution requires
the frozen protocol, a clean worktree and separate hash-bound operator approval.
The assistant must not commit the freeze on the operator's behalf.

Connect AC power, close other user applications, and record Windows power mode
and Defender protection as found; leave both unchanged. Record any interaction
or background process as a deviation. The operator and assistant leave the host
alone during execution. The driver records revision, runner/plan hashes, timer
self-test, power scheme and disk space. At least 5 GB must be free on output and
scratch volumes. Budget **about 4.5 hours** for 480 trials plus retries: nominal
480 x (12.5 s trial + 20 s pause) = 4 h 20 min, before other process/fixture costs.

Build and generate the plan without trials:

```powershell
$env:GODEBUG = 'goindex=0'
$locksRunner = Join-Path $PWD 'i7trial.exe'
$locksCampaign = 'C:\scratch\x-locks-v1'
$locksSmoke = 'C:\scratch\x-locks-v1-smoke'
go build -o $locksRunner ./research/experiments/phase_i7/cmd/i7trial
go run ./research/experiments/phase_i7/cmd/xcampaign -design locks -runner $locksRunner -out $locksCampaign -dry-run
```

During preparation only, a dirty-tree dry-run may append `-allow-dirty`; this
override is recorded. Expect master seed 20261109, 8 entries, 24 cells, 480
trials, block X1 only, and `settle_ms=20000`. Verify the plan SHA-256 against
section 7 of the protocol. Plan hashing covers indented JSON bytes without a
trailing newline. Rebuilding changes the runner hash; repeat preflight and
obtain matching approval. Do not use `-allow-dirty` for execution.

After operator approval, create `approval-locks.json` using the printed hashes:

```json
{
  "campaign_id": "X-locks-v1",
  "approved": true,
  "runner_sha256": "<printed runner_sha256>",
  "plan_sha256": "<printed plan_sha256>"
}
```

If a mechanical smoke is separately approved after the freeze, use a separate
directory and only the first six identities (one setting's policy/arm entry):

```powershell
go run ./research/experiments/phase_i7/cmd/xcampaign -design locks -runner $locksRunner -out $locksSmoke -dry-run
go run ./research/experiments/phase_i7/cmd/xcampaign -design locks -runner $locksRunner -out $locksSmoke -approve .\approval-locks.json -limit 6
```

Inspect status, cleanup and inter-attempt gaps only; do not inspect outcomes.
Record any smoke in the protocol deviations log and exclude its directory from
every analysis. Every attempt, including retries, has a 20-second pause. Each
entry-round has go/phasefair/readerpref crossed with A/AMIN, with one request
seed shared by all six. No B arm or anchors are present. Go's default policy
omits `lock_policy` in JSON. All three policies record write service histograms
without enabling `diag_trace`; latency samples include only successful
in-window completions, and late-write latency is separate.

Execute all 480 fresh identities in the full directory, then analyze; repeat
the execution command to resume. Never copy smoke artifacts into this directory.

```powershell
go run ./research/experiments/phase_i7/cmd/xcampaign -design locks -runner $locksRunner -out $locksCampaign -approve .\approval-locks.json
python research/experiments/analysis/x_locks_analyze.py --data-dir $locksCampaign --output-dir C:\scratch\x-locks-v1-analysis
```

Analysis requires NumPy, SciPy and Matplotlib. Outputs are deterministic
`x_locks_results.json`, `x_locks_tables.md`, `x_locks_q.png` and `x_locks_s.png`.
Use `--allow-partial` only to label incomplete data. Failed attempts retry once;
an exhausted failure drops the entire six-run entry-round from paired tests and
cell summaries. Successful observations in dropped units remain in trial output.
The existing driver abort rule (>5% final failures after 40 attempted identities)
and append-only resume ledger apply.

Zero in-window commits are valid in this campaign. Raw W remains zero; only zero
W becomes 0.1 writes/s in log ratios, while positive paired W is unchanged.
Zero-commit q is explicitly above every bound, never excluded or replaced with
a finite value. The frozen section 3 amendment leaves the t CI undefined when
any included trial has zero commits: H-L1 is not met, and H-L2 is met exactly
when every finite q in the setting is strictly above 2.10, including the case
where all included trials have zero commits. H-L1/H-L2 always give decisions;
insufficient CI evidence or partial data cannot establish campaign support.
Every policy, including `go`, calls the manager lock through an interface;
earlier campaigns did not include that dynamic-call overhead.
Read/write p50 and p99 are undefined when no corresponding completion exists;
p99 also requires the existing minimum sample count. Commit lock-site wait is
the first acquisition only and includes post-window drain, as in earlier X.

## X-followup-v1

Protocol: [X_FOLLOWUP_PREREGISTRATION.md](X_FOLLOWUP_PREREGISTRATION.md).
Run commands from the repository root in PowerShell. Preparation and dry-run
authorize no trial. Execute the smoke and full campaign only after the protocol
is frozen and the operator has provided hash-bound approval. No trial or smoke
was run while implementing this campaign.

Connect AC power, close other user applications, and record Windows power mode
and Defender real-time protection as found. Leave both unchanged. Record these
host conditions and any interaction/background process as deviations in the run
record. The operator and assistant leave the machine alone during execution.
The driver records revision, runner hash, plan hash, timer self-test and active
power scheme in `platform.json`. At least 5 GB must be free on output and scratch
volumes. Use the frozen clean worktree for execution; do not use `-allow-dirty`
for real trials. Budget **about 85 minutes** for 150 trials and 20-second pauses,
plus retries if needed (nominal 150 × 32.5 seconds = 81.25 minutes).

Build the runner and generate the plan without trials:

```powershell
$env:GODEBUG = 'goindex=0'
$runner = Join-Path $PWD 'i7trial.exe'
$campaign = 'C:\scratch\x-followup-v1'
$smoke = 'C:\scratch\x-followup-v1-smoke'
go build -o $runner ./research/experiments/phase_i7/cmd/i7trial
go run ./research/experiments/phase_i7/cmd/xcampaign -design followup -runner $runner -out $campaign -dry-run
```

During preparation only, a dirty-tree dry-run can append `-allow-dirty`; that
override and full status are recorded. The output must show seven cells, 150
trials, `settle_ms=20000`, and the runner/plan SHA-256 values. Verify the plan hash
against the protocol and record it in the campaign run record too.
Rebuilding changes the runner hash; repeat preflight and obtain matching approval.

Create `approval-followup.json` from the printed hashes after operator approval:

```json
{
  "campaign_id": "X-followup-v1",
  "approved": true,
  "runner_sha256": "<printed runner_sha256>",
  "plan_sha256": "<printed plan_sha256>"
}
```

Mechanical smoke uses a separate directory and only the first seven trial
identities. Its plan and runner hashes must match the approved full plan:

```powershell
go run ./research/experiments/phase_i7/cmd/xcampaign -design followup -runner $runner -out $smoke -dry-run
go run ./research/experiments/phase_i7/cmd/xcampaign -design followup -runner $runner -out $smoke -approve .\approval-followup.json -limit 7
```

Inspect only status, expected trace/histogram presence, scratch cleanup, and
inter-attempt gaps. Do not inspect outcomes. Record the smoke in the deviations
log and exclude its directory from every analysis. The mixed cell has a read
histogram and no `diag_trace`; the paced cells have diagnostics and no persisted
read histogram. A seven-trial limit can end mid-unit; it is not seven cells.

Execute all 150 fresh identities in the full directory; repeat the same command
to resume after interruption. The append-only ledger handles one retry and
skips successful/exhausted identities. Do not copy smoke artifacts into it.

```powershell
go run ./research/experiments/phase_i7/cmd/xcampaign -design followup -runner $runner -out $campaign -approve .\approval-followup.json
python research/experiments/analysis/x_followup_analyze.py --data-dir $campaign --output-dir C:\scratch\x-followup-v1-analysis
```

Analysis requires NumPy, SciPy and Matplotlib. Use `--allow-partial` only to
report incomplete data explicitly. Outputs are `x_followup_results.json`,
`x_followup_tables.md`, `x_followup_f4_cdf.png` and
`x_followup_reads_free_share.png`. No analysis command launches trials.

The frozen H-F4 rule assigns each histogram bucket to the region containing its
`lower_ns`, including buckets spanning 200 µs or 500 µs. Boundary buckets are
listed descriptively and do not suspend the H-F4 decision. Hold/free shares and
fsync-duration diagnostics are reported for paced cells only: F4 has no
`diag_trace` and therefore no phase data.
