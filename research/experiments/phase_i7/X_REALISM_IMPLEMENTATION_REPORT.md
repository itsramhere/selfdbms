# X-realism-v1 implementation report

Branch: `research/x-phase3`; base HEAD: `4d7d41408d849c1ee7e2a8d948e24fc93b47fcac`.
No commit, campaign trial or smoke was run. The operator's pre-data freeze is still
outstanding. The supplied protocol was initially copied byte-for-byte with only
the requested hash line replaced. The authorized pre-freeze amendments now permit
writer conflicts, make Linux replication optional, and replace H-X with descriptive
abort reporting. The plan and its hash remain unchanged.

## Pre-freeze amendment: writer conflicts permitted

The existing stride partitions writer request **indices**, not keys. KeyStream.At
hashes those indices into the common 10,000-key space. In the default plan's
r04/round-1 stream (seed 3507978258608147492), indices 40 and 97 both produce
`user_key_0000009160`. They belong to writers 0 and 1 with either 2 or 4 writers.
The pre-freeze amendment explicitly accepts this existing mapping: writers are
not required to use disjoint keys, and conflict aborts are possible. No key
partitioning was introduced. TestRealismWriterStrideKeyCollision records the
counterexample without a trial. Conflict aborts are identified by ErrWriteConflict,
counted per trial attempt (including late attempts), and retained without failing
the trial. W counts successful in-window commits only. H-X now reports per-cell
sum(aborts)/sum(attempted commits), over all recorded attempts including retries,
failures and superseded attempts. A share strictly greater than 1% is flagged for
disclosure beside the cell's results. Zero attempted commits leave the share
undefined; unknown counts/missing trials are listed and do not establish a full
cell share. Observed counts and observed shares remain visible. H-X has no
supported/not-supported prediction verdict. This resolves the earlier protocol
conflict without changing the harness, treatment or plan.

Native Linux ext4/xfs replication is optional if a host becomes available.
Not running it is not a deviation. If run, it uses the same plan/hash and
predictions and is reported beside Windows without pooling. WSL2 remains a
rehearsal only.

## Plan and seed audit

Plan SHA-256 (indented JSON bytes without trailing newline):
`e41320390c819d25fe74132d242e555e5831e063f71cba2d51eae0fc1d34681a`.

Eight cells, 240 trials, 20-second settling pauses, default master seed 20261111.
The single harness seed uses pairedSeed(master, "D_r04"/"D_r16", round), shared
across arms, writer counts and paths within a reader setting. Read At(j) requests
are identical, including hit flags; reader strides are unchanged within each
setting. Warmup seeds use the same existing XOR across comparisons. Writers also
share the existing XOR-derived indexed stream, with assignment to workers changed
by writer stride. Wall-clock issue order, completion counts and the finite set of
indices completed need not match, so realized request traces are only partially
paired. The seed is not paired between the two reader settings.

All earlier frozen-plan checks pass, including X-inputopen-v1
`5b685feb2327ff8d9b1e72b4bc0637570f0fe0014690ec9e214e84f163893a98`.
The existing regression test now includes that plan as well.

## Pre-freeze amendment verification

No trials or commits were run. The following follow-up checks pass with
GODEBUG=goindex=0 on all Go commands:

- `python -m unittest discover -s research/experiments/analysis -p "test_x_realism*.py"`: 12 tests, including the strictly-greater-than-1% boundary, weighted cell shares, zero denominators, unknown counts and retry accounting.
- `go test ./research/experiments/phase_i7/cmd/xcampaign/ -count=1`: PASS, including frozen plans.
- Fresh runner build and `xcampaign -design realism -dry-run -allow-dirty`: PASS; 8 cells, 240 trials. Plan SHA-256 remains `e41320390c819d25fe74132d242e555e5831e063f71cba2d51eae0fc1d34681a`.
- Dry-run output: `C:/Users/ramst/AppData/Local/Temp/x-realism-amendment-810afcb10b844dd7bf192f0f63448b9d/dry-run`. No trial or ledger output was created.

The follow-up changes only the protocol, analyzer/tests, implementation report,
run instructions and driver README. Engine/harness/plan code is unchanged.

