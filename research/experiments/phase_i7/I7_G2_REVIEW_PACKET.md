# I7 G2 Review Packet

**Gate:** G2 — Windows-native harness, drivers and orchestration.
**Status:** **COMPLETE.** G3/G4/G5 **not** executed. No engine performance trial was run.
**Date:** 2026-10-28
**Host:** Windows 11 Home 10.0.26200 · AMD Ryzen 7 7445HS (6C/12T) · 15.2 GiB · Samsung MZVL8512HFLU-00BH1 NVMe · NTFS
**Toolchain:** `go1.26.3 windows/amd64`, `CGO_ENABLED=0`, no C compiler
**Protocol version:** `1.2.0-windows`
**Commits:** `4c5a37c` (protocol amendments, docs) · `0317d0b` (implementation)

Companion documents: [01](01_A_MIN_DESIGN.md), [08](08_HARNESS_CHANGES.md), [12](12_WINDOWS_EXECUTION_AMENDMENT.md), [13](13_G1_REVIEW_EVIDENCE.md).

---

## 1. Corrections requested at G2, and what was done

### 1.1 Resolve A-min shutdown safety

**Defect (accepted).** The G1 drain counted leases only on `ActiveSSreaders`. A table superseded *while a reader was scanning it* was untracked, so `Close()` could return while a live lease still referenced that descriptor, and the last releaser could still run reclamation after `Close` returned.

**Fix.** Three changes, all in `lsmengine`:

| Change | Effect |
| :--- | :--- |
| `Manager.retiredReaders []*SSTableReader`, appended **inside** the publication lock | Retirement is now observable to shutdown |
| `countOutstandingLeasesLocked()` sums leases over **active and retired** tables; `drainReaderLeases()` uses it | Shutdown waits for the full lease population |
| `Close()` restructured: snapshot both lists under the lock, then for each reader **skip any whose `leaseCount != 0`** and record an explicit error | A timeout cannot cause unsafe cleanup: no leased descriptor is closed, ever |

`SSTableReader.reclaimDone` lets the retired list forget completed tables without losing pending ones.

**Proof obligation, restated.** On a drain timeout `Close()` returns an error and leaves every leased descriptor open. Nothing is closed twice (`freeOnce`). Reclamation of a still-leased table is therefore performed exactly once, by its last releaser, and never concurrently with `Close`. The timeout changes *which error is reported*, not the safety of any descriptor. This is the argument the new test encodes.

**New deterministic test — `TestAMin_ShutdownWaitsForRetiredLease`.** One interleaving combining all three elements the reviewer asked for:

1. a reader is paused mid-scan holding leases on four tables;
2. `CompactAll()` retires all four (asserted: `len(retiredReaders) == 4`);
3. `Close()` is started and **must not return** within 250 ms, and no retired file may disappear;
4. the reader is released; the read returns the correct value;
5. `Close()` completes with `nil`, and **all four retired files are reclaimed by shutdown**.

**Second new test — `TestAMin_ShutdownTimeoutFailsClosed`.** A lease that is never released, with `leaseDrainTimeout = 50 ms`:

- `Close()` returns an error mentioning the lease;
- every descriptor still answers `File.Stat()`;
- every file is still on disk;
- releasing the lease afterwards reclaims without double-close.

**Source diff for external review:** `diff/a_min.patch` in the review ZIP, generated as `git diff f1a561a..0317d0b -- lsmengine research/experiments/harness/runner.go`.

```
 lsmengine/approach_a_min_test.go       | 629 +++++++++++++
 lsmengine/compaction.go                |  22 +
 lsmengine/flush_gate_test.go           | 132 ++++
 lsmengine/manager.go                   | 414 +++++++++++++-
 lsmengine/snapshot.go                  |   1 +
 lsmengine/sstableReader.go             |  55 +++
 research/experiments/harness/runner.go |   3 +
 7 files changed, 1244 insertions(+), 12 deletions(-)
```

### 1.2 Complete the G2 drivers

A new executor package `research/experiments/phase_i7/harness2` implements the four modes. It does **not** modify the frozen harness, so the frozen campaigns' reproducibility story is untouched.

