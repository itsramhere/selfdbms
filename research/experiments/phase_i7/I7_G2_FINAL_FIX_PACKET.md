# I7 G2 Final Fix Packet

**Gate:** G2 Final Corrective Patch  
**Status:** **COMPLETE.** Execution strictly stopped at G2. G3/G4/G5 **not** initiated.  
**Date:** 2026-10-28  
**Host:** Windows 11 Home 10.0.26200 · AMD Ryzen 7 7445HS (6C/12T) · 15.2 GiB · Samsung MZVL8512HFLU-00BH1 NVMe · NTFS  
**Toolchain:** `go1.26.3 windows/amd64`, `CGO_ENABLED=0`  
**Protocol version:** `1.2.0-windows`  
**Latest Commits:**
- `8d965fd` — `docs(manuscript, reviews): record I6.10 provenance audit and revised manuscript`
- `afe1fb1` — `fix(i7-g2): E2 publication controls, E4 open-loop boundaries, and idempotent G3 calibration`  
**Working tree status:** Clean (0 untracked files, 0 unstaged edits; `git_dirty = false`).

Companion documents: [00_README.md](00_README.md), [01_A_MIN_DESIGN.md](01_A_MIN_DESIGN.md), [06_TRIAL_INVENTORY.md](06_TRIAL_INVENTORY.md), [08_HARNESS_CHANGES.md](08_HARNESS_CHANGES.md), [12_WINDOWS_EXECUTION_AMENDMENT.md](12_WINDOWS_EXECUTION_AMENDMENT.md), [I7_G2_REVIEW_PACKET.md](I7_G2_REVIEW_PACKET.md).

---

## 1. Summary of Affected Source Files & Git Diff

```
 .gitignore                                         |   5 +
 research/experiments/phase_i7/cmd/i7stub/main.go   |   8 +
 research/experiments/phase_i7/harness2/metrics.go  |  81 +++++++++++--
 research/experiments/phase_i7/harness2/run.go      | 130 ++++++++++++++-------
 research/experiments/phase_i7/harness2/run_test.go | 187 ++++++++++++++++++++++++++++-
 research/experiments/phase_i7/harness2/spec.go     |  47 ++++++--
 research/experiments/phase_i7/run_i7.ps1           | 136 +++++++++++++++------
 .../phase_i7/tests/orchestration_tests.ps1         |  61 +++++++++-
 8 files changed, 550 insertions(+), 105 deletions(-)
```

### Detailed File Changes:
1. `research/experiments/phase_i7/harness2/metrics.go`:
   - Separated counters: `ReadsCompletedInWindow`, `ReadsCompletedLate`, `WritesCompletedInWindow`, `WritesCompletedLate`.
   - Separate late sample buffers: `SamplesLate`, `RespLate`, `IssuesLate`.
   - Extended `Shard`, `Totals`, and `Sum` functions to aggregate and preserve both within-window and late samples.
2. `research/experiments/phase_i7/harness2/spec.go`:
   - `OperationCounts`: added `read_ops_completed_in_window`, `read_ops_completed_late`, `write_ops_completed_in_window`, `write_ops_completed_late`.
   - `TrialResult`: added `latency_read_late_nanos`, `latency_response_late_nanos`.
   - `Diagnostics`: added `initial_frozen_memtables`, `final_frozen_memtables`, `publication_events_target`, `publication_events_released`, `publication_events_realized`, `topology_note`.
3. `research/experiments/phase_i7/harness2/run.go`:
   - **E2 Mode Separation**: Both `pub_physical` and `pub_none` pre-freeze `PublicationEvents` memtables before the window, matching initial workloads. In `pub_physical`, injector fires at midpoints $t_i = (2i+1)W/(2N)$ strictly within $[0, W)$, releasing exactly 20 events. In `pub_none`, target is 0, no releases occur during window, and 0 flushes are realized.
   - **Topology Tracking**: Measured flushes/compactions subtracted from post-fixture counters; diagnostics report initial/final frozen memtables and initial/final SSTables with explicit topology classification.
   - **E4 Open-Loop Boundaries**: Rechecks lateness immediately after waking (`issued > intended + wMaxNs`) before starting `Get()`. Partitions completions strictly at `done <= windowNs` vs `done > windowNs`.
   - **Throughput Calculation**: Throughput computed strictly using `ReadsCompletedInWindow / secs`, completely excluding post-window drain completions.
4. `research/experiments/phase_i7/harness2/run_test.go`:
   - Added `TestE2_PhysicalVsNoneModeComparison`: validates manifest timestamps within $[0, W)$, initial workload parity, physical release/realization (4/4), none release/realization (0/0), and topology reporting.
   - Added `TestE4_OpenLoopOverloadAndLateAccounting`: validates waking lateness abandonment, exact conservation identity, and throughput based strictly on within-window completions.
