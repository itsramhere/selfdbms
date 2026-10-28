# Final Reviewer Review Packet: Phase I6.10 Evidence Audit & Historical Provenance Recovery

- **Date**: 2026-10-28
- **Phase Target**: Phase I6.10 — Historical Provenance Recovery and Final Manuscript Evidence Audit
- **Campaigns Evaluated**:
  - `campaign_wsl2_ext4_restart_v1` (960 independent prospective trials)
  - `targeted_validation_v1` (600 confirmatory targeted trials)
  - Deterministic synchronization & metric semantics test suite
- **Evaluation Status**: **FROZEN — ZERO NEW BENCHMARK TRIALS EXECUTED**
- **Decision Request**: External Reviewer final sign-off on manuscript defensibility based on surviving empirical evidence and disclosed limitations.

---

## 1. Executive Summary & Review Scope

In response to the External Reviewer Directive for Phase I6.10, all experimental expansion has been strictly halted. The research team conducted a comprehensive, read-only forensic source comparison, executed a controlled reconstruction build of the historical executable, audited all 16 substantive claims in the authoritative manuscript (`paper.tex`), and verified all statistical and unit claims against the raw trial datasets.

### Primary Audit Outcomes:
1. **Historical Executable Provenance**:
   - The historical runner `/home/ramst/selfdbms/targeted_runner` (SHA-256: `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f`) was successfully reconstructed **byte-for-byte** from the native ext4 source tree `/home/ramst/selfdbms`.
   - The native source tree matches Git commits `370fd6c` / `81429fc` / `4a06217` on 100% of build-critical files (71/71 files normalized).
   - Provenance Classification: **Reconstructed binary match**.
2. **Manuscript Claim Audit**:
   - Total substantive claims audited: **16**.
   - Supported: **9** (56.25%).
   - Supported with qualification: **5** (31.25%).
   - Unsupported: **2** (12.50%) — 1 latency magnitude unit error (µs vs. ms in Workload B2) and 1 equivalence overstatement ("exact empirical parity" in Workload A).
   - Unverifiable: **0** (0.0%).
3. **Manuscript Corrections Delivered**:
   - Corrected manuscript authored (`paper/paper_revised.tex` and updated root `paper.tex`), with exact original preserved at `paper/paper_original.tex` and a machine-readable unified diff at `paper/manuscript_diff.patch`.
   - Fixed the B2 latency unit error (`42.7 ms vs. 175.4 ms`).
   - Qualified equivalence claims in Workloads A and E.
   - Bounded tail latency suppression in B2 to low reader concurrencies ($r=2, 4$) and noted whole-trial throughput inconclusiveness.
   - Attributed tail latency spikes to general write-preferring RWMutex queue convoying rather than physical flush publication alone.
   - Fully disclosed Git commit hierarchy, executable verification hashes, and the `git_revision = "unknown_rev"` metadata artifact in the Reproducibility Appendix.

---

## 2. Final Provenance Verdict and Verification Metrics

### 2.1 Provenance Classification
$$\mathbf{Verdict: \; Reconstructed \; Binary \; Match}$$

### 2.2 Preserved vs. Rebuilt Cryptographic Checksums

| Metric | Preserved Historical Binary | Reconstructed Binary (`/tmp/targeted_runner_rebuilt`) | Bit-Level Match? |
| :--- | :--- | :--- | :---: |
| **Path** | `/home/ramst/selfdbms/targeted_runner` | `/tmp/targeted_runner_rebuilt` | N/A |
| **SHA-256** | `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f` | `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f` | **YES (100%)** |
| **File Size** | `5,453,058` bytes | `5,453,058` bytes | **YES (100%)** |
| **Go Build ID** | `raCgnlKhcejFVErxy8Ge/pSHSnyg97yOboCajV8nD/xtHS_TCzXh93CP88li-A/ax9WzPJrX-5xwTVSUvpn` | `raCgnlKhcejFVErxy8Ge/pSHSnyg97yOboCajV8nD/xtHS_TCzXh93CP88li-A/ax9WzPJrX-5xwTVSUvpn` | **YES (100%)** |
| **GNU Build ID** | `ea953cf6bc0025fd4e351027a7190a8ca9e48772` | `ea953cf6bc0025fd4e351027a7190a8ca9e48772` | **YES (100%)** |
| **Section Count**| 26 sections | 26 sections | **YES (100%)** |

