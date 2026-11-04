#!/usr/bin/env python3
"""Exploratory Phase 2, frozen in OVERNIGHT_NOTES.md: existing records only.

Run from the repository root:
  python research/experiments/analysis/x_phase2_analyze.py

No fitted parameters, inference tests, resampling, or trials. Also generates
paper/generated/phase2.tex from the serialized phase2_results.json values.
JSON uses sorted keys and twelve fixed decimal places; PDF dates are suppressed.
"""
import argparse
from collections import Counter, defaultdict
import hashlib
import json
import math
from pathlib import Path
import re
import shutil
import statistics as st

import numpy as np
from scipy.stats import spearmanr
import x_readcost_figures as style
from matplotlib.lines import Line2D


ROOT = Path(__file__).resolve().parents[3]
DEFAULT_X = ROOT / 'research/experiments/phase_i7/X_READCOST'
BOUND = math.log(1 / .90)
MODELS = ('M1', 'M2')
PHASES = ('lock_wait', 'flush_check', 'wal_sync', 'total')
QUANTILES = ('min', 'p50', 'p95', 'p99', 'p999', 'max')
SETTLED = ('S1_d0000_f20_closed', 'S1_d2000_f20_closed')
FIGURES = ('fig_penalty_collapse', 'fig_read_quantiles', 'fig_commit_cdf')
SOURCES = ('campaign', 'intervention', 'compaction', 'tablecount', 'settled')
# Enumerate the frozen selection so an entirely missing cell cannot disappear.
SELECTION = {
    'development': ('campaign', [f'X1_r{r:02d}_d{d:04d}'
                                for r in (4, 16) for d in (0, 20, 200, 2000)]),
    'legacy': ('campaign', [f'X1_r{r:02d}_legacy' for r in (4, 16)]),
    'intervention_second': ('intervention', [f'I1_r{r:02d}_d{d:04d}_second'
                                           for r in (4, 16) for d in (0, 20, 200, 2000)]),
    'intervention_inline': ('intervention', [f'I1_r{r:02d}_d{d:04d}_inline'
                                           for r in (4, 16) for d in (0, 20, 200, 2000)]),
    'compaction_on': ('compaction', [f'C1_r{r:02d}_d{d:04d}_on'
                                   for r in (4, 16) for d in (0, 200, 2000)]),
    'compaction_off': ('compaction', [f'C1_r{r:02d}_d{d:04d}_off'
                                    for r in (4, 16) for d in (0, 200, 2000)]),
    'churn': ('campaign', [f'X3_r{r:02d}' for r in (1, 4, 16)]),
    'tablecount_writer': ('tablecount', [f'T1_n{n:03d}_r{r:02d}_w1'
                                       for n in (4, 16, 64, 256) for r in (4, 16)]),
}
TEST_GROUPS = tuple(sorted(set(SELECTION) - {'development', 'legacy'}))
METADATA = {'schema', 'trial_id', 'block', 'cell', 'triplet', 'arm',
            'order_position', 'runlist_position', 'seed', 'data_dir'}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def positive(value, context):
    require(isinstance(value, (int, float)) and not isinstance(value, bool)
            and math.isfinite(value) and value > 0, 'nonpositive/missing ' + context)
    return value


def nominal(spec):
    """All settings except scheduling/identity metadata; retain acquisition mode."""
    out = {k: v for k, v in spec.items() if k not in METADATA}
    for key, default in (('read_io_delay_us', 0), ('inline_flush_check', False),
                         ('compaction_trigger_interval_ms', 0)):
        out.setdefault(key, default)
    return out


def predictions(h, h_p95, cycle, k):
    for key, val in (('h', h), ('h_p95', h_p95), ('cycle', cycle)):
        positive(val, key)
    require(k in (1, 2), 'k must be 1 or 2')
    return {'M1': 1 / (1 + k * h / cycle),
            'M2': 1 / (1 + k * h_p95 / cycle)}


def residual_summary(values):
    require(bool(values) and all(math.isfinite(v) for v in values), 'empty/invalid residuals')
    return dict(n=len(values), median_absolute=st.median(abs(v) for v in values),
                largest_absolute=max(abs(v) for v in values), mean_signed=st.mean(values))


