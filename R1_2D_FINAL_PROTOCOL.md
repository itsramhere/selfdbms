# Phase I7 — Checkpoint R1.2d: Final Scientific Protocol Specification

**DOCUMENT ID:** `R1_2D_FINAL_PROTOCOL.md`  
**STATUS:** FORMAL PRE-REGISTERED EXPERIMENTAL PROTOCOL  
**GOVERNING DIRECTIVE:** CHECKPOINT R1.2d EXTERNAL SCIENTIFIC REVIEW  
**EVALUATED GIT REVISION:** `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`  
**BOUNDARY:** RIGOROUS DESIGN SPECIFICATION ONLY (ZERO BENCHMARK EXECUTION)

---

## 1. Research Framework and Core Questions (R1–R4)

This protocol governs the prospective experimental investigation into concurrency control mechanisms within log-structured merge-tree (LSM-tree) storage engines. The central research question remains strictly bounded:

> **How much of the concurrency benefit attributed to immutable reader snapshots can be obtained by shortening the manager-lock critical section using simpler reader-lifetime management (Approach A-min) compared to full snapshot isolation (Approach B) and coarse-grained locking (Approach A)?**

The four accepted research questions are:
- **R1 (Writer Progress):** To what extent does reader leasing (Approach A-min) eliminate reader-induced write starvation and achieve write throughput non-inferior to immutable snapshots (Approach B)?
- **R2 (Causal Synchronization Mechanism):** Does writer drain latency scale sub-linearly according to the multi-core renewal drain model $\mathbb{E}[W_{\text{drain}}] = \frac{r}{r+1} T_{\text{readCS}}$, rather than linear serial queueing $r \times T_{\text{readCS}}$?
- **R3 (Synchronization Overhead & Cardinality Scaling):** How does the metadata acquisition cost of Approach A-min ($O(n)$ per-SSTable atomic leases) compare to Approach B ($O(1)$ snapshot pins under exclusive `snapshotMu`) as active table cardinality $n$ varies?
- **R4 (Fixed-Volume Workload Equivalence):** Under matched fixed-write volume ($M = 2,500$ writes) and concurrent open-loop read pressure, does Approach A-min achieve time-to-completion and survival characteristics comparable to Approach B?

---

## 2. EXP-1: Writer Progress Specification

### 2.1 Separation of Workload Regimes
To prevent conflating closed-loop throughput limits with client queuing delays, EXP-1 evaluates two distinct workload operating modes:

1. **Free-Running (Closed-Loop) Writer Progress:**
   - A single dedicated writer goroutine continuously submits transactions: as soon as transaction $k$ commits, transaction $k+1$ is initiated immediately without inter-arrival delay.
   - **Primary Metric:** Sustainable write completion rate:
     $$\dot{W}_{\text{free}} = \frac{W_{\text{comp}}}{T_{\text{window}}} \quad [\text{writes/second}]$$
     where $T_{\text{window}} = 10.0\,\text{seconds}$.
   - Directly measures the engine's write processing capacity under continuous reader concurrency without client queue backpressure.

2. **Independently Scheduled (Paced / Open-Loop) Workloads:**
   - An independent Poisson/periodic arrival generator releases write requests at nominal target rate $\lambda_{\text{offered}} = 250\,\text{writes/s}$ into a client worker pool.
   - If writer publication stalls inside the engine, client worker threads block in `Commit()`. When all client worker threads are busy, incoming scheduled transactions accumulate in the unissued client queue ($W_{\text{queue}}$).
   - **Primary Metrics:**
     - Realized Completion Fraction: $\rho_{\text{comp}} = \frac{W_{\text{comp}}}{W_{\text{offered}}}$
     - Client Submission Backlog: $W_{\text{queue}}$
     - Paced Latency Percentiles: $p50, p95, p99$ of committed transactions.

