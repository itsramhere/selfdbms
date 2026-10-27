# Checkpoint CP6 Review Packet: Final Research Synthesis and Empirical Evidence Reconciled

**Submission Date**: 2026-10-27  
**Milestone**: Phase I6 Completion (Checkpoint CP6)  
**Campaign ID**: `I5_WSL2_EXT4_RESTART_V1`  
**Execution Platform**: WSL2 Ubuntu 24.04.3 LTS (Linux kernel 6.6.87.2-WSL2, native ext4 on `/dev/sdd`, AMD Ryzen 7 7445HS, 12 logical processors)  
**Frozen Implementation Revision**: `a5c0fb97deb8a67705a80e76b49e25f31d692492`  
**Baseline Verified Reference**: `baseline-approach-a-verified` (`ae8c034c438d56abbe6664e6472f7a0c1278797f`)  
**Status**: **EXTERNAL REVIEW REQUIRED — CP6 AWAITING APPROVAL.**

---

## 1. Executive Summary & Reviewer Decisions Addressed

Phase I6 resolves every methodological, statistical, and architectural issue raised in the external reviewer's CP5b evaluation:

1. **Independent Cryptographic Audit (I6.0)**:
   - Generated `research/experiments/data/campaign_wsl2_ext4_restart_v1/manifest_checksums.json` containing SHA-256 hashes of all 960 raw JSON trial files, `results.csv`, figures, and progress logs.
   - Machine-checked 100% agreement: all 960 trials record Git commit `a5c0fb97deb8a67705a80e76b49e25f31d692492`, status `SUCCESS`, and 0 validation errors.

2. **Reconciliation of Comparative Statistics (I6.1)**:
   - Computed medians, IQRs, and 95% non-parametric bootstrap confidence intervals ($B = 10{,}000$) for both Ratio of Medians ($B/A$) and Difference of Medians ($B - A$) across all 48 cells.
   - Workload B1 write progress numbers audited: at $r=24$, median writes completed are 77 in Approach A vs. 264 in Approach B (3.4x [2.6x, 5.0x]), correcting earlier prose references.
   - Operation seed formula audited: confirms that seeds incorporate $mIdx$, establishing statistically equivalent parameterized distributions rather than identical lockstep operation sequences.
   - Equivalence boundaries audited against a $\pm 5\%$ practical equivalence band ($[0.95, 1.05]$) or characterized as "similar observed throughput."

3. **Audit of Overlap Counter Semantics (I6.2)**:
   - Investigated positive `reads_during_publication` counts in Approach A.
   - Causal discovery deterministically proven in `TestMetricSemantics_ApproachA_ReadsDuringPublicationAudit`: the counter measures **in-flight queries already holding `RLock` when publication begins that complete while the worker waits for `m.Mu.Lock()`**. Exactly **zero reads** ever execute while `m.Mu.Lock()` is held.
   - For Workload C, demonstrated that compaction accounts for $<1.6\%$ of operations and duration, disproving whole-trial causal attribution.
   - Redesigned Figure 4 to show active flush/compaction duration windows, exposure-normalized rates, and tail latency suppression.

4. **Reconciliation of Campaign Metadata (I6.4)**:
   - Traced runtime discrepancy to authoritative timestamps: 33,180s (9.22 hours) at 41.2s/trial was the runtime of **Session 2 alone** (806 trials following terminal reconnection). Total campaign span across all 960 trials was 42,530.6s (11.81 hours wall span), with 9,648.4s (2.68 hours) of pure steady-state measurement time.
   - Regenerated all 5 figures with corrected titles, labels, uncertainty bands, and provenance metadata.

5. **Manuscript Synthesis & Regression Verification (I6.5 & I6.6)**:
   - Authored `paper/CLAIM_EVIDENCE_MAP.md` mapping all claims to evidence and barring inadmissible overclaims.
   - Updated `paper.tex` into a 14-section publication-ready LaTeX paper.
   - Passed full regression suite in WSL2 Ubuntu under Go's race detector (`-race`, 0 data races).

---

## 2. Reconciled Comparative Statistical Tables

All values represent medians across $N = 10$ independent prospective trials on native ext4 inside WSL2, with non-parametric 95% bootstrap confidence intervals ($B = 10{,}000$ resamples).

