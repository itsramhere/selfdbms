#!/usr/bin/env python3
"""Generate manuscript table bodies and exploratory mechanism arithmetic.

No trials or inferential analyses are run. Existing CIs/classifications are copied
from the frozen results inputs. Mechanism summaries are arithmetic means of per-trial quantities;
cycle = measured_window_ns / in-window commits. Wait instrumentation also includes
the post-window drain, as specified by the protocol. No late operation counts enter
the admission ratio. Anchors are excluded from cell summaries.
Table files define row macros for loading in the preamble; tabular environments
expand the macros instead of reading files while an alignment is active.

After rendering figures, copy their PDFs reproducibly from the repository root:
  python -c "from pathlib import Path; import shutil; d=Path('paper/figures/x_readcost'); d.mkdir(parents=True, exist_ok=True); [shutil.copyfile(p,d/p.name) for p in sorted(Path('research/experiments/phase_i7/X_READCOST/analysis/figures').glob('fig_*.pdf'))]"

Existing --mechanism-out JSON is verified and never rewritten.
All table outputs use sorted input names, UTF-8, LF, and no wall-clock timestamps.

Full six-campaign regeneration, from the repository root (PowerShell):
  $X = 'research/experiments/phase_i7/X_READCOST'
  python research/experiments/analysis/x_readcost_paper_tables.py --results "$X/analysis/x_readcost_results.json" --trials-dir "$X/campaign/trials" --tables-dir paper/generated --mechanism-out "$X/analysis/mechanism.json" --intervention-results "$X/analysis_intervention/x_intervention_results.json" --compaction-results "$X/analysis_compaction/x_compaction_results.json" --tablecount-results "$X/analysis_tablecount/x_tablecount_results.json" --paced-results "$X/analysis_paced/x_paced_results.json" --settled-results "$X/analysis_settled/x_settled_results.json" --fsync-report "$X/fsync_checks/report/report.json"

The settled output re-expresses general-cell frozen 95% log-CI widths at the
requested 90% level; H_M2, H_M3, H_W, H_R CIs are copied without conversion.
"""
import argparse
from collections import defaultdict
from datetime import datetime
import hashlib
import json
import math
from pathlib import Path
import re
import statistics as st

ARMS = ('A', 'AMIN', 'B')
DOSES = ('d0000', 'd0020', 'd0200', 'd2000', 'legacy')
TABLE_MACROS = {'arms': 'TblArmsRows', 'x1': 'TblXOneRows',
                'mechanism': 'TblMechanismRows', 'h2_publication': 'TblHTwoPubRows',
                'x2': 'TblXTwoRows', 'x3': 'TblXThreeRows',
                'intervention': 'TblInterventionRows', 'summary': 'TblSummaryRows',
                'coverage': 'TblCoverageRows', 'compaction': 'TblCompactionRows',
                'tablecount': 'TblTablecountRows', 'microbench': 'TblMicrobenchRows',
                'compaction_throughput': 'TblCompactionThroughputRows',
                'compaction_lifecycle': 'TblCompactionLifecycleRows'}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def summarize(values):
    return dict(n=len(values), mean=st.mean(values), median=st.median(values),
                min=min(values), max=max(values))


def mechanism(trials):
    """Compute R/(r W), cycle and the equal-second-wait consistency check.

    Zero commits are an error, never silently dropped or continuity-adjusted.
    Successful X2 trials contribute publication distributions, not commit ratios.
    """
    groups = defaultdict(lambda: defaultdict(list))
    seen = set()
    for t in trials:
        if t['status'] != 'SUCCESS':
            raise ValueError('mechanism requires successful trial envelopes')
        if t['trial_id'] in seen:
            raise ValueError('duplicate trial_id: ' + t['trial_id'])
        seen.add(t['trial_id'])
        if t['cell'] != 'ANCHOR':
            groups[t['cell']][t['arm']].append(t)
    cells = {}
    for cell, arms in sorted(groups.items()):
        out = {'arms': {}}
        for arm, rows in sorted(arms.items()):
            rows = sorted(rows, key=lambda t: t['trial_id'])
            quantities = defaultdict(list)
            records = []
            for t in rows:
                res = t['result']
                rec = dict(trial_id=t['trial_id'], triplet=t['triplet'])
                quantities['measured_flushes'].append(res['lsm_diagnostics']['measured_flushes'])
                if cell.startswith('X2'):
                    quantities['publications_in_window'].append(res['lsm_diagnostics']['publication_events_realized_in_window'])
                else:
                    ops = res['operations']
                    reads = ops['read_ops_completed_in_window']
                    writes = ops['write_ops_completed_in_window']
                    if writes <= 0:
                        raise ValueError('zero in-window commits: ' + t['trial_id'])
                    q = reads / (t['spec']['reader_concurrency'] * writes)
                    cycle = res['timestamps']['measured_window_ns'] / writes / 1e6
                    wait = res['sync_instrumentation']['writer_lock_wait_nanos']['commit']['mean_nanos'] / 1e6
                    rec.update(reads_in_window=reads, commits_in_window=writes,
                               reads_per_reader_per_commit=q, cycle_ms=cycle,
                               first_commit_wait_ms=wait)
                    for key in ('reads_per_reader_per_commit', 'cycle_ms', 'first_commit_wait_ms', 'commits_in_window'):
                        quantities[key].append(rec[key])
                records.append(rec)
            out['arms'][arm] = {k: summarize(v) for k, v in sorted(quantities.items())}
            out['arms'][arm]['trials'] = records
        if not cell.startswith('X2') and 'A' in arms and 'AMIN' in arms:
            a, m = out['arms']['A'], out['arms']['AMIN']
            observed = a['cycle_ms']['mean']
            predicted = m['cycle_ms']['mean'] + 2 * a['first_commit_wait_ms']['mean']
            out['cycle_model'] = dict(observed_ms=observed, predicted_ms=predicted,
                                     error_percent=100 * (predicted / observed - 1))
        cells[cell] = out
    return {'schema': 'x-readcost-paper-mechanism-v1', 'exploratory_post_hoc': True,
            'definitions': {'admission': 'mean over trials of reads_in_window / (r * commits_in_window)',
                            'cycle_ms': 'mean over trials of measured_window_ns / commits_in_window / 1e6',
                            'predicted_ms': 'mean A-min cycle_ms + 2 * mean A first_commit_wait_ms',
                            'error_percent': '100 * (predicted_ms / observed_ms - 1)',
                            'flush_scope': 'reset at window start; includes post-window drain, not strictly in-window'},
            'cells': cells}


def num(x, places=3):
    return f'{x:.{places}f}'


def magnitude_number(x, small_places):
    """Round the source value once; brace grouping makes commas math-safe."""
    return (f'{x:.{small_places}f}' if x < 100 else f'{x:,.0f}').replace(',', '{,}')


def cycle_number(x):
    return (f'{x:.3f}' if x < 10 else f'{x:,.1f}').replace(',', '{,}')


def descriptive_geometric_ratio(pairs):
    """Geometric mean of paired positive ratios, used for raw-trial p99 checks.

    The intervention results JSON omits p99, so this descriptive calculation
    takes (inline p99, second p99) pairs from trial JSON. It supplies no new CI
    or test and is distinct from a ratio of arithmetic means.
    """
    pairs = list(pairs)
    if not pairs or any(not math.isfinite(x) or not math.isfinite(y) or
                        x <= 0 or y <= 0 for x, y in pairs):
        raise ValueError('geometric ratios require nonempty positive finite pairs')
    return math.exp(st.mean(math.log(x) - math.log(y) for x, y in pairs))


def ratio(x, ci='t_ci', places=3):
    lo, hi = x[ci]
    return f"{num(x['geometric_mean_ratio'], places)} [{num(lo, places)}, {num(hi, places)}]"


