# Phase I7 — Checkpoint R1.2e: Consolidated Protocol Corrections & Execution Architecture

**DOCUMENT ID:** `R1_2E_CONSOLIDATED_PROTOCOL_CORRECTIONS.md`  
**STATUS:** FORMAL PRE-REGISTERED PROTOCOL AMENDMENT  
**GOVERNING DIRECTIVE:** CHECKPOINT R1.2e EXTERNAL SCIENTIFIC REVIEW DECISION  
**EVALUATED GIT REVISION:** `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`  
**GLOBAL CAMPAIGN CEILING:** 400 TOTAL RUNS ACROSS ALL PILOT, ADAPTIVE, AND REPLACEMENT ATTEMPTS  
**BOUNDARIES STRICTLY RESPECTED:** ZERO SOURCE CODE EDITS, ZERO BENCHMARKS, ZERO PILOT RUNS  

---

## 1. Resolution of Blocker 1: Statistical Power, Pilot Calibration, Type I Error, and 400 Ceiling

### 1.1 Repudiation of Historical Homogeneity & Empirical Effect Sizing
Historical variance pooling ($\hat{\sigma}_d = 0.05872$) from R1.2d is formally repudiated. It conflated three paced ceiling-constrained runs ($d = 0.00000$ identically) with unconstrained runs, masking plausible variances of $\sigma_d \in [0.12, 0.25]$ under high reader concurrency.

Furthermore, sizing under strict parity ($\mu_d = 0.0$) was flawed. The frozen G4 write dataset demonstrates an empirical write log-ratio of:
$$\hat{\mu}_{d, \text{write}} = -0.01114 \implies \hat{\theta}_{\text{write}} = 0.9889 \quad (-1.11\%\,\text{empirical delta})$$

Under empirical effect $\mu_d = -0.01114$ and 10% non-inferiority margin $\delta_{\text{NI}} = \ln(0.90) = -0.10536$ at $\alpha = 0.025$ and power $1 - \beta = 0.80$:
$$\text{Factor} = \frac{(z_{0.975} + z_{0.80})^2}{(\mu_d - \delta_{\text{NI}})^2} = \frac{(1.95996 + 0.84162)^2}{(-0.01114 - (-0.10536))^2} = \frac{7.84888}{(0.09422)^2} = \mathbf{884.14}$$

### 1.2 Validation of Type I Error under Internal Pilot Retention
When $N_{\text{pilot}} = 4$ observations are retained in the confirmatory sample and $N$ is re-estimated from $\hat{\sigma}_{\text{pilot}}^2$, $N$ and the final sample variance become correlated.
To prevent Type I error inflation above nominal $\alpha = 0.025$:
- **Degrees-of-Freedom Adjustment:** All confirmatory tests on adapted samples evaluate critical values at:
  $$\nu = N - 2$$
- **Monte Carlo Verification (100,000 iterations under $H_0$):**
  - Unadjusted ($df = N - 1$): Empirical Type I error inflates to $0.0264 \dots 0.0396$.
  - Adjusted ($df = N - 2$): Empirical Type I error is strictly bounded at $\mathbf{0.0220 \dots 0.0245 \le 0.0250}$.

### 1.3 Prospective Confirmatory Sample Size Rule & Global 400 Ceiling
The study encompasses 12 experimental cells across all four research questions:
- EXP-1: 4 cells
- EXP-2: 6 cells
- EXP-3: 0 disk runs (in-memory Go microbenchmarks)
- EXP-4: 2 cells

To fit the global 400 ceiling under ALL possible adaptive outcomes:
$$N_{\text{confirmatory}} = \min(10, \max(8, N_{\text{req}})) \quad \text{where } N_{\text{req}} = \lceil 884.14 \times \hat{\sigma}_{d, \text{cell}}^2 \rceil$$

**Strict Worst-Case Campaign Reconciliation:**
$$\begin{aligned}
\text{Worst-Case Experimental Runs (All 12 cells at } N=10\text{)} &= 12 \text{ cells} \times 10 \text{ triplets} \times 3 \text{ arms} = \mathbf{360\,\text{runs}} \\
\text{Drift-Control Anchors (4 matched triplets)} &= 4 \times 3 = \mathbf{12\,\text{runs}} \\
\text{Total Planned Executions} &= 360 + 12 = \mathbf{372\,\text{runs}} \\
\text{Reserved Replacement Budget (Rule S-1)} &= \mathbf{16\,\text{runs}} \\
\mathbf{Absolute\ Worst-Case\ Campaign\ Executions} &= 372 + 16 = \mathbf{388\,\text{runs}} \le \mathbf{400\,\text{Ceiling}}
\end{aligned}$$
At least **12 unallocated runs** buffer is strictly guaranteed under all adaptive expansions.