def criteria(residuals, pair_differences):
    """Frozen inclusive bounds, evaluated before output rounding; no tolerance."""
    summary = residual_summary(residuals)
    mean_difference = st.mean(pair_differences) if pair_differences else None
    cs = {
        '1': dict(value=summary['median_absolute'], bound=BOUND,
                  passes=summary['median_absolute'] <= BOUND),
        '2': dict(value=summary['largest_absolute'], bound=2 * BOUND,
                  passes=summary['largest_absolute'] <= 2 * BOUND),
        '3': dict(value=mean_difference, bound=BOUND, n_pairs=len(pair_differences),
                  passes=abs(mean_difference) <= BOUND if mean_difference is not None else None),
    }
    return dict(criteria=cs, collapses=all(c['passes'] is True for c in cs.values()),
                failed_criteria=[k for k, c in cs.items() if c['passes'] is False],
                uncomputable_criteria=[k for k, c in cs.items() if c['passes'] is None])


def reader_pairs(cells):
    """Pair only test cells in the same group/condition, changing r alone."""
    groups = defaultdict(dict)
    for cell, row in sorted(cells.items()):
        if row['set'] == 'test' and row['r'] in (4, 16):
            key = (row['group'], re.sub(r'_r\d+(?=_|$)', '_r*', cell))
            require(row['r'] not in groups[key], 'ambiguous reader pair: ' + cell)
            groups[key][row['r']] = cell
    pairs = []
    for (group, _), arms in sorted(groups.items()):
        if set(arms) != {4, 16}:
            continue
        low, high = (cells[arms[r]] for r in (4, 16))
        settings = [{k: v for k, v in c['settings'].items() if k != 'reader_concurrency'}
                    for c in (low, high)]
        require(settings[0] == settings[1] and low['k'] == high['k'],
                'reader pair differs in other settings: ' + str(arms))
        pairs.append(dict(group=group, r4=arms[4], r16=arms[16],
                          differences={m: high['residuals'][m] - low['residuals'][m]
                                       for m in MODELS}))
    return pairs


def summarize_cell(cell, group, trials):
    """Mean hold/p95 and Lease cycles; geometric mean of paired recorded W."""
    by_round = defaultdict(dict)
    settings = None
    for t in sorted(trials, key=lambda t: t['trial_id']):
        arm, triplet = t['arm'], t['triplet']
        if arm not in ('A', 'AMIN'):
            continue
        require(arm not in by_round[triplet], 'duplicate cell/round/arm: ' + t['trial_id'])
        n = nominal(t['spec'])
        if settings is None:
            settings = n
        require(n == settings, 'condition mismatch: ' + t['trial_id'])
        by_round[triplet][arm] = t
    n_rounds = 5 if group.startswith('intervention_') else 10
    require(set(by_round) == set(range(1, n_rounds + 1)), f'expected {n_rounds} rounds: ' + cell)
    require(all(set(v) == {'A', 'AMIN'} for v in by_round.values()), 'unpaired rounds: ' + cell)
    records = []
    for triplet, arms in sorted(by_round.items()):
        a, lease = arms['A']['result'], arms['AMIN']['result']
        # Discover the key from this trial's recorded mode; never pick the first
        # dict entry or silently use another mode when instrumentation is absent.
        mode = a.get('sync_mode')
        instrumentation = a.get('sync_instrumentation', {}).get('read_critical_section_nanos', {})
        require(mode is not None and mode in instrumentation,
                'missing CoarseLock mode key: ' + arms['A']['trial_id'])
        hold = instrumentation[mode]
        h = positive(hold.get('mean_nanos'), cell + ' hold mean')
        hp = positive(hold.get('p95_nanos'), cell + ' hold p95')
        window = positive(lease['timestamps'].get('measured_window_ns'), cell + ' Lease window')
        commits = positive(lease['operations'].get('write_ops_completed_in_window'), cell + ' Lease commits')
        wa, wl = [positive(r['throughput'].get('write_ops_per_sec'), cell + ' throughput')
                  for r in (a, lease)]
        records.append(dict(round=triplet, coarse_trial_id=arms['A']['trial_id'],
                            lease_trial_id=arms['AMIN']['trial_id'], coarse_mode_key=mode,
                            h_ns=h, h_p95_ns=hp, lease_window_ns=window,
                            lease_commits_in_window=commits, lease_cycle_ns=window / commits,
                            W_CL=wa, W_L=wl, log_ratio=math.log(wa) - math.log(wl)))
    h, hp, cycle = [st.mean(t[key] for t in records)
                    for key in ('h_ns', 'h_p95_ns', 'lease_cycle_ns')]
    k = 1 if group == 'intervention_inline' else 2
    predicted = predictions(h, hp, cycle, k)
    log_s = st.mean(t['log_ratio'] for t in records)
    return dict(group=group, set=group if group in ('development', 'legacy') else 'test',
                r=settings['reader_concurrency'], k=k, n_rounds=len(records), settings=settings,
                h_ns=h, h_p95_ns=hp, T_L_ns=cycle, S=math.exp(log_s), x=h / cycle,
                predicted=predicted, residuals={m: log_s - math.log(p) for m, p in predicted.items()},
                trials=records)


