# Phase I7 — Checkpoint R1.1: Independent Scientific Audit Handoff

**DATE:** 2026-10-29  
**EVALUATED GIT REVISION:** `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`  
**RESULTS ARCHIVE SHA-256:** `52c52ad137acac08035b0e508acb2707f2abaaa506478f171d32c829fab2723b`  
**STATUS:** RESEARCH DRAFT — EVIDENCE AUDIT ONLY. EXPERIMENTS CLOSED.

---

## 1. Executive Summary & Research Question

This document accompanies the Checkpoint R1.1 evidence handoff for the LSM-tree reader-writer synchronization research program. In strict adherence to scientific rigor, the revised manuscript is treated as a **working research draft**, not a collection of accepted claims.

### Core Research Question
> *"How much of the concurrency benefit attributed to immutable reader snapshots can be obtained by shortening the manager-lock critical section using simpler reader-lifetime management?"*

### Primary Assessment
- **Approach A (Coarse Manager RWMutex):** Reader-lock holding during table and index traversal directly causes writer queue convoying and flush publication stalls (300–640 ms per event under 16 concurrent readers). This bottleneck produces physical deadline failures in E2 (1–3 late flushes) and E4 (5–10 late flushes, 3–4 abandoned requests), and 45%–78% write progress suppression in E3. **This failure mode is ESTABLISHED.**
- **Approach A-min (Descriptor Leasing):** Shortening the manager-lock critical section to sub-microsecond atomic counter and pointer updates eliminates Approach A's writer convoying, achieving 20/20 in-window flushes in E2 and E4, and 100% write completion in E3. Across all 18 complete experimental triplets in Phase II, A-min matches Approach B within a 95% bootstrap confidence interval of $[-4.94\%, +1.43\%]$ (mean relative difference $-1.65\%$, median ratio $1.0000$). **Parity across the observed aggregate is ESTABLISHED, but equivalence across the full factorial operating grid is UNRESOLVED due to G4 stopping-rule truncation.**
- **Approach B (Immutable Snapshots):** Eliminates writer interference via atomic snapshot pointer swapping, but incurs memory reclamation overhead and metadata lifecycle costs that degraded throughput by 26%–37% under rapid reader-set churn in Phase I.

---

## 2. Experimental Campaign Status & Trial Ledger

Experimental execution was halted at Gate G4 in strict accordance with prospective stopping rule S-1 ($N_{\text{fail}} = 10$). All 60 executed trials were preserved unchanged in `C:\Users\ramst\I7_WINDOWS_G4_RESULTS`.

```
Total Attempted Trials: 60 (18.2% of 330 planned)
├── Successful Trials: 50
├── Treatment-Related Deadline Outcomes (Arm A): 4
│   ├── i7_E2_pub_physical_t01_A (1 late flush)
│   ├── i7_E2_pub_physical_t09_A (3 late flushes)
│   ├── i7_E4_L3_t02_A (10 late flushes, 3 abandoned requests)
│   └── i7_E4_L3_t06_A (5 late flushes, 4 abandoned requests)
├── Mechanical Failures (Block D3 Runner Omission): 3
│   └── i7_D3_start_t01_A, AMIN, B (runner exited: 'unsupported block: D3')
└── Protocol-Invalid Trials (Pebble Envelope Divergence): 3
    └── i7_E7_Y1_t05, Y2_t01, Y3_t03 (missing result.timestamps; buffer exhausted)
```

### Complete Matched Triplet Accounting
Across the 50 successful trials and 4 treatment-related outcomes, exactly **18 complete three-arm triplets** exist across Blocks E1, E2, E3, E4, and E5 sharing identical logical request seeds and workload parameters:
- **14 Fully Successful Triplets:** All three arms (A, A-min, B) completed with status `SUCCESS`.
- **4 Treatment-Failure Triplets:** Arms A-min and B completed with status `SUCCESS`, while Arm A produced a treatment-related deadline failure.
- **Arm A-min vs. Arm B Paired Comparisons:** Valid across all **18 triplets**, as Arms A-min and B completed successfully in every executed triplet.

---

## 3. Platform & Phase Separation (Unpooled Analysis)

In accordance with preregistration principle 1.1, data from the two experimental phases are strictly separated:

| Dimension | Phase I (Historical WSL2) | Phase II (Confirmatory I7) | Pooling Status |
| :--- | :--- | :--- | :--- |
| **Operating System** | Linux 5.15 (Ubuntu 22.04 on WSL2) | Windows 11 Pro 64-bit (Build 26100) | **STRICTLY UNPOOLED** |
| **File System** | ext4 (virtualized VHDX) | NTFS (native volume) | **STRICTLY UNPOOLED** |
| **Timer Primitive** | `CLOCK_MONOTONIC_RAW` (nanosecond) | `QueryPerformanceCounter` (100 ns) | **STRICTLY UNPOOLED** |
| **Compiler / Runtime** | Go 1.22.4 (linux/amd64, `-race` active) | Go 1.22.4 (windows/amd64, no `-race`) | **STRICTLY UNPOOLED** |
| **Storage Device** | Virtualized ext4 backing store | Physical Samsung PM9B1 NVMe SSD | **STRICTLY UNPOOLED** |
| **Evaluated Arms** | Approach A vs. Approach B | Approach A vs. Approach A-min vs. Approach B | **SEPARATE FINDINGS** |
| **Trial Volume** | 960 exploratory trials | 60 confirmatory trial attempts | **NO COMBINED EFFECT** |

