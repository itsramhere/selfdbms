# R2 Statistical Decision Preparation — Absolute Write-Rate Estimand and Pilot-Based Sizing

**DOCUMENT ID:** `R2_STATISTICAL_DECISION_PREPARATION.md`
**STATUS:** RECOMMENDATION PACKAGE FOR EXTERNAL REVIEW — **NO MARGIN OR SAMPLE SIZE HAS BEEN CHANGED; THE PROTOCOL IS NOT FROZEN**
**INPUTS:** `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md`; `R1_2E_ESTIMAND_SCALE_MISMATCH_NOTICE.md`; `I7_R2_ENGINEERING_CHECKPOINT.md` §6.3 (instrumentation limitations bearing on D-2)
**DATE:** 2026-10-29

---

## 1. Recommendation

**Retain the absolute write-rate primary estimand** and determine its sample-size requirements from **properly controlled pilot observations taken on the absolute scale**. Concretely:

- Primary estimand (unchanged from R1.2e §2.3): the paired in-window write-rate difference
  $$\Delta \dot{W}_i = W_{\text{amin},i} - W_{B,i} \quad [\text{writes/s, same measured window}]$$
- Non-inferiority margin (unchanged, as registered): $\delta_{\text{rate\_NI}} = 25.0$ writes/s (paced, $\lambda = 250$) and $26.5$ writes/s (free-running, $\bar W \approx 265$).
- Confirmatory bounds (unchanged, as registered): $N \in [8,10]$ triplets per cell; strict 400-run global ceiling.

