#!/usr/bin/env python3
"""
generate_manifest.py

Generates the preregistered trial manifest for Phase I6.8: I6_TARGETED_VALIDATION_V1.
Total: 600 trials across T1, T2, and T3.
Ensures:
- Paired block structure with common input seeds.
- Strict 50% / 50% order alternation (A-then-B vs B-then-A).
- Unique trial IDs.
"""

import json
import os

CAMPAIGN_ID = "I6_TARGETED_VALIDATION_V1"
BASE_SEED = 20261027

def generate_manifest():
    trials = []
    trial_idx = 0

    # =========================================================================
    # EXPERIMENT T1: Tail Latency Verification (Workload B2) - 240 trials
    # =========================================================================
    t1_concurrencies = [2, 4, 8]
    t1_conditions = ["flush_enabled", "sham_flush"]
    t1_reps = 20

    for rc in t1_concurrencies:
        for cond in t1_conditions:
            for pair in range(1, t1_reps + 1):
                # Deterministic common seed for the pair
                seed = BASE_SEED + (rc * 1000) + (100 if cond == "flush_enabled" else 200) + pair
                # Balanced order: odd pairs = A-first, even pairs = B-first
                order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if (pair % 2 != 0) else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
                
                for mode in order:
                    mode_short = "A" if "ApproachA" in mode else "B"
                    t_id = f"targeted_T1_B2_{cond}_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                    trials.append({
                        "campaign_id": CAMPAIGN_ID,
                        "experiment": "T1",
                        "trial_id": t_id,
                        "trial_index": trial_idx,
                        "workload_id": "WORKLOAD_B2_CONTROLLED_FLUSH_INTERFERENCE",
                        "condition": cond,
                        "sync_mode": mode,
                        "reader_concurrency": rc,
                        "writer_concurrency": 0, # Driven by flush coordinator
                        "pair_index": pair,
                        "order_in_pair": 1 if mode == order[0] else 2,
                        "seed": seed,
                        "measurement_duration_ms": 5000,
                        "warmup_duration_ms": 1000,
                        "cooldown_duration_ms": 200,
                        "sham_flush": (cond == "sham_flush"),
                        "flush_trigger_interval_ms": 500,
                        "memtable_threshold_bytes": 64 * 1024 * 1024,
                        "initial_sstable_count": 4,
                        "initial_keys_per_sstable": 2500,
                        "keyspace_cardinality": 10000,
                        "target_read_hit_ratio": 0.95,
                        "wal_sync": True,
                    })
                    trial_idx += 1

    # =========================================================================
    # EXPERIMENT T2: Practical Equivalence via TOST (Workloads A & E) - 160 trials
    # =========================================================================
    t2_workloads = [
        ("WORKLOAD_A_READ_ONLY", "standard", 0, 4, 2500, 64 * 1024 * 1024),
        ("WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE", "standard", 2, 0, 0, 64 * 1024 * 1024),
    ]
    t2_concurrencies = [1, 32]
    t2_reps = 20

    for w_id, cond, writers, sst_cnt, keys_per_sst, mem_thresh in t2_workloads:
        w_short = "A" if "WORKLOAD_A" in w_id else "E"
        for rc in t2_concurrencies:
            for pair in range(1, t2_reps + 1):
                seed = BASE_SEED + 50000 + (10000 if w_short == "A" else 20000) + (rc * 100) + pair
                order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if (pair % 2 != 0) else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
                for mode in order:
                    mode_short = "A" if "ApproachA" in mode else "B"
                    t_id = f"targeted_T2_{w_short}_{cond}_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                    trials.append({
                        "campaign_id": CAMPAIGN_ID,
                        "experiment": "T2",
                        "trial_id": t_id,
                        "trial_index": trial_idx,
                        "workload_id": w_id,
                        "condition": cond,
                        "sync_mode": mode,
                        "reader_concurrency": rc,
                        "writer_concurrency": writers,
                        "pair_index": pair,
                        "order_in_pair": 1 if mode == order[0] else 2,
                        "seed": seed,
                        "measurement_duration_ms": 5000,
                        "warmup_duration_ms": 1000,
                        "cooldown_duration_ms": 200,
                        "sham_flush": False,
                        "flush_trigger_interval_ms": 0,
                        "memtable_threshold_bytes": mem_thresh,
                        "initial_sstable_count": sst_cnt,
                        "initial_keys_per_sstable": keys_per_sst,
                        "keyspace_cardinality": 10000,
                        "target_read_hit_ratio": 0.95,
                        "wal_sync": True,
                    })
                    trial_idx += 1

    # =========================================================================
    # EXPERIMENT T2 Diagnostic: Workload E Writer-Free (w=0) - 20 trials
    # =========================================================================
    for rc_idx, rc in enumerate([1, 32]):
        for pair in range(1, 6): # 5 pairs
            seed = BASE_SEED + 80000 + (rc * 100) + pair
            # Alternate start per concurrency to keep 5 A-first and 5 B-first across the 10 pairs
            is_a_first = ((pair + rc_idx) % 2 != 0)
            order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if is_a_first else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
            for mode in order:
                mode_short = "A" if "ApproachA" in mode else "B"
                t_id = f"targeted_T2_E_writer_free_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                trials.append({
                    "campaign_id": CAMPAIGN_ID,
                    "experiment": "T2_DIAGNOSTIC",
                    "trial_id": t_id,
                    "trial_index": trial_idx,
                    "workload_id": "WORKLOAD_E_NEGATIVE_CONTROL_MEMTABLE",
                    "condition": "writer_free",
                    "sync_mode": mode,
                    "reader_concurrency": rc,
                    "writer_concurrency": 0, # WRITER-FREE DIAGNOSTIC
                    "pair_index": pair,
                    "order_in_pair": 1 if mode == order[0] else 2,
                    "seed": seed,
                    "measurement_duration_ms": 5000,
                    "warmup_duration_ms": 1000,
                    "cooldown_duration_ms": 200,
                    "sham_flush": False,
                    "flush_trigger_interval_ms": 0,
                    "memtable_threshold_bytes": 64 * 1024 * 1024,
                    "initial_sstable_count": 0,
                    "initial_keys_per_sstable": 0,
                    "keyspace_cardinality": 10000,
                    "target_read_hit_ratio": 0.95,
                    "wal_sync": True,
                })
                trial_idx += 1

    # =========================================================================
    # EXPERIMENT T3: Rapid Churn Overhead (Workload D) - 180 trials
    # =========================================================================
    t3_concurrencies = [1, 4, 16]
    t3_conditions = [
        ("controlled_churn", 16 * 1024, 600),
        ("no_churn_control", 64 * 1024 * 1024, 0),
    ]
    t3_reps = 15

    cell_counter = 0
    for rc in t3_concurrencies:
        for cond, mem_thresh, comp_interval in t3_conditions:
            cell_counter += 1
            for pair in range(1, t3_reps + 1):
                seed = BASE_SEED + 90000 + (1000 if cond == "controlled_churn" else 2000) + (rc * 100) + pair
                # Alternate offset across the 6 cells so 3 cells have 8 A-first and 3 cells have 7 A-first
                is_a_first = ((pair + cell_counter) % 2 != 0)
                order = ["ApproachA_RWMutexBaseline", "ApproachB_ReaderSnapshot"] if is_a_first else ["ApproachB_ReaderSnapshot", "ApproachA_RWMutexBaseline"]
                for mode in order:
                    mode_short = "A" if "ApproachA" in mode else "B"
                    t_id = f"targeted_T3_D_{cond}_r{rc:02d}_pair{pair:02d}_mode{mode_short}"
                    trials.append({
                        "campaign_id": CAMPAIGN_ID,
                        "experiment": "T3",
                        "trial_id": t_id,
                        "trial_index": trial_idx,
                        "workload_id": "WORKLOAD_D_READER_CHURN",
                        "condition": cond,
                        "sync_mode": mode,
                        "reader_concurrency": rc,
                        "writer_concurrency": 3,
                        "pair_index": pair,
                        "order_in_pair": 1 if mode == order[0] else 2,
                        "seed": seed,
                        "measurement_duration_ms": 5000,
                        "warmup_duration_ms": 1000,
                        "cooldown_duration_ms": 200,
                        "sham_flush": False,
                        "flush_trigger_interval_ms": 0,
                        "compaction_trigger_interval_ms": comp_interval,
                        "memtable_threshold_bytes": mem_thresh,
                        "initial_sstable_count": 4,
                        "initial_keys_per_sstable": 2500,
                        "keyspace_cardinality": 10000,
                        "target_read_hit_ratio": 0.95,
                        "wal_sync": True,
                    })
                    trial_idx += 1

    manifest = {
        "schema_version": "1.0.0",
        "campaign_id": CAMPAIGN_ID,
        "total_planned_trials": len(trials),
        "experiment_breakdown": {
            "T1_flush_vs_sham": 240,
            "T2_practical_equivalence": 160,
            "T2_writer_free_diagnostic": 20,
            "T3_churn_vs_no_churn": 180,
        },
        "trials": trials
    }

    manifest_path = os.path.join(os.path.dirname(__file__), "manifest.json")
    with open(manifest_path, 'w', encoding='utf-8') as f:
        json.dump(manifest, f, indent=2)

    print(f"Generated manifest with {len(trials)} trials at {manifest_path}")

    # Audit assertions
    assert len(trials) == 600, f"Expected 600 trials, got {len(trials)}"
    trial_ids = [t['trial_id'] for t in trials]
    assert len(set(trial_ids)) == 600, "Duplicate trial ID found!"
    print("All manifest assertions passed!")

if __name__ == "__main__":
    generate_manifest()
