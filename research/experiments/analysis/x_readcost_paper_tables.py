#!/usr/bin/env python3
"""Generate manuscript table bodies and exploratory mechanism arithmetic.

No trials or inferential analyses are run. Existing CIs/classifications are copied
from --results and --intervention-results. Mechanism summaries are arithmetic means of per-trial quantities;
cycle = measured_window_ns / in-window commits. Wait instrumentation also includes
the post-window drain, as specified by the protocol. No late operation counts enter
the admission ratio. Anchors are excluded from cell summaries.
Table files define row macros for loading in the preamble; tabular environments
expand the macros instead of reading files while an alignment is active.

After rendering figures, copy their PDFs reproducibly from the repository root:
  python -c "from pathlib import Path; import shutil; d=Path('paper/figures/x_readcost'); d.mkdir(parents=True, exist_ok=True); [shutil.copyfile(p,d/p.name) for p in sorted(Path('research/experiments/phase_i7/X_READCOST/analysis/figures').glob('fig_*.pdf'))]"

All outputs use sorted input names, UTF-8, LF, and no wall-clock timestamps.
"""
import argparse
from collections import defaultdict
from datetime import datetime
import hashlib
import json
import math
from pathlib import Path
import statistics as st

ARMS = ('A', 'AMIN', 'B')
DOSES = ('d0000', 'd0020', 'd0200', 'd2000', 'legacy')
TABLE_MACROS = {'arms': 'TblArmsRows', 'x1': 'TblXOneRows',
                'mechanism': 'TblMechanismRows', 'h2_publication': 'TblHTwoPubRows',
                'x2': 'TblXTwoRows', 'x3': 'TblXThreeRows',
                'intervention': 'TblInterventionRows'}


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
            'd2000': '2 ms', 'legacy': 'legacy'}.get(cell.split('_')[-1], '0')


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
        rows.append(' & '.join([c[:2], str(reader(c)), dose(c)] + qs + cycle) + r' \\')
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
        r'\newcommand{\DriftLog}{' + num(abs(data['drift']['ln_D'])) + '}'])


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
    args.mechanism_out.parent.mkdir(parents=True, exist_ok=True)
    args.mechanism_out.write_text(json.dumps(mech, indent=2, sort_keys=True, allow_nan=False) + '\n',
                                  encoding='utf-8', newline='\n')
    print(f'Generated 8 table/macro bodies; mechanism from {len(trials)} trial envelopes.')
    print(json.dumps(mech['h2_bootstrap_check'], sort_keys=True))


if __name__ == '__main__':
    main()
