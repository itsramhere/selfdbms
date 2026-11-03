# Instrument the commit path to locate the write stall

## Goal
Every paced trial shows one write service time of about 55 ms (range 32 to 103 ms) in every arm and cell. Existing records show first-hold lock wait is never the cause, that readers see the same delay, and that read critical sections stay far shorter, which rules out a process-wide freeze in CoarseLock at d=2000. Done means a diagnostic-only command that runs one existing X4 spec and writes, for each slow commit, when it happened and which phase of the commit took the time. This is a diagnostic, not a campaign. Do not run any trial beyond the short test in Verification. Do not commit.

## Context
- `lsmengine/transaction.go` `Commit`: first `Mu.Lock`, conflict check, `Log.Append` (write and fsync, `lsmengine/wal.go`), `Mem.Put`, unlock, then `checkAndTriggerFlush` (`lsmengine/manager.go:1076`), which takes `Mu.Lock` again.
- `lsmengine/sync_instrumentation.go`: the observer contract. Observers are non-blocking, lock-free and allocation-free. `CompactionObserver` is the pattern for an optional extension of `SyncObserver`.
- `research/experiments/phase_i7/harness2/run.go`: `Run`, `pacedWriter` (line 729), `closedLoopReader`, `openLoopWorker`. `syncCollector.Reset()` and `base := Now()` mark the window start. `qpcElapsedNanos` is the engine clock.
- `research/experiments/phase_i7/harness2/spec.go`, `metrics.go`: spec and `TrialResult`.
- `research/experiments/phase_i7/cmd/i7trial/main.go`: the approval-gated runner. Leave it unchanged.
- Existing specs can be copied from `research/experiments/phase_i7/X_READCOST/paced/trials/*_a1.json` (the `spec` key of each record).

## Spec

### Engine
Add an optional observer, in the style of `CompactionObserver`:

```go
type CommitPhases struct {
    ArrivalNs, AcquiredNs, ValidatedNs, WalWriteDoneNs, WalSyncDoneNs, AppliedNs, ReleasedNs, FlushCheckDoneNs int64
}
type CommitTraceObserver interface { OnCommitPhases(p CommitPhases) }
```

- `SetSyncObserver` caches the type assertion in one Manager field, so an untraced commit pays one nil check per site at most.
- `Commit` fills the struct only when that field is non-nil, using `instrumentClock`. Report it only for a successful commit, after `checkAndTriggerFlush` returns. If a commit writes more than one key, the WAL timestamps cover the last key.
- WAL timing: keep `Wal.Append`'s signature and behaviour. Factor out an internal function that optionally takes the clock and returns the two timestamps, with `Append` calling it with no clock. With `DisableSync` set, `WalSyncDoneNs` equals `WalWriteDoneNs`.
- Behaviour, ordering, record format and error paths must not change.

### Harness
Add one optional spec field, `diag_trace bool` with `json:"diag_trace,omitempty"`, and one optional result field, `stall_diagnostics` with `omitempty`. With `diag_trace` false, specs, results and plan hashes must stay byte-identical to today. It requires `instrument_sync`; fail the trial otherwise.

When on, attach a collector extending the X histogram sync collector with these parts. Preallocate before the window, and make no allocation on the hot path. Reset at the same point as `syncCollector.Reset()`.

1. **Commit trace.** Keep every commit in the window at or above 3 ms total (`FlushCheckDoneNs - ArrivalNs`) and always the first 5 commits, up to a capacity of 4,096 with a dropped counter. Store times relative to window start. Also keep whole-window phase histograms for all commits, using the existing `Histogram` type: lock wait, validate, WAL write, WAL sync, apply, flush check, and total.
2. **Heartbeat.** One goroutine sleeps 1 ms in a loop for the window and records each gap above 3 ms between wakeups (start relative to the window, length), up to a capacity of 4,096 with a dropped counter. It takes no engine lock.
3. **Slowest read per reader.** In `closedLoopReader` and `openLoopWorker`, when tracing is on, keep each worker's longest read service and its start time relative to the window. Report all of them. Keep the untraced path unchanged apart from one nil check on the shard.
4. **Go runtime.** Immediately before the window and after the workers join: `debug.ReadGCStats` (GC count delta, pause total delta, and each pause ending in the window, with its offset, derived from `time.Now` taken at the window start), and from `runtime/metrics` the `/sched/latencies:seconds` delta as a non-empty bucket list and `/sched/pauses/total/gc:seconds` the same way.

### Command
New `research/experiments/phase_i7/cmd/stalldiag/main.go`:

`stalldiag -spec S.json -out F.json [-data-dir D] [-arm A|AMIN|B] [-wal-sync true|false] [-trial-id ID]`

- Calls `harness2.Run` directly with `diag_trace` forced on and writes the result as JSON with a top-level `"diagnostic": true`. The spec's other fields are unchanged except the given overrides. It may read a whole trial record and take its `spec` key, or a bare spec.
- Removes the data directory afterwards.
- Refuses an `-out` path inside any `X_READCOST/*/trials` directory, so diagnostics can't be mistaken for campaign evidence.

### Analysis
New `research/experiments/analysis/stall_diag_report.py`, standard library only, which takes a directory of `stalldiag` outputs and prints:
- For each output: its arm, delay, write rate and wal_sync; the 10 slowest commits with index, time in the window and each phase in ms; the phase holding the largest share of each commit; the slowest read's start and length; heartbeat gaps of 10 ms or more; GC pauses in the window; whether the slowest commit overlaps a heartbeat gap, a GC pause or the slowest read.
- A summary across outputs: per arm and cell, the median largest commit; the share of commits at or above 10 ms by phase; the distribution of slow commits' positions in the window, in 1 s bins; and how many of the largest commits are the first commit of the trial.

## Constraints
- Do not modify `cmd/i7trial`, `cmd/xcampaign`, plans, approvals, or anything under `X_READCOST/`.
- No new module dependencies.
- Match the surrounding code's style and comment density.
- Existing behaviour with `diag_trace` off is untouched, including `DisableWALSync` handling.

## Tests
- Engine: a test with a stub `CommitTraceObserver` asserts the timestamps are non-decreasing in field order and that one report arrives per successful commit, with `WalSyncDoneNs` equal to `WalWriteDoneNs` with sync disabled; a failed commit reports nothing; a Manager with a plain `SyncObserver` behaves as before.
- Harness: `diag_trace` false marshals a spec identical to today (no new key); a short X4 trial with `diag_trace` on (about 500 ms, small fixture, temp dir) fills the trace, with first commits recorded and times inside the window.
- Command: the refusal path for `-out`.
- Analysis: a small unit test using a hand-written fixture JSON, named `test_stall_diag_report.py` in the same directory.

## Verification
Run these and report the output. All must pass.
- `go build ./...`
- `go vet ./lsmengine/ ./research/experiments/phase_i7/...`
- `go test ./lsmengine/ -count=1`
- `go test ./research/experiments/phase_i7/... -count=1`
- `go test -race ./lsmengine/ -run "Commit|Sync" -count=1` if the race detector is available; say so if it is not.
- `python -m unittest research/experiments/analysis/test_stall_diag_report.py` (or the discover form)
- `python -m unittest discover -s research/experiments/analysis -p "test_x_readcost*.py"`
- Confirm a plan generated by `xcampaign` for X-paced-v1 hashes to the same value as before, using its existing tests or a dry run into a temp directory, if one exists.

## Report
End with: files changed; commands run and their results; how the untraced path differs (show the exact added checks); and anything unfinished or uncertain.
