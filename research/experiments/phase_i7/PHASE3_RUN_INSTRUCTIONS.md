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

## X-inputopen-v1

Protocol: [X_INPUTOPEN_PREREGISTRATION.md](X_INPUTOPEN_PREREGISTRATION.md).
Run from the repository root in PowerShell. Implementation verification ran only
unit tests, synthetic analysis fixtures and a driver dry-run. No campaign trial
or smoke was run. The operator freezes the reviewed protocol before real data;
the assistant does not commit it. Budget **about 3 hours**: 300 trials x
(12.5 seconds + 20-second settling pause) = 2 h 42 min 30 s before fixture,
process and retry costs. The pause applies before every attempt, including retries.

Connect AC power, close other user applications, record Windows power mode and
Defender protection as found, and leave both unchanged. The operator and assistant
leave the machine alone during execution; record interactions and background
processes as deviations. At least 5 GB must be free on both output and scratch
volumes. The existing preflight records revision, runner and plan hashes, timer
self-test, power scheme and disk space.

Build and prepare without launching trials:

```powershell
$env:GODEBUG = 'goindex=0'
$inputopenRunner = Join-Path $PWD 'i7trial.exe'
$inputopenCampaign = 'C:\scratch\x-inputopen-v1'
go build -o $inputopenRunner ./research/experiments/phase_i7/cmd/i7trial
go run ./research/experiments/phase_i7/cmd/xcampaign -design inputopen -runner $inputopenRunner -out $inputopenCampaign -dry-run
```

During preparation only, append `-allow-dirty` if needed; do not use it for
execution. Expect default master seed 20261110, six settings, twelve path cells,
300 trials, block X3 only, `settle_ms=20000`, and a merge-all 250 ms compaction
timer. Each setting-round has A default/aligned, AMIN default/aligned, and B
under default only. All five share `pairedSeed(master, entryID, round)`; both
entry order and run order are shuffled as specified. No anchors are present.
`compaction_open_outside_lock` is absent for default cells and true for aligned
cells. B already opens outside locks. Rebuilding changes the runner hash.

Verify the indented `plan.json` bytes, with no trailing newline, against the hash
in protocol section 7. After the operator freezes the protocol and approves the
printed hashes, create `approval-inputopen.json`:

```json
{
  "campaign_id": "X-inputopen-v1",
  "approved": true,
  "runner_sha256": "<printed runner_sha256>",
  "plan_sha256": "<printed plan_sha256>"
}
```

The runner's X gate accepts this campaign only for X3. The driver and runner both
check the hash binding. No smoke is authorized by these implementation instructions.
Any later separately approved smoke must use its own directory, only the first
five plan identities, inspect status/cleanup/gaps without inspecting outcomes,
and be logged in the protocol and excluded from every analysis.

After freeze and operator approval, execute all 300 fresh identities, then analyze:

```powershell
$env:GODEBUG = 'goindex=0'
go run ./research/experiments/phase_i7/cmd/xcampaign -design inputopen -runner $inputopenRunner -out $inputopenCampaign -approve .\approval-inputopen.json
python research/experiments/analysis/x_inputopen_analyze.py --data-dir $inputopenCampaign --output-dir C:\scratch\x-inputopen-v1-analysis
```

Repeat the execution command to resume; never copy smoke artifacts into the full
campaign directory. Failed attempts retry once; a second failure drops the whole
five-run setting-round from paired tests and cell summaries, with successful
observations retained in trial output. The existing abort rule (>5% final
failures after 40 attempted identities) applies. Zero throughput is an outcome.
For log W ratios only, add 0.1 writes/s to all five runs of a zero-containing
complete unit, following X-paced; retain raw zeros in summaries. Missing and
nonpositive latency log pairs are listed without pseudocounts.

Analysis requires NumPy, SciPy and Matplotlib. Deterministic outputs are
`x_inputopen_results.json`, `x_inputopen_tables.md` and the single figure
`x_inputopen_commit_wait_p99.png`. `--allow-partial` explicitly labels incomplete
data. H-O1 uses paired 95% log-t intervals; H-O2 uses 90% intervals and Holm TOST
at +/-ln(1.5). H-O3/H-O4 each have separate six-setting W/R families at
+/-ln(1/0.90). Bootstrap sensitivity uses 10000 paired resamples, seed 20261110.
The analysis reports all hypotheses, CoarseLock path ratios, Lease default/B
against the earlier 1.43-7.69 range as across-campaign descriptive context,
start hold, compaction count, retirement lag/high water, and commit-wait p50/max.
Earlier X-compaction runs did not include the manager lock interface call.

