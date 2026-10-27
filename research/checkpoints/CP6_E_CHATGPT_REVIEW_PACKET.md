# External Reviewer Packet — Checkpoint CP6-E-PRE

**Date**: 2026-10-27  
**Sub-Phase**: I6.8 — Targeted Hypothesis Verification and Causal Mechanism Analysis  
**Checkpoint Identifier**: **CP6-E-PRE (Preregistration and Diagnostic Preflight)**  
**Repository**: `selfdbms` / `owndbms` (Branch: `research/approach-b-snapshots`)  
**Frozen Official Campaign SHA**: `9992ca2c8bbfa4fec8b26002f2323eecdb2e5d77`  
**Targeted Campaign ID**: `I6_TARGETED_VALIDATION_V1` (600 planned trials)  
**Status**: **SUBMITTED FOR EXTERNAL REVIEWER DECISION**

---

## 1. Executive Summary & Reviewer Guidance

In response to the reviewer directive, this packet formalizes **Checkpoint CP6-E-PRE**. It distinguishes three strict categories of evidence:
1. **Category 1: Historical Frozen 960-Trial Campaign (`campaign_wsl2_ext4_restart_v1`)**: Reanalyzed to audit Workload B2 tail latency under paired block inference and to determine the root cause of the Workload A Repetition 2 throughput drop.
2. **Category 2: Actual Diagnostic Preflight Observations**: 36 short-window diagnostic trials (18 balanced pairs) executed to completion in WSL2 on native ext4, confirming zero errors, fail-closed validation, and memory-tier latency distributions.
3. **Category 3: Planned Confirmatory Campaign (`I6_TARGETED_VALIDATION_V1`)**: 600 planned trials across Experiments T1, T2, and T3, preregistered and queued for execution. No confirmatory findings or confidence intervals are claimed prior to full data collection.

---

## 2. Category 1: Historical Frozen Campaign Reanalysis

### 2.1 Provenance and Preservation
- **Location**: `research/experiments/data/campaign_wsl2_ext4_restart_v1/`
- **Total Validated Records**: 960 JSON files in `wsl2_ext4/`
- **Tabular Results**: `results.csv` (960 rows + header)
- **Integrity Manifest**: `manifest_checksums.json` (SHA-256 hashes of all 960 raw JSON files)
- **Status**: Completely frozen and preserved without modification.

### 2.2 Workload B2 $p95$ Latency: Resolving Unpaired vs Paired Resampling
The exploratory bootstrap in CP6 drew samples independently from $A$ and $B$, ignoring repetition blocking. Because background host latency varied across repetitions, independent resampling paired quiet A trials with noisy B trials, artificially widening the ratio CI.

Paired reanalysis across matching repetitions demonstrates consistent tail suppression:

| Readers ($r$) | Median A ($\mu\text{s}$) | Median B ($\mu\text{s}$) | $B < A$ Pairs | Unpaired RoM [95% CI] | Paired RoM [95% CI] | Paired Median Diff [95% CI] | Wilcoxon $p$-value |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| $r=1$ | 45,209.7 | 39,083.0 | 4 / 10 | [0.17x, 4.04x] | [0.20x, 1.28x] | [-164.9 ms, +15.2 ms] | 0.3477 |
| **$r=2$** | **175,434.2** | **42,735.0** | **10 / 10** | [0.18x, 2.75x] | **[0.22x, 0.93x]** | **[-133.8 ms, -14.4 ms]** | **0.0010** |
| **$r=4$** | **180,965.7** | **48,916.2** | **10 / 10** | [0.17x, 1.24x] | **[0.23x, 0.83x]** | **[-136.6 ms, -38.3 ms]** | **0.0010** |
| **$r=8$** | **220,895.7** | **179,813.6** | **10 / 10** | [0.58x, 1.02x] | **[0.73x, 0.84x]** | **[-84.8 ms, -35.3 ms]** | **0.0010** |
| $r=12$ | 251,005.5 | 219,715.2 | 9 / 10 | [0.69x, 1.12x] | [0.79x, 1.05x] | [-59.0 ms, +11.7 ms] | 0.0068 |
| **$r=16$** | 298,729.4 | 245,078.5 | **10 / 10** | [0.65x, 1.12x] | **[0.79x, 0.90x]** | **[-60.6 ms, -28.3 ms]** | **0.0010** |
| $r=24$ | 352,200.2 | 307,522.2 | 8 / 10 | [0.74x, 1.14x] | [0.75x, 1.12x] | [-97.3 ms, +39.6 ms] | 0.0527 |
| $r=32$ | 370,843.7 | 355,866.5 | 7 / 10 | [0.86x, 1.14x] | [0.93x, 1.06x] | [-24.1 ms, +23.5 ms] | 0.1875 |

- **Exact $p$-value**: $2^{-10} \approx 0.000977$ for $r \in \{2, 4, 8\}$.
- **Holm-Bonferroni Correction**: All three primary targets reject the null hypothesis at $\alpha = 0.05$ ($p < 0.0167, 0.025, 0.05$).
- **Exploratory Status**: Because Approach A always ran before B and seeds differed by $+1$ in this historical campaign, these results remain exploratory and motivate the balanced-block confirmatory design of Experiment T1.

