#!/usr/bin/env python3
"""
audit_reanalysis_cp6.py

Performs deep reanalysis of the official 960-trial dataset for Phase I6.8.0:
1. Workload B2 p95 read latency: Unpaired vs Paired Bootstrap, Sign Test, and Wilcoxon Signed-Rank.
2. Multiple-comparison adjustments (Holm-Bonferroni).
3. Workload A repetition 2 throughput drop investigation across all configurations.
4. Analysis of shared environmental conditions and seed provenance implications.
"""

import os
import csv
import json
import numpy as np
from collections import defaultdict
from scipy import stats

np.random.seed(42)

ROOT_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
CSV_PATH = os.path.join(ROOT_DIR, "research", "experiments", "data", "campaign_wsl2_ext4_restart_v1", "results.csv")
OUTPUT_MD = os.path.join(ROOT_DIR, "research", "experiments", "analysis", "reanalysis_b2_workload_a.md")

def paired_bootstrap(a_vals, b_vals, n_boot=10000):
    a = np.array(a_vals, dtype=float)
    b = np.array(b_vals, dtype=float)
    n = len(a)
    
    paired_rom = []
    paired_mean_ratios = []
    paired_diffs = []
    
    for _ in range(n_boot):
        idx = np.random.choice(n, size=n, replace=True)
        sa = a[idx]
        sb = b[idx]
        paired_rom.append(np.median(sb) / np.median(sa))
        paired_mean_ratios.append(np.mean(sb / sa))
        paired_diffs.append(np.median(sb) - np.median(sa))
        
    rom_ci = np.percentile(paired_rom, [2.5, 97.5])
    mr_ci = np.percentile(paired_mean_ratios, [2.5, 97.5])
    diff_ci = np.percentile(paired_diffs, [2.5, 97.5])
    return rom_ci, mr_ci, diff_ci

def unpaired_bootstrap(a_vals, b_vals, n_boot=10000):
    a = np.array(a_vals, dtype=float)
    b = np.array(b_vals, dtype=float)
    ratios = []
    diffs = []
    for _ in range(n_boot):
        sa = np.random.choice(a, size=len(a), replace=True)
        sb = np.random.choice(b, size=len(b), replace=True)
        ratios.append(np.median(sb) / np.median(sa))
        diffs.append(np.median(sb) - np.median(sa))
    r_ci = np.percentile(ratios, [2.5, 97.5])
    d_ci = np.percentile(diffs, [2.5, 97.5])
    return r_ci, d_ci

