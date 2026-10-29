#!/usr/bin/env python3
"""
Phase I7 - Checkpoint R1.2a: Verified, Portable, Fail-Closed Statistical Reconciliation Script

Implements read-only statistical reproduction conforming strictly to 07_ANALYSIS_PLAN.md.
Features:
- Portable CLI arguments (--data-dir, --output-dir) with automatic fallback discovery.
- Fail-closed execution: terminates with non-zero exit code if archives/trials are missing or empty.
- Preregistered paired log-ratio metrics (geometric mean ratios, bootstrap 95% CIs).
- Sensitivity cohorts (All 18, Exclude E5, Exclude E4, Exclude E4+E5, Sub-blocks E1..E5).
- Multi-cohort statistical heterogeneity assessment.
"""

import os
import sys
import glob
import json
import math
import argparse
import numpy as np

def discover_data_dir(cli_dir=None):
    """Locate G4 results directory portably, failing closed if unavailable."""
    candidates = []
    if cli_dir:
        candidates.append(cli_dir)
    if os.environ.get('I7_DATA_DIR'):
        candidates.append(os.environ.get('I7_DATA_DIR'))
    
    # Candidate relative paths from repository root and script location
    script_dir = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.abspath(os.path.join(script_dir, '..', '..', '..'))
    candidates.extend([
        os.path.join(repo_root, 'I7_WINDOWS_G4_RESULTS'),
        r'C:\Users\ramst\I7_WINDOWS_G4_RESULTS',
        os.path.join(os.path.expanduser('~'), 'I7_WINDOWS_G4_RESULTS')
    ])

    for c in candidates:
        if c and os.path.isdir(c):
            # Verify it contains trial records
            pattern = os.path.join(c, 'trials', 'attempts', '*_a01.json')
            files = glob.glob(pattern)
            if len(files) >= 50:
                return os.path.abspath(c)

    sys.stderr.write(
        "FAIL-CLOSED ERROR: Unable to locate valid G4 results directory.\n"
        "Pass --data-dir <path> or set I7_DATA_DIR environment variable.\n"
        f"Checked candidates:\n" + "\n".join(f"  - {c}" for c in candidates) + "\n"
    )
    sys.exit(1)

def load_trial_records(data_dir):
    """Load and validate all attempt JSON files, failing closed on schema or count errors."""
    pattern = os.path.join(data_dir, 'trials', 'attempts', '*_a01.json')
    files = sorted(glob.glob(pattern))
    if len(files) != 60:
        sys.stderr.write(
            f"FAIL-CLOSED ERROR: Expected exactly 60 trial records in G4 root, found {len(files)}.\n"
        )
        sys.exit(1)

    trials = []
    for fpath in files:
        with open(fpath, 'r', encoding='utf-8') as f:
            try:
                record = json.load(f)
            except Exception as e:
                sys.stderr.write(f"FAIL-CLOSED ERROR: Corrupted JSON in {fpath}: {e}\n")
                sys.exit(1)

        # Basic schema integrity validation
        required_fields = ['trial_id', 'block', 'cell', 'arm', 'status']
        for rf in required_fields:
            if rf not in record:
                sys.stderr.write(f"FAIL-CLOSED ERROR: Missing required field '{rf}' in {fpath}\n")
                sys.exit(1)
        # If trial claims SUCCESS, it must have 'result'
        if record['status'] == 'SUCCESS' and 'result' not in record:
            sys.stderr.write(f"FAIL-CLOSED ERROR: Trial marked SUCCESS without 'result' in {fpath}\n")
            sys.exit(1)
        trials.append(record)
    return trials

def group_complete_triplets(trials):
    """Group trials by (block, cell, triplet) across experimental blocks E1..E5."""
    triplets = {}
    for t in trials:
        b = t['block']
        cell = t['cell']
        trip = t.get('triplet') or (t.get('spec') or {}).get('triplet', 0)
        # Exclude harness failure controls (D3) and invalid baseline runs (E7)
        if b in ('D3', 'E7'):
            continue
        key = (b, cell, trip)
        if key not in triplets:
            triplets[key] = {}
        triplets[key][t['arm']] = t

    complete_triplets = []
    for k, arms in sorted(triplets.items()):
        if 'A' in arms and 'AMIN' in arms and 'B' in arms:
            complete_triplets.append((k, arms))

    if len(complete_triplets) != 18:
        sys.stderr.write(
            f"FAIL-CLOSED ERROR: Expected exactly 18 complete triplets, found {len(complete_triplets)}.\n"
        )
        sys.exit(1)
    return complete_triplets

