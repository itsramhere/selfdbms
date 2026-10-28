# Phase I7 — Pebble Trial Runner: Verification Record

**Module:** `research/experiments/phase_i7/pebble_runner/`
**Module path:** `owndbms/pebble_runner`
**Pinned engine:** `github.com/cockroachdb/pebble v1.1.5`
**Date:** 2026-10-28
**Host:** native Windows, `go1.26.3 windows/amd64`, `CGO_ENABLED=0`, no C compiler,
PowerShell 5.1/7.

This runner executes **one** descriptive trial per invocation. It is an external anchor for
block E7 and **must never be used for a paired comparison**.

## Toolchain-gate results

All commands below were run with the working directory set to
`C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\pebble_runner`
and with `$env:CGO_ENABLED = "0"`.

| # | Command | Exit code | Output |
|---|---------|-----------|--------|
| 1 | `go vet ./...` | **0** | no findings |
| 2 | `go test ./...` | **0** | `ok owndbms/pebble_runner` |
| 3 | `go build -trimpath -o pebble_runner.exe .` | **0** | `pebble_runner.exe`, 20,787,712 bytes, SHA-256 `66ff490871a4fde16863ea93277f162ec10ceb406a0c1523e2536b951f7d9bb1` |
| 4 | `go test -count=1 ./...` (uncached confirmation) | **0** | `ok owndbms/pebble_runner` |

The tests never open Pebble: they cover `absentKeyIndex`/`absentKey`, `envelopeJSON`,
`approvalCheck`, and `percentile`/`summarizeLatency` only.

## Self-test trial (cell Y1)

Spec written **outside the repository**, under `%TEMP%`
(`C:\Users\ramst\AppData\Local\Temp\i7_runner_selftest_04a753b08b5b4f2fac9dbbc77caf2bd2`,
deleted afterwards):

```json
{
  "schema": "i7.trial-spec.v1",
  "trial_id": "I7-E7-Y1-RUNNER-SELFTEST",
  "block": "E7",
  "cell": "Y1",
  "arm": "PEBBLE",
  "seed": 20261028,
  "reader_concurrency": 2,
  "writer_concurrency": 0,
  "measurement_duration_ms": 400,
  "warmup_duration_ms": 100,
  "cooldown_duration_ms": 0,
  "keyspace_cardinality": 200,
  "initial_sstable_count": 2,
  "initial_keys_per_sstable": 100,
  "target_read_hit_ratio": 0.95,
  "memtable_threshold_bytes": 67108864,
  "data_dir": "<TEMP>\\i7_runner_selftest_04a753b08b5b4f2fac9dbbc77caf2bd2\\db"
}
```

Synthetic approval file (also outside the repository):

```json
{"campaign_id":"I7_STRENGTHENING_V1","gate":"G4","approved":true,
 "protocol_version":"1.2.0-windows","approved_by":"selftest"}
```

Invocation:

```powershell
& .\pebble_runner.exe `
  -spec   "$env:TEMP\...\spec.json" `
  -out    "$env:TEMP\...\envelope.json" `
  -platform "$env:TEMP\...\platform.json" `
  -git-rev "selftest-sha" `
  -gate G4 `
  -approval "$env:TEMP\...\approval.json"
```

stdout (one line):

```
trial=I7-E7-Y1-RUNNER-SELFTEST cell=Y1 arm=PEBBLE status=SUCCESS reads=310478/310478 writes=0/0 errors=0 p50_ns=1700
```

**Process exit code: 0.** `status` was `SUCCESS`, `error` was `""`, the data directory was
removed immediately after close (`data_dir_removed: true`), and no database directory or temp
file remained. All temp artifacts were then deleted.

### Envelope JSON (trimmed: `provenance` and `spec` omitted, no field renamed)

```json
{
  "schema": "i7.trial.v1",
  "trial_id": "I7-E7-Y1-RUNNER-SELFTEST",
  "block": "E7",
  "cell": "Y1",
  "arm": "PEBBLE",
  "runtime": {
    "go_version": "go1.26.3",
    "gos": "windows",
    "goos": "windows",
    "goarch": "amd64",
    "gomaxprocs": 12,
    "num_cpu": 12,
    "pebble_version": "v1.1.5",
    "exe_sha256": "66ff490871a4fde16863ea93277f162ec10ceb406a0c1523e2536b951f7d9bb1"
  },
  "timing": {
    "started_utc": "2026-10-28T10:52:42.948886Z",
    "finished_utc": "2026-10-28T10:52:43.8473118Z",
    "wall_ms": 898,
    "cooldown_ms": 0
  },
  "pebble_options": {
    "memtable_size_bytes": 67108864,
    "memtable_size_source": "spec",
    "cache_size_bytes": 67108864,
    "cache_kind": "pebble.NewCache (DB block cache)",
    "disable_automatic_compactions": true,
    "automatic_compactions_controlled": true,
    "automatic_compactions_note": "automatic compactions disabled via Options.DisableAutomaticCompactions (supported in pebble v1.1.5)",
    "overrides": [
      "disable_automatic_compactions=true",
      "memtable_size_bytes_from_spec"
    ]
  },
  "status": "SUCCESS",
  "error": "",
  "data_dir_removed": true,
  "result": {
    "operations": {
      "read_ops_attempted": 310478,
      "read_ops_completed": 310478,
      "write_ops_attempted": 0,
      "write_ops_completed": 0,
      "error_ops_count": 0,
      "validation_error_count": 0
    },
    "throughput": {
      "read_ops_per_sec": 775139.4538487216,
      "write_ops_per_sec": 0,
      "total_ops_per_sec": 775139.4538487216
    },
    "latency_read_nanos": {
      "sample_count": 310478,
      "p50": 1700,
      "p95": 2900,
      "p99": 4000,
      "p999": 26700,
      "p95_supported": true,
      "p99_supported": true,
      "p999_supported": true,
      "samples_dropped": 0,
      "sample_capacity_per_reader": 200000,
      "sampling_note": "per-goroutine preallocated slices; merged after the window",
      "clock_source": "windows.QueryPerformanceCounter",
      "clock_resolution_nanos": 100
    },
    "lsm_diagnostics": {
      "measured_flushes": 2,
      "measured_compactions": 0,
      "final_sstables": 2,
      "fixture_sstables": 2,
      "backing_table_count": 0,
      "pebble_metrics_snapshot": "flush.count=2 compact.count=0 table.backing=0 table.obsolete=0 levels=L0:2,L1:0,L2:0,L3:0,L4:0,L5:0,L6:0 memtable.count=1 uptime_ms=806"
    },
    "read_misses": 15514,
    "warmup": {
      "attempted": 82006,
      "completed": 82006,
      "errors": 0,
      "duration_ms": 100
    },
    "measurement_window_ms": 400
  }
}
```

