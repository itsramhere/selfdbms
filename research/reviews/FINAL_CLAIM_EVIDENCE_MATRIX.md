# Phase I6.10: Final Manuscript Claim-Evidence Matrix

- **Document**: `paper.tex` (Authoritative Manuscript, revision before Phase I6.10)
- **Auditor**: External Reviewer Forensic Protocol (Phase I6.10)
- **Scope**: Systematic audit of every substantive technical, empirical, and architectural claim.
- **Underlying Campaigns**:
  - `campaign_wsl2_ext4_restart_v1` (960 trials)
  - `targeted_validation_v1` (600 trials)
  - Deterministic synchronization unit test suite (`lsmengine/` and `targeted_test.go`)
- **Audit Date**: 2026-10-28

---

## 1. Summary of Claim Audit Verdicts

| Total Claims Audited | Supported | Supported with Qualification | Unsupported | Unverifiable |
| :---: | :---: | :---: | :---: | :---: |
| **16** | **9** | **5** | **2** | **0** |

- **Supported (9)**: Solid empirical data (95% bootstrap CIs, verified medians) or machine-checked deterministic test proofs directly support the assertion.
- **Supported with Qualification (5)**: Empirical phenomenon is present and verified, but scope constraints, causal attributions, or equivalence wording require qualification.
- **Unsupported (2)**:
  1. *Claim 6 (Sec 5.3 line 234)*: Latency unit error asserting "42.7 µs vs. 175.4 µs" when the actual data are milliseconds (42.7 ms vs. 175.4 ms; or 42,735 µs vs. 175,434 µs).
  2. *Claim 9 (Sec 5.4 line 236)*: Assertion of "exact empirical parity" in Workload A, conflating failure to reject the null (wide CIs [0.63x, 1.44x]) with proof of equivalence (TOST inconclusive at $r=32$).

---

## 2. Systematic Claim-Evidence Matrix

### Claim 1: Coarse-Grained RWMutex Contention Mechanism
- **Location**: Abstract (lines 39), Section 1 (lines 53–54), Section 3.1 (lines 101–102).
- **Original Wording**:
  > *"In coarse-grained synchronization models (Approach A), acquiring a manager-wide reader-writer lock (`sync.RWMutex.RLock`) over physical disk I/O windows starves concurrent writes and background flush/compaction publications... When a reader executes pread against an SSTable on disk, it holds m.Mu.RLock(). Because Go's sync.RWMutex grants writer preference, an incoming writer or flusher calling m.Mu.Lock() immediately blocks all subsequent reader queries."*
- **What it Asserts**: The structural coupling of Go's writer-preferring `sync.RWMutex` with blocking disk I/O creates mutual starvation between readers and concurrent writers/flushers.
- **Supporting Evidence**: Source inspection of `lsmengine/manager.go` (`Get` wraps `m.Mu.RLock()`), Go standard library `sync/rwmutex.go` (writer preference semantics), and Workload B1 empirical collapse (writes drop from 270 at $r=1$ down to 60 at $r=32$).
- **Supporting Campaign**: Source code inspection, Go runtime specification, and 960-trial Campaign (Workload B1).
- **Configuration & N**: $r \in [1, 32]$, $w = 2$, $N = 10$ per cell ($N = 160$ in B1).
- **Measured Effect**: Writes completed collapse by 77.8% in A (270 to 60) while in B they remain resilient (2,918 to 198).
- **Claim Nature**: Structural / Mechanistic.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 2: Writer Progress Improvement Under Concurrent Reads (Workload B1)
- **Location**: Abstract (lines 41), Section 1 (line 61), Section 5.2 (lines 230–231).
- **Original Wording**:
  > *"Approach B eliminates reader-writer starvation, achieving a 3.3x to 18.5x increase in completed write progress (up to +2,712 writes in 10s)... completing 2,573 writes at r=8 (vs. 139 in Approach A, +2,434 writes) and 264 writes at r=24 (vs. 77 in Approach A, +187 writes)."*
