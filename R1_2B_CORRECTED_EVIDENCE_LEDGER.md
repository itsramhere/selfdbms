# Phase I7 — Checkpoint R1.2b: Corrected Evidence Ledger and Analytical Erratum

**DATE:** 2026-10-29  
**DATASET REFERENCE:** `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\I7_WINDOWS_RESULTS.zip` (SHA-256: `52c52ad137acac08035b0e508acb2707f2abaaa506478f171d32c829fab2723b`)  
**EVALUATED GIT REVISION:** `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`  
**STATUS:** FORMAL SCIENTIFIC CORRECTION OF R1.2a RECORD. NO NEW BENCHMARKS.

---

## 1. Correction 1: Block E1 Trial 09 Write Accounting and Arm A-min Error Investigation

### A. Raw JSON Accounting Reconciliation
In the R1.2a report, Block E1 trial 09 was erroneously described as a "read-only cell with writes in writer threads... undefined in E1 due to $W_B \approx W_A \approx 0$."

Direct inspection of `i7_E1_r08_t09_{A,AMIN,B}_a01.json` refutes that description. Block E1 ($r=8$) contained an active free-running writer thread. The exact operations ledger from raw JSON is:

| Metric | Approach A (`_A_a01.json`) | Approach A-min (`_AMIN_a01.json`) | Approach B (`_B_a01.json`) | Contrast AMIN / A | Contrast AMIN / B |
| :--- | :---: | :---: | :---: | :---: | :---: |
| **Write Ops Attempted** | 7 | 2,674 | 2,659 | $382.0\times$ | $1.0056$ |
| **Write Ops Completed (In-Window)** | **5** | **2,671** | **2,657** | **$534.2\times$** | **$1.0053$** |
| **Write Ops Completed (Late)** | 2 | 2 | 2 | — | — |
| **Write Ops Completed (Total)** | 7 | 2,673 | 2,659 | $381.9\times$ | $1.0053$ |
| **Write Throughput (writes/s)** | **0.50** | **267.10** | **265.70** | **$534.2\times$** | **$1.0053$** |
| **Error Ops Count** | 0 | **1** | 0 | — | — |
| **Validation Errors** | 0 | 0 | 0 | — | — |
| **Read Ops Completed (In-Window)** | 91 | 167 | 176 | $1.835\times$ | $0.9489$ |
| **Read Throughput (reads/s)** | **9.10** | **16.70** | **17.60** | **$1.835\times$** | **$0.9489$** |

### B. Scientific Implications of Corrected E1 Accounting
1. **Severe Approach A Writer Starvation:** In Approach A, 8 concurrent readers holding `RLock` during table scans starved the writer thread so severely that only 7 write operations were attempted and only 5 completed in-window over the entire 10-second window ($0.50\text{ writes/s}$).
2. **Massive Progress Capture:** Approach A-min completed 2,671 in-window writes ($267.1\text{ writes/s}$), matching Approach B (2,657 writes, $265.7\text{ writes/s}$) with a ratio of **$1.0053$**.
3. **Capture Fraction ($CF_w$):**
   $$CF_w = \frac{W_{\text{amin}} - W_A}{W_B - W_A} = \frac{2,671 - 5}{2,657 - 5} = \frac{2,666}{2,652} = \mathbf{1.0053}$$
   This satisfies the preregistered threshold ($CF_w \ge 0.7$) for this single observed triplet. However, because $N=1$, it remains **underpowered to establish confidence intervals**.

### C. Investigation of the Single Arm A-min Operation Error
In `i7_E1_r08_t09_AMIN_a01.json`, `operations.error_ops_count` is recorded as `1`:
- In `harness2/run.go`:
  ```go
  sh.WritesAttempted++
  tx := man.Begin()
  if tx == nil { sh.WritesError++; continue }
  tx.Put(key, val)
  if err := tx.Commit(); err != nil {
      sh.WritesError++
  } else {
      sh.WritesCompleted++
  }
  ```
- Ledger arithmetic: $\text{WritesAttempted} (2,674) - \text{WritesCompleted} (2,673) = 1$.
- In `lsmengine/transaction.go`:
  ```go
  if err == nil && latestSeqNo > t.ReadTs {
      t.Man.Mu.Unlock()
      t.Abort()
      return errors.New("Write-write conflict detected. Transaction aborted.")
  }
  ```
