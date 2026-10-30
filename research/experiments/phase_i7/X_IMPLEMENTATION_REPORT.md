# X read-cost implementation report

Implemented runtime WAL-sync switching, X1/X2/X3 harness support, bounded latency
and sync histograms, gate X approval binding, and the sequential resumable
`xcampaign` driver. No commit was made. The full campaign was not executed.
Exactly three real runner trials were executed, all from the starting anchor
triplet; subprocess tests otherwise used a fake runner without an engine.

## Files changed

- `lsmengine/manager.go`: atomic WAL policy and SetWALSync; policy survives rotations.
- `lsmengine/wal.go`: atomic DisableSync read by Append.
- `lsmengine/wal_sync_test.go`: runtime toggles and manual/threshold rotations.
- `lsmengine/read_path_test.go`: existing WAL policy assertions use atomic loads.
- `lsmengine/read_delay_test.go`: timing-accuracy test skips under the race detector.
- `lsmengine/race_enabled_test.go`: race build-tagged test constant.
- `lsmengine/race_disabled_test.go`: non-race test constant.
- `research/experiments/phase_i7/harness2/spec.go`: read-path validation, X modes, explicit X treatment JSON and fixture WAL annotation.
- `research/experiments/phase_i7/harness2/run.go`: engine configuration, fixture WAL toggle, X recording and X2 shortfall outcomes.
- `research/experiments/phase_i7/harness2/metrics.go`: bounded X shards; existing raw recorder paths retained.
- `research/experiments/phase_i7/harness2/histogram.go`: allocation-free log-linear histograms, merge and latency summaries.
- `research/experiments/phase_i7/harness2/syncinstr.go`: optional atomic sync histograms and percentile fields for X.
- `research/experiments/phase_i7/harness2/x_test.go`: spec, percentile accuracy, merge, allocation, concurrent sync and shortfall/fault tests.
- `research/experiments/phase_i7/harness2/run_test.go`: E2 synthetic fixtures retain disk reads instead of overflowing raw memory-only samples.
- `research/experiments/phase_i7/cmd/i7trial/main.go`: X dispatch and separate X approval/plan checks; old approval checks retained.
- `research/experiments/phase_i7/cmd/i7trial/main_test.go`: dispatch inventory and X approval coverage.
- `research/experiments/phase_i7/cmd/xcampaign/main.go`: CLI, preflight, approval, immutable plan checks and result validation.
- `research/experiments/phase_i7/cmd/xcampaign/plan.go`: fixed cells, paired seeds, balanced arm permutations, randomized rounds and anchors.
- `research/experiments/phase_i7/cmd/xcampaign/execution.go`: process timeout, retry, ledger, resume, progress and failure-rate stop.
- `research/experiments/phase_i7/cmd/xcampaign/disk_windows.go`: Windows available disk-space probe.
- `research/experiments/phase_i7/cmd/xcampaign/disk_linux.go`: Linux available disk-space probe.
- `research/experiments/phase_i7/cmd/xcampaign/main_test.go`: design, hash refusals, fake-process retry/timeout, resume and stop-threshold tests.
- `research/experiments/phase_i7/cmd/xcampaign/README.md`: invocation, approval, seed encoding, lifecycle and measurement semantics.
- `research/experiments/phase_i7/X_IMPLEMENTATION_REPORT.md`: this report.

Pre-existing engine changes, ZIPs, preregistration, frozen results, evidence and
acceptance-source copies were preserved. `run_i7.ps1` was not changed.

## Verification

Final commands used a temporary `GOCACHE` under `%TEMP%\xcheck\gocache` because
an earlier default-cache invocation returned Access is denied. There are no new
third-party dependencies.

| Command/check | Final result |
|---|---|
| `gofmt -l lsmengine research/experiments/phase_i7` | 24 pre-existing findings in unrelated files/frozen acceptance copies; left untouched. |
| `gofmt -l` over the 22 changed Go files | PASS: 22 clean, 0 findings. |
| `git diff --check` | PASS: 0 whitespace errors. |
| `go vet ./lsmengine ./research/experiments/phase_i7/...` | PASS: 0 diagnostics. |
| `go build ./...` | PASS. |
| `go test ./lsmengine ./research/experiments/phase_i7/... -count=1 -short` (JSON output for counting) | PASS: 351 test/subtest pass events, 3 intentional short-test skips, 0 failures; 5 tested packages pass, 2 packages have no tests. |
| `go test ./research/experiments/phase_i7/cmd/xcampaign -count=1` | PASS after final driver corrections. |
| `go build -o %TEMP%\xcheck\i7trial.exe ./research/experiments/phase_i7/cmd/i7trial` | PASS. |
| `go run ./research/experiments/phase_i7/cmd/xcampaign -runner %TEMP%\xcheck\i7trial.exe -out %TEMP%\xcheck\out -dry-run -allow-dirty` | PASS: full cell table, 20 schedule rows, hashes, preflight and ETA printed. |
| Same dry-run with `-out %TEMP%\xcheck\final-out` after final round-seed correction | PASS: 519 rows, specified master_seed + round shuffling. |
| Driver with `-limit 3 -approve %TEMP%\xcheck\approval.json -allow-dirty` | PASS: 3 SUCCESS, 0 retries, 0 failures. |
| `GOOS=linux GOARCH=amd64 go build -o %TEMP%\xcheck\xcampaign-linux ./research/experiments/phase_i7/cmd/xcampaign` | PASS, including final driver revision. |

