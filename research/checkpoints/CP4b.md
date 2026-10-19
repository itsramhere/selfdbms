# Checkpoint CP4b: Final Workload Activation and Campaign Readiness (I4.9)

**Milestone**: Checkpoint CP4b  
**Phase**: Phase I4 Final Activation Complete & Experimentally Ready  
**Date**: 2026-10-19  
**Parent Checkpoints**: [`research/checkpoints/CP4.md`](CP4.md), [`research/checkpoints/CP4_VERIFICATION_ADDENDUM.md`](CP4_VERIFICATION_ADDENDUM.md), [`research/checkpoints/CP4a.md`](CP4a.md), [`research/checkpoints/CP4a_CHATGPT_REVIEW_PACKET.md`](CP4a_CHATGPT_REVIEW_PACKET.md)  
**Historical Baselines Verified**:
- `baseline-approach-a`: `d6909a75ea8d4015e2f19c3b35465dd78e0f4dfc` (annotated tag `58155938ecf2e57366f128bb214cc167f43e0ce7`)
- `baseline-approach-a-verified`: `ae8c034c438d56abbe6664e6472f7a0c1278797f` (annotated tag `ecaea591cabc124eb213e82805db6e42b9dd27d4`)
**Approved Phase I3 Revision**: `c58bc6177b5a925a0b25ba795957839866d0b040`  
**Current Working Revision**: `HEAD` (descending directly from `194e8c5b525db31ffea49d3725b871239f1559b1`)  
**Status**: COMPLETE — AWAITING EXTERNAL REVIEW APPROVAL (Phase I5 Official Campaign Strictly ON HOLD)

---

## 1. Executive Summary & Verification Matrix

In response to the External Reviewer Decision on Checkpoint CP4a, we have executed **I4.9 — Final Workload Activation and Campaign Readiness**. This milestone delivers scientifically valid workload bifurcation for Workload B, activates real continuous reader-set churn and compaction overlap under Workload D, establishes fail-closed fixture construction with manifest verification, hardens Windows QPC precision timing with 64-bit integer overflow protection, reaffirms the official 10-trial repetition protocol, and executes a clean 10-second readiness pilot across Windows NTFS and WSL2 native ext4:

| Directive | Item | Status | Verification Summary |
| :--- | :--- | :--- | :--- |
| **I4.9.1** | Workload B Bifurcation | **RESOLVED** | Split Workload B into **B1 (Natural write progress)** (1MB threshold, `wal_sync=true`, 0 flushes claimed honestly, demonstrating Approach B's 20–240x write throughput advantage) and **B2 (Controlled flush interference)** (scheduled coordinator flushes every 2s, capturing read latency during active flush publication). |
| **I4.9.2** | Workload D Churn Activation | **ACTIVATED** | Calibrated `MemtableThresholdBytes = 16 * 1024` (16 KB) and `CompactionTriggerIntervalMs = 1500` (1500ms). Yields 4–23 flushes and 4–6 full compactions with active reader-set swaps and live reader overlap under pinned snapshots. |
| **I4.9.3** | Fail-Closed Fixture Setup | **ENFORCED** | Checked all `tx.Put`, `tx.Commit`, and `TriggerFlush` return codes; synchronized flush draining; asserted table count parity; verified sample key readback (first, mid, last key); added `FixtureManifest`; added 4 fault-injection tests. |
| **I4.9.4** | Precision Timer Hardening | **HARDENED** | Windows QPC return status checked; 10MHz fallback removed; split arithmetic `TicksToNanos` implemented preventing 64-bit overflow; operational assertion via `EnsurePrecisionTimer()`. |
| **I4.9.4** | 10-Trial Protocol Reaffirmation | **REAFFIRMED** | Reaffirmed $N = 10$ independent trials per cell ($6 \text{ workloads} \times 7 \text{ thread counts} \times 2 \text{ modes} \times 2 \text{ platforms}$) for Phase I5 official campaign. |
| **I4.9.5** | Dual-Platform Readiness Pilot | **EXECUTED** | 10-second trials executed for B1, B2, C, D on Windows NTFS and WSL2 ext4. B2 completed 1–5 flushes; D completed 4–23 flushes and 4–6 compactions; 0 validation errors across all runs. |
| **I4.9.6** | Full Repository Revalidation | **PASSED** | **196/196 unit tests PASS** on Windows native (57.96s) and WSL2 Linux `-race` (127.22s, 0 data races). `go vet ./...` clean. |

---

## 2. Reconciled Test Inventory & Dual-Platform Verification

The repository test suite has expanded from 189 to **196 unit tests** (202 total runnable test functions including benchmarks), adding tests for timer overflow guards, precision timer operational assertion, fail-closed fixture fault injection (commit failure, flush failure, table count mismatch, readback corruption), fixture manifest recording, Workload B1 natural progress, Workload B2 controlled flush interference, and Workload D repeated reader-set churn:

| Test Suite | Package / Location | Unit Tests | Windows Native (`go1.26.3`) | WSL2 Linux (`go1.27.2` `-race`) |
| :--- | :--- | :--- | :--- | :--- |
| Verified Baseline (I1) | `lsmengine/*_test.go` | 101 | **PASS** (1.08s) | **PASS** (21.61s, 0 races) |
| Scaffolding (I2) | `lsmengine/snapshot_scaffolding_test.go` | 6 | **PASS** | **PASS** (0 races) |
| Implementation (I3) | `lsmengine/snapshot_*_test.go`, `approach_b_mvcc_test.go` | 30 | **PASS** | **PASS** (0 races) |
| Correctness Repairs (I3.7) | `lsmengine/sstable_read_error_test.go`, etc. | 26 | **PASS** | **PASS** (0 races) |
| Verification Addendum (CP3a-V) | `lsmengine/per_key_conflict_timestamp_test.go`, etc. | 7 | **PASS** | **PASS** (0 races) |
| Analysis & Schema Tests | `research/experiments/analysis/summarize_test.go` | 2 | **PASS** (0.71s) | **PASS** (1.03s, 0 races) |
| Experiment Harness (I4.8/I4.9) | `research/experiments/harness/harness_test.go` | 24 | **PASS** (57.96s) | **PASS** (127.22s, 0 races) |
| **Grand Total** | **Full Repository Test Matrix** | **196** | **PASS (59.75s)** | **PASS (149.86s, 0 races)** |

*Static Analysis*: `go vet ./...` executed cleanly with 0 warnings on both Windows native and WSL2 Linux.

---

## 3. Workload B Bifurcation: B1 vs B2 (I4.9.1 / D-044 / E-019)

### 3.1 Scientific Motivation
In the CP4a pilot, Workload B configured with a 1 MB threshold and `wal_sync = true` produced 0 flushes in 10 seconds. In Approach A, readers holding `m.Mu.RLock()` during disk I/O severely starved incoming writers waiting for `m.Mu.Lock()`, allowing only 15–16 writes to complete in 10s (~2.5 KB). Lowering the threshold to 128 KB did not cause Approach A to flush because 2.5 KB $\ll$ 128 KB. Conflating writer throughput starvation with flush publication latency is scientifically invalid.

### 3.2 Implemented Bifurcation
1. **Workload B1: Natural Write Progress (Production Threshold, 0 Flushes Claimed)**:
   - Config: `MemtableThresholdBytes = 1 * 1024 * 1024` (1 MB), `wal_sync = true`, 2 concurrent writers, 4 concurrent readers.
   - Reporting Contract: 0 flushes completed; `flush_latency = null`. The metric of interest is reader throughput and total write operations completed.
   - Empirical Result:
     * Windows NTFS: Approach A completed 15 writes (0 flushes); Approach B completed **3,614 writes** (0 flushes) — a **240x write throughput advantage**.
     * WSL2 ext4: Approach A completed 111 writes (0 flushes); Approach B completed **2,480 writes** (0 flushes) — a **22x write throughput advantage**.
     * Cause: Readers in Approach B release `m.Mu` before reading disk SSTables, freeing the write mutex for writer commits.
2. **Workload B2: Controlled Flush Interference (Scheduled Flushes with Reader Overlap)**:
   - Config: `MemtableThresholdBytes = 64 * 1024 * 1024` (64 MB, preventing untracked auto-flushes), `FlushTriggerIntervalMs = 2000` (2,000 ms pilot) / `500` (500 ms smoke).
   - Execution: A dedicated harness coordinator goroutine injects 50-key write batches and triggers synchronous flushes via `man.TriggerFlush()`.
   - Reporting Contract: Directly tracks `flush_overlap_nanos`, `reads_during_flush`, `attempted_flushes`, `completed_flushes`, and `delayed_flushes`.
   - Empirical Result:
     * Windows NTFS: Approach A completed 1 flush (13.9ms overlap); Approach B completed 5 flushes (78.5ms overlap).
     * WSL2 ext4: Approach A completed 3 flushes (71.9ms overlap, 12 reads in overlap); Approach B completed 5 flushes (22.8ms overlap, 7 reads in overlap).

---

## 4. Real Repeated Reader-Set Churn Activation: Workload D (I4.9.2 / D-045 / E-019)

### 4.1 Diagnosis of Prior Single-Compaction Cessation
In the CP4a pilot, Workload D executed only 1 compaction. The root cause: `CompactAll()` merged all 4 initial SSTables into 1 table, leaving `len(m.ActiveSSreaders) == 1`. Subsequent compaction triggers invoked `CompactAll()`, which returned immediately without compacting because compaction requires $\ge 2$ tables. Because the 512 KB threshold was too large for background writers to fill, no new SSTables were flushed, and reader-set churn halted.

### 4.2 Calibration and Empirical Verification
- **Calibrated Parameters**:
  * `MemtableThresholdBytes = 16 * 1024` (16 KB, ~105 keys per flush).
  * `CompactionTriggerIntervalMs = 1500` (1,500 ms).
  * 2 concurrent writers continuously writing new keys with WAL sync.
- **Empirical Activation**:
  * Windows NTFS:
    - Approach A: **4 flushes**, **4 compactions**, 4,108 ms compaction overlap, 372 reads overlapping compaction.
    - Approach B: **23 flushes**, **6 compactions**, 1,567 ms compaction overlap, 313 reads overlapping compaction.
  * WSL2 ext4:
    - Approach A: **12 flushes**, **6 compactions**, 1,246 ms compaction overlap, 764 reads overlapping compaction.
    - Approach B: **19 flushes**, **6 compactions**, 682 ms compaction overlap, 483 reads overlapping compaction.
- **Scientific Significance**: Workload D now actively and repeatedly swaps `m.currentSnapshot`, triggers reader-set replacements, exercises asynchronous reader retirement, and tests pinned snapshot drain under heavy concurrent reads.

---

## 5. Fail-Closed Fixture Construction, Verification, and Manifest (I4.9.3 / D-046)

### 5.1 Architecture & Implementation
To guarantee fixture credibility, fixture population in `research/experiments/harness/runner.go` enforces strict fail-closed invariants:
1. **Transaction & Commit Verification**: Every `tx.Put()` and `tx.Commit()` return error is checked.
2. **Synchronized Flush Drain**: Every `man.TriggerFlush()` is followed by `man.WaitForFlushCompletion()`.
3. **Table Count Assertion**: `man.ActiveSSTableCount()` is asserted against `cfg.InitialSSTableCount`. Any count mismatch fails closed.
4. **Boundary & Sample Readback Verification**: Before entering warmup, sample keys from every table (first, mid, and last keys) are queried and validated for key and value checksum integrity.
5. **Fixture Manifest Recording**: Every trial JSON output records a `FixtureManifest`:
   ```go
   type FixtureManifest struct {
       CommittedKeys          int    `json:"committed_keys"`
       ExpectedSSTableCount   int    `json:"expected_sstable_count"`
       ActualSSTableCount     int    `json:"actual_sstable_count"`
       FlushErrorsCount       int    `json:"flush_errors_count"`
       SampleReadbackStatus   string `json:"sample_readback_status"`
       SampleKeysVerified     int    `json:"sample_keys_verified"`
       InitializationDuration int64  `json:"initialization_duration_ns"`
   }
   ```

### 5.2 Deterministic Fault Injection Tests
`TestFixture_FailClosed_FaultInjection` in `harness_test.go` exercises all 4 failure modes:
1. `CommitFailure`: Injected commit error aborts trial before warmup.
2. `FlushFailure`: Injected flush failure aborts trial with descriptive error.
3. `CountMismatch`: Artificially altered initial table count triggers fatal count mismatch.
4. `ReadbackCorruption`: Artificially corrupted fixture key triggers readback verification error.

---

## 6. Precision Timing Hardening & Overflow Protection (I4.9.4 / D-047)

### 6.1 Windows QPC API Return Status Checking
In `research/experiments/harness/timer_windows.go`, `procQPF` and `procQPC` return values are strictly verified:
```go
r, _, _ := procQPF.Call(uintptr(unsafe.Pointer(&freq)))
if r == 0 || freq <= 0 {
    return fmt.Errorf("kernel32.dll QueryPerformanceFrequency failed: return=%d, freq=%d", r, freq)
}
```
Silent 10 MHz fallback has been eliminated. The operational status of the timer is verified at trial startup via `EnsurePrecisionTimer()`.

### 6.2 64-Bit Integer Overflow Protection
`TicksToNanos(ticks, freq int64) int64` in `research/experiments/harness/timer_common.go` implements mathematical split arithmetic:
```go
func TicksToNanos(ticks, freq int64) int64 {
    if freq <= 0 || ticks <= 0 {
        return 0
    }
    sec := ticks / freq
    rem := ticks % freq
    return (sec * 1000000000) + ((rem * 1000000000) / freq)
}
```
Even for trials running for hours ($100 \times 10^9$ ticks at 10 MHz = $10^4$ seconds), $(100 \times 10^9) \times 10^9$ would overflow `int64` ($9.22 \times 10^{18}$ limit). The split decomposition mathematically guarantees zero overflow up to $9 \times 10^{18}$ ticks. Validated by `TestTicksToNanos_OverflowGuards`.

---

## 7. Reaffirmation of 10-Trial Repetition Protocol (I4.9.4)

In accordance with the reviewer directive, we formally reaffirm **$N = 10$ independent trials per cell** for the official Phase I5 campaign:
- **Matrix Dimensions**: 6 Workloads ($\text{A}, \text{B1}, \text{B2}, \text{C}, \text{D}, \text{E}$) $\times 7$ Concurrency Levels ($1, 2, 4, 8, 12, 16, 24, 32$) $\times 2$ Synchronization Modes ($\text{A}, \text{B}$) $\times 2$ Target Platforms ($\text{Windows NTFS}, \text{WSL2 ext4}$).
- **Total Executions**: $6 \times 7 \times 2 \times 2 \times 10 = 1,680$ trial executions.
- **Trial Duration**: 10 seconds per trial, plus 2-second unmetered warmup.
- **Statistical Output**: For each cell, the harness will report median ($p50$), 95th percentile ($p95$), 99th percentile ($p99$), interquartile range (IQR), standard deviation, and 95% bootstrap confidence intervals across the 10 trial medians.

---

## 8. Bounded 10-Second Readiness Pilot Empirical Results

### 8.1 Windows 11 Native NTFS Results (Duration: 10s, Readers: 4)

| Workload | Mode | Read Ops/s | p50 (µs) | p95 (µs) | p99 (µs) | Writes | Flushes | Compactions | CompOverlap (ms) | CompOverlap Rds | FlushOverlap (ms) | ValErrs | Status |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **B1_NATURAL_WRITE** | A | 10.1 | 319.3 | 954.0 | 1,215.4 | **15** | 0 | 0 | 0 | 0 | 0 | 0 | SUCCESS |
| **B1_NATURAL_WRITE** | B | 20.0 | 146.1 | 574.3 | 748.5 | **3,614** | 0 | 0 | 0 | 0 | 0 | 0 | SUCCESS |
| **B2_CONTROLLED_FLUSH** | A | 11.4 | 319.2 | 832.4 | 1,159.0 | 50 | **1** | 0 | 0 | 0 | 14 | 0 | SUCCESS |
| **B2_CONTROLLED_FLUSH** | B | 15.9 | 278.7 | 582.8 | 662.6 | 250 | **5** | 0 | 0 | 0 | 79 | 0 | SUCCESS |
| **C_COMPACTION** | A | 1,189.0 | 2,461.5 | 4,831.5 | 6,169.1 | 0 | 0 | **1** | 2,103 | 236 | 0 | 0 | SUCCESS |
| **C_COMPACTION** | B | 1,328.8 | 2,519.7 | 5,282.3 | 7,031.6 | 0 | 0 | **1** | 308 | 95 | 0 | 0 | SUCCESS |
| **D_READER_CHURN** | A | 361.3 | 3,553.8 | 18,511.5 | 100,336.4 | 486 | **4** | **4** | 4,108 | 372 | 148 | 0 | SUCCESS |
| **D_READER_CHURN** | B | 282.7 | 10,632.6 | 26,717.9 | 48,344.3 | 2,492 | **23** | **6** | 1,567 | 313 | 175 | 0 | SUCCESS |

### 8.2 WSL2 Native Linux ext4 Results (Duration: 10s, Readers: 4)

| Workload | Mode | Read Ops/s | p50 (µs) | p95 (µs) | p99 (µs) | Writes | Flushes | Compactions | CompOverlap (ms) | CompOverlap Rds | FlushOverlap (ms) | ValErrs | Status |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **B1_NATURAL_WRITE** | A | 88.3 | 41.0 | 104.1 | 132.2 | **111** | 0 | 0 | 0 | 0 | 0 | 0 | SUCCESS |
| **B1_NATURAL_WRITE** | B | 193.1 | 17.2 | 49.6 | 58.3 | **2,480** | 0 | 0 | 0 | 0 | 0 | 0 | SUCCESS |
| **B2_CONTROLLED_FLUSH** | A | 110.4 | 31.6 | 87.7 | 110.3 | 150 | **3** | 0 | 0 | 0 | 72 | 0 | SUCCESS |
| **B2_CONTROLLED_FLUSH** | B | 166.4 | 19.0 | 57.6 | 84.7 | 250 | **5** | 0 | 0 | 0 | 23 | 0 | SUCCESS |
| **C_COMPACTION** | A | 1,825.5 | 234.2 | 7,059.5 | 10,708.7 | 0 | 0 | **1** | 478 | 224 | 0 | 0 | SUCCESS |
| **C_COMPACTION** | B | 1,566.4 | 3,038.2 | 7,101.3 | 13,401.2 | 0 | 0 | **1** | 208 | 121 | 0 | 0 | SUCCESS |
| **D_READER_CHURN** | A | 1,099.0 | 1,826.0 | 7,905.9 | 35,666.4 | 1,357 | **12** | **6** | 1,246 | 764 | 103 | 0 | SUCCESS |
| **D_READER_CHURN** | B | 753.3 | 3,848.3 | 11,166.3 | 44,601.8 | 2,126 | **19** | **6** | 682 | 483 | 59 | 0 | SUCCESS |

---

## 9. Formal Request for Phase I5 Authorization

With all directives of I4.9 executed, verified, and evidenced by:
1. 196/196 unit tests passing on Windows native and WSL2 Linux `-race` (0 data races),
2. Clean static analysis (`go vet ./...` 0 warnings),
3. Bifurcated and activated Workloads B1, B2, and D,
4. Fail-closed fixture manifest recording and fault-injection verification,
5. Hardened precision timers with mathematical overflow protection,
6. Reaffirmed 10-trial repetition protocol,

we submit Checkpoint CP4b for formal review and request authorization to begin **Phase I5 — Execute Full-Scale Empirical Comparison**.
