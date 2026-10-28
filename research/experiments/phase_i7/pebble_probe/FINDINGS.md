# Phase I7 — Pebble Windows-Native Probe: Findings

**Probe location:** `research/experiments/phase_i7/pebble_probe/`
**Date:** 2026-10-28
**Host:** native Windows, `go1.26.3 windows/amd64`, `CGO_ENABLED=0`, no C compiler (no gcc), PowerShell 5.1/7

## Verdict

**(a) COMPATIBLE** — the pinned module resolves, builds with `CGO_ENABLED=0`, and the full
open/set/get/flush/close → reopen/get/close → `os.RemoveAll` smoke test passes (exit code 0,
`error` field empty).

## Exact pinned version resolved

`github.com/cockroachdb/pebble v1.1.5` — resolved exactly as requested. **No fallback was needed
and no other v1.x version was consulted.** Network was reachable; the module and its dependency
graph were downloaded successfully on the first `go get` attempt (one attempt only).

## Commands run and exit status

All commands were run with the working directory set to
`C:\Users\ramst\Documents\Projects\selfdbms\research\experiments\phase_i7\pebble_probe`
(quoted, since the path contains spaces) unless noted.

| # | Command | Exit code | Notes |
|---|---------|-----------|-------|
| 1 | `go mod init owndbms/pebble_probe` | **0** | created independent module; no root `go.mod` involvement |
| 2 | `go get github.com/cockroachdb/pebble@v1.1.5` | **0** | network reachable; `go: downloading github.com/cockroachdb/pebble v1.1.5` + 26 dependency modules; `go: added ... v1.1.5` |
| 3 | `go build ./...` | **0** | compiled cleanly with `CGO_ENABLED=0`; note: this wrote a 20 MB `pebble_probe.exe` into the probe dir, which was deleted afterwards |
| 4 | `go build -o "$env:TEMP\pebble_probe_bin.exe" .` | **0** | binary built outside the repo to keep the probe tree small |
| 5 | `& "$env:TEMP\pebble_probe_bin.exe"` | **0** | smoke test; single JSON object on stdout (captured below) |
| 6 | `go list -m all` | **0** | 127 lines → `probe_module_graph.txt` |
| 7 | `go version` | **0** | → `probe_environment.txt` |
| 8 | `go vet ./...` | **0** | no findings |
| 9 | `go list -deps .` | **0** | 357 packages; confirms no `runtime/cgo` in the linked graph |

### Environment (`probe_environment.txt`)

```
go version go1.26.3 windows/amd64

CGO_ENABLED=0
GOOS=windows
GOARCH=amd64
GOMODCACHE=C:\Users\ramst\go\pkg\mod
```

## Captured JSON (canonical run, exit code 0)

```json
{"pebble_version":"v1.1.5","go_version":"go1.26.3","goos":"windows","goarch":"amd64","open_ok":true,"set_ok":true,"get_ok":true,"flush_ok":true,"close_ok":true,"reopen_ok":true,"reget_ok":true,"files_after_close":["000005.sst","000006.log","CURRENT","LOCK","MANIFEST-000001","MANIFEST-000007","OPTIONS-000008"],"removeall_ok":true,"error":"","elapsed_ms":439}
```

Every `*_ok` field is `true`, `error` is the empty string, and the process exited 0
(the probe exits 1 if any check fails).

### Run history (honest record)

Two runs were made. The first run also exited 0 with all checks true, but its
`files_after_close` field was `["db/"]` because the listing was taken over the *parent* temp
directory rather than over the Pebble DB directory itself. `main.go` was corrected to list the
Pebble DB directory (`<temp>/pebble_probe_*/db`), rebuilt, and re-run; the JSON above is that
corrected run. Both runs passed every functional check; only the file-listing granularity
changed. Run 1 output, for the record:

```json
{"pebble_version":"v1.1.5","go_version":"go1.26.3","goos":"windows","goarch":"amd64","open_ok":true,"set_ok":true,"get_ok":true,"flush_ok":true,"close_ok":true,"reopen_ok":true,"reget_ok":true,"files_after_close":["db/"],"removeall_ok":true,"error":"","elapsed_ms":326}
```