- **What it Asserts**: Isolating readers onto pinned immutable snapshots prevents reader disk reads from blocking writers, dramatically increasing write transaction completion under strict durability (`wal_sync=true`).
- **Supporting Evidence**: `research/experiments/data/campaign_wsl2_ext4_restart_v1/results.csv`, Workload B1.
- **Supporting Campaign**: 960-trial Campaign (`campaign_wsl2_ext4_restart_v1`).
- **Configuration & N**: Workload B1 ($w=2$, `wal_sync=true`, 1MB Memtable), $r \in [1, 32]$, $N = 10$ per cell.
- **Measured Effect**:
  - $r=1$: A=270, B=2,918 (10.8x [9.1x, 15.6x])
  - $r=4$: A=159, B=2,871 (18.1x [15.2x, 28.1x], diff +2,712 writes)
  - $r=8$: A=139, B=2,573 (18.5x [16.8x, 28.4x], diff +2,434 writes)
  - $r=24$: A=77, B=264 (3.4x [2.6x, 5.0x], diff +187 writes)
  - $r=32$: A=60, B=198 (3.3x [2.7x, 5.0x], diff +138 writes)
  95% bootstrap CIs strictly exclude 1.0 across all reader concurrencies.
- **Claim Nature**: Observational.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain numerical assertions; clarify that this effect is evaluated under synchronous WAL fsync.

---

### Claim 3: Read Throughput Doubling Under Writers (Workload B1)
- **Location**: Abstract (line 41), Section 1 (line 61), Section 5.2 (line 231).
- **Original Wording**:
  > *"Concurrently, read throughput increases by up to 2.07x (260.3 vs. 125.5 ops/s at r=4)..."*
- **What it Asserts**: Eliminating lock contention not only helps writers, but concurrently increases read throughput by up to 2.07x at intermediate concurrencies ($r=4, 8$).
- **Supporting Evidence**: Workload B1 in `comparative_tables.md`.
- **Supporting Campaign**: 960-trial Campaign.
- **Configuration & N**: Workload B1, $r=4$, $N=10$ per mode.
- **Measured Effect**:
  - $r=4$: Median A = 125.5 ops/s, Median B = 260.3 ops/s. Ratio of Medians = 2.07x [1.47x, 3.21x], Diff = +134.8 ops/s [+61.3, +181.3].
  - $r=8$: Median A = 219.2 ops/s, Median B = 450.7 ops/s. Ratio of Medians = 2.06x [1.44x, 3.18x].
- **Claim Nature**: Observational.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 4: Read Tail Latency Drop in Workload B1 (p99)
- **Location**: Section 5.2 (line 231).
- **Original Wording**:
  > *"...while 99th-percentile tail latency drops from 89.3 ms to 39.8 ms."*
- **What it Asserts**: In Workload B1 at $r=4$, 99th-percentile point lookup latency decreases from 89.3 ms in Approach A to 39.8 ms in Approach B.
- **Supporting Evidence**: Workload B1 $r=4$ in `comparative_analysis.json`:
  `latency_read_p99_us`: A median = 89,258.7 µs (89.26 ms), B median = 39,830.4 µs (39.83 ms).
- **Supporting Campaign**: 960-trial Campaign.
- **Configuration & N**: Workload B1, $r=4$, $N=10$.
- **Measured Effect**: Ratio = 0.45x (55.4% latency drop).
- **Claim Nature**: Observational.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording (explicitly identify $r=4$).

---

### Claim 5: 95th-Percentile Read Tail Latency Suppression Under Flushes (Workload B2)
- **Location**: Abstract (line 41), Section 1 (line 61), Section 5.3 (lines 233–234).
- **Original Wording**:
  > *"...suppressing 95th-percentile read tail latency by 73%--76% during active Memtable flushes... At r=2 and r=4, 95th-percentile read latency drops by 73% to 76% (42.7 µs vs. 175.4 µs at r=2; 48.9 µs vs. 181.0 µs at r=4)."*