The first development test run found the expected dispatch inventory change and
two E2 fixtures that exceeded raw-sample capacity after the fast engine change.
Those tests were corrected without changing E/D recorder behavior. A subsequent
verification attempt hit a default-cache access failure; the temporary-cache
runs above pass. Race runs were skipped as requested; no WSL2 copy was attempted.

## Final dry-run

- X1: 10 cells; X2: 4 cells; X3: 3 cells.
- 17 x 10 x 3 primary runs + 9 anchor runs = **519 runs**.
- ETA at 14 seconds per run: **2h1m6s**.
- HEAD: `4a9a798cfb4e79a12cf7f3fd83f2eefcfa15f9ec`; dirty-tree override recorded.
- Runner selftest: PASS; Windows power scheme: Balanced.
- Final minimum free disk: 82,307,993,600 bytes (above 5 GB).
- Runner SHA-256: `24c311596f04c5cc6270dceb105aeda38b6e5abe2835b9770fb37c5773384dd0`.
- Final plan SHA-256: `c601f7ecfca764b3c70d913c7a0851d80726583aa38c9dc9e59fe4188956dfbb`.
- Opening rows: ANCHOR A / AMIN / B, then X1_r16_legacy B / A / AMIN.

## Three-trial smoke

| Trial | Status | Attempt | Runner wall | Driver wall | Fixture |
|---|---|---:|---:|---:|---:|
| ANCHOR_t00_A | SUCCESS | 1 | 12.431 s | 12.529 s | 188 ms |
| ANCHOR_t00_AMIN | SUCCESS | 1 | 12.474 s | 12.527 s | 240 ms |
| ANCHOR_t00_B | SUCCESS | 1 | 12.462 s | 12.517 s | 229 ms |

All three results record `fixture_wal_sync=false`, `read_path="block"`,
`read_io_delay_us=0`, and `wal_sync=true`. Progress recorded three successes,
zero failed identities and 516 remaining. No additional real trials were run.

A final audit after smoke changed round randomization from a derived hash to the
specified `int64(master_seed + round)` and removed blank lines from normal ledger
appends. Driver subprocess tests, full vet/build/tests and dry-run were repeated.
The final plan's first three trial specs were compared to the smoke plan and are
identical; the runner binary is unchanged. The smoke approval bound the earlier
full-plan hash `fccbf00e88f0e9f25ea88e4729c61df65ce16563c1111bcefe50c7622b68e86f`.
No extra engine trials were used to recheck the later driver-only edits.

For ANCHOR_t00_A, the measured read histogram has 68,977 in-window samples;
68,981 reads completed including four late completions. There were 8,592 writes,
zero operation errors and zero validation errors.

| Read latency | Microseconds |
|---|---:|
| Min | 0.800 |
| Mean | 578.343 |
| Max | 53,248.200 |
| p50 | 52.480 |
| p95 | 1,966.080 |
| p99 | 3,162.112 |
| p999 | 6,946.816 |

Sync summaries cover the entire trial lifecycle, including fixture, warmup and
drain; they are not restricted to the measurement window. All durations below
are microseconds. Existing publication aggregate fields remain unchanged.

| Sync distribution | Count | Min | Mean | Max | p50 | p95 | p99 |
|---|---:|---:|---:|---:|---:|---:|---:|
| A read critical section | 425,299 | 0.200 | 20.648 | 3,156.700 | 9.984 | 71.168 | 169.984 |
| Commit lock wait | 8,596 | 0.500 | 42.875 | 601.500 | 37.120 | 78.848 | 139.264 |
| Flush publication lock wait | 5 | 0.500 | 345.160 | 1,722.300 | 0.900 | 1,720.320 | 1,720.320 |
| Flush publication duration | 5 | 0.500 | 0.900 | 1.600 | n/a | n/a | n/a |

## Choices and limits

X blocks require `wal_sync=true`, as permitted by the brief; a missing/false bool
is rejected. Seed encoding is documented in the driver README and persisted in
the plan. Histogram percentile bucket error is below 0.8% in the specified
1 ns..120 s range; min/max/mean are exact aggregates. X2 event counts and late
records are retained while shortfall alone no longer fails a trial. Existing
faults remain failures. The shortfall test uses synthetic publication counts and
also checks a real fixture validation fault; it does not depend on scheduler
lateness to manufacture a shortfall. All non-engine subprocess execution tests
use the fake runner. No unresolved spec questions remain.

Temporary cleanup: BLOCKED. Automatic approval review rejected both the
validated cleanup command and a narrower absolute-path cleanup with
"blocked by policy". The target was verified as the ordinary directory
`C:\Users\ramst\AppData\Local\Temp\xcheck`, without a reparse point.
It remains on disk; the approval policy does not permit escalation.
