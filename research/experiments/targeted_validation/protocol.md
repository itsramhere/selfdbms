# Protocol: Phase I6.8 Targeted Hypothesis Verification and Causal Mechanism Analysis

- **Campaign ID**: `I6_TARGETED_VALIDATION_V1`
- **Protocol Version**: 1.0.0 (Preregistered)
- **Date**: 2026-10-27
- **Platform**: WSL2 Ubuntu 24.04.3 LTS, Linux Kernel 6.6.87.2-WSL2, Go 1.24.0 linux/amd64
- **Filesystem**: Native Linux ext4 on virtual disk (`/tmp/targeted_validation_v1`)
- **Primary Comparative Target**: Approach A (`ApproachA_RWMutexBaseline`) vs Approach B (`ApproachB_SnapshotReaders`)

---

## 1. Objectives and Scope

This experimental addendum is conducted pursuant to the External Reviewer Directive for Sub-Phase I6.8. Its purpose is confirmatory hypothesis testing and causal mechanism analysis to resolve specific questions identified in the exploratory 960-trial dataset:

1. **Experiment T1 (Tail Latency Causality)**: Test whether Approach B's lower read $p95$ tail latency under Workload B2 is causally attributable to SSTable flush and publication isolation, verified against a sham-flush control.
2. **Experiment T2 (Practical Equivalence)**: Apply formal Two One-Sided Tests (TOST) with a prespecified $\pm 5\%$ margin on Workloads A and E ($r \in \{1, 32\}$) to determine whether modes are statistically equivalent or inconclusive. Additionally, run a writer-free ($w=0$) diagnostic on Workload E.
3. **Experiment T3 (Rapid Churn Overhead Decomposition)**: Determine why rapid reader-set churn penalizes Approach B at low reader concurrencies ($r \in \{1, 4, 16\}$) by comparing `controlled_churn` against a `no_churn_control` and performing dedicated Go CPU/memory/mutex profiling.

The original 960-trial dataset (`campaign_wsl2_ext4_restart_v1`) remains frozen and preserved. Data from this targeted campaign will not be pooled with exploratory data.

---

## 2. Experimental Design and Block Scheduling

To eliminate order confounding, warm-up drift, and seed divergence present in the exploratory campaign:

1. **Balanced Paired Blocks**: Trials are executed in matched pairs $(A, B)$ sharing the exact same random input seed.
2. **Order Alternation**: Execution order within pairs is strictly balanced:
   - 50% of pairs run **Approach A first**, then **Approach B**.
   - 50% of pairs run **Approach B first**, then **Approach A**.
3. **Common Input Seeds**: Both modes within a pair receive identical pseudorandom operation streams generated deterministically from the pair's designated seed.
4. **Isolated Directories**: Every individual trial executes in a freshly created, clean ext4 directory (`/tmp/targeted_validation_v1/trial_<id>`), removed immediately upon trial completion and validation.
5. **Phase Timing**:
   - Measurement Duration: **5,000 ms** (5.0 s).
   - Warmup Duration: **1,000 ms** (1.0 s).
   - Cooldown Duration: **200 ms** (0.2 s).

---

## 3. Experiment Specifications

### 3.1 Experiment T1: Flush Tail Latency Verification (Workload B2)
- **Workload**: `WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE`
- **Initial Fixture**: 4 SSTables, 2,500 keys/SSTable (10,000 keys total), 95% target hit ratio.
- **Reader Concurrency**: $r \in \{2, 4, 8\}$ (3 levels). Primary confirmatory targets: $r=2, 4$; secondary endpoint: $r=8$.
- **Conditions**:
  - `flush_enabled`: Background flush coordinator commits a batch of 50 keys every 500 ms and invokes `man.TriggerFlush()` followed by `man.WaitForFlushCompletion()`.
  - `sham_flush`: Background coordinator commits the identical batch of 50 keys every 500 ms with WAL fsync, but omits `TriggerFlush()`.
- **Sample Size**: 20 independent paired blocks per configuration ($3 \text{ concurrencies} \times 2 \text{ conditions} \times 20 \text{ blocks} \times 2 \text{ modes} = 240 \text{ trials}$).

