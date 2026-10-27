# Preregistered Hypotheses: Phase I6.9 Corrective Validation

- **Campaign ID**: `I6_CORRECTIVE_MECHANISMS_V1`
- **Protocol Version**: 1.1.0
- **Primary Target**: Causal Decomposition of Read-Tail Latency, TOST Equivalence, and Snapshot Churn Isolation
- **Evaluation Baseline**: WSL2 Ubuntu 24.04 LTS on native Linux ext4 (`/dev/sdd`), Go 1.27.3 linux/amd64

---

## 1. Experiment T1: Flush Tail Latency & Causal Decomposition (Workload B2)

### 1.1 Research Question
Does Approach B's read-tail improvement arise specifically from avoiding physical flush publication locking, or from a broader reduction in reader/writer contention (including WAL fsync under `m.Mu.Lock()`)?

### 1.2 Hypotheses

#### Primary Endpoint 1: Active Flush Replication with Corrected Worker Seeds
- **Null Hypothesis ($H_{0, \text{T1-C}}$)**: In Condition C (active flushing with periodic transactional write batches, $r \in \{2, 4\}$), the median paired difference in $p95$ read latency between Approach B and Approach A is greater than or equal to zero:
  $$\tilde{D}_{\text{C}} = \text{Median}(p95_{B, i} - p95_{A, i}) \ge 0$$
- **Alternative Hypothesis ($H_{1, \text{T1-C}}$)**: Approach B achieves strictly lower $p95$ read latency than Approach A under active flushing:
  $$\tilde{D}_{\text{C}} < 0$$

#### Primary Endpoint 2: Causal Decomposition of Mechanism
We decompose the total observed latency difference into two additive components:
1. **Writer Contention Effect ($\Delta_{\text{contention}}$)**: Difference induced by WAL-synchronized writes without physical flushing:
   $$\Delta_{\text{contention}} = \text{Median}(p95_{A} - p95_{B})_{\text{Cond B}} - \text{Median}(p95_{A} - p95_{B})_{\text{Cond A}}$$
2. **Flush Publication Effect ($\Delta_{\text{pub}}$)**: Incremental difference induced by physical flush and SSTable publication locking:
   $$\Delta_{\text{pub}} = \text{Median}(p95_{A} - p95_{B})_{\text{Cond C}} - \text{Median}(p95_{A} - p95_{B})_{\text{Cond B}}$$

- **Hypothesis $H_{1, \text{T1-M1}}$ (Contention Mechanism)**: $\Delta_{\text{contention}} > 0$. Reader/writer mutex convoying during synchronous WAL `fsync` under `m.Mu.Lock()` contributes significantly to Approach A's tail latency.
- **Hypothesis $H_{1, \text{T1-M2}}$ (Publication Locking Mechanism)**: $\Delta_{\text{pub}} > 0$. SSTable publication exclusive lock acquisition creates an additional tail latency penalty in Approach A that is avoided by Approach B's atomic snapshot publication.

#### Secondary Endpoint: Deterministic Synchronization Barrier (Condition D)
- **Hypothesis $H_{1, \text{T1-D}}$**: In a deterministic barrier test where publication lock acquisition is aligned with reader arrivals, arriving reader latency in Approach A is strictly bounded from below by the publication lock hold duration ($T_{\text{read, A}} \ge T_{\text{pub\_hold}}$), whereas arriving reader latency in Approach B is decoupled from publication lock hold duration ($T_{\text{read, B}} \ll T_{\text{pub\_hold}}$).

---

## 2. Experiment T2: Practical Equivalence via TOST (Workloads A & E)

### 2.1 Research Question
Are read throughputs between Approach A and Approach B practically equivalent within a prespecified $\pm 5\%$ margin under non-churn workloads (Workloads A and E) when writer interference is eliminated?

### 2.2 Hypotheses

#### Workload A (Static SSTable Reads, $w=0$)
- **Equivalence Margin**: $\delta = \pm 5\%$ ($\theta_L = \ln(0.95) \approx -0.0513$, $\theta_U = \ln(1.05) \approx +0.0488$).
- **Null Hypothesis ($H_{0, \text{T2-A}}$)**: Mean paired log-throughput difference $\bar{d}$ falls on or outside equivalence bounds:
  $$\bar{d} \le \theta_L \quad \text{or} \quad \bar{d} \ge \theta_U$$
- **Alternative Hypothesis ($H_{1, \text{T2-A}}$)**: Mean paired log-throughput difference $\bar{d}$ falls strictly within equivalence bounds:
  $$\theta_L < \bar{d} < \theta_U$$

#### Workload E (Negative Control: Memtable-Resident, Writer-Free $w=0$)
- **Null Hypothesis ($H_{0, \text{T2-E}}$)**: Mean paired log-throughput difference $\bar{d}$ in writer-free Workload E falls on or outside equivalence bounds:
  $$\bar{d} \le \theta_L \quad \text{or} \quad \bar{d} \ge \theta_U$$
- **Alternative Hypothesis ($H_{1, \text{T2-E}}$)**: Approach A and Approach B are practically equivalent in writer-free Workload E, and multi-millisecond tail latency is completely suppressed ($p99 < 50\,\mu\text{s}$) for both approaches.

---

## 3. Experiment T3: Rapid Churn Overhead Decomposition (Workload D)

### 3.1 Research Question
What causes Approach B's observed read-throughput regression under rapid reader-set churn, and how much is attributable to snapshot management overhead versus background write contention and SSTable topology differences?

### 3.2 Hypotheses

#### Hypothesis $H_{1, \text{T3-Micro}}$ (Pure Snapshot Overhead)
In the dedicated microbenchmark with zero background writes and zero disk I/O, `AcquireSnapshot` + `ReleaseSnapshot` introduces a small, measurable CPU overhead of approximately 30–40 ns/op over uncontended `RWMutex.RLock` + `RUnlock` (approx. 7 ns/op), with exactly 0 heap allocations per operation.

#### Hypothesis $H_{1, \text{T3-Topology}}$ (Confound Resolution)
When SSTable topology is held strictly constant ($c=0$, initial 4 SSTables frozen throughout measurement) and writers are held to equal completed work:
1. The previously observed 10x throughput collapse in "no-churn" control does not recur.
2. In writer-free churn ($w=0$), Approach B's throughput regression relative to Approach A is bounded by the microarchitectural snapshot management cost.
3. Under paced equal-work background writes ($w=1$ paced), Approach B outperforms Approach A because Approach A readers suffer lock contention behind writer WAL `fsync`.
