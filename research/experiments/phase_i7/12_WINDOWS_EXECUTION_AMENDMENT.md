# 12 — Windows-Native Execution Amendment

**Status:** AMENDMENT TO PHASE I7.0. Design + G1/G2 implementation. **No performance trial has been run.**
**Applies to:** `I7_STRENGTHENING_V1`, protocol version **1.2.0-windows** (was 1.1.0 / bare-metal Linux).
**Supersedes:** the platform assumptions of [05_PLATFORM_VERIFICATION.md](05_PLATFORM_VERIFICATION.md) and the cost figures of [06_TRIAL_INVENTORY.md](06_TRIAL_INVENTORY.md) §9. Everything else in Phase I7.0 stands.
**Evidence:** [13_G1_REVIEW_EVIDENCE.md](13_G1_REVIEW_EVIDENCE.md).

---

## 1. Summary

The operator has **no dedicated bare-metal Linux machine**. All I7 execution therefore runs **natively on Windows 11 Home** with PowerShell, the Windows Go toolchain, an internal Samsung NVMe SSD and NTFS. No Ubuntu, WSL2, Bash, POSIX utilities or Linux filesystem is required or used.

This is a **different experiment** from the one described in Phase I7.0 §05. It keeps the same research questions, the same three-arm ladder, the same preregistered inventory (366 planned / 384 ceiling), the same metrics and the same analysis plan — but it measures a different platform, and its conclusions are scoped to that platform.

> **Scope statement (must appear verbatim in the manuscript):**
> "All I7 measurements were collected on a single Windows 11 Home host with an AMD Ryzen 7 7445HS (6 cores / 12 threads), 16 GiB RAM, an internal Samsung MZVL8512HFLU-00BH1 NVMe SSD formatted NTFS, using the Go 1.26.3 windows/amd64 toolchain with CGO disabled. Results are not measurements of bare-metal Linux, of WSL2, or of a data-centre NVMe device, and no claim in this paper should be read as validating those platforms."

### 1.1 What this amendment does not claim

- It does **not** validate bare-metal Linux behaviour, WSL2 behaviour, ext4/XFS behaviour, or enterprise-NVMe queueing behaviour.
- It does **not** reproduce the frozen WSL2/ext4 campaigns. Those remain frozen, unmodified and unrepeated.
- It does **not** turn the study into a cross-platform comparison. There is exactly one I7 platform.
- It does **not** claim that the engine's "disk tier" touches the device during a trial; §5.6 explains why it usually does not.

---

## 2. Measured environment (from `platform_manifest.json`)

| Item | Value |
| :--- | :--- |
| Host | `LAPTOP-6O160KFN` |
| OS | Microsoft Windows 11 Home Single Language, 10.0.26200 (build 26200), 64-bit |
| Shell | Windows PowerShell 5.1.26100.9711 (PowerShell 7 not required) |
| CPU | AMD Ryzen 7 7445HS w/ Radeon 740M Graphics — **6 physical / 12 logical** |
| RAM | 16,357,158,912 B (15.2 GiB) |
| Data volume | `C:` NTFS, 510.8 GB, **104.9 GiB free** |
| Device | SAMSUNG MZVL8512HFLU-00BH1, serial `0025_38F6_51B1_6592.`, **NVMe**, SSD |
| Go | `go1.26.3 windows/amd64`, `GOOS=windows`, `GOARCH=amd64`, `CGO_ENABLED=0`, `GOAMD64=v1` |
| Compiler for cgo | **none** (`gcc`/`clang`/`cc` absent) |
| Race detector | **unavailable** (§4.3) |
| Power scheme | Balanced (recorded, **not changed**) |
| Defender real-time | enabled (recorded, **not disabled**) |
| Storage probe | 32 MiB buffered write + `Flush(true)`: **526.9 MB/s**, flush 60.7 ms |
| QPC | 10,000,000 Hz, minimum step **100 ns** |
| `time.Now` | ≈1,337 Hz effective, minimum step **404 µs** |
| `time.Sleep(1 ms)` | mean **1.536 ms**, max abs error **0.948 ms** |

---

## 3. Errata against Phase I7.0

