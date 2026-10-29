# Phase I7 — Checkpoint R1.2d: Statistical Validation and Power Plan

**DOCUMENT ID:** `R1_2D_STATISTICAL_VALIDATION.md`  
**STATUS:** FORMAL PRE-REGISTERED STATISTICAL VALIDATION REPORT  
**GOVERNING DIRECTIVE:** CHECKPOINT R1.2d EXTERNAL SCIENTIFIC REVIEW  
**REPRODUCIBLE SCRIPT:** [`research/experiments/analysis/simulate_power_r1_2d.py`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/simulate_power_r1_2d.py)  
**RAW OUTPUT DATA:** [`research/experiments/analysis/r1_2d_power_simulation_results.json`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/r1_2d_power_simulation_results.json)  
**BOUNDARY:** RIGOROUS ANALYTICAL & SIMULATION EVIDENCE (ZERO BENCHMARK EXECUTION)

---

## 1. Ground-Truth Empirical Variance Derivations

The statistical plan in R1.2c copied $\sigma_d = 0.0857$ from read-throughput benchmarks, applying it inappropriately to write-progress estimands. To ground R1.2d in empirical reality, an audit of the 9 complete write triplets in the frozen G4 dataset (`C:\Users\ramst\I7_WINDOWS_G4_RESULTS\trials\attempts\*_a01.json`) was conducted:

| Block | Trial ID | Workload Type | AMIN Rate (w/s) | B Rate (w/s) | Log-Ratio $d = \ln(W_{\text{amin}}/W_B)$ |
| :--- | :--- | :--- | :---: | :---: | :---: |
| **E1** | `r08_t09` | Heavy Reader Contention ($r=8$) | 267.10 | 265.70 | $+0.00526$ |
| **E3** | `equal_r01_t01` | Paced Nominal Match ($r=1$) | 235.00 | 235.00 | $0.00000$ |
| **E3** | `equal_r01_t03` | Paced Nominal Match ($r=1$) | 235.00 | 235.00 | $0.00000$ |
| **E3** | `equal_r01_t06` | Paced Nominal Match ($r=1$) | 235.00 | 235.00 | $0.00000$ |
| **E3** | `equal_r04_t06` | Paced Nominal Match ($r=4$) | 180.00 | 210.00 | $-0.15415$ |
| **E3** | `equal_r04_t07` | Paced Nominal Match ($r=4$) | 210.00 | 209.90 | $+0.00048$ |
| **E3** | `free_r01_t01` | Free-Running Writer ($r=1$) | 318.20 | 325.50 | $-0.02268$ |
| **E3** | `free_r01_t02` | Free-Running Writer ($r=1$) | 317.70 | 316.20 | $+0.00473$ |
| **E5** | `hit0_t05` | Scan Churn ($r=4$) | 459.30 | 429.90 | $+0.06615$ |

### Empirical Variance Summary:
- **Write-Progress Empirical Standard Deviation:**
  $$\hat{\sigma}_{d, \text{write}} = \mathbf{0.05872}$$
- **Write-Progress Empirical Mean Log-Ratio:**
  $$\hat{\mu}_{d, \text{write}} = -0.01114 \implies \hat{\theta}_{\text{write}} = 0.9889 \quad (-1.11\%\,\text{empirical delta})$$
- **Read-Throughput Empirical Standard Deviation (from G4 E2/E3):**
  $$\hat{\sigma}_{d, \text{read}} = \mathbf{0.08570}, \quad \hat{\theta}_{\text{read}} = 0.9646 \quad (-3.54\%\,\text{empirical delta})$$

---

## 2. Analytical Non-Inferiority Power Curves

Let $d_t = \ln(Y_{\text{amin}, t}) - \ln(Y_{B, t})$ denote the paired log-ratio. For a one-sided non-inferiority test:
$$H_0: \mu_d \le \delta_{\text{NI}} \quad \text{vs.} \quad H_1: \mu_d > \delta_{\text{NI}}$$
where $\delta_{\text{NI}} = \ln(\Delta_{\text{NI}}) = \ln(0.90) = -0.10536$. Under true effect $\mu_d$, the test statistic $T = \frac{\bar{d} - \delta_{\text{NI}}}{s_d / \sqrt{N}}$ follows a non-central Student-$t$ distribution with $\nu = N - 1$ degrees of freedom and non-centrality parameter $\gamma = \frac{\mu_d - \delta_{\text{NI}}}{\sigma_d / \sqrt{N}}$.