def dose(cell):
    return {'d0000': '0', 'd0020': r'20\,\(\mu\)s', 'd0200': r'200\,\(\mu\)s',
            'd2000': '2 ms', 'legacy': 'legacy scan'}.get(
                next((p for p in cell.split('_') if p in DOSES), ''), '0')


def reader(cell):
    return int(cell.split('_')[1][1:])


def write_body(directory, name, rows):
    body = '\n'.join(rows)
    if name in TABLE_MACROS:
        body = '\\gdef\\' + TABLE_MACROS[name] + '{%\n' + body + '\n}%'
    (directory / (name + '.tex')).write_text('% Generated by x_readcost_paper_tables.py; do not edit.\n' +
                                             body + '\n', encoding='utf-8', newline='\n')


def generate_tables(data, mech, directory):
    directory.mkdir(parents=True, exist_ok=True)
    def geo(c, a, metric, scale=1):
        return data['cells'][c]['arms'][a][metric]['geometric_mean'] * scale
    def rates(c, metric):
        return [magnitude_number(geo(c, a, metric), 1) for a in ARMS]
    x1 = [f'X1_r{r:02d}_{d}' for r in (4, 16) for d in DOSES]
    rows = []
    for c in x1:
        if c == 'X1_r16_d0000':
            rows.append(r'\midrule')
        arms = data['cells'][c]['arms']
        cs = arms['A']['read_cs_mean']['mean'] / 1e3
        w = data['cells'][c]['ratios']['W']['A/AMIN']
        rr = data['cells'][c]['ratios']['R']['A/AMIN']
        rows.append(' & '.join([str(reader(c)), dose(c), magnitude_number(cs, 1)] + rates(c, 'W') +
                              [ratio(w, places=6 if c.endswith('legacy') else 3)] + rates(c, 'R') + [ratio(rr)]) + r' \\')
    write_body(directory, 'x1', rows)
    rows = []
    for c in x1 + ['X3_r01', 'X3_r04', 'X3_r16']:
        if c in ('X1_r16_d0000', 'X3_r01'):
            rows.append(r'\midrule')
        cell = mech['cells'][c]
        qs = [num(cell['arms'][a]['reads_per_reader_per_commit']['mean']) for a in ARMS]
        model = cell['cycle_model']
        cycle = [cycle_number(model[k]) for k in ('observed_ms', 'predicted_ms')] + [num(model['error_percent'], 1)] if c.startswith('X1') else ['--'] * 3
        rows.append(' & '.join(['Mixed' if c.startswith('X1') else 'Churn', str(reader(c)), dose(c)] + qs + cycle) + r' \\')
    write_body(directory, 'mechanism', rows)
    rows = []
    for c, x in sorted(data['H2']['publication_lock_wait_p99'].items()):
        cls = x['classification'].replace('_', ' ')
        rows.append(' & '.join([dose(c), ratio(x, places=2), cls]) + r' \\')
    write_body(directory, 'h2_publication', rows)
    rows = []
    for d in ('d0000', 'd0200', 'd2000', 'legacy'):
        c = 'X2_r16_' + d
        counts = []
        for a in ARMS:
            s = mech['cells'][c]['arms'][a]['publications_in_window']
            counts.append(f"{s['median']:g} [{s['min']}, {s['max']}]")
        rows.append(' & '.join([dose(c)] + rates(c, 'R') + counts +
                              [magnitude_number(geo(c, a, 'publication_lock_wait_p99', 1e-3), 2) for a in ARMS]) + r' \\')
    write_body(directory, 'x2', rows)
    rows = []
    for c in ('X3_r01', 'X3_r04', 'X3_r16'):
        flush = '/'.join(num(mech['cells'][c]['arms'][a]['measured_flushes']['mean'], 1) for a in ARMS)
        for metric in ('W', 'R'):
            x = data['H3'][c][metric]
            rows.append(' & '.join([str(reader(c)), metric] + rates(c, metric) + [ratio(x),
                f"[{num(x['bootstrap_ci'][0])}, {num(x['bootstrap_ci'][1])}]", flush if metric == 'W' else '--']) + r' \\')
    write_body(directory, 'x3', rows)
    # Architecture rows are audited against the source, not inferred from timings.
    write_body(directory, 'arms', [
        r'Memory-tier search under \texttt{Mu.RLock} & yes & yes & yes \\',
        r'Reader SSTable I/O under \texttt{Mu.RLock} & yes & no & no \\',
        r'Disk-reader lifetime & lock & table leases & set pin \\',
        r'Per-read lifetime synchronization & \texttt{Mu} & atomic leases & \texttt{snapshotMu} \\',
        r'New set descriptor on publication & no & no & yes \\',
        r'Conflict check and WAL sync under \texttt{Mu.Lock} & yes & yes & yes \\',
        r'Exclusive acquisitions per successful commit & 2 & 2 & 2 \\'])
    # Check the existing bootstrap intervals, without inventing bootstrap TOST p-values.
    margin = math.exp(data['equivalence_margin_log'])
    checks = {}
    for metric in ('W', 'R'):
        fam = data['H2'][metric]
        checks[metric] = dict(cells=len(fam), equivalent=sum(x['classification'] == 'equivalent' for x in fam.values()),
            all_bootstrap_intervals_within_margin=all(1 / margin < x['bootstrap_ci'][0] <= x['bootstrap_ci'][1] < margin for x in fam.values()))
    mech['h2_bootstrap_check'] = checks
    write_body(directory, 'facts', [
        r'\newcommand{\HOneP}{' + f"{data['H1']['4']['p'] / 1e-9:.2f}" + r'\times10^{-9}}',
        r'\newcommand{\CampaignSuccess}{' + str(data['counts']['successful']) + '}',
        r'\newcommand{\DriftD}{' + num(data['drift']['D']) + '}',
        r'\newcommand{\DriftLog}{' + num(abs(data['drift']['ln_D'])) + '}'] + [
        r'\newcommand{\Sensitivity' + metric + '}{' + str(sensitivity_count(data['H2'][metric])) + '}'
        for metric in ('W', 'R')])
    write_body(directory, 'summary', [
        ' & '.join([str(reader(c)), dose(c),
                    magnitude_number(data['cells'][c]['arms']['A']['read_cs_mean']['mean'] / 1e3, 1),
                    percent_change(data['cells'][c]['ratios']['W']['A/AMIN']['geometric_mean_ratio']),
                    percent_change(data['cells'][c]['ratios']['R']['A/AMIN']['geometric_mean_ratio'])]) + r' \\'
        for c in x1])


def percent_change(value):
    return f'{100 * (value - 1):+.1f}' + r'\%'


def sensitivity_count(family):
    """Post-hoc interval containment only, with no new tests or adjustment."""
    return sum(.95 <= v['t_ci'][0] <= v['t_ci'][1] <= 1 / .95 for v in family.values())


def coverage(trials):
    diagnostics = [t['result']['lsm_diagnostics'] for t in trials]
    flushes = [d['measured_flushes'] for d in diagnostics]
    return (len(trials), st.mean(flushes), max(flushes),
            sum(d['measured_compactions'] for d in diagnostics))


def generate_coverage(groups, directory):
    rows = []
    for name, trials in groups:
        n, mean, maximum, compactions = coverage(trials)
        rows.append(f'{name} & {n} & {mean:.2f} & {maximum} & {compactions}' + r' \\')
        print(f'Coverage {name}: trials={n}, mean_flushes={mean:.6f}, max_flushes={maximum}, compactions={compactions}')
    write_body(directory, 'coverage', rows)


def microbench_means(data):
    """Arithmetic mean of independent benchmark counts, not iteration-weighted."""
    groups = defaultdict(list)
    for row in data['microbenchmark']['measurements']:
        if row['compile_check_only']:
            continue
        name = row['benchmark'].split('-')[0]
        groups[name].append(row)
    return {name: {key: st.mean(row[key] for row in rows)
                   for key in ('ns_per_op', 'allocs_per_op')} for name, rows in groups.items()}