| # | Phase I7.0 said | Amendment | Reason |
| :--- | :--- | :--- | :--- |
| E-1 | Target platform: bare-metal Linux + NVMe (05) | Windows 11 + NTFS on internal NVMe | No Linux host exists |
| E-2 | Storage verification via `fio`, `/sys/block`, `nvme-cli`, `findmnt` (05) | Windows-native: `Get-Volume`/`Get-Partition`/`Get-Disk`/`Get-PhysicalDisk`, bounded write+`Flush(true)` probe | Those tools do not exist on Windows and require root |
| E-3 | Cold-read condition via `posix_fadvise(DONTNEED)` (OD-6) | **Dropped.** No equivalent that does not require `O_DIRECT` or admin cache clearing | §5.6 |
| E-4 | Open-loop scheduler driven by `time.Now`/`time.Until` (04) | **Mandatory QPC clock**; `time.Now` has ~0.4 ms steps | Measured (§5.2) |
| E-5 | Cost: ≈5.9 h at 58 s/trial (06 §9) | ≈3.4 h at 27 s/trial (13.2 s fixture measured on NTFS) | Measured archived NTFS fixtures |
| E-6 | Affinity via `taskset`, `GOMAXPROCS` env (05 §4) | `Process.ProcessorAffinity` on the launcher; children inherit; `GOMAXPROCS` = popcount(mask) | No `taskset`; no admin needed |
| E-7 | Pebble "must build on the target platform" (02 §5) | **Verified:** Pebble v1.1.5 builds CGO-free on windows/amd64 and passes an open/write/get/flush/close/reopen/delete smoke test | §6 |
| E-8 | A-min not implemented; G1 pending (01) | **A-min implemented and validated**; see 13 | This turn's authorization |
| E-9 | Runner: reuse `harness` + a new `harness2` (08) | Additive only: one `case` in `harness.RunTrial` + new `cmd/i7trial`; no rewrite | Protects frozen-binary provenance |
| E-10 | Preflight = 4 triplets incl. E2/E4 (06) | Preflight = 4 triplets from **runner-supported** blocks (E1 r8, E1 r1, E3-free r4, E5); E2/E4 join preflight only when their drivers land | The runners for E2/E4 do not exist yet |
| E-11 | "Reader holds `RLock` across blocking POSIX `pread`" (paper; 00 AF-1) | On Windows the working set is page-cache resident; the critical section is CPU-bound scanning. Phrase as "across the tiered lookup", not "across disk I/O" | Correctness of the mechanism claim |
| E-12 | Windows unlink of an open file fails with `ERROR_SHARING_VIOLATION` (01 §3) | **Correction:** Go opens files with `FILE_SHARE_DELETE`, so unlink can succeed while a descriptor is open. The A-min failure mode is therefore *silent* fd reuse, not a loud sharing violation. | Measured Go `os` semantics |

Errata E-12 is the only place where the earlier design text was **wrong**, not merely out of scope. It is corrected in [01_A_MIN_DESIGN.md](01_A_MIN_DESIGN.md) §3.

---

## 4. Windows-specific semantics

### 4.1 NTFS handle lifetime and file deletion

Three facts govern reader reclamation on NTFS:

1. **`os.Open` requests `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE`.** Consequently `DeleteFile`/`os.Remove` on a path whose handle is still open **may succeed**, leaving the name gone while the data survives until the last handle closes. The naive "Windows refuses to delete open files" belief is wrong for Go's own handles; it *is* true for third-party handles (Defender, Search indexer, backup agents), which can make `os.Remove` fail intermittently.
2. **`Close` on a descriptor that another goroutine is using is still a race**, and the OS may reuse the descriptor number for an unrelated file. This, not the sharing violation, is why A-min needs a lease.
3. **Delete-pending state is observable**: a path can disappear while a reader is still reading through its handle, so "the file is gone" is not evidence that a reader finished.

Design consequences already implemented:

| Requirement | Implementation |
| :--- | :--- |
| A reader never touches a closed descriptor | Per-table `leaseCount` taken under `m.Mu.RLock` before unlocking; reclamation waits for the last release ([01](01_A_MIN_DESIGN.md) §6) |
| Close before unlink | `SSTableReader.reclaim` closes, checks the error, then unlinks |
| Unlink retries | 3 attempts × 10 ms, then the error is recorded and surfaced by `Manager.Close` |
| No descriptor is closed at shutdown while leased | `drainActiveReaderLeases(5 s)` before the final close |
| Tests prove the ordering | `TestAMin_CompactionDuringScan` asserts the retired file still exists *and its descriptor still works* while leased, and is gone after release (runs on NTFS) |

**Not changed:** the WAL and SSTable formats, `Close` semantics for valid tables (files are preserved, never unlinked), and Approach B's `SharedReader` cleanup path.

### 4.2 Timer precision

| Clock | Resolution | Use |
| :--- | :--- | :--- |
| `QueryPerformanceCounter` | 10 MHz, **100 ns** step | **All operation latency**, measurement windows, open-loop scheduling |
| `time.Now` (monotonic) | ~1,337 Hz, **404–503 µs** step | Log timestamps, wall-clock bounds only |
| `time.Sleep` | mean 1.54 ms for a 1 ms request; max error 0.95 ms | Coarse waits only; the last ~1.5 ms must be spun |

The frozen harness already measures latency with QPC (`timer_windows.go`), so **the historical Windows measurements were not affected** by the coarse `time.Now`. The amendment makes this an explicit requirement for all new code:

- latency: `harness.NowPrecision` / `SincePrecisionNanos` only;
- open-loop schedule: absolute targets compared against QPC elapsed, spin the final 1.5 ms;
- measurement window: `time.Sleep` is acceptable because a ±1 ms error on a 10 s window is 0.01 %.

**Validated wakeup accuracy** (200 wakeups at 5 ms spacing, QPC clock, **five runs**): p50 0.3–0.5 µs; p95 10–39 µs in four runs and 2.9 ms in one; p99 **170–387 µs** in four runs and **30.5 ms** in one; max **311–691 µs** in four runs and **40.5 ms** in the outlier. The host is a laptop with real-time scanning enabled, so multi-millisecond scheduler stalls genuinely occur (~1 in 5 runs).

Consequences, all of which are now requirements rather than cautions:

1. these events are ≈4 orders of magnitude below `W_max = 250 ms`, so jitter never causes abandonment;
2. the E4 driver must record per-request lateness (`a_j − t_j`) and report its maximum; a trial whose maximum lateness exceeds **5 ms** is flagged `SCHED_JITTER`;
3. a flagged trial is reported with its lateness distribution and is not silently pooled with clean trials;
4. the same applies to any stall overlapping a publication event, which is why [04](04_OFFERED_LOAD_METHODOLOGY.md) reports queueing delay separately from service time.

This is sufficient for the E4 open-loop design (millisecond-scale pacing) but it is *not* a real-time guarantee, and the manuscript must not treat arrival times as exact.

### 4.3 Race detector

`go test -race` **cannot run on this host**:

```
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
```

No C compiler is installed, and installing one would be a system change outside this protocol. **Therefore I7 makes no claim that the race detector was run on Windows.** G1 evidence substitutes (13 §4):

- deterministic interleaving tests using the engine's barrier hooks;
- `-count=5` repetition stress of the concurrency tests;
- an invariant checker (lease balance, published-reader count vs `.sst` files on disk, removable data directory);
- an observational-equivalence oracle against the frozen Approach A code path.

The frozen manuscript's race-detector claim refers to the **WSL2/Linux** campaign and must not be transferred to Windows.

### 4.4 Process scheduling and affinity

| Control | Mechanism | Privilege |
| :--- | :--- | :--- |
| Process affinity | `[System.Diagnostics.Process]::GetCurrentProcess().ProcessorAffinity = mask` **on the launcher**; every trial process inherits it | none |
| Verification | `i7trial -selftest` reports its own mask; `run_i7.ps1` compares requested vs actual and fails on mismatch | none |
| `GOMAXPROCS` | set to `popcount(mask)` in the environment before launch | none |
| Requested / actual recording | `platform_manifest.json` fields `affinity_requested`, `affinity_actual`, and per-trial `runtime.affinity_mask` | none |