def pool_histograms(histograms):
    """Sum bucket counts across trials; CDF masses stay at recorded lower_ns.

    This is a bucket-resolution empirical CDF, not exact within-bucket timing.
    Zero mass is retained in the denominator and CDF, including on log plots.
    """
    counts = Counter()
    require(bool(histograms), 'no histograms to pool')
    for h in histograms:
        seen = set()
        for b in h['buckets']:
            low, count = b['lower_ns'], b['count']
            require(type(low) is int and low >= 0 and type(count) is int and count >= 0,
                    'invalid histogram bucket')
            require(low not in seen, 'duplicate histogram bucket')
            seen.add(low)
            counts[low] += count
        require(sum(b['count'] for b in h['buckets']) == h['stats']['sample_count'],
                'histogram count differs from sample_count')
    total = sum(counts.values())
    require(total > 0, 'empty pooled histogram')
    cumulative, buckets = 0, []
    for low, count in sorted(counts.items()):
        if count:
            cumulative += count
            buckets.append(dict(lower_ns=low, count=count, cdf=cumulative / total))
    return dict(sample_count=total, zero_count=counts[0], buckets=buckets)


def latency_data(index):
    reads, commit = {}, {}
    for arm in ('A', 'AMIN'):
        trials = sorted([t for t in index['campaign', 'X1_r04_d0000'] if t['arm'] == arm],
                        key=lambda t: t['triplet'])
        require([t['triplet'] for t in trials] == list(range(1, 11)), 'read quantiles need ten rounds')
        records = []
        for t in trials:
            hist = t['result']['latency_read_nanos']
            values = {q: positive(hist.get(q), t['trial_id'] + ' read ' + q) / 1000
                      for q in QUANTILES + ('mean',)}
            require(all(values[a] <= values[b] for a, b in zip(QUANTILES, QUANTILES[1:])),
                    'unordered read quantiles: ' + t['trial_id'])
            records.append(dict(trial_id=t['trial_id'], round=t['triplet'], **values))
        reads[arm] = dict(n_trials=len(records), trials_us=records,
                          mean_above_p95_trials=sum(t['mean'] > t['p95'] for t in records),
                          arithmetic_mean_us={q: st.mean(t[q] for t in records)
                                              for q in QUANTILES + ('mean',)},
                          median_us={q: st.median(t[q] for t in records) for q in QUANTILES})
    for cell in SETTLED:
        commit[cell] = {}
        for arm in ('A', 'AMIN'):
            trials = sorted([t for t in index['settled', cell] if t['arm'] == arm],
                            key=lambda t: t['triplet'])
            require([t['triplet'] for t in trials] == list(range(1, 11)), 'commit CDF needs ten rounds: ' + cell)
            commit[cell][arm] = dict(n_trials=len(trials),
                trial_ids=[t['trial_id'] for t in trials],
                phases={p: pool_histograms([t['result']['stall_diagnostics']['phase_histograms'][p]
                                           for t in trials]) for p in PHASES})
    return dict(read_quantiles=reads, commit_cdfs=commit)