### What the smoke test actually did

1. `os.MkdirTemp("", "pebble_probe_*")` — fresh directory under the system temp dir
   (`C:\Users\ramst\AppData\Local\Temp\...`), **not** under the repository.
2. `pebble.Open(<temp>/db, &pebble.Options{})`.
3. 200 × `Set` of keys `user_key_%010d` with deterministic 128-byte values using
   `&pebble.WriteOptions{Sync: true}` — i.e. 200 fsync'd writes on Windows.
4. `Get` of 5 keys (`indices 0, 1, 42, 137, 199`) with exact byte-for-byte value comparison.
5. `Flush()`.
6. `Close()`.
7. Reopen of the *same* directory (deliberately not deleted), `Get` of the same 5 keys again
   with exact value comparison, then `Close()`.
8. `os.ReadDir` over the DB directory → 7 entries: an SST, a WAL, `CURRENT`, `LOCK`, two
   `MANIFEST`s, and `OPTIONS`.
9. `os.RemoveAll(<temp dir>)` followed by an `os.Stat` existence check →
   `removeall_ok: true`. **This is the Windows file-handle check: no handle leaked at close,
   so the directory was fully removable immediately after close.**

The only stderr output was Pebble's informational WAL-replay log line on reopen
(`[JOB 1] WAL file ... stopped reading at offset: 0; replayed 0 keys in 0 batches`), which is
expected on a cleanly closed DB and is not an error.

## Compatibility notes

- **No CGO / no C compiler required.** The linked package graph contains `github.com/klauspost/compress/zstd`
  (pure Go) and `github.com/golang/snappy`, and contains **no `runtime/cgo`** and no
  `github.com/DataDog/zstd`. Pebble v1.1.5 therefore compiles and runs on native Windows with
  `CGO_ENABLED=0` and no gcc. (`DataDog/zstd` appears in `go.sum` as an indirect module for
  optional/test paths but is not linked into this probe.)
- **Windows file locking is a non-issue for the normal lifecycle.** `Close()` released every
  handle; a second `Open` on the same directory succeeded; `RemoveAll` then succeeded
  immediately.
- **Sync writes work.** All 200 `Sync: true` writes completed without error.

## Constraint compliance

- Root `go.mod` / `go.sum` were **not** modified: root `go.mod` is still
  `module owndbms` + `go 1.26.3` with only `go.etcd.io/bbolt` and `golang.org/x/sys`, and both
  files retain their 2026-08-01 timestamp. The probe is a fully separate module.
- No vendoring. `GOMODCACHE` is `C:\Users\ramst\go\pkg\mod`, **outside** the repo tree.
  `github.com/cockroachdb/pebble@v1.1.5` occupies **14.97 MB** there — under the 15 MB budget
  for the pinned module. Nothing from the dependency graph was added to the repository tree.
- Total bytes added to the repo by this probe: **28,671 bytes** (source and text artifacts only;
  21,515 of that without this findings file).
- No benchmarks were run. No packages were installed. No administrator rights, no system
  settings changed. Nothing outside `research/experiments/phase_i7/pebble_probe/` was written;
  `research/experiments/data/`, the frozen executables, and `paper.tex` were untouched.

## Full file listing of the probe directory (end of run)

```
research/experiments/phase_i7/pebble_probe/
├── FINDINGS.md                7,156 bytes   (this file)
├── go.mod                     1,462 bytes   module owndbms/pebble_probe, go 1.26.3
├── go.sum                     8,694 bytes
├── main.go                    6,147 bytes   the smoke test
├── probe_environment.txt        121 bytes   go version + GOOS/GOARCH/CGO_ENABLED/GOMODCACHE
└── probe_module_graph.txt     5,091 bytes   output of `go list -m all` (127 lines)
```

No binaries, no `vendor/`, no database directories, and no temp data remain in the probe
directory.