Default is **all 12 logical CPUs (0xfff)**, which matches `GOMAXPROCS=12` used by the archived Windows campaign and keeps comparability with the only Windows data that exists. `-Affinity 0x3f` selects the low 6 logical CPUs for a physical-core-style run; **the mapping from logical CPU to physical core is not verified by this tooling** (that needs privileged topology APIs), so a physical-only run is recorded as an operator-supplied mask and must be described as such. No `isolcpus`, `cset`, cgroup cpuset, or priority class changes are used.

### 4.5 Antivirus and indexing

Defender real-time protection is **enabled and recorded**; disabling it, adding exclusions, or stopping services would be a system modification and is prohibited by this protocol. The risk is concrete: a scan can inject tens of milliseconds into an `fsync` or an `open`, which would appear as a tail event.

Mitigations:

- the results root and trial data directories live under the repository, not under `%TEMP%` scanning hotspots (still scanned, but deterministically so);
- every trial records `read_bytes`-equivalent proxies it can obtain and the wall-clock vs measured-window gap; a trial whose wall clock exceeds `measurement + warmup + 60 s` is excluded as `X-12`;
- `failure_analysis` (13 §7) reports the distribution of per-trial overhead, so a Defender-induced tail is visible rather than silently pooled;
- the manuscript reports Defender state and does not claim it was excluded from the measurements.

### 4.6 Thermal and power

A laptop cannot be thermally pinned. The protocol therefore:

- records the active power scheme and the CPU clock at `-Check` (Balanced, MaxClockSpeed vs CurrentClockSpeed);
- records `MHZ` before and after each block where CIM exposes it;
- keeps the pre-registered **drift triplets** (D3 start/end) and the **S-6** stopping rule (an arm-independent >15 % throughput change aborts);
- never changes the power plan, never disables C-states, never pins frequency.

### 4.7 File cache and the "disk" question

Windows has no `posix_fadvise`, and clearing the standby list requires admin tooling. **OD-6 (cold-per-trial reads) is therefore dropped for I7.** All I7 trials are warm-cache by construction: the fixture writes ≈1.5 MB of SSTables and the working set stays in the file cache.

This is not a defect to hide; it is the same situation as the frozen campaigns, and it is why [00](00_README.md) AF-1 insists that the critical section is CPU-bound scanning rather than device I/O. The manuscript must say: *"the measured read path is served from the Windows file cache; the experiment measures synchronization around a CPU-bound tiered lookup, not device latency."*

The one storage-side number the campaign does measure is the 32 MiB `Flush(true)` probe (526.9 MB/s, 60.7 ms), which bounds how fast a durable write can complete and is recorded once per platform, not per trial.

### 4.8 Durability caveat for `fsync`

`File.Sync` on Windows issues `FlushFileBuffers`, which flushes the volume cache to the device. Whether the device then commits to media before acknowledging depends on its volatile write cache and FUA behaviour; consumer NVMe drives frequently acknowledge a flush without a media commit. This affects the **epoch** interpretation of durability, not the ordering guarantees, and applies equally to our engine and to Pebble with `Sync:true`. I7 reports "successful commit ⇒ WAL record handed to the OS with a flush request issued", not "committed to media".

### 4.9 Paths, spaces and encoding

- All paths are built with `Join-Path`/`[System.IO.Path]`, quoted with an explicit `Quote-Arg` helper, and passed to the runner as a single argument string. Verified to work with a results root containing spaces ([13](13_G1_REVIEW_EVIDENCE.md) §6).
- `run_i7.ps1` is written as **UTF-8 without BOM and contains only ASCII**, because Windows PowerShell 5.1 decodes BOM-less scripts as ANSI. Non-ASCII characters in JSON output are written with an explicit BOM-less UTF-8 encoder.
- JSON is written atomically (temp file + rename) so an interrupted trial cannot leave a half-written record.

---

## 5. Revised study structure

The inventory is unchanged in size (366 planned, 384 ceiling). What changes is **which blocks the current runner can execute**:

| Block | Trials | Runner support | Blocker |
| :--- | ---: | :--- | :--- |
| E1 starvation (r=1,8) | 72 | **ready** | — |
| E3 churn, free-work (r=1,4) | 48 | **ready** | — |
| E3 churn, equal-work (r=1,4) | 48 | blocked | paced writer (H-08) |
| E5 all-miss dose-response | 18 | **ready** | — |
| D3 drift triplets | 6 | **ready** | — |
| E2 publication tail | 60 | blocked | publication-only driver (H-07) |
| E4 open-loop load | 54 | blocked | open-loop scheduler (H-05) + event conditioning (H-06) |
| E7 Pebble anchor | 24 | blocked | Pebble runner (H-22) |
| PF preflight | 12 | **ready** | — |
| D1/D2 diagnostics | 24 | blocked | profiling/instrumented builds (H-10) |

**Runnable today: 144 confirmatory + 12 preflight = 156 of 366 rows.**

`run_i7.ps1` refuses `-Campaign` and `-Preflight` while any selected row is blocked, unless `-AllowPartial` is passed; a partial run is marked `partial: true` in the campaign manifest and may not be reported as the preregistered confirmatory campaign.

**Consequence for the paper:** the A/A-min/B comparisons in E1, E3-free and E5 are already executable end-to-end on the target platform. E2 (publication tail) and E4 (offered load) remain blocked on harness work and are the two blocks that carry the reviewer's measurement-methodology asks; they must be completed before the campaign is claimed complete.

---

## 6. Pebble on Windows

**Verdict: COMPATIBLE.** `github.com/cockroachdb/pebble v1.1.5` was resolved from the network, built with `CGO_ENABLED=0` and the stock Windows toolchain, and passed an isolated functional smoke test:

```
open_ok  set_ok(200 sync writes)  get_ok(byte-exact)  flush_ok  close_ok
reopen_ok  reget_ok(byte-exact)  close_ok  removeall_ok(immediately)  elapsed_ms=439
```

`files_after_close` listed `000005.sst`, `000006.log`, `CURRENT`, `LOCK`, `MANIFEST-000001`, `MANIFEST-000007`, `OPTIONS-000008` — i.e. a normal Pebble directory with no orphaned handles, and the directory was deletable immediately after `Close()`. The dependency graph contains `runtime/cgo` nowhere, so the CGO-free build is genuine. Details: [pebble_probe/FINDINGS.md](pebble_probe/FINDINGS.md).

Residual Windows risk is **not** buildability but concurrent multi-process access and third-party handles on the DB directory. The campaign uses one process and one directory per trial, so that risk is out of scope; if a reviewer asks for a two-process test, that is a separate probe.

Pebble stays **descriptive** ([02](02_COMPATIBILITY_MATRIX.md) §4): no ratio-based claim, no durability comparison, no causal use.

---

## 7. Gates and current status

| Gate | Deliverable | Status |
| :--- | :--- | :--- |
| **G1** | A-min implementation, correctness tests, NTFS validation, timing validation, race-detector determination | **COMPLETE** — evidence in [13](13_G1_REVIEW_EVIDENCE.md) |
| **G2** | Windows-native harness + PowerShell automation | **PARTIAL** — `run_i7.ps1` validated (`-Check`, `-DryRun`, gate refusals, packaging); `cmd/i7trial` runs closed-loop trials; E2/E4/E3-equal/E7/D1/D2 drivers pending |
| **G3** | Preflight, 12 trials | **NOT AUTHORIZED** — requires `APPROVAL.json` with `gate: "G3"` |
| **G4** | Confirmatory campaign | **NOT AUTHORIZED** — requires `gate: "G4"`, clean tree, G2 complete |
| **G5** | Profiling / diagnostics | **NOT AUTHORIZED** |

**No performance trial has been executed.** The only trials run in this phase are 400 ms functional smokes (80 keys, 2 tables), explicitly labelled engineering validation and excluded from every analysis.

---

## 8. Cost revalidation

Measured fixture cost on Windows NTFS from the archived exploratory campaign (`research/experiments/data/campaign/windows_ntfs/`, 314 trials):