def compute_triplet_metrics(complete_triplets):
    """Extract throughput, status, log ratios, and relative differences per triplet."""
    rows = []
    for (b, cell, trip), arms in complete_triplets:
        tp_a = arms['A']['result']['throughput']['read_ops_per_sec']
        tp_amin = arms['AMIN']['result']['throughput']['read_ops_per_sec']
        tp_b = arms['B']['result']['throughput']['read_ops_per_sec']

        st_a = arms['A']['status']
        st_amin = arms['AMIN']['status']
        st_b = arms['B']['status']

        if tp_b <= 0 or tp_amin <= 0:
            sys.stderr.write(f"FAIL-CLOSED ERROR: Non-positive throughput in triplet {b}-{cell}-t{trip:02d}\n")
            sys.exit(1)

        d_log = math.log(tp_amin / tp_b)
        rel_diff = (tp_amin - tp_b) / tp_b
        ratio = tp_amin / tp_b

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
            'd_log': d_log,
            'rel_diff': rel_diff,
            'ratio': ratio
        })
    return rows

def analyze_cohort(rows, seed=20261101, n_boot=10000):
    """Perform preregistered geometric mean ratio and relative difference analysis."""
    N = len(rows)
    if N == 0:
        return None

    log_diffs = np.array([r['d_log'] for r in rows])
    rel_diffs = np.array([r['rel_diff'] for r in rows])
    ratios = np.array([r['ratio'] for r in rows])

    geom_mean_ratio = math.exp(float(np.mean(log_diffs)))
    median_ratio = float(np.median(ratios))
    mean_rel_diff = float(np.mean(rel_diffs))

    rng = np.random.default_rng(seed)
    boot_geom = []
    boot_rel = []
    boot_med = []

    for _ in range(n_boot):
        idx = rng.choice(N, size=N, replace=True)
        boot_geom.append(math.exp(float(np.mean(log_diffs[idx]))))
        boot_rel.append(float(np.mean(rel_diffs[idx])))
        boot_med.append(float(np.median(ratios[idx])))

    ci_geom = [float(x) for x in np.percentile(boot_geom, [2.5, 97.5])]
    ci_rel = [float(x) for x in np.percentile(boot_rel, [2.5, 97.5])]
    ci_med = [float(x) for x in np.percentile(boot_med, [2.5, 97.5])]

    # Equivalence within +-5% check
    equiv_rel_5pct = (ci_rel[0] >= -0.05 and ci_rel[1] <= 0.05)
    # Non-inferiority check (> 0.90 margin)
    ni_geom_10pct = (ci_geom[0] > 0.90)

    return {
        'N': N,
        'geom_mean_ratio': geom_mean_ratio,
        'ci_geom_95': ci_geom,
        'median_ratio': median_ratio,
        'ci_med_95': ci_med,
        'mean_rel_diff': mean_rel_diff,
        'ci_rel_diff_95': ci_rel,
        'equiv_rel_5pct_supported': equiv_rel_5pct,
        'ni_geom_10pct_supported': ni_geom_10pct
    }

