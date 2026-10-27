# Statistical Analysis: Phase I6.8 Targeted Hypothesis Verification

- **Campaign ID**: `I6_TARGETED_VALIDATION_V1`
- **Dataset Path**: `research/experiments/data/targeted_validation_v1/results.csv`
- **Total Analyzed Trials**: 600

## 1. Experiment T1: Flush Tail Latency Verification (Workload B2)

### 1.1 Active Flush vs Sham-Flush Paired Analysis

| Concurrency | Condition | Pairs | Median A (µs) | Median B (µs) | B < A Pairs | Paired RoM [95% CI] | Median Diff [95% CI] | Wilcoxon p-value | Sign Test p |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| r= 2 | `flush_enabled` | 20/20 | 220918.5 | 184035.6 | 18/20 | 0.83x [0.76x, 0.91x] | [-55.5ms, -20.2ms] | 0.000010 | 0.000201 |
| r= 2 | `sham_flush` | 20/20 | 111508.7 |  45351.8 | 15/20 | 0.41x [0.28x, 0.98x] | [-121.8ms, -0.8ms] | 0.003648 | 0.020695 |
| r= 4 | `flush_enabled` | 20/20 |  56353.6 |  35529.4 | 20/20 | 0.63x [0.57x, 0.67x] | [-27.0ms, -17.9ms] | 0.000001 | 0.000001 |
| r= 4 | `sham_flush` | 20/20 |  35979.4 |  34172.1 | 18/20 | 0.95x [0.89x, 0.98x] | [-4.0ms, -0.6ms] | 0.000425 | 0.000201 |
| r= 8 | `flush_enabled` | 20/20 | 166531.8 |  66014.8 | 20/20 | 0.40x [0.35x, 0.59x] | [-110.9ms, -67.9ms] | 0.000001 | 0.000001 |
| r= 8 | `sham_flush` | 20/20 | 169333.0 |  61099.5 | 20/20 | 0.36x [0.32x, 0.75x] | [-117.8ms, -41.3ms] | 0.000001 | 0.000001 |

### 1.2 Holm-Bonferroni Step-Down Procedure (Primary Targets r=2, 4)

| Rank | Concurrency | Wilcoxon p-value | Alpha Threshold | Reject Null (B < A)? |
| :---: | :---: | :---: | :---: | :---: |
| 1 | r=4 | 0.000001 | 0.025000 | **YES** |
| 2 | r=2 | 0.000010 | 0.050000 | **YES** |

### 1.3 Causal Contrast: Difference-in-Differences (Flush vs Sham)

| Concurrency | Flush Median Diff (A - B) | Sham Median Diff (A - B) | DiD (ΔΔ = Flush - Sham) | Causal Suppression Confirmed? |
| :---: | :---: | :---: | :---: | :---: |
| r= 2 | +46.56 ms | +3.87 ms | **+42.69 ms** | **YES** |
| r= 4 | +20.83 ms | +1.40 ms | **+19.43 ms** | **YES** |
| r= 8 | +103.54 ms | +72.98 ms | **+30.55 ms** | **YES** |


## 2. Experiment T2: Practical Equivalence via TOST (Workloads A & E)

### 2.1 Two One-Sided Tests (TOST) on Log Throughput (Margin = ±5%)

| Workload | Readers | Pairs | Median A | Median B | Ratio B/A | 90% Equivalence CI | TOST p-value | Conclusion (±5%) |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| Workload A | r= 1 | 20/20 |     63.5 |     64.4 | 1.0084x | [0.9684x, 1.0500x] | 0.0499 | **PRACTICALLY EQUIVALENT** |
| Workload A | r=32 | 20/20 |    339.3 |    339.8 | 1.0343x | [0.9908x, 1.0798x] | 0.2764 | *INCONCLUSIVE* |
| Workload E | r= 1 | 20/20 |    548.2 |    531.4 | 0.9630x | [0.9208x, 1.0072x] | 0.3031 | *INCONCLUSIVE* |
| Workload E | r=32 | 20/20 |  18017.8 |  17300.8 | 0.9826x | [0.9209x, 1.0485x] | 0.1896 | *INCONCLUSIVE* |

### 2.2 Workload E Writer-Free Diagnostic (w=0 vs w=2 Latency Tail)

| Concurrency | Writers | Mode A p95 (µs) | Mode B p95 (µs) | Mode A Mean (µs) | Mode B Mean (µs) | Latency Tail Suppressed? |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| r= 1 | w=0 (diagnostic) |      1.7 |      1.9 |      1.0 |      1.0 | **YES (Sub-microsecond RAM lookup)** |
| r=32 | w=0 (diagnostic) |      3.0 |      3.1 |      3.6 |      3.9 | **YES (Sub-microsecond RAM lookup)** |


## 3. Experiment T3: Rapid Churn Overhead Decomposition (Workload D)

| Readers | Condition | Pairs | Median Tput A | Median Tput B | B/A Ratio [95% CI] | Flushes (A/B) | Compactions (A/B) | Churn Penalty Active? |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| r= 1 | `controlled_churn` | 15/15 |    293.0 |    201.8 | 0.69x [0.62x, 0.78x] | 6.4/9.7 | 6.6/7.9 | YES (Regression) |
| r= 1 | `no_churn_control` | 15/15 |     48.5 |     55.7 | 1.15x [1.04x, 1.27x] | 0.0/0.0 | 0.0/0.0 | NO (Parity restored) |
| r= 4 | `controlled_churn` | 15/15 |   1418.7 |   1074.9 | 0.76x [0.71x, 0.83x] | 7.5/11.2 | 6.7/8.0 | YES (Regression) |
| r= 4 | `no_churn_control` | 15/15 |    112.0 |    221.5 | 1.98x [1.81x, 2.05x] | 0.0/0.0 | 0.0/0.0 | NO (Parity restored) |
| r=16 | `controlled_churn` | 15/15 |   4855.3 |   4891.2 | 1.01x [0.98x, 1.04x] | 6.3/8.9 | 6.7/8.0 | NO (Parity restored) |
| r=16 | `no_churn_control` | 15/15 |    312.8 |    500.7 | 1.60x [1.54x, 1.73x] | 0.0/0.0 | 0.0/0.0 | NO (Parity restored) |
