# 13 — G1 Review Evidence (Windows-native)

**Gate:** G1 — A-min implementation and correctness, Windows NTFS behaviour, timing precision, race-detector determination.
**Status:** **COMPLETE.** No performance trial was executed.
**Date:** 2026-10-28
**Host:** Windows 11 Home 10.0.26200, AMD Ryzen 7 7445HS (6C/12T), 15.2 GiB RAM, Samsung MZVL8512HFLU-00BH1 NVMe, NTFS.
**Toolchain:** `go1.26.3 windows/amd64`, `CGO_ENABLED=0`, no C compiler.
**Base revision:** `f1a561a364b4e6719b3867400370a5b6228cba33` (working tree dirty; see §8).

Companion: [12_WINDOWS_EXECUTION_AMENDMENT.md](12_WINDOWS_EXECUTION_AMENDMENT.md), [01_A_MIN_DESIGN.md](01_A_MIN_DESIGN.md).

---

## 1. What was implemented

| File | Δ lines | Content |
| :--- | ---: | :--- |
| `lsmengine/sstableReader.go` | +53 | `leaseCount`, `retired`, `freeOnce`, `freeErr`; `lease()`, `releaseLease()`, `reclaim()` (close-before-unlink, 3×10 ms retry, error captured) |
| `lsmengine/manager.go` | +238/−1 | `SyncModeApproachAMin`; `getApproachAMin`; `leaseScratch` pool; `scanLeasedTables`; `reclaimIfDrained`; `drainActiveReaderLeases`; `recordCleanupErr`/`drainCleanupErrs`; mode dispatch; shutdown drain |
| `lsmengine/compaction.go` | +18/−1 | A-min retirement inside the publication lock (`retired=true` under `m.Mu`), reclamation after unlock; Approach A's original close/unlink path preserved verbatim in the `else` branch |
| `research/experiments/harness/runner.go` | +3 | accept `"ApproachAMin_Lease"` / `"AMIN"` in the sync-mode switch (additive only) |
| `lsmengine/approach_a_min_test.go` | new (456 lines) | 7 deterministic/empirical tests, T-02/T-03/T-07..T-13 |
| `research/experiments/phase_i7/cmd/i7trial/` | new (~700 lines) | per-trial process runner, approval gate, platform handshake, timer/schedule probes, Windows affinity readback |
| `research/experiments/phase_i7/run_i7.ps1` | new (~1290 lines) | campaign driver (`-Check`/`-Preflight`/`-Campaign`/`-Resume`/`-Package` + `-DryRun`) |

**Not changed:** `Transaction.Commit`, `LatestValue`, `flushToDisk`, `snapshot.go` (Approach B), `wal.go`, `sstableBuilder.go`, `memtable.go`, `keys.go`, the WAL and SSTable formats, and Approach A's `Get` body. `go vet ./...` is clean.

Single-variable audit: A → A-min differs only in (i) `m.Mu.RUnlock()` moving before the SSTable scan, (ii) the lease acquisition that makes that safe, (iii) retirement/reclamation bookkeeping, (iv) the shutdown drain. No change to visibility, tier order, error strings or the write path.

---

## 2. Correctness tests

```
go test ./lsmengine -run TestAMin -count=1 -v
```

| Test | T-ID | Asserts | Result |
| :--- | :--- | :--- | :--- |
| `TestAMin_ParityWithApproachA` | T-13 | 203-operation sequence (puts, deletes, reads, absent key, tombstone) produces **byte-identical** `(value, error)` sequences under A and A-min | **PASS** |
| `TestAMin_LeasesAreBalanced` | T-02/T-09 | after hits, misses, tombstones and multi-table scans, every published reader has `leaseCount == 0` | **PASS** |
| `TestAMin_CompactionDuringScan` | T-03/T-07 | reader pauses mid-scan holding 4 leases; compaction publishes and retires all 4 inputs; **descriptors still valid**, files still present while leased, value still correct; after release all 4 files are unlinked | **PASS** |
| `TestAMin_ShutdownDrainsLeases` | T-11/T-12 | `Close()` does not return while a lease is outstanding (200 ms window), returns `nil` after release; in-flight read completes normally | **PASS** |
| `TestAMin_NoOrphanTablesUnderChurn` | T-08 | 1.2 s of 4 readers + 1 writer + compaction every 40 ms with a 16 KiB memtable; after drain, `.sst` files on disk **== published readers** (no orphans, no leaks) | **PASS** |
| `TestAMin_NoExtraAllocations` | T-10/M-7 | `testing.AllocsPerRun(200)` on the disk read path: **A = 109.00, A-min = 109.00 allocs/read** | **PASS** |
| `TestAMin_ConcurrentCloseWithReaders` | T-11 | `Close()` concurrent with 6 looping readers completes without deadlock or panic | **PASS** |