The 5% absent mix is visible: 15,514 misses / 310,478 reads = 4.997%.

## Negative approval case

Same spec geometry, but approval `gate` = `"G3"` while `-gate G4` was passed:

```
not approved: approval gate "G3" != -gate "G4"
```

| Check | Result |
|-------|--------|
| process exit code | **4** |
| output envelope file created | **no** (`Test-Path` false) |
| data directory created | **no** (`Test-Path` false) |

The negative-case temp directory was deleted afterwards.

## Additional smoke test: cell Y3 + writer concurrency

Not required, run to exercise the Y3 option override and writer path. Spec: cell `Y3`,
`reader_concurrency=1`, `writer_concurrency=1`, 300 ms measurement, 50 ms warmup,
`memtable_threshold_bytes=0`. Result: exit **0**, `SUCCESS`, `data_dir_removed=true`,
`memtable_size_bytes=16384` (`cell_y3_override`), `disable_automatic_compactions=false`,
120,971 reads/120,971 attempts, 243 writes/243 attempts, 0 errors, p50 1,600 ns.
With automatic compactions enabled, the fixture produced 7 flushes and 1 compaction
(`final_sstables=3`), unlike Y1's 2 flushes / 0 compactions / 2 SSTables.

## v1.1.5 / Windows API surprises actually encountered

1. **Automatic compactions ARE controllable in v1.1.5.** `Options.DisableAutomaticCompactions`
   exists (`options.go:884`), so Y1/Y2 set it to `true`; the envelope reports
   `automatic_compactions_controlled=true`. No "option absent" fallback was needed.
   Y3 explicitly leaves it `false` and forces `MemTableSize=16384`; a 16 KiB memtable is
   accepted without a lower-bound error.
2. **`Metrics.Table.BackingTableCount` is not the number of fixture SSTables.** On a fresh
   v1.1.5 DB it reported **0** while `Metrics.Levels[0].NumFiles` was 2. Therefore
   `result.lsm_diagnostics.final_sstables` is computed by summing `Levels[*].NumFiles`;
   the raw metric is still reported as the additional field `backing_table_count`.
3. **`time.Now()` has ~1 ms granularity on this Windows host.** A probe of 200,000
   back-to-back `time.Now()` pairs produced 199,996 zero deltas (max 1.7 ms), which made
   per-operation latency samples 0 at p50/p95/p99. The measurement path now uses
   Windows `QueryPerformanceCounter` through `kernel32.dll` (`syscall.SyscallN`, CGO-free):
   frequency 10,000,000 (100 ns ticks), recorded as
   `latency_read_nanos.clock_source` / `clock_resolution_nanos`. `go:build`-tagged
   `clock_windows.go` / `clock_other.go` keep non-Windows builds/test runs working.
4. `pebble.NewCache(64<<20)` is used for a fixed DB block cache; the cache reference is
   released after the DB is closed.

## Hygiene / constraints

- Only `research/experiments/phase_i7/pebble_runner/` was added. The existing
  `pebble_probe/` directory was read, never modified.
- Root `go.mod` / `go.sum`, `research/experiments/data/`, `paper.*`, and
  `I7_WINDOWS_RESULTS/` were not touched. No vendoring; `GOMODCACHE` remains
  `C:\Users\ramst\go\pkg\mod`.
- `go mod tidy` was run inside the new module to mark Pebble as a direct dependency; it
  resolved Pebble's own (test-only) module dependencies into this module's `go.sum` only.
  Nothing was added to the repository module graph.
- The self-test spec, approval, platform manifest, envelope, and data directories were all
  under `%TEMP%` and were deleted after verification. The unit tests use `t.TempDir()` and
  leave nothing behind.
- `pebble_runner.exe` is left in the module directory as the build artifact produced by the
  verified build command (20.8 MB). Delete it if a source-only tree is preferred; it can be
  rebuilt byte-identically with the command above.