def generate_compaction(data, directory):
    rows, throughput, lifecycle = [], [], []
    for c, cell in sorted(data['cells'].items()):
        if not c.endswith('_on'):
            continue
        eq = cell['equivalence']
        fields = [str(reader(c)), dose(c)]
        for metric in ('W', 'R'):
            fields += [ratio(eq[metric]), eq[metric]['classification'].replace('_', ' ')]
        fields += [ratio(cell['ratios'][metric]['A/AMIN']) for metric in ('W', 'R')]
        fields += [num(cell['arms'][a]['commit_wait_p99']['mean'] / 1e3, 1) for a in ARMS]
        fields += [num(cell['arms'][a]['retirement_lag_mean_nanos']['mean'] / 1e6, 2) for a in ARMS]
        rows.append(' & '.join(fields) + r' \\')
        throughput.append(' & '.join(fields[:8]) + r' \\')
        lifecycle.append(' & '.join(fields[:2] + fields[8:]) + r' \\')
    write_body(directory, 'compaction', rows)
    write_body(directory, 'compaction_throughput', throughput)
    write_body(directory, 'compaction_lifecycle', lifecycle)


def generate_tablecount(data, directory):
    rows = []
    for c, cell in sorted(data['cells'].items()):
        n = cell['nominal']
        fields = [str(n['initial_sstable_count']), str(n['reader_concurrency']), str(n['writer_concurrency'])]
        for metric in ('R', 'W'):
            eq = cell['equivalence'].get(metric)
            fields += [ratio(eq), eq['classification'].replace('_', ' ')] if eq else ['--', '--']
        fields += [num(cell['arms'][a]['read_cs_mean']['mean'] / 1e3, 3) for a in ARMS]
        rows.append(' & '.join(fields) + r' \\')
    write_body(directory, 'tablecount', rows)
    bench = microbench_means(data)
    rows = []
    for n in (4, 16, 64, 256):
        fields = [str(n)]
        fields += [num(bench[f'BenchmarkLifecycle/{a}/N{n}/g{g}']['ns_per_op'], 1)
                   for g in (1, 16) for a in ('AMIN', 'B')]
        fields += [num(bench[f'BenchmarkLifecyclePublication/{a}/N{n}']['ns_per_op'], 1) for a in ('AMIN', 'B')]
        fields += [num(bench[f'BenchmarkLifecyclePublication/{a}/N{n}']['allocs_per_op'], 0) for a in ('AMIN', 'B')]
        rows.append(' & '.join(fields) + r' \\')
    write_body(directory, 'microbench', rows)


def paced_stall_summary(trials):
    """Summarize each trial's in-window maximum, never pool operation samples.

    This is descriptive arithmetic. Late service histograms are separate and do
    not enter the maximum of the in-window write-service distribution.
    """
    groups = defaultdict(list)
    seen = set()
    for trial in trials:
        if trial['trial_id'] in seen or trial['status'] != 'SUCCESS':
            raise ValueError('paced maxima require unique successful trials')
        seen.add(trial['trial_id'])
        hist = trial['result']['paced_load']['write_service_nanos']
        if hist['sample_count'] <= 0 or hist['max'] <= 0:
            raise ValueError('paced maximum missing: ' + trial['trial_id'])
        groups[(trial['cell'], trial['arm'])].append(hist['max'] / 1e6)
    if not groups:
        raise ValueError('paced maxima require nonempty trials')
    medians = [st.median(v) for v in groups.values()]
    maxima = [x for values in groups.values() for x in values]
    return dict(cell_arm_median_ms=(min(medians), max(medians)),
                individual_trial_ms=(min(maxima), max(maxima)))


def ticks_during_stall(stall_ms, writes_per_second):
    """Arrival-count scale only; not a claim about measured queue length."""
    return stall_ms * writes_per_second / 1000


