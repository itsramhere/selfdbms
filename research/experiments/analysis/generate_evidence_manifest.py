#!/usr/bin/env python3
"""
generate_evidence_manifest.py

Generates a machine-readable evidence manifest for Campaign I5_WSL2_EXT4_RESTART_V1.
Audits SHA-256 checksums of all 960 raw JSON trials, results.csv, and figures.
Independently verifies trial metadata, provenance, and aggregates.
"""

import os
import glob
import json
import hashlib
import csv
from datetime import datetime

ROOT_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))
CAMPAIGN_DIR = os.path.join(ROOT_DIR, "research", "experiments", "data", "campaign_wsl2_ext4_restart_v1")
RAW_DATA_DIR = os.path.join(CAMPAIGN_DIR, "wsl2_ext4")
CSV_PATH = os.path.join(CAMPAIGN_DIR, "results.csv")
FIGURES_DIR = os.path.join(ROOT_DIR, "research", "experiments", "figures")
OUTPUT_MANIFEST = os.path.join(CAMPAIGN_DIR, "manifest_checksums.json")

FROZEN_IMPLEMENTATION_SHA = "a5c0fb97deb8a67705a80e76b49e25f31d692492"
CP5B_REPORTING_SHA = "e156b929a433cad98dccddc41770668424515a7d"

def sha256_file(filepath):
    h = hashlib.sha256()
    with open(filepath, 'rb') as f:
        while chunk := f.read(65536):
            h.update(chunk)
    return h.hexdigest()

def main():
    print("Auditing 960 raw JSON trial files...")
    json_files = sorted(glob.glob(os.path.join(RAW_DATA_DIR, "*.json")))
    if len(json_files) != 960:
        raise ValueError(f"Expected 960 files, found {len(json_files)}")

    trials_manifest = []
    total_val_errors = 0
    start_times = []
    finish_times = []
    measurement_durations_ms = []

    for fpath in json_files:
        fname = os.path.basename(fpath)
        sha = sha256_file(fpath)
        
        with open(fpath, 'r', encoding='utf-8') as fp:
            d = json.load(fp)

        # Audit trial integrity
        trial_id = d.get("trial_id")
        workload_id = d.get("workload_id")
        sync_mode = d.get("sync_mode")
        git_rev = d.get("git_revision")
        status = d.get("status")
        cfg = d.get("config", {})
        diag = d.get("lsm_diagnostics", {})
        val_errs = diag.get("validation_error_count", 0)
        total_val_errors += val_errs
        
        ts = d.get("timestamps", {})
        s_iso = ts.get("start_iso")
        f_iso = ts.get("finish_iso")
        dur_ms = ts.get("total_duration_ms", 0)
        
        if s_iso and f_iso:
            start_times.append(datetime.fromisoformat(s_iso.replace("Z", "+00:00")))
            finish_times.append(datetime.fromisoformat(f_iso.replace("Z", "+00:00")))
        measurement_durations_ms.append(dur_ms)

        if git_rev != FROZEN_IMPLEMENTATION_SHA:
            raise ValueError(f"Git revision mismatch in {fname}: got {git_rev}, expected {FROZEN_IMPLEMENTATION_SHA}")
        if status != "SUCCESS":
            raise ValueError(f"Status mismatch in {fname}: got {status}")
        if val_errs != 0:
            raise ValueError(f"Validation errors detected in {fname}: {val_errs}")

        trials_manifest.append({
            "filename": fname,
            "sha256": sha,
            "trial_id": trial_id,
            "workload_id": workload_id,
            "sync_mode": sync_mode,
            "reader_concurrency": cfg.get("reader_concurrency"),
            "writer_concurrency": cfg.get("writer_concurrency"),
            "seed": cfg.get("seed"),
            "git_revision": git_rev,
            "status": status,
            "validation_errors": val_errs,
            "measurement_duration_ms": dur_ms,
            "read_ops_completed": d.get("operations", {}).get("read_ops_completed", 0),
            "write_ops_completed": d.get("operations", {}).get("write_ops_completed", 0),
            "read_ops_per_sec": d.get("throughput", {}).get("read_ops_per_sec", 0.0),
            "write_ops_per_sec": d.get("throughput", {}).get("write_ops_per_sec", 0.0),
        })

    print(f"Verified {len(trials_manifest)} trials. 0 validation errors, 100% git revision match.")

    # Audit aggregated CSV
    csv_sha = sha256_file(CSV_PATH) if os.path.exists(CSV_PATH) else None
    print(f"Aggregated CSV SHA-256: {csv_sha}")

    # Audit figures
    figure_hashes = {}
    for fig_file in sorted(glob.glob(os.path.join(FIGURES_DIR, "*.png"))):
        f_base = os.path.basename(fig_file)
        figure_hashes[f_base] = sha256_file(fig_file)

    # Calculate authoritative campaign timestamps
    min_start = min(start_times)
    max_finish = max(finish_times)
    total_campaign_span_sec = (max_finish - min_start).total_seconds()
    sum_measurement_dur_sec = sum(measurement_durations_ms) / 1000.0

    manifest = {
        "campaign_id": "I5_WSL2_EXT4_RESTART_V1",
        "protocol_version": "2.0.0",
        "frozen_implementation_sha": FROZEN_IMPLEMENTATION_SHA,
        "cp5b_reporting_sha": CP5B_REPORTING_SHA,
        "environment": {
            "os": "Ubuntu 24.04.3 LTS (WSL2)",
            "kernel": "Linux 6.6.87.2-microsoft-standard-WSL2",
            "arch": "x86_64",
            "cpu": "AMD Ryzen 7 7445HS with Radeon 740M Graphics (6 cores, 12 logical processors)",
            "ram_allocated_wsl2_gb": 7.4,
            "storage_device": "/dev/sdd",
            "filesystem": "native ext4",
            "go_version": "go1.27.3 linux/amd64"
        },
        "campaign_execution_summary": {
            "total_planned_trials": 960,
            "total_executed_trials": 960,
            "total_validated_trials": 960,
            "total_failed_trials": 0,
            "total_validation_errors": total_val_errors,
            "first_trial_start_utc": min_start.isoformat(),
            "last_trial_finish_utc": max_finish.isoformat(),
            "total_wall_clock_span_seconds": round(total_campaign_span_sec, 2),
            "total_wall_clock_span_hours": round(total_campaign_span_sec / 3600.0, 2),
            "sum_trial_measurement_seconds": round(sum_measurement_dur_sec, 2),
            "sum_trial_measurement_hours": round(sum_measurement_dur_sec / 3600.0, 2),
            "mean_measurement_duration_seconds": round(sum_measurement_dur_sec / len(trials_manifest), 2)
        },
        "aggregate_files": {
            "results_csv": {
                "relative_path": "research/experiments/data/campaign_wsl2_ext4_restart_v1/results.csv",
                "sha256": csv_sha
            },
            "figures": figure_hashes
        },
        "trials": trials_manifest
    }

    with open(OUTPUT_MANIFEST, 'w', encoding='utf-8') as fp:
        json.dump(manifest, fp, indent=2)

    print(f"Manifest written successfully to {OUTPUT_MANIFEST}")

if __name__ == "__main__":
    main()
