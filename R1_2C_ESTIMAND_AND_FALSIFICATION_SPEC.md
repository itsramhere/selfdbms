# Phase I7 — Checkpoint R1.2c: Estimand and Falsification Specification

**DOCUMENT ID:** `R1_2C_ESTIMAND_AND_FALSIFICATION_SPEC.md`  
**STATUS:** FORMAL PRE-REGISTERED PROTOCOL SPECIFICATION  
**GOVERNING DIRECTIVE:** CHECKPOINT R1.2c EXTERNAL REVIEW DECISION  
**EVALUATED GIT REVISION:** `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`  
**BOUNDARY:** DESIGN AND SPECIFICATION ONLY (ZERO BENCHMARK EXECUTION)

---

## 1. Executive Summary & Epistemic Framework

The primary research question of this investigation is:
> **How much of the concurrency benefit attributed to immutable reader snapshots can be obtained by shortening the manager-lock critical section using simpler reader-lifetime management (Approach A-min) compared to full snapshot isolation (Approach B) and coarse-grained locking (Approach A)?**

Scientific integrity requires that every empirical test be bounded by pre-registered, mathematically well-conditioned estimands and quantitative falsification thresholds. In particular:
1. **Absence of Evidence is Not Evidence of Absence:** A non-significant statistical test ($p > 0.05$) shall never be interpreted as proof that two architectures are equivalent or that an effect is absent. Equivalence claims must be evaluated via Two One-Sided Tests (TOST) against explicit Minimum Substantive Effect Sizes (MES).
2. **First-Class Negative & Neutral Outcomes:** An experiment that demonstrates no architectural crossover, or that refutes the theoretical linear wait model, or that finds Approach A-min inferior to Approach B under specific workloads, is a successful, highly informative scientific result.
3. **Robustness to Boundary Collapses:** Estimands must remain mathematically defined and computationally stable even under extreme physical phenomena, such as complete writer starvation ($W_A = 0$), publication deadline overruns, and run-time right-censoring.

---

## 2. EXP-1: Writer Progress Under Concurrent Read Pressure

### 2.1 The Problem with Ratio Estimands
Historical analyses attempted to quantify writer progress using the simple ratio $W_{\text{amin}} / W_A$. When Approach A experiences reader-induced write starvation, completed in-window writes $W_A$ collapse to zero ($W_A = 0$). Under these conditions:
- The ratio $W_{\text{amin}} / W_A$ is undefined ($\frac{k}{0} \to \infty$).
- Trial-level capture fractions $CF_{w, t} = \frac{W_{\text{amin}, t} - W_{A, t}}{W_{B, t} - W_{A, t}}$ produce ill-conditioned, heavy-tailed (Cauchy-like) distributions where single-trial fluctuations dominate sample statistics.
- Treatment-induced timeouts distort mean rate calculations if late or unissued writes are silently dropped.

### 2.2 Five-Category Write Accounting Ledger
To guarantee physical accounting conservation, every trial in EXP-1 shall record transactions into five mutually exclusive and exhaustive categories:
$$\text{Total Offered Writes } (W_{\text{offered}}) = W_{\text{comp}} + W_{\text{late}} + W_{\text{fail}} + W_{\text{queue}}$$
1. **$W_{\text{comp}}$ (Completed In-Window):** Transactions that fully committed and acquired a finalized sequence number strictly before the trial measurement window elapsed ($t \le T = 10.0\,\text{s}$).
2. **$W_{\text{late}}$ (Completed Post-Window):** Transactions initiated before $T$ but whose commit finalized after $T$.
3. **$W_{\text{fail}}$ (Failed / Aborted Operations):** Transactions that were rejected by the engine (e.g., write-write conflict, engine closed, I/O error).
4. **$W_{\text{queue}}$ (Queued / Unissued):** Transactions scheduled by the paced workload generator that could not be issued before trial termination due to client-side pipeline backpressure.
5. **$W_{\text{att}}$ (Total Attempted):** $W_{\text{att}} = W_{\text{comp}} + W_{\text{late}} + W_{\text{fail}}$.