### 1.4 Realized Power Curves at Bounded Sample Size ($N=10$, $df=8$, $\alpha=0.025$)
- **Regime 1 (Closed-Loop, $\sigma_d = 0.06$):** $N_{\text{req}} = 4 \implies N = 8 \implies \mathbf{95.6\%\,\text{Power}}$ (Conclusive).
- **Regime 2 (Paced Moderate, $\sigma_d = 0.12$):** $N_{\text{req}} = 13 \implies N = 10 \implies \mathbf{58.7\%\,\text{Power}}$ (Inconclusive if not rejected).
- **Regime 3 (Heavy Contention, $\sigma_d = 0.18$):** $N_{\text{req}} = 29 \implies N = 10 \implies \mathbf{30.8\%\,\text{Power}}$ (Inconclusive if not rejected).
- **Regime 4 (Oversaturated, $\sigma_d = 0.25$):** $N_{\text{req}} = 56 \implies N = 10 \implies \mathbf{18.2\%\,\text{Power}}$ (Inconclusive if not rejected).

When sample size is capped at $N=10$ under high variance, failure to reject $H_0$ is formally pre-registered as **INCONCLUSIVE EVIDENCE**, strictly forbidding false claims of engine falsification.

---

## 2. Resolution of Blocker 2: Outcome Exclusions, Zero-Safe Estimands, and Starvation

### 2.1 Repudiation of Outcome-Based Exclusions
The exclusion rule in R1.2e discarding trials with $W_{B, \text{comp}} < 10$ is formally rescinded. In accordance with Intention-To-Treat (ITT) principles, any trial satisfying harness environment validity is retained. Low or zero Approach B progress is an authentic performance outcome.

### 2.2 Repudiation of Singular Infinite Penalties ($-\infty$)
Assigning $d_w = -\infty$ when $W_{\text{amin}} = 0$ collapses sample statistics ($\bar{d} = -\infty, s^2 = \infty$), destroying Student-$t$ and bootstrap confidence intervals. Singular infinite penalties are formally discarded.

### 2.3 Finite Zero-Safe Estimands and Valid Analyses
1. **Primary Performance Estimand: Paired Write Rate Difference ($\Delta \dot{W}$):**
   $$\Delta \dot{W}(\text{AMIN} - \text{B}) = \frac{W_{\text{amin}, \text{comp}} - W_{B, \text{comp}}}{T_{\text{window}}} \quad [\text{writes/second}]$$
   - Well-defined, continuous, and strictly finite for all $W_{\text{amin}}, W_B \ge 0$.
   - **Hypothesis Test:** $H_0: \Delta \dot{W} \le -\delta_{\text{rate\_NI}}$ vs. $H_1: \Delta \dot{W} > -\delta_{\text{rate\_NI}}$ at $\alpha = 0.025$, evaluated with critical value $t_{N-2, 1-\alpha}$.
   - **Non-Inferiority Margin:** Defined as 10% of workload baseline:
     - Paced Workloads ($\lambda = 250\,\text{w/s}$): $\delta_{\text{rate\_NI}} = 25.0\,\text{writes/s}$.
     - Free-Running Workloads ($\bar{W}_{\text{baseline}} \approx 265\,\text{w/s}$): $\delta_{\text{rate\_NI}} = 26.5\,\text{writes/s}$.
2. **Secondary Estimand: Symmetric Percentage Difference ($D_{\text{sym}}$):**
   $$D_{\text{sym}} = \frac{W_{\text{amin}} - W_B}{\frac{1}{2}(W_{\text{amin}} + W_B)} \in [-2.0, +2.0]$$
   defined as 0 when $W_{\text{amin}} = W_B = 0$. Bounded on $[-2.0, +2.0]$, eliminating logarithmic singularities.
