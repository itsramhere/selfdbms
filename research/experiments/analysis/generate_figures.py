#!/usr/bin/env python3
"""
generate_figures.py

Generates 5 publication-quality figures for the research manuscript and Checkpoint CP6.
Includes accurate titles, units, uncertainty intervals, provenance disclosures, and corrected overlap visualizations.
"""

import os
import glob
import json
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

ROOT_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
DATA_DIR = os.path.join(ROOT_DIR, "research", "experiments", "data", "campaign_wsl2_ext4_restart_v1", "wsl2_ext4")
FIG_DIR = os.path.join(ROOT_DIR, "research", "experiments", "figures")
os.makedirs(FIG_DIR, exist_ok=True)

PROVENANCE_TEXT = "Platform: WSL2 Ubuntu 24.04 (Linux 6.6, native ext4 on /dev/sdd, N=10/cell)"

def load_all_results():
    pattern = os.path.join(DATA_DIR, "*.json")
    files = glob.glob(pattern)
    results = []
    for f in sorted(files):
        with open(f, 'r', encoding='utf-8') as fp:
            try:
                data = json.load(fp)
                if data.get("status") == "SUCCESS":
                    results.append(data)
            except Exception as e:
                print(f"Error loading {f}: {e}")
    return results

def bootstrap_ci(values, n_resamples=5000, ci=95):
    if len(values) == 0:
        return 0.0, 0.0, 0.0
    if len(values) == 1:
        return float(values[0]), float(values[0]), float(values[0])
    vals = np.array(values, dtype=float)
    med = float(np.median(vals))
    boot_medians = [np.median(np.random.choice(vals, size=len(vals), replace=True)) for _ in range(n_resamples)]
    alpha = (100 - ci) / 2.0
    low = float(np.percentile(boot_medians, alpha))
    high = float(np.percentile(boot_medians, 100 - alpha))
    return med, low, high