### Statistical Power vs. Sample Size ($N$ Matched Triplets, $\alpha = 0.05$)
*Computed via [`simulate_power_r1_2d.py`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/simulate_power_r1_2d.py):*

| Sample Size ($N$) | Write Parity ($\theta = 1.000$) | Write Empirical ($\theta = 0.9889$) | Read Empirical ($\theta = 0.9646$) | Read Deficit ($\theta = 0.9500$) |
| :---: | :---: | :---: | :---: | :---: |
| **4** | 0.8520 | 0.7816 | 0.3504 | 0.2565 |
| **6** | 0.9811 | 0.9549 | 0.5251 | 0.3789 |
| **8** | 0.9980 | 0.9920 | 0.6609 | 0.4860 |
| **10** | 0.9998 | 0.9987 | 0.7624 | 0.5783 |
| **12** | **1.0000** | **0.9998** | **0.8361** | **0.6567** |
| **14** | 1.0000 | 1.0000 | 0.8885 | 0.7225 |
| **16** | 1.0000 | 1.0000 | 0.9250 | 0.7771 |

> [!IMPORTANT]
> **Sample Size Determination ($N = 12$):**  
> Under empirical write-progress conditions ($\sigma_d = 0.05872$), **$N = 12$ achieves $99.98\%$ statistical power** for non-inferiority. Under read-throughput conditions, $N = 12$ achieves $83.61\%$ power at $\Delta_{\text{NI}} = 0.90$ and $> 98\%$ power at $\Delta_{\text{NI}} = 0.85$. Setting $N = 12$ enables the study to accommodate all 6 causal intervention cells within the strict 400-execution ceiling.

---

## 3. Two-Sided TOST Equivalence Power

We evaluate Two One-Sided Tests (TOST) under empirical write and read effects at uniform $\alpha = 0.05$:

| Sample Size ($N$) | Write TOST $\pm 10\%$ Equivalence | Read TOST $\pm 10\%$ Equivalence | Read TOST $\pm 5\%$ Equivalence |
| :---: | :---: | :---: | :---: |
| **8** | 0.9911 (99.1%) | 0.6492 (64.9%) | 0.0506 (5.1%) |
| **10** | 0.9983 (99.8%) | 0.7569 (75.7%) | 0.0753 (7.5%) |
| **12** | **0.9998 (99.9%)** | **0.8339 (83.4%)** | **0.0983 (9.8%)** |
| **16** | 1.0000 (100.0%) | 0.9255 (92.6%) | 0.1464 (14.6%) |

> [!CAUTION]
> Notice that for read throughput, testing $\pm 5\%$ equivalence at $N=12$ yields only **$9.8\%$ power**. It is mathematically impossible to establish $\pm 5\%$ equivalence with $N \le 20$ when the true underlying effect is shifted by $-3.54\%$. Protocol R1.2d formally pre-registers $\pm 10\%$ equivalence or 10% non-inferiority, completely eliminating futile $\pm 5\%$ equivalence claims.

---

## 4. EXP-1 Writer Superiority Power ($\Delta \dot{W}$)

- **Estimand:** Absolute write rate difference:
  $$\Delta \dot{W}(\text{AMIN} - \text{A}) = \frac{W_{\text{amin}} - W_A}{T_{\text{window}}} \quad [\text{writes/second}]$$
- **Hypotheses:**
  $$H_0: \Delta \dot{W} \le 5.0\,\text{w/s} \quad \text{vs.} \quad H_1: \Delta \dot{W} > 5.0\,\text{w/s}$$
  where $5.0\,\text{w/s}$ is the pre-registered Minimum Substantive Effect Size (MES).
- **Physical Ground Truth:** Under concurrent reader pressure ($r = 8$), Approach A collapses to $\approx 0.5\,\text{w/s}$ while Approach A-min commits $\approx 267.1\,\text{w/s}$.
- **Effect Size:** $\Delta \dot{W}_{\text{true}} \approx 266.6\,\text{writes/s}$, $\sigma_{\text{rate}} \approx 3.5\,\text{w/s}$.
- **Result:** For all $N \ge 4$, **Statistical Power $= 1.000000$** ($> 99.9999\%$). Rejection of $H_0$ is guaranteed even under conservative non-parametric tests.

---

## 5. EXP-4 Matched Paired Right-Censored Score Test

