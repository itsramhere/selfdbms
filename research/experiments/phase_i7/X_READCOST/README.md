# X_READCOST: local evidence for the X-readcost-v1 campaign (git-ignored)

Everything in this folder is ignored by git (`.gitignore`). Final results will be added explicitly later.

| Path | What it is |
|---|---|
| `smoke_20261030/` | 21-trial mechanical smoke run (first 21 plan rows, dirty tree, `-allow-dirty`). **Excluded from all analyses** (see `X_PREREGISTRATION.md` §9). Plan SHA-256 `68ad96f8…8136`, runner SHA-256 `cbd7b256…adac`. |
| `probes/readpath_timing/` | Throwaway Go module (`go.mod` uses a `replace` to the repo root) that times single-goroutine point reads on a G4-shaped fixture. Results are below. |
| `codex_briefs/` | The exact briefs and follow-ups given to Codex for the engine and harness changes (provenance). Codex run logs are in `~/.claude/codex-runs/`. |
| `campaign/` | (created by the real campaign) xcampaign output: plan, ledger, specs, per-trial JSON. |

## Aborted first campaign start (2026-10-31)

The real campaign was started from `afdfb67` (runner `cb5c3d33…7948`, plan `68ad96f8…8136`).
The operator stopped it after 38 of 519 trials (all SUCCESS; trial 39 was interrupted)
because continuous mains power could not be guaranteed. Per the operator's instruction, all partial
output was deleted and none of it was looked at for analysis. A §9 deviation entry for
`X_PREREGISTRATION.md` should record this when the campaign is re-run.

## Read-path timing probe (2026-10-30, Windows 11, NTFS, AMD Ryzen 7 7445HS)

Fixture: 4 SSTables × 2,500 keys (disjoint ranges, 128 B values), Approach A, single goroutine.

**Before the fix**, at revision 4a9a798, using the only read path that existed then (now `legacy_scan`); 20 Gets per row:

| Key location | ms per Get |
|---|---|
| SSTable 0 (oldest) | 156.38 |
| SSTable 1 | 97.33 |
| SSTable 2 | 49.17 |
| SSTable 3 (newest) | 0.29 |
| above keyspace (G4 E5 "all-miss") | 0.29 |

**After the fix** (block path; 200 Gets per row, legacy 20):

| Read path / injected delay | µs per Get (SSTable 0 / 1 / 2 / 3) |
|---|---|
| block, d = 0 | 10.5 / 8.1 / 11.4 / 7.8 |
| block, d = 20 µs | 44.6 / 34.8 / 32.1 / 32.2 |
| block, d = 200 µs | 234.5 / 225.4 / 204.7 / 205.5 |
| block, d = 2 ms | 2265.6 / 2275.4 / 2227.9 / 2249.1 |
| legacy_scan | 109,858.6 / 71,499.7 / 35,697.0 / 217.8 |

Before the QPC fix, `injectReadDelay` spun on `time.Now()`, which has about 1 ms granularity on Windows.
That measured 1,470–1,940 µs at 20 µs and 1,400–1,870 µs at 200 µs.

## Verification runs (2026-10-30)

- Windows: `go test ./lsmengine ./research/experiments/phase_i7/... -count=1 -short`: all packages pass.
- WSL2 (Ubuntu 24.04, Go 1.27.3, gcc 13.3), tree copied to the Linux filesystem:
  `CGO_ENABLED=1 go test -race -short -count=1 ./lsmengine ./research/experiments/phase_i7/harness2 ./research/experiments/phase_i7/cmd/xcampaign`: all `ok`.
- G4 reproduction: `reconcile_r1_2a.py` on `I7_WINDOWS_RESULTS.zip` (SHA-256 `52c52ad1…723b`) regenerated
  `research/experiments/analysis/r1_2a_sensitivity_results.json` byte-identically.