def main():
    results = load_all_results()
    print(f"Loaded {len(results)} valid trial results from {DATA_DIR}")
    if not results:
        print("No results found.")
        return

    # Group by (workload, readers, mode)
    cells = {}
    for r in results:
        w = r["workload_id"]
        rd = r["config"]["reader_concurrency"]
        mode = "A" if "ApproachA" in r["sync_mode"] else "B"
        key = (w, rd, mode)
        if key not in cells:
            cells[key] = []
        cells[key].append(r)

    workloads = [
        "WORKLOAD_A_READ_ONLY",
        "WORKLOAD_B1_NATURAL_WRITE_PROGRESS",
        "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE",
        "WORKLOAD_C_COMPACTION_OVERLAP",
        "WORKLOAD_D_READER_CHURN",
        "WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE"
    ]
    concurrencies = [1, 2, 4, 8, 12, 16, 24, 32]

    plt.style.use('seaborn-v0_8-whitegrid' if 'seaborn-v0_8-whitegrid' in plt.style.available else 'default')
    plt.rcParams.update({'font.size': 10, 'figure.autolayout': True})

    w_titles = {
        "WORKLOAD_A_READ_ONLY": "Workload A: Uncontended Read-Only SSTables",
        "WORKLOAD_B1_NATURAL_WRITE_PROGRESS": "Workload B1: Natural Write Progress (2 Writers)",
        "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE": "Workload B2: Controlled Flush Interference (20 Flushes)",
        "WORKLOAD_C_COMPACTION_OVERLAP": "Workload C: Compaction Overlap (1 Compaction)",
        "WORKLOAD_D_READER_CHURN": "Workload D: High Reader-Set Churn (16KB Threshold)",
        "WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE": "Workload E: In-Memory Memtable (2 Writers)"
    }

    # =========================================================================
    # FIGURE 1: Read Throughput Scaling across All Six Workloads
    # =========================================================================
    fig, axes = plt.subplots(2, 3, figsize=(16, 10), sharex=True)
    axes = axes.flatten()

    for idx, w in enumerate(workloads):
        ax = axes[idx]
        tput_A, tput_A_err = [], []
        tput_B, tput_B_err = [], []

        for rd in concurrencies:
            vals_A = [r["throughput"]["read_ops_per_sec"] for r in cells.get((w, rd, "A"), [])]
            vals_B = [r["throughput"]["read_ops_per_sec"] for r in cells.get((w, rd, "B"), [])]

            med_A, lo_A, hi_A = bootstrap_ci(vals_A)
            med_B, lo_B, hi_B = bootstrap_ci(vals_B)

            tput_A.append(med_A)
            tput_A_err.append([med_A - lo_A, hi_A - med_A])
            tput_B.append(med_B)
            tput_B_err.append([med_B - lo_B, hi_B - med_B])

        err_A = np.array(tput_A_err).T if tput_A_err else None
        err_B = np.array(tput_B_err).T if tput_B_err else None

        ax.errorbar(concurrencies, tput_A, yerr=err_A, fmt='-o', color='#1f77b4', label='Approach A (RWMutex)', capsize=3, alpha=0.9)
        ax.errorbar(concurrencies, tput_B, yerr=err_B, fmt='-s', color='#2ca02c', label='Approach B (Snapshots)', capsize=3, alpha=0.9)
        ax.set_title(w_titles.get(w, w), fontsize=11, fontweight='bold')
        ax.set_xlabel('Reader Goroutines')
        ax.set_ylabel('Read Throughput (ops/sec)')
        ax.legend(loc='best', frameon=True)
        ax.grid(True, linestyle='--', alpha=0.6)

    fig.suptitle(f"Figure 1: Read Throughput Scaling (Median & 95% Bootstrap CI)\n{PROVENANCE_TEXT}", fontsize=13, fontweight='bold', y=1.01)
    fig.tight_layout()
    fig1_path = os.path.join(FIG_DIR, "fig1_throughput_scaling.png")
    fig.savefig(fig1_path, dpi=300, bbox_inches='tight')
    plt.close(fig)
    print(f"Generated {fig1_path}")

    # =========================================================================
    # FIGURE 2: Tail Latency (p99) Scaling
    # =========================================================================
    fig, axes = plt.subplots(2, 3, figsize=(16, 10), sharex=True)
    axes = axes.flatten()

    for idx, w in enumerate(workloads):
        ax = axes[idx]
        p99_A, p99_A_err = [], []
        p99_B, p99_B_err = [], []

        for rd in concurrencies:
            vals_A = [r["latency_read_nanos"]["p99"] / 1000.0 for r in cells.get((w, rd, "A"), []) if r["latency_read_nanos"].get("p99") is not None]
            vals_B = [r["latency_read_nanos"]["p99"] / 1000.0 for r in cells.get((w, rd, "B"), []) if r["latency_read_nanos"].get("p99") is not None]

            med_A, lo_A, hi_A = bootstrap_ci(vals_A)
            med_B, lo_B, hi_B = bootstrap_ci(vals_B)

            p99_A.append(med_A)
            p99_A_err.append([med_A - lo_A, hi_A - med_A])
            p99_B.append(med_B)
            p99_B_err.append([med_B - lo_B, hi_B - med_B])

        err_A = np.array(p99_A_err).T if p99_A_err else None
        err_B = np.array(p99_B_err).T if p99_B_err else None

        ax.errorbar(concurrencies, p99_A, yerr=err_A, fmt='-o', color='#1f77b4', label='Approach A (p99)', capsize=3, alpha=0.9)
        ax.errorbar(concurrencies, p99_B, yerr=err_B, fmt='-s', color='#2ca02c', label='Approach B (p99)', capsize=3, alpha=0.9)
        ax.set_title(w_titles.get(w, w), fontsize=11, fontweight='bold')
        ax.set_xlabel('Reader Goroutines')
        ax.set_ylabel('Read p99 Latency (µs, log scale)')
        ax.set_yscale('log')
        ax.legend(loc='best', frameon=True)
        ax.grid(True, linestyle='--', alpha=0.6)

    fig.suptitle(f"Figure 2: Read 99th-Percentile Tail Latency (Log Scale, 95% Bootstrap CI)\n{PROVENANCE_TEXT}", fontsize=13, fontweight='bold', y=1.01)
    fig.tight_layout()
    fig2_path = os.path.join(FIG_DIR, "fig2_tail_latency.png")
    fig.savefig(fig2_path, dpi=300, bbox_inches='tight')
    plt.close(fig)
    print(f"Generated {fig2_path}")

    # =========================================================================
    # FIGURE 3: Write Progress Under Concurrent Readers (Workloads B1 & D)
    # =========================================================================
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6))

    for w, ax, title in [("WORKLOAD_B1_NATURAL_WRITE_PROGRESS", ax1, "Workload B1: Natural Write Progress (2 Writers, wal_sync=true)"),
                         ("WORKLOAD_D_READER_CHURN", ax2, "Workload D: Rapid Churn Write Progress (16KB Memtable)")]:
        writes_A, errs_A = [], []
        writes_B, errs_B = [], []
        for rd in concurrencies:
            w_A = [r["operations"]["write_ops_completed"] for r in cells.get((w, rd, "A"), [])]
            w_B = [r["operations"]["write_ops_completed"] for r in cells.get((w, rd, "B"), [])]
            med_A, lo_A, hi_A = bootstrap_ci(w_A)
            med_B, lo_B, hi_B = bootstrap_ci(w_B)
            writes_A.append(med_A)
            errs_A.append([med_A - lo_A, hi_A - med_A])
            writes_B.append(med_B)
            errs_B.append([med_B - lo_B, hi_B - med_B])

        e_A = np.array(errs_A).T
        e_B = np.array(errs_B).T

        ax.errorbar(concurrencies, writes_A, yerr=e_A, fmt='-o', color='#d62728', label='Approach A (Starved by Readers)', capsize=3)
        ax.errorbar(concurrencies, writes_B, yerr=e_B, fmt='-s', color='#2ca02c', label='Approach B (Decoupled Writes)', capsize=3)
        ax.set_title(title, fontsize=11, fontweight='bold')
        ax.set_xlabel('Concurrent Reader Goroutines')
        ax.set_ylabel('Completed Writes (10s Measurement Duration)')
        ax.legend(loc='best', frameon=True)
        ax.grid(True, linestyle='--', alpha=0.6)

    fig.suptitle(f"Figure 3: Writer Progress Isolation Under Reader Load\n{PROVENANCE_TEXT}", fontsize=13, fontweight='bold', y=1.02)
    fig.tight_layout()
    fig3_path = os.path.join(FIG_DIR, "fig3_write_progress.png")
    fig.savefig(fig3_path, dpi=300, bbox_inches='tight')
    plt.close(fig)
    print(f"Generated {fig3_path}")

    # =========================================================================
    # FIGURE 4: Exposure-Normalized Overlap & Window Duration Analysis
    # =========================================================================
    fig, ((ax1, ax2), (ax3, ax4)) = plt.subplots(2, 2, figsize=(15, 10))

    # Panel 4A: Workload B2 Flush Window Duration
    dur_flush_A, dur_flush_B = [], []
    for rd in concurrencies:
        d_A = [r["lsm_diagnostics"]["flush_active_overlap_ms"] for r in cells.get(("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", rd, "A"), [])]
        d_B = [r["lsm_diagnostics"]["flush_active_overlap_ms"] for r in cells.get(("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", rd, "B"), [])]
        dur_flush_A.append(np.median(d_A))
        dur_flush_B.append(np.median(d_B))

    ax1.plot(concurrencies, dur_flush_A, '-o', color='#1f77b4', label='Approach A (Stalled by Reader Drain)')
    ax1.plot(concurrencies, dur_flush_B, '-s', color='#2ca02c', label='Approach B (Instantaneous Publish)')
    ax1.set_title('4A. Workload B2: Total Active Flush Window Duration', fontsize=11, fontweight='bold')
    ax1.set_xlabel('Reader Goroutines')
    ax1.set_ylabel('Cumulative Flush Active Window (ms)')
    ax1.legend(loc='best', frameon=True)
    ax1.grid(True, linestyle='--', alpha=0.6)

    # Panel 4B: Workload B2 p95 Latency Suppression
    p95_flush_A, p95_flush_B = [], []
    for rd in concurrencies:
        p_A = [r["latency_read_nanos"]["p95"] / 1000.0 for r in cells.get(("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", rd, "A"), [])]
        p_B = [r["latency_read_nanos"]["p95"] / 1000.0 for r in cells.get(("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", rd, "B"), [])]
        p95_flush_A.append(np.median(p_A))
        p95_flush_B.append(np.median(p_B))

    ax2.plot(concurrencies, p95_flush_A, '-o', color='#1f77b4', label='Approach A (Blocked behind Flushes)')
    ax2.plot(concurrencies, p95_flush_B, '-s', color='#2ca02c', label='Approach B (Isolated on Snapshots)')
    ax2.set_title('4B. Workload B2: 95th-Percentile Read Tail Latency', fontsize=11, fontweight='bold')
    ax2.set_xlabel('Reader Goroutines')
    ax2.set_ylabel('Read p95 Latency (µs)')
    ax2.legend(loc='best', frameon=True)
    ax2.grid(True, linestyle='--', alpha=0.6)

    # Panel 4C: Workload C Compaction Active Duration
    comp_dur_A, comp_dur_B = [], []
    for rd in concurrencies:
        cd_A = [r["lsm_diagnostics"]["compaction_active_overlap_ms"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "A"), [])]
        cd_B = [r["lsm_diagnostics"]["compaction_active_overlap_ms"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "B"), [])]
        comp_dur_A.append(np.median(cd_A))
        comp_dur_B.append(np.median(cd_B))

    ax3.plot(concurrencies, comp_dur_A, '-o', color='#1f77b4', label='Approach A Compaction Window')
    ax3.plot(concurrencies, comp_dur_B, '-s', color='#2ca02c', label='Approach B Compaction Window')
    ax3.set_title('4C. Workload C: Compaction Window Duration', fontsize=11, fontweight='bold')
    ax3.set_xlabel('Reader Goroutines')
    ax3.set_ylabel('Compaction Active Window (ms)')
    ax3.legend(loc='best', frameon=True)
    ax3.grid(True, linestyle='--', alpha=0.6)

    # Panel 4D: Workload C Overlap Read Proportion (%)
    pct_comp_A, pct_comp_B = [], []
    for rd in concurrencies:
        tot_A = [r["operations"]["read_ops_completed"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "A"), [])]
        cr_A = [r["lsm_diagnostics"]["reads_during_compaction_count"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "A"), [])]
        tot_B = [r["operations"]["read_ops_completed"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "B"), [])]
        cr_B = [r["lsm_diagnostics"]["reads_during_compaction_count"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "B"), [])]
        pct_A = [c / t * 100.0 for c, t in zip(cr_A, tot_A) if t > 0]
        pct_B = [c / t * 100.0 for c, t in zip(cr_B, tot_B) if t > 0]
        pct_comp_A.append(np.median(pct_A))
        pct_comp_B.append(np.median(pct_B))

    ax4.plot(concurrencies, pct_comp_A, '-o', color='#1f77b4', label='Approach A (% in Compaction)')
    ax4.plot(concurrencies, pct_comp_B, '-s', color='#2ca02c', label='Approach B (% in Compaction)')
    ax4.axhline(1.5, color='gray', linestyle=':', label='Max Overlap (<1.6%)')
    ax4.set_title('4D. Workload C: Proportion of Reads During Compaction', fontsize=11, fontweight='bold')
    ax4.set_xlabel('Reader Goroutines')
    ax4.set_ylabel('% of Completed Reads in Overlap Window')
    ax4.legend(loc='best', frameon=True)
    ax4.grid(True, linestyle='--', alpha=0.6)

    fig.suptitle(f"Figure 4: Flush & Compaction Overlap Accounting and Exposure Duration\n{PROVENANCE_TEXT}", fontsize=13, fontweight='bold', y=1.01)
    fig.tight_layout()
    fig4_path = os.path.join(FIG_DIR, "fig4_flush_compaction_overlap.png")
    fig.savefig(fig4_path, dpi=300, bbox_inches='tight')
    plt.close(fig)
    print(f"Generated {fig4_path}")

    # =========================================================================
    # FIGURE 5: Empirical Speedup Tradeoffs with Equivalence & Parity Bands
    # =========================================================================
    fig, ax = plt.subplots(figsize=(13, 7))

    markers = ['o', 's', '^', 'D', 'v', 'p']
    colors = ['#1f77b4', '#ff7f0e', '#2ca02c', '#d62728', '#9467bd', '#8c564b']

    # Shaded ±5% practical equivalence band
    ax.axhspan(0.95, 1.05, color='gray', alpha=0.15, label='Equivalence Band (±5%: [0.95x, 1.05x])')
    ax.axhline(1.0, color='black', linestyle='--', linewidth=1.2, label='Parity (1.0x)')

    for idx, w in enumerate(workloads):
        speedups = []
        for rd in concurrencies:
            t_A = [r["throughput"]["read_ops_per_sec"] for r in cells.get((w, rd, "A"), [])]
            t_B = [r["throughput"]["read_ops_per_sec"] for r in cells.get((w, rd, "B"), [])]
            if t_A and t_B:
                ratio = np.median(t_B) / np.median(t_A) if np.median(t_A) > 0 else 1.0
                speedups.append(ratio)
            else:
                speedups.append(np.nan)
        ax.plot(concurrencies, speedups, marker=markers[idx], color=colors[idx], linewidth=2.2, label=w_titles.get(w, w))

    ax.set_title(f'Figure 5: Throughput Speedup Factor (Approach B / Approach A) Across Concurrencies\n{PROVENANCE_TEXT}', fontsize=12, fontweight='bold')
    ax.set_xlabel('Reader Goroutines (Host Logical Processors = 12)')
    ax.set_ylabel('Speedup Ratio (B / A, Log Scale)')
    ax.set_yscale('log')
    ax.set_yticks([0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.2, 1.4, 1.6, 1.8, 2.0, 2.2])
    ax.get_yaxis().set_major_formatter(matplotlib.ticker.ScalarFormatter())
    ax.legend(loc='best', frameon=True, fontsize=9.5)
    ax.grid(True, which='both', linestyle='--', alpha=0.6)

    fig.tight_layout()
    fig5_path = os.path.join(FIG_DIR, "fig5_speedup_tradeoffs.png")
    fig.savefig(fig5_path, dpi=300, bbox_inches='tight')
    plt.close(fig)
    print(f"Generated {fig5_path}")

    print("All 5 figures successfully updated and regenerated.")

if __name__ == '__main__':
    main()