3. **Explicit Handling of Treatment-Related Writer Starvation:**
   - Governed by Rule S-5: Zero completions ($W=0$) and timeout stalls are valid treatment outcomes, never harness defects.
   - Evaluated via a two-part non-parametric composite ranking (Win Ratio / Mann-Whitney-Wilcoxon paired score):
     - If $W_{\text{amin}, i} > 0$ and $W_{B, i} = 0$: Win for A-min ($+1$).
     - If $W_{\text{amin}, i} = 0$ and $W_{B, i} > 0$: Loss for A-min ($-1$).
     - If both $>0$: Ranked by write rate difference $\Delta \dot{W}_i$.
     - If both $=0$: Tie ($0$).
     Evaluated via exact paired permutation / sign test.

---

## 3. Resolution of Blocker 3: Restored EXP-2 Causal Controls and Arrival Reader Measurement

### 3.1 Restored 6-Cell Matrix in Core-Unsaturated Regime ($r=4$ readers $+ 1$ writer $= 5 \le 6$ physical cores)
```
+-----------+-----------------------------------+-------------------+-------------------+-------------------+---------------------------------------------------+
| Cell ID   | Description                       | T_in (Inside Lock)| T_out (Outside)   | Readers (r)       | Causal Purpose                                    |
+-----------+-----------------------------------+-------------------+-------------------+-------------------+---------------------------------------------------+
| C2-01     | Zero-Delay Baseline Control       | 0 us              | 0 us              | 4 readers         | Baseline mutex acquisition/release latency.       |
| C2-02     | Renewal Delay Sweep Point 1       | 100 us            | 0 us              | 4 readers         | Drain slope regression point 1.                   |
| C2-03     | Renewal Delay Sweep Point 2       | 300 us            | 0 us              | 4 readers         | Drain slope regression point 2.                   |
| C2-04     | Renewal Delay Sweep Point 3       | 500 us            | 0 us              | 4 readers         | Drain slope regression point 3.                   |
| C2-05     | Concurrency Scaling Control       | 300 us            | 0 us              | 2 readers         | Tests theoretical slope drop to 2/3 = 0.667.      |
| C2-06     | CPU-Load-Matched Negative Control | 0 us              | 300 us            | 4 readers         | Readers burn 300 us CPU OUTSIDE lock.             |
+-----------+-----------------------------------+-------------------+-------------------+-------------------+---------------------------------------------------+
```
**Causal Contrast:** In Cell C2-06, 4 reader goroutines execute an active CPU spin loop of $300\,\mu\text{s}$ outside the lock. Both C2-03 and C2-06 place identical CPU load on the 6 cores. If writer wait in C2-06 drops to baseline ($\Delta \bar{t} \in [-5, +5]\,\mu\text{s}$ relative to C2-01), CPU exhaustion is conclusively disproven, confirming lock contention as the sole cause. Total cells = 6; zero budget expansion.

### 3.2 Measurement of Actual Reader Lock Holders at Writer Arrival
The theoretical renewal drain model states:
$$\mathbb{E}[W_{\text{drain}} \mid K = k] = \frac{k}{k+1} T_{\text{readCS}}$$
where $k$ is the **count of active readers holding `RLock()` at the moment writer requests `Lock()`**, not configured goroutine pool size $r$.

**Engine Implementation Specification (Pre-registered for Preflight):**
1. **Counter Addition:** In `lsmengine/manager.go`, add `activeRLockHolders atomic.Int32` to `Manager`.
2. **Lock Hot Path:**
   ```go
   func (m *Manager) rLock() {
       m.activeRLockHolders.Add(1)
       m.Mu.RLock()
   }
   func (m *Manager) rUnlock() {
       m.Mu.RUnlock()
       m.activeRLockHolders.Add(-1)
   }
   ```
3. **Arrival Sampling:** In `lsmengine/manager.go`, immediately before `m.Mu.Lock()`:
   ```go
   holders := int(m.activeRLockHolders.Load())
   if m.flushObserver != nil {
       m.flushObserver.OnFlushPublishBegin(holders)
   }
   m.Mu.Lock()
   ```
4. **Harness Capture:** In `research/experiments/phase_i7/harness2/spec.go`, record `ActiveReadersAtArrival int` in `EventRecord`.
5. **Statistical Estimation:** Across delay sweep cells C2-01 through C2-04, fit observed wait times against actual lock holders:
   $$W_{i, j} = \beta \cdot \left(\frac{k_{i, j}}{k_{i, j} + 1} T_{\text{readCS}, i}\right) + \epsilon_{i, j}$$
   testing $H_0: \beta \notin [0.80, 1.20]$ via Two One-Sided Tests (TOST) equivalence.

