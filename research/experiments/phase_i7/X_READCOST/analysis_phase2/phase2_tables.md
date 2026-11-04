# Phase 2: exploratory existing-record analysis

Development and legacy cells count for nothing in the verdict. All three frozen criteria must pass. No fitted parameters or new CIs.

- h: Arithmetic mean of CoarseLock trial mean read critical section (ns).
- h_p95: Arithmetic mean of CoarseLock trial p95 read critical section (ns).
- T_L: Arithmetic mean of per-trial Lease measured_window_ns / in-window commits (ns).
- S: exp(mean(log(recorded W_CL) - log(recorded W_L))), paired by round and condition.
- intervention: Use result.throughput.write_ops_per_sec for S and the same condition's measured_window_ns / write_ops_completed_in_window for T_L. The earlier intervention analyzer used count / 10 s; it is not used here. Recorded W uses its measured window.
- x: h / T_L; T_L also enters S, so some coupling remains.
- M1: 1 / (1 + k * h / T_L)
- M2: 1 / (1 + k * h_p95 / T_L)
- residual: log(S) - log(predicted); each cell has equal weight.
- rounds: Five paired rounds per intervention cell; ten per other selected cell. Read quantiles and settled CDFs each use ten rounds per arm.
- criteria: Inclusive bounds, computed at full precision before serialization.
- read_quantiles: Per-trial points and median line; means in table. These percentiles cannot identify modes.
- commit_cdf: Pool the ten rounds by summing counts at each lower_ns. Bucket-resolution empirical CDF; exact within-bucket latencies are unavailable. Mass is placed at each recorded lower bound. Histograms cover commits arriving inside the nominal window, including their post-window drain.
- zero_log_handling: Keep zero bucket counts in total and cumulative probability. For display only, place zero at half the smallest positive lower_ns across all four series of that phase; annotate its mass. Positive buckets are unmodified.
- precision: JSON floats: twelve fixed decimal places; comparisons use unrounded values.

## Cell counts

| Group | Cells |
| --- | ---: |
| churn | 3 |
| compaction_off | 6 |
| compaction_on | 6 |
| development | 8 |
| intervention_inline | 8 |
| intervention_second | 8 |
| legacy | 2 |
| tablecount_writer | 8 |
| test | 39 |
| k1_only | 8 |
| reader_pairs | 19 |

## Frozen criteria and verdict

| Model | Median absolute residual | Largest absolute residual | Mean r16 - r4 residual | Collapses | Failed criteria |
| --- | ---: | ---: | ---: | --- | --- |
| M1 | 0.030813308 | 0.306677192 | -0.041627074 | false | 2 |
| M2 | 0.105792724 | 0.448232037 | 0.001502302 | false | 1, 2 |

Bounds: 0.105360515658, 0.210721031316, +/-0.105360515658, inclusive.
Spearman(x, S), test cells only: rho = -0.905668016, n = 39; descriptive only.

**No collapse was found for either model; no rule is offered.**


## Test cells