### 3.2 Experiment T2: Practical Equivalence via TOST (Workloads A & E)
- **Workload A**: `WORKLOAD_A_READ_ONLY` (4 SSTables, 10,000 keys, 0 writers).
- **Workload E**: `WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE` (0 initial SSTables, 10,000 keys, 2 WAL writers, 64MB Memtable threshold).
- **Reader Concurrency**: $r \in \{1, 32\}$ (2 levels).
- **Sample Size**: 20 independent paired blocks per configuration ($2 \text{ workloads} \times 2 \text{ concurrencies} \times 20 \text{ blocks} \times 2 \text{ modes} = 160 \text{ trials}$).
- **Equivalence Margin**: $\pm 5\%$ throughput ratio ($B/A \in [0.95, 1.05]$).
- **Workload E Writer-Free Diagnostic**:
  - Workload E with 0 writers ($w=0$) at $r \in \{1, 32\}$, 5 paired blocks per concurrency ($2 \times 5 \times 2 = 20 \text{ trials}$).

### 3.3 Experiment T3: Rapid Churn Overhead (Workload D)
- **Workload**: `WORKLOAD_D_READER_CHURN`
- **Initial Fixture**: 4 SSTables, 2,500 keys/SSTable (10,000 keys total).
- **Reader Concurrency**: $r \in \{1, 4, 16\}$ (3 levels).
- **Conditions**:
  - `controlled_churn`: 3 concurrent writers, 16KB Memtable threshold, 600ms background compaction trigger.
  - `no_churn_control`: 3 concurrent writers writing identically, 64MB Memtable threshold (no Memtable rotation), 0 compactions triggered.
- **Sample Size**: 15 independent paired blocks per configuration ($3 \text{ concurrencies} \times 2 \text{ conditions} \times 15 \text{ blocks} \times 2 \text{ modes} = 180 \text{ trials}$).
- **Profiling Runs**: Dedicated non-concurrent profiling runs capturing Go CPU (`pprof`), Heap allocation, and Mutex contention profiles for both modes under controlled churn.

---

## 4. Total Trial Accounting

| Experiment | Workload | Concurrencies | Conditions | Paired Blocks | Total Trials |
| :--- | :--- | :---: | :---: | :---: | :---: |
| **T1** | Workload B2 | $r \in \{2, 4, 8\}$ | Flush vs Sham | 20 per cell | 240 |
| **T2 (Formal)** | Workloads A & E | $r \in \{1, 32\}$ | Normal | 20 per cell | 160 |
| **T2 (Diagnostic)** | Workload E | $r \in \{1, 32\}$ | Writer-free ($w=0$) | 5 per cell | 20 |
| **T3** | Workload D | $r \in \{1, 4, 16\}$ | Churn vs No-Churn | 15 per cell | 180 |
| **Total** | | | | | **600 trials** |

---

## 5. Statistical Analysis Methods

1. **Paired Bootstrap**: Resample matched pairs $(A_i, B_i)$ with replacement ($B = 10,000$ iterations) preserving the block structure.
2. **Wilcoxon Signed-Rank Test**: Exact two-sided and one-sided signed-rank test on matched pair differences.
3. **Multiple Testing Correction**: Holm-Bonferroni step-down procedure applied to primary confirmatory hypotheses ($r=2, 4$ in T1).
4. **Two One-Sided Tests (TOST)**:
   - Log-transformed throughput ratio: $d_i = \ln(T_{B,i}) - \ln(T_{A,i})$.
   - Margin: $[-\Delta, +\Delta] = [\ln(0.95), \ln(1.05)] \approx [-0.051293, +0.048790]$.
   - Compute 90% confidence interval for mean log difference $\mu_d$. If $90\%\text{ CI} \subset [-\Delta, +\Delta]$, reject non-equivalence at $\alpha=0.05$.
5. **Difference-in-Differences (DiD)**:
   $$\Delta\Delta = \text{Median}(p95_{A,\text{flush}} - p95_{B,\text{flush}}) - \text{Median}(p95_{A,\text{sham}} - p95_{B,\text{sham}})$$

---

## 6. Execution and Stopping Rules

- **Preflight Check**: 1 paired block per experiment condition (16 trials) must pass fail-closed validation and show zero runtime panics, zero data races, and correct metric tracking before full execution.
- **Fail-Closed Policy**: Any trial encountering fixture verification failure, disk full, or unexpected I/O abort is flagged INVALID and excluded; the pair is immediately re-executed with a new seed.
- **Stopping Rule**: All 600 planned trials must execute to completion. Sample size will not be dynamically expanded based on intermediate results.