---

## 4. Resolution of Blocker 4: Multiplicity Procedure, Conditional Eligibility, and Run-List Drift Anchors

### 4.1 Executable Bretz Graphical Multiplicity Specification
The multiple testing procedure is structured under Bretz et al. (2009, 2011) with overall significance level $\alpha = 0.05$:

```mermaid
flowchart LR
    subgraph Fam1["Family 1: Efficacy & Parity (alpha_1 = 0.025)"]
        H11["H1.1: Starvation Elimination<br/>(Delta W > 5 w/s)"]
        H12["H1.2: Write Non-Inferiority<br/>(Rate Diff >= -delta_NI)"]
        H13["H1.3: Benefit Capture<br/>(CF_w >= 0.70)"]
        H41["H4.1: Censored Superiority<br/>(Exact Binomial p < 0.025)"]
        H42["H4.2: RMST Non-Inferiority<br/>(Delta RMST < 1.5s)"]

        H11 -->|1.0| H12
        H12 -->|1.0| H13
        H13 -->|1.0| H41
        H41 -->|1.0| H42
    end

    subgraph Fam2["Family 2: Causal Synchronization Mechanics (alpha_2 = 0.025)"]
        H21["H2.1: Renewal Drain Slope<br/>(TOST beta in [1.0 +- 0.20])"]
        H22["H2.2: Reader Hold Causality<br/>(beta_A > 0.20)"]
        H23["H2.3: CPU-Load Separation<br/>(TOST Delta t in [-5, +5] us)"]

        H21 -->|1.0| H22
        H22 -->|1.0| H23
    end

    H42 -.->|Transfer alpha_1 (0.025)| H21
    H23 -.->|Transfer alpha_2 (0.025)| H11
```

**Procedural Rules:**
1. **Initial Split:** $\alpha_1 = 0.025$ to Family 1; $\alpha_2 = 0.025$ to Family 2.
2. **Within-Family Progression:** Rejection of a node transfers 100% of its local alpha to the downstream node.
3. **Cross-Family Symmetric Transfers:**
   - Full rejection of Family 1 transfers $\alpha_1 = 0.025$ to $H_{2.1}$, allowing causal hypotheses to be re-evaluated at full $\alpha = 0.050$.
   - Symmetrically, full rejection of Family 2 transfers $\alpha_2 = 0.025$ to $H_{1.1}$.
4. **Decoupling on Gate Failure:** If Family 1 fails at $H_{1.1}$ or $H_{1.2}$, Family 2 retains its full $\alpha_2 = 0.025$ confirmatory significance.
5. **Conditional Eligibility Handling for $H_{1.3}$ (Capture Fraction):**
   - Benefit Gate: Approach B must demonstrate significant write advantage over A ($\bar{W}_B - \bar{W}_A > 5.0\,\text{w/s}$, $p < 0.025$).
   - If the gate fails: $CF_w$ is classified as **Conditionally Ineligible (Null Concurrency Advantage Regime)**.
   - Per Bretz et al. (2009) node-removal algorithm, $H_{1.3}$ is bypassed and its allocated alpha transfers directly to $H_{4.1}$ with weight $1.0$:
     $$g_{1.2 \to 4.1} = g_{1.2 \to 1.3} \times g_{1.3 \to 4.1} = 1.0 \times 1.0 = 1.0$$
     No alpha is lost, and the testing sequence continues to EXP-4 without distortion.

### 4.2 Milestone-Anchored Drift Controls & Run-List Generation
Anchors execute Cell C2-01 ($T = 0\,\mu\text{s}, r = 4$) as 4 matched triplets ($4 \times 3 = 12$ runs) positioned at logical campaign milestones:
- **Triplet D3-01 (Opening Baseline):** Trials 1, 2, 3 (Campaign initiation).
- **Triplet D3-02 (Post-EXP-1 Milestone):** Spliced immediately following completion of all EXP-1 trials (pilot + adapted confirmatory).
- **Triplet D3-03 (EXP-2 Midpoint Milestone):** Spliced immediately following completion of delay sweep cells C2-01 through C2-03.
- **Triplet D3-04 (Closing Baseline):** Spliced as the final 3 trials of the campaign, immediately following EXP-4 completion.