def main():
    parser = argparse.ArgumentParser(description="Phase I7 Checkpoint R1.2a Portable Statistical Reconciliation")
    parser.add_argument('--data-dir', type=str, default=None, help="Path to G4 results root")
    parser.add_argument('--output-dir', type=str, default=None, help="Path to write JSON output")
    args = parser.parse_args()

    data_dir = discover_data_dir(args.data_dir)
    print(f"[*] Validated G4 Data Root: {data_dir}")

    trials = load_trial_records(data_dir)
    print(f"[*] Loaded and validated {len(trials)} trial records.")

    complete_triplets = group_complete_triplets(trials)
    triplet_data = compute_triplet_metrics(complete_triplets)

    # Define sensitivity cohorts
    cohorts = {
        "cohort_1_all_18": {
            "name": "1. All 18 Triplets (Full Observed Pooled)",
            "data": triplet_data,
            "description": "Pooled across all 18 complete triplets across E1..E5."
        },
        "cohort_2_successful_14": {
            "name": "2. Fully Successful Triplets (N=14)",
            "data": [r for r in triplet_data if r['status_a'] == 'SUCCESS'],
            "description": "Excludes the 4 triplets where Approach A suffered treatment deadline failures."
        },
        "cohort_3_exclude_e5": {
            "name": "3. Exclude Block E5 (N=17)",
            "data": [r for r in triplet_data if r['block'] != 'E5'],
            "description": "Excludes E5 due to MC-5 keyspace mismatch & single-block page cache pinning."
        },
        "cohort_4_exclude_e4": {
            "name": "4. Exclude Block E4 (N=13)",
            "data": [r for r in triplet_data if r['block'] != 'E4'],
            "description": "Excludes E4 open-loop arrival-capped throughput (artificially fixed to 1.0000)."
        },
        "cohort_5_exclude_e4_e5": {
            "name": "5. Exclude Both E4 and E5 (N=12)",
            "data": [r for r in triplet_data if r['block'] not in ('E4', 'E5')],
            "description": "True closed-loop read-write workloads (E1, E2, E3 only)."
        },
        "subblock_e1": {
            "name": "Sub-block E1 (r=8 Mixed Read-Write)",
            "data": [r for r in triplet_data if r['block'] == 'E1'],
            "description": "Primary write-progress and read-throughput block."
        },
        "subblock_e2": {
            "name": "Sub-block E2 (r=4 Publication Flushes)",
            "data": [r for r in triplet_data if r['block'] == 'E2'],
            "description": "pub_none and pub_physical publication timing contrasts."
        },
        "subblock_e3": {
            "name": "Sub-block E3 (r=4 Paced Writes)",
            "data": [r for r in triplet_data if r['block'] == 'E3'],
            "description": "equal_r01, equal_r04, and free_r01 write progress blocks."
        },
        "subblock_e4": {
            "name": "Sub-block E4 (r=16 Open-Loop Arrivals)",
            "data": [r for r in triplet_data if r['block'] == 'E4'],
            "description": "L1, L2, L3 offered arrival load response latencies."
        },
        "subblock_e5": {
            "name": "Sub-block E5 (r=16 All-Miss Read Path)",
            "data": [r for r in triplet_data if r['block'] == 'E5'],
            "description": "hit0 all-miss read path (invalid manipulation)."
        }
    }

    results = {}
    print("\n" + "="*80)
    print("PHASE I7 CHECKPOINT R1.2A: SENSITIVITY RECONCILIATION SUMMARY")
    print("="*80)

    for cid, cinfo in cohorts.items():
        res = analyze_cohort(cinfo['data'])
        results[cid] = {
            'name': cinfo['name'],
            'description': cinfo['description'],
            'metrics': res
        }
        if res:
            ci_g = res['ci_geom_95']
            ci_r = res['ci_rel_diff_95']
            print(f"\n[{cinfo['name']}] (N={res['N']})")
            print(f"  Geom Mean Ratio (AMIN/B): {res['geom_mean_ratio']:.4f}  (95% CI: [{ci_g[0]:.4f}, {ci_g[1]:.4f}])")
            print(f"  Median Ratio (AMIN/B):    {res['median_ratio']:.4f}  (95% CI: [{res['ci_med_95'][0]:.4f}, {res['ci_med_95'][1]:.4f}])")
            print(f"  Mean Rel Diff (AMIN-B)/B: {res['mean_rel_diff']*100:+.2f}% (95% CI: [{ci_r[0]*100:+.2f}%, {ci_r[1]*100:+.2f}%])")
            print(f"  Equivalence in [-5%, +5%]: {'SUPPORTED' if res['equiv_rel_5pct_supported'] else 'NOT SUPPORTED'}")
            print(f"  Non-Inferiority (> 0.90):   {'SUPPORTED' if res['ni_geom_10pct_supported'] else 'NOT SUPPORTED'}")

    # Output JSON report if requested or to default research directory
    out_dir = args.output_dir or os.path.join(os.path.dirname(data_dir), 'research', 'experiments', 'analysis')
    if not os.path.isdir(out_dir):
        out_dir = os.path.dirname(os.path.abspath(__file__))

    out_file = os.path.join(out_dir, 'r1_2a_sensitivity_results.json')
    with open(out_file, 'w', encoding='utf-8') as f:
        json.dump(results, f, indent=2)
    print(f"\n[*] Reconciled statistical report written to: {out_file}")

if __name__ == '__main__':
    main()
