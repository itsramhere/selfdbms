# Response to External Reviewer — Phase I7 G4 Forensic Decision

We thank the External Reviewer for the rigorous forensic audit and decisive methodological guidance. In accordance with the Reviewer's directive, **Option A has been adopted: all experimental execution is closed, no additional benchmarks have been run, and the manuscript has been revised to be strictly evidence-bounded.**

Below is our point-by-point response detailing how each mandatory correction was incorporated into the revised manuscript, analysis scripts, and claim-to-evidence matrix.

---

## 1. Compliance with Mandatory Scientific Corrections

### 1. Evidence-Bounded Language
- **Correction:** Replace claims of "completely proven", "airtight", "eliminates starvation", and "proven parity" with evidence-bounded wording.
- **Action Taken:** All occurrences of absolute claims were purged. Phrases have been revised to: "substantially mitigates writer starvation", "demonstrates operational parity within a $\pm 5\%$ practical equivalence band across evaluated cells", "within the evaluated 10-second measurement window", and "verified under the specified single-node storage conditions."

### 2. Treatment of Approach A Deadline Failures
- **Correction:** Distinguish 14 fully successful three-arm G4 triplets from 4 additional matched triplets containing treatment-related Approach A deadline failures. Preserve and report those failures as outcomes.
- **Action Taken:** The 4 failed Approach A trials (`i7_E2_pub_physical_t01_A`, `i7_E2_pub_physical_t09_A`, `i7_E4_L3_t02_A`, `i7_E4_L3_t06_A`) are explicitly preserved and presented as **empirical treatment outcomes**. They illustrate the physical breakdown of coarse reader-writer locking under high reader concurrency ($r=16$) and open-loop arrival rates ($L_3$), where writer flushes stalled behind reader drain delays (300–640 ms hold times), exceeding the 10.0-second window.

### 3. Direct Computation from Raw Archived JSON
- **Correction:** Recompute all manuscript metrics directly from raw archived JSON. Do not copy numerical summaries from earlier review packets without verification.
- **Action Taken:** A standalone Python analysis script (`research/experiments/analysis/reproduce_all_metrics.py`) was implemented to parse the raw JSON attempt records in `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\trials\attempts\*_a01.json` and `calibration/cal_*.json`. Every table, percentile, confidence interval, and plot in the manuscript was generated directly by this script.

### 4. Paired Effect Estimates & Equivalence Bounds for A-min vs B
- **Correction:** Calculate available paired effect estimates, confidence intervals, and preregistered equivalence/noninferiority assessments. Distinguish statistically supported conclusions from inconclusive comparisons. Never interpret failure to reject a difference as proof of equivalence.
- **Action Taken:** Paired differences, ratios, and 95% bootstrap confidence intervals ($B = 10{,}000$ resamples) were computed across all 18 complete triplets ($\bar{d}_{\text{rel}} = -1.65\%$, 95% CI: $[-4.94\%, +1.43\%]$) and across the 14 fully successful triplets ($\bar{d}_{\text{rel}} = -0.90\%$, 95% CI: $[-4.55\%, +2.56\%]$). Because the 95% bootstrap CI falls entirely within the $[-5\%, +5\%]$ equivalence band, practical equivalence is statistically supported for the evaluated aggregate. For individual blocks with wider intervals (e.g., E1, E2), results are explicitly disclosed as inconclusive.

### 5. E2 Publication Controls Reconciliation
- **Correction:** Present E2 physical-versus-no-publication results descriptively unless the relevant repetitions are actually paired. Acknowledge changes in memtable/SSTable topology across the physical-publication window.
- **Action Taken:** E2 results are presented descriptively with actual `latency_read_nanos` percentiles ($p_{50}$ between 156.6 ms and 355.0 ms). The manuscript explicitly notes the dynamic structural topology shift in `pub_physical` (from 4 SSTables + 20 frozen memtables to 24 SSTables + 0 frozen memtables) and acknowledges that read latency reflects both writer lock hold times and structural scan length.

### 6. Relabeling E3 Write Pacing Condition
- **Correction:** Reconcile E3 scheduled, attempted, completed, and late write counts. Describe the pacing condition as matched nominal offered write rate, not equal realized or attempted work.
- **Action Taken:** Block E3 is formally relabeled as **"Matched Nominal Offered Write Rate."** The write accounting table clearly documents that Approach A completed only $1,152\text{–}1,293$ writes in `equal_r01` (vs 2,350–2,351 in A-min/B) and only $450\text{–}454$ writes in `equal_r04` (vs 2,100–2,101 in A-min/B), confirming that coarse locking starved writers and reduced background write interference on readers.

### 7. Exclusion of Pebble and E5 All-Miss Mechanisms
- **Correction:** Exclude Pebble latency comparisons and the unvalidated E5 scan-length mechanism from the primary findings.
- **Action Taken:** Pebble comparisons are excluded from primary evaluation and discussed only in threats to validity (disclosing envelope schema incompatibility and >50% dropped samples due to buffer exhaustion). Block E5 is excluded as an exhaustive scan validation, noting that absent keys generated with index $> 10{,}000$ caused SSTable binary searches to truncate immediately at the final data block index.

### 8. Transparency on Campaign Halting & Execution Environment
- **Correction:** Disclose that G4 terminated after 60/330 attempts, that D3 drift controls were unavailable, that the Windows race detector was not run, and that experiments involved warm-cache reads on one Windows/NTFS laptop.
- **Action Taken:** Explicitly disclosed in Section IV (Experimental Methodology), Section V (Evaluation), and Section VI (Threats to Validity).

### 9. Separation of Historical WSL2 and Windows Datasets
- **Correction:** Describe the historical WSL2/ext4 dataset and the Windows I7 dataset as separate experimental phases. Never pool their performance ratios.
- **Action Taken:** The manuscript structures empirical results into **Phase I (WSL2/ext4, 960 trials)** and **Phase II (Windows 11 NTFS, 60 trials)**. Metrics, speedup ratios, and confidence intervals are reported separately in dedicated subsections and distinct tables.

### 10. Accurate Framing Relative to Prior Work
- **Correction:** Compare synchronization designs with prior work accurately. Do not claim that reference-counted readers, immutable reader versions, or short-lock lifetime protection are fundamentally new inventions.
- **Action Taken:** Section I (Introduction) and Section III (Related Work) explicitly attribute reader versioning and snapshot pinning to LevelDB's `VersionSet`, RocksDB's `SuperVersion`, and classic RCU/leasing literature. The contribution is framed strictly as an empirical architectural dissection comparing descriptor leasing against snapshot pinning in Go.

---

## 2. Frank Disclosure of Strongest Remaining Weaknesses

1. **Truncated Factorial Coverage**: Halting at 60 of 330 planned trials reduces statistical power across multi-dimensional parameter interactions (e.g., varying reader concurrency from 1 to 32 under all write rates). However, 18 complete matched triplets across 5 blocks were preserved.
2. **Missing Longitudinal Drift Control**: Startup failure in Block D3 ($N_{\text{start}} = 0$) means environmental drift across the 26.4-minute run cannot be mathematically bounded by the protocol's primary control.
3. **Single-Host Warm-Cache Storage**: All experiments ran on a single AMD Ryzen 7 laptop on an NTFS NVMe volume with database sizes fitting within OS cache. Findings characterize in-process memory-lock bottlenecks rather than physical disk seek serialization.
4. **Absence of External Baseline**: Pebble data were invalidated by harness buffer exhaustion, leaving the comparison purely internal between Approach A, Approach A-min, and Approach B.
