# Phase I7 — Checkpoint R1.2b: Statistical Analysis and Power Sizing Plan

**DATE:** 2026-10-29  
**FRAMEWORK:** FORMAL STATISTICAL INFERENCE, SAMPLE SIZING, & MULTIPLICITY CONTROL  
**DATASET REFERENCE:** OBSERVED VARIANCE FROM FROZEN G4 RESULTS ROOT  
**STATUS:** PROSPECTIVE STATISTICAL PROTOCOL (DESIGN-ONLY).

---

## 1. Primary Estimands and Mathematical Formulations

To eliminate the inconsistencies identified in earlier drafts, all statistical estimands in the prospective study are defined with mathematical rigor:

### A. Paired Log-Ratio Metric
For each experimental triplet $t$ evaluating synchronizer 1 ($S_1$, e.g. Approach A-min) and synchronizer 2 ($S_2$, e.g. Approach B) on metric $X$ (e.g. read throughput, completed writes):
$$d_t = \ln X_{t, S_1} - \ln X_{t, S_2}$$

### B. Primary Effect Measure: Geometric Mean Ratio
The primary point estimate of relative performance across $N$ matched triplets is the **ratio of geometric means**:
$$\widehat{R}_g(S_1 / S_2) = \exp\left(\bar{d}\right) = \exp\left(\frac{1}{N}\sum_{t=1}^N d_t\right) = \left(\prod_{t=1}^N \frac{X_{t, S_1}}{X_{t, S_2}}\right)^{1/N}$$

### C. Secondary Effect Measure: Median Ratio
To protect against heavy-tailed outliers, the secondary point estimate is the **median ratio**:
$$\widehat{R}_m(S_1 / S_2) = \text{median}\left(\frac{X_{t, S_1}}{X_{t, S_2}}\right)$$

### D. Write Progress Capture Fraction
For mixed read-write workloads (EXP-1), the extent to which Approach A-min captures Approach B's progress over Approach A is:
$$CF_w = \text{median}_t\left(\frac{W_{t,\text{AMIN}} - W_{t,\text{A}}}{W_{t,\text{B}} - W_{t,\text{A}}}\right)$$
Defined for triplets where $W_{t,\text{B}} - W_{t,\text{A}} \ge 50$ operations.

---

## 2. Confidence Interval Construction: BCa vs. Percentile Bootstrap

### A. Mathematical Formulation of BCa Bootstrap
For all confirmatory estimands, uncertainty is reported via **Bias-Corrected and Accelerated (BCa) bootstrap 95% confidence intervals** using $B = 10,000$ resamples:
1. **Bootstrap Distribution:** Generate $B$ resamples $\{d^{*b}\}_{b=1}^B$ by sampling with replacement from $\{d_t\}_{t=1}^N$. Compute bootstrap estimates $\widehat{\theta}^{*b} = \exp(\bar{d}^{*b})$.
2. **Bias-Correction Parameter ($z_0$):**
   $$z_0 = \Phi^{-1}\left(\frac{1}{B}\sum_{b=1}^B \mathbb{I}(\widehat{\theta}^{*b} < \widehat{\theta})\right)$$
   where $\Phi^{-1}$ is the standard normal quantile function.
3. **Acceleration Parameter ($a$):** Computed via leave-one-out jackknife estimates $\widehat{\theta}_{(i)}$:
   $$a = \frac{\sum_{i=1}^N (\bar{\theta}_{(\cdot)} - \widehat{\theta}_{(i)})^3}{6 \left[\sum_{i=1}^N (\bar{\theta}_{(\cdot)} - \widehat{\theta}_{(i)})^2\right]^{3/2}}$$
4. **BCa Percentiles:** The 95% confidence interval is $[\widehat{\theta}^{*(\alpha_1)}, \widehat{\theta}^{*(\alpha_2)}]$ with adjusted percentiles:
   $$\alpha_1 = \Phi\left(z_0 + \frac{z_0 + z_{0.025}}{1 - a(z_0 + z_{0.025})}\right), \quad \alpha_2 = \Phi\left(z_0 + \frac{z_0 + z_{0.975}}{1 - a(z_0 + z_{0.975})}\right)$$

### B. Methodological Requirement
If sample size $N < 10$, the jackknife estimate of $a$ becomes unstable; in those cases, the plan mandates reporting the standard **percentile bootstrap** accompanied by exact non-parametric permutation intervals.

---

## 3. Pre-Declared Hypothesis Testing & Margins

Rather than asserting universal $\pm 5\%$ equivalence across all workloads, the prospective study pre-declares context-appropriate margins:

| Experiment / Contrast | Test Type | Pre-Declared Margin | Null Hypothesis ($H_0$) | Alternative Hypothesis ($H_1$) |
| :--- | :--- | :---: | :--- | :--- |
| **EXP-1: Write Progress ($R_w(\text{AMIN}/\text{A})$)** | Superiority | Margin $\ge 3.0\times$ | $H_0: R_w \le 1.50$ | $H_1: R_w \ge 3.00$ |
| **EXP-1: Write Progress ($R_w(\text{AMIN}/\text{B})$)** | Non-Inferiority | $\Delta_{\text{NI}} = \mathbf{0.80}$ | $H_0: \ln(R_w) \le \ln(0.80)$ | $H_1: \ln(R_w) > \ln(0.80)$ |
| **EXP-1: Capture Fraction ($CF_w$)** | Superiority | Margin $\ge 0.70$ | $H_0: CF_w \le 0.50$ | $H_1: CF_w \ge 0.70$ |
| **EXP-1: Read Throughput ($R_r(\text{AMIN}/\text{B})$)** | Non-Inferiority | $\Delta_{\text{NI}} = \mathbf{0.90}$ | $H_0: \ln(R_r) \le \ln(0.90)$ | $H_1: \ln(R_r) > \ln(0.90)$ |
| **EXP-2: Causal Scan Slope ($\beta_1$)** | Linear Regression | Slope $\beta_1 > 0$ | $H_0: \beta_1 = 0$ | $H_1: \beta_1 > 0$ |
| **EXP-4: Fixed-Work Time ($T_A / T_{\text{AMIN}}$)** | Superiority | Margin $\ge 1.50\times$ | $H_0: T_A / T_{\text{AMIN}} \le 1.10$ | $H_1: T_A / T_{\text{AMIN}} \ge 1.50$ |
| **EXP-4: Fixed-Work Time ($T_{\text{AMIN}} / T_B$)** | Non-Inferiority | Margin $\le \mathbf{1.10}$ | $H_0: T_{\text{AMIN}} / T_B \ge 1.15$ | $H_1: T_{\text{AMIN}} / T_B < 1.10$ |

---

## 4. Formal Sample Size Sizing & Power Analysis

### A. Empirical Variance Baseline
From the frozen Phase II G4 dataset, the within-triplet log-ratio standard deviation across closed-loop replicates (Blocks E2 and E3) was observed as:
$$\widehat{\sigma}_d^2 \approx 0.00734 \implies \widehat{\sigma}_d \approx \mathbf{0.0857}$$
This corresponds to an approximate within-triplet coefficient of variation of $8.6\%$.

### B. Sample Size Derivation for Non-Inferiority Testing
For a one-sided non-inferiority test of log-ratio mean $\mu_d$ at significance level $\alpha = 0.025$ (equivalent to a two-sided 95% confidence interval lower bound) with power $1 - \beta = 0.90$:
$$N \ge \frac{(z_{1-\alpha} + z_{1-\beta})^2 \cdot \sigma_d^2}{(\mu_1 - \mu_0)^2}$$
where $z_{0.975} = 1.960$ and $z_{0.90} = 1.282$, so $(z_{1-\alpha} + z_{1-\beta})^2 = (1.960 + 1.282)^2 = 10.510$.

#### Case 1: 10% Non-Inferiority Margin ($\Delta_{\text{NI}} = 0.90$)
- Margin: $\mu_0 = \ln(0.90) = -0.10536$.
- True Effect: $\mu_1 = 0.00$ (parity).
- Sizing calculation:
  $$N \ge \frac{10.510 \cdot (0.0857)^2}{(-0.10536)^2} = \frac{10.510 \cdot 0.00734}{0.01110} = \frac{0.07714}{0.01110} = \mathbf{6.95} \implies \mathbf{N \ge 8 \text{ triplets}}$$

#### Case 2: 5% Equivalence Margin ($\Delta_{\text{equiv}} = \pm 5\%$)
- Margin: $\mu_0 = \ln(0.95) = -0.05129$.
- Sizing calculation:
  $$N \ge \frac{10.510 \cdot (0.0857)^2}{(-0.05129)^2} = \frac{0.07714}{0.00263} = \mathbf{29.33} \implies \mathbf{N \ge 30 \text{ triplets}}$$

### C. Recommended Study Sample Sizing
1. **EXP-1 (Writer-Progress Scaling):** $N = 25$ triplets per concurrency cell ($r \in \{1, 4, 8, 16, 32\}$). Total: $5 \text{ cells} \times 25 \text{ triplets} = 125 \text{ triplets}$ (375 trial runs). Provides $>99\%$ power for 10% non-inferiority and $85\%$ power for 5% non-inferiority.
2. **EXP-2 (Causal Scan Intervention):** $N = 15$ triplets across 5 scan duration levels. Total: $5 \text{ levels} \times 15 \text{ triplets} = 75 \text{ triplets}$ (225 trial runs). Provides $>95\%$ power to detect regression slope $\beta_1 > 0$.
3. **EXP-3 (Complexity Crossover Sweep):** $N = 10$ triplets across the $4\times 3\times 2$ grid (24 cells). Total: 240 triplets.
4. **EXP-4 (Fixed-Work Batching):** $N = 20$ triplets across 3 concurrency levels ($r \in \{4, 8, 16\}$). Total: 60 triplets (180 trial runs).

---

## 5. Multiplicity Control Protocol

To control Family-Wise Error Rate (FWER) at $\alpha = 0.05$ across the study:
1. **Within-Family Multiplicity:** Holm-Bonferroni step-down adjustment applied across all secondary estimands within an experimental block.
2. **Across-Family Multiplicity:** Primary confirmatory estimands for EXP-1, EXP-2, and EXP-4 are ordered hierarchically:
   - **Gate 1:** EXP-1 Write Progress ($R_w(\text{AMIN}/\text{A})$ and $CF_w$) tested at $\alpha = 0.05$.
   - **Gate 2:** If Gate 1 succeeds, EXP-2 Causal Scan Slope ($\beta_1$) tested at $\alpha = 0.05$.
   - **Gate 3:** If Gate 2 succeeds, EXP-4 Fixed-Work Time Non-Inferiority tested at $\alpha = 0.05$.
3. **Exploratory Labeling:** EXP-3 (complexity crossover sweep) is exploratory by construction and carries no formal FWER adjustment.
