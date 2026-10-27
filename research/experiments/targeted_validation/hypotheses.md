# Preregistered Hypotheses: Phase I6.8 Targeted Validation

- **Campaign ID**: `I6_TARGETED_VALIDATION_V1`
- **Protocol Version**: 1.0.0
- **Date**: 2026-10-27

---

## 1. Experiment T1: Flush Tail Latency Verification (Workload B2)

### 1.1 Primary Confirmatory Targets ($r=2$ and $r=4$ under Flush Enabled)
- **Primary Hypothesis 1 ($H_{T1.1a}$, $r=2$)**:
  - $H_{0, T1.1a}: \mu_{p95, B} \ge \mu_{p95, A}$ (Approach B read $p95$ tail latency is greater than or equal to Approach A under background flushing at $r=2$).
  - $H_{1, T1.1a}: \mu_{p95, B} < \mu_{p95, A}$ (Approach B read $p95$ tail latency is strictly lower than Approach A).
  - Test: Paired Wilcoxon signed-rank test and 95% paired bootstrap CI on ratio of medians.
- **Primary Hypothesis 2 ($H_{T1.1b}$, $r=4$)**:
  - $H_{0, T1.1b}: \mu_{p95, B} \ge \mu_{p95, A}$ at $r=4$.
  - $H_{1, T1.1b}: \mu_{p95, B} < \mu_{p95, A}$ at $r=4$.
  - Test: Paired Wilcoxon signed-rank test and 95% paired bootstrap CI.
- **Familywise Error Rate Control**:
  - Holm-Bonferroni step-down correction over $\{H_{T1.1a}, H_{T1.1b}\}$ at overall $\alpha = 0.05$.
  - Sorted $p$-values: smallest must satisfy $p_{(1)} < 0.025$; second must satisfy $p_{(2)} < 0.05$.

### 1.2 Secondary Target ($r=8$ under Flush Enabled)
- **Secondary Hypothesis ($H_{T1.2}$, $r=8$)**:
  - Replicate tail suppression at higher concurrency ($r=8$).
  - Evaluated conditionally upon rejection of primary hypotheses.

### 1.3 Causal Mechanism Test (Difference-in-Differences vs Sham Flush)
- **Causal Contrast Hypothesis ($H_{T1.3}$)**:
  - Let $\Delta_{\text{flush}} = p95_A - p95_B$ under `flush_enabled`.
  - Let $\Delta_{\text{sham}} = p95_A - p95_B$ under `sham_flush`.
  - $H_{0, T1.3}: \Delta\Delta = \Delta_{\text{flush}} - \Delta_{\text{sham}} \le 0$.
  - $H_{1, T1.3}: \Delta\Delta > 0$.
  - Causal interpretation: If the observed tail suppression in B is causally caused by snapshot readers bypassing SSTable publication locks, the benefit must vanish under sham flush ($\Delta_{\text{sham}} \approx 0$) and persist under active flush ($\Delta_{\text{flush}} > 0$).

---

## 2. Experiment T2: Practical Equivalence via TOST (Workloads A & E)

### 2.1 Equivalence Specification
- **Estimand**: Paired log-throughput difference $d_i = \ln(T_{B,i}) - \ln(T_{A,i})$ across matched pairs.
- **Equivalence Bound**: $\pm 5\%$ throughput ratio ($B/A \in [0.95, 1.05]$).
  - Log bounds: $\theta_L = \ln(0.95) = -0.051293$, $\theta_U = \ln(1.05) = +0.048790$.
- **TOST Hypotheses**:
  - Lower bound test: $H_{01}: \mu_d \le \theta_L$ vs $H_{11}: \mu_d > \theta_L$.
  - Upper bound test: $H_{02}: \mu_d \ge \theta_U$ vs $H_{12}: \mu_d < \theta_U$.
- **Equivalence Decision Rule**:
  - If BOTH $H_{01}$ and $H_{02}$ are rejected at $\alpha = 0.05$ (or equivalently, if the 90% confidence interval of $\mu_d$ falls entirely within $[\theta_L, \theta_U]$), declare **Practical Equivalence at $\pm 5\%$**.
  - If the 90% CI crosses either bound, classify the comparison as **Inconclusive** (do not expand the margin post-hoc).

### 2.2 Evaluated Cells
1. $H_{T2.A1}$: Workload A at $r=1$
2. $H_{T2.A32}$: Workload A at $r=32$
3. $H_{T2.E1}$: Workload E at $r=1$
4. $H_{T2.E32}$: Workload E at $r=32$

### 2.3 Diagnostic Hypothesis: Writer-Free Workload E ($w=0$)
- **Hypothesis $H_{T2.\text{diag}}$**:
  - Under official Workload E ($w=2$), $p95$ read latency exhibits millisecond tails ($>1\text{ ms}$) due to WAL sync and write mutex contention.
  - In writer-free Workload E ($w=0$), $p95$ read latency drops to sub-microsecond levels ($<50\ \mu\text{s}$) in both modes, proving that the latency tail in Workload E is driven by disk/lock interference from WAL writers, not Memtable lookup overhead.

---

## 3. Experiment T3: Rapid Churn Overhead Decomposition (Workload D)

### 3.1 Controlled Churn vs No-Churn Hypotheses
- **Hypothesis $H_{T3.1}$ (Penalty Persistence)**:
  - Under `controlled_churn` (16KB Memtable, repeated flushes and compactions), Approach B exhibits a significant throughput penalty at low concurrency ($r=1$ and $r=4$), replicating the exploratory finding ($B/A < 0.80\text{x}$).
- **Hypothesis $H_{T3.2}$ (No-Churn Baseline Parity)**:
  - Under `no_churn_control` (same 3 writers, identical write schedule, but 64MB Memtable and 0 compactions), the throughput gap between B and A is substantially eliminated ($B/A \ge 0.90\text{x}$), confirming that reader-set churn and snapshot management—not writer presence—causes the regression.

### 3.2 Profiling Attribution
- **Profile Metric Attribution**:
  - Go CPU profiling: Measure percentage of CPU samples in snapshot management (`manager.GetSnapshot()`, `manager.releaseSnapshotInternal()`, atomic operations) vs reader point lookup (`sstable.Reader.Get()`, `memtable.Get()`).
  - Mutex contention profiling: Compare lock wait cycles between `m.Mu.RLock()` in Approach A and snapshot registry mutex in Approach B.