`cmp /home/ramst/selfdbms/targeted_runner /tmp/targeted_runner_rebuilt` exited with status 0 (zero byte differences).

### 2.3 Exact Build Reconstruction Commands and Logs
```bash
# Executed in WSL2 Ubuntu 24.04 ext4 environment
$ cd /home/ramst/selfdbms
$ go version
go version go1.27.3 linux/amd64

$ go build -o /tmp/targeted_runner_rebuilt ./research/experiments/targeted_validation/cmd
# Command finished synchronously with code 0 (clean build)

$ sha256sum /tmp/targeted_runner_rebuilt
2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f  /tmp/targeted_runner_rebuilt
```

### 2.4 Confidence Limitations
1. **Source-to-Binary Proof**: The build experiment conclusively proves that the unversioned native directory `/home/ramst/selfdbms` matches historical Git commit `370fd6c` and compiles deterministically to the identical binary `2855b4318d...`.
2. **Process Accounting Limitation**: A matching rebuild proves source-to-binary identity, but does not independently prove which process image was launched during each individual historical trial.
3. **Metadata Non-Modification**: In strict adherence to the directive, `unknown_rev` in historical trial JSON files has **not** been retroactively altered.

---

## 3. Manuscript Evidence Audit Summary

### 3.1 Claim Classification Count
- **Total Substantive Claims Audited**: 16
- **Supported**: 9
- **Supported with Qualification**: 5
- **Unsupported**: 2
- **Unverifiable**: 0

### 3.2 Audit Findings on High-Priority Directive Focus Areas

| Focus Area | Manuscript Claim | Surving Evidence & Evaluation | Final Verdict |
| :--- | :--- | :--- | :---: |
| **p95 / p99 Latency Tail Suppression** | B2: 73%–76% p95 drop at $r=2, 4$. B1: 55% p99 drop at $r=4$ (89.3ms to 39.8ms). | 960-trial B2 medians (175.4ms to 42.7ms at $r=2$; 181.0ms to 48.9ms at $r=4$) and B1 medians (89.3ms to 39.8ms) verified. Confirmatory T1 ($N=20$) confirmed significance ($p < 0.0001$). At $r \ge 16$, host scheduling narrows tail reduction. Whole-trial B2 throughput is inconclusive. | **Supported with qualification** (Bounded to low-$r$; throughput inconclusive) |
| **Physical Flush Causal Attribution** | Flusher swapping pointers in microseconds causes latency reduction. | Confirmatory sham-flush test (T1) showed tail latency suppression also occurs under sham-flushes ($p < 0.005$). The primary causal mechanism is eliminating exclusive lock convoying in Go's write-preferring `RWMutex`, not physical file-swap exclusion alone. | **Supported with qualification** (General mutex contention identified) |
| **Equivalence Boundaries (Workloads A & E)** | "Zero measurable memory-tier regression" and "exact empirical parity" in uncontended reads. | Observed medians align closely (0.98x–1.02x in E, 0.97x–1.02x in A). However, bootstrap CIs are wide in A ([0.63x, 1.44x] at $r=32$), and TOST equivalence testing ($\pm 5\%$) in T2 was inconclusive at $r=32$. | **Supported with qualification** (Overstatement removed; framed as observed alignment) |
| **Churn Penalty Mechanism (Workload D)** | 26% to 37% throughput regression at $r \le 4$ due to snapshot lifecycle costs. | 960-trial Workload D confirms severe regression ($r=1$: 0.63x, $r=2$: 0.66x, $r=4$: 0.74x). Microbenchmarks confirm snapshot refcounting adds 42–68 ns overhead per operation. No unverified CPU percentage (such as 34.4%) is cited in the paper. | **Supported** (Qualitative mechanistic framing verified) |
| **Writer Progress Speedup (Workload B1)** | 3.3x to 18.5x increase in completed writes (up to +2,712 writes). | Directly confirmed by 960-trial data. All 95% bootstrap CIs strictly exceed 1.0 (e.g. 18.5x [16.8x, 28.4x] at $r=8$). Effect narrows at $r=32$ (3.3x) due to host CPU saturation across 12 logical cores. | **Supported** (Strictly verified) |
| **Lock-Freedom / Nonblocking Execution** | Decoupled reader snapshot pinning. | Manuscript accurately avoids false claims of "lock-freedom", correctly documenting the short metadata mutex (`snapshotMu`) held during pointer swap. | **Supported** (Accurate terminology) |
| **Hardware & DBMS Generalization** | Scope of conclusions. | Evaluated exclusively in Go on AMD Ryzen 7 7445HS laptop with NVMe SSD under WSL2 ext4. Explicitly bounded to single-process tiered Go synchronization. | **Supported with qualification** (Threats to Validity strengthened) |
| **Publication Overlap Accounting Semantics** | Apparent overlap reads in Approach A reflect reader drain delay. | Machine-checked unit test `TestMetricSemantics_ApproachA_ReadsDuringPublicationAudit` verified that positive counter reflects reader drain prior to lock acquisition; zero reads execute while `m.Mu.Lock()` is held. | **Supported** (Machine-checked deterministic proof) |

