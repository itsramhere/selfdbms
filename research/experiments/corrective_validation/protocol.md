# Corrective Experimental Protocol: Phase I6.9

- **Campaign ID**: `I6_CORRECTIVE_MECHANISMS_V1`
- **Protocol Version**: 1.1.0
- **Status**: **PREREGISTERED — READY FOR BOUNDED PREFLIGHT**
- **Target Source Branch**: `research/i6.9-corrective-preflight`
- **Base Commit**: `4a062174bb617a649cc3a2b03a83ee0e1e38ee45`
- **Target Platform**: WSL2 Ubuntu 24.04 LTS on native Linux ext4 (`/dev/sdd`), Go 1.27.3 linux/amd64
- **Historical Campaign Preservation**: All 960 trials of `campaign_wsl2_ext4_restart_v1` and all 600 trials of `targeted_validation_v1` remain strictly frozen and unmodified.

---

## 1. Experimental Objectives

This protocol implements the corrective requirements authorized under the **External Reviewer Directive — Phase I6.9**:
1. **Provenance Enforcement**: Replace silent `"unknown_rev"` fallback with strict fail-closed provenance validation, capturing full 40-character Git commit SHA, source tree hash, binary SHA-256, compiler version, build flags, and execution directory.
2. **Effective Random Seed Repair**: Eliminate trial-index-dependent seed derivation (`baseSeed := Seed + TrialIndex*10007`). Worker seeds are derived strictly from `(PairSeed, Role, WorkerIndex, Purpose)`, guaranteeing bit-for-bit identical PRNG consumption streams across matched A/B pairs.
3. **T1 Causal Mechanism Experiment**: Disentangle general reader/writer lock contention on `m.Mu` during WAL `fsync` from physical flush publication mutual exclusion, supplemented by a deterministic barrier synchronization test.
4. **T3 Churn Experiment**: Decompose snapshot churn overhead while holding SSTable topology constant (compaction disabled) and controlling background write volume.
5. **Measurement-Window Profiling**: Constrain Go CPU, mutex, block, allocation, and heap profiling strictly to the 5-second measurement window, completely excluding fixture setup and warmup. Profiling of Approach A and Approach B is executed in isolated processes.

---

## 2. Experimental Design & Inventory

### 2.1 Paired Block Structure
- **Experimental Unit**: A matched trial pair $(A_i, B_i)$ sharing an identical `PairSeed`.
- **Order Balancing**: Exactly 50% of pairs execute Forward order (Approach A then Approach B), and 50% execute Reverse order (Approach B then Approach A).
- **Trial Duration**: 5,000 ms measurement window, preceded by 1,000 ms warmup and 200 ms cooldown.

### 2.2 Sample Size Justification & Power Analysis
Using paired variance estimates from the 600-trial dataset:
- For T1 ($r=4$, active flush): observed paired effect size $d = \mu_d / s_d \approx 20.83 / 2.4 \approx 8.68$. With $N=10$ pairs, statistical power $1 - \beta > 0.999$ at $\alpha = 0.01$.
- For T2 (Workload A): log-throughput ratio standard deviation $s_{\ln} \approx 0.015$. For equivalence margin $\delta = \pm 5\%$ ($\theta = 0.0488$), ratio $\theta / s_{\ln} \approx 3.25$. With $N=10$ pairs, TOST power $> 0.99$.
- Therefore, $N = 10$ pairs per cell provides decisive statistical power without unnecessary execution cost.

### 2.3 Complete Campaign Inventory (280 Trials Total)

| Experiment | Workload | Condition | Concurrency ($r$) | Writers ($w$) | Pairs | Total Trials |
| :--- | :--- | :--- | :---: | :---: | :---: | :---: |
| **T1** | B2 (Controlled Flush) | `condA_pure_read` | 2, 4 | 0 | 10 per $r$ | 40 |
| **T1** | B2 (Controlled Flush) | `condB_wal_writes_no_flush` | 2, 4 | 2 | 10 per $r$ | 40 |
| **T1** | B2 (Controlled Flush) | `condC_flush_enabled` | 2, 4 | 0 (coord) | 10 per $r$ | 40 |
| **T2** | Workload A (Static Disk) | `pure_read` | 1, 32 | 0 | 10 per $r$ | 40 |
| **T2** | Workload E (Memtable) | `writer_free_memtable` | 1, 32 | 0 | 10 per $r$ | 40 |
| **T3** | Workload D (Reader Churn)| `writer_free_churn` | 1, 4, 16 | 0 | 10 per $r$ | 60 |
| **T3** | Workload D (Reader Churn)| `equal_work_churn` | 4 | 1 (paced) | 10 | 20 |
| **TOTAL** | — | — | — | — | **140 pairs** | **280 trials** |

- **Estimated Wall-Clock Cost**: $280 \times 6.2\,\text{s} \approx 1,736\,\text{s} \approx 29\,\text{minutes}$.

---

## 3. Bounded Preflight Protocol (Max 24 Trials)

Pursuant to Section I6.9.7 of the directive, the preflight suite is strictly bounded:
- **Authorized Bound**: Maximum 24 diagnostic trials across all configurations.
- **Selection**: Exactly Pair 1 of each of the 12 experimental conditions:
  - T1: 6 pairs (2 concurrencies $\times$ 3 conditions) = 12 trials
  - T2: 4 pairs (Workload A: $r=1, 32$; Workload E: $r=1, 32$) = 8 trials
  - T3: 2 pairs (Writer-free churn $r=4$; Equal-work churn $r=4$) = 4 trials
- **Preflight Total**: Exactly 24 trials.
- **Preflight Purpose**: Validate pipeline wiring, provenance assertions, worker seed identity, measurement-window hooks, and clean fail-closed shutdown.
- **Policy**: Preflight observations shall NOT be used to claim experimental findings or alter preregistered hypotheses.

---

## 4. Failure and Exclusion Criteria

A trial shall be marked as failed or excluded if:
1. `git_commit` is empty, `"unknown_rev"`, or cannot be verified against repository HEAD.
2. `fixture_manifest.validation_readback_status != "PASSED"`.
3. Any unexpected key validation error occurs (`validation_error_count > 0`).
4. Data race detector reports any race (`-race` test failure).
5. Trial execution exceeds timeout deadline (15 seconds).
6. Filesystem is detected as `9P_drvfs` (Windows-backed mount) instead of native Linux `ext4`.
