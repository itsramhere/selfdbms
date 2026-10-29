# Reproducibility and Provenance Appendix

This document provides complete instructions and provenance records to reproduce all figures, tables, and statistical estimates presented in the manuscript from immutable raw JSON data files.

---

## 1. Frozen Artifacts & Cryptographic Checksums

All experimental data files, runner manifests, and execution ledgers are permanently frozen and cryptographically signed:

| Artifact Description | Archive / File Path | SHA-256 Checksum | Size | Verified Scope |
| :--- | :--- | :--- | :---: | :---: |
| **Phase II: G4 Confirmatory Campaign** | `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\I7_WINDOWS_RESULTS.zip` | `52c52ad137acac08035b0e508acb2707f2abaaa506478f171d32c829fab2723b` | 13,515,151 B | 60 trial records, manifests, logs |
| **Phase II: G3 Calibration V2** | `C:\Users\ramst\I7_WINDOWS_CALIBRATION_V2\I7_G3_CALIBRATION_V2_RESULTS.zip` | `9ca8f1cd73f8b4931b00dafd00f19b16c33fe19cd4a6e98b69be627875423214` | 46,394 B | 9 calibration trials, $C_{\text{ref}}$ |
| **Phase II: G3 Preflight** | `research/experiments/phase_i7/I7_WINDOWS_RESULTS/I7_WINDOWS_RESULTS.zip` | `323b5e5cff58cf4a868f3e68435d1d980cc83d1d93fc403ea47a4c2bf8a48a4e` | 64,812 B | 12 preflight trials |
| **Phase I: WSL2 960-Trial Campaign** | `research/experiments/data/campaign_wsl2_ext4_restart_v1/manifest_checksums.json` | Committed in Git tree | 960 trials | Workloads A, B1, B2, C, D, E |
| **Phase II Master Runlist** | `research/experiments/phase_i7/I7_WINDOWS_RESULTS/runlist.csv` | `0708ca4cb2785650fb1cbecf7cb5ee139e86ee442e0d505447ab15815880e13c` | 81,706 B | 366 rows |
| **Phase II Runner Binary (`i7trial.exe`)** | `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\bin\i7trial.exe` | `493c97869e1507e753ba18d18f14de000478dccf0744d7ce774a5f5cfd825060` | ~12 MB | Evaluated commit `f4cce4b` |
| **Phase II Pebble Binary (`pebble_runner.exe`)** | `C:\Users\ramst\I7_WINDOWS_G4_RESULTS\bin\pebble_runner.exe` | `4c4f3622573a52272e681d5abb3911a32b5ccb5779fa1929794cfb7145fffcd1` | ~15 MB | Pinned Pebble v1.1.5 |

---

## 2. Platform & Hardware Specification

All Phase II experiments were conducted on a single Windows laptop host with the following specifications:

- **Host Name:** `LAPTOP-6O160KFN`
- **Operating System:** Microsoft Windows 11 Home Single Language (64-bit, build 26200)
- **Processor:** AMD Ryzen 7 7445HS with Radeon 740M Graphics (6 physical cores, 12 logical processors, base clock 3.2 GHz)
- **System Memory:** 15.2 GiB DDR5 RAM
- **Primary Storage:** Samsung MZVL8512HFLU-00BH1 NVMe SSD (NTFS filesystem, 4KB cluster size)
- **Go Toolchain:** `go version go1.26.3 windows/amd64`
- **Timer / Clock Source:** Windows `QueryPerformanceCounter` (resolution: $100\text{ ns}$)
- **Process Affinity:** Unbound (process mask `0xfff`, 12 logical cores)

---

## 3. Step-by-Step Reproduction Instructions

To reproduce all manuscript tables, confidence intervals, and figures directly from the frozen JSON records:

### Prerequisites
Ensure Python 3.10+ is installed with `numpy`, `scipy`, and `matplotlib`:
```bash
pip install numpy scipy matplotlib
```

### Execution
Run the master metric reproduction script from the repository root:
```bash
python research/experiments/analysis/reproduce_all_metrics.py
```

### Outputs Generated
1. **JSON Metric Database**: `research/experiments/analysis/i7_reproduced_metrics.json` containing exact point estimates, sample counts, and 95% bootstrap confidence intervals ($B = 10{,}000$ resamples, seed `20261101`).
2. **Manuscript Figures**:
   - `paper/figures/fig_i7_amin_vs_b_paired.png`: Paired read throughput across 18 matched triplets (Approaches A-min vs B).
   - `paper/figures/fig_i7_e3_write_disparity.png`: Write progress disparity under nominal offered pacing rates.
   - `paper/figures/fig_i7_e4_open_loop_p50.png`: Open-loop response latency percentiles across arrival levels.

---

## 4. Methodological & Execution Disclosures

In accordance with scientific integrity guidelines, the following execution conditions and limitations are formally disclosed:

1. **Premature Campaign Halting at 60 Attempts**:
   Phase II planned 330 confirmatory trials across 366 runlist rows. The campaign automatically halted after 60 trial attempts upon triggering the preregistered stopping rule ($N_{\text{fail}} = 10$). Replacements were not consumed (`replacements_used = 0 / 18`).
2. **Missing Start Drift Baseline**:
   Block D3 start trials (trials 1, 2, 3) failed on launch because `"D3"` was omitted from `supportedBlocks` in `cmd/i7trial/main.go`. Consequently, $N_{\text{start}} = 0$, rendering the campaign's start/end drift check mathematically unavailable.
3. **Pebble Runner Schema Mismatch & Buffer Overflow**:
   Pebble trials were flagged `INVALID` due to missing `result.timestamps` in `pebble_runner.exe`. In addition, high throughput ($\sim 1.88\text{M ops/s}$) exhausted the 1M-sample preallocated reader buffer after ~5 seconds, dropping over 10.45 million latency samples (>50% of trial operations). Pebble data are excluded from primary comparisons.
4. **Race Detector Status**:
   Go's race detector requires cgo and a C compiler (GCC/MinGW). The Windows benchmark runners were compiled with `CGO_ENABLED=0` without `-race` to avoid measurement skew. Concurrency correctness and data race freedom were verified independently on Linux via `go test -race ./lsmengine/...`.
5. **Warm-Cache Single-Node Storage**:
   Trial working sets fit comfortably within system RAM and OS page cache. Read latencies reflect in-process and OS cache behavior rather than physical flash access latency.