def generate_paced(data, trials, directory, earlier, prior_trial_count, probe_text):
    """Copy frozen paced inference and emit explicitly descriptive arithmetic."""
    rows, descriptive = [], []
    for c, cell in sorted(data['cells'].items()):
        nominal = cell['nominal']
        mode = c.rsplit('_', 1)[1]
        fraction = .2 if '_f20_' in c else .8
        eq = cell['equivalence']['W_A/AMIN']
        delay = {0: '0', 200: r'200\,\(\mu\)s', 2000: '2 ms'}[nominal['read_io_delay_us']]
        fields = [delay, f'{fraction:.1f}', mode, str(nominal['paced_write_rate_per_sec'])]
        fields += [num(cell['arms'][a]['achieved_fraction']['mean']) for a in ARMS]
        fields += [str(cell['saturated_trials']['A']), ratio(eq), eq['classification'].replace('_', ' ')]
        fields += [num(cell['arms'][a]['write_response_p50']['mean'] / 1e6) for a in ('A', 'AMIN')]
        rows.append(' & '.join(fields) + r' \\')
        read_metric = 'R' if mode == 'closed' else 'read_response_p99'
        fields = [delay, f'{fraction:.1f}', mode]
        fields += [ratio(cell['ratios'][m]['A/AMIN']) for m in
                   ('write_response_p50', 'write_service_p50', read_metric)]
        fields += [ratio(cell['ratios']['write_response_p99']['AMIN/B'])]
        descriptive.append(' & '.join(fields) + r' \\')
    facts = {}
    def span(values, places=2):
        values = list(values)
        lo, hi = num(min(values), places), num(max(values), places)
        return lo if lo == hi else lo + '--' + hi
    def cell(delay, f, mode):
        return data['cells'][f'P1_d{delay:04d}_f{f}_{mode}']
    def series(metric, f, mode, ci=False, places=3):
        vals = [cell(d, f, mode)['ratios'][metric]['A/AMIN'] for d in (0, 200, 2000)]
        return ', '.join(ratio(v, places=places) if ci else num(v['geometric_mean_ratio'], places) for v in vals)
    def sci(value):
        mantissa, exponent = f'{value:.1e}'.split('e')
        return mantissa + r'\times10^{' + str(int(exponent)) + '}'
    facts['PacedTrialCount'] = str(data['counts']['successful'])
    facts['PacedTotalTrials'] = f"{prior_trial_count + data['counts']['successful']:,}"
    for delay, name in ((0, 'Zero'), (200, 'Medium'), (2000, 'High')):
        facts['PacedRates' + name] = '/'.join(str(cell(delay, f, 'closed')['nominal']['paced_write_rate_per_sec']) for f in (20, 80))
    facts['PacedOpenRates'] = ', '.join(magnitude_number(cell(d, 20, 'open')['nominal']['open_loop_rate_per_sec'], 0) for d in (0, 200, 2000))
    for f, load in ((20, 'Low'), (80, 'High')):
        for mode in ('closed', 'open'):
            test = data['H_P1'][f'f{f}_{mode}']
            name = load + mode.title()
            facts['PacedPage' + name] = num(test['L'], 0)
            facts['PacedPageP' + name] = num(test['p']) if f == 20 else sci(test['p'])
            facts['PacedPageHolm' + name] = num(test['holm_p']) if f == 20 else sci(test['holm_p'])
            facts['PacedResponse' + name] = series('write_response_p50', f, mode)
            facts['PacedService' + name] = series('write_service_p50', f, mode)
    facts['PacedHPTwoCount'] = str(sum(v['classification'] == 'equivalent' for v in data['H_P2'].values()))
    facts['PacedMedianPenalty'] = num(max(cell(d, 20, mode)['ratios']['write_response_p50']['A/AMIN']['geometric_mean_ratio']
                                        for d in (0, 200, 2000) for mode in ('closed', 'open')), 1)
    facts['PacedHPThreeCount'] = str(sum(v['matches'] for v in data['predictions'] if v['hypothesis'] == 'H_P3'))
    for m in ('W', 'R'):
        facts['PacedHPFour' + m + 'Count'] = str(sum(v['classification'] == 'equivalent' for v in data['H_P4'][m].values()))
    for mode in ('closed', 'open'):
        facts['PacedHPThreeMedium' + mode.title()] = ratio(cell(200, 80, mode)['equivalence']['W_A/AMIN'])
        facts['PacedHPThreeHigh' + mode.title()] = num(cell(2000, 80, mode)['equivalence']['W_A/AMIN']['geometric_mean_ratio'])
    facts['PacedMediumSaturated'] = str(sum(cell(200, 80, mode)['saturated_trials']['A'] for mode in ('closed', 'open')))
    exception = data['H_P4']['R']['P1_d0000_f80_closed']
    facts['PacedReadException'] = ratio(exception)
    for key, suffix, places in [('holm_p', 'Holm', 2), ('median_ratio', 'Median', 3), ('sd_log', 'SD', 3)]:
        facts['PacedReadException' + suffix] = num(exception[key], places)
    capacity = earlier['cells']['X1_r16_d0200']['arms']['A']['W']['mean']
    offered = cell(200, 80, 'closed')['nominal']['paced_write_rate_per_sec']
    facts['PacedDesignCapacity'] = num(capacity, 1)
    facts['PacedDesignFraction'] = num(capacity / offered)
    facts['PacedDesignOffered'] = str(offered)
    stall = paced_stall_summary(trials)
    facts['PacedStallMedians'] = span(stall['cell_arm_median_ms'], 0)
    facts['PacedStallRange'] = span(stall['individual_trial_ms'], 0)
    facts['PacedStallTicks'] = span([ticks_during_stall(x, cell(0, 20, 'closed')['nominal']['paced_write_rate_per_sec']) for x in stall['cell_arm_median_ms']], 0)
    zero_trials = [t for t in trials if t['cell'] == 'P1_d0000_f20_closed']
    n = round(st.median(t['result']['paced_load']['write_service_nanos']['sample_count'] for t in zero_trials))
    facts['PacedLowWriteCount'] = str(n)
    facts['PacedTailRank'] = str(round(.01 * n))
    paired = defaultdict(dict)
    for t in data['trials'].values():
        if t['cell'] == 'P1_d0000_f20_closed':
            paired[t['triplet']][t['arm']] = t['write_response_p99']
    facts['PacedTailRatioRange'] = span([v['A'] / v['AMIN'] for v in paired.values()], 2)
    maxima = [float(v) for v in re.findall(r'^rate .*? max ([\d.]+) ms;', probe_text, re.M)]
    if len(maxima) != 6:
        raise ValueError('expected six saved fsync probe summaries')
    facts['PacedProbeCount'] = str(len(maxima))
    facts['PacedProbeMaxima'] = span(maxima, 1)
    for delay, name in ((200, 'Medium'), (2000, 'High')):
        arm = cell(delay, 80, 'closed')['arms']['A']
        facts['PacedCapacity' + name] = num(arm['W']['mean'], 1)
        facts['PacedEarlierCapacity' + name] = num(earlier['cells'][f'X1_r16_d{delay:04d}']['arms']['A']['W']['mean'], 1)
        facts['PacedReads' + name] = magnitude_number(arm['R']['mean'], 0)
        facts['PacedCoupledReads' + name] = magnitude_number(2 * cell(delay, 80, 'closed')['nominal']['reader_concurrency'] * arm['W']['mean'], 0)
    high = cell(2000, 80, 'closed')
    facts['PacedBacklogObserved'] = num(high['arms']['A']['write_response_p99']['mean'] / 1e9, 2)
    facts['PacedBacklogModel'] = num(high['nominal']['measurement_duration_ms'] / 1000 * (1 - high['arms']['A']['W']['mean'] / high['nominal']['paced_write_rate_per_sec']), 2)
    facts['PacedHighOffered'] = str(high['nominal']['paced_write_rate_per_sec'])
    facts['PacedReadLowClosed'] = series('R', 20, 'closed', ci=True)
    facts['PacedReadHighClosed'] = series('R', 80, 'closed', ci=True)
    facts['PacedReadResponseLowOpen'] = series('read_response_p99', 20, 'open', ci=True, places=2)
    high_cells = [v for k, v in data['cells'].items() if '_f80_' in k]
    facts['PacedLSTailMedians'] = span([st.median(c['arms'][a]['write_response_p99']['values']) / 1e6 for c in high_cells for a in ('AMIN', 'B')], 0)
    facts['PacedLSResponseMedians'] = span([st.median(c['arms'][a]['write_response_p50']['values']) / 1e6 for c in high_cells for a in ('AMIN', 'B')], 1)
    facts['PacedUnissuedAboveFlag'] = str(max(t['unissued_ticks'] for t in data['trials'].values() if not t['saturated']))
    facts['PacedLSServiceRange'] = span([c['ratios']['write_service_p50']['AMIN/B']['geometric_mean_ratio'] for c in data['cells'].values()], 3)
    definitions = [r'\gdef\TblPacedRows{%' + '\n' + '\n'.join(rows) + '\n}%']
    definitions += [r'\gdef\TblPacedDescriptiveRows{%' + '\n' + '\n'.join(descriptive) + '\n}%']
    definitions += ['\\newcommand{\\' + k + '}{' + v + '}' for k, v in sorted(facts.items())]
    write_body(directory, 'paced', definitions)