- **What it Asserts**: Under periodic injected Memtable flushes (Workload B2), Approach B suppresses 95th-percentile read tail latency by 73% to 76% at $r=2$ and $r=4$.
- **Supporting Evidence**: Workload B2 in `comparative_analysis.json` and confirmatory Experiment T1 in `targeted_validation_v1`.
- **Supporting Campaign**: 960-trial Campaign (Workload B2) and 600-trial Campaign (Experiment T1).
- **Configuration & N**: $r=2, 4$, injected 500ms flushes, $N=10$ per mode in B2; $N=20$ pairs per condition in T1.
- **Measured Effect**:
  - In 960-trial B2:
    - $r=2$: A = 175,434 µs (175.4 ms), B = 42,735 µs (42.7 ms), suppression = 75.6% (~76%), paired Wilcoxon $p = 0.0010$.
    - $r=4$: A = 180,966 µs (181.0 ms), B = 48,916 µs (48.9 ms), suppression = 73.0%, paired Wilcoxon $p = 0.0010$.
  - In 600-trial T1 (`flush_enabled`):
    - $r=2$: A = 220,919 µs, B = 184,036 µs (paired RoM = 0.83x [0.76x, 0.91x], $p = 0.000010$).
    - $r=4$: A = 56,354 µs, B = 35,529 µs (paired RoM = 0.63x [0.57x, 0.67x], $p = 0.000001$).
- **Claim Nature**: Observational / Comparative.
- **Verdict**: **Supported with qualification**.
- **Qualification Required**: The 73%--76% effect is specific to low reader concurrencies ($r=2, 4$). At $r=1$, the difference is not statistically significant ($p=0.3477$). At higher concurrencies ($r \ge 16$), host scheduling delays dominate, narrowing tail reduction to 18% ($r=16$) and 4% ($r=32$). Furthermore, overall trial throughput differences in Workload B2 are statistically inconclusive (CIs cross 1.0).
- **Replacement Wording**:
  *"...suppressing 95th-percentile read tail latency by 73%--76% at low reader concurrencies ($r=2, 4$) during periodic Memtable flushes, though whole-trial throughput differences remain statistically inconclusive across all concurrency levels."*

---

### Claim 6: Workload B2 Latency Magnitude Unit Error
- **Location**: Section 5.3 (line 234).
- **Original Wording**:
  > *"At r=2 and r=4, 95th-percentile read latency drops by 73% to 76% (42.7 µs vs. 175.4 µs at r=2; 48.9 µs vs. 181.0 µs at r=4)."*
- **What it Asserts**: Latencies are 42.7 to 181.0 microseconds (µs).
- **Supporting Evidence**: `comparative_tables.md` and raw JSON fields `latency_read_p95_us`:
  - $r=2$: A = 175,434.2 µs = **175.4 ms**, B = 42,735.0 µs = **42.7 ms**.
  - $r=4$: A = 180,965.7 µs = **181.0 ms**, B = 48,916.2 µs = **48.9 ms**.
- **Supporting Campaign**: 960-trial Campaign.
- **Measured Effect**: Values are in milliseconds, but labeled with the unit symbol "µs".
- **Claim Nature**: Empirical measurement / typographical error.
- **Verdict**: **Unsupported (Unit Error)**.
- **Replacement Wording**:
  Change `42.7 µs vs. 175.4 µs` to `42.7 ms vs. 175.4 ms (42,735 µs vs. 175,434 µs)`.

---

### Claim 7: Causal Attribution of B2 Latency Suppression to Physical Flush Publication
- **Location**: Section 5.3 (line 234–235).
- **Original Wording**:
  > *"In Approach A, readers arriving during publication stall behind the exclusive lock, inflating latency to hundreds of milliseconds. In Approach B, readers continue querying their pinned snapshot uninterrupted while the flusher swaps pointers in microseconds."*