### 2.2 Five-Category Transaction Accounting Ledger
Physical accounting conservation is strictly enforced for every trial:
$$W_{\text{offered}} = W_{\text{comp}} + W_{\text{late}} + W_{\text{fail}} + W_{\text{queue}}$$
1. **$W_{\text{comp}}$ (Completed In-Window):** Transactions that fully committed and acquired a finalized sequence number strictly before the 10.0-second window elapsed ($t_{\text{commit}} \le T_{\text{window}}$).
2. **$W_{\text{late}}$ (Completed Post-Window):** Transactions initiated before $T_{\text{window}}$ whose commit returned after $T_{\text{window}}$.
3. **$W_{\text{fail}}$ (Failed / Aborted Operations):** Transactions rejected by the engine, logging an exact diagnostic error code:
   - `ERR_CLIENT_TIMEOUT`: Client-side deadline expired ($t_{\text{client}} > 5.0\,\text{s}$).
   - `ERR_TX_CONFLICT`: Transaction validation conflict (invariant: strictly 0 under single-writer workloads; [`lsmengine/transaction.go#L88`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L88)).
   - `ERR_ENGINE_CLOSED`: Premature engine shutdown or unhandled panic.
   - `ERR_WAL_IO`: Operating system write failure on write-ahead log sync.
4. **$W_{\text{queue}}$ (Queued / Unissued):** Scheduled transactions that could not be dispatched before trial termination due to worker pool exhaustion.
5. **$W_{\text{att}}$ (Total Attempted):** $W_{\text{att}} = W_{\text{comp}} + W_{\text{late}} + W_{\text{fail}}$.

### 2.3 Mathematical Handling When Approach A Completes Zero Writes ($W_A = 0$)
Under intense reader concurrency, Approach A can suffer complete writer starvation ($W_{A, \text{comp}} = 0$). The protocol estimands are formulated to remain mathematically robust and non-singular:
- **Superiority of A-min over A:**
  $$\Delta \dot{W}(\text{AMIN} - \text{A}) = \frac{W_{\text{AMIN, comp}} - W_{A, \text{comp}}}{T_{\text{window}}} = \frac{W_{\text{AMIN, comp}} - 0}{10.0} = \dot{W}_{\text{AMIN}} > 0$$
  This difference is well-defined, strictly positive, and directly reflects writer progress.
- **Non-Inferiority of A-min to B:**
  $$d_{w, t} = \ln\left(\frac{W_{\text{AMIN, comp}, t} + 1}{W_{B, \text{comp}, t} + 1}\right)$$
  The $+1$ regularization prevents log-zero singularities and compares A-min directly to Approach B, independent of Approach A's starvation collapse.
- **Cohort-Level Capture Fraction:**
  $$\widehat{CF}_w = \frac{\bar{W}_{\text{AMIN, comp}} - \bar{W}_{A, \text{comp}}}{\bar{W}_{B, \text{comp}} - \bar{W}_{A, \text{comp}}} = \frac{\bar{W}_{\text{AMIN, comp}} - 0}{\bar{W}_{B, \text{comp}} - 0} = \frac{\bar{W}_{\text{AMIN, comp}}}{\bar{W}_{B, \text{comp}}}$$
  When Approach A starves completely, the capture fraction reduces smoothly to the direct throughput ratio between A-min and B.

---

## 3. EXP-2: Causal Synchronization Mechanism

### 3.1 Conditional Renewal Model vs. Universal Law
The renewal drain equation $\mathbb{E}[W_{\text{drain}}] = \frac{r}{r+1} T_{\text{readCS}}$ is treated as a **conditional mathematical hypothesis**, valid only when the following four physical prerequisites are satisfied:
1. **Parallel Execution without Core Saturation:** Reader thread count $r$ does not exceed available physical CPU cores ($r \le N_{\text{physical\_cores}}$), avoiding scheduler CPU starvation.
2. **Uniform Entry Phase Offsets:** Reader goroutines enter critical sections asynchronously, uniformly sampling arrival offsets across $[0, T_{\text{readCS}}]$.
3. **Immediate Writer Preemption:** Go standard library `sync.RWMutex` immediately bars incoming readers by atomically subtracting `rwmutexMaxReaders` ($1 \ll 30$) from `rw.readerCount`.
4. **Absence of Blocking Operations inside `RLock`:** Readers execute pure CPU memory operations without invoking page faults, disk I/O, or OS context switches while holding the lock.

### 3.2 Disjoint Latency Decomposition
To eliminate physical measurement overlaps, publication and reader latencies are decomposed into strictly disjoint intervals:

```
Sequential Flush Worker Timeline:
|--- t_flush_build (No locks held) ---|--- t_pub_wait (Drain) ---|--- t_pub_hold (Exclusive) ---|
```

1. **Sequential Publisher Disjoint Intervals (Flush Worker):**
   - $t_{\text{flush\_build}}$: SSTable file creation, sorting, and block indexing ([`lsmengine/manager.go#L1102-L1159`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L1102)). Executed with **zero manager locks held**.
   - $t_{\text{pub\_wait}}$: Elapsed duration from calling `m.Mu.Lock()` until the exclusive lock is granted. Quantifies active reader drain time.
   - $t_{\text{pub\_hold}}$: Duration `m.Mu.Lock()` is held exclusively to swap active SSTable metadata slices ($< 1\,\mu\text{s}$ in A and A-min; $\approx 1\dots 5\,\mu\text{s}$ in B).
   - Total Publication Time: $T_{\text{pub}} = t_{\text{pub\_wait}} + t_{\text{pub\_hold}}$.
2. **Concurrent Reader-Side Intervals (Reader Threads):**
   - $t_{\text{reader\_hold}}$: Duration reader holds protection (`RLock()` in Approach A; lease scratch active in Approach A-min; snapshot pin active in Approach B).
   - $t_{\text{reader\_mem}}$: Duration inspecting active `m.Mem` and `m.Frozenmem`.
   - $t_{\text{reader\_disk}}$: Duration reading SSTable blocks and performing binary search.
   *Reader intervals are reported as concurrent distributions and are never added into the publisher's sequential latency equation.*

### 3.3 Six-Cell Causal Intervention Suite
EXP-2 deploys a 6-cell factorial design ($N = 12$ matched triplets per cell = 216 executions):
- **Cell C2-01 (Zero-Delay Baseline Control):** $T_{\text{readCS}} = 0\,\mu\text{s}$, $r = 8$ readers.
- **Cell C2-02 (Delay Point 1):** $T_{\text{readCS}} = 100\,\mu\text{s}$, $r = 8$ readers.
- **Cell C2-03 (Delay Point 2):** $T_{\text{readCS}} = 300\,\mu\text{s}$, $r = 8$ readers.
- **Cell C2-04 (Delay Point 3):** $T_{\text{readCS}} = 500\,\mu\text{s}$, $r = 8$ readers.  
  *(Cells C2-01 through C2-04 test regression slope $\hat{\beta}_1 = \frac{\partial \bar{t}_{\text{pub\_wait}}}{\partial T_{\text{readCS}}}$ against theoretical $\frac{8}{9} \approx 0.889$.)*
- **Cell C2-05 (Concurrency Manipulation Control):** $T_{\text{readCS}} = 300\,\mu\text{s}$, $r = 2$ readers. Tests whether reducing reader pool drops the slope to $\frac{2}{3} \approx 0.667$.
- **Cell C2-06 (CPU-Load-Matched Control):** Readers execute an equivalent $300\,\mu\text{s}$ CPU spin loop *outside* the reader critical section ($T_{\text{inside}} = 0\,\mu\text{s}$, $T_{\text{outside}} = 300\,\mu\text{s}$), $r = 8$ readers. Demonstrates whether writer stalls are caused by lock contention or CPU core exhaustion.

---

## 4. EXP-3: Synchronization Overhead and Cardinality Scaling