| Cell | Group | k | h (us) | h_p95 (us) | T_L (ms) | x | S | M1 | M2 | Residual M1 | Residual M2 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| C1_r04_d0000_off | compaction_off | 2 | 185.620741 | 551.731200 | 1.441978 | 0.128726 | 0.812922 | 0.795258 | 0.566494 | 0.021968 | 0.361168 |
| C1_r04_d0000_on | compaction_on | 2 | 34.328881 | 73.830400 | 1.228728 | 0.027939 | 0.918895 | 0.947080 | 0.892719 | -0.030212 | 0.028900 |
| C1_r04_d0200_off | compaction_off | 2 | 1548.991978 | 3158.835200 | 3.864788 | 0.400796 | 0.594207 | 0.555065 | 0.379554 | 0.068143 | 0.448232 |
| C1_r04_d0200_on | compaction_on | 2 | 393.714673 | 639.795200 | 1.732733 | 0.227222 | 0.680809 | 0.687548 | 0.575215 | -0.009851 | 0.168538 |
| C1_r04_d2000_off | compaction_off | 2 | 5434.856174 | 10217.062400 | 11.140336 | 0.487854 | 0.532223 | 0.506148 | 0.352827 | 0.050235 | 0.411084 |
| C1_r04_d2000_on | compaction_on | 2 | 2357.424345 | 4097.638400 | 4.186122 | 0.563152 | 0.438870 | 0.470299 | 0.338098 | -0.069166 | 0.260869 |
| C1_r16_d0000_off | compaction_off | 2 | 330.283840 | 981.401600 | 1.729010 | 0.191025 | 0.721396 | 0.723563 | 0.468336 | -0.002999 | 0.432002 |
| C1_r16_d0000_on | compaction_on | 2 | 78.139119 | 213.708800 | 1.317410 | 0.059313 | 0.850846 | 0.893954 | 0.755037 | -0.049424 | 0.119463 |
| C1_r16_d0200_off | compaction_off | 2 | 1576.386186 | 3180.134400 | 3.975765 | 0.396499 | 0.581799 | 0.557725 | 0.384651 | 0.042259 | 0.413789 |
| C1_r16_d0200_on | compaction_on | 2 | 412.434989 | 700.416000 | 1.867755 | 0.220819 | 0.655841 | 0.693656 | 0.571426 | -0.056057 | 0.137784 |
| C1_r16_d2000_off | compaction_off | 2 | 5179.447518 | 10066.329600 | 11.147126 | 0.464644 | 0.497421 | 0.518326 | 0.356368 | -0.041168 | 0.333471 |
| C1_r16_d2000_on | compaction_on | 2 | 2323.498921 | 4177.920000 | 4.281393 | 0.542697 | 0.393100 | 0.479526 | 0.338792 | -0.198734 | 0.148677 |
| I1_r04_d0000_inline | intervention_inline | 1 | 17.496480 | 46.489600 | 1.010774 | 0.017310 | 0.962888 | 0.982985 | 0.956028 | -0.020656 | 0.007150 |
| I1_r04_d0000_second | intervention_second | 2 | 13.682404 | 36.403200 | 1.027944 | 0.013310 | 0.964615 | 0.974069 | 0.933857 | -0.009754 | 0.032405 |
| I1_r04_d0020_inline | intervention_inline | 1 | 36.822760 | 84.889600 | 1.081373 | 0.034052 | 0.974448 | 0.967069 | 0.927212 | 0.007601 | 0.049688 |
| I1_r04_d0020_second | intervention_second | 2 | 33.617506 | 78.233600 | 1.085664 | 0.030965 | 0.945808 | 0.941682 | 0.874033 | 0.004372 | 0.078921 |
| I1_r04_d0200_inline | intervention_inline | 1 | 169.233180 | 362.086400 | 1.222081 | 0.138480 | 0.863947 | 0.878365 | 0.771434 | -0.016550 | 0.113260 |
| I1_r04_d0200_second | intervention_second | 2 | 164.815397 | 241.664000 | 1.229621 | 0.134038 | 0.731293 | 0.788597 | 0.717839 | -0.075441 | 0.018570 |
| I1_r04_d2000_inline | intervention_inline | 1 | 1839.698816 | 2254.438400 | 2.836582 | 0.648562 | 0.539331 | 0.606589 | 0.557174 | -0.117523 | -0.032547 |
| I1_r04_d2000_second | intervention_second | 2 | 1888.071996 | 2218.393600 | 2.749301 | 0.686746 | 0.356370 | 0.421320 | 0.382587 | -0.167422 | -0.070984 |
| I1_r16_d0000_inline | intervention_inline | 1 | 27.104114 | 117.760000 | 1.072286 | 0.025277 | 0.987093 | 0.975346 | 0.901046 | 0.011971 | 0.091208 |
| I1_r16_d0000_second | intervention_second | 2 | 21.274773 | 96.358400 | 1.119628 | 0.019002 | 0.944775 | 0.963388 | 0.853151 | -0.019509 | 0.102011 |
| I1_r16_d0020_inline | intervention_inline | 1 | 45.724030 | 134.144000 | 1.095187 | 0.041750 | 0.982825 | 0.959923 | 0.890881 | 0.023578 | 0.098221 |
| I1_r16_d0020_second | intervention_second | 2 | 38.384383 | 112.742400 | 1.071664 | 0.035818 | 0.855333 | 0.933153 | 0.826169 | -0.087079 | 0.034691 |
| I1_r16_d0200_inline | intervention_inline | 1 | 168.670991 | 296.140800 | 1.260121 | 0.133853 | 0.848441 | 0.881949 | 0.809710 | -0.038733 | 0.046724 |
| I1_r16_d0200_second | intervention_second | 2 | 175.277524 | 265.830400 | 1.253463 | 0.139835 | 0.662173 | 0.781452 | 0.702172 | -0.165627 | -0.058651 |
| I1_r16_d2000_inline | intervention_inline | 1 | 1865.240880 | 2264.268800 | 2.823151 | 0.660695 | 0.479162 | 0.602158 | 0.554928 | -0.228479 | -0.146798 |
| I1_r16_d2000_second | intervention_second | 2 | 1917.228517 | 2254.438400 | 2.788980 | 0.687430 | 0.309866 | 0.421077 | 0.382164 | -0.306677 | -0.209711 |
| T1_n004_r04_w1 | tablecount_writer | 2 | 13.545668 | 35.609600 | 1.018664 | 0.013297 | 0.928233 | 0.974094 | 0.934654 | -0.048225 | -0.006894 |
| T1_n004_r16_w1 | tablecount_writer | 2 | 21.681971 | 98.406400 | 1.096513 | 0.019774 | 0.947883 | 0.961957 | 0.847824 | -0.014739 | 0.111557 |
| T1_n016_r04_w1 | tablecount_writer | 2 | 13.792262 | 36.275200 | 1.065547 | 0.012944 | 1.003318 | 0.974766 | 0.936253 | 0.028870 | 0.069182 |
| T1_n016_r16_w1 | tablecount_writer | 2 | 19.558634 | 95.590400 | 1.114975 | 0.017542 | 0.948889 | 0.966106 | 0.853631 | -0.017982 | 0.105793 |
| T1_n064_r04_w1 | tablecount_writer | 2 | 14.961552 | 37.811200 | 1.022428 | 0.014633 | 0.965083 | 0.971565 | 0.931130 | -0.006695 | 0.035815 |
| T1_n064_r16_w1 | tablecount_writer | 2 | 20.045472 | 95.897600 | 1.114607 | 0.017984 | 0.943095 | 0.965280 | 0.853188 | -0.023251 | 0.100187 |
| T1_n256_r04_w1 | tablecount_writer | 2 | 17.045438 | 39.884800 | 1.037532 | 0.016429 | 0.938810 | 0.968188 | 0.928605 | -0.030813 | 0.010929 |
| T1_n256_r16_w1 | tablecount_writer | 2 | 22.348872 | 97.638400 | 1.127070 | 0.019829 | 0.964912 | 0.961854 | 0.852326 | 0.003174 | 0.124069 |
| X3_r01 | churn | 2 | 133.681882 | 344.678400 | 1.341712 | 0.099635 | 0.933248 | 0.833840 | 0.660594 | 0.112630 | 0.345532 |
| X3_r04 | churn | 2 | 185.117111 | 552.140800 | 1.424885 | 0.129917 | 0.833338 | 0.793755 | 0.563381 | 0.048665 | 0.391483 |
| X3_r16 | churn | 2 | 334.562617 | 956.825600 | 1.759573 | 0.190139 | 0.728934 | 0.724492 | 0.479027 | 0.006113 | 0.419827 |

