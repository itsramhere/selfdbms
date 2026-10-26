#!/bin/bash
set -euo pipefail

REPO_DIR="/home/ramst/selfdbms"
CAMPAIGN_DIR="research/experiments/data/campaign_wsl2_ext4_restart_v1"
WIN_SYNC_DIR="/mnt/c/Users/ramst/Documents/Projects/selfdbms/research/experiments/data/campaign_wsl2_ext4_restart_v1"
PROGRESS_LOG="$REPO_DIR/$CAMPAIGN_DIR/campaign_progress.log"
GIT_REV="a5c0fb97deb8a67705a80e76b49e25f31d692492"
BATCH_SIZE=${BATCH_SIZE:-24}
DURATION=10000
WARMUP=2000

cd "$REPO_DIR"
mkdir -p "$CAMPAIGN_DIR/wsl2_ext4"
mkdir -p "$WIN_SYNC_DIR/wsl2_ext4"

# Check initial audit status before starting
INIT_AUDIT=$(./experiment_runner_linux -audit \
    -platform=wsl2_ext4 \
    -campaign-dir="$CAMPAIGN_DIR" \
    -duration="$DURATION" \
    -warmup="$WARMUP")

INITIAL_VALIDATED=$(echo "$INIT_AUDIT" | grep "Total Validated Trials:" | awk '{print $4}')
echo "[CAMPAIGN_DRIVER] Resuming Phase I5 WSL2 campaign (Campaign ID: I5_WSL2_EXT4_RESTART_V1)" | tee -a "$PROGRESS_LOG"
echo "[CAMPAIGN_DRIVER] Initial Validated: $INITIAL_VALIDATED / 960 | Batch Size: $BATCH_SIZE | Duration: ${DURATION}ms | Warmup: ${WARMUP}ms" | tee -a "$PROGRESS_LOG"

START_CAMPAIGN_EPOCH=$(date +%s)
BATCH_NUM=1

while true; do
    echo "================================================================================" | tee -a "$PROGRESS_LOG"
    echo "[BATCH $BATCH_NUM] Starting batch at $(date -u '+%Y-%m-%dT%H:%M:%SZ')" | tee -a "$PROGRESS_LOG"
    BATCH_START=$(date +%s)

    # Run batch with resume=true (skips already validated trials)
    ./experiment_runner_linux -campaign \
        -platform=wsl2_ext4 \
        -campaign-dir="$CAMPAIGN_DIR" \
        -batch-size="$BATCH_SIZE" \
        -duration="$DURATION" \
        -warmup="$WARMUP" \
        -git-rev="$GIT_REV" \
        -resume=true 2>&1 | tee -a "$PROGRESS_LOG"

    BATCH_END=$(date +%s)
    BATCH_WALLCLOCK=$((BATCH_END - BATCH_START))

    # Sync results to Windows directory
    rsync -a "$CAMPAIGN_DIR/wsl2_ext4/" "$WIN_SYNC_DIR/wsl2_ext4/" || true

    # Run auditor
    AUDIT_OUT=$(./experiment_runner_linux -audit \
        -platform=wsl2_ext4 \
        -campaign-dir="$CAMPAIGN_DIR" \
        -duration="$DURATION" \
        -warmup="$WARMUP")

    echo "$AUDIT_OUT" | tee -a "$PROGRESS_LOG"

    # Extract validated count and failed count
    VALIDATED=$(echo "$AUDIT_OUT" | grep "Total Validated Trials:" | awk '{print $4}')
    FAILED=$(echo "$AUDIT_OUT" | grep "Total Failed Trials:" | awk '{print $4}')
    PLANNED=960

    if [ "$FAILED" -gt 0 ]; then
        echo "[ERROR] Detected $FAILED failed trials in audit! Halting campaign driver for investigation." | tee -a "$PROGRESS_LOG"
        exit 1
    fi

    TOTAL_ELAPSED=$((BATCH_END - START_CAMPAIGN_EPOCH))
    REMAINING=$((PLANNED - VALIDATED))
    NEW_VALIDATED=$((VALIDATED - INITIAL_VALIDATED))

    if [ "$NEW_VALIDATED" -gt 0 ]; then
        AVG_TIME_PER_TRIAL=$(echo "scale=2; $TOTAL_ELAPSED / $NEW_VALIDATED" | bc)
        EST_REMAINING_SECS=$(echo "scale=0; $AVG_TIME_PER_TRIAL * $REMAINING / 1" | bc)
        EST_REMAINING_MINS=$((EST_REMAINING_SECS / 60))
        echo "[PROGRESS] Validated: $VALIDATED / $PLANNED ($((VALIDATED * 100 / PLANNED))%) | Remaining: $REMAINING" | tee -a "$PROGRESS_LOG"
        echo "[TIMING] Batch $BATCH_NUM Wall-clock: ${BATCH_WALLCLOCK}s | Session Elapsed: ${TOTAL_ELAPSED}s | Avg/Trial: ${AVG_TIME_PER_TRIAL}s | Est Remaining: ~${EST_REMAINING_MINS}m" | tee -a "$PROGRESS_LOG"
    else
        echo "[PROGRESS] Validated: $VALIDATED / $PLANNED ($((VALIDATED * 100 / PLANNED))%) | Remaining: $REMAINING" | tee -a "$PROGRESS_LOG"
    fi

    if [ "$VALIDATED" -ge "$PLANNED" ]; then
        echo "================================================================================" | tee -a "$PROGRESS_LOG"
        echo "[SUCCESS] Campaign I5_WSL2_EXT4_RESTART_V1 complete! All $PLANNED trials validated." | tee -a "$PROGRESS_LOG"
        echo "================================================================================" | tee -a "$PROGRESS_LOG"
        break
    fi

    BATCH_NUM=$((BATCH_NUM + 1))
done