| Requirement | Implementation | Evidence |
| :--- | :--- | :--- |
| **E2** publication-only driver + correct event timing | Engine `flushInjectionGate`: the flush worker blocks after dequeue and before build until `ReleaseOneFlush()`. The harness pre-freezes N memtables **before** the window, then releases one at each scheduled instant via a non-blocking injector. Zero writes occur in the window. Events are timestamped on QPC from `OnFlushPublishBegin` to `OnFlushPublishEnd`. | `TestFlushInjectionGate_BlocksUntilReleased` (3 arms) proves one token ⇒ exactly one publication and none before; `TestFlushInjectionGate_DoesNotBlockClose`; `TestRunPublicationOnlyDriver` asserts 4 events, `write_ops_attempted == 0`, valid begin/end |
| **E3 equal-work** paced writer + matched-work accounting | Absolute QPC schedule at `W_paced` (235 writes/s at `r=1`, 210 at `r=4`, fixed from frozen A-arm medians). The writer records attempted/completed separately; the campaign table reports both. | `TestRunPacedWriterMatchesTargetRate`: 50/s over 200 ms ⇒ 6–14 attempts (expected 10) |
| **E4** QPC open-loop + accounting + event conditioning | Pre-computed exponential arrival schedule (identical across arms), workers claim indices, `SleepUntilElapsed` on QPC with a 1.5 ms spin, `W_max` abandonment, per-request `(intended, issued, completed)` triples. Accounting closes: `scheduled = issued + abandoned + unissued`. | `TestRunOpenLoopAccounting` asserts the identity and that response ≥ service; `TestScheduleDeterministicAndRateAccurate`; `TestConditionEventsBlockRatio` |
| **E7** pinned Pebble descriptive runner | Separate module `pebble_runner/`, `github.com/cockroachdb/pebble v1.1.5`, CGO-free, QPC timing, own approval gate, `Metrics`-based flush/compaction/table counts. Y1/Y2 disable automatic compactions (`Options.DisableAutomaticCompactions` exists in v1.1.5). | `go vet`/`go test`/`go build` all exit 0; functional Y1 and Y3 smokes exit 0; negative approval ⇒ exit 4, no envelope. See `pebble_runner/VERIFICATION.md` |
| **H-02/H-03** per-worker metrics | `harness2/metrics.go`: one `Shard` per worker, no lock on the read path, merged once after `wg.Wait()`. Overflow is counted (`Dropped`) and fails the trial instead of biasing percentiles. | `TestShardMergePreservesSamples`, `TestStatsPercentilesAndSufficiency` |
| **H-11** deterministic request streams | `harness2/keystream.go`: `key_j = SHA256(seed, "k", j)`, hit/miss and absent-key index derived from the same digest. Request *j* is identical in A, A-min and B regardless of how many requests each completed. | `TestKeyStreamIsSharedAcrossArms` (100,000 positions, byte-identical), `TestKeyStreamAbsentKeysAreOutsideKeyspace` |

### 1.3 Orchestration correctness (all eleven items)