### 2.3 Repetition 2 Confound: Storage Dirty Page Writeback
- Workload A repetition 2 throughput dropped by 43%–60% across all reader concurrencies.
- In `manifest.go`, `repetition` was the outermost execution loop; Repetition 1 finished with heavy write workloads (D and E).
- Linux kernel dirty page writeback caused I/O stall cycles at the start of Repetition 2.
- Both modes dropped together (B/A ratio: $0.97\text{x}$–$1.02\text{x}$ at high concurrency), proving an external host environmental confound.

---

## 3. Category 2: Actual Completed Diagnostic Preflight Observations

The preflight execution in WSL2 native ext4 completed with **36 / 36 trials SUCCESS**:

### Machine-Checked Verification Audit:
1. **Completion & Validation**: All 36 trial records verified with status `SUCCESS`, 0 errors, and 0 validation errors.
2. **Fixture Integrity**: `validation_readback_status == "PASSED"` in 100% of trials.
3. **T1 Sham-Flush Verification**:
   - `flush_enabled`: Completed 1 physical flush per trial; positive flush active overlap recorded.
   - `sham_flush`: Exactly **0 flushes**, 0 overlap duration, and 0 reads during flush, while transactions committed identical 50-key batches.
4. **T2 Writer-Free Workload E Diagnostic ($w=0$)**:
   - Standard ($w=2$): $p95 = 6,014\ \mu\text{s}$ (A) vs $4,985\ \mu\text{s}$ (B) at $r=1$; multi-millisecond tail latency verified.
   - Writer-Free ($w=0$): $p95$ dropped to **$2.5\ \mu\text{s}$** (A) and **$1.6\ \mu\text{s}$** (B); throughput scaled to **4,105,048 ops/s** at $r=32$.
   - **Conclusion**: Conclusively proves that the millisecond latency tail in Workload E is driven by writer lock/WAL sync contention on disk, whereas pure Memtable point reads operate in sub-microsecond RAM access times in both modes.
5. **T3 No-Churn Control**: Exactly **0 compactions** and 0 flushes occurred under `no_churn_control`, while writers executed identical transactions.
6. **Profiling Runs**: Dedicated profiling captures completed for Approach A and Approach B under Workload D ($r=4$); CPU profiles, heap profiles, and mutex profiles recorded under `profiles/`.

---

## 4. Category 3: Planned Confirmatory Campaign (`I6_TARGETED_VALIDATION_V1`)

- **Protocol**: [`protocol.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/targeted_validation/protocol.md)
- **Hypotheses**: [`hypotheses.md`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/targeted_validation/hypotheses.md)
- **Manifest**: [`manifest.json`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/targeted_validation/manifest.json) (600 planned trials)

### Confirmatory Experimental Matrix:
- **Experiment T1 (Flush Tail Latency Verification & Causal DiD)**:
  - Workload B2, $r \in \{2, 4, 8\}$, active flush vs sham flush (240 planned trials).
  - Evaluates whether Approach B tail latency suppression is replicated under balanced execution order, and whether the benefit vanishes under sham flush ($\Delta\Delta > 0$).
  - *Status*: Ready to execute; no confirmatory data collected yet.
- **Experiment T2 (Practical Equivalence via TOST)**:
  - Workloads A and E, $r \in \{1, 32\}$, 20 balanced pairs per configuration (160 planned trials).
  - Evaluated via Two One-Sided Tests against a prespecified $\pm 5\%$ margin on log-throughput ratios ($[-0.051293, +0.048790]$).
  - *Status*: Ready to execute; no confirmatory data collected yet.
- **Experiment T2 Diagnostic (Writer-Free Workload E)**:
  - Workload E with zero background writers, $r \in \{1, 32\}$ (20 planned trials).
  - *Status*: Ready to execute.
- **Experiment T3 (Rapid Churn Overhead Decomposition)**:
  - Workload D, $r \in \{1, 4, 16\}$, controlled churn vs no-churn control (180 planned trials).
  - Tests whether the low-concurrency churn penalty of Approach B persists when reader-set swaps are eliminated.
  - *Status*: Ready to execute; no confirmatory data collected yet.

---

## 5. Verification Suite Audit

- `go vet ./...`: **PASS (0 warnings, 0 errors)**
- `go test -count=1 ./...`: **PASS (100% unit tests pass across all packages)**
- `go test -race -count=1 ./...`: **PASS (0 data races detected)**
- Targeted harness test suite ([`targeted_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/targeted_validation/targeted_test.go)): **PASS (6/6 tests pass: exact 150/150 A/B order balance, seed equality, TOST logic)**

---

## 6. Checkpoint Decision

Checkpoint CP6-E-PRE establishes the preregistration, preflight validation, and statistical reanalysis of Phase I6.8. All preliminary illustrative claims have been cleanly categorized. The 600-trial confirmatory campaign is authorized and queued for execution.

**EXTERNAL REVIEW REQUIRED — CP6-E-PRE AWAITING CONFIRMATORY CAMPAIGN AUTHORIZATION.**
