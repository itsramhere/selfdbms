# Phase I7 — Checkpoint R1.2a: Unresolved Scientific Questions and Interpretive Corrections

**DATE:** 2026-10-29  
**EVALUATION FOCUS:** SCIENTIFIC RIGOR, CAUSAL INTEGRITY, AND OBJECTIVE UNCERTAINTY  
**FROZEN MANIFEST REFERENCE:** `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\platform_manifest.json`  
**STATUS:** UNRESOLVED HYPOTHESES CATALOGED FOR EXTERNAL SCIENTIFIC REVIEW.

---

## 1. Research Question Integrity

In adherence to scientific principles, the central research question must **never be retroactively narrowed or altered** to fit truncated or negative empirical results:

> **Preregistered Research Question:**  
> *"How much of the concurrency benefit attributed to immutable reader snapshots can be obtained by shortening the manager-lock critical section using simpler reader-lifetime management?"*

The research program set out to determine whether fine-grained descriptor leasing (Approach A-min) could capture the concurrency benefits of immutable snapshots (Approach B) across the full factorial operating space of an LSM storage engine. 

The honest scientific answer supported by the current evidence is:
- **In-process writer queue convoying and write starvation are eliminated by shortening the manager lock.**
- **Throughput non-inferiority at the 10% margin ($> 0.90$) is established across the evaluated closed-loop workloads ($N=12$).**
- **Equivalence within $\pm 5\%$ across the full factorial LSM grid is UNRESOLVED and is NOT supported by the data.**

---

## 2. Mandatory Interpretive Corrections

The following five interpretive errors present in earlier draft summaries are formally corrected:

### Correction 1: Historical 3.3×–18.5× Improvement is Approach B vs. Approach A
* **Correction:** The historical 3.3×–18.5× read throughput improvement documented in Phase I (WSL2 ext4) evaluated **Approach B (immutable snapshots) versus Approach A (coarse manager lock)**.
* **Integrity Boundary:** Approach A-min did not exist during Phase I; it was designed, specified, and implemented specifically for Phase I7. Any claim that "Approach A-min achieves 3.3×–18.5× speedups" is factually false and conflates distinct experimental phases.

### Correction 2: Approach A-min Was Never Evaluated Under Rapid Reader Churn
* **Correction:** The 26%–37% throughput degradation under rapid reader-set registration/deregistration churn was observed in Phase I microbenchmarks comparing Approach B against static baselines.
* **Integrity Boundary:** Approach A-min was **never tested under high reader-set churn** in Phase II due to the truncation of Block D2. Whether A-min's atomic descriptor leasing outperforms or underperforms Approach B under extreme churn on Windows NTFS remains completely unknown.

### Correction 3: Deadline Overruns are Treatment-Related Outcomes, Not Exclusions
* **Correction:** The four deadline failures observed in Arm A (`i7_E2_pub_physical_t01_A`, `i7_E2_pub_physical_t09_A`, `i7_E4_L3_t02_A`, and `i7_E4_L3_t06_A`) were caused directly by writer goroutines stalling for 300–640 ms waiting for 16 readers to drain `RLock`.
* **Integrity Boundary:** These overruns are physical manifestations of Approach A's architectural defect under concurrency. They must never be treated as mechanical run failures or dropped from analysis.

### Correction 4: Block E3 Did Not Achieve Equal Work (MC-3 Violation)
* **Correction:** Block E3 was designed to test whether Approach B's penalty persisted when write work was matched. However, Approach A suffered severe writer starvation, completing only 450–1,293 writes (a 45%–78% loss) compared to 2,100–2,351 in Approaches A-min and B.
* **Integrity Boundary:** Block E3 failed preregistered manipulation check MC-3. Because Approach A's writer stalled, its readers experienced substantially lower background write and flush interference. Apparent read throughput advantages in Approach A are artifacts of writer starvation, not read path efficiency.

### Correction 5: Campaign Truncation and Failed Drift Baseline Limit Inference
* **Correction:** Phase II G4 halted at attempt 60 of 330 planned trials under stopping rule S-1 ($N_{\text{fail}} = 10$). Full factorial concurrency scaling ($r=1\dots 32$) was unexecuted.
* **Integrity Boundary:** Startup harness crashes in Block D3 (`unsupported block: D3`) prevented measurement of the primary drift baseline ($D = C_{\text{end}} / C_{\text{start}}$). Campaign-wide thermal and environmental drift over the 26.4-minute run is unmonitored by the primary instrument.

---

## 3. Platform Metadata Reconciliation Against Frozen Manifest

