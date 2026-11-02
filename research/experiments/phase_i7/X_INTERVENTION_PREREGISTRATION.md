# X-intervention-v1 preregistration

Status: FROZEN once committed, before any real trial. This document is prepared before data; no real trial has been run for this follow-up.

## Purpose

This follow-up tests by intervention the exploratory two-acquisition explanation found after X-readcost-v1. It is separate from the frozen campaign. No X-readcost-v1 test, margin, or result changes.

## Intervention and design

Transaction.Commit normally acquires Manager.Mu exclusively for validation and application, unlocks, and then checkAndTriggerFlush acquires it again even below the flush threshold. The experiment-only InlineFlushCheck flag checks the threshold while the first lock is held and skips the second acquisition below threshold. At or above threshold, checkAndTriggerFlush still rechecks under its own lock and rotates and flushes as before. The threshold includes the existing 64 MiB default when configured at zero or below. The flag defaults to false; validation, WAL, error paths, and other lock sites are unchanged.

| Design element | Fixed value |
| --- | --- |
| Campaign ID | X-intervention-v1 |
| Master seed | 20261103 by default; a supplied master seed is recorded in the plan |
| Cells | r = 4, 16 crossed with d = 0, 20, 200, 2000 microseconds; 8 cells, no legacy path |
| Cell ID | I1_r%02d_d%04d |
| Engine and workload | X1 spec: block read path, 1 MiB memtable, 1 writer, 10,000 keys, 4 initial SSTables of 2,500 keys, 95% target hit ratio, uniform reads, WAL sync and synchronization instrumentation on |
| Timing | 2 s warmup, 10 s measurement, 200 ms cooldown |
| Conditions | second: InlineFlushCheck=false; inline: InlineFlushCheck=true |
| Arms | A and AMIN (A-min) |
| Replication | 5 rounds, k = 1 through 5; 4 runs per cell per round; 160 trials |

Each round shuffles the canonical cell order (r ascending, then d ascending) using Go rand.New(rand.NewSource(int64(master + uint64(k)))). For each cell, its canonical zero-based cellIndex defines rand.New(rand.NewSource(int64(master)*1000 + int64(k)*100 + int64(cellIndex))).Perm(4). The canonical four runs are second/A, second/AMIN, inline/A, inline/AMIN. All four runs share pairedSeed(master, cellID, k): SHA-256 of the decimal master, cell ID, and decimal k separated by vertical bars; the first eight bytes are big-endian and interpreted as int64. Cell is <cellID>_<condition>, Triplet is k, and TrialID is <cellID>_<condition>_t%02d_<arm>. OrderPosition is 1 through 4 within the unit; RunlistPosition is 1 through 160.

The baseline is rerun in the same session on the same seeds, so comparisons do not depend on earlier data. B is omitted because A-min and B were equivalent on throughput in X1, and to shorten the run.

## Outcomes and fixed predictions

The outcomes use in-window completions: q = reads / (r times commits); W = commits / 10 s; R = reads / 10 s; T = 1/W = 10 s / commits. w_A is A's mean first-acquisition commit lock wait, converted from nanoseconds to seconds. Late completions are not in these rates or q. Cell-condition means of q are arithmetic means of trial q; rates and other positive outcomes also have geometric summaries. The cycle comparison uses arithmetic means of T_AMIN, w_A, and observed T_A within each cell and condition; signed relative error is (predicted T_A - observed T_A) / observed T_A. This fixes the aggregation before data.

| Prediction | Fixed criterion |
| --- | --- |
| P1 (primary) | Under A, per-cell mean q is in [1.90, 2.10] for second and [0.90, 1.10] for inline, in all 8 cells. Supported only if all 16 condition-cell means are in range. |
| P2 | For each r at d = 0, 20, 200 microseconds (6 cells), the 95% paired t CI of ln[(R_A/R_AMIN)_inline / (R_A/R_AMIN)_second] lies wholly below 0. Each cell is reported separately. No multiplicity correction; n = 5; descriptive strength only. Also report R_A/R_AMIN under inline with its 95% CI, with no prediction about crossing 1. |
| P3 cycle model | Under inline, T_A is predicted by T_AMIN + w_A (one wait); under second, by T_AMIN + 2 w_A. Prediction error must be within +/-15% in all 8 cells per condition. |
| P3 writer improvement | The 95% paired t CI for W_A(inline)/W_A(second) lies wholly above 1 at d = 200, 2000 microseconds (4 cells). |

A-min under inline is reported descriptively, with no prediction. The pairing unit is a round within a cell. All paired log ratios use the same complete four-run units and two-sided 95% Student t intervals (4 degrees of freedom with all 5 rounds). No bootstrap or multiplicity adjustment is required. Zero outcomes remain data: undefined q, T, logarithms, or cycle errors are reported as undefined with their counts, never replaced or used to fail a trial. A criterion that cannot be assessed is inconclusive; it cannot support a prediction. All summaries use retained complete units; successful observations from dropped units remain visible in the trial-level output.

## Reporting

All results are reported whichever way they go. If P1 fails, the paper must say the two-acquisition explanation was not confirmed, and describe what was observed. Partial support is described as partial. Per-cell verdicts and the overall conjunctions are reported; incomplete data are explicitly marked.

## Failures and exclusions

The rules are the same as X-readcost-v1 section 6. Treatment outcomes never fail a trial. Only engine or harness faults (ledger violation, close or lease-drain error, validation error, crash, or timeout) fail a trial. One retry is allowed. If any of a cell-round's four runs fails after retry, that unit is dropped for that cell and listed. Missing or unfinished runs also make the unit unavailable; --allow-partial is required to analyze missing trials. Dropped units and every failed attempt are listed. Abort above 5% failures after at least 40 attempts, preserving the existing driver's counting rule. There are no outcome-based exclusions.

## Host protocol

The protocol is the same as X-readcost-v1 section 8: AC power; Windows power mode "Best performance"; no other user applications; Defender real-time protection left as found and recorded. The driver records git revision, runner SHA-256, timer self-test, and power scheme in platform.json. Approval remains bound to campaign, runner, and plan hashes, with the clean-tree check, ledger, retry, and abort rules retained.

## Deviations and pilot log

On 2026-11-02, after the predictions above were fixed in writing, a mechanical smoke run executed the first 8 rows of the plan from a dirty tree with an earlier build of the runner. All 8 trials succeeded. The operator saw their per-trial values, one trial per condition-arm in two cells. These trials are excluded from analysis, and no design value or prediction was changed because of them. Their output is kept in the git-ignored `X_READCOST/intervention_smoke` folder. No other trial precedes the freeze.

## Analysis implementation

The deterministic script is research/experiments/analysis/x_intervention_analyze.py. It reads the authoritative ledger and latest attempts, checks envelopes and provenance, and writes x_intervention_results.json and x_intervention_tables.md. Optional --tex-out writes the TblInterventionRows LaTeX macro to the explicit path. It does not modify paper sources or existing generated tables by default.
