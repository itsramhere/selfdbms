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


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for arg in ('results', 'trials-dir', 'tables-dir', 'mechanism-out', 'intervention-results'):
        p.add_argument('--' + arg, type=Path, required=True)
    for arg in ('compaction-results', 'tablecount-results', 'paced-results'):
        p.add_argument('--' + arg, type=Path)
    args = p.parse_args()
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
    generate_coverage(groups, args.tables_dir)
    generate_campaign_facts(extra, intervention_trials, args.tables_dir)
    print(f'Generated manuscript tables; verified unchanged mechanism from {len(trials)} trial envelopes.')
    print(json.dumps(mech['h2_bootstrap_check'], sort_keys=True))


if __name__ == '__main__':
    main()