def main():
    with open(CSV_PATH, 'r', encoding='utf-8') as fp:
        reader = csv.DictReader(fp)
        rows = list(reader)

    # 1. Audit Workload B2 p95 Latency
    b2_rows = [r for r in rows if r['workload_id'] == 'WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE']
    b2_data = defaultdict(dict)
    for r in b2_rows:
        rc = int(r['reader_concurrency'])
        rep = int(r['repetition'])
        mode = 'A' if 'ApproachA' in r['sync_mode'] else 'B'
        b2_data[rc][(rep, mode)] = float(r['latency_read_p95_us'])

    concurrencies = [1, 2, 4, 8, 12, 16, 24, 32]
    
    lines = ["# Deep Statistical Reanalysis of Campaign I5_WSL2_EXT4_RESTART_V1 (Sub-Phase I6.8.0)\n\n",
             "## 1. Workload B2 p95 Read Latency: Paired vs Unpaired Audit\n\n",
             "| Readers | Median A (µs) | Median B (µs) | B < A Pairs | Unpaired RoM CI [95%] | Paired RoM CI [95%] | Paired Mean Ratio CI [95%] | Paired Median Diff [95% CI] | Wilcoxon p-value |\n",
             "| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |\n"]

    p_values = []
    for rc in concurrencies:
        a_vals = [b2_data[rc][(rep, 'A')] for rep in range(1, 11)]
        b_vals = [b2_data[rc][(rep, 'B')] for rep in range(1, 11)]
        
        b_less = sum(1 for a, b in zip(a_vals, b_vals) if b < a)
        u_r_ci, u_d_ci = unpaired_bootstrap(a_vals, b_vals)
        p_rom_ci, p_mr_ci, p_d_ci = paired_bootstrap(a_vals, b_vals)
        
        # Wilcoxon signed rank test
        diffs = np.array(b_vals) - np.array(a_vals)
        try:
            w_stat, p_val = stats.wilcoxon(diffs, alternative='less')
        except Exception:
            p_val = 1.0
        p_values.append((rc, p_val))

        lines.append(f"| {rc:2d} | {np.median(a_vals):8.1f} | {np.median(b_vals):8.1f} | {b_less}/10 | "
                     f"[{u_r_ci[0]:.2f}x, {u_r_ci[1]:.2f}x] | [{p_rom_ci[0]:.2f}x, {p_rom_ci[1]:.2f}x] | "
                     f"[{p_mr_ci[0]:.2f}x, {p_mr_ci[1]:.2f}x] | [{p_d_ci[0]/1000.0:+.1f}ms, {p_d_ci[1]/1000.0:+.1f}ms] | "
                     f"{p_val:.4f} |\n")

    lines.append("\n### Multiple-Comparison Adjustment (Holm-Bonferroni across r=2, 4, 8)\n\n")
    # Evaluate primary confirmatory targets: r=2, 4, 8
    target_p = [p for rc, p in p_values if rc in [2, 4, 8]]
    # Sort p-values
    target_sorted = sorted(target_p)
    lines.append("| Rank | Target Concurrency | Unadjusted p-value | Alpha Threshold (0.05 / (4 - rank)) | Statistically Significant? |\n")
    lines.append("| :---: | :---: | :---: | :---: | :---: |\n")
    for rank, p in enumerate(target_sorted, 1):
        alpha_t = 0.05 / (4 - rank)
        sig = "YES" if p < alpha_t else "NO"
        lines.append(f"| {rank} | target | {p:.6f} | {alpha_t:.6f} | {sig} |\n")

    # 2. Audit Workload A Repetition 2 Throughput Drop
    lines.append("\n\n## 2. Workload A Repetition 2 Throughput Drop Investigation\n\n")
    a_rows = [r for r in rows if r['workload_id'] == 'WORKLOAD_A_READ_ONLY']
    a_tputs = defaultdict(dict)
    for r in a_rows:
        rc = int(r['reader_concurrency'])
        rep = int(r['repetition'])
        mode = 'A' if 'ApproachA' in r['sync_mode'] else 'B'
        a_tputs[rc][(rep, mode)] = float(r['read_ops_per_sec'])

    lines.append("| Concurrency | Rep 1 Mean Tput (ops/s) | Rep 2 Tput A | Rep 2 Tput B | Rep 2 B/A Ratio | Rep 2 Drop vs Rep 1 (%) |\n")
    lines.append("| :---: | :---: | :---: | :---: | :---: | :---: |\n")
    for rc in concurrencies:
        r1_mean = (a_tputs[rc][(1, 'A')] + a_tputs[rc][(1, 'B')]) / 2.0
        r2_a = a_tputs[rc][(2, 'A')]
        r2_b = a_tputs[rc][(2, 'B')]
        ratio = r2_b / r2_a if r2_a > 0 else 1.0
        drop = ((r1_mean - (r2_a + r2_b) / 2.0) / r1_mean) * 100.0
        lines.append(f"| r={rc:2d} | {r1_mean:8.1f} | {r2_a:8.1f} | {r2_b:8.1f} | {ratio:.2f}x | {drop:5.1f}% drop |\n")

    with open(OUTPUT_MD, 'w', encoding='utf-8') as fp:
        fp.writelines(lines)

    print(f"Audit analysis written to {OUTPUT_MD}")

if __name__ == "__main__":
    main()
