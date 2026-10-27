# Deep Statistical Reanalysis of Campaign I5_WSL2_EXT4_RESTART_V1 (Sub-Phase I6.8.0)

## 1. Workload B2 p95 Read Latency: Paired vs Unpaired Audit

| Readers | Median A (µs) | Median B (µs) | B < A Pairs | Unpaired RoM CI [95%] | Paired RoM CI [95%] | Paired Mean Ratio CI [95%] | Paired Median Diff [95% CI] | Wilcoxon p-value |
| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
|  1 |  45209.7 |  39083.0 | 4/10 | [0.17x, 4.04x] | [0.20x, 1.28x] | [0.75x, 1.07x] | [-164.9ms, +15.2ms] | 0.3477 |
|  2 | 175434.2 |  42735.0 | 10/10 | [0.18x, 2.75x] | [0.22x, 0.93x] | [0.50x, 0.82x] | [-133.8ms, -14.4ms] | 0.0010 |
|  4 | 180965.7 |  48916.2 | 10/10 | [0.17x, 1.24x] | [0.23x, 0.83x] | [0.31x, 0.66x] | [-136.6ms, -38.3ms] | 0.0010 |
|  8 | 220895.7 | 179813.6 | 10/10 | [0.58x, 1.02x] | [0.73x, 0.84x] | [0.76x, 0.83x] | [-84.8ms, -35.3ms] | 0.0010 |
| 12 | 251005.5 | 219715.2 | 9/10 | [0.69x, 1.12x] | [0.79x, 1.05x] | [0.82x, 0.94x] | [-59.0ms, +11.7ms] | 0.0068 |
| 16 | 298729.4 | 245078.5 | 10/10 | [0.65x, 1.12x] | [0.79x, 0.90x] | [0.82x, 0.86x] | [-60.6ms, -28.3ms] | 0.0010 |
| 24 | 352200.2 | 307522.2 | 8/10 | [0.74x, 1.14x] | [0.75x, 1.12x] | [0.85x, 1.02x] | [-97.3ms, +39.6ms] | 0.0527 |
| 32 | 370843.7 | 355866.5 | 7/10 | [0.86x, 1.14x] | [0.93x, 1.06x] | [0.95x, 1.01x] | [-24.1ms, +23.5ms] | 0.1875 |

### Multiple-Comparison Adjustment (Holm-Bonferroni across r=2, 4, 8)

| Rank | Target Concurrency | Unadjusted p-value | Alpha Threshold (0.05 / (4 - rank)) | Statistically Significant? |
| :---: | :---: | :---: | :---: | :---: |
| 1 | target | 0.000977 | 0.016667 | YES |
| 2 | target | 0.000977 | 0.025000 | YES |
| 3 | target | 0.000977 | 0.050000 | YES |


## 2. Workload A Repetition 2 Throughput Drop Investigation

| Concurrency | Rep 1 Mean Tput (ops/s) | Rep 2 Tput A | Rep 2 Tput B | Rep 2 B/A Ratio | Rep 2 Drop vs Rep 1 (%) |
| :---: | :---: | :---: | :---: | :---: | :---: |
| r= 1 |     73.8 |     41.2 |     42.2 | 1.02x |  43.5% drop |
| r= 2 |    140.4 |     91.2 |     68.2 | 0.75x |  43.2% drop |
| r= 4 |    270.4 |    141.3 |    123.1 | 0.87x |  51.1% drop |
| r= 8 |    443.5 |    206.0 |    245.3 | 1.19x |  49.1% drop |
| r=12 |    546.2 |    229.4 |    209.3 | 0.91x |  59.8% drop |
| r=16 |    491.9 |    223.5 |    219.5 | 0.98x |  55.0% drop |
| r=24 |    547.4 |    238.9 |    237.3 | 0.99x |  56.5% drop |
| r=32 |    535.0 |    221.4 |    215.6 | 0.97x |  59.2% drop |