| Workload | n | mean fixture init |
| :--- | ---: | ---: |
| A read-only | 64 | 13,237 ms |
| B1 | 58 | 13,181 ms |
| B2 | 48 | 12,637 ms |
| C | 48 | 12,832 ms |
| D | 48 | 13,163 ms |
| E | 48 | 2,317 ms |

Per-trial budget: 13.2 s fixture + 2 s warmup + 10 s window + 0.2 s cooldown + ≈1.5 s teardown ≈ **27 s**.

| Scope | Trials | Wall clock |
| :--- | ---: | ---: |
| Currently runnable (144) | 144 | **≈1.1 h** |
| Full confirmatory set (330) | 330 | ≈2.5 h |
| Full inventory (366) | 366 | **≈2.7 h** |
| With `-Check`, preflight, retries, packaging | — | **≈3.4 h ± 1 h** |

This is **cheaper** than the Linux estimate (5.9 h) because NTFS on the host NVMe builds fixtures about 2.4× faster than the WSL2 virtual disk did. The pre-registered cost gate (06 §9.3) is retained and re-anchored: if a G3 preflight measures > 45 s/trial, the reduction ladder applies unchanged.

The 366/384 limits are **unchanged**. No trial budget is added.

---

## 9. Runner contract

`run_i7.ps1` launches **one OS process per trial**:

```
i7trial.exe -spec <spec.json> -out <trial.json> -gate G1|G3|G4
            -approval <APPROVAL.json> -platform <platform_manifest.json>
            -git-rev <sha> [-keep-data]
```

The spec is a single JSON object (schema `i7.trial-spec.v1`) produced from `runlist.csv`, containing the trial id, block, cell, triplet, arm, order, seed, workload, concurrency, durations, fixture shape, hit ratio, memtable threshold, trigger intervals, WAL-sync flag, data directory and expected reader-set size.

The runner:

- refuses to start without an approval file whose `campaign_id`, `gate` and `approved` fields match the requested gate;
- refuses blocks E2/E4 with exit code 3 (no silent downgrade);
- requires an **empty** data directory (a retry cleans it first);
- writes an envelope `{schema, trial_id, block, cell, arm, spec, runtime, timing, status, error, result}` atomically;
- reports its own Go version, `GOMAXPROCS`, **actual affinity mask** and executable SHA-256 in every trial record;
- exits 0 only when the engine trial reports `SUCCESS`.

`i7trial -selftest` is the platform handshake used by `-Check`: timers, QPC step, `time.Now` step, sleep jitter, open-loop wakeup accuracy, affinity, supported/unsupported blocks.

---

## 10. Operator workflow

One command per stage, no administrator rights:

```powershell
# 1. once per machine / per commit
cd C:\path\to\selfdbms
.\research\experiments\phase_i7\run_i7.ps1 -Check

# 2. inspect what would run (no trial process is launched)
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Preflight
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Campaign

# 3. after external approval of gate G3
#    create I7_WINDOWS_RESULTS\APPROVAL.json  (see below)
.\research\experiments\phase_i7\run_i7.ps1 -Preflight

# 4. after external approval of gate G4 and a clean commit
.\research\experiments\phase_i7\run_i7.ps1 -Campaign
#    if interrupted:
.\research\experiments\phase_i7\run_i7.ps1 -Resume

# 5. always last
.\research\experiments\phase_i7\run_i7.ps1 -Package
```

`-Package` prints and writes the ZIP path and SHA-256; that file is the single artefact to hand to a reviewer.

### 10.1 Approval file

`I7_WINDOWS_RESULTS\APPROVAL.json` — created by the operator only after a reviewer authorizes the gate:

```json
{
  "campaign_id": "I7_STRENGTHENING_V1",
  "gate": "G3",
  "approved": true,
  "approved_by": "<reviewer name>",
  "approved_at": "2026-11-01T09:00:00Z",
  "scope": "preflight: 12 trials, 1500 ms window"
}
```

A file for gate `G3` cannot start `-Campaign`, and an approval for another campaign id is rejected.

### 10.2 Interruption and resume

