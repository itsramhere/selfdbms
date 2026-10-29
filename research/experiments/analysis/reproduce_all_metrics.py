"""
Phase I7 - Master Analysis and Metric Reproduction Script
Reproduces all manuscript metrics, tables, and figures directly from immutable raw JSON artifacts.
"""

import os
import glob
import json
import numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from collections import defaultdict
from scipy import stats

G4_ROOT = r'C:\Users\ramst\I7_WINDOWS_G4_RESULTS'
CAL_ROOT = r'C:\Users\ramst\I7_WINDOWS_CALIBRATION_V2'
HIST_ROOT = r'research\experiments\data\campaign_wsl2_ext4_restart_v1'
OUTPUT_DIR = r'paper\figures'

def load_json(path):
    with open(path, 'r', encoding='utf-8') as f:
        return json.load(f)

def load_g4_trials():
    trials = []
    pattern = os.path.join(G4_ROOT, 'trials', 'attempts', '*_a01.json')
    for p in sorted(glob.glob(pattern)):
        trials.append(load_json(p))
    return trials

def load_cal_v2():
    trials = []
    pattern = os.path.join(CAL_ROOT, 'calibration', 'cal_*.json')
    for p in sorted(glob.glob(pattern)):
        trials.append(load_json(p))
    return trials

def analyze_g4_triplets(trials):
    triplets = defaultdict(dict)
    for t in trials:
        b = t['block']
        cell = t['cell']
        trip = t.get('triplet') or (t.get('spec') or {}).get('triplet', 0)
        triplets[(b, cell, trip)][t['arm']] = t

    complete_exp = []
    for (b, cell, trip), arms in sorted(triplets.items()):
        if b != 'D3' and 'A' in arms and 'AMIN' in arms and 'B' in arms:
            complete_exp.append(((b, cell, trip), arms))

    return complete_exp

def run_paired_analysis(exp_triplets):
    rows = []
    for (b, cell, trip), arms in exp_triplets:
        a_res = arms['A']['result']
        amin_res = arms['AMIN']['result']
        b_res = arms['B']['result']

        st_a = arms['A']['status']
        st_amin = arms['AMIN']['status']
        st_b = arms['B']['status']

        tp_a = a_res['throughput']['read_ops_per_sec']
        tp_amin = amin_res['throughput']['read_ops_per_sec']
        tp_b = b_res['throughput']['read_ops_per_sec']

        diff_amin_b = tp_amin - tp_b
        rel_amin_b = diff_amin_b / tp_b if tp_b > 0 else 0.0
        ratio_amin_b = tp_amin / tp_b if tp_b > 0 else 0.0

        diff_amin_a = tp_amin - tp_a if tp_a > 0 else None
        ratio_amin_a = tp_amin / tp_a if tp_a > 0 else None

        rows.append({
            'block': b,
            'cell': cell,
            'triplet': trip,
            'status_a': st_a,
            'status_amin': st_amin,
            'status_b': st_b,
            'tp_a': tp_a,
            'tp_amin': tp_amin,
            'tp_b': tp_b,
            'diff_amin_b': diff_amin_b,
            'rel_amin_b': rel_amin_b,
            'ratio_amin_b': ratio_amin_b,
            'diff_amin_a': diff_amin_a,
            'ratio_amin_a': ratio_amin_a
        })
    return rows

def compute_bootstrap_cis(amin_vals, b_vals, n_boot=10000, seed=20261101):
    rng = np.random.default_rng(seed)
    diffs = amin_vals - b_vals
    rel_diffs = diffs / b_vals

    mean_diff = np.mean(diffs)
    mean_rel = np.mean(rel_diffs)
    ratio_of_means = np.mean(amin_vals) / np.mean(b_vals)
    median_ratio = np.median(amin_vals / b_vals)

    boot_mean_diff = []
    boot_rel_diff = []
    boot_ratio_means = []

    N = len(amin_vals)
    for _ in range(n_boot):
        idx = rng.choice(N, size=N, replace=True)
        boot_mean_diff.append(np.mean(diffs[idx]))
        boot_rel_diff.append(np.mean(rel_diffs[idx]))
        boot_ratio_means.append(np.mean(amin_vals[idx]) / np.mean(b_vals[idx]))

    ci_diff = np.percentile(boot_mean_diff, [2.5, 97.5])
    ci_rel = np.percentile(boot_rel_diff, [2.5, 97.5])
    ci_ratio = np.percentile(boot_ratio_means, [2.5, 97.5])

    return {
        'mean_diff': mean_diff,
        'ci_diff': ci_diff,
        'mean_rel_diff': mean_rel,
        'ci_rel_diff': ci_rel,
        'ratio_of_means': ratio_of_means,
        'ci_ratio_of_means': ci_ratio,
        'median_ratio': median_ratio
    }

