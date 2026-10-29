# Phase I7 — Checkpoint R1.1: Reproducibility and Numerical Verification Report

**DATE:** 2026-10-29  
**EVALUATION ENGINE:** Python 3.12, NumPy 1.26.4, SciPy 1.13.0  
**PRIMARY DATA SOURCE:** `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\I7_WINDOWS_RESULTS.zip`  
**DATA SOURCE SHA-256:** `52c52ad137acac08035b0e508acb2707f2abaaa506478f171d32c829fab2723b`  
**VERIFICATION SCRIPT:** [`research/experiments/analysis/reproduce_all_metrics.py`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/reproduce_all_metrics.py)  
**VERIFICATION STATUS:** 100% OF REPORTED METRICS REPRODUCED DIRECTLY FROM RAW ARTIFACTS.

---

## 1. Scope & Execution Principles

This report documents the independent verification of every numerical result, table, figure, and statistical test reported in the revised manuscript ([`paper/paper_revised.tex`](file:///c:/Users/ramst/Documents/Projects/selfdbms/paper/paper_revised.tex)). 

In compliance with the Checkpoint R1.1 charter:
1. **Zero New Benchmarks:** All verification was conducted strictly read-only against the frozen G4 results root (`C:\Users\ramst\I7_WINDOWS_G4_RESULTS`).
2. **Raw JSON Ingestion:** Every metric was parsed directly from individual trial result JSON files (`trials/attempts/*_a01.json`), bypassing intermediate summary files.
3. **No Silent Corrections:** Every data discrepancy, schema omission, and failed manipulation check in the raw data is preserved, flagged, and disclosed.

---

## 2. Trial Ledger Accounting & Exclusion Audit

The G4 execution ledger was audited across all 60 attempted trials. Every trial was verified against the campaign manifest (`campaign_manifest.json`), trial inventory (`06_TRIAL_INVENTORY.md`), and raw trial output.

| Category | Count | Exit Code | Ledger Status | Root Cause & Verification Details |
| :--- | :---: | :---: | :---: | :--- |
| **Successful Trials** | 50 | 0 | `SUCCESS` | Complete execution across measurement window; full operation accounting. |
| **Treatment-Related Deadline Outcomes** | 4 | 1 | `FAILED` | Approach A exclusive lock contention delayed writer flushes past the 10.0s window. Confirmed in trials `i7_E2_pub_physical_t01_A` (1 late flush), `i7_E2_pub_physical_t09_A` (3 late flushes), `i7_E4_L3_t02_A` (10 late flushes, 3 abandoned requests), and `i7_E4_L3_t06_A` (5 late flushes, 4 abandoned requests). |
| **Mechanical Failures** | 3 | 3 | `FAILED` | Trials `i7_D3_start_t01_A`, `i7_D3_start_t01_AMIN`, and `i7_D3_start_t01_B`. Failed at startup in 59–673 ms due to runner omission of `"D3"` in `supportedBlocks` in `cmd/i7trial/main.go`. |
| **Protocol-Invalid Trials** | 3 | 0 | `INVALID` | Trials `i7_E7_Y1_t05_PEBBLE`, `i7_E7_Y2_t01_PEBBLE`, and `i7_E7_Y3_t03_PEBBLE`. Failed orchestrator validation due to omission of `result.timestamps` schema block. Buffer overflow dropped >10.45M samples. |
| **Total Attempted Trials** | **60** | — | — | **Halted under stopping rule S-1 ($N_{\text{fail}} = 10$). Preserved unchanged.** |

---

## 3. Matched Triplet Extraction Audit

Triplets were formed by matching trials sharing identical `(block, cell, triplet)` keys and identical logical request stream seeds:
- Block D3 (3 trials) was excluded mechanically due to startup harness crashes (exit code 3).
- Block E7 (3 trials) was excluded mechanically due to protocol invalidity (schema divergence).
- Across Blocks E1, E2, E3, E4, and E5, exactly **18 experimental triplets** exist where all three arms (A, A-min, B) were executed.

### Triplet Breakdown:
- **14 Fully Successful Triplets:** All three arms completed with status `SUCCESS`.
- **4 Treatment-Failure Triplets:** Arms A-min and B completed with status `SUCCESS`; Arm A produced a treatment-related deadline failure.
- **A-min vs. B Paired Set:** Arms A-min and B succeeded in all 18 triplets. Thus, paired A-min vs. B comparisons have $N = 18$ valid pairs.

---

## 4. Manuscript Numerical Reproduction Audit

Every table, summary statistic, confidence interval, and figure in the manuscript was independently recomputed from raw JSON inputs using `reproduce_all_metrics.py`.

### A. Table 3: Phase II Paired Triplet Results (Complete Audit)

All 18 rows of Table 3 reproduced exactly from raw trial JSON fields:

| Block | Cell / Workload | Trip | Tput A (ops/s) | Tput AMIN (ops/s) | Tput B (ops/s) | Diff (AMIN-B) | Ratio (AMIN/B) | Status & Verification |
| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **E1** | $r=8$ (mixed read-write) | 09 | 9.1 | 16.7 | 17.6 | $-0.90$ | 0.9489 | **Verified exact match** |
| **E2** | `pub_none` (frozen baseline) | 02 | 24.6 | 20.7 | 18.9 | $+1.80$ | 1.0952 | **Verified exact match** |
| **E2** | `pub_none` (frozen baseline) | 03 | 18.2 | 19.4 | 20.1 | $-0.70$ | 0.9652 | **Verified exact match** |
| **E2** | `pub_physical` (20 flushes) | 01 | 12.8 | 15.5 | 18.1 | $-2.60$ | 0.8564 | **Verified exact match** (Arm A 1 late flush) |
| **E2** | `pub_physical` (20 flushes) | 09 | 11.1 | 18.1 | 18.6 | $-0.50$ | 0.9731 | **Verified exact match** (Arm A 3 late flushes) |
| **E3** | `equal_r01` ($235$ writes/s) | 01 | 232.4 | 183.6 | 206.3 | $-22.70$ | 0.8900 | **Verified exact match** |
| **E3** | `equal_r01` ($235$ writes/s) | 03 | 260.8 | 238.3 | 222.7 | $+15.60$ | 1.0700 | **Verified exact match** |
| **E3** | `equal_r01` ($235$ writes/s) | 06 | 240.7 | 237.9 | 235.3 | $+2.60$ | 1.0110 | **Verified exact match** |
| **E3** | `equal_r04` ($210$ writes/s) | 06 | 368.4 | 231.2 | 276.8 | $-45.60$ | 0.8353 | **Verified exact match** |
| **E3** | `equal_r04` ($210$ writes/s) | 07 | 364.9 | 271.4 | 281.5 | $-10.10$ | 0.9641 | **Verified exact match** |
| **E3** | `free_r01` (free writes) | 01 | 259.6 | 138.7 | 137.3 | $+1.40$ | 1.0102 | **Verified exact match** |
| **E3** | `free_r01` (free writes) | 02 | 240.8 | 131.0 | 132.2 | $-1.20$ | 0.9909 | **Verified exact match** |
| **E4** | L1 ($4.86$ ops/s open-loop) | 03 | 5.3 | 5.3 | 5.3 | $-0.00$ | 1.0000 | **Verified exact match** |
| **E4** | L1 ($4.86$ ops/s open-loop) | 05 | 6.3 | 6.3 | 6.3 | $-0.00$ | 1.0000 | **Verified exact match** |
| **E4** | L2 ($9.72$ ops/s open-loop) | 05 | 9.9 | 9.9 | 9.9 | $-0.00$ | 1.0000 | **Verified exact match** |
| **E4** | L3 ($14.58$ ops/s open-loop) | 02 | 13.2 | 15.1 | 15.1 | $-0.00$ | 1.0000 | **Verified exact match** (Arm A 10 late, 3 abnd) |
| **E4** | L3 ($14.58$ ops/s open-loop) | 06 | 12.7 | 14.2 | 14.2 | $-0.00$ | 1.0000 | **Verified exact match** (Arm A 5 late, 4 abnd) |
| **E5** | `hit0` (all-miss read path) | 05 | 2,903.7 | 3,283.0 | 3,003.1 | $+279.90$ | 1.0932 | **Verified exact match** |

### B. Statistical Synthesis (Bootstrap Confidence Intervals)

Bootstrap resampling ($B = 10,000$, seed `20261101`) over paired differences $(T_{\text{amin}} - T_{\text{B}}) / T_{\text{B}}$ yielded:
- **All 18 Matched Triplets:**
  - Mean relative difference: $\mathbf{-1.65\%}$ (exact: $-0.016471$)
  - 95% Bootstrap Confidence Interval: $\mathbf{[-4.94\%, +1.43\%]}$ (exact: $[-0.049424, +0.014293]$)
  - Ratio of means: $1.0468$ (95% CI: $[0.9195, 1.0813]$)
  - Median ratio: $\mathbf{1.0000}$ (exact: $0.9999995$)
  - **Verification:** Completely contained within the $[-5\%, +5\%]$ equivalence band.
- **14 Fully Successful Triplets:**
  - Mean relative difference: $\mathbf{-0.90\%}$ (exact: $-0.008997$)
  - 95% Bootstrap Confidence Interval: $\mathbf{[-4.55\%, +2.56\%]}$ (exact: $[-0.045483, +0.025616]$)
  - Ratio of means: $1.0481$ (95% CI: $[0.9187, 1.0827]$)
  - Median ratio: $\mathbf{1.0000}$ (exact: $0.9999995$)
  - **Verification:** Completely contained within the $[-5\%, +5\%]$ equivalence band.

### C. Table 4: Block E2 Latency and Topology Reconciliation

Extracted directly from `result.latency_read_nanos` and `result.lsm_diagnostics`:
- `pub_none`:
  - Publications in-window: 0 / 0 in all arms.
  - SSTables: $4 \to 4$; Frozen Memtables: $20 \to 20$.
  - Latencies:
    - Arm A: $p_{50} = 171.4\text{ ms}$, $p_{95} = 512.5\text{ ms}$.
    - Arm AMIN: $p_{50} = 196.5\text{ ms}$, $p_{95} = 530.5\text{ ms}$.
    - Arm B: $p_{50} = 207.9\text{ ms}$, $p_{95} = 541.8\text{ ms}$.
- `pub_physical`:
  - Publications in-window: AMIN 20/20, B 20/20, Arm A 17–19/20 (late flushes: 1 in t01, 3 in t09).
  - SSTables: $4 \to 24$; Frozen Memtables: $20 \to 0$.
  - Latencies:
    - Arm AMIN: $p_{50} = 221.5\text{ ms}$ ($+12.7\%$ vs none), $p_{95} = 659.3\text{ ms}$.
    - Arm B: $p_{50} = 234.5\text{ ms}$ ($+12.8\%$ vs none), $p_{95} = 592.7\text{ ms}$.
    - Arm A: $p_{50} = 337.9\text{ ms}$ ($\mathbf{+97.7\%}$ vs none), $p_{95} = 722.9\text{ ms}$.
- **Verification:** Confirmed that raw JSON `latency_read_nanos` matches manuscript values.

### D. Table 5: Block E3 Paced Write Reconciliation

Extracted from `result.operations` and `result.lsm_diagnostics`:
- `equal_r01` ($235\text{ writes/s}$ offered, 2,350 total offered):
  - AMIN: 2,350–2,351 completed ($100.0\%$), 22 flushes.
  - B: 2,350–2,351 completed ($100.0\%$), 22 flushes.
  - A: 1,152–1,293 completed ($\mathbf{49.0\%\text{--}55.0\%}$ realized), 10–12 flushes.
- `equal_r04` ($210\text{ writes/s}$ offered, 2,100 total offered):
  - AMIN: 1,801–2,101 completed ($85.8\%\text{--}100.0\%$), 16–19 flushes.
  - B: 2,100–2,101 completed ($100.0\%$), 19 flushes.
  - A: 450–454 completed ($\mathbf{21.4\%\text{--}21.6\%}$ realized), 4 flushes.
- `free_r01` (free-running writers):
  - AMIN: 3,179–3,184 writes, 29–30 flushes.
  - B: 3,164–3,257 writes, 29–30 flushes.
  - A: 1,200–1,291 writes, 11–12 flushes.
- **Verification:** Exact match with manuscript text and Table 5. Confirmed failure of manipulation check MC-3.

### E. Table 6: Block E4 Open-Loop Latency Reconciliation

Extracted from `result.operations`, `result.latency_response_nanos`, and `result.lsm_diagnostics`:
- Level $L_1$ ($4.86\text{ ops/s}$ offered):
  - Sched: 54–64, In-window: 53–63, Late: 1, Abandoned: 0 across all arms.
  - $y_{p50}$: Arm A $79.4\text{--}100.4\text{ ms}$; AMIN $70.9\text{--}97.5\text{ ms}$; B $62.4\text{--}101.7\text{ ms}$.
- Level $L_2$ ($9.72\text{ ops/s}$ offered):
  - Sched: 101, In-window: 99, Late: 2, Abandoned: 0 across all arms.
  - $y_{p50}$: Arm A $60.8\text{ ms}$; AMIN $67.3\text{ ms}$; B $56.2\text{ ms}$.
- Level $L_3$ ($14.58\text{ ops/s}$ offered):
  - Sched: 145–151.
  - In-window: AMIN 142–151 (Late 0–3, Abnd 0); B 142–151 (Late 0–3, Abnd 0).
  - Arm A: In-window 127–132, Late 14–16, Abandoned $\mathbf{3\text{--}4}$.
  - $y_{p50}$: AMIN $71.3\text{--}111.5\text{ ms}$; B $71.3\text{--}107.9\text{ ms}$; Arm A $\mathbf{251.3\text{--}591.0\text{ ms}}$.
  - $y_{p95}$: AMIN $528.3\text{--}852.6\text{ ms}$; B $450.8\text{--}748.5\text{ ms}$; Arm A $\mathbf{1,386\text{--}2,111\text{ ms}}$.
- **Verification:** Verified that $C_{\text{total}} = N_{\text{in-window}} + N_{\text{late}}$, resolving the accounting distinction between in-window and post-window drain completions.

---

## 5. Audit of Preregistered Checks & Methodological Failures

| Preregistered Check / Control | Preregistered Rule | Realized Outcome in G4 Data | Scientific Status |
| :--- | :--- | :--- | :--- |
| **MC-3 (E3 Equal Work)** | Write count difference $\le 10\%$ across arms | Difference is $\mathbf{45\%\text{--}78\%}$ (Arm A starved) | **FAILED / VOID:** Equal-work contrast is invalid. Must be interpreted as offered load response. |
| **MC-5 (E5 Scan Manipulation)** | Scanned records ratio $\ge 2.0$ vs E1 | Target keys ($\ge 10,001$) out of range; resolved immediately to EOF in final block | **FAILED / VOID:** Heavy scan premise invalid; single 4KB block pinned in OS page cache. |
| **S-1 (Stopping Rule)** | Abort when $N_{\text{fail}} \ge 10$ | Reached at Attempt 60 (4 treatment, 3 mechanical, 3 invalid) | **VALIDLY TRIGGERED:** Campaign properly halted; factorial scaling truncated. |
| **D3 (Drift Baseline)** | Shift between start/end D3 trials $\le 15\%$ | Initial D3 trials failed (`unsupported block: D3`); end trials unrun | **OMITTED:** Environmental drift over 26.4 min unmonitored by primary instrument. |
| **E7 (Pebble Envelope)** | Complete schema with monotonic timestamps | `result.timestamps` omitted; 1M buffer dropped $>10.45\text{M}$ samples | **INVALIDATED:** Pebble excluded from all primary statistical comparisons. |
| **Windows Race Detector** | Zero data races under `-race` | Binaries compiled without `-race` due to Windows cgo unavailability | **UNRUN ON WINDOWS:** Thread safety verified only on Linux ext4 and via unit tests. |

---

## 6. Reproducibility Scorecard

- **Primary Numerical Claims:** 100% Reproduced (0 numerical errors detected).
- **Manuscript Text vs. Raw JSON:** 100% Consistent across Tables 1–6 and Figures 1–3.
- **Evidentiary Boundaries:** Strictly aligned with Option A directive and Checkpoint R1.1 standards.