def generate_campaign_facts(extra, intervention_trials, directory):
    """Descriptive arithmetic for prose, copied from frozen results or raw p99s.

    No CI, test, or classification is recomputed here.
    """
    facts = {}
    def span(name, values, places=2, scale=1):
        vals = [v * scale for v in values]
        lo, hi = f'{min(vals):.{places}f}', f'{max(vals):.{places}f}'
        facts[name] = lo if lo == hi else lo + '--' + hi
    paired = defaultdict(dict)
    for t in intervention_trials:
        condition = 'inline' if '_inline_' in t['trial_id'] else 'second'
        paired[(t['cell'].rsplit('_', 1)[0], t['triplet'], t['arm'])][condition] = t['result']['latency_read_nanos']['p99']
    for arm, name in [('A', 'CL'), ('AMIN', 'L')]:
        by_cell = defaultdict(list)
        for (cell, _, a), vals in paired.items():
            if a == arm:
                by_cell[cell].append((vals['inline'], vals['second']))
        span('InterventionTail' + name, [descriptive_geometric_ratio(v) for v in by_cell.values()], 3)
    if 'Compaction' in extra:
        data = extra['Compaction']
        on = [v for k, v in sorted(data['cells'].items()) if k.endswith('_on')]
        span('CompWaitRatio', [v['ratios']['commit_wait_p99']['AMIN/B']['geometric_mean_ratio'] for v in on])
        for a, name in [('A', 'CL'), ('AMIN', 'L'), ('B', 'S')]:
            span('CompWait' + name, [v['arms'][a]['commit_wait_p99']['mean'] for v in on], 0, 1e-3)
            span('CompLag' + name, [v['arms'][a]['retirement_lag_mean_nanos']['mean'] for v in on], 2, 1e-6)
        span('CompLeaseStartHold', [v['arms']['AMIN']['compaction_start_hold_mean_nanos']['mean'] for v in on], 0, 1e-3)
        span('CompHighWater', [v['arms'][a]['retired_tables_high_water']['mean'] for v in on for a in ARMS], 1)
        low = [v for k, v in data['cells'].items() if k.endswith('_on') and '_d2000_' not in k]
        high = [v for k, v in data['cells'].items() if k.endswith('_on') and '_d2000_' in k]
        span('CompCountLow', [v['arms'][a]['compactions']['mean'] for v in low for a in ARMS], 0)
        span('CompCountHighCL', [v['arms']['A']['compactions']['mean'] for v in high], 0)
        span('CompCountHighOther', [v['arms'][a]['compactions']['mean'] for v in high for a in ('AMIN', 'B')], 1)
        span('CompTablesOn', [v['arms'][a]['final_sstables']['mean'] for v in on for a in ARMS], 1)
        off = [v for k, v in data['cells'].items() if k.endswith('_off')]
        facts['CompTablesOffMax'] = num(max(v['arms'][a]['final_sstables']['mean'] for v in off for a in ARMS), 1)
        for a, name in [('A', 'CL'), ('AMIN', 'L')]:
            span('CompHighTables' + name, [v['arms'][a]['final_sstables']['mean'] for k, v in data['cells'].items() if k.endswith('_off') and '_d2000_' in k], 1)
        for m in ('W', 'R'):
            span('CompGain' + m, [v[a][m]['geometric_mean_ratio'] for v in data['on_off'].values() for a in ARMS])
        for r, name in [(4, 'Four'), (16, 'Sixteen')]:
            cells = [data['cells'][f'C1_r{r:02d}_d{d:04d}_on'] for d in (0, 200, 2000)]
            facts['CompS' + name] = ', '.join(num(v['ratios']['W']['A/AMIN']['geometric_mean_ratio']) for v in cells)
            for state in ('on', 'off'):
                v = data['cells'][f'C1_r{r:02d}_d2000_{state}']['ratios']['R']['A/AMIN']
                facts['CompRead' + name + state.title()] = ratio(v) if state == 'on' else num(v['geometric_mean_ratio'])
    if 'Table-count' in extra:
        data = extra['Table-count']
        bench = microbench_means(data)
        for r, name in [(4, 'Four'), (16, 'Sixteen')]:
            cells = [data['cells'][f'T1_n{n:03d}_r{r:02d}_w0'] for n in (4, 16, 64, 256)]
            test = data['H_T1'][f'r{r:02d}_w0']
            facts['TablePage' + name] = num(test['L'], 0)
            facts['TablePageP' + name] = num(test['holm_p'] / 1e-9, 2) + r'\times10^{-9}'
            facts['TableRead' + name] = ', '.join(num(v['equivalence']['R']['geometric_mean_ratio']) for v in cells)
            span('TableLeaseCS' + name, [v['arms']['AMIN']['read_cs_mean']['mean'] for v in cells], 0)
            facts['TableSlope' + name] = num((cells[-1]['arms']['AMIN']['read_cs_mean']['mean'] - cells[0]['arms']['AMIN']['read_cs_mean']['mean']) / 252, 0)
        span('TableWriterRatios', [v['geometric_mean_ratio'] for m in ('R', 'W') for c, v in data['H_T2'][m].items() if c.endswith('w1')], 3)
        for g, name in [(1, 'One'), (16, 'Sixteen')]:
            for a, arm in [('AMIN', 'L'), ('B', 'S')]:
                span('BenchRead' + arm + name, [bench[f'BenchmarkLifecycle/{a}/N{n}/g{g}']['ns_per_op'] for n in (4, 16, 64, 256)], 0)
        for a, arm in [('AMIN', 'L'), ('B', 'S')]:
            span('BenchPub' + arm, [bench[f'BenchmarkLifecyclePublication/{a}/N{n}']['ns_per_op'] for n in (4, 16, 64, 256)], 0)
        facts['BenchSlope'] = num((bench['BenchmarkLifecycle/AMIN/N256/g1']['ns_per_op'] - bench['BenchmarkLifecycle/AMIN/N4/g1']['ns_per_op']) / 252, 1)
    write_body(directory, 'campaign_facts', ['\\newcommand{\\' + k + '}{' + v + '}' for k, v in sorted(facts.items())])


def generate_intervention_table(data, directory):
    """Copy pre-registered estimates/CIs; q uses arithmetic condition means."""
    rows = []
    previous_r = None
    for cell, cs in sorted(data['cells'].items()):
        r = reader(cell)
        if previous_r is not None and r != previous_r:
            rows.append(r'\midrule')
        previous_r = r
        second, inline = [cs['conditions'][c] for c in ('second', 'inline')]
        qs = [num(condition['arms'][arm]['q']['mean'])
              for arm in ('A', 'AMIN') for condition in (second, inline)]
        writer = ratio(cs['ratios_inline_over_second']['A']['W'])
        ss = [num(condition['ratios_A_over_AMIN']['W']['geometric_mean_ratio'])
              for condition in (second, inline)]
        reads = [ratio(condition['ratios_A_over_AMIN']['R']) for condition in (second, inline)]
        rows.append(' & '.join([str(r), dose(cell)] + qs + [writer] + ss + reads) + r' \\')
    write_body(directory, 'intervention', rows)


def settled_phase(trial, phase, statistic='p50'):
    return trial['result']['stall_diagnostics']['phase_histograms'][phase]['stats'][statistic]


def settled_ratio90(source):
    """Re-express a stored Student-t log CI at 90%, preserving its estimate.

    General cell summaries only store 95% CIs. Scale their frozen log widths
    by t(.95, n-1)/t(.975, n-1); do not estimate variance or pairing again.
    H_M2's frozen 90% intervals pass through unchanged.
    """
    if source['level'] == .90:
        return source
    if source['level'] != .95 or source['n'] < 2:
        raise ValueError('expected a frozen 90% or 95% Student-t interval')
    from scipy.stats import t
    center = source['mean_log']
    if not math.isclose(math.exp(center), source['geometric_mean_ratio']):
        raise ValueError('frozen ratio and log center disagree')
    if any(not math.isclose(math.exp(v), b) for v, b in zip(source['t_ci_log'], source['t_ci'])):
        raise ValueError('frozen ratio/log interval disagree')
    scale = float(t.ppf(.95, source['n'] - 1) / t.ppf(.975, source['n'] - 1))
    logs = [center + (v - center) * scale for v in source['t_ci_log']]
    return dict(source, level=.90, t_ci_log=logs, t_ci=[math.exp(v) for v in logs])


def settled_groups(data, trials):
    """Validate raw coverage and frozen descriptive quantities before formatting."""
    if len(trials) != data['counts']['successful']:
        raise ValueError('settled trial count differs from frozen results')
    groups, seen = defaultdict(list), set()
    for t in sorted(trials, key=lambda t: t['trial_id']):
        if t['status'] != 'SUCCESS' or t['trial_id'] in seen:
            raise ValueError('settled requires unique successful trials')
        seen.add(t['trial_id'])
        groups[t['cell'], t['arm']].append(t)
    for c, cell in sorted(data['cells'].items()):
        if not c.startswith(('S1_', 'S2_', 'S3_')):
            continue
        for a, arm in cell['arms'].items():
            rows = groups[c, a]
            if sorted(t['triplet'] for t in rows) != cell['complete_rounds']:
                raise ValueError('settled pairing differs: ' + c + ' ' + a)
            for phase in ('lock_wait', 'flush_check', 'total'):
                source = arm['phase_' + phase + '_p50']
                values = [settled_phase(t, phase) for t in rows]
                if sorted(values) != sorted(source['values']) or not math.isclose(st.mean(values), source['mean']):
                    raise ValueError('settled phase differs: ' + c + ' ' + a + ' ' + phase)
            for metric, field in (('write_response_p50', 'write_response_nanos'),
                                  ('write_service_p50', 'write_service_nanos')):
                values = [t['result']['paced_load'][field]['p50'] for t in rows]
                if sorted(values) != sorted(arm[metric]['values']):
                    raise ValueError('settled response/service differs: ' + c + ' ' + a)
    return groups