Rationale for retention: the absolute scale is the only zero-safe scale (R1.2e §2.2 repudiated the log-ratio's singularity at $W=0$, and Rule S-5 declares $W=0$ a valid treatment outcome), it matches the harness's primary reported quantity (in-window write throughput, now ledger-conserved), and it is the scale on which the margin was actually registered. The log-ratio scale is retained **only** as a descriptive diagnostic, never as the confirmatory test.

## 2. The compatible primary test

One-sided paired Student-$t$ non-inferiority test **on the absolute scale**:

$$H_0: \bar{\Delta \dot W} \le -\delta_{\text{rate\_NI}} \quad \text{vs.} \quad H_1: \bar{\Delta \dot W} > -\delta_{\text{rate\_NI}}, \qquad \alpha = 0.025 \text{ (one-sided)}, \quad \text{reject if } t > t_{N-2,\,1-\alpha}$$

with $t$ the usual one-sample statistic over the $N$ per-cell paired differences. The $df = N-2$ critical value is the pre-registered internal-pilot adjustment (R1.2e §1.2); it is scale-free and carries over unchanged. Supporting procedures, all scale-compatible and unchanged:

- **Starvation composite (Rule S-5):** two-part non-parametric ranking (win/loss on $W=0$ patterns; paired permutation on $\Delta\dot W$ when both arms complete) — operates on the absolute scale by construction.
- **Secondary estimand:** symmetric percentage difference $D_{\text{sym}} \in [-2, +2]$ — unchanged.
- **Multiplicity:** the Bretz graphical procedure (families $\alpha_1 = \alpha_2 = 0.025$, cross-family transfers) is a level-allocation structure and is scale-free; it applies unchanged — reviewer confirmation requested (§7, D-5).
- **EXP-4 survival procedures** (censored sign test, RMST) are on their own time scale and are unaffected.

## 3. The relevant variance estimate

The only variance that sizes the primary test is the **per-cell standard deviation of the paired absolute differences**, $\hat\sigma_{\Delta\dot W,\text{cell}}$, in writes/s, computed from that cell's own pilot observations with the $n-1$ denominator:

$$N_{\text{req}} = \left\lceil \frac{(z_{1-\alpha} + z_{1-\beta})^2 \, \hat\sigma_{\Delta\dot W,\text{cell}}^2}{(\hat\mu_{\Delta\dot W,\text{cell}} + \delta_{\text{rate\_NI}})^2} \right\rceil$$

Explicit exclusions: the frozen historical log-ratio dispersions ($\hat\sigma_d = 0.05872$ pooled, or the regime values 0.06–0.25) are **not** inputs — the R1.2e §1.1 repudiation and the scale mismatch notice both apply. There is no pooling across cells; each of the 12 cells is sized from its own regime. For reference only (not a sizing input): the frozen G4 empirical log-ratio effect $-1.11\%$ corresponds to roughly $-2.9$ writes/s at a 265 writes/s baseline, which is what the pilot must re-estimate properly as $\hat\mu_{\Delta\dot W}$ on the absolute scale.

**Honest caveat about pilot noise:** a 4-observation SD is itself highly uncertain (a $\chi^2_3$ 95% interval for $\sigma$ spans roughly $[0.57\hat\sigma,\,3.7\hat\sigma]$). The pre-registered $N\in[8,10]$ clamp absorbs this noise by design; the pilot-based $N_{\text{req}}$ is therefore a **bounded adaptation input**, not a free re-sizing.

## 4. How sample-size adaptation affects inference

Internal-pilot re-estimation with the pilot retained makes $N$ and the final sample variance correlated, which inflates the Type I error of a nominal-$t$ test (R1.2e §1.2: empirically 0.0264–0.0396 unadjusted). The pre-registered controls are: (a) a **single** adaptation, pilot → confirmatory N, per cell; (b) critical values at $df = N-2$, Monte-Carlo verified at $\le 0.0245$; (c) the $[8,10]$ clamp bounding the adaptation's reach. Any additional adaptation (unblinded interim monitoring, mid-confirmatory re-sizing, outcome-adaptive exclusion) is **not** covered by that verification and must not be introduced without a fresh Monte-Carlo Type-I-error analysis approved by the reviewer.

## 5. Retained pilot (recommended) vs. independent pilot

**Recommendation: retain** (internal pilot — the first 4 triplets of each cell's confirmatory $N$). Reasons:

1. **Budget:** an independent pilot would consume $4 \times 12 = 48$ additional ceiling runs before any confirmatory power exists; retention spends the same observations as the first quarter of $N$.
2. **Pre-registration coherence:** the $df = N-2$ adjustment was derived and Monte-Carlo verified exactly for the retained case (R1.2e §1.2). An independent pilot would make that adjustment conservative (harmless but unvalidatable as written) and would require re-registration.
3. **Drift control:** the D3 milestone anchors bracket the campaign in time; a retained pilot keeps the earliest observations inside the same anchored sequence rather than in a separate campaign phase.

## 6. How the 400-run ceiling constrains conclusions

The ceiling is a **hard epistemic constraint**, and every physical execution counts against it (pilot attempts, drift anchors, retries, replacements, interrupted and aborted executions — enforced by the execution ledger, `runlistctl ceiling`):

- Worst-case campaign: $372$ planned $+\,16$ replacement/retry $= 388 \le 400$; at least 12 runs of headroom.
- Where the pilot-based $\hat\sigma_{\Delta\dot W}$ implies $N_{\text{req}} > 10$, the capped $N=10$ has **reduced power**, and failure to reject $H_0$ must be reported as **inconclusive evidence** (never as falsification of the engine or of A-min), with the realized confidence interval reported.
- Because the ceiling admits no extension, conclusions must be staged per cell: cells whose pilot variance fits $N \le 10$ can be conclusive; high-variance cells are pre-registered as likely-inconclusive. This staging is a **reporting rule**, not a data-dependent exclusion (ITT holds; Rule S-1 governs re-executions only).

## 7. Decisions that still require reviewer approval

| ID | Decision | Status |
| :--- | :--- | :--- |
| D-1 | Confirm the primary test and margin as stated in §2 (unchanged values: 25.0 / 26.5 writes/s) | **Open — reviewer** |
| D-2 | Treatment of the one-sided active-holder sample bias in the EXP-2 TOST (regression attenuation; see R2 engineering checkpoint §6.3) | **Open — reviewer** |
| D-3 | Approve the pilot protocol: pilot = first 4 triplets per cell, retained; $\hat\sigma_{\Delta\dot W}$ per cell with $n-1$ denominator; no pooling | **Open — reviewer** |
| D-4 | Any adaptation beyond the single pre-registered pilot→N step (requires new Type-I-error verification) | **Open — reviewer; default is NO** |
| D-5 | Confirm the Bretz multiplicity graph applies unchanged on the absolute scale | **Open — reviewer** |
| D-6 | Exact wording of the inconclusive-evidence classes per variance regime | **Open — reviewer** |

**The statistical protocol is not frozen.** It is pre-registered pending D-1…D-6; nothing in this document alters a registered value.