- **Finding:** Exactly one write transaction encountered a write-write conflict or engine abort during `Commit()`. The transaction was cleanly rolled back (`t.Abort()`), leaving zero data corruption (`validation_error_count = 0`). The trial completed with status `SUCCESS`.

---

## 2. Correction 2: Rule X-6 and Event Count Validity vs. Timing Performance

Preregistration rule X-6 stated:
> *"P2 is only evaluated if realized publication count = 20 in every included trial."* (`07_ANALYSIS_PLAN.md` §3.2).

### Separation of Event Count Validity vs. Deadline Performance
Inspection of `lsm_diagnostics` across all four Approach A overrun records clarifies this distinction:

| Trial ID | Block / Cell | Target Pubs | Released Pubs | Realized In-Window | Realized Late | Total Realized | Measured Flushes | Trial Status |
| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| `i7_E2_pub_physical_t01_A` | E2 / `pub_physical` | 20 | 20 | **19** | 1 | **20** | **20** | `FAILED` |
| `i7_E2_pub_physical_t09_A` | E2 / `pub_physical` | 20 | 20 | **17** | 3 | **20** | **20** | `FAILED` |
| `i7_E4_L3_t02_A` | E4 / $L_3$ open-loop | 20 | 20 | **10** | 10 | **20** | **20** | `FAILED` |
| `i7_E4_L3_t06_A` | E4 / $L_3$ open-loop | 20 | 20 | **15** | 5 | **20** | **20** | `FAILED` |

### Formal Correction:
1. **Event Count Validity Was Maintained:** In all four trials, `measured_flushes = 20` and `publication_events_realized = 20`. All 20 scheduled flushes were released by the orchestrator and completely executed by the engine. The trials were **not mechanically truncated**.
2. **Timing Deadline Was Violated:** Due to Approach A's exclusive lock contention holding the manager lock for 300–640 ms per flush, flushes were delayed past the 10.0-second measurement boundary into the post-window drain phase.
3. **Classification:** In-window publication shortfall is a **treatment-related performance failure**, not a fixture defect. The trials represent valid evidence of Approach A's failure to meet deadlines under concurrency.

---

## 3. Correction 3: Complete Ledger of All 20 Named P1–P5 Estimands

Below is the definitive census of all 20 named estimands specified in `07_ANALYSIS_PLAN.md` §3:

| ID | Block / Condition | Target Question / Estimand | Preregistered Decision Rule | Planned Population | Realized Valid Population | Final Audit Status |
| :---: | :--- | :--- | :--- | :---: | :---: | :--- |
| **P1a** | E1 ($r=8$) | $R_w(\text{A-min}/\text{A})$ write ratio | Supported if 95% CI lower $> 1.0$ and point estimate $\ge 3.0$; refuted if CI upper $< 1.5$. | $N=30$ | $N=1$ | **Underpowered ($N=1$):** Point estimate is $534.2\times$, but CI uncomputable. |
| **P1b** | E1 ($r=8$) | Write capture fraction $CF_w$ | Supported if median $CF_w \ge 0.7$ and CI lower $\ge 0.5$; refuted if median $\le 0.3$. | $N=30$ | $N=1$ | **Underpowered ($N=1$):** Point estimate is $1.0053$, but CI uncomputable. |
| **P1c** | E1 ($r=8$) | $R_w(\text{A-min}/\text{B})$ write non-inferiority | Supported if one-sided 95% CI lower bound $> 0.80$. | $N=30$ | $N=1$ | **Underpowered ($N=1$):** Point estimate is $1.0053 > 0.80$. |
| **P1d** | E1 ($r=8$) | $R_r(\text{A-min}/\text{B})$ read non-inferiority | Supported if $R_r(\text{A-min}/\text{A}) \ge 1.5$ and $R_r(\text{A-min}/\text{B}) > 0.90$. | $N=30$ | $N=1$ | **Underpowered ($N=1$):** Point estimates satisfy ($1.84 \ge 1.5, 0.95 > 0.90$). |
| **P2a** | E2 ($r=4$) | $L(\text{A-min}/\text{A})$ event-conditioned latency | Supported if 95% CI upper bound $< 0.50$. | $N=30$ | $N=2$ (Overrun in A) | **Underpowered ($N=2$):** Lock convoying observed; formal CI underpowered. |
| **P2b** | E2 ($r=4$) | $L(\text{A-min}/\text{B})$ latency non-inferiority | Supported if 95% CI upper bound $< 1.15$. | $N=30$ | $N=4$ | **Underpowered ($N=4$):** 95% CI $[0.88, 1.24]$ exceeds 1.15 margin. |
| **P2c** | E2 ($r=4$) | `block_ratio = cond_max / hold` | Descriptive; expected $\approx 1$ for A, $\approx 0$ for A-min/B. | $N=30$ | $N=4$ | **Exploratory / Descriptive.** |
| **P2d** | E2 ($r=4$) | `stall_time_share` in physical mode | Secondary; same directional expectation as P2a. | $N=30$ | $N=4$ | **Exploratory / Descriptive.** |
| **P3a** | E3 ($r=4$) | Free regime $R_r(\text{B}/\text{A})$ replication | Supported if 95% CI upper bound $< 0.95$. | $N=30$ | $N=2$ | **Confounded:** Arm A completed only 1,200 writes vs 3,164 in B. |
| **P3b** | E3 ($r=4$) | Equal-work $R_r(\text{B}/\text{A})$ non-inferiority | Supported if CI lower $> 0.90$; penalty persists if upper $< 0.95$. | $N=30$ | $N=5$ | **OFFICIALLY VOID:** MC-3 failed (Arm A starved by 45%–78%). |
| **P3c** | E3 ($r=4$) | Workload D regime interaction $\Delta$ | Supported if 95% CI excludes 0 and $|\Delta| \ge 0.5 |\ln R_r(\text{free})|$. | $N=30$ | $N=5$ | **OFFICIALLY VOID:** MC-3 failed. |
| **P3d** | E3 ($r=4$) | Total-op ratio $R_{\text{tot}}(\text{B}/\text{A})$ | Descriptive composition shift. | $N=30$ | $N=2$ | **Exploratory / Descriptive.** |
| **P3e** | E3 ($r=1$) | Median read latency ratio at $r=1$ | Secondary lock-sharing test. | $N=30$ | $N=0$ | **Unexecuted:** $r=1$ cell unrun in G4. |
| **P4a** | E4 ($L_2$) | Response latency $Y_{99}$ at $0.60 C_{\text{ref}}$ | Supported if both A-min/A and B/A CI upper bounds $< 0.50$. | $N=30$ | $N=1$ | **Underpowered ($N=1$):** $N=99$ requests; $p_{99}$ statistically invalid. |
| **P4b** | E4 ($L_3$) | Overload abandonment rate at $0.90 C_{\text{ref}}$ | Descriptive; expected $\text{Arm A} > \text{Arm A-min} \approx \text{Arm B}$. | $N=30$ | $N=2$ | **Supported (Descriptive):** Arm A had 3–4 abandoned; A-min/B had 0. |
| **P4c** | E4 ($L_1..L_3$) | Coordinated omission inflation factor | Descriptive comparison of E2 vs E4 tail inflation. | $N=30$ | $N=5$ | **Exploratory / Descriptive.** |
| **P4d** | E4 ($L_1$) | Mean queueing delay $q_{\text{mean}}$ at $L_1$ | Validity check ($q_{\text{mean}} < 1\text{ ms}$). | $N=30$ | $N=2$ | **Passed:** $q_{\text{mean}} < 0.2\text{ ms}$. |
| **P5a** | E5 vs E1 | Dose-response write progress scaling | Supported if A CI upper $< 0.80$ and A-min/B contain 1.0. | $N=30$ | $N=1$ | **OFFICIALLY VOID:** MC-5 failed (keys out of range). |
| **P5b** | E5 vs E1 | Regression of writes on $T_{\text{readCS}}$ | Negative slope for A; zero for A-min/B. | $N=30$ | $N=1$ | **OFFICIALLY VOID:** MC-5 failed. |
| **P5c** | D2 | Component cost Θ(k) vs leases | Descriptive microbenchmark. | $N=15$ | $N=0$ | **Unexecuted:** Block D2 omitted from G4. |

---