def settled_check_facts(report):
    """Only format the deterministic exploratory report; never rerun inference."""
    facts = {}
    def span(v, places=1):
        return num(v['min'], places) + '--' + num(v['max'], places)
    def count(v):
        return str(v['k']) + '/' + str(v['n'])
    burst_rows = []
    for label, rate in (('72', '72'), ('155', '155'), ('183', '183'), ('650--730', '700')):
        settled = report['engine_reference'][rate if rate != '700' else '650-730']
        bare = report['bare'][rate]
        burst_rows.append(' & '.join([label, count(settled['any_ge_25']),
            num(100 * settled['fraction_ge_25'], 3), count(bare['any_ge_25']),
            count(bare['any_ge_25_excluding_first']), num(100 * bare['fraction_ge_25'], 3)]) + r' \\')
    facts['TblBurstRows'] = '%\n' + '\n'.join(burst_rows) + '\n'
    rho = report['bare_spearman_rate_bursts']
    facts.update(BareSpearman=num(rho['r'], 2), BareSpearmanP=num(rho['p']),
                 BareBurstRuns=str(len(report['burst_run_positions'])))
    # Use the report's individual slow-first population, not per-rate maxima.
    slow_first = report['bare_first_slow_ms']
    facts['BareFirstSlow'] = span(slow_first)
    facts['BareFirstSlowCount'] = str(sum(v['first_fsync_ge_25'] for v in report['bare'].values()))
    fsync_rows = []
    for condition in ('on', 'off'):
        arms = report['engine_on_off'][condition]
        pairs = report['paired_ratios'][condition]
        hold = report['hold_account'][condition]
        read_ranges = [magnitude_number(arms[a]['R']['min'], 0) + '--' +
                       magnitude_number(arms[a]['R']['max'], 0) for a in ('AMIN', 'B')]
        k = sum(arms[a]['any_ge_25']['k'] for a in arms)
        n = sum(arms[a]['runs'] for a in arms)
        facts['Fsync' + condition.title() + 'Bursts'] = f'{k}/{n}'
        fields = ['fsync ' + condition] + read_ranges + [f'{k}/{n}', num(hold['hold_share_pct']['median'], 1)]
        for metric in ('read', 'write'):
            v = pairs[metric]
            fields += [ratio(dict(geometric_mean_ratio=v['geometric_mean'], t_ci=v['ci90'])),
                       v['classification'].replace('_', ' ')]
        fsync_rows.append(' & '.join(fields) + r' \\')
        facts['FsyncPooled' + condition.title()] = num(report['pooled_log_R'][condition]['sd'])
    facts['TblFsyncRows'] = '%\n' + '\n'.join(fsync_rows) + '\n'
    v = report['pooled_log_R']['sd_off_over_on']
    facts['FsyncSdRatio'] = ratio(dict(geometric_mean_ratio=v['ratio'], t_ci=v['ci90']))
    off_service = [report['engine_on_off']['off'][a]['largest_write_service_ms'][k]
                   for a in ('AMIN', 'B') for k in ('min', 'max')]
    facts['FsyncOffService'] = num(min(off_service), 1) + '--' + num(max(off_service), 1)
    facts['FsyncOnPearson'] = r'\(-' + num(abs(report['on_pearson_log_R_write_excess']['r']), 2) + r'\)'
    for dataset, suffix in (('on', 'On'), ('settled_S3', 'Settled')):
        h = report['hold_account'][dataset]
        facts['Hold' + suffix + 'Median'] = num(h['hold_share_pct']['median'], 1)
        facts['Hold' + suffix + 'Range'] = span(h['hold_share_pct'])
        facts['Hold' + suffix + 'R'] = num(h['log_R_log_free_r'])
        facts['Hold' + suffix + 'Slope'] = num(h['log_R_log_free_slope'], 2)
        facts['HoldBurst' + suffix] = num(h['burst_share_pct']['median'], 1)
        facts['HoldVsBurst' + suffix] = num(h['hold_burst_r'], 2)
        facts['HoldUndershoot' + suffix] = num(h['mean_log_observed_over_predicted'], 2)
        adj = h['paired']['adjusted']
        facts['HoldAdj' + suffix] = ratio(dict(geometric_mean_ratio=adj['geometric_mean'], t_ci=adj['ci90']))
        facts['HoldAdj' + suffix + 'SD'] = num(adj['sd_log'])
    facts['HoldOff'] = num(report['hold_account']['off']['hold_share_pct']['median'], 1)
    facts['HoldRatioR'] = num(report['hold_account']['settled_S3']['paired']['raw_log_ratio_log_free_ratio_r'], 2)
    return facts


def settled_stall_facts(settle, runs, notes):
    """Cell medians of exploratory per-trial p50s; no campaign inference."""
    facts = {}
    for prefix, name in (('S_settled_', 'Settled'), ('S_back_to_back_', 'Back')):
        values = [r['stall_diagnostics']['phase_histograms']['wal_sync']['stats']['max'] / 1e6
                  for filename, r in sorted(settle.items()) if filename.startswith(prefix)]
        if len(values) != 9:
            raise ValueError('expected nine exploratory stall trials: ' + prefix)
        facts['Stall' + name + 'Median'] = num(st.median(values), 1)
        facts['Stall' + name + 'Range'] = num(min(values), 1) + '--' + num(max(values), 1)
    for arm, name in (('A', 'CL'), ('AMIN', 'Lease'), ('B', 'Snapshot')):
        for sync, suffix in ((True, 'On'), (False, 'Off')):
            rows = [r for r in runs.values() if r['arm'] == arm and
                    r['config']['read_io_delay_us'] == 0 and r['config']['wal_sync'] == sync and
                    r['config']['paced_write_rate_per_sec'] == 183]
            if len(rows) != 3:
                raise ValueError('expected three sync diagnostic trials')
            facts['StallSyncOff' + name + suffix] = num(st.median(
                r['stall_diagnostics']['phase_histograms']['total']['stats']['p50'] / 1e6 for r in rows))
    rows = [r for r in runs.values() if r['arm'] == 'A' and
            r['config']['read_io_delay_us'] == 2000 and r['config']['wal_sync']]
    for phase, suffix in (('lock_wait', 'Lock'), ('flush_check', 'Flush')):
        value = num(st.median(r['stall_diagnostics']['phase_histograms'][phase]['stats']['p50'] / 1e6 for r in rows))
        # Include only when the saved narrative explicitly agrees with evidence.
        label = 'lock wait ' if phase == 'lock_wait' else 'flush check '
        if len(rows) == 3 and label + value in notes:
            facts['StallSecondWait' + suffix] = value
        else:
            print('Dropped StallSecondWait' + suffix + ': notes/evidence do not agree')
    return facts