Verification found a pre-existing Windows wall-clock filename collision when
synthetic compactions recur within one clock tick: `data_compacted_<UnixNano>.sst`
can reuse and truncate a live input. Output naming is unchanged by this treatment.
The read stress test waits for a distinct tick between successive compactions;
the campaign retains its specified 250 ms timer. Race verification is left to the
operator. The copied protocol is unchanged except for the requested plan-hash line.


## X-realism-v1

Protocol: [X_REALISM_PREREGISTRATION.md](X_REALISM_PREREGISTRATION.md).
Eight X1 cells, three arms, ten rounds: **240 trials**, with a 20-second pause
before every attempt. Budget **about 2.3 hours** (240 x (12.5 + 20) seconds =
2 h 10 min before fixture, process and retry overhead). Direct cells have 4/16
readers and 1/2/4 free writers; buffered controls have one writer. All retain
four tables, 1 MiB memtable, 2 s warmup, 10 s window, 200 ms cooldown, no
compaction, WAL sync and sync instrumentation on, diag_trace off, no delay.

Writers retain the existing stride, which separates request indices rather than
keys. KeyStream.At hashes into a shared 10,000-key domain, so conflict aborts are
possible. The pre-freeze amendment explicitly permits them: an abort is not a
failure and the trial is kept. W counts successful in-window commits only.
H-X reports abort counts per trial attempt and the abort share per cell, including
retries and late attempts; a share strictly above 1% must be disclosed beside that
cell's results. Unknown counts are marked explicitly. No campaign trial or smoke
was run during implementation.

Connect AC power, close other user applications, record Windows power mode and
Defender as found, and leave them unchanged. Record any interaction or background
process as a deviation; the operator and assistant leave the host alone during
execution. The operator handles the pre-data freeze; the assistant never commits.
Use the existing hash-bound gate X approval with block X1 only.

Prepare in PowerShell without launching trials:

```powershell
$env:GODEBUG = 'goindex=0'
$realismRunner = Join-Path $PWD 'i7trial.exe'
$realismCampaign = 'C:\scratch\x-realism-v1'
go build -o $realismRunner ./research/experiments/phase_i7/cmd/i7trial
go run ./research/experiments/phase_i7/cmd/xcampaign -design realism -runner $realismRunner -out $realismCampaign -dry-run
```

During preparation, use -allow-dirty if needed and record it. Expect master seed
20261111, settle_ms=20000, no anchors, the eight cells in protocol table order,
and plan SHA-256 (indented JSON bytes without trailing newline)
`e41320390c819d25fe74132d242e555e5831e063f71cba2d51eae0fc1d34681a`.
All paths/writer counts within a reader setting and round share
pairedSeed(master, "D_r04" or "D_r16", round). Each read logical index has the
same key/hit flag across comparisons. Read workers keep the same reader stride;
actual timing, counts and the set of indices completed can differ. Writer
indices retain their existing stride, so each indexed write is issued by one
worker, but this does not make their hashed keys disjoint. Sync before direct
handle open covers fixture writes that omitted sync. A rejected direct open or
aligned probe fails explicitly without buffered fallback.

The operator freezes and reviews the amended protocol and unchanged plan, then creates approval-realism.json with the current printed runner hash:

```json
{
  "campaign_id": "X-realism-v1",
  "approved": true,
  "runner_sha256": "<printed runner_sha256>",
  "plan_sha256": "e41320390c819d25fe74132d242e555e5831e063f71cba2d51eae0fc1d34681a"
}
```

Execution, only after freeze and operator approval:

```powershell
$env:GODEBUG = 'goindex=0'
go run ./research/experiments/phase_i7/cmd/xcampaign -design realism -runner $realismRunner -out $realismCampaign -approve .\approval-realism.json
python research/experiments/analysis/x_realism_analyze.py --data-dir $realismCampaign --output-dir C:\scratch\x-realism-v1-analysis
```