Full-package context:

```
go test ./lsmengine -count=1      -> ok   owndbms/lsmengine   15.123s
go test -run 'TestAMin_NoOrphan|TestAMin_ConcurrentClose|TestAMin_CompactionDuringScan' -count=5
                                   -> ok   (10.094s, 15 executions)
go test ./... -count=1             -> ok   9 packages, 0 failures (65.8s)
go vet ./...                       -> clean
```

The 109 == 109 allocation result required two fixes during G1, both recorded because they are exactly the kind of thing M-7 exists to catch:

1. a `defer func(){...}()` closure in the read path escaped and cost **+1 alloc/read**; replaced with a single explicit release path;
2. `sync.Pool.Put(buf[:0])` boxes a slice header into `any`, costing **+1 alloc/read**; the pool now holds `*leaseScratch`.

---

## 3. Race detector: unavailable, and not claimed

Both attempts, verbatim:

```
$ CGO_ENABLED=0 go test -race ./lsmengine
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1

$ CGO_ENABLED=1 go test -race ./lsmengine
# runtime/cgo
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

No C compiler exists on the host, and installing one is a system modification outside this protocol. **I7 therefore makes no claim that the race detector ran on Windows.**

Substitute evidence, all on the target platform:

| Substitute | Coverage |
| :--- | :--- |
| Deterministic interleaving tests via engine hooks (`duringPointReadHook`, `beforeCompactionPublishHook`) | Forces the exact reader/compaction and reader/shutdown interleavings the lease protocol exists to make safe |
| `-count=5` repetition of the three concurrency tests | 15 executions, 0 failures |
| Invariant checker (`leaseCount == 0`, `files == published readers`, directory removable) | Detects the failure modes that a race would produce (leak, orphan, use-after-close) |
| Full suite (98 pre-existing tests + 7 new) | No regression in A or B behaviour |

This is weaker than `-race` and is reported as such in the amendment (§4.3). The manuscript's existing race-detector statement refers to the WSL2/Linux campaign and must not be transferred here.

---

## 4. Windows NTFS validation

### 4.1 Functional smokes through the production harness path

`i7trial -smoke -mode {A,AMIN,B}` — 2 tables × 40 keys, 2 readers, 1 writer, 400 ms window, WAL sync on, data directory under `%TEMP%`:

| Arm | reads | writes | p50 | status | data dir removable after `Close` |
| :--- | ---: | ---: | ---: | :--- | :--- |
| A | 830 | 202 | 872.2 µs | SUCCESS | yes |
| A-min | 1,034 | 304 | 570.9 µs | SUCCESS | yes |
| B | 1,189 | 347 | 444.5 µs | SUCCESS | yes |

**These numbers are engineering validation on an 80-key fixture and must not be reported as a finding.** Their only purpose is to prove that all three arms start, read, write, flush, drain and release handles on NTFS.

### 4.2 Handle-lifetime evidence

- `TestAMin_CompactionDuringScan` runs on NTFS and asserts, *while a reader is paused mid-scan*: the retired descriptor answers `File.Stat()`, all four retired files still exist, and the read returns the correct value; after release all four are unlinked.
- `TestAMin_NoOrphanTablesUnderChurn` asserts `len(.sst files) == len(ActiveSSreaders)` after churn, which fails if a retired table is leaked or a live table is unlinked.
- Every smoke and every trial ends with `os.RemoveAll(dataDir)`; failure is recorded as `data_dir_removed = false` and turns the trial `FAILED`.

### 4.3 Correction to the Phase I7.0 text

[01](01_A_MIN_DESIGN.md) §3 originally claimed that Windows unlink of an open file raises `ERROR_SHARING_VIOLATION`. **That is wrong for Go's own handles**: `os.Open` requests `FILE_SHARE_DELETE`, so deletion can succeed while a descriptor is open (delete-pending). The real hazard is silent descriptor reuse after `Close`, not a loud sharing violation. The design was already correct (close-before-unlink, lease-gated), and the text is corrected in place.

---

## 5. Timing validation

`i7trial -selftest` on the target host:

| Quantity | Measured | Consequence |
| :--- | :--- | :--- |
| QPC frequency | 10,000,000 Hz | 100 ns tick |
| QPC minimum positive step | **100 ns** | adequate for every latency the engine produces |
| `time.Now` distinct values in 50.1 ms | 67 | ~1,337 Hz |
| `time.Now` minimum positive step | **404,400 ns** | `time.Now` must not be used for latency or scheduling |
| `time.Sleep(1 ms)` mean / max abs error | **1.50–1.54 ms / 0.948 ms** | coarse sleep; the final ~1.5 ms must be spun |
| Open-loop wakeup, 5 ms interval, 200 events, **5 runs** | p50 **0.3–0.5 µs**; p95 10–39 µs (4 runs) / 2.9 ms (1 run); p99 **170–387 µs** (4 runs) / **30.5 ms** (1 run); max **311–691 µs** / **40.5 ms** | ms-scale pacing is feasible; multi-ms scheduler stalls occur ~1 run in 5 and must be recorded, not ignored |

The five-run spread matters: an earlier single run reported p99 65.7 µs, and a later single run reported 254.5 µs, so quoting one run would have overstated the host's determinism. The full table is in `evidence/selftest_runs.json`. Per-request lateness is now a required E4 output, and a trial with >5 ms maximum lateness is flagged `SCHED_JITTER` ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §4.2).

`run_i7.ps1 -Check` records these numbers in `platform_manifest.json` for every campaign, so a reviewer can see the clock quality that produced the data.

**Decision carried forward:** the open-loop driver (H-05) and event timestamps (H-06) must use QPC only. Any implementation that uses `time.Now` for scheduling is rejected at review.

---

## 6. Runner and automation validation

`run_i7.ps1 -Check` on the target host (abridged, verbatim):

```
[ OK ] host: LAPTOP-6O160KFN / Microsoft Windows 11 Home Single Language (build 26200)
[ OK ] cpu: AMD Ryzen 7 7445HS w/ Radeon 740M Graphics | 6 physical / 12 logical
[ OK ] ram: 15.2 GiB
[ OK ] filesystem: NTFS on SAMSUNG MZVL8512HFLU-00BH1 (NVMe)
[ OK ] results root resides on an NVMe device
[ OK ] free space: 104.8 GiB
[ OK ] go: go version go1.26.3 windows/amd64
[WARN] race detector NOT available (requires cgo + a C compiler)
[ OK ] frozen artifact hashes recorded
[ OK ] storage probe: 526.9 MB/s buffered write, flush(true) in 60.7 ms
[ OK ] runner built: ...\I7_WINDOWS_RESULTS\bin\i7trial.exe
[ OK ] runner self-test: qpc=10000000 Hz, qpc step=100 ns, time.Now step=404400 ns
[ OK ] open-loop wakeup error (5 ms target): p50=0.2 us, p99=65.7 us, max=552.7 us
[WARN] git working tree is dirty; -Campaign will refuse until it is clean
Check PASSED. No trial was executed.
```

Runlist generation and dry runs:

| Command | Result |
| :--- | :--- |
| `-DryRun -Campaign` | 366 rows written; `E1=72 E2=60 E3=96 E4=54 E5=18 E7=24 PF=12 D1=9 D2=15`; sha256 `09babe88…de19`; **144 ready / 186 blocked**; estimate 1.1 h for the ready set |
| `-DryRun -Preflight` | 12 rows selected, 12 ready, 0 blocked |
| `-Campaign` (no approval) | refused: `git working tree is dirty; commit before a confirmatory campaign` |
| `-Preflight` (no approval) | refused: `approval file not found … Create it only after external review authorizes gate G3.` |
| `-Preflight` with a `G4` approval | refused: `approval is for gate G4, this mode requires G3` |
| `-Package` | 161 files + `checksums.sha256` → ZIP, printed path and SHA-256 |

Process mechanics validated independently: `.NET Process` with `ReadToEndAsync` on both streams captures exit code **0** for a successful run and returns **−1** after a timeout kill at 1.2 s.

**Nothing above launched a performance trial.** The only engine executions were the three 400 ms smokes of §4.1.

---

## 7. Pebble Windows probe

**Verdict: COMPATIBLE** — `github.com/cockroachdb/pebble v1.1.5`, `CGO_ENABLED=0`:

```json
{"pebble_version":"v1.1.5","go_version":"go1.26.3","goos":"windows","goarch":"amd64",
 "open_ok":true,"set_ok":true,"get_ok":true,"flush_ok":true,"close_ok":true,
 "reopen_ok":true,"reget_ok":true,"removeall_ok":true,"error":"","elapsed_ms":439,
 "files_after_close":["000005.sst","000006.log","CURRENT","LOCK","MANIFEST-000001","MANIFEST-000007","OPTIONS-000008"]}
