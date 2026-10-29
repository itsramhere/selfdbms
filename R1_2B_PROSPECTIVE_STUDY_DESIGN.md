# Phase I7 — Checkpoint R1.2b: Prospective Research Study Design

**DATE:** 2026-10-29  
**DESIGN STATUS:** INDEPENDENT PROSPECTIVE EXPERIMENTAL STUDY (DESIGN-ONLY)  
**FRAMEWORK:** STRICT SCIENTIFIC RIGOR, FALSIFICATION-FIRST ARCHITECTURE  
**EXECUTION STATUS:** ZERO BENCHMARKS RUN. DESIGN SPECIFICATIONS ONLY.

---

## 1. Study Overview & Architectural Framing

This document details the prospective experimental design for the independent study on LSM-tree synchronization architectures:
- **Approach A (Coarse Manager Lock):** Readers hold an exclusive/shared manager lock (`sync.RWMutex`) across SSTable and memtable index scans.
- **Approach A-min (Descriptor Leasing):** Readers acquire a short-lived atomic lease on active table descriptors under a microsecond mutex, releasing the manager lock before reading data blocks.
- **Approach B (Immutable Snapshots):** Readers atomically load an immutable `SuperVersion` pointer, relying on background epoch/refcount tracking.

Rather than assuming universal $\pm 5\%$ equivalence, the prospective study explicitly investigates **causal mechanisms, non-inferiority boundaries, and algorithmic complexity crossovers**.

---

## 2. Experiment 1 (EXP-1): Writer-Progress Capture Across Concurrency

### A. Scientific Objective & Hypotheses
* **Central Question (R1):** Can Approach A-min reliably capture the writer-progress improvement demonstrated by Approach B over Approach A across varying reader concurrency levels?
* **Primary Hypothesis ($H_1$):** Shortening the manager lock to descriptor leasing allows writers to commit transactions at rates matching immutable snapshot isolation, achieving a capture fraction $CF_w \ge 0.70$ and non-inferiority to B at the 10% margin ($R_w(\text{AMIN}/\text{B}) > 0.90$) across all reader thread counts $r \ge 4$.
* **Null Hypothesis ($H_0$):** Approach A-min's manager lock contention during lease acquisition creates writer starvation under high reader concurrency ($r \ge 16$), causing $CF_w < 0.50$ and $R_w(\text{AMIN}/\text{B}) \le 0.80$.
* **Competing Explanation:** Writer progress differences are driven by background disk flush contention on NVMe write queues, not in-process lock synchronization.

### B. Experimental Variables
* **Manipulated Independent Variable:** Reader concurrency level $r \in \{1, 4, 8, 16, 32\}$ reader goroutines.
* **Evaluated Synchronizers:** Approach A, Approach A-min, Approach B (paired triplets sharing seeds).
* **Controlled Variables (Held Constant):**
  - Database fixture: 4 SSTables ($4\times 2,500$ keys, 128-byte values, total 10,000 keys).
  - Writer workload: 1 dedicated free-running writer thread generating unique sequential keys.
  - Read workload: Zipfian key distribution ($\theta = 0.95$), 95% hit ratio target.
  - Measurement window: Exactly 10.0 seconds.
  - Execution environment: Single core-pinned worker pool on native NTFS.

### C. Observables & Primary Estimands
1. **Completed Writes Ratio:** $R_w(\text{AMIN}/\text{A}) = W_{\text{amin}} / W_A$ and $R_w(\text{AMIN}/\text{B}) = W_{\text{amin}} / W_B$.
2. **Write Capture Fraction:**
   $$CF_w = \frac{W_{\text{amin}} - W_A}{W_B - W_A}$$
3. **Writer Lock-Wait Duration:** Cumulative nanoseconds spent by the writer blocked in `man.Mu.Lock()` awaiting reader drain.
4. **Read Throughput:** Completed reads per second ($T_r$).

### D. Manipulation Checks & Correctness Safeguards
* **MC-1.1 (Concurrency Verification):** Active reader goroutine count confirmed at runtime via hardware thread counters.
* **MC-1.2 (Data Integrity):** Post-run fixture validation verifies zero corrupted records (`validation_error_count = 0`).
* **MC-1.3 (Disk State Verification):** Memtable payload and WAL write byte count matches completed write count within $\pm 1\%$.

### E. Falsification Criteria
* If $CF_w$ upper bound of 95% BCa CI $< 0.50$ at $r=16$ or $r=32$, $H_1$ is falsified (leasing fails to capture snapshot progress).
* If writer lock-wait duration in Arm A does not differ significantly from Arm A-min ($p > 0.05$), the lock-contention explanation is falsified.

---