- **What it Asserts**: Tail latency inflation in Approach A is causally caused specifically by physical flush publication mutual exclusion.
- **Supporting Evidence**: Confirmatory 600-trial campaign, Experiment T1 (`confirmatory_analysis.md`).
- **Supporting Campaign**: 600-trial Campaign (`targeted_validation_v1`).
- **Configuration & N**: Experiment T1 tested `flush_enabled` vs `sham_flush` (where the flusher acquires the lock for 50µs without physical disk flush publication) across $N = 20$ pairs per condition.
- **Measured Effect**:
  Under `sham_flush`, Approach B also suppressed tail latency significantly:
  - $r=2$: Sham A = 111.5 ms, Sham B = 45.4 ms (paired RoM = 0.41x [0.28x, 0.98x], $p=0.0036$).
  - $r=4$: Sham A = 36.0 ms, Sham B = 34.2 ms (paired RoM = 0.95x [0.89x, 0.98x], $p=0.0004$).
  - $r=8$: Sham A = 169.3 ms, Sham B = 61.1 ms (paired RoM = 0.36x [0.32x, 0.75x], $p=0.000001$).
  The Difference-in-Differences (DiD) between active flush and sham flush confirms an additional physical flush effect ($\Delta\Delta = +42.69$ ms at $r=2$, $+19.43$ ms at $r=4$, $+30.55$ ms at $r=8$), but demonstrates that general exclusive lock contention accounts for a substantial fraction of the observed latency spike.
- **Claim Nature**: Causal mechanism.
- **Verdict**: **Supported with qualification**.
- **Qualification Required**: The tail latency spike in Approach A is not caused solely by the physical file-swapping event, but by general exclusive lock convoying in Go's write-preferring `RWMutex`, which blocks arriving readers whenever an exclusive lock is queued or held.
- **Replacement Wording**:
  *"In Approach A, readers arriving while an exclusive publication lock is queued or held stall behind the writer lock, inflating latency to hundreds of milliseconds. In Approach B, readers continue querying their pinned snapshot uninterrupted while metadata swaps execute without blocking readers."*

---

### Claim 8: Memory-Tier Zero Measurable Regression (Workload E)
- **Location**: Abstract (line 41), Section 5.4 (lines 236–237).
- **Original Wording**:
  > *"In uncontended disk reads and pure in-memory Memtable lookups, both synchronization strategies demonstrate similar observed throughput (0.97x--1.03x), confirming zero measurable memory-tier regression."*
- **What it Asserts**: When queries hit active SkipList Memtables with 2 concurrent writers, Approach B introduces no measurable throughput penalty or regression compared to Approach A.
- **Supporting Evidence**: Workload E in `comparative_tables.md`.
- **Supporting Campaign**: 960-trial Campaign (Workload E) and 600-trial Campaign (Experiment T2).
- **Configuration & N**: Workload E ($w=2$, 64MB Memtable, 0 disk reads), $r \in [1, 32]$, $N=10$ per cell ($N=160$).
- **Measured Effect**:
  - $r=1$: A=656.2, B=666.1 ops/s (1.02x [0.92x, 1.17x])
  - $r=8$: A=5,159.5, B=5,066.0 ops/s (0.98x [0.82x, 1.25x])
  - $r=32$: A=20,150.8, B=20,601.8 ops/s (1.02x [0.86x, 1.09x])
  Median read latency remains sub-2µs ($p50 \approx 1.5\text{--}1.6$ µs) in both modes.