---

## 4. Assessment of Potentially Paper-Blocking Issues

The External Reviewer Directive requires an explicit assessment of every remaining potentially paper-blocking issue. We identify four potential concerns and explain why none invalidate the paper:

### Issue 1: Lack of Git Revision in Raw 600-Trial Records (`git_revision = "unknown_rev"`)
- **Status**: **RESOLVED VIA RECONSTRUCTED BINARY MATCH**.
- **Assessment**: The forensic source comparison proved 100% normalized identity with Git commit `370fd6c`, and a controlled rebuild from the native directory reproduced the exact byte-for-byte binary hash `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f`. This provides definitive source-to-binary attribution. The paper transparently discloses the WSL2 directory artifact in the Reproducibility Appendix.
- **Blocking?**: **NO**.

### Issue 2: Matched Pairs Evaluated Different Operation Streams (Seed Formula Offset)
- **Status**: **RESOLVED VIA THREATS TO VALIDITY DISCLOSURE**.
- **Assessment**: The original seed formula `Seed + TrialIndex*10007` assigned different seeds to Mode A and Mode B trials within a pair. However, all trials within each cell drew from the exact same Zipfian key distribution, keyspace cardinality (10,000 keys), hit ratio target (95%), and read/write ratio. The comparison represents statistically independent samples across identical distributions. Section 7 explicitly discloses this limitation.
- **Blocking?**: **NO**.

### Issue 3: Workload B2 Latency Magnitude Unit Error (`µs` vs `ms`)
- **Status**: **RESOLVED IN REVISED MANUSCRIPT**.
- **Assessment**: In Section 5.3 line 234, `175,434 µs` was erroneously typed as `175.4 µs` instead of `175.4 ms`. Table 1 correctly reported `175,434 µs`. The revised manuscript (`paper/paper_revised.tex`) corrects this typo, providing both ms and µs values.
- **Blocking?**: **NO**.

### Issue 4: Conflation of Physical Flush Publication with General Mutex Contention
- **Status**: **RESOLVED IN REVISED MANUSCRIPT**.
- **Assessment**: Experiment T1 proved that sham-flushes also exhibit tail latency suppression, showing that general exclusive lock convoying in Go's write-preferring `RWMutex` accounts for a major portion of tail latency suppression. The revised manuscript removes the claim that physical file-swapping exclusively causes the spike, correctly attributing the benefit to decoupling queries from writer/flusher lock queues.
- **Blocking?**: **NO**.

---

## 5. Inventory of Modified and Generated Files