def analyze(datasets):
    index = defaultdict(list)
    for source, trials in sorted(datasets.items()):
        seen = set()
        for t in trials:
            require(t['status'] == 'SUCCESS' and t['result']['status'] == 'SUCCESS',
                    'unsuccessful trial: ' + t['trial_id'])
            require(t['trial_id'] not in seen, 'duplicate trial_id: ' + t['trial_id'])
            seen.add(t['trial_id'])
            for key in ('trial_id', 'cell', 'arm'):
                require(t[key] == t['spec'][key] == t['result'][key], 'envelope mismatch: ' + key)
            require(t['triplet'] == t['spec']['triplet'], 'envelope mismatch: round')
            index[source, t['cell']].append(t)
    cells = {}
    for group, (source, names) in sorted(SELECTION.items()):
        for cell in names:
            cells[cell] = summarize_cell(cell, group, index[source, cell])
    cells = dict(sorted(cells.items()))
    pairs = reader_pairs(cells)
    test = [c for c in cells.values() if c['set'] == 'test']
    verdicts = {m: criteria([c['residuals'][m] for c in test],
                            [p['differences'][m] for p in pairs]) for m in MODELS}
    summaries = {g: {m: residual_summary([c['residuals'][m] for c in cells.values() if c['group'] == g])
                      for m in MODELS} for g in sorted(SELECTION)}
    summaries['k1_only'] = {m: residual_summary([c['residuals'][m] for c in test if c['k'] == 1])
                            for m in MODELS}
    summaries['test_all'] = {m: residual_summary([c['residuals'][m] for c in test]) for m in MODELS}
    rho = float(spearmanr([c['x'] for c in test], [c['S'] for c in test]).statistic)
    require(math.isfinite(rho), 'Spearman undefined: constant test data')
    result = dict(schema='x-readcost-phase2-v1', exploratory=True,
        definitions=dict(h='Arithmetic mean of CoarseLock trial mean read critical section (ns).',
            h_p95='Arithmetic mean of CoarseLock trial p95 read critical section (ns).',
            T_L='Arithmetic mean of per-trial Lease measured_window_ns / in-window commits (ns).',
            S='exp(mean(log(recorded W_CL) - log(recorded W_L))), paired by round and condition.',
            intervention='Use result.throughput.write_ops_per_sec for S and the same condition\'s '
                'measured_window_ns / write_ops_completed_in_window for T_L. The earlier intervention '
                'analyzer used count / 10 s; it is not used here. Recorded W uses its measured window.',
            x='h / T_L; T_L also enters S, so some coupling remains.',
            M1='1 / (1 + k * h / T_L)', M2='1 / (1 + k * h_p95 / T_L)',
            residual='log(S) - log(predicted); each cell has equal weight.',
            rounds='Five paired rounds per intervention cell; ten per other selected cell. '
                'Read quantiles and settled CDFs each use ten rounds per arm.',
            criteria='Inclusive bounds, computed at full precision before serialization.',
            read_quantiles='Per-trial points and median line; means in table. These percentiles cannot identify modes.',
            commit_cdf='Pool the ten rounds by summing counts at each lower_ns. '
                'Bucket-resolution empirical CDF; exact within-bucket latencies are unavailable. '
                'Mass is placed at each recorded lower bound. Histograms cover commits arriving '
                'inside the nominal window, including their post-window drain.',
            zero_log_handling='Keep zero bucket counts in total and cumulative probability. '
                'For display only, place zero at half the smallest positive lower_ns across all '
                'four series of that phase; annotate its mass. Positive buckets are unmodified.',
            precision='JSON floats: twelve fixed decimal places; comparisons use unrounded values.'),
        counts=dict(by_group={g: sum(c['group'] == g for c in cells.values()) for g in sorted(SELECTION)},
                    test=len(test), development=8, legacy=2, k1_only=sum(c['k'] == 1 for c in test),
                    reader_pairs=len(pairs)), cells=cells, reader_pairs=pairs,
        k1_cells=[cell for cell, row in cells.items() if row['set'] == 'test' and row['k'] == 1],
        verdicts=verdicts, residual_summaries=summaries,
        spearman=dict(n_cells=len(test), rho=rho, description_only=True),
        could_not_compute=[], uncertainties=[
            'Exploratory, with shared Lease throughput/cycle coupling; no confirmatory interpretation.',
            'Read percentiles cannot determine bimodality or any other number of modes.',
            'Commit CDF timing is resolved only to histogram lower bounds; operation counts weight pooled rounds.'])
    result.update(latency_data(index))
    return result


def load_datasets(root):
    datasets, sources = {}, {}
    for name in SOURCES:
        paths = sorted((root / name / 'trials').glob('*_a*.json'))
        require(bool(paths), 'no trial files: ' + name)
        digest = hashlib.sha256()
        rows = []
        for path in paths:
            raw = path.read_bytes()
            digest.update(path.name.encode('utf-8') + b'\0' + hashlib.sha256(raw).digest())
            rows.append(json.loads(raw))
        datasets[name] = rows
        sources[name] = dict(n_files=len(paths), sha256=digest.hexdigest(),
                             fingerprint='sha256(sorted filename + NUL + raw-file sha256 bytes)',
                             glob=name + '/trials/*_a*.json')
    return datasets, sources