Repeat the execution command to resume. Failed attempts retry once; second failures
drop the complete three-arm cell-round. Cross-cell tests require both complete
triplets, paired by round. Successful observations in dropped units stay in trial
output. No outcome-based exclusions. For W log ratios only, a zero anywhere in
the comparison unit adds 0.1 writes/s to every run in that unit, as X-paced.
q with a zero W denominator is undefined and cannot establish a band prediction;
raw zeros remain visible. Missing data requires --allow-partial. No smoke is
authorized by these implementation instructions; any later operator-authorized
mechanical smoke must be separate, first plan identities only, logged in the
protocol, checked for status/cleanup/gaps only, and excluded from analysis.

Native Linux replication is optional if a native ext4/xfs host becomes available;
not running it is not a deviation. It uses the **same plan bytes/hash, seed and predictions**,
with fresh fixtures in a separate output directory on ext4 or xfs. WSL2 is only
a rehearsal: its virtual disk counts for nothing, and the analyzer rejects it
as replication. From PowerShell a cross-build check is:

```powershell
$env:GODEBUG = 'goindex=0'
$env:GOOS = 'linux'
go build ./research/experiments/phase_i7/...
go build -o .\i7trial-linux ./research/experiments/phase_i7/cmd/i7trial
go build -o .\xcampaign-linux ./research/experiments/phase_i7/cmd/xcampaign
Remove-Item Env:GOOS
```

On the native Linux host (or build there with GODEBUG=goindex=0), use uname and
findmnt from util-linux. The driver's platform.json records kernel, scratch
filesystem and source device, plus revision, runner/plan hashes, self-test,
architecture, power availability and disk space. Also record the physical host,
storage model/firmware and mount options, native-versus-virtual status, AC/power
settings and background activity/deviations. Keep these records beside platform.json.
The runner hash differs by platform; create a separate Linux approval bound to
that runner and the unchanged plan hash. Compare the generated plan bytes with
the Windows plan before executing:

```bash
export GODEBUG=goindex=0
./xcampaign-linux -design realism -runner ./i7trial-linux -out /mnt/ext4/x-realism-linux -dry-run
sha256sum /mnt/ext4/x-realism-linux/plan.json
# After operator freeze/approval, using the Linux runner hash:
./xcampaign-linux -design realism -runner ./i7trial-linux -out /mnt/ext4/x-realism-linux -approve ./approval-realism-linux.json
python research/experiments/analysis/x_realism_analyze.py --data-dir /path/to/windows-results --replication-dir /mnt/ext4/x-realism-linux --output-dir /path/to/side-by-side-analysis
```

Analysis requires NumPy, SciPy and Matplotlib, and deterministically writes
x_realism_results.json, x_realism_tables.md and one x_realism_s_q.png figure.
With --replication-dir, Windows and native Linux have separate estimates,
intervals, predictions, exclusions and provenance, shown side by side and never
pooled. An optional --readcost-dir <earlier-X-readcost-campaign> supplies hold
against injected-dose context; the default looks for the sibling campaign folder.
If unavailable, that descriptive item is explicitly marked unavailable. Earlier
campaign values never enter any confirmatory test. Hold, read p50/p95/p99,
S/read ratios at multiple writers, direct/buffered R/W ratios, commit wait,
flushes and descriptive drift are all retained. Bootstrap sensitivity uses
10000 paired resamples with seed 20261111.

Operator verification (no campaign or smoke):

```powershell
$env:GODEBUG = 'goindex=0'
go test -race ./lsmengine/ ./research/experiments/phase_i7/... -count=1
```

Targeted Linux/WSL engine names: TestDirectAlignment,
TestDirectReadEquivalenceConcurrent, TestDirectOpenFailureAndDefault,
TestDirectManagerValidationAndClose. Harness names: TestRealismSpecAndReadPath,
TestRealismPairedStreams, TestRealismWriterStrideKeyCollision,
TestRealismWriterCommitCountOnce and TestRealismConflictAccounting. Driver:
TestRealismPlan and TestExistingDesignFrozenPlans. On Linux/WSL set
GODEBUG=goindex=0 before every Go command, with -race available on the chosen host.
WSL tests validate mechanics only.

Windows coherence limitation: file.Sync calls FlushFileBuffers on a writable
cached handle before opening the separate NO_BUFFERING handle. SSTables remain
immutable afterward. Tests compare direct and buffered results, including a
cached rewrite without Sync, but cannot prove storage firmware persistence or
absence of device/controller caches. The flag bypasses the OS page cache; it
does not disable caches inside the SSD. 4096-byte alignment is the fixed
protocol requirement; devices/filesystems that reject it fail explicitly.
