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


## 5. Seventh campaign: X-followup-v1

This campaign supplements the six corrected-reader campaigns in `paper.tex`; the older Phase I/II artifacts above remain historical and are not pooled. Protocol: `research/experiments/phase_i7/X_FOLLOWUP_PREREGISTRATION.md`. Completion records: `b19ee36` on `research/x-phase3`. The measured runner used `fc7998c`, plan prefix `394675b73698`, runner prefix `6d065832f639`, master seed `20261108`, and a 20 s pause. Seven cells supplied 150 retained first-attempt successes; gaps were 20.0 to 22.5 s. Seven mechanical smoke trials and an aborted start (13 completed, a 14th killed) were excluded; the latter was set aside unread and restarted with the same plan, seeds and runner.

Regenerate the gitignored results JSON from the committed raw records, then the manuscript outputs, from the repository root (analysis only, no trials):

```bash
python research/experiments/analysis/x_followup_analyze.py --data-dir research/experiments/phase_i7/X_READCOST/followup --output-dir research/experiments/phase_i7/X_READCOST/analysis_followup
python research/experiments/analysis/x_followup_paper_outputs.py --results research/experiments/phase_i7/X_READCOST/analysis_followup/x_followup_results.json --data-dir research/experiments/phase_i7/X_READCOST/followup --macros-out paper/generated/followup.tex --figures-dir paper/figures/x_readcost
python -m unittest discover -s research/experiments/analysis -p "test_x_followup*.py"
python paper/check_revision_numbers.py --verify
```

Outputs are `paper/generated/followup.tex`, `paper/figures/x_readcost/fig_followup_reads.pdf`, and `fig_read_cdf.pdf`, with PNG previews. The paper generator checks retained raw trial metrics against `analysis_followup/x_followup_results.json`, copies the frozen confirmatory estimates, computes descriptive paired 90% log-t intervals from raw trials, and pools F4 histogram mass at bucket lower bounds. For F3 against F1 at 732 writes/s, it records each round's log read gain, log free-share gain and difference in generated TeX comments, and a 90% paired-t CI on that difference. Cumulative trial count adds this campaign to the prior `SettledTotalTrials` macro in `paper/generated/settled.tex`. PDF timestamps are suppressed, paths and wall-clock times are absent from outputs, and repeated generation must be byte-identical.

Capacity scaling in the new prose is cell mean reads divided by (each arm's median F3 reads times cell mean free share), rather than the mean of per-trial ratios. Slow-group medians are the arithmetic mean of each trial's conditional median, at recorded bucket lower bounds. The 549 writes/s read interval's upper bound is 1.1084820951208332 and rounds directly to 1.108; the requested 1.109 is not reproduced. No data or frozen analyzer is changed. H-F1 failed, H-F2 and H-F4 held, and H-F3 was only partly supported; no mechanism is offered for the less-than-proportional read decline.


## 6. Conference paper and extended report

`paper.tex` is the conference paper (no appendix) and `paper_extended.tex` is the extended report (it inputs `paper/appendix.tex`). Both read macros and figures from `paper/generated/` and `paper/figures/x_readcost/` relative to the repository root, so build from the root.

Regenerate the main-paper macros and `fig_penalty_no_legacy.pdf` from retained results only (analysis, no trials; running it twice gives byte-identical `mainpaper.tex`):

```bash
python research/experiments/analysis/x_main_paper_outputs.py
```

It reads `analysis/x_readcost_results.json`, `analysis/mechanism.json`, `analysis_intervention/x_intervention_results.json`, `analysis_followup/x_followup_results.json` with the raw `followup/trials`, `analysis_settled/x_settled_results.json`, and the `platform.json` of the seven campaigns (all under `research/experiments/phase_i7/X_READCOST/`), and stops if a statement the prose relies on stops holding. Defaults can be overridden; see `--help`.

Check and test, from the repository root:

```bash
python paper/check_revision_numbers.py
python paper/check_revision_numbers.py --verify
python -m unittest discover -s research/experiments/analysis -p "test_x_main_paper_outputs.py"
```

Build both documents with `pdflatex` (two or three passes for references), writing auxiliary files outside the repository. Under WSL, from the repository root:

```bash
mkdir -p /tmp/build_paper /tmp/build_ext
pdflatex -interaction=nonstopmode -halt-on-error -output-directory=/tmp/build_paper paper.tex
pdflatex -interaction=nonstopmode -halt-on-error -output-directory=/tmp/build_paper paper.tex
pdflatex -interaction=nonstopmode -halt-on-error -output-directory=/tmp/build_ext paper_extended.tex
pdflatex -interaction=nonstopmode -halt-on-error -output-directory=/tmp/build_ext paper_extended.tex
```

`paper.tex` must stay at 10 IEEE conference pages or fewer including references (8 at the last build). `paper_extended.tex` was 26 pages. The Linux section of `paper.tex` is behind `\withlinuxtrue`; leave the default `\withlinuxfalse` until native Linux results exist, and then fill the guarded block from `xcampaign -design settled` and `-design followup` runs on that host, in separate output folders.