### 2.3 Diagnostic Error Classification
The single recorded error in historical E1 trial 09 (Arm A-min: 2,674 attempted, 2,671 completed in-window, 2 late, 1 error) cannot be attributed to a write-write conflict without diagnostic logs. Single-writer transactions in `lsmengine` monotonically increment `CurrentSeqNo` under `m.Mu.Lock()` ([`lsmengine/transaction.go#L88`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L88)), guaranteeing that concurrent write-write conflicts cannot occur with a single writer. 

EXP-1 shall enforce structured error logging with four mutually exclusive diagnostic codes:
- `ERR_TX_CONFLICT`: Transaction validation conflict (impossible in single-writer; logged if multi-writer).
- `ERR_ENGINE_CLOSED`: Premature engine shutdown or resource reclamation race.
- `ERR_WAL_IO`: Operating system write failure on write-ahead log flush.
- `ERR_CLIENT_TIMEOUT`: Client-side deadline expiration before commit return.

### 2.4 Primary Estimand 1: Absolute Write Rate Difference (Superiority of A-min over A)
To measure whether Approach A-min eliminates writer starvation, we evaluate the paired absolute write rate difference:
$$\Delta \dot{W}(\text{AMIN} - \text{A}) = \frac{W_{\text{AMIN, comp}} - W_{\text{A, comp}}}{T} \quad [\text{writes/second}]$$
- **Hypotheses:**
  $$H_0: \Delta \dot{W} \le 5.0\,\text{writes/s} \quad \text{vs.} \quad H_1: \Delta \dot{W} > 5.0\,\text{writes/s}$$
  where $5.0\,\text{writes/s}$ represents the pre-registered Minimum Substantive Effect Size (MES) for starvation mitigation.
- **Statistical Test:** One-sided paired Student-$t$ test (or Wilcoxon signed-rank test if normality is rejected by Shapiro-Wilk at $\alpha=0.01$) with family-wise significance level $\alpha = 0.01$.
- **Falsification Threshold:** If the lower bound of the 99% confidence interval for $\Delta \dot{W}$ is $\le 5.0\,\text{writes/s}$, the claim that A-min mitigates reader-induced writer starvation is **formally falsified**.

