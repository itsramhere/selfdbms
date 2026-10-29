# R1.2e Statistical Notice — Scale Mismatch Between the Log-Ratio Sample-Size Calculation and the Absolute Write-Rate Primary Estimand

**DOCUMENT ID:** `R1_2E_ESTIMAND_SCALE_MISMATCH_NOTICE.md`
**STATUS:** OPEN STATISTICAL FINDING — SUBMITTED FOR EXTERNAL REVIEW; **NO PARAMETER HAS BEEN CHANGED**
**EVALUATED ARTIFACTS:** `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md` §1.1/§1.3/§1.4 vs. §2.3; `research/experiments/analysis/simulate_power_r1_2e.py`; `r1_2e_power_simulation_results.json`
**ENGINEERING REVISION:** branch `research/i7-engineering-preflight`
**DIRECTIVE COMPLIANCE:** This notice identifies the mismatch only. It does **not** select a new margin, does **not** change the confirmatory sample size, and does **not** claim that adaptive inference has been validated.

---

## 1. The two parameterizations currently coexist in the R1.2e protocol

### 1.1 The sample-size calculation lives on the log-ratio scale
R1.2e §1.1 sizes the confirmatory sample from the paired write **log-ratio** $d_i = \ln(W_{\text{amin},i} / W_{B,i})$:

$$N_{\text{req}} = \left\lceil \underbrace{\frac{(z_{0.975} + z_{0.80})^2}{(\mu_d - \delta_{\text{NI}})^2}}_{=\,884.14} \times \hat{\sigma}_d^2 \right\rceil, \quad \mu_d = -0.01114,\; \delta_{\text{NI}} = \ln(0.90) = -0.10536$$

Every input is dimensionless (log units): the effect $\mu_d$, the margin $\delta_{\text{NI}}$, and the dispersion $\hat{\sigma}_d \in [0.06, 0.25]$. `simulate_power_r1_2e.py::compute_required_n` and `compute_t_power` implement exactly this scale, and the JSON metadata records `margin_non_inferiority_10pct = -0.10536` and `prospective_sample_size_factor_empirical = 884.14`.

### 1.2 The declared primary estimand lives on the absolute scale
R1.2e §2.3 re-registers the **primary** confirmatory estimand as the paired write-rate **difference**

$$\Delta \dot{W}_i = \frac{W_{\text{amin},i} - W_{B,i}}{T_{\text{window}}} \;\; [\text{writes/s}], \qquad H_0: \Delta\dot{W} \le -\delta_{\text{rate\_NI}} \;\text{vs.}\; H_1: \Delta\dot{W} > -\delta_{\text{rate\_NI}}$$

with margin $\delta_{\text{rate\_NI}} = 25.0$ writes/s (paced, $\lambda = 250$) or $26.5$ writes/s (free-running, $\bar W \approx 265$), tested by a Student-$t$ at $\alpha = 0.025$ with $df = N-2$.

## 2. The mismatch

**The factor 884.14 sizes a log-ratio test, but the confirmatory hypothesis is now tested on the absolute scale. These are different tests, and the sample-size mathematics of one does not transfer to the other.** Specifically:

1. **Standardized effect is scale-dependent.** On the log scale the standardized effect is $(\mu_d - \delta_{\text{NI}})/\sigma_d$ with $\sigma_d$ dimensionless. On the absolute scale it is $(\mu_{\Delta\dot W} + \delta_{\text{rate\_NI}})/\sigma_{\Delta\dot W}$ with $\sigma_{\Delta\dot W}$ in writes/s. $\sigma_{\Delta\dot W}$ is **not** a rescaling of $\sigma_d$: since $\Delta\dot W = W_B(e^{d}-1)$, the delta method gives
   $$\mathrm{Var}(\Delta\dot W) \approx e^{2\mu_d}\left(\mathrm{Var}(W_B) + \bar W_B^{\,2}\sigma_d^2 + 2\bar W_B\,\mathrm{Cov}(W_B, d)\right),$$
   so the absolute-scale variance depends on the baseline write-rate level and its covariance with the log-ratio. Substituting $\sigma_d^2$ into $N = \lceil 884.14\,\sigma^2\rceil$ therefore sizes a different estimand.