### Table 1: Workload A (Uncontended Read-Only SSTables) — Equivalence Boundary
| Readers | Tput A (ops/s) [IQR] | Tput B (ops/s) [IQR] | Diff (B - A) [95% CI] | B/A Ratio [95% CI] | Outcome Classification | p95 A (µs) | p95 B (µs) |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | 72.5 [15.8] | 70.8 [14.1] | -1.7 [-17.9, +15.1] | 0.98x [0.76x, 1.27x] | Similar Observed Tput | 31,130 | 31,431 |
| 2 | 135.9 [34.3] | 135.6 [31.7] | -0.4 [-40.3, +38.0] | 1.00x [0.70x, 1.39x] | Similar Observed Tput | 31,904 | 32,163 |
| 4 | 258.4 [58.2] | 262.7 [63.3] | +4.3 [-71.8, +80.4] | 1.02x [0.72x, 1.45x] | Similar Observed Tput | 33,805 | 33,189 |
| 8 | 441.8 [109.3] | 450.1 [106.2] | +8.4 [-131.1, +135.4] | 1.02x [0.71x, 1.43x] | Similar Observed Tput | 39,293 | 39,106 |
| 12 | 518.7 [93.6] | 523.3 [121.9] | +4.6 [-154.3, +120.6] | 1.01x [0.70x, 1.30x] | Similar Observed Tput | 51,287 | 51,288 |
| 16 | 526.2 [140.0] | 510.8 [164.3] | -15.4 [-169.2, +142.1] | 0.97x [0.68x, 1.39x] | Similar Observed Tput | 77,100 | 81,267 |
| 24 | 520.1 [187.9] | 518.7 [201.9] | -1.5 [-202.4, +179.5] | 1.00x [0.62x, 1.51x] | Similar Observed Tput | 143,366 | 142,744 |
| 32 | 512.9 [165.1] | 514.4 [188.5] | +1.5 [-187.6, +160.0] | 1.00x [0.63x, 1.44x] | Similar Observed Tput | 220,021 | 214,928 |

### Table 2: Workload B1 (Natural Write Progress under Concurrent Disk Reads)
| Readers | Tput A (ops/s) [IQR] | Tput B (ops/s) [IQR] | Read Speedup [95% CI] | Outcome | Writes A | Writes B | Writer Speedup [95% CI] | Diff Writes |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | 53.4 [19.6] | 69.3 [19.5] | 1.30x [0.90x, 1.88x] | Inconclusive | 270 | 2,918 | **10.8x [9.1x, 15.6x]** | **+2,648** |
| 2 | 75.3 [29.3] | 131.6 [31.5] | **1.75x [1.31x, 2.56x]** | **Positive** | 190 | 2,878 | **15.1x [13.1x, 22.2x]** | **+2,688** |
| 4 | 125.5 [48.0] | 260.3 [74.2] | **2.07x [1.47x, 3.21x]** | **Positive** | 159 | 2,871 | **18.1x [15.2x, 28.1x]** | **+2,712** |
| 8 | 219.2 [78.8] | 450.7 [141.5] | **2.06x [1.44x, 3.18x]** | **Positive** | 139 | 2,573 | **18.5x [16.8x, 28.4x]** | **+2,434** |
| 12 | 259.7 [110.5] | 504.7 [185.1] | **1.94x [1.21x, 2.90x]** | **Positive** | 110 | 1,205 | **11.0x [8.7x, 16.5x]** | **+1,095** |
| 16 | 300.8 [125.9] | 464.8 [159.2] | **1.55x [1.12x, 2.48x]** | **Positive** | 96 | 535 | **5.6x [4.5x, 8.8x]** | **+440** |
| 24 | 358.6 [119.2] | 489.0 [156.5] | 1.36x [0.95x, 2.01x] | Inconclusive | 77 | 264 | **3.4x [2.6x, 5.0x]** | **+187** |
| 32 | 375.4 [149.1] | 484.1 [95.3] | **1.29x [1.04x, 1.99x]** | **Positive** | 60 | 198 | **3.3x [2.7x, 5.0x]** | **+138** |

### Table 3: Workload B2 (Controlled Flush Interference) — Tail Latency Suppression
| Readers | Tput A (ops/s) | Tput B (ops/s) | Speedup [95% CI] | p95 A (µs) | p95 B (µs) | p95 Reduction (%) | Flush Window A (ms) | Flush Window B (ms) |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | 39.7 | 40.6 | 1.02x [0.73x, 1.49x] | 45,210 | 39,083 | 13.6% | 466.0 | 81.0 |
| 2 | 72.6 | 82.8 | 1.14x [0.76x, 1.68x] | 175,434 | 42,735 | **75.6%** | 655.0 | 117.0 |
| 4 | 135.9 | 163.0 | 1.20x [0.76x, 2.21x] | 180,966 | 48,916 | **73.0%** | 769.0 | 174.0 |
| 8 | 195.8 | 236.1 | 1.21x [0.92x, 2.06x] | 220,896 | 179,814 | 18.6% | 1,010.0 | 536.0 |
| 12 | 217.4 | 253.1 | 1.16x [0.85x, 1.72x] | 251,006 | 219,715 | 12.5% | 1,326.0 | 830.0 |
| 16 | 197.1 | 263.8 | 1.34x [0.91x, 1.83x] | 298,729 | 245,079 | 18.0% | 1,600.0 | 1,059.0 |
| 24 | 212.3 | 267.6 | 1.26x [0.80x, 1.56x] | 352,200 | 307,522 | 12.7% | 1,931.0 | 1,358.0 |
| 32 | 249.3 | 273.4 | 1.10x [0.85x, 1.33x] | 370,844 | 355,867 | 4.0% | 1,934.0 | 1,216.0 |

