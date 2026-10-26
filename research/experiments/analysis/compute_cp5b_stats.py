import os
import glob
import json
import numpy as np

DATA_DIR = os.path.join("research", "experiments", "data", "campaign_wsl2_ext4_restart_v1", "wsl2_ext4")

def bootstrap_ci(values, n_resamples=10000, ci=95):
    if len(values) == 0:
        return 0.0, 0.0, 0.0
    if len(values) == 1:
        return float(values[0]), float(values[0]), float(values[0])
    med = float(np.median(values))
    boot_medians = [np.median(np.random.choice(values, size=len(values), replace=True)) for _ in range(n_resamples)]
    alpha = (100 - ci) / 2.0
    low = float(np.percentile(boot_medians, alpha))
    high = float(np.percentile(boot_medians, 100 - alpha))
    return med, low, high

def main():
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
                pass

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

    print("# FULL EMPIRICAL STATISTICAL SUMMARY (960 Trials, WSL2 Native ext4)\n")

    for w in workloads:
        print(f"\n### {w}")
        print("| Readers | Tput A (ops/s) [95% CI] | Tput B (ops/s) [95% CI] | Speedup (B/A) | p50 A (µs) | p50 B (µs) | p95 A (µs) | p95 B (µs) | p99 A (µs) | p99 B (µs) | Writes A | Writes B | Meas Flushes | Meas Comp |")
        print("| :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: | :---: |")

        for rd in concurrencies:
            list_A = cells.get((w, rd, "A"), [])
            list_B = cells.get((w, rd, "B"), [])

            tput_A_vals = [r["throughput"]["read_ops_per_sec"] for r in list_A]
            tput_B_vals = [r["throughput"]["read_ops_per_sec"] for r in list_B]

            med_A, lo_A, hi_A = bootstrap_ci(tput_A_vals)
            med_B, lo_B, hi_B = bootstrap_ci(tput_B_vals)
            speedup = (med_B / med_A) if med_A > 0 else 1.0

            p50_A = np.median([r["latency_read_nanos"]["p50"] / 1000.0 for r in list_A])
            p50_B = np.median([r["latency_read_nanos"]["p50"] / 1000.0 for r in list_B])

            p95_A_vals = [r["latency_read_nanos"]["p95"] / 1000.0 for r in list_A if r["latency_read_nanos"].get("p95") is not None]
            p95_B_vals = [r["latency_read_nanos"]["p95"] / 1000.0 for r in list_B if r["latency_read_nanos"].get("p95") is not None]
            p95_A_str = f"{np.median(p95_A_vals):.1f}" if p95_A_vals else "null"
            p95_B_str = f"{np.median(p95_B_vals):.1f}" if p95_B_vals else "null"

            p99_A_vals = [r["latency_read_nanos"]["p99"] / 1000.0 for r in list_A if r["latency_read_nanos"].get("p99") is not None]
            p99_B_vals = [r["latency_read_nanos"]["p99"] / 1000.0 for r in list_B if r["latency_read_nanos"].get("p99") is not None]
            p99_A_str = f"{np.median(p99_A_vals):.1f}" if p99_A_vals else "null"
            p99_B_str = f"{np.median(p99_B_vals):.1f}" if p99_B_vals else "null"

            w_A = np.median([r["operations"]["write_ops_completed"] for r in list_A])
            w_B = np.median([r["operations"]["write_ops_completed"] for r in list_B])

            flushes = np.median([r["lsm_diagnostics"]["measured_flushes"] for r in list_B])
            comps = np.median([r["lsm_diagnostics"]["measured_compactions"] for r in list_B])

            print(f"| {rd} | {med_A:.1f} [{lo_A:.1f}-{hi_A:.1f}] | {med_B:.1f} [{lo_B:.1f}-{hi_B:.1f}] | **{speedup:.2f}x** | {p50_A:.1f} | {p50_B:.1f} | {p95_A_str} | {p95_B_str} | {p99_A_str} | {p99_B_str} | {int(w_A)} | {int(w_B)} | {int(flushes)} | {int(comps)} |")

if __name__ == "__main__":
    main()
