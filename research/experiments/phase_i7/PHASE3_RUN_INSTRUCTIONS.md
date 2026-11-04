# Phase 3 run instructions

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
