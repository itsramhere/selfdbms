# Paper names and repository identifiers

This table was moved from the manuscript's Artifact Availability section in Phase 1. Identifiers retain their frozen repository spellings.

| Repository identifier | Paper name |
|---|---|
| `A` | CoarseLock |
| `AMIN`, `A-min` | Lease |
| `B` | Snapshot |
| `X1` | Mixed workload |
| `X2` | Publication workload |
| `X3` | Churn workload |
| `second` | two-acquisition commit (default) |
| `inline` | one-acquisition commit |
| `legacy_scan` | legacy scan |
| `X-intervention-v1` | the intervention |
| `X-compaction-v1` | the compaction campaign |
| `X-tablecount-v1` | the table-count campaign |
| `X-paced-v1` | the paced-load campaign |
| `X4` | Paced workload |
| `X-settled-v1` | the settled campaign |
| `S1`, `S2`, `S3` | settled paced cells |
| `R_*` | settled sentinel cells |

| `X-followup-v1` | the follow-up campaign (seventh campaign) |
| `F1_w0183_m16`, `F1_w0366_m16`, `F1_w0549_m16`, `F1_w0732_m16` | four paced write-rate cells, 16 MiB, fsync on |
| `F2_w0732_m01` | 732 writes/s, 1 MiB reference cell |
| `F3_w0732_m16_syncoff` | 732 writes/s, 16 MiB, fsync off |
| `F4_r04_d0000` | full-histogram Mixed cell, four readers, zero delay |

The follow-up records are on `research/x-phase3`, completed in `b19ee36`, with measured revision `fc7998c`, plan SHA-256 prefix `394675b73698`, runner prefix `6d065832f639`, master seed `20261108`, and a 20 s pause. Raw records are `research/experiments/phase_i7/X_READCOST/followup/`; regenerated results are in `analysis_followup/` beside it. From the repository root:

```bash
python research/experiments/analysis/x_followup_analyze.py --data-dir research/experiments/phase_i7/X_READCOST/followup --output-dir research/experiments/phase_i7/X_READCOST/analysis_followup
python research/experiments/analysis/x_followup_paper_outputs.py --results research/experiments/phase_i7/X_READCOST/analysis_followup/x_followup_results.json --data-dir research/experiments/phase_i7/X_READCOST/followup --macros-out paper/generated/followup.tex --figures-dir paper/figures/x_readcost
```