def reconcile_e2(trials):
    e2_trials = [t for t in trials if t['block'] == 'E2']
    rows = []
    for t in sorted(e2_trials, key=lambda x: (x['cell'], x.get('triplet') or 0, x['arm'])):
        res = t['result']
        lsm = res['lsm_diagnostics']
        lat = res.get('latency_read_nanos', {})

        rows.append({
            'trial_id': t['trial_id'],
            'cell': t['cell'],
            'arm': t['arm'],
            'status': t['status'],
            'throughput': res['throughput']['read_ops_per_sec'],
            'p50_ms': (lat.get('p50') or 0) / 1e6,
            'p95_ms': (lat.get('p95') or 0) / 1e6,
            'p99_ms': (lat.get('p99') or 0) / 1e6,
            'mean_ms': (lat.get('mean') or 0) / 1e6,
            'sample_count': lat.get('sample_count', 0),
            'pub_target': lsm.get('publication_events_target', 0),
            'pub_in_win': lsm.get('publication_events_realized_in_window', 0),
            'pub_late': lsm.get('publication_events_realized_late', 0),
            'sstable_init': lsm.get('initial_sstables', 0),
            'sstable_final': lsm.get('final_sstables', 0),
            'frozen_init': lsm.get('initial_frozen_memtables', 0),
            'frozen_final': lsm.get('final_frozen_memtables', 0)
        })
    return rows

def reconcile_e3(trials):
    e3_trials = [t for t in trials if t['block'] == 'E3']
    rows = []
    for t in sorted(e3_trials, key=lambda x: (x['cell'], x.get('triplet') or 0, x['arm'])):
        res = t['result']
        ops = res['operations']
        lsm = res['lsm_diagnostics']
        spec = t.get('spec') or {}
        if not spec:
            spec_path = os.path.join(G4_ROOT, 'work', 'specs', f"{t['trial_id']}.json")
            if os.path.exists(spec_path):
                spec = load_json(spec_path)

        rate = spec.get('paced_write_rate_per_sec', 0)
        dur = spec.get('measurement_duration_ms', 10000) / 1000.0
        intended = int(rate * dur) if rate > 0 else 'free'

        rows.append({
            'trial_id': t['trial_id'],
            'cell': t['cell'],
            'arm': t['arm'],
            'status': t['status'],
            'read_tp': res['throughput']['read_ops_per_sec'],
            'intended_writes': intended,
            'attempted_writes': ops.get('write_ops_attempted', 0),
            'completed_writes': ops.get('write_ops_completed', 0),
            'failed_writes': ops.get('error_ops_count', 0),
            'in_win_writes': ops.get('write_ops_completed_in_window', 0),
            'late_writes': ops.get('write_ops_completed_late', 0),
            'flushes': lsm.get('measured_flushes', 0),
            'compactions': lsm.get('measured_compactions', 0)
        })
    return rows

def reconcile_e4(trials):
    e4_trials = [t for t in trials if t['block'] == 'E4']
    rows = []
    for t in sorted(e4_trials, key=lambda x: (x['cell'], x.get('triplet') or 0, x['arm'])):
        res = t['result']
        ops = res['operations']
        lsm = res['lsm_diagnostics']
        lat_resp = res.get('latency_response_nanos') or {}

        rows.append({
            'trial_id': t['trial_id'],
            'cell': t['cell'],
            'arm': t['arm'],
            'status': t['status'],
            'read_tp': res['throughput']['read_ops_per_sec'],
            'resp_p50_ms': (lat_resp.get('p50') or 0) / 1e6,
            'resp_p95_ms': (lat_resp.get('p95') or 0) / 1e6,
            'resp_mean_ms': (lat_resp.get('mean') or 0) / 1e6,
            'sched': ops.get('read_ops_scheduled', 0),
            'issued': ops.get('read_ops_issued', 0),
            'in_win': ops.get('read_ops_completed_in_window', 0),
            'late': ops.get('read_ops_completed_late', 0),
            'tot_comp': ops.get('read_ops_completed', 0),
            'abandoned': ops.get('read_ops_abandoned', 0),
            'unissued': ops.get('read_ops_unissued', 0),
            'pub_in_win': lsm.get('publication_events_realized_in_window', 0),
            'pub_late': lsm.get('publication_events_realized_late', 0)
        })
    return rows

