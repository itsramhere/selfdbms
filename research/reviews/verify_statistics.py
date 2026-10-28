import os
import json
import glob
import numpy as np

def load_960_data():
    base_dir = 'research/experiments/data/campaign_wsl2_ext4_restart_v1/wsl2_ext4'
    files = glob.glob(os.path.join(base_dir, '*.json'))
    print(f"Total raw JSON files found: {len(files)}")
    
    records = []
    for f in files:
        if 'manifest' in f or 'results' in f:
            continue
        with open(f, 'r') as fp:
            d = json.load(fp)
            records.append(d)
    print(f"Total valid trial records loaded: {len(records)}")
    return records

def analyze_cells(records):
    # Group by (workload, concurrency, mode)
    groups = {}
    for r in records:
        wl = r.get('workload_name', r.get('workload', ''))
        rc = r.get('reader_concurrency', r.get('concurrency', 0))
        mode = r.get('sync_mode', r.get('mode', ''))
        
        # Normalize workload name
        wl_norm = wl
        if 'WORKLOAD_A' in wl: wl_norm = 'A'
        elif 'WORKLOAD_B1' in wl: wl_norm = 'B1'
        elif 'WORKLOAD_B2' in wl: wl_norm = 'B2'
        elif 'WORKLOAD_C' in wl: wl_norm = 'C'
        elif 'WORKLOAD_D' in wl: wl_norm = 'D'
        elif 'WORKLOAD_E' in wl: wl_norm = 'E'
        
        key = (wl_norm, rc, mode)
        if key not in groups:
            groups[key] = []
        groups[key].append(r)
        
    print(f"Total experimental cells: {len(groups)}")
    
    # Check sample size per cell
    sample_sizes = [len(v) for v in groups.values()]
    print(f"Sample sizes per cell: min={min(sample_sizes)}, max={max(sample_sizes)}")
    
    # Let's inspect Workload B1 at r=4 and r=8
    for rc in [1, 2, 4, 8, 16, 24, 32]:
        cell_A = groups.get(('B1', rc, 'A'), [])
        cell_B = groups.get(('B1', rc, 'B'), [])
        
        tput_A = [t['throughput_ops_per_sec'] for t in cell_A]
        tput_B = [t['throughput_ops_per_sec'] for t in cell_B]
        writes_A = [t.get('total_writes_completed', t.get('operations_count', {}).get('writes_completed', 0)) for t in cell_A]
        writes_B = [t.get('total_writes_completed', t.get('operations_count', {}).get('writes_completed', 0)) for t in cell_B]
        
        med_tput_A = np.median(tput_A) if tput_A else 0
        med_tput_B = np.median(tput_B) if tput_B else 0
        med_w_A = np.median(writes_A) if writes_A else 0
        med_w_B = np.median(writes_B) if writes_B else 0
        
        print(f"B1 r={rc:2d}: Tput A={med_tput_A:.1f}, B={med_tput_B:.1f} (RoM={med_tput_B/med_tput_A:.2f}x) | Writes A={med_w_A:.0f}, B={med_w_B:.0f} (RoM={med_w_B/med_w_A:.1f}x)")

if __name__ == '__main__':
    recs = load_960_data()
    analyze_cells(recs)