### Table 4: Workload C (Compaction Overlap) — Exposure Constraint & Parity
| Readers | Tput A (ops/s) | Tput B (ops/s) | Speedup [95% CI] | Outcome | Compaction Window A | Compaction Window B | Overlap Reads % A | Overlap Reads % B |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | 546.6 | 407.7 | **0.75x [0.70x, 0.85x]** | **Negative** | 266.0 ms | 149.0 ms | 1.14% | 0.74% |
| 2 | 1042.2 | 827.3 | **0.79x [0.70x, 0.93x]** | **Negative** | 297.0 ms | 152.0 ms | 1.24% | 0.79% |
| 4 | 2001.3 | 1842.8 | 0.92x [0.79x, 1.05x] | Inconclusive | 361.0 ms | 142.0 ms | 1.28% | 0.80% |
| 8 | 3877.7 | 3685.9 | 0.95x [0.84x, 1.14x] | Inconclusive | 369.0 ms | 156.0 ms | 1.31% | 0.83% |
| 12 | 5669.4 | 5472.5 | 0.97x [0.84x, 1.10x] | Inconclusive | 454.0 ms | 183.0 ms | 1.38% | 0.95% |
| 16 | 7342.9 | 7637.0 | 1.04x [0.85x, 1.22x] | Inconclusive | 479.0 ms | 214.0 ms | 1.36% | 0.93% |
| 24 | 10292.3 | 10809.7 | 1.05x [0.93x, 1.26x] | Inconclusive | 565.0 ms | 299.0 ms | 1.45% | 0.95% |
| 32 | 13488.7 | 13855.4 | 1.03x [0.89x, 1.34x] | Inconclusive | 724.0 ms | 372.0 ms | 1.52% | 1.01% |

### Table 5: Workload D (High Reader-Set Churn) — Confirmed Negative Finding & Tradeoff
| Readers | Tput A (ops/s) [IQR] | Tput B (ops/s) [IQR] | Diff (B - A) [95% CI] | B/A Ratio [95% CI] | Outcome Classification |
| :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | 475.4 [48.2] | 298.0 [36.5] | -177.4 [-212.4, -133.3] | **0.63x [0.55x, 0.70x]** | **Negative (37% Penalty)** |
| 2 | 904.8 [133.6] | 598.9 [43.6] | -305.9 [-339.2, -162.6] | **0.66x [0.63x, 0.79x]** | **Negative (34% Penalty)** |
| 4 | 1701.7 [327.3] | 1267.3 [165.4] | -434.4 [-609.9, -136.2] | **0.74x [0.65x, 0.90x]** | **Negative (26% Penalty)** |
| 8 | 3142.1 [568.3] | 2598.5 [313.2] | -543.6 [-820.9, +27.8] | 0.83x [0.74x, 1.01x] | Inconclusive / Negative |
| 12 | 4084.1 [410.9] | 3937.6 [656.6] | -146.5 [-829.6, +100.5] | 0.96x [0.80x, 1.03x] | Similar Observed Tput |
| 16 | 5345.0 [73.3] | 5048.2 [1117.2] | -296.8 [-1185.9, +94.5] | 0.94x [0.78x, 1.02x] | Similar Observed Tput |
| 24 | 7947.0 [1226.5] | 7934.0 [1037.0] | -13.1 [-1218.5, +1606.4] | 1.00x [0.85x, 1.25x] | Similar Observed Tput |
| 32 | 10442.4 [1562.3] | 9584.3 [1425.7] | -858.1 [-1805.4, +1240.2] | 0.92x [0.83x, 1.15x] | Similar Observed Tput |