5. `research/experiments/phase_i7/cmd/i7stub/main.go`:
   - Added throughput fields to stub runner envelope to support synthetic calibration tests.
6. `research/experiments/phase_i7/run_i7.ps1`:
   - **Calibration Correctness**:
     - Dedicated ledger: `$Layout.Calibration\calibration_ledger.jsonl`.
     - Idempotent: returns immediately if 9 successful runs already exist without launching any process.
     - Restart-safe: resumes partial runs, skipping already completed runs.
     - Hard ceiling: 12 calibration attempts maximum (9 planned + 3 replacements).
     - Scope binding: requires `-RequiredScope 'calibration'`, preventing preflight approvals from running calibration.
     - Workload transparency: reports 366 inventory trials + 9 calibration runs = 375 total planned executions.
7. `research/experiments/phase_i7/tests/orchestration_tests.ps1`:
   - Added S10 scenarios: scope refusal, template generation, execution, idempotency, and crash/resume.
8. `.gitignore`:
   - Added ignore patterns for LaTeX compilation artifacts (`*.aux`, `*.log`) and data archives (`research/experiments/data/**/*.zip`), enabling clean git working tree.

---

## 2. Test Logs & Validation Summary

| Test Suite | Outcome | Details / Scenarios |
| :--- | :--- | :--- |
| `go vet ./...` | **PASS (exit 0)** | Zero vet issues across all packages |
| `go test ./lsmengine/... -run "TestAMin\|TestFlushInjectionGate"` | **PASS (11/11)** | All A-min shutdown and gate concurrency tests pass |
| `go test ./research/experiments/phase_i7/harness2/...` | **PASS (17/17)** | All driver tests pass, including E2 comparison and E4 overload |
| `go test ./research/experiments/phase_i7/pebble_runner/...` | **PASS (exit 0)** | Envelope JSON, percentile, approval tests pass |
| `powershell orchestration_tests.ps1` | **PASS (30/30)** | 30 synthetic scenarios pass (0 failures) |
| `run_i7.ps1 -Check` | **PASS (exit 0)** | Platform validated, runners built, clean git revision `afe1fb1` |
| `run_i7.ps1 -DryRun -Preflight` | **PASS (exit 0)** | 12 preflight specs generated, 0 skipped, 0 launched |
| `run_i7.ps1 -DryRun -Campaign` | **PASS (exit 0)** | 276 specs generated, 54 skipped (gated on calibration), 0 launched |

---

## 3. Specific Control Assertions

### 3.1 E2 Physical vs None Event-Count & Timestamp Assertions
From `TestE2_PhysicalVsNoneModeComparison`:
* **Preregistered 20-event schedule bounds** ($W = 10,000\text{ ms}$, $N = 20$):
  - $t_i = (2i + 1) \times \frac{10,000\text{ ms}}{40} \in [0, 10,000\text{ ms})$ for all $i \in \{0, \dots, 19\}$.
  - First event timestamp: $t_0 = 250\text{ ms} > 0$.
  - Last event timestamp: $t_{19} = 9,750\text{ ms} < 10,000\text{ ms}$ (250 ms margin before window closes).
* **Initial Workload Parity**:
  - `pub_physical` and `pub_none` both pre-freeze 20 memtables before the window starts.
  - Both start with `initial_sstables = 4`, `initial_frozen_memtables = 20`.
* **Realized Event Separation**:
  - `pub_physical`: `publication_events_target = 20`, `publication_events_released = 20`, `publication_events_realized = 20`, `final_frozen_memtables = 0`, `final_sstables = 24`.
  - `pub_none`: `publication_events_target = 0`, `publication_events_released = 0`, `publication_events_realized = 0`, `final_frozen_memtables = 20`, `final_sstables = 4`.
  - In `pub_none`, zero flushes occur during measurement; memtables remain static until drained cleanly at `Close()`.
* **Diagnostics Reporting**:
  - `topology_note` explicitly records memtable and SSTable transitions (`"memtables_drained_to_sstables"` vs `"memtables_static_unreleased"`).

### 3.2 E4 Open-Loop Request Accounting & Late-Completion Assertions
From `TestE4_OpenLoopOverloadAndLateAccounting`:
* **Waking Lateness Recheck**:
  - When worker wakes at `issued := ElapsedNs(base)`, if `issued > a.IntendedNs + wMaxNs`, the request is immediately abandoned without issuing a database read (`sh.Abandoned++`).
* **Exact Conservation Identity**:
  $$\text{ReadOpsScheduled} = \text{ReadOpsCompletedInWindow} + \text{ReadOpsCompletedLate} + \text{ReadOpsAbandoned} + \text{ReadOpsUnissued}$$
  Verified to close with zero error ($0 = 0$ discrepancy).