| # | Requirement | Implementation | Test |
| :--- | :--- | :--- | :--- |
| 1 | D3 drift controls anchored at the real start/end | Runlist generation sorts triplets by SHA-256, then forces the `D3/start` triplet to rows 1–3 and `D3/end` to rows 364–366 | `S2-anchor-start`, `S2-anchor-end` |
| 2 | Revalidate git rev, worktree, runner hash, platform and frozen checksums immediately before execution | `Assert-ExecutionIntegrity` runs before the trial loop and re-checks: `git rev-parse`, a worktree fingerprint (diff + untracked list), runner SHA-256 vs platform manifest vs campaign manifest, platform manifest SHA-256, and all four frozen artifact hashes | S9; enforced on every `-Campaign`/`-Resume`/`-Calibrate` |
| 3 | Bind G3/G4/G5 approval to protocol, runlist, executable and scope | Approval JSON must carry `campaign_id`, `protocol_version`, `gate`, `runlist_sha256`, `runner_sha256`, `allowed_blocks`, `max_trials`; `allow_dirty` defaults to false. `i7trial` independently re-checks protocol, gate, arm/block allow-list and its own exe hash | `S4-runlist-binding`, `S4-runner-binding`, `S4-block-binding`, `S4-dirty-binding`, `S4-gate-binding` |
| 4 | Safe runlist capability updates before freezing | `-RefreshRunlist` diffs the new list against the committed one on 29 **semantic** columns and refuses if anything but `runner_support`/`note` changed while results exist | `S2` (regeneration), `Compare-Semantics` |
| 5 | Resume accounts for interrupted attempts; budget global | Every attempt appends `STARTED` before launch and `FINISHED` after. An interrupted attempt is an attempt. `replacements_used` is persisted in the campaign manifest and incremented per retry, capped at 18 | `S6-crash-recorded`, `S6-resume`, `S6-replacements`, `S6-idempotent-resume` |
| 6 | Clear/archive stale output; validate envelopes | Data directory removed before launch; a previous envelope archived to `trials/attempts/*.prev.json`; the envelope is schema-validated (identity, result payload, positive window, zero validation errors) before it can count as success | `S8-invalid-envelope` ⇒ `INVALID` |
| 7 | Enforce stopping rules | `Assert-Resources` before every trial: free space floor, campaign-duration limit (default 480 min), total-failure limit (10), lease/lifetime-failure limit (2). Drift is computed at packaging and flags >15 % | implemented; unit-exercised via the budget scenarios |
| 8 | Reject results roots inside frozen directories | Guard runs **before any directory is created**, at dispatch, plus a second check in each mode | `S9-frozen-root` |
| 9 | Diagnostics behind a separate G5 path | `-Diagnostics` requires gate G5 and refuses until implemented; diagnostic rows are `phase=diagnostic` and are never selected by `-Campaign`/`-Preflight` | `S9-diagnostics-gate` |
| 10 | Per-trial process isolation | One OS process per trial via `.NET Process` with both streams drained asynchronously; `WaitForExit(timeout)` then kill | `S7-timeout` ⇒ `TIMEOUT` |
| 11 | `-MaxTrials` / hard ceiling enforced globally | Attempts counted from `STARTED` lines; second invocation at the ceiling refuses | `S7-max-trials`, `S7-ceiling-blocks` |

### 1.4 Source control and cleanliness

- Two commits, deliberately separated: `4c5a37c` (protocol amendments) and `0317d0b` (implementation).
- `.gitignore` now excludes `I7_WINDOWS_RESULTS/`, `evidence/`, `**/bin/` and the storage probe file. No generated trial output, generated spec or archive is committed.
- Both frozen campaigns and all historical executable hashes are unchanged: the manifest hashes still match (`debce63a…` and `21005ded…`), and no file under the frozen data roots differs from `HEAD`.
- One incident, corrected: an early guard bug created `targeted_validation_v1/i7_should_refuse/` during the frozen-root test. It was removed; the guard now runs before any directory creation, and the test asserts refusal without writing.

### 1.5 Validation performed (no campaign executed)

| Check | Result | Log |
| :--- | :--- | :--- |
| `go vet ./...` | **exit 0** | `tests/vet.log` |
| `go test ./... -count=1` | **exit 0**, 11 packages, 0 failures | `tests/go-test-all.log` |
| A-min + gate tests (11 functions) | **all PASS** | `tests/go-test-a-min.log` |
| Concurrency stress `-count=5` (churn, close-race, compaction-during-scan, retired-lease shutdown, gate) | **PASS** | `tests/go-test-stress-count5.log` |
| `harness2` (15 functions incl. 5 mini end-to-end drivers) | **PASS** | `tests/go-test-harness2.log` |
| Orchestration (25 synthetic scenarios) | **25/25 PASS** | `tests/orchestration_final.log` |
| `go test -race` | **NOT AVAILABLE** — `go: -race requires cgo`; with cgo enabled `gcc not found` | `tests/go-test-race-attempt.log` |
| Pebble runner | vet/test/build exit 0; Y1 and Y3 smokes SUCCESS; negative approval exit 4 | `pebble_runner/VERIFICATION.md` |

**No performance trial was executed.** The mini end-to-end drivers use 60–80 keys, 200–400 ms windows, and are labelled engineering validation. The orchestration scenarios use a stub runner that never touches the engine.

---

## 2. Manifest / runlist integrity evidence

