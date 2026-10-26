import os
import glob
import json
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

DATA_DIR = os.path.join("research", "experiments", "data", "campaign_wsl2_ext4_restart_v1", "wsl2_ext4")
FIG_DIR = os.path.join("research", "experiments", "figures")
os.makedirs(FIG_DIR, exist_ok=True)

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

def bootstrap_ci(values, n_resamples=10000, ci=95):
    if len(values) == 0:
        return 0.0, 0.0, 0.0
    if len(values) == 1:
        return values[0], values[0], values[0]
    med = float(np.median(values))
    boot_medians = [np.median(np.random.choice(values, size=len(values), replace=True)) for _ in range(n_resamples)]
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

    # Style configuration
    plt.style.use('seaborn-v0_8-whitegrid' if 'seaborn-v0_8-whitegrid' in plt.style.available else 'default')
    plt.rcParams.update({'font.size': 11, 'figure.autolayout': True})

    # FIGURE 1: Read Throughput Scaling across Workloads
    fig, axes = plt.subplots(2, 3, figsize=(16, 10), sharex=True)
    axes = axes.flatten()

    w_titles = {
        "WORKLOAD_A_READ_ONLY": "Workload A: Read-Only SSTable Baseline",
        "WORKLOAD_B1_NATURAL_WRITE_PROGRESS": "Workload B1: Natural Write Progress",
        "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE": "Workload B2: Controlled Flush Interference",
        "WORKLOAD_C_COMPACTION_OVERLAP": "Workload C: Compaction Overlap",
        "WORKLOAD_D_READER_CHURN": "Workload D: Reader Churn & Churn",
        "WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE": "Workload E: Negative Control (Memtable)"
    }

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

        ax.errorbar(concurrencies, tput_A, yerr=err_A, fmt='-o', color='#1f77b4', label='Approach A (RWMutex)', capsize=3)
        ax.errorbar(concurrencies, tput_B, yerr=err_B, fmt='-s', color='#2ca02c', label='Approach B (Snapshot)', capsize=3)
        ax.set_title(w_titles.get(w, w), fontsize=12, fontweight='bold')
        ax.set_xlabel('Reader Goroutines')
        ax.set_ylabel('Read Ops/sec')
        ax.legend(loc='best')
        ax.grid(True, linestyle='--', alpha=0.6)

    fig.tight_layout()
    fig1_path = os.path.join(FIG_DIR, "fig1_throughput_scaling.png")
    fig.savefig(fig1_path, dpi=300)
    plt.close(fig)
    print(f"Generated {fig1_path}")

    # FIGURE 2: Tail Latency (p99) Scaling
    fig, axes = plt.subplots(2, 3, figsize=(16, 10), sharex=True)
    axes = axes.flatten()

    for idx, w in enumerate(workloads):
        ax = axes[idx]
        p99_A, p99_B = [], []

        for rd in concurrencies:
            vals_A = [r["latency_read_nanos"]["p99"] / 1000.0 for r in cells.get((w, rd, "A"), []) if r["latency_read_nanos"].get("p99") is not None]
            vals_B = [r["latency_read_nanos"]["p99"] / 1000.0 for r in cells.get((w, rd, "B"), []) if r["latency_read_nanos"].get("p99") is not None]

            p99_A.append(np.median(vals_A) if vals_A else np.nan)
            p99_B.append(np.median(vals_B) if vals_B else np.nan)

        ax.plot(concurrencies, p99_A, '-o', color='#1f77b4', label='Approach A (p99)')
        ax.plot(concurrencies, p99_B, '-s', color='#2ca02c', label='Approach B (p99)')
        ax.set_title(w_titles.get(w, w), fontsize=12, fontweight='bold')
        ax.set_xlabel('Reader Goroutines')
        ax.set_ylabel('Read p99 Latency (µs)')
        ax.set_yscale('log')
        ax.legend(loc='best')
        ax.grid(True, linestyle='--', alpha=0.6)

    fig.tight_layout()
    fig2_path = os.path.join(FIG_DIR, "fig2_tail_latency.png")
    fig.savefig(fig2_path, dpi=300)
    plt.close(fig)
    print(f"Generated {fig2_path}")

    # FIGURE 3: Write Progress Under Concurrent Readers (Workloads B1 & D)
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6))

    for w, ax, title in [( "WORKLOAD_B1_NATURAL_WRITE_PROGRESS", ax1, "Workload B1: Write Progress (1MB Memtable)"),
                         ("WORKLOAD_D_READER_CHURN", ax2, "Workload D: Write Progress (16KB Churn)")]:
        writes_A = []
        writes_B = []
        for rd in concurrencies:
            w_A = [r["operations"]["write_ops_completed"] for r in cells.get((w, rd, "A"), [])]
            w_B = [r["operations"]["write_ops_completed"] for r in cells.get((w, rd, "B"), [])]
            writes_A.append(np.median(w_A) if w_A else 0)
            writes_B.append(np.median(w_B) if w_B else 0)

        ax.plot(concurrencies, writes_A, '-o', color='#d62728', label='Approach A Writes')
        ax.plot(concurrencies, writes_B, '-s', color='#2ca02c', label='Approach B Writes')
        ax.set_title(title, fontsize=12, fontweight='bold')
        ax.set_xlabel('Reader Goroutines')
        ax.set_ylabel('Completed Writes (10s)')
        ax.legend(loc='best')
        ax.grid(True, linestyle='--', alpha=0.6)

    fig.tight_layout()
    fig3_path = os.path.join(FIG_DIR, "fig3_write_progress.png")
    fig.savefig(fig3_path, dpi=300)
    plt.close(fig)
    print(f"Generated {fig3_path}")

    # FIGURE 4: Flush and Compaction Interference (Workloads B2 and C)
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(14, 6))

    # B2 Reads During Active Flush
    reads_flush_A, reads_flush_B = [], []
    for rd in concurrencies:
        rf_A = [r["lsm_diagnostics"]["reads_during_flush_count"] for r in cells.get(("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", rd, "A"), [])]
        rf_B = [r["lsm_diagnostics"]["reads_during_flush_count"] for r in cells.get(("WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE", rd, "B"), [])]
        reads_flush_A.append(np.median(rf_A) if rf_A else 0)
        reads_flush_B.append(np.median(rf_B) if rf_B else 0)

    ax1.plot(concurrencies, reads_flush_A, '-o', color='#1f77b4', label='Approach A (Blocked)')
    ax1.plot(concurrencies, reads_flush_B, '-s', color='#2ca02c', label='Approach B (Concurrent)')
    ax1.set_title('Workload B2: Reads Executed During Active Flush', fontsize=12, fontweight='bold')
    ax1.set_xlabel('Reader Goroutines')
    ax1.set_ylabel('Reads During Physical Flush Window')
    ax1.legend(loc='best')
    ax1.grid(True, linestyle='--', alpha=0.6)

    # C Compaction Overlap Reads
    comp_reads_A, comp_reads_B = [], []
    for rd in concurrencies:
        cr_A = [r["lsm_diagnostics"]["reads_during_compaction_count"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "A"), [])]
        cr_B = [r["lsm_diagnostics"]["reads_during_compaction_count"] for r in cells.get(("WORKLOAD_C_COMPACTION_OVERLAP", rd, "B"), [])]
        comp_reads_A.append(np.median(cr_A) if cr_A else 0)
        comp_reads_B.append(np.median(cr_B) if cr_B else 0)

    ax2.plot(concurrencies, comp_reads_A, '-o', color='#1f77b4', label='Approach A')
    ax2.plot(concurrencies, comp_reads_B, '-s', color='#2ca02c', label='Approach B')
    ax2.set_title('Workload C: Reads Executed During Compaction', fontsize=12, fontweight='bold')
    ax2.set_xlabel('Reader Goroutines')
    ax2.set_ylabel('Reads in Compaction Overlap Window')
    ax2.legend(loc='best')
    ax2.grid(True, linestyle='--', alpha=0.6)

    fig.tight_layout()
    fig4_path = os.path.join(FIG_DIR, "fig4_flush_compaction_overlap.png")
    fig.savefig(fig4_path, dpi=300)
    plt.close(fig)
    print(f"Generated {fig4_path}")

    # FIGURE 5: Speedup Summary (B/A Ratio Across All Workloads)
    fig, ax = plt.subplots(figsize=(12, 7))

    markers = ['o', 's', '^', 'D', 'v', 'p']
    colors = ['#1f77b4', '#ff7f0e', '#2ca02c', '#d62728', '#9467bd', '#8c564b']

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
        ax.plot(concurrencies, speedups, marker=markers[idx], color=colors[idx], linewidth=2, label=w_titles.get(w, w))

    ax.axhline(1.0, color='gray', linestyle='--', linewidth=1.5, label='Parity (1.0x)')
    ax.set_title('Empirical Speedup Ratio (Approach B / Approach A) across Concurrencies', fontsize=13, fontweight='bold')
    ax.set_xlabel('Reader Goroutines (Logical Cores = 12)')
    ax.set_ylabel('Throughput Speedup Factor (B / A)')
    ax.set_yscale('log')
    ax.legend(loc='best', frameon=True)
    ax.grid(True, linestyle='--', alpha=0.6)

    fig.tight_layout()
    fig5_path = os.path.join(FIG_DIR, "fig5_speedup_tradeoffs.png")
    fig.savefig(fig5_path, dpi=300)
    plt.close(fig)
    print(f"Generated {fig5_path}")

    print("All 5 figures generated successfully.")

if __name__ == '__main__':
    main()