* **Throughput Purity**:
  $$\text{ReadOpsPerSec} = \frac{\text{ReadOpsCompletedInWindow}}{\text{secs}}$$
  Late completions occurring during post-window drain are strictly excluded from the throughput rate numerator.
* **Latency Partitioning**:
  - Within-window response latency: recorded in `latency_response_nanos` (sample count equals `ReadOpsCompletedInWindow`).
  - Drain completions: recorded in `latency_response_late_nanos` (sample count equals `ReadOpsCompletedLate`), preserving uncensored latencies without biasing in-window percentiles.

### 3.3 G3 Calibration Idempotency & Budget Assertions
From `orchestration_tests.ps1` (Scenarios `S10-cal-*`):
* **Idempotency**: Running `-Calibrate` when 9 runs are already recorded as `SUCCESS` in `calibration_ledger.jsonl` executes **0** new processes and returns the verified `calibration.json`.
* **Restart-Safety**: When calibration is interrupted (e.g. 4 of 9 runs done), invoking `-Calibrate` skips the 4 completed runs and executes only the remaining 5 runs.
* **Execution Budget**: Bounded by hard ceiling of 12 attempts (9 planned + 3 replacements), tracked in `calibration_ledger.jsonl`.
* **Scope Binding**: `-Calibrate` requires approval with scope `calibration`. A preflight-only approval is refused (`S10-cal-scope-refusal`).
* **Workload Transparency**: Planned execution explicitly reported as:
  $$\text{Total Workload} = 366\text{ inventory trials} + 9\text{ calibration runs} = 375\text{ total planned runs}$$

---

## 4. Frozen Datasets & Executable Integrity

| Item | Expected SHA-256 | Current Verified SHA-256 | Status |
| :--- | :--- | :--- | :--- |
| Frozen 960-trial manifest (`wsl2_ext4`) | `debce63aef5dd07fac344974a4e4bc334447c89e18dd428948479f828a5802e6` | `debce63aef5dd07fac344974a4e4bc334447c89e18dd428948479f828a5802e6` | **MATCH** |
| Frozen 600-trial manifest (`targeted`) | `21005ded5f9ec1b208861a31dffbc14a405bd574ee867cc5bb95cc2ba9427468` | `21005ded5f9ec1b208861a31dffbc14a405bd574ee867cc5bb95cc2ba9427468` | **MATCH** |
| Runlist (`runlist.csv`) | `0708ca4cb2785650fb1cbecf7cb5ee139e86ee442e0d505447ab15815880e13c` | `0708ca4cb2785650fb1cbecf7cb5ee139e86ee442e0d505447ab15815880e13c` | **MATCH** |
| Windows runner (`i7trial.exe`) | — | `fa5c9463fabc3f6bc26e8534395fc9889e6de2c615a9c8777ba20c6f6a64060f` | **BUILT** |
| Pebble runner (`pebble_runner.exe`) | — | `1fe91cb065501d7880a51f6511b1e703b6ec3e9fd1c5ba4f1f85f4d96c3c12e1` | **BUILT** |
| Review ZIP (`I7_G2_REVIEW.zip`) | — | `8b9c91047a5798f41dd11cab5c05e82189800fde72e0249e47c8730a6a3f53bc` | **STAGED** |

---

## 5. Exact Commands for Gate G3 Preflight Execution

Run from repository root in **Windows PowerShell** (non-elevated):

```powershell
cd C:\Users\ramst\Documents\Projects\selfdbms
$env:CGO_ENABLED = '0'

# 1. Platform validation and runner verification
.\research\experiments\phase_i7\run_i7.ps1 -Check

# 2. Generate G3 preflight approval template (scope: preflight, allowed_blocks: E1,E3,E5,PF)
.\research\experiments\phase_i7\run_i7.ps1 -Preflight -WriteApprovalTemplate -Gate G3
# Edit APPROVAL.template.G3.json: set approved=true, approved_by, approved_at.
# Move/rename to: .\research\experiments\phase_i7\I7_WINDOWS_RESULTS\APPROVAL.json

# 3. Dry-run inspection (validates approval, generates 12 specs, executes 0 processes)
.\research\experiments\phase_i7\run_i7.ps1 -DryRun -Preflight

# 4. Execute the preregistered 12-trial preflight
.\research\experiments\phase_i7\run_i7.ps1 -Preflight

# 5. Package preflight outcome
.\research\experiments\phase_i7\run_i7.ps1 -Package
```

---

> [!IMPORTANT]
> **STOPPED AT G2. Awaiting external reviewer authorization before executing Gate G3.** No preflight or confirmatory performance campaign has been executed.