def generate_manuscript_figures(paired_rows, e2_rows, e3_rows, e4_rows):
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    plt.rcParams.update({
        'font.size': 10,
        'font.family': 'sans-serif',
        'figure.autolayout': True
    })

    # Figure 1: Approach A-min vs Approach B Paired Throughput across 18 triplets
    fig, ax = plt.subplots(figsize=(7, 4))
    triplets = [f"{r['block']}-{r['cell']}\nt{r['triplet']:02d}" for r in paired_rows]
    amin_vals = [r['tp_amin'] for r in paired_rows]
    b_vals = [r['tp_b'] for r in paired_rows]

    x = np.arange(len(triplets))
    width = 0.38

    # Use log scale because E5 is ~3000 while E4 is ~10
    ax.bar(x - width/2, amin_vals, width, label='Approach A-min (Leased)', color='#2b5c8f')
    ax.bar(x + width/2, b_vals, width, label='Approach B (Snapshot)', color='#d95f02')
    ax.set_yscale('log')
    ax.set_ylabel('Read Throughput (ops/sec, log scale)')
    ax.set_title('Phase II (Windows/NTFS): Paired Throughput of A-min vs B Across 18 Matched Triplets')
    ax.set_xticks(x)
    ax.set_xticklabels(triplets, rotation=45, ha='right', fontsize=8)
    ax.legend(frameon=True)
    ax.grid(axis='y', linestyle='--', alpha=0.5)
    fig.savefig(os.path.join(OUTPUT_DIR, 'fig_i7_amin_vs_b_paired.png'), dpi=300)
    plt.close(fig)

    # Figure 2: E3 Matched Offered Write Rate vs Realized Write Work
    fig, ax = plt.subplots(figsize=(6, 3.5))
    e3_cells = ['equal_r01\nt01', 'equal_r01\nt03', 'equal_r01\nt06', 'equal_r04\nt06', 'equal_r04\nt07', 'free_r01\nt01', 'free_r01\nt02']
    
    # Extract writes per cell for A, AMIN, B
    e3_by_cell = defaultdict(dict)
    for r in e3_rows:
        key = f"{r['cell']}\nt{int(r['trial_id'].split('_')[-2][1:]):02d}"
        e3_by_cell[key][r['arm']] = r['completed_writes']

    x = np.arange(len(e3_cells))
    w_a = [e3_by_cell[c].get('A', 0) for c in e3_cells]
    w_amin = [e3_by_cell[c].get('AMIN', 0) for c in e3_cells]
    w_b = [e3_by_cell[c].get('B', 0) for c in e3_cells]

    width = 0.25
    ax.bar(x - width, w_a, width, label='Approach A (Coarse Lock)', color='#7570b3')
    ax.bar(x, w_amin, width, label='Approach A-min (Leased)', color='#2b5c8f')
    ax.bar(x + width, w_b, width, label='Approach B (Snapshot)', color='#d95f02')

    ax.set_ylabel('Completed Write Operations (10s)')
    ax.set_title('Block E3: Disparity in Realized Write Work Under Offered Rates')
    ax.set_xticks(x)
    ax.set_xticklabels(e3_cells, fontsize=8)
    ax.legend(frameon=True)
    ax.grid(axis='y', linestyle='--', alpha=0.5)
    fig.savefig(os.path.join(OUTPUT_DIR, 'fig_i7_e3_write_disparity.png'), dpi=300)
    plt.close(fig)

    # Figure 3: E4 Open-Loop Latency y_p50 across rate levels
    fig, ax = plt.subplots(figsize=(6, 3.5))
    e4_levels = ['L1 (4.86 ops/s)', 'L2 (9.72 ops/s)', 'L3 (14.58 ops/s)']
    # Group by level and arm
    e4_by_level = defaultdict(lambda: defaultdict(list))
    for r in e4_rows:
        lvl = r['cell']
        e4_by_level[lvl][r['arm']].append(r['resp_p50_ms'])

    x = np.arange(3)
    p50_a = [np.mean(e4_by_level['L1']['A']), np.mean(e4_by_level['L2']['A']), np.mean(e4_by_level['L3']['A'])]
    p50_amin = [np.mean(e4_by_level['L1']['AMIN']), np.mean(e4_by_level['L2']['AMIN']), np.mean(e4_by_level['L3']['AMIN'])]
    p50_b = [np.mean(e4_by_level['L1']['B']), np.mean(e4_by_level['L2']['B']), np.mean(e4_by_level['L3']['B'])]

    width = 0.25
    ax.bar(x - width, p50_a, width, label='Approach A (Coarse Lock)', color='#7570b3')
    ax.bar(x, p50_amin, width, label='Approach A-min (Leased)', color='#2b5c8f')
    ax.bar(x + width, p50_b, width, label='Approach B (Snapshot)', color='#d95f02')

    ax.set_ylabel('Mean Response Latency $y_{p50}$ (ms)')
    ax.set_title('Block E4: Open-Loop Response Latency Under Arrival Rates')
    ax.set_xticks(x)
    ax.set_xticklabels(e4_levels)
    ax.legend(frameon=True)
    ax.grid(axis='y', linestyle='--', alpha=0.5)
    fig.savefig(os.path.join(OUTPUT_DIR, 'fig_i7_e4_open_loop_p50.png'), dpi=300)
    plt.close(fig)

    print("Generated 3 figures in", OUTPUT_DIR)