## Commands and final results

All Go commands used GODEBUG=goindex=0. In PowerShell this was set with
`$env:GODEBUG='goindex=0'` before execution.

- `go test ./lsmengine/ ./research/experiments/phase_i7/... -count=1`: PASS on the final full run.
- `python -m unittest discover -s research/experiments/analysis -p "test_x_realism*.py"`: PASS, 12 synthetic tests after the pre-freeze amendment. Includes all predictions, descriptive metrics and earlier hold context, missing/failed/retried units, retained conflicts, zeros, fail-closed provenance/treatment checks, Holm family sizes, deterministic JSON/Markdown/PNG, and unpooled native Linux replication with WSL rejection.
- `GOOS=linux go build ./research/experiments/phase_i7/...` (PowerShell `$env:GOOS='linux'` in that process): PASS. No Linux executable or test was run.
- `go build -o <scratchpad>/i7trial-realism-final.exe ./research/experiments/phase_i7/cmd/i7trial`: PASS.
- `go run ./research/experiments/phase_i7/cmd/xcampaign -design realism -runner <scratchpad>/i7trial-realism-final.exe -out <scratchpad>/realism-dry-run-final -dry-run -allow-dirty`: PASS. Eight cells, 240 trials; preflight/self-test only. No ledger.jsonl or trials directory was created. Base estimate 2h10m; documentation budgets about 2.3 hours.
- Analyzer validation of the actual generated Go plan: PASS, 240 trials / 80 cell-round units.
- `git diff --check`: PASS.
- Original implementation protocol reverse-replacement comparison: PASS at that time. The current protocol additionally contains the four authorized pre-freeze text amendments.

Scratchpad is
`C:/Users/ramst/AppData/Local/Temp/claude/8a41a9e007ad83fc/c4daef8c-329e-45ab-9d62-3e83c9298e35/scratchpad`.
Final dry-run runner hash:
`196eea5555620433e59a7206e417a4ef38fa58ba81fd69a447f1373d78934b18`.
Dry-run preflight recorded Balanced power and a dirty worktree with the explicit
preparation override. These are mechanical verification records, not run approval.

An intermediate full run intermittently failed the existing
TestWALSyncRecovery/disabled=true byte-parity assertion. Its targeted rerun and
the final full-suite rerun passed. Cause remains unconfirmed; rapid Windows
clock-based WAL filename reuse is a possibility, not an established diagnosis.
No WAL behavior or filename was changed. Earlier implementation test failures
(EOF retry alignment and a Windows closed-handle assertion) were corrected;
final direct-reader tests pass.

## Operator race/Linux tests

Race and Linux runtime verification are left to the operator as requested:

```bash
GODEBUG=goindex=0 go test -race ./lsmengine/ ./research/experiments/phase_i7/... -count=1
GODEBUG=goindex=0 go test -race ./lsmengine/ -run '^TestDirect' -count=1
GODEBUG=goindex=0 go test -race ./research/experiments/phase_i7/harness2 -run '^TestRealism' -count=1
GODEBUG=goindex=0 go test ./research/experiments/phase_i7/cmd/xcampaign -run 'TestRealismPlan|TestExistingDesignFrozenPlans' -count=1
```

Engine names: TestDirectAlignment, TestDirectReadEquivalenceConcurrent,
TestDirectOpenFailureAndDefault, TestDirectManagerValidationAndClose.
Harness names: TestRealismSpecAndReadPath, TestRealismPairedStreams,
TestRealismWriterStrideKeyCollision, TestRealismWriterCommitCountOnce,
TestRealismConflictAccounting. Runner gate: TestXApproval.
Use an ext4/xfs directory for Linux direct I/O checks. WSL2 results validate
mechanics only and are excluded from replication analysis.

## Limits and uncertainty