- **Claim Nature**: Observational / Comparative.
- **Verdict**: **Supported with qualification**.
- **Qualification Required**: While observed throughput medians are within $\pm 2\%$, formal equivalence testing (TOST at $\pm 5\%$) in Experiment T2 was inconclusive ($p=0.3031$ at $r=1$, $p=0.1896$ at $r=32$). The paper should state that observed throughput is closely aligned within observed bootstrap intervals rather than claiming a mathematically proven zero regression.
- **Replacement Wording**:
  *"In pure in-memory Memtable lookups (Workload E), observed throughput medians are closely aligned (0.98x--1.02x) across all reader counts with sub-2µs median latencies, showing no detectable memory-tier throughput regression within the experimental resolution."*

---

### Claim 9: Exact Empirical Parity in Uncontended Disk Reads (Workload A)
- **Location**: Section 5.4 (line 236).
- **Original Wording**:
  > *"In Workload A (uncontended disk reads)... Approach B demonstrates exact empirical parity with Approach A (0.97x--1.03x)."*
- **What it Asserts**: Approach A and B have proven identical performance in read-only disk workloads.
- **Supporting Evidence**: Workload A in `comparative_tables.md` and Experiment T2 in `confirmatory_analysis.md`.
- **Supporting Campaign**: 960-trial Campaign and 600-trial Campaign (T2).
- **Configuration & N**: Workload A ($w=0$, 4 pre-built SSTables), $r \in [1, 32]$, $N=10$ per cell.
- **Measured Effect**:
  - $r=1$: 0.98x [0.76x, 1.27x]
  - $r=4$: 1.02x [0.72x, 1.45x]
  - $r=16$: 0.97x [0.68x, 1.39x]
  - $r=32$: 1.00x [0.63x, 1.44x]
  In Experiment T2, TOST ($\pm 5\%$) confirmed equivalence at $r=1$ ($p=0.0499$), but was inconclusive at $r=32$ ($p=0.2764$, 90% CI [0.9908x, 1.0798x]).
- **Claim Nature**: Equivalence claim.
- **Verdict**: **Unsupported (Overstatement)**.
- **Reasoning**: Failure to reject the null hypothesis with wide bootstrap confidence intervals (e.g. $[0.63, 1.44]$ at $r=32$) does not constitute proof of "exact empirical parity".
- **Replacement Wording**:
  *"In Workload A (uncontended disk reads), observed throughput medians are statistically indistinguishable (0.97x--1.02x), with 95% bootstrap confidence intervals spanning parity across all tested reader concurrencies."*

---

### Claim 10: High Reader-Set Churn Throughput Penalty (Workload D)
- **Location**: Abstract (line 41), Section 1 (line 62), Section 5.5 (line 240).
- **Original Wording**:
  > *"...under rapid reader-set churn (>4 flushes/compactions per second), Approach B incurs a 26% to 37% throughput penalty at low thread counts (r <= 4, 0.63x--0.74x)..."*
- **What it Asserts**: When Memtables are small (16KB), forcing rapid flushes and compactions, Approach B suffers a severe throughput regression at low concurrencies ($r=1, 2, 4$).
- **Supporting Evidence**: Workload D in `comparative_tables.md` and Experiment T3 in `confirmatory_analysis.md`.
- **Supporting Campaign**: 960-trial Campaign and 600-trial Campaign.
- **Configuration & N**: Workload D (16KB Memtable, >4 publication events/sec), $r=1, 2, 4$, $N=10$ per cell.
- **Measured Effect**:
  - $r=1$: A=475.4, B=298.0 ops/s (0.63x [0.55x, 0.70x], 37% penalty)
  - $r=2$: A=904.8, B=598.9 ops/s (0.66x [0.63x, 0.79x], 34% penalty)
  - $r=4$: A=1,701.7, B=1,267.3 ops/s (0.74x [0.65x, 0.90x], 26% penalty)
  All 95% CIs are strictly below 1.0.
  Experiment T3 confirmed regression in `controlled_churn`: $r=1$ (0.69x [0.62x, 0.78x]), $r=4$ (0.76x [0.71x, 0.83x]).
