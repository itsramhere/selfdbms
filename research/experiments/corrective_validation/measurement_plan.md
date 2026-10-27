# Corrective Measurement & Statistical Analysis Plan: Phase I6.9

- **Campaign ID**: `I6_CORRECTIVE_MECHANISMS_V1`
- **Protocol Version**: 1.1.0
- **Document Status**: **PREREGISTERED**

---

## 1. Statistical Estimators & Inferential Methods

### 1.1 Experimental Unit & Pairing
The fundamental experimental unit is a matched trial pair:
$$P_i = (T_{A, i}, T_{B, i})$$
where both trials share:
- Identical `PairSeed`: $S_i$
- Identical hardware, filesystem, and OS environment
- Balanced execution order: alternating between $A \to B$ and $B \to A$ across consecutive pairs

### 1.2 Primary Endpoint Estimators for Experiment T1

#### A. Paired Median Difference
For a metric $X$ (e.g., $p95$ read latency in microseconds):
$$D_i = X_{B, i} - X_{A, i}$$
The primary point estimator is the median paired difference $\tilde{D} = \text{Median}(\{D_i\})$.

#### B. Ratio of Medians (RoM)
$$\text{RoM} = \frac{\text{Median}(X_B)}{\text{Median}(X_A)}$$

#### C. Non-Parametric Bootstrap Confidence Intervals
All confidence intervals for paired differences and ratios are constructed using **BCa (Bias-Corrected and Accelerated) non-parametric bootstrap** with $B = 10,000$ paired resamples at nominal $95\%$ confidence level.

#### D. Hypothesis Testing & Family-Wise Error Rate Control
- Two-sided Wilcoxon signed-rank test and paired sign test.
- Primary family-wise error rate ($\alpha = 0.05$) controlled via the **Holm-Bonferroni step-down procedure** across the primary concurrency endpoints ($r=4$ rank 1, $r=2$ rank 2).

### 1.3 Causal Decomposition Estimators for Experiment T1
To isolate competing mechanisms, we define:
1. **Writer Contention Effect**:
   $$\Delta_{\text{contention}} = \text{Median}(p95_{A} - p95_{B})_{\text{Cond B}} - \text{Median}(p95_{A} - p95_{B})_{\text{Cond A}}$$
   Isolates reader blocking behind writer `m.Mu.Lock()` and WAL `fsync`.
2. **Flush Publication Effect**:
   $$\Delta_{\text{pub}} = \text{Median}(p95_{A} - p95_{B})_{\text{Cond C}} - \text{Median}(p95_{A} - p95_{B})_{\text{Cond B}}$$
   Isolates manager-wide exclusive locking during SSTable publication.
3. **Total Latency Advantage**:
   $$\Delta_{\text{total}} = \text{Median}(p95_{A} - p95_{B})_{\text{Cond C}} = \Delta_{\text{contention}} + \Delta_{\text{pub}} + \epsilon$$

### 1.4 Two One-Sided Tests (TOST) for Practical Equivalence (Experiment T2)
- **Equivalence Margin**: $\delta = \pm 5\%$ throughput ratio ($B/A \in [0.95, 1.05]$).
- In log space: $\theta_L = \ln(0.95) \approx -0.051293$, $\theta_U = \ln(1.05) \approx +0.048790$.
- For paired log-throughput differences $d_i = \ln(\text{Ops}_B) - \ln(\text{Ops}_A)$:
  - Calculate mean $\bar{d}$ and standard error $SE(\bar{d})$.
  - Construct $90\%$ two-sided confidence interval: $[\bar{d} - t_{0.05, n-1} SE, \bar{d} + t_{0.05, n-1} SE]$.
  - Practical Equivalence is declared if and only if:
    $$90\%\text{ CI} \subset [\theta_L, \theta_U]$$

---

## 2. Metric Collection & Observability Architecture

### 2.1 Operation Accounting
To account for speed differences and distinguish submitted vs realized work, the runner records:
- `read_ops_attempted`: Key generated and read initiated by worker.
- `read_ops_admitted`: Read request evaluated and allowed to proceed.
- `read_ops_completed`: Successful read completion or valid `ErrNotFound`.
- `write_ops_attempted`: Write transaction initiated.
- `write_ops_admitted`: Write transaction admitted to commit queue.
- `write_ops_completed`: Write transaction successfully committed and logged.

### 2.2 Direct Synchronization & Contention Metrics
- `flush_overlap_ms`: Total wall-clock time active flush worker was executing.
- `reads_during_flush`: Point reads completed while flush worker was active.
- `reads_during_publication`: Point reads in-flight when publication lock was requested or held.
- `lock_hold_nanos`: Exact duration `m.Mu.Lock()` was held during SSTable publication.

### 2.3 Profiling Isolation & Metrics
Profiling runs are strictly isolated to single-mode process invocations:
- **CPU Profiling**: `pprof.StartCPUProfile` activated strictly at `OnMeasurementStart` and stopped at `OnMeasurementEnd`.
- **Contention Profiling**: `runtime.SetMutexProfileFraction(1)` and `runtime.SetBlockProfileRate(1)` active strictly during the 5s window.
- **Allocation Accounting**: Total allocated bytes during the window derived from differential `runtime.ReadMemStats(&m)` taken at window boundaries.
- **Reporting Rule**: Flat CPU samples and cumulative CPU samples must be presented separately; cumulative percentages must never be summed as mutually exclusive components.