## 3. Experiment 2 (EXP-2): Causal Isolation of the Read Critical Section

### A. Scientific Objective & Hypotheses
* **Central Question (R2):** Is Approach A's writer convoying causally driven by the duration of holding `RLock` during physical SSTable reads, and does A-min eliminate this sensitivity?
* **Primary Hypothesis ($H_1$):** In Approach A, writer publication delay ($\Delta t_{\text{wait}}$) scales linearly with reader critical-section duration:
  $$\Delta t_{\text{wait}}(\text{Arm A}) \propto r \times T_{\text{readCS}}$$
  In Approach A-min and Approach B, writer publication delay is completely invariant to $T_{\text{readCS}}$ ($\text{slope} \approx 0$).
* **Null Hypothesis ($H_0$):** Writer publication delay is independent of $T_{\text{readCS}}$ and is dominated by background file flush and fsync latency on NTFS.

### B. Controlled Experimental Intervention
To avoid the keyspace caching flaw of Block E5, $T_{\text{readCS}}$ is manipulated through two complementary, rigorously controlled mechanisms:
1. **Method A (Calibrated Synthetic Scan Delay):** Readers holding their synchronization lease/lock execute a calibrated, cache-line-independent CPU spin inside the read critical section for $T_{\text{delay}} \in \{0, 25\mu\text{s}, 100\mu\text{s}, 500\mu\text{s}, 2000\mu\text{s}\}$.
2. **Method B (Physical Multi-Block Traversal):** Readers query keys strictly distributed across intermediate SSTable data blocks ($0\dots 9,999$), forcing 1, 2, 4, or 8 block index decodes from disk/cache with non-zero payload validation.

### C. Observables & Estimands
1. **Writer Drain Latency ($\Delta t_{\text{drain}}$):** Time from writer `man.Mu.Lock()` invocation to lock grant.
2. **Flush Interval Jitter:** Variance of publication completion timestamps relative to 500 ms periodic schedule.
3. **Regression Slope ($\beta_1$):** Fitted slope of $\Delta t_{\text{drain}}$ on $T_{\text{readCS}}$.

### D. Manipulation & Isolation Checks
* **MC-2.1 (Intervention Duration Verification):** Instrumentation confirms actual read critical-section hold time matches target $T_{\text{readCS}}$ within $\pm 5\%$.
* **MC-2.2 (Decoupled Write Load):** Background writes are disabled during physical flush tests to prevent memtable allocation interference.

### E. Falsification Criteria
* If $\beta_1(\text{Arm A})$ is not statistically greater than zero ($\alpha = 0.01$), the lock-holding causal hypothesis is falsified.
* If $\beta_1(\text{Arm A-min})$ is statistically greater than zero ($\alpha = 0.01$), the claim that leasing decouples readers from writers is falsified.

---

## 4. Experiment 3 (EXP-3): Complexity Crossover Between Leasing and Snapshots

### A. Scientific Objective & Hypotheses
* **Central Question (R3):** Under what SSTable cardinality ($k$) and reader session lifetime does Approach A-min's atomic descriptor leasing outperform Approach B's immutable snapshots, and where is the crossover boundary?
* **Theoretical Model:**
  - **Approach A-min Cost:** $C_{\text{AMIN}} = C_{\text{mutex}} + \Theta(1)\text{ atomic refcount} + \Theta(k)\text{ descriptor slice copy}$.
  - **Approach B Cost:** $C_{\text{B}} = C_{\text{atomic load}} + \Theta(1)\text{ snapshot refcount} + C_{\text{cleanup}}$.
  - **Prediction:** Under high reader registration churn (short-lived sessions) and small $k$, A-min outperforms B because B's snapshot creation/retirement overhead dominates. Under large $k$ ($k \ge 64$) and long-lived sessions, B outperforms A-min because B eliminates the $\Theta(k)$ descriptor copy.
* **Primary Hypothesis ($H_1$):** A measurable performance crossover surface exists in the $(k, \text{churn})$ parameter space.

### B. Experimental Factorial Grid ($4 \times 3 \times 2$)
1. **SSTable Cardinality ($k$):** $k \in \{4, 16, 64, 256\}$ active on-disk SSTable files.
2. **Reader Session Churn Mode:**
   - *Ephemeral Mode:* Readers acquire a new lease/snapshot per point read (maximum churn).
   - *Batched Mode:* Readers hold a lease/snapshot for a batch of 50 reads.
   - *Persistent Mode:* Readers hold a single continuous lease/snapshot across the 10.0s trial.
3. **Reader Concurrency ($r$):** $r \in \{8, 32\}$ concurrent reader goroutines.