## Development cells (not counted)

| Cell | Group | k | h (us) | h_p95 (us) | T_L (ms) | x | S | M1 | M2 | Residual M1 | Residual M2 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| X1_r04_d0000 | development | 2 | 13.572311 | 36.121600 | 1.067542 | 0.012714 | 0.988163 | 0.975203 | 0.936617 | 0.013202 | 0.053573 |
| X1_r04_d0020 | development | 2 | 33.646414 | 78.233600 | 1.057017 | 0.031831 | 0.926110 | 0.940147 | 0.871060 | -0.015043 | 0.061283 |
| X1_r04_d0200 | development | 2 | 163.304215 | 241.049600 | 1.233523 | 0.132388 | 0.758465 | 0.790653 | 0.718995 | -0.041563 | 0.053443 |
| X1_r04_d2000 | development | 2 | 1885.018359 | 2191.360000 | 2.785185 | 0.676802 | 0.361076 | 0.424880 | 0.388563 | -0.162718 | -0.073366 |
| X1_r16_d0000 | development | 2 | 21.242431 | 96.460800 | 1.095858 | 0.019384 | 0.936990 | 0.962678 | 0.850307 | -0.027046 | 0.097076 |
| X1_r16_d0020 | development | 2 | 39.307050 | 116.633600 | 1.139823 | 0.034485 | 0.943773 | 0.935480 | 0.830115 | 0.008826 | 0.128321 |
| X1_r16_d0200 | development | 2 | 173.412738 | 264.396800 | 1.293757 | 0.134038 | 0.705201 | 0.788596 | 0.709861 | -0.111772 | -0.006586 |
| X1_r16_d2000 | development | 2 | 1914.742941 | 2234.777600 | 2.797576 | 0.684429 | 0.310553 | 0.422144 | 0.384963 | -0.306992 | -0.214792 |