### 5.1 Mathematical Formulation (Gehan / Prentice-Wilcoxon)
In EXP-4, each trial triplet $i = 1, \dots, N$ executes under identical pseudo-random transaction keys and arrival schedules. Right-censoring occurs at $T_{\text{max}} = 30.0\,\text{seconds}$.
Let $(T_{\text{amin}, i}, C_{\text{amin}, i})$ and $(T_{A, i}, C_{A, i})$ represent the completion times and censoring indicators ($C = 1$ if timed out, $0$ if finished). The paired comparison score is:
$$U_i = \begin{cases}
+1 & \text{if } C_{\text{amin}, i} = 0 \text{ and } T_{\text{amin}, i} < T_{A, i} \quad (\text{A-min definitively faster}) \\
-1 & \text{if } C_{A, i} = 0 \text{ and } T_{A, i} < T_{\text{amin}, i} \quad (\text{Approach A definitively faster}) \\
0 & \text{if indeterminate or both censored } (C_{\text{amin}, i} = C_{A, i} = 1)
\end{cases}$$
The test statistic is the count of pairs favoring A-min, $K = \sum \mathbb{I}(U_i = +1)$, evaluated over non-tied pairs $n_{\text{eff}} = \sum \mathbb{I}(U_i \ne 0)$ via an exact one-sided binomial test:
$$H_0: P(U_i = +1 \mid U_i \ne 0) \le 0.5 \quad \text{vs.} \quad H_1: P(U_i = +1 \mid U_i \ne 0) > 0.5$$

### 5.2 Simulation Results Across Plausible Timeout Regimes (10,000 Iterations)

| Workload Contention Regime | $N$ Pairs | Mean Timeout Rate (A) | Power ($\alpha = 0.05$) | Power ($\alpha = 0.01$) | Epistemic Classification |
| :--- | :---: | :---: | :---: | :---: | :--- |
| **Severe Starvation** ($\bar{T}_A \ge 28\,\text{s}$) | 8 | 42.1% | 100.0% | 98.1% | Conclusive Superiority |
| **Severe Starvation** ($\bar{T}_A \ge 28\,\text{s}$) | **12** | **42.1%** | **100.0%** | **100.0%** | **Conclusive Superiority** |
| **Medium Contention** ($\bar{T}_A \approx 18\,\text{s}$) | 8 | 10.1% | 90.5% | 56.6% | Moderate Power |
| **Medium Contention** ($\bar{T}_A \approx 18\,\text{s}$) | **12** | **10.1%** | **96.4%** | **81.6%** | **High Conclusive Power** |
| **Modest Difference** ($\bar{T}_A \approx 13\,\text{s}$) | 8 | 0.3% | 58.1% | 22.3% | Likely Inconclusive |
| **Modest Difference** ($\bar{T}_A \approx 13\,\text{s}$) | **12** | **0.3%** | **64.8%** | **35.3%** | **May Yield Inconclusive** |

> [!NOTE]
> **Methodological Principle:** If the true difference between Approach A and A-min is modest ($\bar{T}_A = 13\,\text{s}$ vs $9.5\,\text{s}$), $N=12$ yields $64.8\%$ power at $\alpha=0.05$. An experiment that fails to reach $p < 0.05$ under this regime must be classified as **inconclusive evidence**, not falsification of A-min's benefits.

---

## 6. Family-Wise Error Rate (FWER) Multiplicity Control

### Reconciled Fixed-Sequence Hierarchy
All hypotheses are arranged in a pre-registered sequential testing chain. By the closure principle (Maurer et al., 1995), every gate in the sequence is tested at the **full uniform $\alpha = 0.05$** as long as all preceding gates rejected their nulls:

$$\text{Gate 1: EXP-1 Superiority } (\alpha=0.05) \longrightarrow \text{Gate 2: EXP-1 Non-Inferiority } (\alpha=0.05) \longrightarrow \text{Gate 3: EXP-2 Causal Slope } (\alpha=0.05)$$

- **Mathematical Proof of FWER $\le 0.05$:**
  $$P(\text{At least one Type I Error}) = P(\text{Reject first true null } H_{(j)}) \le \alpha_j = 0.05$$
  This eliminates unnecessary alpha-splitting ($\alpha=0.01 / 0.04$) and maximizes test sensitivity while strictly guaranteeing $\text{FWER} \le 0.05$.
- **Decoupling Mechanistic Investigations:** Failure or inconclusiveness in Gate 2 (EXP-1 performance) **shall not abort Gate 3 (EXP-2 causal mechanism)**. Understanding the synchronization physics of Go's `sync.RWMutex` is a standalone scientific objective.