Windows sync opens a writable cached handle and calls file.Sync (FlushFileBuffers)
before opening the separate NO_BUFFERING handle. Metadata retains its ordinary
handle, and SSTables are immutable after publication. A pooled 4096-aligned
buffer covers each wanted block; one native ReadFile operation avoids os.File.ReadAt
retrying an EOF short read with unaligned arguments. Concurrent correctness,
MVCC/tombstones/misses, straddled versions, partial EOF, cached rewrites without
Sync, manager conflict validation in all arms and direct-handle close are tested.
These checks support cached-write/direct-read coherence on this tested Windows
filesystem, but do not prove storage firmware persistence or bypass SSD/controller
caches. Fixed 4096 alignment can fail on an unsupported device/filesystem; open
and aligned-probe rejection is explicit, with no engine fallback. The Windows
handle is synchronous; native handle scheduling can serialize overlapping reads.
Native Linux filesystem/device probing and O_DIRECT runtime behavior are compiled
but not exercised here.

Microsoft documents the required flushing and alignment behavior:
[flushing buffered I/O](https://learn.microsoft.com/en-us/windows/win32/fileio/flushing-system-buffered-i-o-data-to-disk),
[file buffering/alignment](https://learn.microsoft.com/en-us/windows/win32/fileio/file-buffering).

The Linux cross-build required a runner platform wrapper for an existing
Windows-only QPCFrequencyHz call. Windows reports its unchanged QPC value;
non-Windows reports null, since its timestamps use Go's monotonic clock. Linux
platform.json now records uname kernel text and findmnt scratch filesystem/source
device; a missing probe fails preflight. Physical hardware, mount options and
host deviations are additional operator records described in the run instructions.

No earlier data was read during verification. Across-campaign hold reporting can
read an explicit --readcost-dir or a sibling campaign directory; unavailable
context is marked unavailable and never pooled. Replication must have identical
plan bytes/hash, separate platform/runner provenance, native Linux ext4/xfs, and
no WSL kernel. It gets its own estimates and predictions beside Windows.
The protocol defines no dedicated q rule for W=0; q is reported undefined and
its band decision inconclusive, without silently dropping the zero denominator.
For W logarithms, the specified X-paced +0.1 whole-unit rule is used. This
undefined-q handling is disclosed for review before observations.

## Files changed

- `lsmengine/direct.go`
- `lsmengine/direct_linux.go`
- `lsmengine/direct_other.go`
- `lsmengine/direct_test.go`
- `lsmengine/direct_windows.go`
- `lsmengine/manager.go`
- `lsmengine/sstableReader.go`
- `lsmengine/transaction.go`
- `research/experiments/analysis/test_x_realism_analyze.py`
- `research/experiments/analysis/x_realism_analyze.py`
- `research/experiments/phase_i7/PHASE3_RUN_INSTRUCTIONS.md`
- `research/experiments/phase_i7/X_REALISM_IMPLEMENTATION_REPORT.md`
- `research/experiments/phase_i7/X_REALISM_PREREGISTRATION.md`
- `research/experiments/phase_i7/cmd/i7trial/main.go`
- `research/experiments/phase_i7/cmd/i7trial/main_test.go`
- `research/experiments/phase_i7/cmd/i7trial/timer_other.go`
- `research/experiments/phase_i7/cmd/i7trial/timer_windows.go`
- `research/experiments/phase_i7/cmd/xcampaign/README.md`
- `research/experiments/phase_i7/cmd/xcampaign/disk_linux.go`
- `research/experiments/phase_i7/cmd/xcampaign/disk_windows.go`
- `research/experiments/phase_i7/cmd/xcampaign/followup_test.go`
- `research/experiments/phase_i7/cmd/xcampaign/main.go`
- `research/experiments/phase_i7/cmd/xcampaign/realism.go`
- `research/experiments/phase_i7/cmd/xcampaign/realism_test.go`
- `research/experiments/phase_i7/harness2/metrics.go`
- `research/experiments/phase_i7/harness2/realism.go`
- `research/experiments/phase_i7/harness2/realism_test.go`
- `research/experiments/phase_i7/harness2/run.go`
- `research/experiments/phase_i7/harness2/spec.go`

The preregistration, instructions and README document the amended design and
permitted writer conflicts. Engine files add direct handles/reads and tests; harness files add
campaign scope, counters and tests; driver/runner files add scheduling, Linux
platform records, approval and compatibility tests; analysis files add every
prediction/descriptive item and deterministic side-by-side artifacts.