2. **Margins agree only at one operating point.** A relative margin $\ln(0.90)$ equals an absolute margin $\delta_{\text{rate\_NI}}$ only when the realized baseline equals the calibration write rate ($\approx 250$–$265$ writes/s, from which 25.0/26.5 were taken as "10% of baseline"). If the realized baseline drifts, the two margins diverge and the sizing inherits the drift.
3. **The §1.4 power figures are log-scale figures.** The realized-power claims (95.6% / 58.7% / 30.8% / 18.2% at $N\in[8,10]$) and the entire `workload_specific_sensitivity_matrix.power_curves` block of `r1_2e_power_simulation_results.json` are power for the **log-ratio** NI test under $\mu_d = -0.01114$. They must not be cited as power for the absolute write-rate estimand until re-derived from pilot estimates of $\sigma_{\Delta\dot W}$ on the absolute scale.
4. **The $N\in[8,10]$ clamp inherits the mismatch.** $N_{\text{confirmatory}} = \min(10,\max(8,N_{\text{req}}))$ with $N_{\text{req}} = \lceil 884.14\,\hat\sigma_d^2\rceil$ was derived under the log-ratio effect. Under the absolute estimand, whether $N=10$ is adequate in Regimes 2–4 is **unknown** — not "58.7%/30.8%/18.2% powered", and not proven inadequate either. The honest classification is *unquantified on the absolute scale pending pilot data*.

## 3. What is and is not affected

| Component | Affected? | Why |
| :--- | :---: | :--- |
| Factor 884.14, $N_{\text{req}}$, §1.4 power curves, JSON power/sensitivity blocks | **Yes** | All log-scale quantities applied to an absolute-scale estimand |
| $df = N-2$ Type-I-error adjustment | No | Scale-free property of the adapted one-sample $t$ (verified by Monte Carlo under $H_0$ in §1.2; the verification simulates the $t$ statistic directly, independent of measurement units) |
| Global 400 ceiling, 372/388 budget reconciliation, $N\in[8,10]$ as **budget bounds** | No | Execution-budget constraints, not statistical claims (the *statistical adequacy* of the bounds is affected, their existence is not) |
| Secondary estimand $D_{\text{sym}}$, starvation composite ranking, EXP-2 causal TOST | No | Registered on their own scales; TOST on $\beta$ is unaffected (but see the arrival-sample caveat in the R2 engineering checkpoint §6) |
| Zero-safety rationale for the absolute estimand | No | §2.2's repudiation of log singularities stands |

## 4. Why this cannot be silently resolved now

Re-deriving $N_{\text{req}}$ on the absolute scale requires a pilot estimate of $\sigma_{\Delta\dot W}$ in writes/s per cell. The pilot is exactly what the next authorization gate controls. Any absolute-scale re-sizing performed now would either (a) reuse the frozen G4 log-ratio dispersions through an unvalidated transform — reintroducing the variance-pooling error repudiated in §1.1 — or (b) silently invent a variance assumption, which the review directive explicitly forbids. The pre-registered ceilings ($N\in[8,10]$, 400 runs) remain in force as **budget** bounds.

## 5. Decision options for the next review gate (enumerated, not chosen)

- **Option A — re-register the confirmatory test on the log-ratio scale** to match the existing sizing. Rejected-leaning: conflicts with §2.2's zero-safety repudiation of logarithmic singularities ($d$ is undefined at $W=0$, and §2.3's Rule S-5 declares $W=0$ a valid treatment outcome).
- **Option B — keep the absolute estimand; re-derive sizing from pilot estimates of $\sigma_{\Delta\dot W}$** after the pilot gate, with the internal-pilot $df=N-2$ rule unchanged. The current $[8,10]$ clamp then serves as the budget bound, with power re-assessed post-pilot.
- **Option C — dual registration:** primary test on the absolute scale, log-ratio power analysis retained only as a *sizing heuristic* explicitly labeled as such, with the discrepancy documented in the analysis plan.

**Recommendation (non-binding):** Option B, because it is the only option consistent with both §2.2 (zero-safety) and §1.2 (Type-I-error control under internal-pilot retention), and it requires no new margin: $\delta_{\text{rate\_NI}}$ stays 25.0/26.5 writes/s as registered.

## 6. Reproduction

`python research/experiments/analysis/simulate_power_r1_2e.py` reproduces `r1_2e_power_simulation_results.json` byte-identically (SHA-256 `4DCE7454…E37D1`, verified 2026-10-29 on Python 3.13.7 / numpy 2.5.1 / scipy 1.18.0). The hash matches the manifest recorded in `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md` §7, so the mismatch documented here is a property of the delivered, verified artifacts — not of a stale copy.