## Legacy scan (not counted)

| Cell | Group | k | h (us) | h_p95 (us) | T_L (ms) | x | S | M1 | M2 | Residual M1 | Residual M2 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| X1_r04_legacy | legacy | 2 | 124852.801286 | 283115.520000 | 1.631560 | 76.523588 | 0.004250 | 0.006492 | 0.002873 | -0.423598 | 0.391486 |
| X1_r16_legacy | legacy | 2 | 430129.799549 | 896742.195200 | 2.828279 | 152.081798 | 0.001696 | 0.003277 | 0.001574 | -0.658448 | 0.074527 |

## k=1 cells alone

| Cell | x | S | M1 | M2 | Residual M1 | Residual M2 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| I1_r04_d0000_inline | 0.017309986 | 0.962888361 | 0.982984552 | 0.956028365 | -0.020655928 | 0.007149894 |
| I1_r04_d0020_inline | 0.034051869 | 0.974447693 | 0.967069477 | 0.927212253 | 0.007600501 | 0.049688334 |
| I1_r04_d0200_inline | 0.138479519 | 0.863947368 | 0.878364506 | 0.771434247 | -0.016549813 | 0.113260409 |
| I1_r04_d2000_inline | 0.648561859 | 0.539330940 | 0.606589310 | 0.557173557 | -0.117522601 | -0.032547414 |
| I1_r16_d0000_inline | 0.025276951 | 0.987092543 | 0.975346221 | 0.901045820 | 0.011971291 | 0.091207687 |
| I1_r16_d0020_inline | 0.041749957 | 0.982824791 | 0.959923246 | 0.890880529 | 0.023577537 | 0.098220533 |
| I1_r16_d0200_inline | 0.133853023 | 0.848440988 | 0.881948524 | 0.809710153 | -0.038733158 | 0.046724186 |
| I1_r16_d2000_inline | 0.660694713 | 0.479162497 | 0.602157635 | 0.554927856 | -0.228479480 | -0.146798333 |