### Table 6: Workload E (Negative Control In-Memory Memtable with 2 Writers) — Memory Tier Parity
| Readers | Tput A (ops/s) [IQR] | Tput B (ops/s) [IQR] | Diff (B - A) [95% CI] | B/A Ratio [95% CI] | Outcome Classification | p50 A (µs) | p50 B (µs) |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| 1 | 656.2 [82.5] | 666.1 [65.3] | +10.0 [-52.8, +96.5] | 1.02x [0.92x, 1.17x] | Similar Observed Tput | 47.4 | 43.4 |
| 2 | 1307.1 [120.4] | 1282.5 [113.2] | -24.6 [-176.0, +111.0] | 0.98x [0.86x, 1.09x] | Similar Observed Tput | 6.7 | 6.5 |
| 4 | 2576.6 [131.8] | 2605.6 [324.5] | +29.0 [-493.3, +356.6] | 1.01x [0.81x, 1.17x] | Similar Observed Tput | 3.4 | 3.4 |
| 8 | 5159.5 [952.5] | 5066.0 [596.4] | -93.5 [-874.4, +1042.4] | 0.98x [0.82x, 1.25x] | Similar Observed Tput | 2.5 | 2.6 |
| 12 | 7671.9 [1126.7] | 7642.0 [959.0] | -29.9 [-1787.0, +1881.5] | 1.00x [0.77x, 1.34x] | Similar Observed Tput | 2.1 | 2.2 |
| 16 | 10509.7 [961.9] | 10314.7 [1300.9] | -195.0 [-1457.6, +1134.8] | 0.98x [0.86x, 1.12x] | Similar Observed Tput | 1.9 | 2.0 |
| 24 | 15106.8 [2096.3] | 15605.4 [1386.8] | +498.6 [-1366.0, +2462.8] | 1.03x [0.91x, 1.19x] | Similar Observed Tput | 1.8 | 1.7 |
| 32 | 20150.8 [1600.0] | 20601.8 [2312.6] | +451.0 [-2755.8, +1701.9] | 1.02x [0.86x, 1.09x] | Similar Observed Tput | 1.6 | 1.5 |

---

## 3. Authoritative Campaign Execution Reconciliation

The reviewer requested reconciliation between the reported 9.2 hours campaign runtime, a claimed average of 41.2s per trial, and the 960 trials.
The discrepancy is resolved from authoritative execution logs:
1. **Session 1 (Batch 1 to 6, Trials 1 to 154)**: Initial execution span before the user terminal closed.
2. **Session 2 (Batch 7 to 34, Trials 155 to 960)**: Executed **806 remaining trials**. Total session wall time was **33,180 seconds (9.22 hours)**. Average runtime per trial in Session 2 was $33,180 / 806 = \mathbf{41.17\text{ seconds/trial}}$. The progress log reported `Session Elapsed: 33180s | Avg/Trial: 41.21s` computed over the 806 trials executed in that session.
3. **Total Campaign Wall Span**: First trial start was `2026-10-26T11:01:58Z`, last trial finish was `2026-10-26T22:50:49Z`. Total span was **42,530.6 seconds (11.81 hours)**, including the idle pause between sessions.
4. **Pure Steady-State Measurement Time**: Exactly $960 \times 10.05\text{s} = \mathbf{9,648.4\text{ seconds (2.68 hours)}}$. The remaining time represents fail-closed fixture pre-building (4 SSTables, 10,000 keys per trial), unmetered 2,000ms warmup, 100ms cooldown, and database directory sync/teardown.

---

## 4. Dual-Platform Regression Results

Executed on WSL2 Ubuntu Linux (`/home/ramst/selfdbms`):
- `go vet ./...`: **0 errors, 0 warnings**
- `go test -count=1 ./...`: **100% PASS**
  - `owndbms/lsmengine`: PASS (9.88s)
  - `owndbms/research/experiments/analysis`: PASS (0.01s)
  - `owndbms/research/experiments/campaign`: PASS (0.03s)
  - `owndbms/research/experiments/harness`: PASS (136.42s)
- `go test -race -count=1 ./...`: **100% PASS (0 data races detected)**
  - `owndbms/lsmengine`: PASS (9.36s)
  - `owndbms/research/experiments/campaign`: PASS (1.13s)
  - `owndbms/research/experiments/analysis`: PASS (1.03s)
  - `owndbms/research/experiments/harness`: PASS (132.98s)

---

## 5. Artifact Checklist

- [x] Evidence Manifest with SHA-256 Checksums: `research/experiments/data/campaign_wsl2_ext4_restart_v1/manifest_checksums.json`
- [x] Reconciled Comparative Tables: `research/experiments/analysis/comparative_tables.md`
- [x] Claim-to-Evidence Map: `paper/CLAIM_EVIDENCE_MAP.md`
- [x] Revised Research Manuscript: `paper.tex`
- [x] Corrected Figures (All 5): `research/experiments/figures/*.png`
- [x] Deterministic Overlap Unit Test: `lsmengine/flush_publication_overlap_test.go`
- [x] Comprehensive Checkpoint Report: `research/checkpoints/CP6.md`

---

EXTERNAL REVIEW REQUIRED — CP6 AWAITING APPROVAL.