- **Claim Nature**: Observational / Negative Result.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 11: Attribution of Churn Penalty to Snapshot Lifecycle Overheads
- **Location**: Abstract (line 41), Section 5.5 (lines 241–242).
- **Original Wording**:
  > *"...due to the CPU costs of snapshot descriptor allocation, atomic reference counting, and deferred reader retirement. Under rapid churn, the CPU cost of allocating new ReaderSnapshot descriptors, executing atomic CAS reference count updates, and tracking asynchronous reader retirement exceeds the concurrency benefit when reader threads are few."*
- **What it Asserts**: The mechanistic driver of the 26%–37% churn regression is snapshot descriptor allocation and reference count tracking.
- **Supporting Evidence**: Microbenchmarks (`t3_microbench_test.go`) and profiling diagnostics in `research/checkpoints/CP6_E.md`.
- **Supporting Campaign**: Microbenchmarks and targeted profiling.
- **Configuration & N**: Profiling trials under 16KB threshold.
- **Measured Effect**: Microbenchmark confirms `AcquireSnapshot` and `ReleaseSnapshot` cost ~42–68 ns per operation with atomic cache-line bouncing. Under >4 flushes/sec, descriptor allocations force periodic GC and metadata lock acquisitions.
- **Claim Nature**: Mechanistic explanation.
- **Verdict**: **Supported with qualification**.
- **Qualification Required**: The manuscript must not attribute an unverified exact numerical CPU percentage (e.g. earlier draft claims of 34.4% CPU time) to snapshot lifecycle costs. The text correctly provides a qualitative mechanistic explanation, but should be explicit that this is an architectural attribution supported by microbenchmarks and profiling rather than a single-variable hardware counter isolation in the 960-trial dataset.
- **Replacement Wording**:
  *"...consistent with the CPU costs of snapshot descriptor allocation, atomic reference counting, and deferred reader retirement under rapid version replacement."*

---

### Claim 12: Compaction Exposure Constraint and Inconclusive High-Concurrency Gains (Workload C)
- **Location**: Section 5.6 (lines 245–247).
- **Original Wording**:
  > *"In Workload C... at high concurrency (r >= 16), Approach B achieves a modest 1.03x--1.05x gain, but its 95% bootstrap CIs cross 1.0 ([0.85, 1.22] at r=16), rendering high-concurrency throughput gains statistically inconclusive. Crucially, instrumentation reveals that the active compaction window spans only 149--372 ms in B and 266--724 ms in A (only 1.5%--3.7% of the 10-second trial). Furthermore, less than 1.6% of total trial reads take place during active compaction."*
- **What it Asserts**: High-concurrency speedups during compaction cannot be asserted as statistically significant, and cannot be causally attributed to compaction concurrency because the compaction window represents <3.7% of trial duration and <1.6% of total queries.
- **Supporting Evidence**: Workload C in `comparative_tables.md` and harness instrumentation logs.
- **Supporting Campaign**: 960-trial Campaign.
- **Configuration & N**: Workload C, $r \in [1, 32]$, $N=10$.
- **Measured Effect**:
  - $r=16$: 1.04x [0.85x, 1.22x] (inconclusive)
  - $r=32$: 1.03x [0.89x, 1.34x] (inconclusive)
  - Active compaction window: 149–372 ms (B), 266–724 ms (A).
  - Exposure fraction: <1.6% of reads.
- **Claim Nature**: Methodological audit / negative finding.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 13: Instrumentation Audit of Publication Overlap Semantics
- **Location**: Section 6 (lines 249–254).
- **Original Wording**:
  > *"The counter measures reads that finish between OnFlushPublishBegin() (called before lock acquisition) and OnFlushPublishEnd() (called after lock release). In Approach A, the flush worker must wait for existing in-flight readers to release RLock() before acquiring Lock(). These draining in-flight readers complete and increment the counter during the worker's wait window. Exactly zero reads ever execute while the exclusive lock is actually held."*