| Artifact | Value |
| :--- | :--- |
| Runlist rows | **366** |
| Runlist SHA-256 (current build) | `0708ca4cb2785650fb1cbecf7cb5ee139e86ee442e0d505447ab15815880e13c` |
| Block counts | E1=72, E2=60, E3=96, E4=54, E5=18, E7=24, D3=6, PF=12, D1=9, D2=15 |
| First 3 rows | `i7_D3_start_t01_*` (cells `start`) |
| Last 3 rows | `i7_D3_end_t01_*` (cells `end`) |
| Campaign manifest | binds `runlist_sha256`, `runlist_revision`, `runner_sha256`, `platform_sha256`, `git_revision`, `worktree_fingerprint`, `replacements_used` |
| Platform manifest | `affinity_actual=0xfff`, QPC 10 MHz / 100 ns, `time.Now` 404–507 µs, storage probe 300–527 MB/s, four frozen artifact hashes |
| Frozen manifests | `debce63aef5dd07fac344974a4e4bc334447c89e18dd428948479f828a5802e6`, `21005ded5f9ec1b208861a31dffbc14a405bd574ee867cc5bb95cc2ba9427468` |

`tests/` and `evidence/` in the review ZIP contain the raw files.

---

## 3. Synthetic interruption / resume outcomes

From `tests/orchestration_final.log` (stub runner, 6 D3 trials per root):

| Scenario | Observation |
| :--- | :--- |
| Clean run | 6 `STARTED`, 6 `FINISHED`, **6 SUCCESS**; package contains `results.jsonl`, `summary.json`, `checksums.sha256`, ZIP |
| Crash (runner writes nothing, exit 2) | 6 `STARTED`, 6 `FINISHED` with **FAILED/INCOMPLETE**; every attempt recorded, none lost |
| `-Resume` after crash | 6 retried ⇒ **6 SUCCESS**; `replacements_used = 6`; ledger 24 lines (12 per phase) |
| `-Resume` again | **0 new attempts** (idempotent) |
| `-MaxTrials 3` | exactly **3** attempts |
| Second run with `-MaxTrials 3` | **refused**: attempts already at the ceiling |
| 2 s timeout on a hanging runner | 1 `FINISHED` with status **TIMEOUT** |
| Envelope with `SUCCESS` but no result block | recorded as **INVALID**, not success |
| Wrong runlist / runner / block / gate / dirty-tree approval | **all refused** with distinct messages |

---

## 4. Updated trial counts and blockers

| Item | Value |
| :--- | :--- |
| Planned trials | **366** (unchanged) |
| Hard ceiling | **384** (unchanged) |
| Replacement cap | 18, persisted globally |
| Confirmatory rows | 330 |
| Preflight rows | 12 |
| Diagnostic rows (G5) | 24 |
| Rows with an implemented driver | **330 / 330** |
| Rows runnable **today without calibration** | 276 confirmatory (E1, E2, E3, E5, E7, D3) |
| Rows gated on G3 calibration | **54 (E4)** — `C_ref` is produced by `-Calibrate` |

**Blockers to G3:** none technical. G3 requires the reviewer's `APPROVAL.json` for gate G3.

**Blockers to G4:** G3 complete; `calibration.json` present (produced by the G3 calibration); reviewer approval for gate G4.

**Known limitations carried forward:** race detector unavailable on this host; `time.Now` unusable for sub-millisecond timing (QPC is used everywhere); warm-cache only; single host/device; Defender and thermal effects recorded but uncontrolled.

---

## 5. Exact commands for the 12-trial preflight (G3)

Run from the repository root in **Windows PowerShell** (no elevation):

```powershell
cd C:\Users\ramst\Documents\Projects\selfdbms
$env:CGO_ENABLED = '0'

# 1. Platform validation + build both runners (no trial)
.\research\experiments\phase_i7\run_i7.ps1 -Check

# 2. Reviewer action: create the gate-G3 approval from the generated template.
#    -WriteApprovalTemplate fills campaign_id, protocol_version, runlist_sha256,
#    runner_sha256, allowed_blocks and max_trials from the CURRENT artifacts.
.\research\experiments\phase_i7\run_i7.ps1 -Preflight -WriteApprovalTemplate -Gate G3
#    Edit APPROVAL.template.G3.json: set approved=true, approved_by, approved_at,
#    then rename it to APPROVAL.json in the results root.
#    The 12-trial preflight also needs an allowed_blocks list of E1, E3, E5, PF.

# 3. Inspect what will run (launches nothing)
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Preflight

# 4. Execute the preregistered 12-trial preflight
.\research\experiments\phase_i7\run_i7.ps1 -Preflight

# 5. Package the outcome
.\research\experiments\phase_i7\run_i7.ps1 -Package
```