```

200 `Sync:true` writes, byte-exact reads, flush, close, reopen with byte-exact re-reads, close, and immediate `RemoveAll` — no leaked handles. The module graph contains no `runtime/cgo`. Full transcript and caveats: [pebble_probe/FINDINGS.md](pebble_probe/FINDINGS.md).

Pebble remains a **descriptive** anchor only (12 §6, 02 §4).

---

## 8. Frozen-artifact integrity

Hashed read-only; nothing was written to these paths.

| Artifact | SHA-256 |
| :--- | :--- |
| `experiment_runner.exe` | `0be4d0a775811cddcb444a9f347ae4c3ed21fc1ed8fe0191b7c2245846f1009e` |
| `experiment_runner_linux` | `24e5b30fc8f75785c7b41d6712a351fe66f38cb8560939146045a18a70b26143` |
| `campaign_wsl2_ext4_restart_v1/manifest_checksums.json` | `debce63aef5dd07fac344974a4e4bc334447c89e18dd428948479f828a5802e6` |
| `targeted_validation_v1/manifest_checksums.json` | `21005ded5f9ec1b208861a31dffbc14a405bd574ee867cc5bb95cc2ba9427468` |

The working tree is dirty (`paper.tex` modified; `paper/`, `research/reviews/` untracked) from the previous phase. **`-Campaign` refuses to run until those are committed**, which is the intended behaviour; G1/G2 validation is unaffected.

I7's own artefacts live only under `research/experiments/phase_i7/` (`I7_WINDOWS_RESULTS/`, `evidence/`, `pebble_probe/`, `cmd/`).

---

## 9. Reviewer checklist

| # | Question | Answer |
| :--- | :--- | :--- |
| 1 | Does A-min release the read lock before the scan? | Yes — `m.Mu.RUnlock()` precedes `scanLeasedTables` |
| 2 | Can a reader observe a closed descriptor? | No — lease before unlock, reclaim after last release (proof in 01 §8.1; test T-03) |
| 3 | Are leases balanced on every path, including errors? | Yes — single explicit release path; T-02/T-09; 0 outstanding after every test |
| 4 | Does shutdown wait for readers? | Yes — `drainActiveReaderLeases(5 s)` before the final close; T-11/T-12 |
| 5 | Is A-min observationally identical to A? | Yes for 203 operations including tombstones and misses (T-13); no other behaviour changed |
| 6 | Does A-min add allocations? | No — 109 vs 109 allocs/read |
| 7 | Does it work on NTFS (delete/close ordering)? | Yes — T-03/T-08 on NTFS; smokes for all three arms |
| 8 | Was `-race` run? | **No** — cgo/C compiler unavailable; substitutes listed, claim withheld |
| 9 | Is Windows timing adequate? | Yes for latency (QPC 100 ns) and for ms-scale pacing (p99 66 µs); `time.Now` is **not** adequate and is not used |
| 10 | Is Pebble usable on Windows? | Yes — v1.1.5, CGO-free, full lifecycle + deletable directory |
| 11 | Is anything frozen modified? | No — hashes recorded, no writes |
| 12 | Was any performance trial run? | **No** — only 400 ms functional smokes |

---

## 10. Reproduction

```powershell
cd C:\Users\ramst\Documents\Projects\selfdbms
$env:CGO_ENABLED='0'

go vet ./...
go test ./lsmengine -run TestAMin -count=1 -v
go test ./... -count=1

go build -trimpath -o .\i7trial.exe .\research\experiments\phase_i7\cmd\i7trial
.\i7trial.exe -selftest
.\i7trial.exe -smoke -mode A    -out .\smoke_A.json
.\i7trial.exe -smoke -mode AMIN -out .\smoke_AMIN.json
.\i7trial.exe -smoke -mode B    -out .\smoke_B.json

.\research\experiments\phase_i7\run_i7.ps1 -Check
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Preflight
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Campaign
```

---

## 11. Residual gaps at G1 close

| Gap | Owner gate |
| :--- | :--- |
| Race detector unavailable on Windows | Permanent platform limitation; stated in every document |
| E2/E4/E3-equal/E7/D1/D2 drivers missing | G2 ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §11) |
| Per-worker recorders (remove two global mutexes from the read loop) | G2 / H-02, H-03 |
| Counter-based key stream (per-operation pairing) | G2 / H-11 |
| Fixture cost measured only from the archived campaign, not from this runner | G3 preflight will measure it |
| Physical-core (non-SMT) affinity mask unverified | Operator-supplied mask; recorded as such |
