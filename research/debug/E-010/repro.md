# Issue E-010: Flush Failure Precedence Inversion Reproduction

**Issue ID:** `E-010`  
**Discovered in Sub-phase:** I1.4.7b  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Date:** 2026-10-17  
**Severity:** Critical (P0 Data Integrity / Stale Read / Tombstone Resurrection / Serializability Violation)  

---

## 1. Description of Defect

When a background Memtable flush $F_0$ fails (e.g., due to disk I/O error or builder failure), $F_0$ is retained in `m.Frozenmem` while the background worker continues executing. If subsequent Memtables ($F_1, F_2$) succeed and publish their on-disk SSTables to `m.ActiveSSreaders`, a fatal precedence inversion occurs:

1. `m.Frozenmem` contains older generation $F_0$ (sequence $S_0$).
2. `m.ActiveSSreaders` contains newer generation $F_1$ (sequence $S_1 > S_0$).
3. Point reads via `Manager.Get` examine `m.Frozenmem` (Tier 2) **before** `m.ActiveSSreaders` (Tier 3).
4. `Manager.Get` returns the stale value from $F_0$, masking the newer committed value or tombstone in $F_1$.
5. `Manager.latestValueLocked` returns the older sequence $S_0$ from $F_0$, causing write-write conflict detection to miss conflicts with $S_1$ and violating snapshot isolation.

---

## 2. Minimal Reproduction Commands

### Windows Native:
```powershell
go test -v ./lsmengine -run TestFailedFlush_E010
```

### WSL2 Linux (-race):
```bash
wsl --cd /mnt/c/Users/ramst/Documents/Projects/selfdbms -e /snap/bin/go test -race -v -count=1 -run TestFailedFlush_E010 ./lsmengine
```

---

## 3. Reproduction Test Results Summary

| Test Case | Scenario | Expected | Actual | Status |
| :--- | :--- | :--- | :--- | :--- |
| `TestFailedFlush_E010_StaleReadRepro` | $F_0$ Put("k1", "old"), $F_1$ Put("k1", "new") | `"new"` | `"old"` | **FAIL (Defect Reproduced)** |
| `TestFailedFlush_E010_TombstoneResurrectionRepro` | $F_0$ Put("k2", "val"), $F_1$ Delete("k2") | `ErrKeyDeleted` | `"val"` | **FAIL (Defect Reproduced)** |
| `TestFailedFlush_E010_StaleDeletionMaskingRepro` | $F_0$ Delete("k3"), $F_1$ Put("k3", "reborn") | `"reborn"` | `ErrKeyDeleted` | **FAIL (Defect Reproduced)** |
| `TestFailedFlush_E010_ThreeGenerationsRepro` | $F_0$ "v0" (fail), $F_1$ "v1", $F_2$ "v2" | `"v2"` | `"v0"` | **FAIL (Defect Reproduced)** |
| `TestFailedFlush_E010_ActiveMemtablePrecedence` | $F_0$ "v0" (fail), $M$ "v_active" | `"v_active"` | `"v_active"` | **PASS** (Memtable checked first) |
| `TestFailedFlush_E010_NoPartialPublication` | $F_0$ build failure cleans up partial file | 0 readers, 1 frozen | 0 readers, 1 frozen | **PASS** (No partial publication) |
| `TestFailedFlush_E010_WriteConflictDetectionFailure` | $F_0$ (S1=1), $F_1$ (S2=2), Tx at S1 writes | Conflict detected | No conflict, committed! | **FAIL (Defect Reproduced)** |
| `TestFailedFlush_E010_ErrorReportingOnClose` | $F_0$ failure surfaced on `Close()` | `closeErr != nil` | `closeErr != nil` | **PASS** (`Close()` returns err) |

---

## 4. Root Cause

1. **Unconditional worker continuation in `flushLoop`:** The single FIFO worker dequeues subsequent tasks after `flushToDisk` returns an error, without halting or marking the queue paused.
2. **Asymmetric tier membership:** The failed Memtable is retained in `m.Frozenmem`, while successful newer Memtables migrate from `m.Frozenmem` to `m.ActiveSSreaders`.
3. **Hardcoded tier-precedence assumption:** `Manager.Get` and `Manager.latestValueLocked` assume $\text{Tier 2 (Frozenmem)} \succ \text{Tier 3 (ActiveSSreaders)}$ holds for all keys, which is violated whenever an older generation remains in `Frozenmem` while a newer generation is published to `ActiveSSreaders`.