- Every execution appends one line to `results.jsonl` **after** the trial process exits, with `attempt`, `status`, `exit_code`, `wall_ms`, `error` and log paths.
- `-Resume` skips `trial_id`s with a `SUCCESS` line, retries `FAILED`/`TIMEOUT`/`INCOMPLETE` at most once, and counts retries against the 18-trial replacement cap.
- Per-attempt envelopes are archived under `trials\attempts\<trial_id>_a<NN>.json`, so a retry never destroys the failed record.
- The runlist is committed on first use and its SHA-256 is stored in `campaign_manifest.json`; the script refuses to run against a different runlist, and refuses to rebuild one after results exist.
- The hard ceiling (384) and `-MaxTrials` are enforced by counting ledger lines before any trial starts.

### 10.3 Safety properties (verified by construction)

| Property | How |
| :--- | :--- |
| No administrator rights | only per-process affinity, file I/O under the repository, read-only CIM queries |
| No power-setting change | `powercfg /getactivescheme` only |
| No Defender change | `Get-MpComputerStatus` only |
| No partition/format/trim | never invokes `diskpart`, `format`, `Optimize-Volume`, `chkdsk` |
| No page-cache manipulation | none attempted |
| Frozen data untouched | the script writes only under `I7_WINDOWS_RESULTS`; frozen paths appear only as hashed inputs |
| No silent block downgrade | unsupported blocks abort the run unless `-AllowPartial` |
| Bounded writes | one 32 MiB probe file, deleted immediately; per-trial data directories deleted on success |

---

## 11. Open G2 items (blocking a complete campaign)

1. **H-05** open-loop scheduler (QPC-based, pre-computed exponential schedule, `W_max` abandonment) — E4.
2. **H-06** publication event log + event-conditioned latency — E2/E4.
3. **H-07** publication-only driver (pre-frozen memtables, non-blocking injector) — E2.
4. **H-08** equal-work paced writer at `W_paced` — E3 equal-work.
5. **H-22** Pebble runner (`pebble_runner` submodule, Y1–Y3) — E7.
6. **H-10** instrumented build (`-tags i7instr`) + **D1/D2** automation.
7. **H-02/H-03** per-worker recorders to remove the two global mutexes from the read loop (measurement validity, not a blocker for E1).
8. **H-11** counter-based key stream (paired per-operation identity).
9. **H-20** new per-trial metrics (`k`, `records_scanned`, stall statistics, abandon counts).

Until 1–5 land, `-Campaign` will refuse (by design) or run a clearly-labelled partial set.

---

## 12. Risks introduced by the platform change

| Risk | Mitigation | Residual |
| :--- | :--- | :--- |
| Laptop thermal throttling | drift triplets, S-6, power scheme recorded | Uncontrolled; reported |
| Defender interference | recorded, overhead distribution reported, X-12 exclusion | Cannot be disabled; acknowledged |
| Coarse `time.Now` | QPC for all latency and scheduling; validated | None for latency |
| `Sleep` overshoot (~0.5 ms) | spin the final 1.5 ms; validated p99 66 µs | Sub-ms jitter in E4 arrivals |
| NTFS delete-pending while reading | close-before-unlink, lease drain, tests on NTFS | Third-party handles can delay unlink |
| No race detector | deterministic interleaving + repetition + invariants | Weaker than `-race`; stated as such |
| Single host, single device | one platform by design | No external validity beyond it |
| Warm cache only | stated in scope; AF-1 | No device-latency claims |
| `fsync` may not commit to media | stated durability wording | Epoch ambiguity acknowledged |

---

## 13. Claim discipline for the manuscript

| Allowed | Not allowed |
| :--- | :--- |
| "On Windows 11 / NTFS / this NVMe device, …" | "On Linux, …" or "on bare metal, …" |
| "the tiered lookup holds the read lock for ≈10–31 ms" | "the read lock is held across blocking disk I/O" |
| "warm-cache measurements" | "cold-read / device-latency measurements" |
| "the race detector was not available on the Windows host" | "zero data races verified under `-race` on Windows" |
| "Pebble v1.1.5 was used descriptively" | any Pebble performance ratio |
| "I7 did not repeat the frozen 1,560 trials" | any pooling of I7 data with the frozen WSL2 campaigns |