## Residual summaries (k=1 reported on its own)

| Group | Model | Cells | Median absolute | Largest absolute | Mean signed |
| --- | --- | ---: | ---: | ---: | ---: |
| churn | M1 | 3 | 0.048664689 | 0.112629691 | 0.055802295 |
| churn | M2 | 3 | 0.391483117 | 0.419826907 | 0.385613972 |
| compaction_off | M1 | 6 | 0.041713399 | 0.068143222 | 0.023072803 |
| compaction_off | M2 | 6 | 0.412436377 | 0.448232037 | 0.399957522 |
| compaction_on | M1 | 6 | 0.052740644 | 0.198734328 | -0.068907337 |
| compaction_on | M2 | 6 | 0.143230461 | 0.260868965 | 0.144038546 |
| development | M1 | 8 | 0.034304714 | 0.306992471 | -0.080388404 |
| development | M2 | 8 | 0.067324794 | 0.214792411 | 0.012368835 |
| intervention_inline | M1 | 8 | 0.022116732 | 0.228479480 | -0.047348956 |
| intervention_inline | M2 | 8 | 0.070448010 | 0.146798333 | 0.028363162 |
| intervention_second | M1 | 8 | 0.081259896 | 0.306677192 | -0.103392119 |
| intervention_second | M2 | 8 | 0.064817722 | 0.209710901 | -0.009093548 |
| legacy | M1 | 2 | 0.541022649 | 0.658447617 | -0.541022649 |
| legacy | M2 | 2 | 0.233006844 | 0.391486493 | 0.233006844 |
| tablecount_writer | M1 | 8 | 0.020616402 | 0.048224741 | -0.013707581 |
| tablecount_writer | M2 | 8 | 0.084684326 | 0.124068577 | 0.068829650 |
| k1_only | M1 | 8 | 0.022116732 | 0.228479480 | -0.047348956 |
| k1_only | M2 | 8 | 0.070448010 | 0.146798333 | 0.028363162 |
| test_all | M1 | 39 | 0.030813308 | 0.306677192 | -0.036492040 |
| test_all | M2 | 39 | 0.105792724 | 0.448232037 | 0.131425960 |

## Criterion 3 pairs

Churn r=1 has no role here. Each pair changes only reader count; the mean difference gives each pair equal weight.

| Group | r=4 | r=16 | M1 difference | M2 difference |
| --- | --- | --- | ---: | ---: |
| churn | X3_r04 | X3_r16 | -0.042552183 | 0.028343789 |
| compaction_off | C1_r04_d0000_off | C1_r16_d0000_off | -0.024967106 | 0.070834251 |
| compaction_off | C1_r04_d0200_off | C1_r16_d0200_off | -0.025884668 | -0.034443362 |
| compaction_off | C1_r04_d2000_off | C1_r16_d2000_off | -0.091402830 | -0.077613214 |
| compaction_on | C1_r04_d0000_on | C1_r16_d0000_on | -0.019212081 | 0.090563424 |
| compaction_on | C1_r04_d0200_on | C1_r16_d0200_on | -0.046206755 | -0.030753853 |
| compaction_on | C1_r04_d2000_on | C1_r16_d2000_on | -0.129568374 | -0.112192010 |
| intervention_inline | I1_r04_d0000_inline | I1_r16_d0000_inline | 0.032627218 | 0.084057793 |
| intervention_inline | I1_r04_d0020_inline | I1_r16_d0020_inline | 0.015977036 | 0.048532199 |
| intervention_inline | I1_r04_d0200_inline | I1_r16_d0200_inline | -0.022183345 | -0.066536223 |
| intervention_inline | I1_r04_d2000_inline | I1_r16_d2000_inline | -0.110956879 | -0.114250919 |
| intervention_second | I1_r04_d0000_second | I1_r16_d0000_second | -0.009755711 | 0.069605601 |
| intervention_second | I1_r04_d0020_second | I1_r16_d0020_second | -0.091451563 | -0.044230072 |
| intervention_second | I1_r04_d0200_second | I1_r16_d0200_second | -0.090186501 | -0.077220895 |
| intervention_second | I1_r04_d2000_second | I1_r16_d2000_second | -0.139254981 | -0.138726500 |
| tablecount_writer | T1_n004_r04_w1 | T1_n004_r16_w1 | 0.033485303 | 0.118450984 |
| tablecount_writer | T1_n016_r04_w1 | T1_n016_r16_w1 | -0.046851944 | 0.036610899 |
| tablecount_writer | T1_n064_r04_w1 | T1_n064_r16_w1 | -0.016556430 | 0.064372231 |
| tablecount_writer | T1_n256_r04_w1 | T1_n256_r16_w1 | 0.033987378 | 0.113139616 |