## 4. Correction 4: Statistical Methodology Erratum

### A. Mathematical Inconsistency in `07_ANALYSIS_PLAN.md` §3
In `07_ANALYSIS_PLAN.md` §3, the text states:
> *"The reported effect is `exp(median_t d_t)` (ratio of geometric means) with a BCa bootstrap 95 % CI over triplets (10,000 resamples)."*

**Mathematical Erratum:**
- $\exp(\bar{d}) = \exp\left(\frac{1}{N}\sum_{t=1}^N \ln(X_{t,\text{a1}} / X_{t,\text{a2}})\right) = \left(\prod_{t=1}^N \frac{X_{t,\text{a1}}}{X_{t,\text{a2}}}\right)^{1/N}$ is the **geometric mean ratio**.
- $\exp(\text{median}_t d_t)$ is the **median ratio** (exponentiated median log-ratio), which does not equal the geometric mean ratio unless the distribution is perfectly symmetric.

### B. Bootstrap Method Distinction: BCa vs. Percentile
- **Percentile Bootstrap:** Computes the empirical 2.5th and 97.5th percentiles of 10,000 bootstrap resamples. This is what was implemented in `reproduce_all_metrics.py` and `reconcile_r1_2a.py`.
- **BCa Bootstrap (Bias-Corrected and Accelerated):** Adjusts the percentile intervals using a bias correction factor $z_0$ and an acceleration parameter $a$ derived from the jackknife influence function.
- **Reporting Standard:** The analytical record explicitly reports both the geometric mean ratio and the median ratio, and clearly labels interval construction as **percentile bootstrap** (with BCa calculated for robustness).

---

## 5. Correction 5: Reclassification of 12-Pair Non-Inferiority as Exploratory

In the R1.2a report, we noted that across the 12 closed-loop pairs (excluding E4 and E5), the geometric mean ratio 95% bootstrap CI was $[0.9222, 1.0079]$, exceeding the $0.90$ non-inferiority margin.

**Formal Reclassification:**
- Preregistered hypothesis **P1d** was pre-declared exclusively for **Block E1 at $r=8$** ($N=30$ planned).
- Pooling 12 pairs across Blocks E1, E2, and E3 is a **post-hoc exploratory pooling**. It cannot be represented as confirmatory validation of P1d.
- It is classified as: *"Exploratory evidence suggesting Approach A-min throughput non-inferiority at the 10% margin across the evaluated closed-loop configurations."*

---

## 6. Correction 6: Resolution of Cochran Heterogeneity Reporting

In the preliminary R1.2a text, a Cochran heterogeneity test was referenced narrative-style without an exact test statistic.

**Rigorous Methodological Resolution:**
1. In Block E4, all 5 triplets report identical throughput ($d_{\log} = 0.0000$), producing **zero within-cell variance**.
2. Standard inverse-variance meta-analytic weighting ($w_i = 1 / \sigma_i^2$) divides by zero and is mathematically undefined without ad-hoc regularization.
3. An unweighted one-way ANOVA across the five blocks yields $F(4, 13) = 0.768, p = 0.565$; Kruskal-Wallis across E2, E3, and E4 yields $H = 0.836, p = 0.658$.
4. **Conclusion:** With sample sizes ranging from $N=1$ to $N=7$, statistical tests are severely underpowered to detect heterogeneity. However, the qualitative difference between arrival-capped open-loop execution (E4) and free-running closed-loop execution (E1–E3) makes pooling mathematically and conceptually invalid regardless of formal test p-values.

---

## 7. Correction 7: Removal of Unsupported Hyperbolic Claims

All instances of the following claims are formally withdrawn and excised from the scientific record:
1. *"Universal equivalence between Approach A-min and Approach Bwithin $\pm 5\%$."* — **Withdrawn.** True closed-loop 95% CI is $[-7.49\%, +1.00\%]$.
2. *"Complete elimination of write starvation."* — **Withdrawn.** A-min eliminates writer queue convoying under the tested laptop parameters, but behavior under extreme memory pressure or high disk saturation remains uncharacterized.
3. *"Proven platform-independent correctness."* — **Withdrawn.** Windows binaries were compiled without `-race` due to cgo unavailability. Concurrency safety on Windows rests on unit tests and Linux verification.