Expected result: 12 trials, 4 triplets (E1 r=8, E1 r=1, E3 free r=4, E5 all-miss), 1,500 ms window, 300 ms warmup. `-Package` prints the ZIP path and its SHA-256.

Gate-G4 sequence, for completeness (not authorized yet):

```powershell
.\research\experiments\phase_i7\run_i7.ps1 -Calibrate      # G3: nine runs -> calibration.json
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Campaign
.\research\experiments\phase_i7\run_i7.ps1 -Campaign       # G4
.\research\experiments\phase_i7\run_i7.ps1 -Resume         # after any interruption
.\research\experiments\phase_i7\run_i7.ps1 -Package
```

---

## 6. What is *not* claimed

- No trial of any arm was executed for performance. The only engine executions were 60–80-key functional smokes of 200–400 ms.
- A-min shutdown safety is argued from a proof plus two deterministic tests, not from the race detector, which is unavailable on this host.
- The 12-trial preflight has **not** been run. Nothing in this packet is a G3 result.
- The E4 `C_ref` value does not exist yet; the dry run reports 54 E4 rows as skipped for that reason.
- The worktree used for these commits still contains unrelated pre-existing changes (`paper.tex`, `paper/`, `research/reviews/`). They are recorded in `worktree_fingerprint`; a G4 approval would have to set `allow_dirty=true` unless they are committed first.

---

## 7. Reviewer checklist

| # | Question | Answer |
| :--- | :--- | :--- |
| 1 | Is the retired-reader shutdown defect fixed? | Yes — retired tables are tracked and drained; a timeout fails closed |
| 2 | Is there a test combining retirement + lease + `Close()`? | Yes — `TestAMin_ShutdownWaitsForRetiredLease` |
| 3 | Can the timeout cause unsafe cleanup? | No — `Close` skips any reader with a nonzero lease and returns an error |
| 4 | Is the A-min source diff provided? | Yes — `diff/a_min.patch` |
| 5 | Are E2/E3-equal/E4/E7 implemented? | Yes, with driver tests and accounting assertions |
| 6 | Are per-worker metrics and deterministic streams in place? | Yes — `harness2/metrics.go`, `keystream.go` |
| 7 | Are D3 controls anchored first/last? | Yes — asserted in the runlist test |
| 8 | Is integrity revalidated immediately before execution? | Yes — `Assert-ExecutionIntegrity` |
| 9 | Is approval bound to protocol/runlist/exe/scope? | Yes — five refusal scenarios pass |
| 10 | Is resume safe, with a global replacement budget? | Yes — STARTED/FINISHED ledger, persisted budget |
| 11 | Are frozen roots protected? | Yes — guard before any directory creation; incident removed and re-tested |
| 12 | Was any trial executed? | **No** |

---

## 8. Review ZIP contents

```
I7_G2_REVIEW/
  README.md                     this packet
  diff/a_min.patch              A-min + harness diff for external review
  source/lsmengine/             manager.go, compaction.go, sstableReader.go, snapshot.go, tests
  source/harness2/              all driver sources
  source/cmd/i7trial/           per-trial runner
  source/cmd/i7stub/            synthetic runner for orchestration tests
  source/run_i7.ps1             the driver
  source/tests/orchestration_tests.ps1
  source/pebble_runner/         go.mod, go.sum, main.go, tests, clock, VERIFICATION.md
  tests/                        vet.log, go-test-all.log, go-test-a-min.log,
                                go-test-stress-count5.log, go-test-harness2.log,
                                go-test-race-attempt.log, orchestration_final.{log,json}
  evidence/                     platform_manifest.json, campaign_manifest.json,
                                runlist.csv, runlist_summary.txt
  PRECFLIGHT_COMMANDS.md        section 5 of this packet
  CHECKSUMS.sha256              every file above
```
