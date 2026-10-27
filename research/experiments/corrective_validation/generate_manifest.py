#!/usr/bin/env python3
"""
generate_manifest.py

Generates the preregistered manifest for Phase I6.9: I6_CORRECTIVE_MECHANISMS_V1.
Total: 280 planned trials (140 balanced pairs).
Bounded Preflight: 24 trials (12 balanced pairs, exactly 1 pair per condition).
Enforces:
- Common PairSeed across matched A/B trials.
- Strict 50% A-first / 50% B-first order balancing.
- Topology preservation across churn comparisons (compaction disabled).
- Distinct worker seeds derived independently of trial index.
"""

import json
import os

CAMPAIGN_ID = "I6_CORRECTIVE_MECHANISMS_V1"
PROTOCOL_VERSION = "1.1.0"
BASE_SEED = 20261028

def generate_manifest():
    trials = []
    trial_idx = 0

    # =========================================================================
    # EXPERIMENT T1: Flush Tail Latency & Causal Decomposition (120 trials)
    # =========================================================================
    t1_concurrencies = [2, 4]
    t1_conditions = [
        ("condA_pure_read", 0, False, 0, 64 * 1024 * 1024),
        ("condB_wal_writes_no_flush", 2, False, 0, 64 * 1024 * 1024),
        ("condC_flush_enabled", 0, False, 500, 64 * 1024 * 1024),
    ]
    t1_reps = 10

    for rc in t1_concurrencies:
        for cond_name, writers, sham, flush_ms, mem_thresh in t1_conditions:
            for pair in range(1, t1_reps + 1):
                # Common pair seed for both trials in the pair
                pair_seed = BASE_SEED + (rc * 1000) + (100 if "A" in cond_name else (200 if "B" in cond_name else 300)) + pair
                order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if (pair % 2 != 0) else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]

                for mode in order:
                    mode_short = "A" if "ApproachA" in mode else "B"
                    t_id = f"corr_T1_{cond_name}_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                    trials.append({
                        "campaign_id": CAMPAIGN_ID,
                        "experiment": "T1",
                        "trial_id": t_id,
                        "trial_index": trial_idx,
                        "workload_id": "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE",
                        "condition": cond_name,
                        "sync_mode": mode,
                        "reader_concurrency": rc,
                        "writer_concurrency": writers,
                        "pair_index": pair,
                        "order_in_pair": 1 if mode == order[0] else 2,
                        "pair_seed": pair_seed,
                        "seed": pair_seed,
                        "measurement_duration_ms": 5000,
                        "warmup_duration_ms": 1000,
                        "cooldown_duration_ms": 200,
                        "sham_flush": sham,
                        "flush_trigger_interval_ms": flush_ms,
                        "compaction_trigger_interval_ms": 0,
                        "memtable_threshold_bytes": mem_thresh,
                        "initial_sstable_count": 4,
                        "initial_keys_per_sstable": 2500,
                        "keyspace_cardinality": 10000,
                        "target_read_hit_ratio": 0.95,
                        "wal_sync": True,
                    })
                    trial_idx += 1

    # =========================================================================
    # EXPERIMENT T2: Practical Equivalence via TOST (Workloads A & E) - 80 trials
    # =========================================================================
    t2_workloads = [
        ("WORKLOAD_A_READ_ONLY", "pure_read", 0, 4, 2500, 64 * 1024 * 1024),
        ("WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE", "writer_free_memtable", 0, 0, 0, 64 * 1024 * 1024),
    ]
    t2_concurrencies = [1, 32]
    t2_reps = 10

    for w_id, cond_name, writers, sst_cnt, keys_per_sst, mem_thresh in t2_workloads:
        w_short = "A" if "WORKLOAD_A" in w_id else "E"
        for rc in t2_concurrencies:
            for pair in range(1, t2_reps + 1):
                pair_seed = BASE_SEED + 50000 + (10000 if w_short == "A" else 20000) + (rc * 100) + pair
                order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if (pair % 2 != 0) else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
                for mode in order:
                    mode_short = "A" if "ApproachA" in mode else "B"
                    t_id = f"corr_T2_{w_short}_{cond_name}_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                    trials.append({
                        "campaign_id": CAMPAIGN_ID,
                        "experiment": "T2",
                        "trial_id": t_id,
                        "trial_index": trial_idx,
                        "workload_id": w_id,
                        "condition": cond_name,
                        "sync_mode": mode,
                        "reader_concurrency": rc,
                        "writer_concurrency": writers,
                        "pair_index": pair,
                        "order_in_pair": 1 if mode == order[0] else 2,
                        "pair_seed": pair_seed,
                        "seed": pair_seed,
                        "measurement_duration_ms": 5000,
                        "warmup_duration_ms": 1000,
                        "cooldown_duration_ms": 200,
                        "sham_flush": False,
                        "flush_trigger_interval_ms": 0,
                        "compaction_trigger_interval_ms": 0,
                        "memtable_threshold_bytes": mem_thresh,
                        "initial_sstable_count": sst_cnt,
                        "initial_keys_per_sstable": keys_per_sst,
                        "keyspace_cardinality": 10000,
                        "target_read_hit_ratio": 0.95,
                        "wal_sync": True,
                    })
                    trial_idx += 1

    # =========================================================================
    # EXPERIMENT T3: Rapid Churn Overhead Decomposition (80 trials)
    # =========================================================================
    # 1. Writer-Free Churn (rc = 1, 4, 16) - 60 trials
    for rc in [1, 4, 16]:
        for pair in range(1, 11): # 10 pairs
            pair_seed = BASE_SEED + 80000 + (rc * 100) + pair
            order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if (pair % 2 != 0) else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
            for mode in order:
                mode_short = "A" if "ApproachA" in mode else "B"
                t_id = f"corr_T3_writer_free_churn_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                trials.append({
                    "campaign_id": CAMPAIGN_ID,
                    "experiment": "T3",
                    "trial_id": t_id,
                    "trial_index": trial_idx,
                    "workload_id": "WORKLOAD_D_READER_CHURN",
                    "condition": "writer_free_churn",
                    "sync_mode": mode,
                    "reader_concurrency": rc,
                    "writer_concurrency": 0, # WRITER-FREE
                    "pair_index": pair,
                    "order_in_pair": 1 if mode == order[0] else 2,
                    "pair_seed": pair_seed,
                    "seed": pair_seed,
                    "measurement_duration_ms": 5000,
                    "warmup_duration_ms": 1000,
                    "cooldown_duration_ms": 200,
                    "sham_flush": False,
                    "flush_trigger_interval_ms": 0,
                    "compaction_trigger_interval_ms": 0, # TOPOLOGY FROZEN
                    "memtable_threshold_bytes": 64 * 1024 * 1024,
                    "initial_sstable_count": 4,
                    "initial_keys_per_sstable": 2500,
                    "keyspace_cardinality": 10000,
                    "target_read_hit_ratio": 0.95,
                    "wal_sync": True,
                })
                trial_idx += 1

    # 2. Equal-Work Paced Churn (rc = 4, w = 1 paced) - 20 trials
    for pair in range(1, 11):
        pair_seed = BASE_SEED + 90000 + (4 * 100) + pair
        order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if (pair % 2 != 0) else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
        for mode in order:
            mode_short = "A" if "ApproachA" in mode else "B"
            t_id = f"corr_T3_equal_work_churn_r04_pair{pair:02d}_mode{mode_short}"
            trials.append({
                "campaign_id": CAMPAIGN_ID,
                "experiment": "T3",
                "trial_id": t_id,
                "trial_index": trial_idx,
                "workload_id": "WORKLOAD_D_READER_CHURN",
                "condition": "equal_work_churn",
                "sync_mode": mode,
                "reader_concurrency": 4,
                "writer_concurrency": 1,
                "pair_index": pair,
                "order_in_pair": 1 if mode == order[0] else 2,
                "pair_seed": pair_seed,
                "seed": pair_seed,
                "measurement_duration_ms": 5000,
                "warmup_duration_ms": 1000,
                "cooldown_duration_ms": 200,
                "sham_flush": False,
                "flush_trigger_interval_ms": 0,
                "compaction_trigger_interval_ms": 0, # TOPOLOGY FROZEN
                "memtable_threshold_bytes": 64 * 1024 * 1024,
                "initial_sstable_count": 4,
                "initial_keys_per_sstable": 2500,
                "keyspace_cardinality": 10000,
                "target_read_hit_ratio": 0.95,
                "wal_sync": True,
            })
            trial_idx += 1

    manifest = {
        "schema_version": "1.0.0",
        "protocol_version": PROTOCOL_VERSION,
        "campaign_id": CAMPAIGN_ID,
        "total_planned_trials": len(trials),
        "preflight_trials": 24, # Exactly 12 balanced pairs (1 pair per cell)
        "experiment_breakdown": {
            "T1_flush_causal_decomposition": 120,
            "T2_tost_practical_equivalence": 80,
            "T3_churn_overhead_decomposition": 80,
        },
        "trials": trials
    }

    manifest_path = os.path.join(os.path.dirname(__file__), "manifest.json")
    with open(manifest_path, 'w', encoding='utf-8') as f:
        json.dump(manifest, f, indent=2)

    print(f"Generated corrective manifest with {len(trials)} trials at {manifest_path}")

    # Integrity assertions
    assert len(trials) == 280, f"Expected 280 trials, got {len(trials)}"
    trial_ids = [t['trial_id'] for t in trials]
    assert len(set(trial_ids)) == 280, "Duplicate trial ID found!"
    print("Manifest assertions passed!")

if __name__ == "__main__":
    generate_manifest()