## Mixed r=4, d=0 read latency (microseconds)

Quantile figure: ten per-trial points per quantile, with a median line. The following are arithmetic means over ten trials, including the trial mean column. No modes can be inferred.

| Arm | min | p50 | p95 | p99 | p999 | max | mean | Trials with mean > p95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| CoarseLock | 0.480000 | 45.414400 | 1618.739200 | 2280.652800 | 28708.044800 | 92206.220000 | 535.000822 | 0/10 |
| Lease | 0.550000 | 808.550400 | 1517.158400 | 2331.443200 | 24584.192000 | 105586.570000 | 639.957352 | 0/10 |

## Settled commit-phase CDFs

Pool the ten rounds by summing counts at each lower_ns. Bucket-resolution empirical CDF; exact within-bucket latencies are unavailable. Mass is placed at each recorded lower bound. Histograms cover commits arriving inside the nominal window, including their post-window drain.

Keep zero bucket counts in total and cumulative probability. For display only, place zero at half the smallest positive lower_ns across all four series of that phase; annotate its mass. Positive buckets are unmodified.

| Cell | Arm | Phase | Rounds | Pooled samples | Zero samples |
| --- | --- | --- | ---: | ---: | ---: |
| S1_d0000_f20_closed | A | lock_wait | 10 | 18300 | 0 |
| S1_d0000_f20_closed | A | flush_check | 10 | 18300 | 0 |
| S1_d0000_f20_closed | A | wal_sync | 10 | 18300 | 0 |
| S1_d0000_f20_closed | A | total | 10 | 18300 | 0 |
| S1_d0000_f20_closed | AMIN | lock_wait | 10 | 18300 | 0 |
| S1_d0000_f20_closed | AMIN | flush_check | 10 | 18300 | 0 |
| S1_d0000_f20_closed | AMIN | wal_sync | 10 | 18300 | 0 |
| S1_d0000_f20_closed | AMIN | total | 10 | 18300 | 0 |
| S1_d2000_f20_closed | A | lock_wait | 10 | 7200 | 0 |
| S1_d2000_f20_closed | A | flush_check | 10 | 7200 | 0 |
| S1_d2000_f20_closed | A | wal_sync | 10 | 7200 | 0 |
| S1_d2000_f20_closed | A | total | 10 | 7200 | 0 |
| S1_d2000_f20_closed | AMIN | lock_wait | 10 | 7200 | 0 |
| S1_d2000_f20_closed | AMIN | flush_check | 10 | 7200 | 0 |
| S1_d2000_f20_closed | AMIN | wal_sync | 10 | 7200 | 0 |
| S1_d2000_f20_closed | AMIN | total | 10 | 7200 | 0 |

## Limitations and unavailable quantities

Could not compute requested quantities: none.

- Exploratory, with shared Lease throughput/cycle coupling; no confirmatory interpretation.
- Read percentiles cannot determine bimodality or any other number of modes.
- Commit CDF timing is resolved only to histogram lower bounds; operation counts weight pooled rounds.