### 2.5 Primary Estimand 2: Regularized Log-Ratio of Completed Writes (Non-Inferiority of A-min to B)
To assess whether Approach A-min preserves write throughput comparable to full snapshot isolation (Approach B) without singularity at zero, we evaluate the regularized paired log-ratio:
$$d_{w, t} = \ln\left(\frac{W_{\text{AMIN, comp}, t} + 1}{W_{\text{B, comp}, t} + 1}\right)$$
- **Non-Inferiority Margin:** We pre-register $\Delta_{\text{NI}} = 0.80$ (retaining $\ge 80\%$ of Approach B's completed writes), corresponding to a log-margin of $\delta_{\text{NI}} = \ln(0.80) = -0.2231$. (A secondary exploratory benchmark is evaluated at $\Delta_{\text{NI}} = 0.90$, $\delta_{\text{NI}} = -0.1054$).
- **Hypotheses:**
  $$H_0: \mu_d \le \delta_{\text{NI}} \quad \text{vs.} \quad H_1: \mu_d > \delta_{\text{NI}}$$
- **Statistical Test:** One-sided paired Student-$t$ test with significance level $\alpha = 0.04$.
- **Falsification Threshold:** If the lower bound of the one-sided 95% confidence interval $\left[\bar{d}_w - t_{0.95, N-1} \frac{s_d}{\sqrt{N}}, \infty\right)$ falls below $\ln(0.80)$, the claim that A-min achieves writer progress non-inferior to Approach B is **formally falsified**.

### 2.6 Cohort-Level Capture Fraction
Rather than averaging volatile per-trial ratios, the write progress capture fraction shall be computed as a single cohort-level ratio of mean completed writes:
$$\widehat{CF}_w = \frac{\bar{W}_{\text{AMIN, comp}} - \bar{W}_{\text{A, comp}}}{\bar{W}_{\text{B, comp}} - \bar{W}_{\text{A, comp}}}$$
- **Uncertainty Quantification:** 95% confidence intervals are computed using 10,000 bias-corrected and accelerated (BCa) bootstrap resamples over paired trial triplets.
- **Falsification Threshold:** If $\widehat{CF}_w < 0.70$, or if the upper bound of the 95% bootstrap confidence interval falls below $0.70$, the hypothesis that A-min captures the vast majority of Approach B's concurrency benefit is **formally falsified**.

---

## 3. EXP-2: Causal Mechanism & Synchronization Physics

### 3.1 Refutation of Linear Wait Scaling ($r \times T_{\text{readCS}}$)
Historical hypotheses asserted that writer wait time scales proportionally to reader count multiplied by reader critical-section duration:
$$\Delta t_{\text{wait}} \propto r \times T_{\text{readCS}}$$
This assumption is physically invalid for shared-exclusive synchronization (`sync.RWMutex`) on multi-core processors:
1. **Parallel Overlap:** Under multi-core execution, concurrent readers execute overlapping critical sections simultaneously, not serially.
2. **Reader Blocking Mechanism:** In Go's `sync.RWMutex`, an incoming writer calling `Lock()` sets a bit that immediately bars *new* readers from acquiring `RLock()`.
3. **Residual Lifetime Expectation:** The writer waits only for readers *already inside* their critical section to exit. For $r$ concurrent readers of uniform duration $T_{\text{readCS}}$, renewal theory dictates that the expected time until the last reader drains is:
   $$\mathbb{E}[\Delta t_{\text{wait}}] = \frac{r}{r + 1} T_{\text{readCS}}$$
   For $r \in [4, 16]$, the theoretical slope $\beta_1 = \frac{r}{r+1}$ scales asymptotically between $0.80$ and $0.94$, never multiplying by $r$.
4. **OS Preemption Quantum Pedestal:** When active reader threads exceed available physical CPU cores, thread preemption introduces a discontinuous pedestal ($\tau \approx 15.6\,\text{ms}$ on default Windows scheduling).

### 3.2 Six-Interval Latency Decomposition
Instrumentation shall explicitly capture six mutually exclusive micro-intervals for each flush/publication:
$$\Delta t_{\text{pub\_total}} = t_{\text{reader\_drain}} + t_{\text{exclusive\_hold}} + t_{\text{pub\_io}} + t_{\text{storage}} + t_{\text{os\_sched}}$$
1. $t_{\text{reader\_drain}}$ ($t_{\text{writer\_acq\_wait}}$): Duration from `Lock()` call until all active readers exit.
2. $t_{\text{exclusive\_hold}}$: Duration exclusive manager lock is held while updating metadata pointers.
3. $t_{\text{pub\_io}}$: Time spent appending manifest records and flushing WAL to disk.
4. $t_{\text{reader\_hold}}$: Individual reader critical-section duration (time between `RLock()`/`AcquireLease()` and `RUnlock()`/`Release()`).
5. $t_{\text{storage}}$: Underlying disk read time during SSTable index/block decoding.
6. $t_{\text{os\_sched}}$: Thread descheduling/wakeup latency measured via high-resolution QPC timestamps.

### 3.3 Controlled Interventions (EXP-2A vs. EXP-2B)
- **EXP-2A (Synthetic CPU Spin Intervention):** Injects controlled delays $T_{\text{readCS}} \in \{0, 50, 200, 500\,\mu\text{s}\}$ into reader critical sections via calibrated CPU spin loops (zero disk I/O) across reader pools $r \in \{2, 4, 8, 16\}$.
- **EXP-2B (Realistic Multi-Block Storage Validation):** Evaluates realistic workloads where reader critical sections traverse multi-block SSTables with cold vs. warm operating system page cache states.

### 3.4 Falsification Criteria for Causal Link
- **Falsification of Linear Scaling:** We test whether the empirical slope $\hat{\beta}_1$ of writer wait vs. $T_{\text{readCS}}$ deviates from the renewal prediction $\frac{r}{r+1}$. If Two One-Sided Tests (TOST) against an equivalence margin of $\text{MES} = \pm 0.20$ fail to establish equivalence, the linear scaling hypothesis is **formally rejected**.
- **Falsification of Coarse-Lock Causal Attribution:** If Approach A's writer wait time does *not* exhibit a statistically significant positive slope ($\beta_A \le 0.10$ or $p > 0.05$), the claim that reader hold time is the root cause of flush starvation in Approach A is **formally falsified**.

---

## 4. EXP-3: Complexity Crossover & Engine Semantics

### 4.1 Engine Concurrency Architecture
Contrary to early theoretical assumptions, Approach B in `lsmengine` does not execute lock-free atomic pointer swaps. Code audit of [`lsmengine/manager.go#L115`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L115) and [`lsmengine/snapshot.go#L302, L345`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/snapshot.go#L302) reveals:
- Approach B protects the global active snapshot linked list with an un-striped exclusive mutex (`snapshotMu sync.Mutex`), acquired twice per point lookup (snapshot registration and snapshot release).
- Approach A-min uses a shared read-lock (`m.Mu.RLock()`) and lock-free atomic integer increments (`atomic.AddInt64(&m.activeLeases, 1)`).
- Consequently, under high concurrent reader loads, Approach B serializes on `snapshotMu`, explaining why Approach A-min matches or outperforms Approach B.

### 4.2 Preservation of Version Semantics
To prevent semantic drift, all comparisons between A-min and B must preserve identical point-in-time snapshot isolation:
- No speculative "persistent reader session" or "batched read" APIs shall be introduced, as they violate engine snapshot boundaries and cause descriptor leaks.
- Both A-min and B must evaluate single-operation point lookups with identical key generation seeds and version boundaries.

### 4.3 Two-Part Testing Protocol
1. **Part A: In-Memory Metadata Microbenchmark Suite (0 Disk Runs):**
   - Implemented via Go's native testing framework (`testing.B`).
   - Measures pure metadata acquisition and release overhead in nanoseconds:
     - `BenchmarkLeaseAcquireRelease_Amin` (atomic lease counter + shared `RLock`).
     - `BenchmarkSnapshotCreateClose_ApproachB` (`snapshotMu` acquisition + linked list insertion/deletion).
   - Metrics: Nanoseconds per op, heap allocations per op (`B/op`), allocation count (`allocs/op`).
2. **Part B: End-to-End Engine Lookup Under Continuous Flush Churn:**
   - Evaluates point read throughput ($\text{ops/s}$) and latency under background table publication churn ($f \in \{0, 5, 20, 50\}\,\text{flushes/s}$).
   - Observable state metrics: Active lease count, snapshot linked list depth, retained descriptor count, and epoch reclamation latency.

### 4.4 Falsification and Acceptance of Non-Crossover
- **Falsification of Crossover Hypothesis:** If Approach A-min maintains higher or equal throughput ($R_{\text{amin}} \ge R_B$) across all reader counts $r \in [1, 32]$ and table churn rates, the hypothesis that "full snapshot isolation crosses over to beat leasing as read-to-write ratio increases" is **formally falsified**.
- **Scientific Value:** Demonstrating that reader leasing strictly dominates snapshot isolation due to exclusive mutex serialization on the snapshot registry is an informative, publishable architectural finding.

---

## 5. EXP-4: Fixed-Write-Volume Comparability

### 5.1 Protocol Definition
EXP-4 evaluates time-to-completion under a fixed write workload ($M = 2,500$ sequential transactions) subjected to continuous open-loop concurrent read pressure ($\lambda = 1,000\,\text{reads/s}$ via QPC-paced Poisson arrivals).
- **Logical Equivalence:** All arms execute the identical pseudo-random write key-value sequence seeded deterministically.

### 5.2 Time-to-Completion & Right-Censoring
- **Measurement Metric:** Elapsed duration to commit all $M$ transactions ($T_{\text{comp}}$ in seconds).
- **Right-Censoring Threshold:** $T_{\text{max}} = 30.0\,\text{seconds}$.
- **Retry Budget:** Client transactions that encounter transient engine errors are permitted up to $K_{\text{retry}} = 3$ immediate retries before logging an operation failure.
- **Handling Starved Arms:** If Approach A cannot complete $M$ writes before $T_{\text{max}}$, the trial is classified as **right-censored** at $T_{\text{comp}} = 30.0\,\text{s}$ ($C_i = 1$). It is NOT discarded as a mechanical failure.

### 5.3 Storage Topology Verification
Matching completed write counts ($M = 2,500$) does not guarantee identical physical disk structures, because an arm that takes $3\times$ longer allows background compaction threads additional time to merge SSTable levels. EXP-4 shall record:
- Total memtable flushes completed (`flushes_completed`).
- Total compactions executed (`compactions_completed`).
- Active SSTable count across all levels (`active_sstables`).
- Total SSTable disk bytes (`total_disk_bytes`).

### 5.4 Primary Estimands & Falsification Criteria
1. **Survival Time Superiority over Approach A:**
   - Evaluated via Log-Rank test and Wilcoxon rank-sum scores treating $T_{\text{max}}$ as right-censored.
   - Falsification Threshold: If survival curves show no statistically significant separation ($p \ge 0.01$), the hypothesis that A-min prevents write starvation under fixed-volume load is **formally falsified**.
2. **Equivalence / Non-Inferiority to Approach B:**
   - Estimand: Log-ratio of median completion times:
     $$d_{T} = \ln\left(\frac{T_{\text{comp, AMIN}}}{T_{\text{comp, B}}}\right)$$
   - Non-inferiority margin: $\Delta_{\text{NI}} = 1.15$ (A-min completion time within 15% of B), corresponding to $\delta_{\text{NI}} = \ln(1.15) = +0.1398$.
   - Falsification Threshold: If the upper bound of the 95% confidence interval for $d_T$ exceeds $+0.1398$, non-inferiority of A-min under fixed-workload conditions is **formally falsified**.

---

## 6. Comprehensive Falsification Ledger

| Experiment | Target Claim | Primary Estimand | Test Type | Pre-Registered Falsification Threshold | Action Upon Falsification |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **EXP-1** | A-min eliminates writer starvation | $\Delta \dot{W}(\text{AMIN} - \text{A})$ | One-sided paired $t$-test ($\alpha=0.01$) | Lower 99% CI bound $\le 5.0\,\text{writes/s}$ | Reject starvation elimination claim; report persistent lock contention |
| **EXP-1** | A-min matches B writer progress | $d_{w,t} = \ln\left(\frac{W_{\text{amin}}+1}{W_B+1}\right)$ | One-sided paired $t$-test ($\alpha=0.04$) | Lower 95% CI bound $< \ln(0.80) = -0.2231$ | Reject writer non-inferiority; report snapshot superiority |
| **EXP-1** | A-min captures concurrency benefit | Cohort $\widehat{CF}_w$ | 10k BCa Bootstrap ($\alpha=0.05$) | Point estimate $< 0.70$ or upper CI bound $< 0.70$ | Reject core architectural claim; conclude leasing is insufficient |
| **EXP-2A** | Writer wait scales as $\frac{r}{r+1} T_{\text{readCS}}$ | Empirical slope $\hat{\beta}_1$ | TOST vs. $\frac{r}{r+1}$ ($\text{MES} = 0.20$) | TOST $p > 0.05$ (fails equivalence bounds) | Reject theoretical queueing model; identify secondary lock bottlenecks |
| **EXP-2A** | Reader hold causes Approach A stall | Regression slope $\beta_{1, A}$ | One-sided $t$-test on slope | Estimated slope $\hat{\beta}_{1, A} \le 0.10$ or $p > 0.05$ | Reject reader-hold as cause of Approach A stall |
| **EXP-3** | Snapshot beats leasing at high load | Throughput ratio $R_{\text{amin}} / R_B$ | Grid evaluation over $r \in [1, 32]$ | No crossover observed ($R_{\text{amin}} \ge R_B$ everywhere) | Formally accept leasing dominance due to mutex serialization |
| **EXP-4** | A-min completes fixed work like B | $d_T = \ln(T_{\text{amin}} / T_B)$ | One-sided paired $t$-test ($\alpha=0.05$) | Upper 95% CI bound $> \ln(1.15) = +0.1398$ | Reject fixed-work non-inferiority; document cumulative latency drag |
