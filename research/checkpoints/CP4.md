# Major Checkpoint CP4: Controlled Experiment Harness and Preliminary Smoke Validation

**Milestone**: Checkpoint CP4  
**Phase**: Phase I4 Complete  
**Date**: 2026-10-18  
**Tested Revision**: `HEAD` (descending from `c58bc6177b5a925a0b25ba795957839866d0b040`)  
**Verified Historical Baseline**: `baseline-approach-a-verified` (`ae8c034c438d56abbe6664e6472f7a0c1278797f`)  
**Status**: COMPLETE — AWAITING EXTERNAL REVIEW APPROVAL (Phase I5 ON HOLD)

---

## 1. Executive Summary

In accordance with `01_IMPLEMENTATION_PLAN.md` and the external reviewer directives for Phase I4, we have designed, implemented, and validated an honest, controlled, reproducible experiment harness to evaluate reader-compactor synchronization trade-offs between **Approach A** (Manager-wide RWMutex baseline) and **Approach B** (Two-tier immutable reader-set snapshots).

### Key Accomplishments in Phase I4:
1. **Revisions Frozen & Seam Validated (I4.0)**:
   - Verified that `SyncModeApproachA` and `SyncModeApproachB` execute from the same engine binary via the existing typed configuration seam (`Config.Mode`).
   - Confirmed that zero storage formats, transaction semantics, WAL append/sync policies, compaction selection rules, or flush admission policies were altered.
   - Verified historical baseline tags remain intact and unmodified.
2. **Exhaustive Scientific Methodology (I4.1 / I4.4)**:
   - Authored [`research/experiments/methodology.md`](../experiments/methodology.md) defining deterministic fixture construction, seedable PRNG key sampling (uniform and Zipfian $s = 0.99$), and five distinct workloads (Workloads A through E, including an explicit negative-control workload).
   - Formulated strict controls distinguishing aggregate `testing.B` `ns/op` from individual operation latencies ($p50, p95, p99, p999$), directly instrumented disk vs memory tier lookups, and enforced strict isolation between Windows NTFS and Linux ext4 datasets.
3. **Common A/B Workload Runner & Schemas (I4.2 / I4.5)**:
   - Authored formal JSON schemas: [`config_schema.json`](../experiments/schemas/config_schema.json) and [`result_schema.json`](../experiments/schemas/result_schema.json).
   - Implemented `research/experiments/harness/`: pre-allocated, lock-free `LatencyRecorder`, deterministic `KeySampler`, and isolated `RunTrial` lifecycle.
   - Built CLI runner [`cmd/experiment_runner`](../experiments/cmd/experiment_runner/main.go) and automated Markdown table generator [`analysis/summarize.go`](../experiments/analysis/summarize.go).
4. **Hardware-Calibrated Concurrency Matrix (I4.3)**:
   - Mapped reader concurrency levels ($1, 2, 4, 8, 12, 16, 24, 32$) to the host processor architecture (AMD Ryzen 7 7445HS, 6 physical cores, 12 logical processors via SMT).
   - Added minimal, non-invasive atomic diagnostic counters to `lsmengine.Manager` (`FlushesCompleted`, `CompactionsCompleted`, `SSTablePointReads`, `MemtablePointReads`, `ActiveSSTableCount`) to empirically observe background activity without inferring overlap from elapsed time.
5. **Preliminary Dual-Platform Smoke Validation (I4.6)**:
   - Executed smoke comparison suites across all 5 workloads on **Windows 11 NTFS native** and **WSL2 Ubuntu Linux native ext4**.
   - Verified that real SSTable reads occurred ($>0$ in Workloads A--D), real flushes occurred (4 in Workloads A--D), real compactions occurred (1 in Workloads C--D), and zero SSTable reads occurred in negative-control Workload E.
   - Resolved an internal concurrency defect in the warmup sampler caught by ThreadSanitizer (Issue [E-015](../logs/ISSUES.md)).
   - Achieved **176/176 unit tests PASS** (182 total runnable tests) across both Windows and WSL2 `-race` with **zero data races, zero deadlocks, and clean `go vet`**.

---

## 2. Reconciled Test Inventory & Dual-Platform Verification

| Test Suite | File Location | Unit Tests | Windows Native (`go1.26.3`) | WSL2 Linux (`go1.27.2` `-race`) |
| :--- | :--- | :--- | :--- | :--- |
| Verified Baseline (I1) | `lsmengine/*_test.go` | 101 | **PASS** | **PASS** (0 races) |
| Scaffolding (I2) | `lsmengine/snapshot_scaffolding_test.go` | 6 | **PASS** | **PASS** (0 races) |
| Implementation (I3) | `lsmengine/snapshot_*_test.go`, `approach_b_mvcc_test.go` | 30 | **PASS** | **PASS** (0 races) |
| Correctness Repairs (I3.7) | `lsmengine/sstable_read_error_test.go`, etc. | 26 | **PASS** | **PASS** (0 races) |
| Verification Addendum (CP3a-V) | `lsmengine/per_key_conflict_timestamp_test.go`, etc. | 7 | **PASS** | **PASS** (0 races) |
| Experiment Harness (I4) | `research/experiments/harness/harness_test.go` | 6 | **PASS** (12.83s) | **PASS** (29.94s, 0 races) |
| **Grand Total** | **Full Repository Test Matrix** | **176** | **PASS (10.46s)** | **PASS (29.94s, 0 races)** |

*Static Analysis*: `go vet ./...` passes with 0 warnings on both Windows native and WSL2 Linux.

---

## 3. Scope Compliance & Boundary Preservation

1. **Phase I5 Remains ON HOLD**: No full statistical matrix (10 runs per cell) or definitive performance conclusions have been executed or claimed.
2. **Historical Baseline Preserved**: `baseline-approach-a` (`d6909a7`) and `baseline-approach-a-verified` (`ae8c034`) remain completely untouched.
3. **No Engine Alterations**: Core storage engines, binary SSTable block formats, SkipList structures, MVCC validation logic, and WAL append sync protocols remain 100% identical between modes.
4. **Separation of Filesystem Populations**: NTFS and ext4 smoke results are stored and analyzed in separate directories (`research/experiments/data/smoke/windows_ntfs/` vs `research/experiments/data/smoke/wsl2_ext4/`).

---

EXTERNAL REVIEW REQUIRED — CP4 AWAITING APPROVAL.