---

## 4. Master Evidence Index & Archive Integrity

All primary evidence is stored in immutable, cryptographically hashed archives outside the git working tree:

| Artifact Name | Storage Path | SHA-256 Checksum | Description |
| :--- | :--- | :--- | :--- |
| **G4 Windows Results ZIP** | `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\I7_WINDOWS_RESULTS.zip` | `52c52ad137acac08035b0e508acb2707f2abaaa506478f171d32c829fab2723b` | 60 trial JSONs, work specs, campaign manifest, checksums |
| **G3 Calibration V2 ZIP** | `C:\Users\ramst\I7_WINDOWS_CALIBRATION_V2\I7_G3_CALIBRATION_V2_RESULTS.zip` | `9ca8f1cd73f8b4931b00dafd00f19b16c33fe19cd4a6e98b69be627875423214` | 9 accepted calibration trial records ($C_{\text{ref}} = 16.2\text{ reads/s}$) |
| **G3 Preflight ZIP** | `research/experiments/phase_i7/I7_WINDOWS_RESULTS/I7_WINDOWS_RESULTS.zip` | `323b5e5cff58cf4a868f3e68435d1d980cc83d1d93fc403ea47a4c2bf8a48a4e` | 12 accepted preflight trials on Windows |
| **Phase I Historical ZIP** | `research/experiments/data/campaign_wsl2_ext4_restart_v1.zip` | *(documented in provenance)* | 960 WSL2 ext4 trial records |
| **Master Reproduction Script** | `research/experiments/analysis/reproduce_all_metrics.py` | `02140f2d1e94bc02d172f89dc23df11f08a8c16927502f9a80125b6efa7649ce` | Standalone script reproducing all tables and figures |
| **Recomputed Metrics JSON** | `research/experiments/analysis/i7_reproduced_metrics.json` | `63ac4ab0ef51ac24027ecaee50085474a26dc9dcad1cfed3ab34c266223a9ae1` | Recomputed metrics across all 60 trials |
| **G4 Forensic Audit** | `I7_G4_FORENSIC_AUDIT.md` | *(in package)* | Independent forensic audit of the 60 G4 attempts |
| **Preregistered Plan** | `research/experiments/phase_i7/07_ANALYSIS_PLAN.md` | *(in package)* | Preregistered estimands, stopping rules, and margins |

---

## 5. Summary of High-Priority Scientific Gaps

Before advancing to research conclusions or considering additional experiments, eight critical issues must be evaluated:

1. **Equivalence vs. Similarity (A-min vs. B):**
   While all 18 triplets pooled yield a 95% CI of $[-4.94\%, +1.43\%]$ inside $\pm 5\%$, individual sub-blocks ($n=1, 4, 7, 5, 1$) lack statistical power to confirm cell-specific noninferiority. Equivalence across the unexecuted factorial space is not proven.
2. **Causal Mechanism Isolation:**
   While Approach A's writer-stall mechanism is causally demonstrated, Approach A-min's performance envelope under high descriptor lease churn ($k \ge 32$) was not tested in Phase II.
3. **Block E3 Work Violation (MC-3):**
   Preregistered manipulation check MC-3 failed. Paced write progress in Approach A collapsed by 45%–78% due to lock contention. Read throughput was elevated in Approach A as an artifact of lower write interference, rendering "equal work" comparisons void.
4. **Campaign Truncation (60 / 330 Attempts):**
   Stopping at 60 trials truncated factorial concurrency scaling ($r=1\dots 32$). The data evaluate fixed thread configurations ($r=4, 8, 16$) rather than broad scalability.
5. **Omitted Drift Baseline (Block D3):**
   Failure of initial D3 trials leaves environmental drift over the 26.4-minute campaign unbounded by the primary preregistered control.
6. **E4 Small-Sample Tail Latency:**
   Low request counts ($N = 53\dots 151$) in Block E4 impart high estimation variance on $p_{95}$ and render $p_{99}$ statistically invalid.
7. **Block E5 All-Miss Cache Artifact (MC-5):**
   Absent keys ($\ge 10,001$) resolved immediately to the last SSTable block due to keyspace mismatch ($0\dots 9,999$), pinning a single 4KB block in OS page cache. E5 does not validate multi-level disk scanning.
8. **External Engine Validity:**
   Findings reflect in-process Go synchronization bottlenecks in a compact storage engine on Windows NTFS. Generalization to C++ engines (RocksDB) or multi-level disk I/O architectures requires experimental qualification.