### 4.1 Ground-Truth Code Operations
Source disassembly of the frozen engine reveals the exact per-read synchronization cost:
- **Approach B:** Executes **three lock acquisitions per point read** ([`lsmengine/snapshot.go#L680-L730`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/snapshot.go#L680)):
  1. `m.Mu.RLock()` to inspect memtables.
  2. `m.snapshotMu.Lock()` to register the snapshot descriptor (`rs.pins.Add(1)`).
  3. `m.snapshotMu.Lock()` in `ReleaseSnapshot()` (`rs.pins.Add(-1)`).
  - Algorithmic complexity: $O(1)$ independent of SSTable set size $n$.
- **Approach A-min:** Executes **one lock acquisition per point read** ([`lsmengine/manager.go#L447-L521`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L447)):
  1. `m.Mu.RLock()` to inspect memtables and copy active table pointers into a `sync.Pool` scratch buffer.
  2. Loops over $n$ active SSTables: calls `buf[i].lease()` (`atomic.Int32.Add(1)`). Zero exclusive locks acquired.
  3. Loops over $n$ active SSTables on release: calls `buf[i].releaseLease()` (`atomic.Int32.Add(-1)`).
  - Algorithmic complexity: $O(n)$ linear in active SSTable cardinality $n$.

### 4.2 Two-Tier Experimental Architecture
1. **Tier 3A (In-Memory Metadata Microbenchmark Suite; Go `testing.B`):**
   - Measures pure acquisition and release nanoseconds, allocations (`B/op`), and hardware lock stall cycles across $r \in \{1, 4, 8, 16\}$ and active cardinality $n \in \{2, 8, 32\}$.
   - Profiles mutex contention directly via `pprof -mutex` and `pprof -block`. Consumes **0 disk runs**.
2. **Tier 3B (In-Memory Engine Lookup Benchmark; Go `testing.B`):**
   - Evaluates point read throughput ($\text{ops/s}$) for `Manager.Get()` on static pre-compacted fixtures with $n \in \{2, 8, 32\}$ SSTables under concurrent read churn. Consumes **0 disk runs**.
- **Cardinality Crossover Hypothesis:** At small $n \le 4$, Approach A-min dominates Approach B due to zero exclusive lock acquisitions. As $n \ge 32$, Approach B's $O(1)$ pin counter is hypothesized to cross over and outperform Approach A-min's $O(n)$ atomic lease loops. If no crossover occurs, leasing dominance is formally affirmed.

---

## 5. EXP-4: Fixed-Write-Volume Comparability

### 5.1 Protocol Specification
- **Fixed Workload:** Sequential commitment of exactly $M = 2,500$ single-key transactions.
- **Concurrent Load:** Open-loop Poisson read arrivals at nominal offered rate $\lambda = 1,000\,\text{reads/s}$ across active memtable and L0 SSTable keys.
- **Execution Ceiling:** $T_{\text{max}} = 30.0\,\text{seconds}$.
- **Retry Budget:** Client transactions encountering transient engine errors execute up to $K_{\text{retry}} = 3$ retries before logging an operation abort.
- **Right-Censoring Treatment:** Trials reaching $T_{\text{max}} = 30.0\,\text{s}$ before committing all $M$ transactions are classified as **right-censored** ($C_i = 1$). They are recorded as legitimate treatment-induced performance outcomes, never discarded as harness failures.

### 5.2 Storage Topology Verification
Because background compaction threads operate continuously during execution, arms requiring longer completion times allow compactions more time to merge SSTable levels. EXP-4 tracks:
1. `flushes_completed`: Total memtable flushes committed.
2. `compactions_completed`: Total background compaction runs executed.
3. `active_sstables`: Total number of live SSTable files on disk at termination.
4. `total_disk_bytes`: Total physical storage footprint in bytes.

---

## 6. Pre-Registered Operational Stopping Rules (S-1 through S-5)

1. **Rule S-1 (Repeated Harness Failure Threshold):** Encountering 3 consecutive Category 2 harness defects, or exceeding the global campaign replacement budget ($k > 16$ replacements across 372 planned runs), triggers an immediate campaign halt.
2. **Rule S-2 (Data Corruption / Safety Invariant):** Any single occurrence of data inconsistency, uncommitted dirty read, or lease counter leak triggers immediate campaign abort and generates a forensic crash report.
3. **Rule S-3 (Storage Exhaustion Threshold):** Available disk space on the results volume dropping below $10.0\,\text{GB}$ causes the orchestrator to cleanly flush active JSON buffers and halt.
4. **Rule S-4 (System Calibration & Thermal Drift):** Anchored D3 drift controls drifting $> \pm 5.0\%$ relative to campaign opening baseline pauses execution for a mandatory 15-minute system stabilization period.
5. **Rule S-5 (Treatment-Related Degradation Exemption):** Occurrence of Approach A publication deadline overruns, write starvation, or $T_{\text{max}} = 30.0\,\text{s}$ timeouts **shall not stop execution**. They are recorded directly as valid experimental outcomes.