**Run-List Generation Algorithm (`run_i7.ps1`):**
1. Triplet keys are grouped by experimental block.
2. Latin-square arm ordering (`A, AMIN, B`, etc.) is permuted within triplets using PRNG seeded by master seed.
3. The runlist generator dynamically inserts D3 milestone triplets at stage boundaries and assigns consecutive `runlist_position` indices after resolving adapted triplet counts.

---

## 5. Master Execution Inventory & Budget Ledger (Max 388 Runs $\le$ 400 Ceiling)

| Stage | Experiment ID | Research Question | Cells | Triplet Sizing ($N$) | Triplet Runs | Drift Anchors | Subtotal Runs | Replacement Budget | Worst-Case Total Runs | 400 Ceiling Status |
| :---: | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **Stage 1** | **EXP-1** | R1: Writer Progress | 4 | $N \in [8, 10]$ | 96–120 | 6 (D3-01, D3-02) | 102–126 | — | — | — |
| **Stage 2** | **EXP-2** | R2: Causal Synchronization | 6 | $N \in [8, 10]$ | 144–180 | 3 (D3-03) | 147–183 | — | — | — |
| **Tier 3** | **EXP-3** | R3: Cardinality Microbenchmarks | — | — | 0 | 0 | 0 | 0 | 0 | **0 Disk Runs** |
| **Stage 4** | **EXP-4** | R4: Fixed-Workload Survival | 2 | $N \in [8, 10]$ | 48–60 | 3 (D3-04) | 51–63 | — | — | — |
| **Total** | **All Stages** | **Unified Prospective Campaign** | **12** | **$N \in [8, 10]$** | **288–360** | **12** | **300–372** | **16** | **316–388** | **$\le 400$ Strictly Compliant** |

- **Minimum possible execution:** $288 + 12 + 16 = \mathbf{316\,\text{runs}}$.
- **Worst-case maximum execution:** $360 + 12 + 16 = \mathbf{388\,\text{runs}} \le \mathbf{400\,\text{Ceiling}}$.
- **Guaranteed buffer:** At least 12 unallocated runs remain under the most extreme adaptive expansion.

---

## 6. Preflight Action Items (Bound to Preflight Phase)

The following engineering modifications are formally bound to the **Engineering Preflight Phase**:
1. **`cmd/i7trial/main.go`:** Add `"D3"` to `supportedBlocks` slice (line 42) and enable D3 execution.
2. **`lsmengine/manager.go`:** Add `activeRLockHolders atomic.Int32` and wrap `m.Mu.RLock()` / `m.Mu.RUnlock()`.
3. **`lsmengine/flush_observer.go` & `harness2/events.go`:** Update `OnFlushPublishBegin(holders int)` to pass sampled count.
4. **`harness2/spec.go`:** Add `ActiveReadersAtArrival int` to `EventRecord`.
5. **`run_i7.ps1`:** Implement milestone-based dynamic anchor splicing and Latin-square triplet generator.
6. **`lsmengine/metadata_bench_test.go`:** Author Go `testing.B` suite for EXP-3 in-memory profiling.

---

## 7. Delivery Manifest & SHA-256 Checksums

| File | Path | SHA-256 Checksum | Description |
| :--- | :--- | :--- | :--- |
| **Statistical Script** | [`simulate_power_r1_2e.py`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/simulate_power_r1_2e.py) | `DBCD469E9B6860E573F039716FAF2EDC92329B86D5D2351379534C49A4D387E3` | Python simulation script implementing factor 884.14, df=N-2, N in [8, 10], and restored C2-06 control. |
| **Output JSON** | [`r1_2e_power_simulation_results.json`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/r1_2e_power_simulation_results.json) | `4DCE74545F427752232FD1360BD2EB3E1F8F212D78B2058F7646AFC1902E37D1` | Raw reproducible JSON output containing all sensitivity curves and causal models. |
| **Audit Report** | `THIS REPORT` | — | Verification audit and blocker resolution report. |

---

**STATUS: ALL BLOCKERS RESOLVED. EXECUTION STRICTLY HALTED FOR EXTERNAL REVIEW.**  
The next authorized gate is **Engineering Implementation and Synthetic/Unit Validation**, not another design iteration.