def main():
    print("=== Master I7 Evidence Reproduction ===")
    g4_trials = load_g4_trials()
    print(f"Loaded {len(g4_trials)} G4 trial records.")

    exp_triplets = analyze_g4_triplets(g4_trials)
    print(f"Identified {len(exp_triplets)} complete experimental triplets (E1..E5).")

    paired_rows = run_paired_analysis(exp_triplets)
    e2_rows = reconcile_e2(g4_trials)
    e3_rows = reconcile_e3(g4_trials)
    e4_rows = reconcile_e4(g4_trials)

    # Statistical summary for A-min vs B
    amin_all = np.array([r['tp_amin'] for r in paired_rows])
    b_all = np.array([r['tp_b'] for r in paired_rows])
    stats_all = compute_bootstrap_cis(amin_all, b_all)

    # 14 fully successful subset
    succ_idx = [i for i, r in enumerate(paired_rows) if r['status_a'] == 'SUCCESS']
    amin_succ = amin_all[succ_idx]
    b_succ = b_all[succ_idx]
    stats_succ = compute_bootstrap_cis(amin_succ, b_succ)

    print("\n--- A-min vs B Statistical Synthesis ---")
    print(f"All 18 Triplets: Mean Rel Diff = {stats_all['mean_rel_diff']:+.2%}, 95% CI = [{stats_all['ci_rel_diff'][0]:+.2%}, {stats_all['ci_rel_diff'][1]:+.2%}], Median Ratio = {stats_all['median_ratio']:.4f}")
    print(f"14 Fully Successful Triplets: Mean Rel Diff = {stats_succ['mean_rel_diff']:+.2%}, 95% CI = [{stats_succ['ci_rel_diff'][0]:+.2%}, {stats_succ['ci_rel_diff'][1]:+.2%}], Median Ratio = {stats_succ['median_ratio']:.4f}")

    # Generate figures
    generate_manuscript_figures(paired_rows, e2_rows, e3_rows, e4_rows)

    # Write out a JSON dump of all exact numbers for manuscript embedding
    summary_data = {
        'total_g4_trials': len(g4_trials),
        'complete_experimental_triplets': len(exp_triplets),
        'fully_successful_triplets': len(succ_idx),
        'treatment_failure_triplets': len(paired_rows) - len(succ_idx),
        'amin_vs_b_all_18': {
            'mean_rel_diff': stats_all['mean_rel_diff'],
            'ci_rel_diff': stats_all['ci_rel_diff'].tolist(),
            'ratio_of_means': stats_all['ratio_of_means'],
            'ci_ratio_of_means': stats_all['ci_ratio_of_means'].tolist(),
            'median_ratio': stats_all['median_ratio']
        },
        'amin_vs_b_succ_14': {
            'mean_rel_diff': stats_succ['mean_rel_diff'],
            'ci_rel_diff': stats_succ['ci_rel_diff'].tolist(),
            'ratio_of_means': stats_succ['ratio_of_means'],
            'ci_ratio_of_means': stats_succ['ci_ratio_of_means'].tolist(),
            'median_ratio': stats_succ['median_ratio']
        },
        'paired_triplets': paired_rows,
        'e2_reconciliation': e2_rows,
        'e3_reconciliation': e3_rows,
        'e4_reconciliation': e4_rows
    }
    with open(r'research\experiments\analysis\i7_reproduced_metrics.json', 'w', encoding='utf-8') as f:
        json.dump(summary_data, f, indent=2)

    print("Wrote research/experiments/analysis/i7_reproduced_metrics.json successfully.")

if __name__ == '__main__':
    main()