def generate_settled(data, trials, directory, earlier, prior_trial_count,
                     paced_trials, compaction_trials, stall_settle, stall_runs,
                     fsync_report, notes):
    """Frozen inference plus raw descriptive arithmetic, with no engine execution.

    S1 nonzero-dose rows copy H_M2 estimates and 90% CIs. Zero-dose S1 and
    S2/S3 rows copy the general cell estimates and rescale frozen 95% log-CI
    widths to the requested 90% level (settled_ratio90). No ratios of arm
    means, saturation exclusions, trial CIs, or new classifications are used.
    The frozen p50 ratios include all complete rounds; only p99 is censored
    for saturation by the analysis. Phase columns are computed from raw p50s.
    """
    groups = settled_groups(data, trials)
    facts = dict(SettledTrialCount=str(data['counts']['successful']),
                 SettledTotalTrials=magnitude_number(prior_trial_count + data['counts']['successful'], 0),
                 SettledGapMin=num(data['gaps']['min_ms'] / 1000),
                 SettledGapMax=num(data['gaps']['max_ms'] / 1000),
                 SettledPlanPrefix=data['plan_sha256'][:12], SettledRunnerPrefix=data['runner_sha256'][:12],
                 SettledSeed=str(data['master_seed']))
    page = data['H_M1']['closed']
    if any(data['H_M1']['open'][k] != page[k] for k in ('L', 'p', 'holm_p')):
        raise ValueError('Page modes differ; require separate macros')
    def sci(v):
        mantissa, exponent = f'{v:.2e}'.split('e')
        return mantissa + r'\times10^{' + str(int(exponent)) + '}'
    facts.update(SettledPageL=num(page['L'], 0), SettledPageP=sci(page['p']), SettledPageHolm=sci(page['holm_p']))
    def cell(d, mode):
        return data['cells'][f'S1_d{d:04d}_f20_{mode}']
    def interval(c):
        source = data['cells'][c]['ratios']['write_response_p50']['A/AMIN']
        if c in data['H_M2']:
            if not math.isclose(source['geometric_mean_ratio'], data['H_M2'][c]['geometric_mean_ratio']):
                raise ValueError('H_M2/cell ratio disagree: ' + c)
            source = data['H_M2'][c]
        return settled_ratio90(source)
    for mode in ('closed', 'open'):
        facts['SettledMedian' + mode.title()] = ', '.join(num(cell(d, mode)['ratios']['write_response_p50']['A/AMIN']['geometric_mean_ratio']) for d in (0, 200, 2000))
        facts['SettledMedianZero' + mode.title()] = ratio(interval(f'S1_d0000_f20_{mode}'))
        for d, suffix in ((200, 'Medium'), (2000, 'High')):
            facts['SettledMTwo' + suffix + mode.title()] = ratio(data['H_M2'][f'S1_d{d:04d}_f20_{mode}'])
        facts['SettledHW' + mode.title()] = ratio(data['H_W'][f'S2_d0200_w700_{mode}'])
    facts['SettledHWFraction'] = num(earlier['cells']['X1_r16_d0200']['arms']['A']['W']['mean'] / 700, 2)
    for phase, name in (('lock_wait', 'LockWait'), ('flush_check', 'Flush')):
        v = data['H_M3']['bands']['A_' + phase]
        facts['Settled' + name + 'CL'] = ratio(dict(geometric_mean_ratio=v['mean'] / 1e6,
            t_ci=[b / 1e6 for b in v['t_ci_95']]))
        facts['Settled' + name + 'L'] = num(data['H_M3']['bands']['AMIN_' + phase]['mean'] / 1e6,
                                          4 if phase == 'lock_wait' else 3)
    for m, name in (('R', 'Read'), ('W', 'Write')):
        facts['SettledHR' + name] = ratio(data['H_R'][m])
    facts['SettledHRReadHolm'] = num(data['H_R']['R']['holm_p'])
    facts['SettledHRReadSD'] = num(data['H_R']['R']['sd_log'])
    hs = [v['median_largest_fsync_ns'] / 1e6 for v in data['H_S']['trials'].values()]
    facts['SettledHSRange'] = num(min(hs), 1) + '--' + num(max(hs), 1)
    hc = data['H_C']
    facts['SettledSentinelTotal'] = str(len(hc))
    for value in ('unchanged', 'inconclusive', 'changed'):
        facts['SettledSentinel' + value.title()] = str(sum(v['comparison']['classification'] == value for v in hc))
    facts['SettledPublishedClasses'] = str(sum(v['published_class'] is not None for v in hc))
    facts['SettledContradicted'] = str(sum(v['classification_contradicted'] for v in hc))
    c1 = next(v for v in hc if v['cell'] == 'R_C1_r16_d0200_on' and v['metric'] == 'commit_wait_p99')
    for source, suffix in (('original', 'Old'), ('new', 'New')):
        facts['SettledCOneTail' + suffix] = num(c1[source]['geometric_mean_ratio'], 2)
    for rows, suffix, c in ((compaction_trials, 'Old', 'C1_r16_d0200_on'),
                             (trials, 'New', 'R_C1_r16_d0200_on')):
        for arm, name in (('AMIN', 'Lease'), ('B', 'Snapshot')):
            vals = [t['result']['sync_instrumentation']['writer_lock_wait_nanos']['commit']['p99_nanos'] / 1e6
                    for t in rows if t['cell'] == c and t['arm'] == arm]
            facts['SettledCOne' + name + suffix] = num(st.median(vals))
    rows = []
    for c, cs in sorted(data['cells'].items()):
        if not c.startswith(('S1_', 'S2_', 'S3_')):
            continue
        fields = [dose(c), str(cs['nominal']['paced_write_rate_per_sec']), c.rsplit('_', 1)[1], ratio(interval(c))]
        for arm in ('A', 'AMIN'):
            for phase in ('lock_wait', 'flush_check'):
                fields.append(num(st.mean(settled_phase(t, phase) for t in groups[c, arm]) / 1e6))
        fields.append(num(st.median(settled_phase(t, 'wal_sync', 'max') / 1e6
                                   for a in ARMS for t in groups[c, a]), 1))
        rows.append(' & '.join(fields) + r' \\')
    facts['TblSettledRows'] = '%\n' + '\n'.join(rows) + '\n'
    shares = []
    for d in (200, 2000):
        c = cell(d, 'closed')['arms']
        wait = sum(c['A']['phase_' + p + '_p50']['mean'] for p in ('lock_wait', 'flush_check'))
        shares.append(100 * wait / (c['A']['phase_total_p50']['mean'] - c['AMIN']['phase_total_p50']['mean']))
    facts['SettledPhaseShare'] = num(min(shares), 0) + '--' + num(max(shares), 0)
    # Across-campaign stall medians pool arms within each corresponding S1 cell.
    for source, label, prefix in ((paced_trials, 'Paced', 'P1_'), (trials, 'Settled', 'S1_')):
        medians = [st.median(t['result']['paced_load']['write_service_nanos']['max'] / 1e6
                            for t in source if t['cell'] == f'{prefix}d{d:04d}_f20_{mode}')
                   for d in (0, 200, 2000) for mode in ('closed', 'open')]
        places = 0 if label == 'Paced' else 1
        # Range endpoints use the prose precision (9.5--22 ms).
        facts['SettledStall' + label] = num(min(medians), places) + '--' + num(max(medians), 0)
    maxima = sorted((settled_phase(t, 'wal_sync', 'max') / 1e6 for t in trials
                     if t['cell'].startswith(('S1_', 'S2_', 'S3_'))), reverse=True)
    facts['SettledOutlier'], facts['SettledSecondOutlier'] = [num(v, 1) for v in maxima[:2]]
    for mode in ('closed', 'open'):
        c = f'S1_d2000_f20_{mode}'
        cross = next(v for v in data['across_campaign_paced'] if v['cell'] == c and
                     v['metric'] == 'write_response_p99' and v['pair'] == 'A/AMIN')
        for source, suffix in (('original', 'Old'), ('new', 'New')):
            facts['SettledTailRatio' + mode.title() + suffix] = num(cross[source]['geometric_mean_ratio'], 2)
        for source, suffix, name in ((paced_trials, 'Old', c.replace('S1_', 'P1_')), (trials, 'New', c)):
            vals = [t['result']['paced_load']['write_response_nanos']['p99'] / 1e6
                    for t in source if t['cell'] == name and t['arm'] == 'A']
            facts['SettledTailCL' + mode.title() + suffix] = num(st.median(vals), 2)
        facts['SettledTailSDZero' + mode.title()] = num(cell(0, mode)['ratios']['write_response_p99']['A/AMIN']['sd_log'], 2)
    first = []
    for t in trials:
        if t['cell'].startswith('S1_'):
            commits = t['result']['stall_diagnostics']['commits']
            c = min(commits, key=lambda c: c['index'])
            if c['index'] != 1:
                raise ValueError('first in-window commit not stored: ' + t['trial_id'])
            first.append((c['WalSyncDoneNs'] - c['WalWriteDoneNs']) / 1e6)
    facts['SettledFirstFsync'] = num(st.median(first), 1)
    facts['SettledFirstFsyncRange'] = num(min(first), 1) + '--' + num(max(first), 1)
    reads = [t['result']['throughput']['read_ops_per_sec'] for t in trials if t['cell'] == 'S3_d0000_f80_closed']
    facts['SettledReadRange'] = magnitude_number(min(reads), 0) + '--' + magnitude_number(max(reads), 0)
    facts.update(settled_stall_facts(stall_settle, stall_runs, notes))
    facts.update(settled_check_facts(fsync_report))
    definitions = [('\\gdef\\' + k + '{' + v + '}%' if k.startswith('Tbl') else
                    '\\newcommand{\\' + k + '}{' + v + '}') for k, v in sorted(facts.items())]
    write_body(directory, 'settled', definitions)
    return facts


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for arg in ('results', 'trials-dir', 'tables-dir', 'mechanism-out', 'intervention-results'):
        p.add_argument('--' + arg, type=Path, required=True)
    for arg in ('compaction-results', 'tablecount-results', 'paced-results', 'settled-results', 'fsync-report'):
        p.add_argument('--' + arg, type=Path)
    args = p.parse_args()
    if bool(args.settled_results) != bool(args.fsync_report):
        p.error('--settled-results and --fsync-report must be supplied together')
    if args.settled_results and not all((args.paced_results, args.compaction_results, args.tablecount_results)):
        p.error('settled output requires all preceding campaign inputs')
    data = json.loads(args.results.read_text(encoding='utf-8'))
    files = sorted(args.trials_dir.glob('*.json'))
    trials = [json.loads(f.read_text(encoding='utf-8')) for f in files]
    if len(trials) != data['counts']['successful']:
        raise ValueError('trial envelope count differs from final successful count')
    mech = mechanism(trials)
    if set(mech['cells']) != set(data['cells']):
        raise ValueError('trial cells differ from final results cells')
    for cell, summary in mech['cells'].items():
        if set(summary['arms']) != set(ARMS):
            raise ValueError('incomplete arm set: ' + cell)
        expected = data['cells'][cell]['n_complete']
        for arm, values in summary['arms'].items():
            expected_triplets = set(data['cells'][cell]['complete_triplets'])
            if (len(values['trials']) != expected or
                    {t['triplet'] for t in values['trials']} != expected_triplets):
                raise ValueError('trial pairing differs from final results: ' + cell + ' ' + arm)
    if any(t['platform']['git_revision'] != data['git_revision'] for t in trials):
        raise ValueError('trial revision differs from final results')
    first = min(t['timing']['started_utc'] for t in trials)
    last = max(t['timing']['finished_utc'] for t in trials)
    mech['campaign_timing'] = dict(first_started_utc=first, last_finished_utc=last,
        elapsed_seconds=(datetime.fromisoformat(last.replace('Z', '+00:00')) -
                         datetime.fromisoformat(first.replace('Z', '+00:00'))).total_seconds())
    mech['summary'] = {}
    for legacy in (False, True):
        vals = [v['arms']['A']['reads_per_reader_per_commit'] for c, v in mech['cells'].items()
                if not c.startswith('X2') and c.endswith('legacy') == legacy]
        mech['summary']['A_admission_legacy' if legacy else 'A_admission_nonlegacy'] = dict(
            individual_trial_min=min(v['min'] for v in vals),
            individual_trial_max=max(v['max'] for v in vals), cells=len(vals))
    errors = [v['cycle_model']['error_percent'] for c, v in mech['cells'].items() if c.startswith('X1')]
    mech['summary']['X1_cycle_error_percent'] = dict(min=min(errors), max=max(errors))
    flushes = [a['measured_flushes'] for c, v in mech['cells'].items() if c.startswith('X3') for a in v['arms'].values()]
    mech['summary']['X3_flushes'] = dict(cell_arm_mean_min=min(v['mean'] for v in flushes),
        cell_arm_mean_max=max(v['mean'] for v in flushes),
        individual_trial_min=min(v['min'] for v in flushes), individual_trial_max=max(v['max'] for v in flushes))
    mech['sources'] = dict(results_sha256=digest(args.results),
                           trials_sha256={f.name: digest(f) for f in files})
    generate_tables(data, mech, args.tables_dir)
    intervention = json.loads(args.intervention_results.read_text(encoding='utf-8'))
    generate_intervention_table(intervention, args.tables_dir)
    # A manuscript rewrite must not rewrite frozen analysis JSON, even identically.
    if json.loads(args.mechanism_out.read_text(encoding='utf-8')) != mech:
        raise ValueError('existing mechanism.json differs; refusing to modify analysis JSON')
    def trial_rows(results_path):
        folder = results_path.parent.parent / results_path.parent.name.removeprefix('analysis_') / 'trials'
        return [json.loads(f.read_text(encoding='utf-8')) for f in sorted(folder.glob('*.json'))]
    intervention_trials = trial_rows(args.intervention_results)
    if len(intervention_trials) != intervention['counts']['successful']:
        raise ValueError('coverage trial count differs: Intervention')
    groups = [('Mixed', [t for t in trials if t['cell'].startswith('X1') or t['cell'] == 'ANCHOR']),
              ('Publication', [t for t in trials if t['cell'].startswith('X2')]),
              ('Churn', [t for t in trials if t['cell'].startswith('X3')]),
              ('Intervention', intervention_trials)]
    extra = {}
    for path, name, generator in ((args.compaction_results, 'Compaction', generate_compaction),
                                  (args.tablecount_results, 'Table-count', generate_tablecount)):
        if path:
            result = json.loads(path.read_text(encoding='utf-8'))
            rows = trial_rows(path)
            if len(rows) != result['counts']['successful']:
                raise ValueError('coverage trial count differs: ' + name)
            generator(result, args.tables_dir)
            groups.append((name, rows))
            extra[name] = result
    if args.paced_results:
        paced = json.loads(args.paced_results.read_text(encoding='utf-8'))
        rows = trial_rows(args.paced_results)
        if len(rows) != paced['counts']['successful']:
            raise ValueError('coverage trial count differs: Paced')
        probe_text = (args.paced_results.parent.parent / 'fsync_probe.txt').read_text(encoding='utf-8')
        prior_count = data['counts']['successful'] + intervention['counts']['successful'] + sum(v['counts']['successful'] for v in extra.values())
        generate_paced(paced, rows, args.tables_dir, data, prior_count, probe_text)
        groups.append(('Paced', rows))
    if args.settled_results:
        # Load the large frozen JSON once. Evidence inputs are read-only.
        settled = json.loads(args.settled_results.read_text(encoding='utf-8'))
        settled_trials = trial_rows(args.settled_results)
        root = args.settled_results.parent.parent
        if digest(root / 'settled' / 'plan.json') != settled['plan_sha256']:
            raise ValueError('settled plan hash differs from frozen source')
        def diagnostics(folder, pattern):
            return {f.name: json.loads(f.read_text(encoding='utf-8')) for f in sorted(folder.glob(pattern))}
        generate_settled(settled, settled_trials, args.tables_dir, data,
            prior_count + paced['counts']['successful'], rows, trial_rows(args.compaction_results),
            diagnostics(root / 'stall_diag' / 'settle', 'S_*.json'),
            diagnostics(root / 'stall_diag' / 'runs', '*.json'),
            json.loads(args.fsync_report.read_text(encoding='utf-8')),
            (root / 'OVERNIGHT_NOTES.md').read_text(encoding='utf-8'))
        groups.append(('Settled', settled_trials))
    generate_coverage(groups, args.tables_dir)
    generate_campaign_facts(extra, intervention_trials, args.tables_dir)
    print(f'Generated manuscript tables; verified unchanged mechanism from {len(trials)} trial envelopes.')
    print(json.dumps(mech['h2_bootstrap_check'], sort_keys=True))


if __name__ == '__main__':
    main()