### C. Observables & Estimands
1. **Per-Read Service Latency:** Median and $p_{99}$ service time per query.
2. **Memory Allocation Rate:** Bytes allocated per second and GC pause duration (`runtime.ReadMemStats`).
3. **Throughput Ratio:** $R_r(\text{AMIN}/\text{B}) = T_{\text{amin}} / T_B$.
4. **Crossover Point ($k^*$):** The cardinality $k$ at which $R_r(\text{AMIN}/\text{B}) = 1.000$.

### D. Manipulation Checks
* **MC-3.1 (Fixture Cardinality):** Directory audit confirms exact active SSTable count matches $k$.
* **MC-3.2 (Snapshot Pinning Audit):** Diagnostic counters verify `active_snapshot_pins` and `max_outstanding_leases` match theoretical session expectations.

### E. Falsification Criteria
* If $R_r(\text{AMIN}/\text{B})$ is invariant to $k$ and churn across all cells, the theoretical complexity model is falsified.

---

## 5. Experiment 4 (EXP-4): Fixed-Work Batching (Resolving Workload D)

### A. Scientific Objective & Hypotheses
* **Central Question (R4):** How do synchronization architectures compare when evaluated under **true matched realized write work**, rather than an offered arrival rate that causes write starvation?
* **Methodological Dilemma:** Paced time-window execution (Block E3) failed because Approach A starved, reducing write volume by 45%–78% and creating an artificial read throughput boost.
* **Proposed Solution (Fixed-Work Contract):** Instead of fixing the measurement time to 10.0 seconds, fix the **work volume**:
  - The writer thread is tasked with committing exactly **$M = 2,500$ write transactions**.
  - Measurement begins at write 1 and ends when write 2,500 commits.
  - Concurrent readers execute continuous closed-loop queries during this interval.
  - **Guarantee:** All three arms commit identical write volume, trigger identical memtable flushes, and construct identical LSM storage topologies.

### B. Observables & Estimands
1. **Writer Completion Time ($T_{\text{complete}}$):** Wall-clock duration required to commit the 2,500 transactions.
2. **Realized Write Throughput:** $W_{\text{actual}} = 2,500 / T_{\text{complete}}$ writes/s.
3. **Concurrent Read Volume & Throughput:** Total reads completed during the writer batch.
4. **Read Response Latency Distribution:** $p_{50}, p_{95}, p_{99}$ latency of concurrent reads.

### C. Manipulation Checks
* **MC-4.1 (Exact Work Invariance):** Committed writes across arms must be identically $2,500$ ($\Delta W = 0$).
* **MC-4.2 (Equal Flush Count):** Number of completed background flushes must be identical across arms within $\pm 1$.

### D. Falsification Criteria
* If Approach A's $T_{\text{complete}}$ matches Approach A-min ($T_A / T_{\text{AMIN}} \le 1.10$), the claim that Approach A suffers writer convoying under concurrent reads is falsified.
* If Approach A-min's concurrent read throughput is $> 10\%$ worse than Approach B, non-inferiority under equal work is falsified.

---

## 6. Comprehensive Decision Matrix Relating Experiments to Central Question

| Experiment | Core Research Question | Tested Synchronizers | Key Manipulations | Primary Falsification Criterion | What a Positive Result Proves | What a Negative Result Proves |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **EXP-1 (Progress)** | **R1:** Writer-progress capture | A, A-min, B | $r \in \{1, 4, 8, 16, 32\}$ | $CF_w < 0.50$ or $R_w(\text{AMIN}/\text{B}) \le 0.80$ | Versioning is unnecessary to eliminate write starvation across concurrency. | Descriptor leasing reintroduces write starvation under high thread counts ($r \ge 16$). |
| **EXP-2 (Causal)** | **R2:** Read critical section mechanism | A, A-min, B | $T_{\text{readCS}} \in [0, 2\text{ms}]$ | $\Delta t_{\text{wait}}(\text{Arm A})$ slope not $> 0$ | Convoying is causally driven by scan hold time, which leasing completely decouples. | Writer delay is driven by disk/OS scheduling, not reader lock hold time. |
| **EXP-3 (Crossover)** | **R3:** Algorithmic complexity crossover | A-min, B | $k \in [4, 256]$, churn modes | No crossover surface exists | Formalizes the operational boundary: when to lease vs when to snapshot. | One architecture strictly dominates across all realistic operating envelopes. |
| **EXP-4 (Work-Match)** | **R4:** True equal-work comparability | A, A-min, B | Fixed $M=2,500$ writes | $T_{\text{complete}}(\text{AMIN}) > 1.20 \times T_B$ | A-min maintains write progress and read parity under strict work invariance. | A-min's performance was an artifact of offered load pacing. |