A forensic comparison between draft summaries and the actual immutable [`platform_manifest.json`](file:///c:/Users/ramst/I7_WINDOWS_G4_RESULTS/platform_manifest.json) resolved several discrepancies:

| System Attribute | Draft Summary Claims | Frozen Manifest Value (`platform_manifest.json`) | Status / Implication |
| :--- | :--- | :--- | :--- |
| **CPU Model** | AMD Ryzen 7 5800H (Assumed) | **AMD Ryzen 7 7445HS w/ Radeon 740M Graphics** | Corrected to 7445HS. |
| **Cores / Threads** | 8 cores / 16 threads | **6 Physical Cores / 12 Logical Threads** | Corrected; `GOMAXPROCS=12`. Concurrency $r=16$ exceeds physical core count. |
| **Operating System** | Windows 11 Pro (Build 26100) | **Microsoft Windows 11 Home Single Language (Build 26200)** | Corrected OS edition and build number. |
| **Go Runtime** | Go 1.22.4 windows/amd64 | **go version go1.26.3 windows/amd64** | Corrected compiler toolchain version. |
| **CGO / Race Status** | `CGO_ENABLED=1` / Race unrun | **`CGO_ENABLED=0`**, `race_detector_available = false` | Confirmed Windows binaries compiled without cgo and without `-race`. |
| **Windows Defender** | Not documented | **`defender_realtime = true`** | Real-time protection was active, introducing potential NTFS file I/O latency variance during flushes. |
| **Power Scheme** | High Performance (Assumed) | **Balanced (`381b4222-f694-41f0-9685-ff5bb260df2e`)** | Active power-saving transitions possible during low-activity intervals. |
| **Storage Device** | Samsung NVMe SSD | **SAMSUNG MZVL8512HFLU-00BH1 (512GB, NVMe, NTFS)** | Reconciled model number (Samsung PM9B1). |

---

## 4. Catalog of Unresolved Scientific Hypotheses

The following questions remain unresolved by the existing empirical records and require explicit external review decisions:

| # | Unresolved Question | Why It Remains Unresolved | What Data Would Be Required to Resolve It |
| :---: | :--- | :--- | :--- |
| **U-1** | **Does Approach A-min achieve throughput equivalence with Approach B across the full LSM operating space?** | G4 evaluated only $r=4, 8, 16$ across 5 blocks ($N=12$ closed-loop triplets). Closed-loop 95% CI is $[-7.49\%, +1.00\%]$, which violates the $\pm 5\%$ equivalence band. | Full factorial campaign across reader counts ($r=1, 2, 4, 8, 16, 32$), key distributions, and memtable sizes ($N \ge 30$ triplets per cell). |
| **U-2** | **Does Approach A-min outperform Approach B under high reader-set registration/deregistration churn?** | Block D2 (rapid-churn microbenchmark) was not executed in G4; historical churn data exist only for Phase I (WSL2 ext4). | Controlled microbenchmark on native Windows NTFS evaluating descriptor lease acquisition vs snapshot pointer swapping under $k \ge 32$ churn. |
| **U-3** | **How does Approach A behave under true equal realized write work?** | In Block E3, Approach A suffered 45%–78% write starvation, violating MC-3. True equal-work behavior was never observed. | Paced write harness with adaptive admission control ensuring all arms commit identical transaction counts before measurement. |
| **U-4** | **What is the true open-loop tail latency distribution ($p_{95}, p_{99}$) under high request volumes?** | Block E4 recorded only 53–151 requests per trial, imparting high sample variance on $p_{95}$ and invalidating $p_{99}$. | High-volume open-loop trial campaign ($N \ge 2,000$ requests per trial) with sub-microsecond hardware timestamping. |
| **U-5** | **Does read latency scale linearly with read critical-section scan length?** | Block E5 failed MC-5 because target keys ($\ge 10,001$) hit EOF on the final block, which stayed cached in OS memory. | Multi-table scan workload using keys distributed uniformly across existing on-disk SSTable key boundaries. |
| **U-6** | **Is Approach A-min strictly race-free under Windows thread scheduling?** | Windows binaries were compiled without `-race` due to cgo unavailability. Concurrency safety was verified on Linux and via deterministic unit tests only. | Native Windows execution using a cgo-enabled MinGW/LLVM toolchain with `-race` enabled. |
| **U-7** | **Was the Windows experimental platform subject to thermal or background indexing drift?** | Startup harness crashes in Block D3 omitted the primary drift baseline ($D = C_{\text{end}} / C_{\text{start}}$). Windows Defender was active. | Stable execution on a dedicated server with background indexing disabled and verified start/end calibration drift anchors. |
| **U-8** | **Do findings generalize to production multi-level C++ engines (RocksDB, LevelDB)?** | Evaluated on a single-writer Go LSM engine with in-memory memtable and active OS page cache. Production engines use lock-free memtables, rate limiters, and background compaction threads. | Comparative benchmark against an instrumented C++ LSM storage engine with identical reader-leasing vs snapshot implementations. |

---

## 5. Decision Gate for External Review

The research is at a critical fork:
1. **Option 1 (Manuscript as Evidence-Bounded Report):** Conclude experimental work and present the findings honestly: Approach A's writer-stall convoying is established, Approach A-min provides non-inferiority ($> 0.90$) across the tested closed-loop configurations, but factorial equivalence within $\pm 5\%$ remains unproven due to campaign truncation.
2. **Option 2 (Controlled Confirmatory Follow-Up Campaign):** If equivalence within $\pm 5\%$ or broad factorial scalability is essential to the core conference contribution, execute a bounded, preflighted confirmatory campaign with corrected harness configurations (repairing D3, E3 admission, E5 keyspace, and cgo `-race`).

**This decision belongs exclusively to the external reviewer and will be addressed in Checkpoint R1.2b.** Execution remains strictly stopped.
