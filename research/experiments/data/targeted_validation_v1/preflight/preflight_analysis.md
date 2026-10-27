# Statistical Analysis: Phase I6.8 Targeted Hypothesis Verification

- **Campaign ID**: `I6_TARGETED_VALIDATION_V1`
- **Dataset Path**: `research/experiments/data/targeted_validation_v1/preflight/results.csv`
- **Total Analyzed Trials**: 10

## 1. Experiment T1: Flush Tail Latency Verification (Workload B2)

### 1.1 Active Flush vs Sham-Flush Paired Analysis

| Concurrency | Condition | Pairs | Median A (µs) | Median B (µs) | B < A Pairs | Paired RoM [95% CI] | Median Diff [95% CI] | Wilcoxon p-value | Sign Test p |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| r= 2 | `flush_enabled` | 1/20 |  50639.7 |  55728.7 | 0/1 | 1.10x [1.10x, 1.10x] | [+5.1ms, +5.1ms] | 1.000000 | 1.000000 |
| r= 2 | `sham_flush` | 1/20 |  47391.4 |  48663.3 | 0/1 | 1.03x [1.03x, 1.03x] | [+1.3ms, +1.3ms] | 1.000000 | 1.000000 |
| r= 4 | `flush_enabled` | 1/20 |  49146.5 |  52022.1 | 0/1 | 1.06x [1.06x, 1.06x] | [+2.9ms, +2.9ms] | 1.000000 | 1.000000 |
| r= 4 | `sham_flush` | 1/20 | 125135.6 |  49169.1 | 1/1 | 0.39x [0.39x, 0.39x] | [-76.0ms, -76.0ms] | 0.500000 | 0.500000 |
| r= 8 | `flush_enabled` | 1/20 |  56155.9 |  61934.2 | 0/1 | 1.10x [1.10x, 1.10x] | [+5.8ms, +5.8ms] | 1.000000 | 1.000000 |

### 1.2 Holm-Bonferroni Step-Down Procedure (Primary Targets r=2, 4)

| Rank | Concurrency | Wilcoxon p-value | Alpha Threshold | Reject Null (B < A)? |
| :---: | :---: | :---: | :---: | :---: |
| 1 | r=2 | 1.000000 | 0.025000 | **NO** |
| 2 | r=4 | 1.000000 | 0.050000 | **NO** |

### 1.3 Causal Contrast: Difference-in-Differences (Flush vs Sham)

| Concurrency | Flush Median Diff (A - B) | Sham Median Diff (A - B) | DiD (ΔΔ = Flush - Sham) | Causal Suppression Confirmed? |
| :---: | :---: | :---: | :---: | :---: |
| r= 2 | -5.09 ms | -1.27 ms | **-3.82 ms** | **NO** |
| r= 4 | -2.88 ms | +75.97 ms | **-78.84 ms** | **NO** |


## 2. Experiment T2: Practical Equivalence via TOST (Workloads A & E)

### 2.1 Two One-Sided Tests (TOST) on Log Throughput (Margin = ±5%)

| Workload | Readers | Pairs | Median A | Median B | Ratio B/A | 90% Equivalence CI | TOST p-value | Conclusion (±5%) |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |

### 2.2 Workload E Writer-Free Diagnostic (w=0 vs w=2 Latency Tail)

| Concurrency | Writers | Mode A p95 (µs) | Mode B p95 (µs) | Mode A Mean (µs) | Mode B Mean (µs) | Latency Tail Suppressed? |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: |


## 3. Experiment T3: Rapid Churn Overhead Decomposition (Workload D)

| Readers | Condition | Pairs | Median Tput A | Median Tput B | B/A Ratio [95% CI] | Flushes (A/B) | Compactions (A/B) | Churn Penalty Active? |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