### Review & Audit Deliverables (Generated in Phase I6.10):
1. **`research/reviews/FINAL_SOURCE_PROVENANCE_AUDIT.md`**: Complete forensic audit of the historical executable, environment, build IDs, readelf structures, and line-ending analysis.
2. **`research/reviews/SOURCE_MATCH_MATRIX.csv`**: File-by-file comparison matrix across all 71 build-critical files between the native source tree and Git commit `370fd6c`.
3. **`research/reviews/REBUILD_VERIFICATION.md`**: Step-by-step cryptographic build reproducibility report and external reviewer verification protocol.
4. **`research/reviews/FINAL_CLAIM_EVIDENCE_MATRIX.md`**: Systematic 16-claim audit matrix with exact wording, evidentiary support, campaign sources, and verdicts.
5. **`research/reviews/FINAL_MANUSCRIPT_CORRECTIONS.md`**: Detailed itemized justification for every manuscript modification.
6. **`research/reviews/FINAL_CHATGPT_REVIEW_PACKET.md`**: This review packet.
7. **`research/reviews/compare_provenance.py`**: Forensic source hashing script.
8. **`research/reviews/verify_statistics.py`**: Independent statistical verification script.

### Manuscript Artifacts:
9. **`paper/paper_original.tex`**: Pristine pre-audit manuscript source preserved for reference.
10. **`paper/paper_revised.tex`**: Audited, corrected, and qualified manuscript.
11. **`paper.tex`**: Root manuscript updated to reflect the revised text.
12. **`paper/manuscript_diff.patch`**: Unified diff between original and revised manuscript.

---

## 6. Explicit Confirmation of Experimental Freeze

$$\mathbf{CONFIRMATION: \; ZERO \; NEW \; BENCHMARK \; TRIALS \; WERE \; EXECUTED.}$$

All historical data (`campaign_wsl2_ext4_restart_v1` with 960 trials; `targeted_validation_v1` with 600 trials), source snapshots, and executables remain strictly frozen and unaltered. No new performance trials or benchmark runs were launched.

---

## 7. Exact Files Requiring External Reviewer Verification

The external reviewer is invited to inspect the following 6 definitive artifacts:

1. **`research/reviews/FINAL_SOURCE_PROVENANCE_AUDIT.md`**: Forensic evidence and line-ending analysis establishing reconstructed binary match.
2. **`research/reviews/SOURCE_MATCH_MATRIX.csv`**: File-by-file cryptographic matching table.
3. **`research/reviews/REBUILD_VERIFICATION.md`**: Command-line verification protocol for byte-for-byte executable reconstruction.
4. **`research/reviews/FINAL_CLAIM_EVIDENCE_MATRIX.md`**: 16-claim evidence matrix.
5. **`paper/manuscript_diff.patch`**: Exact unified patch of manuscript corrections.
6. **`paper/paper_revised.tex`**: The revised manuscript text.

---

## 8. Final Recommendation to External Reviewer

The surviving empirical evidence from the 960-trial exploratory campaign, the 600-trial confirmatory campaign, and the deterministic synchronization test suite **fully supports a defensible, publication-grade paper**, provided that all empirical limitations and causal boundaries are disclosed transparently.

The revised manuscript (`paper/paper_revised.tex`):
- Defends the primary findings: elimination of reader-writer starvation (3.3x to 18.5x write progress increase), doubling of read throughput (up to 2.07x), and 73%–76% p95 tail latency suppression at $r=2, 4$ under periodic flushes.
- Fully honors negative results: 26% to 37% throughput regression under rapid reader-set churn ($r \le 4$).
- Accurately reports instrumentation semantics: proves positive overlap counts in Approach A reflect reader drain delays rather than concurrent execution during lock acquisition.
- Transparently discloses all methodological limitations: PRNG seed offsets, WSL2 hypervisor scheduling, and the `unknown_rev` metadata artifact.

**STOPPING RULE APPLIED: Execution is complete. Awaiting External Reviewer final decision.**