def fixed_json(value, indent=0):
    """Small explicit serializer: numeric JSON with a fixed float policy."""
    pad, child = ' ' * indent, ' ' * (indent + 2)
    if isinstance(value, dict):
        return '{\n' + ',\n'.join(child + json.dumps(k) + ': ' + fixed_json(v, indent + 2)
                                  for k, v in sorted(value.items())) + '\n' + pad + '}' if value else '{}'
    if isinstance(value, (list, tuple)):
        return '[\n' + ',\n'.join(child + fixed_json(v, indent + 2) for v in value) + '\n' + pad + ']' if value else '[]'
    if isinstance(value, float):
        require(math.isfinite(value), 'nonfinite JSON float')
        return f'{value:.12f}'
    return json.dumps(value, ensure_ascii=True, allow_nan=False)


def write_tables(data, path):
    lines = ['# Phase 2: exploratory existing-record analysis', '',
             'Development and legacy cells count for nothing in the verdict. '
             'All three frozen criteria must pass. No fitted parameters or new CIs.', '']
    lines += [f'- {key}: {value}' for key, value in data['definitions'].items()]
    lines += ['', '## Cell counts', '', '| Group | Cells |', '| --- | ---: |']
    lines += [f'| {g} | {n} |' for g, n in data['counts']['by_group'].items()]
    lines += [f'| {g} | {data["counts"][g]} |' for g in ('test', 'k1_only', 'reader_pairs')]
    lines += ['', '## Frozen criteria and verdict', '',
              '| Model | Median absolute residual | Largest absolute residual | Mean r16 - r4 residual | Collapses | Failed criteria |',
              '| --- | ---: | ---: | ---: | --- | --- |']
    for m, v in data['verdicts'].items():
        cs = v['criteria']
        vals = ' | '.join(f'{cs[str(i)]["value"]:.9f}' if cs[str(i)]['value'] is not None else 'uncomputable'
                          for i in (1, 2, 3))
        lines.append(f'| {m} | {vals} | {str(v["collapses"]).lower()} | {", ".join(v["failed_criteria"]) or "none"} |')
    lines += ['', f'Bounds: {BOUND:.12f}, {2 * BOUND:.12f}, +/-{BOUND:.12f}, inclusive.',
              f'Spearman(x, S), test cells only: rho = {data["spearman"]["rho"]:.9f}, '
              f'n = {data["spearman"]["n_cells"]}; descriptive only.', '']
    if not any(v['collapses'] for v in data['verdicts'].values()):
        lines += ['**No collapse was found for either model; no rule is offered.**', '']
    else:
        lines += ['A passing model is exploratory only; k=1 results are called out below.', '']
    for set_, title in (('test', 'Test cells'), ('development', 'Development cells (not counted)'),
                        ('legacy', 'Legacy scan (not counted)')):
        lines += ['', '## ' + title, '',
                  '| Cell | Group | k | h (us) | h_p95 (us) | T_L (ms) | x | S | M1 | M2 | Residual M1 | Residual M2 |',
                  '| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
        for cell, c in data['cells'].items():
            if c['set'] != set_:
                continue
            vals = [c['h_ns'] / 1000, c['h_p95_ns'] / 1000, c['T_L_ns'] / 1e6, c['x'], c['S'],
                    c['predicted']['M1'], c['predicted']['M2'], c['residuals']['M1'], c['residuals']['M2']]
            lines.append(f'| {cell} | {c["group"]} | {c["k"]} | ' + ' | '.join(f'{v:.6f}' for v in vals) + ' |')
    lines += ['', '## k=1 cells alone', '',
              '| Cell | x | S | M1 | M2 | Residual M1 | Residual M2 |',
              '| --- | ---: | ---: | ---: | ---: | ---: | ---: |']
    for cell in data['k1_cells']:
        c = data['cells'][cell]
        vals = [c['x'], c['S'], c['predicted']['M1'], c['predicted']['M2'],
                c['residuals']['M1'], c['residuals']['M2']]
        lines.append(f'| {cell} | ' + ' | '.join(f'{v:.9f}' for v in vals) + ' |')
    lines += ['', '## Residual summaries (k=1 reported on its own)', '',
              '| Group | Model | Cells | Median absolute | Largest absolute | Mean signed |',
              '| --- | --- | ---: | ---: | ---: | ---: |']
    for g, models in data['residual_summaries'].items():
        for m, s in models.items():
            lines.append(f'| {g} | {m} | {s["n"]} | {s["median_absolute"]:.9f} | '
                         f'{s["largest_absolute"]:.9f} | {s["mean_signed"]:.9f} |')
    lines += ['', '## Criterion 3 pairs', '',
              'Churn r=1 has no role here. Each pair changes only reader count; '
              'the mean difference gives each pair equal weight.', '',
              '| Group | r=4 | r=16 | M1 difference | M2 difference |',
              '| --- | --- | --- | ---: | ---: |']
    for p in data['reader_pairs']:
        lines.append(f'| {p["group"]} | {p["r4"]} | {p["r16"]} | '
                     f'{p["differences"]["M1"]:.9f} | {p["differences"]["M2"]:.9f} |')
    lines += ['', '## Mixed r=4, d=0 read latency (microseconds)', '',
              'Quantile figure: ten per-trial points per quantile, with a median line. '
              'The following are arithmetic means over ten trials, including the trial mean column. '
              'No modes can be inferred.', '',
              '| Arm | min | p50 | p95 | p99 | p999 | max | mean | Trials with mean > p95 |',
              '| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
    for arm, row in data['read_quantiles'].items():
        lines.append(f'| {"CoarseLock" if arm == "A" else "Lease"} | ' +
                     ' | '.join(f'{row["arithmetic_mean_us"][q]:.6f}' for q in QUANTILES + ('mean',)) +
                     f' | {row["mean_above_p95_trials"]}/{row["n_trials"]} |')
    lines += ['', '## Settled commit-phase CDFs', '', data['definitions']['commit_cdf'], '',
              data['definitions']['zero_log_handling'], '',
              '| Cell | Arm | Phase | Rounds | Pooled samples | Zero samples |',
              '| --- | --- | --- | ---: | ---: | ---: |']
    for cell, arms in data['commit_cdfs'].items():
        for arm, row in arms.items():
            for phase, h in row['phases'].items():
                lines.append(f'| {cell} | {arm} | {phase} | {row["n_trials"]} | '
                             f'{h["sample_count"]} | {h["zero_count"]} |')
    lines += ['', '## Limitations and unavailable quantities', '',
              'Could not compute requested quantities: ' + ('; '.join(data['could_not_compute']) or 'none.'), '']
    lines += ['- ' + s for s in data['uncertainties']]
    path.write_text('\n'.join(lines) + '\n', encoding='utf-8', newline='\n')


def fig_penalty(data, directory):
    fig, ax = style.plt.subplots(figsize=(3.5, 3.6))
    markers = dict(development='o', intervention_second='s', intervention_inline='v',
                   compaction_on='^', compaction_off='D', churn='P', tablecount_writer='X')
    labels = dict(development='Development', intervention_second='Intervention second',
                  intervention_inline='Intervention inline', compaction_on='Compaction on',
                  compaction_off='Compaction off', churn='Churn', tablecount_writer='Table count, w=1')
    shown = [c for c in data['cells'].values() if c['set'] != 'legacy']
    xs = np.geomspace(min(c['x'] for c in shown) * .8, max(c['x'] for c in shown) * 1.25, 200)
    for k, line in ((1, '--'), (2, '-')):
        ax.plot(xs, 1 / (1 + k * xs), color='#555555', linestyle=line, linewidth=1,
                label=f'M1, k={k}', zorder=1)
    for g, marker in markers.items():
        rows = [c for c in shown if c['group'] == g]
        color = '#aaaaaa' if g == 'development' else '#222222'
        ax.scatter([c['x'] for c in rows], [c['S'] for c in rows], marker=marker, s=25,
                   facecolors='white' if g == 'intervention_inline' else color,
                   edgecolors=color, linewidths=.7, zorder=2 if g == 'development' else 3,
                   label=labels[g])
    ax.set(xscale='log', xlabel=r'$x = h / T_L$', ylabel=r'$S = W_{CL} / W_L$', ylim=(0, 1.12))
    ax.legend(fontsize=5.6, ncol=2, loc='lower left', bbox_to_anchor=(-.18, 1.01),
              columnspacing=.8, handletextpad=.4)
    fig.text(.5, .005, 'Filled: k=2; hollow: k=1. Grey: development only.', ha='center', fontsize=6)
    style.decorate(ax)
    fig.tight_layout(rect=(0, .035, 1, 1))
    save(fig, directory, 'fig_penalty_collapse')


def fig_quantiles(data, directory):
    fig, ax = style.plt.subplots(figsize=(3.5, 3.0))
    for arm, offset in (('A', -.10), ('AMIN', .10)):
        row = data['read_quantiles'][arm]
        color = style.STYLE[arm]['color']
        for i, t in enumerate(row['trials_us']):
            # Fixed offsets show all ten points without random jitter.
            jitter = (i - 4.5) * .011
            ax.scatter([j + offset + jitter for j in range(len(QUANTILES))],
                       [t[q] for q in QUANTILES], color=color, marker=style.STYLE[arm]['marker'],
                       s=8, alpha=.50, edgecolors='none')
        ax.plot([j + offset for j in range(len(QUANTILES))], [row['median_us'][q] for q in QUANTILES],
                **style.STYLE[arm], markersize=3, linewidth=1)
    ax.set(xticks=range(len(QUANTILES)), xticklabels=QUANTILES, yscale='log',
           ylabel='Read latency (\u00b5s)', xlabel='Recorded quantile', title='Mixed, r=4, d=0')
    ax.legend(fontsize=7, loc='upper left')
    style.decorate(ax)
    fig.text(.5, .005, 'Points: ten trials; lines: median at each quantile.', ha='center', fontsize=6)
    fig.tight_layout(rect=(0, .035, 1, 1))
    save(fig, directory, 'fig_read_quantiles')


def fig_cdfs(data, directory):
    fig, axes = style.plt.subplots(2, 2, figsize=(7.16, 5.0))
    handles = []
    for phase, ax in zip(PHASES, axes.flat):
        all_h = [arms[a]['phases'][phase] for arms in data['commit_cdfs'].values() for a in ('A', 'AMIN')]
        floor = min(b['lower_ns'] for h in all_h for b in h['buckets'] if b['lower_ns'] > 0) / 2
        zero_notes = []
        for cell, arms in data['commit_cdfs'].items():
            dose = '2 ms' if 'd2000' in cell else '0'
            line = '-' if dose == '2 ms' else '--'
            for arm, row in arms.items():
                h = row['phases'][phase]
                kw = dict(color=style.STYLE[arm]['color'], linestyle=line, linewidth=1.2,
                          label=f'{style.STYLE[arm]["label"]}, d={dose}')
                ax.step([max(floor, b['lower_ns']) / 1e6 for b in h['buckets']],
                        [b['cdf'] for b in h['buckets']], where='post', **kw)
                if phase == PHASES[0]:
                    handles.append(Line2D([], [], **kw))
                if h['zero_count']:
                    zero_notes.append(f'{style.STYLE[arm]["label"]}, d={dose}: '
                                      f'{100 * h["zero_count"] / h["sample_count"]:.2f}%')
        if zero_notes:
            ax.text(.03, .05, f'Zero at {floor / 1e6:.2g} ms:\n' + '\n'.join(zero_notes),
                    transform=ax.transAxes, fontsize=5.7, va='bottom')
        ax.set(xscale='log', xlabel='Commit phase latency (ms)', ylabel='Pooled empirical CDF',
               title=phase.replace('_', ' '), ylim=(0, 1.025))
        style.decorate(ax)
    fig.legend(handles=handles, loc='upper center', ncol=4, fontsize=6.5, frameon=False)
    fig.text(.5, .005, 'Ten rounds pooled by summing bucket counts; mass at recorded bucket lower bounds.',
             ha='center', fontsize=7)
    fig.tight_layout(rect=(0, .035, 1, .94))
    save(fig, directory, 'fig_commit_cdf')


def save(fig, directory, name):
    # Layout is already finalized by each renderer. No run-specific metadata.
    fig.savefig(directory / (name + '.pdf'), metadata={'CreationDate': None, 'ModDate': None})
    fig.savefig(directory / (name + '.png'), dpi=300)
    style.plt.close(fig)


def macro_values(data):
    """Format manuscript scalars from Phase 2 JSON, without changing criteria.

    Flush share counts bucket lower bounds strictly below 3 ms. The p90 is the
    first lower bound whose cumulative count reaches 90%, without interpolation.
    Signed negative prose scalars carry their own TeX math delimiters.
    """
    def signed(value, places):
        text = f'{value:.{places}f}'
        return r'\(' + text + r'\)' if value < 0 else text

    one, two = [data['verdicts'][m]['criteria'] for m in MODELS]
    test = [c for c in data['cells'].values() if c['set'] == 'test']
    k1 = [c for c in test if c['k'] == 1]
    bound = one['1']['bound']
    hist = data['commit_cdfs']['S1_d2000_f20_closed']['A']['phases']['flush_check']
    buckets = sorted(hist['buckets'], key=lambda b: b['lower_ns'])
    total = hist['sample_count']
    require(total > 0 and sum(b['count'] for b in buckets) == total, 'invalid flush-check count')
    below = sum(b['count'] for b in buckets if b['lower_ns'] < 3_000_000)
    cumulative = 0
    for bucket in buckets:
        cumulative += bucket['count']
        if cumulative * 10 >= total * 9:
            p90 = bucket['lower_ns'] / 1e6
            break
    return dict(
        PhaseTwoTestCells=str(data['counts']['test']),
        PhaseTwoReaderPairs=str(data['counts']['reader_pairs']),
        PhaseTwoMOneMedian=f'{one["1"]["value"]:.3f}',
        PhaseTwoMOneMax=f'{one["2"]["value"]:.3f}',
        PhaseTwoMOneReader=signed(one['3']['value'], 3),
        PhaseTwoMTwoMedian=f'{two["1"]["value"]:.3f}',
        PhaseTwoMTwoMax=f'{two["2"]["value"]:.3f}',
        PhaseTwoMarginBound=f'{bound:.3f}',
        PhaseTwoMaxBound=f'{one["2"]["bound"]:.3f}',
        PhaseTwoRho=signed(data['spearman']['rho'], 2),
        PhaseTwoWithinMargin=str(sum(abs(c['residuals']['M1']) <= bound for c in test)),
        PhaseTwoKOneWithin=str(sum(abs(c['residuals']['M1']) <= bound for c in k1)),
        PhaseTwoKOneCells=str(len(k1)),
        PhaseTwoFlushLowShare=f'{100 * below / total:.0f}',
        PhaseTwoFlushHighNote=f'{p90:.1f}')


def write_macros(results_path, path):
    """Load the written JSON so manuscript numbers have that exact provenance."""
    values = macro_values(json.loads(results_path.read_text(encoding='utf-8')))
    lines = ['% Generated by x_phase2_analyze.py; do not edit.']
    lines += ['\\newcommand{\\' + name + '}{' + value + '}' for name, value in sorted(values.items())]
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text('\n'.join(lines) + '\n', encoding='utf-8', newline='\n')
    return values


def run(root, output_dir, paper_figures, macros_out):
    datasets, sources = load_datasets(root)
    data = analyze(datasets)
    data['sources'] = sources
    output_dir.mkdir(parents=True, exist_ok=True)
    results_path = output_dir / 'phase2_results.json'
    results_path.write_text(fixed_json(data) + '\n', encoding='utf-8', newline='\n')
    write_macros(results_path, macros_out)
    write_tables(data, output_dir / 'phase2_tables.md')
    style.setup()
    fig_penalty(data, output_dir)
    fig_quantiles(data, output_dir)
    fig_cdfs(data, output_dir)
    paper_figures.mkdir(parents=True, exist_ok=True)
    for name in FIGURES:
        shutil.copyfile(output_dir / (name + '.pdf'), paper_figures / (name + '.pdf'))
    return data


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', type=Path, default=DEFAULT_X)
    p.add_argument('--output-dir', type=Path)
    p.add_argument('--paper-figures', type=Path, default=ROOT / 'paper/figures/x_readcost')
    p.add_argument('--macros-out', type=Path, default=ROOT / 'paper/generated/phase2.tex')
    args = p.parse_args()
    data = run(args.root, args.output_dir or args.root / 'analysis_phase2', args.paper_figures, args.macros_out)
    for m, verdict in data['verdicts'].items():
        print(f'{m}: collapses={verdict["collapses"]}; criteria=' +
              ', '.join(f'{i}: {v["value"]:.9f} ({v["passes"]})' for i, v in verdict['criteria'].items()))
    print('Cell counts: ' + str(data['counts']))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
