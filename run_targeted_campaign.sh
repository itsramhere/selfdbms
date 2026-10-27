#!/bin/bash
set -euo pipefail

REPO_DIR="/home/ramst/selfdbms"
DATA_DIR="research/experiments/data/targeted_validation_v1"
WIN_SYNC_DIR="/mnt/c/Users/ramst/Documents/Projects/selfdbms/research/experiments/data/targeted_validation_v1"
PROGRESS_LOG="$REPO_DIR/$DATA_DIR/targeted_progress.log"
BATCH_SIZE=${BATCH_SIZE:-10}
TOTAL_PLANNED=600

cd "$REPO_DIR"
mkdir -p "$DATA_DIR"
mkdir -p "$WIN_SYNC_DIR"

echo "================================================================================" | tee -a "$PROGRESS_LOG"
echo "[TARGETED_CAMPAIGN_DRIVER] Starting Phase I6.8 Targeted Validation (Campaign ID: I6_TARGETED_VALIDATION_V1)" | tee -a "$PROGRESS_LOG"
echo "[TARGETED_CAMPAIGN_DRIVER] Total Planned: $TOTAL_PLANNED trials | Batch Size: $BATCH_SIZE" | tee -a "$PROGRESS_LOG"

START_EPOCH=$(date +%s)
BATCH_NUM=1

while true; do
    echo "--------------------------------------------------------------------------------" | tee -a "$PROGRESS_LOG"
    echo "[BATCH $BATCH_NUM] Starting batch at $(date -u '+%Y-%m-%dT%H:%M:%SZ')" | tee -a "$PROGRESS_LOG"
    BATCH_START=$(date +%s)

    # Run batch
    ./targeted_runner -run -batch-size="$BATCH_SIZE" 2>&1 | tee -a "$PROGRESS_LOG"

    BATCH_END=$(date +%s)
    BATCH_WALLCLOCK=$((BATCH_END - BATCH_START))

    # Sync results to Windows
    rsync -a "$DATA_DIR/" "$WIN_SYNC_DIR/" || true

    # Count completed trials from results.csv
    if [ -f "$DATA_DIR/results.csv" ]; then
        COMPLETED=$(grep -c ",SUCCESS," "$DATA_DIR/results.csv" || true)
    else
        COMPLETED=0
    fi

    TOTAL_ELAPSED=$((BATCH_END - START_EPOCH))
    REMAINING=$((TOTAL_PLANNED - COMPLETED))

    if [ "$COMPLETED" -gt 0 ]; then
        AVG_TIME=$(echo "scale=2; $TOTAL_ELAPSED / $COMPLETED" | bc)
        EST_REMAINING_SECS=$(echo "scale=0; $AVG_TIME * $REMAINING / 1" | bc)
        EST_REMAINING_MINS=$((EST_REMAINING_SECS / 60))
        echo "[PROGRESS] Validated: $COMPLETED / $TOTAL_PLANNED ($((COMPLETED * 100 / TOTAL_PLANNED))%) | Remaining: $REMAINING" | tee -a "$PROGRESS_LOG"
        echo "[TIMING] Batch $BATCH_NUM Wall-clock: ${BATCH_WALLCLOCK}s | Session Elapsed: ${TOTAL_ELAPSED}s | Avg/Trial: ${AVG_TIME}s | Est Remaining: ~${EST_REMAINING_MINS}m" | tee -a "$PROGRESS_LOG"
        
        # Run live statistical analysis
        python3 research/experiments/targeted_validation/analyze_targeted.py "$DATA_DIR/results.csv" "$DATA_DIR/live_analysis.md" || true
    else
        echo "[PROGRESS] Validated: $COMPLETED / $TOTAL_PLANNED" | tee -a "$PROGRESS_LOG"
    fi

    if [ "$COMPLETED" -ge "$TOTAL_PLANNED" ]; then
        echo "================================================================================" | tee -a "$PROGRESS_LOG"
        echo "[SUCCESS] Campaign I6_TARGETED_VALIDATION_V1 complete! All $TOTAL_PLANNED trials validated." | tee -a "$PROGRESS_LOG"
        echo "================================================================================" | tee -a "$PROGRESS_LOG"
        break
    fi

    BATCH_NUM=$((BATCH_NUM + 1))
done