- **What it Asserts**: Positive overlap counters in Approach A reflect reader drain delay prior to lock acquisition, not concurrent read execution while `m.Mu.Lock()` is active.
- **Supporting Evidence**: Deterministic unit test `TestMetricSemantics_ApproachA_ReadsDuringPublicationAudit` in `lsmengine/flush_publication_overlap_test.go`.
- **Supporting Campaign**: Machine-checked unit testing.
- **Measured Effect**: Test deterministically forces an in-flight reader, triggers publication, and verifies that the counter increments during reader drain, while zero operations execute between `m.Mu.Lock()` acquisition and release.
- **Claim Nature**: Deterministic mechanistic proof.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 14: Formal Lifecycle Invariant Guarantees
- **Location**: Section 3.3 (lines 132–139).
- **Original Wording**:
  > *"Approach B enforces four strict lifecycle invariants: 1. Pin Exclusivity... 2. Atomic Reference Tracking... 3. Exactly-Once Reclamation... 4. Shutdown Drain Barrier..."*
- **What it Asserts**: These four protocol invariants prevent use-after-free, double-close, and file descriptor leaks under concurrent execution.
- **Supporting Evidence**: Unit tests in `lsmengine/reader_lifetime_test.go`, `cleanup_coordination_test.go`, `snapshot_shutdown_test.go`, `combined_lifecycle_validation_test.go`.
- **Supporting Campaign**: Correctness test suite (run under Go race detector `go test -race`).
- **Measured Effect**: 100% pass rate with zero race warnings.
- **Claim Nature**: Architectural / Formal Invariant.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 15: PRNG Seed Structure and Lack of Lockstep Operation Parity
- **Location**: Section 7 (line 258).
- **Original Wording**:
  > *"Internal Validity: The PRNG seed formula contains a mode index mIdx, ensuring that Approach A and B execute statistically equivalent Zipfian distributions rather than identical lockstep operation traces."*
- **What it Asserts**: Approach A and Approach B trials did not execute identical key streams because the original `runner.go` seed formula included a mode-dependent offset.
- **Supporting Evidence**: Inspection of `research/experiments/harness/runner.go` (`baseSeed := Seed + TrialIndex*10007`).
- **Supporting Campaign**: Source code audit and Defect D2 analysis in CP6_F_PRE.
- **Measured Effect**: Verified that paired trials drew keys from distinct independent pseudorandom sequences initialized with different seeds.
- **Claim Nature**: Limitation disclosure.
- **Verdict**: **Supported**.
- **Replacement Wording**: Retain existing wording.

---

### Claim 16: Reproducibility Appendix Commit Reference
- **Location**: Appendix (line 279).
- **Original Wording**:
  > *"Frozen Git Commit: a5c0fb97deb8a67705a80e76b49e25f31d692492"*
- **What it Asserts**: The research artifacts and code are frozen at commit `a5c0fb97deb8a67705a80e76b49e25f31d692492`.
- **Supporting Evidence**: Git history audit. Commit `a5c0fb9` froze the Protocol 2.0.0 specification, but the 960-trial dataset completed in `e156b92`, and the targeted validation runner was authored in `370fd6c` and evaluated in `4a06217`.
- **Supporting Campaign**: Git provenance forensic audit (Part A).
- **Claim Nature**: Procedural / Provenance.
- **Verdict**: **Supported with qualification**.
- **Qualification Required**: Commit `a5c0fb9` only froze Campaign I5. The appendix should explicitly list:
  1. The frozen 960-trial campaign commit `e156b92` (protocol freeze `a5c0fb9`).
  2. The 600-trial confirmatory targeted campaign commit `4a06217` (source identical to `370fd6c`).
  3. The independent provenance verification showing the historical executable `targeted_runner` matches Git commit `370fd6c` byte-for-byte upon rebuild.
- **Replacement Wording**: Update Appendix with complete provenance hierarchy.
