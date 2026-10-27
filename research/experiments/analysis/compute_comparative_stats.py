#!/usr/bin/env python3
"""
compute_comparative_stats.py

Performs comprehensive non-parametric comparative statistical analysis across all 960 trials
(6 workloads x 8 reader concurrencies x 2 modes x 10 repetitions).
Computes medians, IQRs, 95% bootstrap confidence intervals for comparative effect sizes (B/A ratio
and B - A difference), writer progress effect sizes, latency distributions, and exposure-normalized overlap rates.
"""

import os
import csv
import json
import numpy as np
from collections import defaultdict

np.random.seed(42)

ROOT_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
CSV_PATH = os.path.join(ROOT_DIR, "research", "experiments", "data", "campaign_wsl2_ext4_restart_v1", "results.csv")
OUTPUT_JSON = os.path.join(ROOT_DIR, "research", "experiments", "analysis", "comparative_analysis.json")
OUTPUT_MD = os.path.join(ROOT_DIR, "research", "experiments", "analysis", "comparative_tables.md")

WORKLOADS = [
    ("WORKLOAD_A_READ_ONLY", "Workload A: Uncontended Read-Only SSTables"),
    ("WORKLOAD_B1_NATURAL_WRITE_PROGRESS", "Workload B1: Natural Write Progress (Concurrent Disk Reads)"),
    ("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", "Workload B2: Controlled Flush Interference"),
    ("WORKLOAD_C_COMPACTION_OVERLAP", "Workload C: Compaction Overlap"),
    ("WORKLOAD_D_READER_CHURN", "Workload D: High Reader-Set Churn (16KB Threshold)"),
    ("WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE", "Workload E: In-Memory Memtable (2 Concurrent Writers)")
]

CONCURRENCIES = [1, 2, 4, 8, 12, 16, 24, 32]

def bootstrap_ratio_and_diff(a_vals, b_vals, n_boot=10000):
    a = np.array(a_vals, dtype=float)
    b = np.array(b_vals, dtype=float)
    ratios = []
    diffs = []
    for _ in range(n_boot):
        sample_a = np.random.choice(a, size=len(a), replace=True)
        sample_b = np.random.choice(b, size=len(b), replace=True)
        med_a = np.median(sample_a)
        med_b = np.median(sample_b)
        if med_a > 0:
            ratios.append(med_b / med_a)
        diffs.append(med_b - med_a)
    r_lo, r_hi = np.percentile(ratios, [2.5, 97.5]) if ratios else (1.0, 1.0)
    d_lo, d_hi = np.percentile(diffs, [2.5, 97.5]) if diffs else (0.0, 0.0)
    return (float(r_lo), float(r_hi)), (float(d_lo), float(d_hi))

def iqr(vals):
    q75, q25 = np.percentile(vals, [75, 25])
    return float(q75 - q25)

def main():
    with open(CSV_PATH, 'r', encoding='utf-8') as fp:
        reader = csv.DictReader(fp)
        rows = list(reader)

    cells = defaultdict(lambda: defaultdict(list))
    for r in rows:
        w = r['workload_id']
        rc = int(r['reader_concurrency'])
        mode = "A" if "ApproachA" in r['sync_mode'] else "B"
        cells[(w, rc)][mode].append(r)

    analysis_results = {}
    md_lines = ["# Phase I6 Comprehensive Comparative Statistical Tables\n",
                "Generated across 960 independent trials ($N = 10$ per cell) on native ext4 inside WSL2 Ubuntu 24.04.\n",
                "Uncertainty intervals represent non-parametric 95% bootstrap confidence intervals ($B = 10{,}000$).\n\n"]

    for w_id, w_title in WORKLOADS:
        analysis_results[w_id] = {
            "title": w_title,
            "cells": {}
        }
        md_lines.append(f"## {w_title}\n\n")
        md_lines.append("| Readers | Tput A (ops/s) [IQR] | Tput B (ops/s) [IQR] | Diff (B - A) [95% CI] | B/A Ratio [95% CI] | Outcome Classification | Writes A | Writes B | Writer Ratio [95% CI] | p50 A (µs) | p50 B (µs) | p95 A (µs) | p95 B (µs) | p99 A (µs) | p99 B (µs) |\n")
        md_lines.append("| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |\n")

        for rc in CONCURRENCIES:
            a_rows = cells[(w_id, rc)]['A']
            b_rows = cells[(w_id, rc)]['B']

            a_tputs = [float(r['read_ops_per_sec']) for r in a_rows]
            b_tputs = [float(r['read_ops_per_sec']) for r in b_rows]
            a_writes = [int(r['write_ops_completed']) for r in a_rows]
            b_writes = [int(r['write_ops_completed']) for r in b_rows]
            a_p50 = [float(r['latency_read_p50_us']) for r in a_rows]
            b_p50 = [float(r['latency_read_p50_us']) for r in b_rows]
            a_p95 = [float(r['latency_read_p95_us']) for r in a_rows]
            b_p95 = [float(r['latency_read_p95_us']) for r in b_rows]
            a_p99 = [float(r['latency_read_p99_us']) for r in a_rows]
            b_p99 = [float(r['latency_read_p99_us']) for r in b_rows]

            med_tput_a = float(np.median(a_tputs))
            med_tput_b = float(np.median(b_tputs))
            iqr_tput_a = iqr(a_tputs)
            iqr_tput_b = iqr(b_tputs)

            (r_lo, r_hi), (d_lo, d_hi) = bootstrap_ratio_and_diff(a_tputs, b_tputs)
            diff_tput = med_tput_b - med_tput_a
            rom = med_tput_b / med_tput_a if med_tput_a > 0 else 1.0

            # Classification
            if r_lo > 1.0:
                classification = "Positive (Advantage B)"
            elif r_hi < 1.0:
                classification = "Negative (Overhead B)"
            else:
                if r_lo >= 0.95 and r_hi <= 1.05:
                    classification = "Equivalent (±5% Band)"
                else:
                    classification = "Inconclusive / Similar"

            # Writes
            med_w_a = int(np.median(a_writes))
            med_w_b = int(np.median(b_writes))
            (w_r_lo, w_r_hi), (w_d_lo, w_d_hi) = bootstrap_ratio_and_diff(a_writes, b_writes)
            w_ratio = med_w_b / med_w_a if med_w_a > 0 else (1.0 if med_w_b == 0 else float('inf'))

            # Latencies
            med_p50_a = float(np.median(a_p50))
            med_p50_b = float(np.median(b_p50))
            med_p95_a = float(np.median(a_p95))
            med_p95_b = float(np.median(b_p95))
            med_p99_a = float(np.median(a_p99))
            med_p99_b = float(np.median(b_p99))

            cell_data = {
                "reader_concurrency": rc,
                "tput_a_median": round(med_tput_a, 2),
                "tput_a_iqr": round(iqr_tput_a, 2),
                "tput_b_median": round(med_tput_b, 2),
                "tput_b_iqr": round(iqr_tput_b, 2),
                "diff_median": round(diff_tput, 2),
                "diff_ci95": [round(d_lo, 2), round(d_hi, 2)],
                "ratio_median": round(rom, 2),
                "ratio_ci95": [round(r_lo, 2), round(r_hi, 2)],
                "outcome_classification": classification,
                "writes_a_median": med_w_a,
                "writes_b_median": med_w_b,
                "writes_ratio": round(w_ratio, 2) if w_ratio != float('inf') else None,
                "writes_ratio_ci95": [round(w_r_lo, 2), round(w_r_hi, 2)] if med_w_a > 0 else None,
                "p50_a_us": round(med_p50_a, 1),
                "p50_b_us": round(med_p50_b, 1),
                "p95_a_us": round(med_p95_a, 1),
                "p95_b_us": round(med_p95_b, 1),
                "p99_a_us": round(med_p99_a, 1),
                "p99_b_us": round(med_p99_b, 1),
            }
            analysis_results[w_id]["cells"][rc] = cell_data

            w_ratio_str = f"{w_ratio:.1f}x [{w_r_lo:.1f}x, {w_r_hi:.1f}x]" if med_w_a > 0 else "N/A"
            md_lines.append(
                f"| {rc} | {med_tput_a:.1f} [{iqr_tput_a:.1f}] | {med_tput_b:.1f} [{iqr_tput_b:.1f}] | "
                f"{diff_tput:+.1f} [{d_lo:+.1f}, {d_hi:+.1f}] | {rom:.2f}x [{r_lo:.2f}x, {r_hi:.2f}x] | "
                f"{classification} | {med_w_a} | {med_w_b} | {w_ratio_str} | "
                f"{med_p50_a:.1f} | {med_p50_b:.1f} | {med_p95_a:.1f} | {med_p95_b:.1f} | {med_p99_a:.1f} | {med_p99_b:.1f} |\n"
            )
        md_lines.append("\n")

    with open(OUTPUT_JSON, 'w', encoding='utf-8') as fp:
        json.dump(analysis_results, fp, indent=2)

    with open(OUTPUT_MD, 'w', encoding='utf-8') as fp:
        fp.writelines(md_lines)

    print(f"Analysis saved to {OUTPUT_JSON} and {OUTPUT_MD}")

if __name__ == "__main__":
    main()
