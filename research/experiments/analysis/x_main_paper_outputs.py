#!/usr/bin/env python3
"""Deterministic outputs for the restructured main paper; never launches trials.

Reads retained results only and writes paper/generated/mainpaper.tex plus
paper/figures/x_readcost/fig_penalty_no_legacy.pdf. Values are copied from stored
results, formatted as in x_readcost_paper_tables.py, or calculated from retained
trials (documented at each use). The memtable fit is post hoc and untested.
Claims the manuscript states in prose are asserted, so a change in the retained
data stops the generator instead of leaving stale text.
"""
import argparse
import json
import math
from pathlib import Path
import statistics as st

import x_readcost_figures as figures
import x_readcost_paper_tables as tables
import x_followup_paper_outputs as follow_tables

ROOT = Path(__file__).resolve().parents[3]
X = ROOT / 'research/experiments/phase_i7/X_READCOST'
F1_RATES = (183, 366, 549, 732)
ARMS = ('AMIN', 'B')
NAMES = {'AMIN': 'Lease', 'B': 'Snapshot'}
DRAFT_HS_MS = 25  # H-S threshold in the draft protocol, before calibration was seen
GAIN_DOSES = ('d0000', 'd0020', 'd0200')


def require(ok, message):
    if not ok:
        raise ValueError(message)


def x1_cell(r, d):
    return f'X1_r{r:02d}_{d}'


def includes_one(ci):
    return ci[0] <= 1 <= ci[1]


def read_gain_cells(x1):
    """Mixed non-legacy cells where CoarseLock read more than Lease by default."""
    cells = [x1_cell(r, d) for r in (4, 16) for d in ('d0000', 'd0020', 'd0200', 'd2000')]
    gain = [c for c in cells if x1['cells'][c]['ratios']['R']['A/AMIN']['geometric_mean_ratio'] > 1]
    require(gain == [x1_cell(r, d) for r in (4, 16) for d in GAIN_DOSES],
            'the six read-gain cells are no longer r=4,16 at 0, 20 us and 200 us')
    return gain


def gain_rows(x1, mech, cells):
    rows, identity = [], []
    for cell in cells:
        if cell == x1_cell(16, 'd0000'):
            rows.append(r'\midrule')
        q = {a: mech['cells'][cell]['arms'][a]['reads_per_reader_per_commit']['mean'] for a in ('A', 'AMIN')}
        s = x1['cells'][cell]['ratios']['W']['A/AMIN']
        rr = x1['cells'][cell]['ratios']['R']['A/AMIN']
        # q_CL/q_L is the quotient of the two stored arithmetic cell means; the product
        # with S is compared to the stored read ratio and the largest gap is reported.
        quotient = q['A'] / q['AMIN']
        identity.append(abs(quotient * s['geometric_mean_ratio'] - rr['geometric_mean_ratio']))
        rows.append(' & '.join([str(tables.reader(cell)), tables.dose(cell), tables.num(q['A']),
                                tables.num(q['AMIN']), tables.num(quotient), tables.ratio(s),
                                tables.ratio(rr)]) + r' \\')
    return rows, max(identity)


def two_of_six(x1, cells):
    """Cells where the write-loss CI includes one but the read-gain CI excludes it."""
    found = [c for c in cells
             if includes_one(x1['cells'][c]['ratios']['W']['A/AMIN']['t_ci'])
             and not includes_one(x1['cells'][c]['ratios']['R']['A/AMIN']['t_ci'])]
    require(found == [x1_cell(4, 'd0000'), x1_cell(16, 'd0020')],
            'two-of-six cells changed: ' + ', '.join(found))
    return found


def intervention_values(inter):
    """One-acquisition read ratios at low doses, and write effects of removing it."""
    cells = inter['cells']
    low = [f'I1_r{r:02d}_d{d:04d}' for r in (4, 16) for d in (0, 20)]
    reads = [cells[c]['conditions']['inline']['ratios_A_over_AMIN']['R'] for c in low]
    require(all(includes_one(x['t_ci']) for x in reads),
            'a one-acquisition low-dose read ratio CI no longer includes one')
    order = sorted(cells)
    effect = {a: {m: [cells[c]['ratios_inline_over_second'][a][m] for c in order] for m in ('W', 'R')}
              for a in ('A', 'AMIN')}
    above = [c for c, x in zip(order, effect['A']['W']) if x['t_ci'][0] > 1]
    inside = [c for c, x in zip(order, effect['A']['W']) if includes_one(x['t_ci'])]
    require(above == ['I1_r04_d0200', 'I1_r04_d2000', 'I1_r16_d0000', 'I1_r16_d0200', 'I1_r16_d2000']
            and inside == ['I1_r04_d0000', 'I1_r04_d0020', 'I1_r16_d0020'],
            'CoarseLock write-effect classes changed')
    require(all(includes_one(x['t_ci']) for x in effect['AMIN']['W']), 'a Lease write-effect CI excludes one')
    return low, reads, effect


def span(values, places=3):
    return f'{min(values):.{places}f}--{max(values):.{places}f}'


def hold_per_commit(follow):
    """Hold per commit (ms) = mean hold share / mean achieved writes per second."""
    out = {}
    for rate in F1_RATES:
        for arm in ARMS:
            c = follow['cells'][f'F1_w{rate:04d}_m16']['arms'][arm]
            out[rate, arm] = 1000 * c['hold_share']['mean'] / c['W']['mean']
    return out


def fsync_shares(directory):
    """Share of the exclusive hold spent in WAL sync, from F1 commit-phase traces.

    Per trial: mean wal_sync / (mean validate + wal_write + wal_sync + apply),
    the same four phases that define the follow-up hold share. Cell/design mean.
    """
    groups = {}
    for path in sorted((directory / 'trials').glob('F1_*_a*.json')):
        trial = json.loads(path.read_text(encoding='utf-8'))
        phases = trial['result']['stall_diagnostics']['phase_histograms']
        count = trial['result']['stall_diagnostics']['commit_count']
        require(all(phases[p]['stats']['sample_count'] == count
                    for p in ('validate', 'wal_write', 'wal_sync', 'apply')), 'phase count mismatch')
        means = {p: phases[p]['stats']['mean'] for p in ('validate', 'wal_write', 'wal_sync', 'apply')}
        groups.setdefault((trial['cell'], trial['arm']), []).append(means['wal_sync'] / sum(means.values()))
    out = {}
    for rate in F1_RATES:
        for arm in ARMS:
            values = groups.get((f'F1_w{rate:04d}_m16', arm), [])
            require(len(values) >= 2, 'missing F1 trials for fsync share')
            out[rate, arm] = 100 * st.mean(values)
    return out


def memtable_values(follow, directory):
    """Arithmetic cell/design means of per-trial memory share and R/free.

    Use the same memory-share denominator as x_memtable_analyze.trial_outcomes.
    The manager counter includes warmup; completed reads cover window-issued
    operations including drain. Their intervals differ, so share is approximate.
    Raw/results identity and R/free inputs are checked by the follow-up loader.
    F2 is calculated too, but the manuscript table contains only F1 and F3.
    """
    checked = {v['trial_id']: v for v in follow_tables.load_raw(directory, follow)}
    groups = {}
    for path in sorted((directory / 'trials').glob('*_a*.json')):
        trial = json.loads(path.read_text(encoding='utf-8'))
        row = checked[trial['trial_id']]
        if row['cell'] not in follow_tables.F1 + (follow_tables.F2, follow_tables.F3):
            continue
        result = trial['result']
        expected = follow['trials'][trial['trial_id']]
        require(all(expected[k] == row[k] for k in ('cell', 'arm', 'round')),
                'raw/results metadata mismatch: ' + trial['trial_id'])
        reads = result['operations']['read_ops_completed']
        memory = result['lsm_diagnostics']['measured_memtable_point_reads']
        require(reads > 0 and memory >= 0, 'invalid memtable share: ' + trial['trial_id'])
        share = memory / reads
        capacity = expected['R'] / expected['free_share']
        require(math.isfinite(share) and math.isfinite(capacity), 'nonfinite memtable metric')
        groups.setdefault((row['cell'], row['arm']), []).append((share, capacity))
    means = {}
    for cell in follow_tables.F1 + (follow_tables.F2, follow_tables.F3):
        for arm in ARMS:
            values = groups.get((cell, arm), [])
            require(len(values) >= 2, 'missing memtable trials: ' + cell + ' ' + arm)
            means[cell, arm] = tuple(st.mean(v[i] for v in values) for i in (0, 1))
    return means


def memtable_outputs(means):
    """Two-parameter OLS of 1/(mean trial R/free) on mean trial share.

    Fit the four F1 cell means separately per design. F3 is outside the fit;
    report its mean R/free divided by the reciprocal line prediction at its
    own mean share. Rounded prose endpoints average the two design means,
    then round share to .01 and R/free to 10,000 reads/s.
    """
    rows, out = [], {}
    for cell in follow_tables.F1 + (follow_tables.F3,):
        label = str(follow_tables.RATES[follow_tables.F1.index(cell)]) if cell in follow_tables.F1 else f'{F1_RATES[-1]} (fsync off)'
        rows.append(' & '.join([label] + [f'{means[cell, a][0]:.3f}' for a in ARMS] +
                               [f'{means[cell, a][1]:,.0f}'.replace(',', '{,}') for a in ARMS]) + r' \\')
    for key, cell in (('Low', follow_tables.F1[0]), ('High', follow_tables.F1[-1])):
        out['MemShareAbout' + key] = f'{st.mean(means[cell, a][0] for a in ARMS):.2f}'
        rounded = round(st.mean(means[cell, a][1] for a in ARMS), -4)
        out['MemCapacityAbout' + key] = f'{rounded:,.0f}'.replace(',', '{,}')
    for arm, suffix in zip(ARMS, ('L', 'S')):
        xs = [means[cell, arm][0] for cell in follow_tables.F1]
        ys = [1 / means[cell, arm][1] for cell in follow_tables.F1]
        fit = st.linear_regression(xs, ys)
        share, capacity = means[follow_tables.F3, arm]
        predicted_cost = fit.intercept + fit.slope * share
        require(predicted_cost > 0, 'nonpositive fsync-off line prediction')
        out['MemFitPearson' + suffix] = f'{st.correlation(xs, ys):.3f}'
        out['MemFitOffRatio' + suffix] = f'{capacity * predicted_cost:.3f}'
        out['MemFitParameterCount'] = str(len(fit))
        out['MemFitCellCount'] = str(len(xs))
    return rows, out


CAMPAIGN_DIRS = ('campaign', 'intervention', 'compaction', 'tablecount', 'paced', 'settled', 'followup')


def go_version(x_root):
    """Toolchain recorded by each campaign's platform self-test; all must agree."""
    found = {}
    for name in CAMPAIGN_DIRS:
        platform = json.loads((Path(x_root) / name / 'platform.json').read_text(encoding='utf-8'))
        found[name] = platform['selftest']['runtime']['go_version']
    require(len(set(found.values())) == 1, 'campaigns used different Go toolchains: ' + json.dumps(found))
    version = next(iter(found.values()))
    require(version.startswith('go'), 'unexpected Go version string')
    return version[2:]


def settled_hs(settled):
    medians = {k: v['median_largest_fsync_ns'] / 1e6 for k, v in settled['H_S']['trials'].items()}
    thresholds = {v['threshold_ns'] for v in settled['H_S']['trials'].values()}
    require(len(thresholds) == 1, 'H-S thresholds differ')
    return medians, thresholds.pop() / 1e6


def macro_text(x1, mech, inter, follow, follow_dir, settled, x_root):
    cells = read_gain_cells(x1)
    rows, identity = gain_rows(x1, mech, cells)
    pair = two_of_six(x1, cells)
    out = {}
    out['ReadGainIdentityMaxGap'] = f'{math.ceil(identity * 1e4) / 1e4:.4f}'  # upper bound, 4 places
    for cell, key in zip(pair, ('FourZero', 'SixteenTwenty')):
        out['GainCell' + key + 'S'] = tables.ratio(x1['cells'][cell]['ratios']['W']['A/AMIN'])
        out['GainCell' + key + 'R'] = tables.ratio(x1['cells'][cell]['ratios']['R']['A/AMIN'])
    low, reads, effect = intervention_values(inter)
    low_rows = [' & '.join([str(tables.reader(c)), tables.dose(c), tables.ratio(x)]) + r' \\'
                for c, x in zip(low, reads)]
    for key, x in zip(('FourZero', 'FourTwenty', 'SixteenZero', 'SixteenTwenty'), reads):
        out['LowDoseRead' + key] = tables.ratio(x)
    pts = [x['geometric_mean_ratio'] for x in reads]
    out['LowDoseReadPoints'] = ', '.join(f'{p:.3f}' for p in pts)
    out['LowDoseReadRange'] = span(pts)
    out['IntLeaseWriteRange'] = span([x['geometric_mean_ratio'] for x in effect['AMIN']['W']])
    out['IntLeaseReadRange'] = span([x['geometric_mean_ratio'] for x in effect['AMIN']['R']])
    out['IntCLReadRange'] = span([x['geometric_mean_ratio'] for x in effect['A']['R']])
    hold = hold_per_commit(follow)
    share = fsync_shares(follow_dir)
    out['HoldMsLeaseLow'] = f"{hold[183, 'AMIN']:.2f}"
    out['HoldMsLeaseHigh'] = f"{hold[732, 'AMIN']:.2f}"
    out['HoldMsSnapshotLow'] = f"{hold[183, 'B']:.2f}"
    out['HoldMsSnapshotHigh'] = f"{hold[732, 'B']:.2f}"
    out['HoldMsRange'] = span(list(hold.values()), 2)
    out['FsyncShareRange'] = span(list(share.values()), 1)
    out['FsyncShareLow'] = f'{min(share.values()):.1f}'
    out['FsyncShareHigh'] = f'{max(share.values()):.1f}'
    hold_rows = [' & '.join([str(rate)] + [f'{hold[rate, a]:.2f}' for a in ARMS] +
                            [f'{share[rate, a]:.1f}' for a in ARMS]) + r' \\' for rate in F1_RATES]
    mem_rows, mem_values = memtable_outputs(memtable_values(follow, follow_dir))
    out.update(mem_values)
    medians, threshold = settled_hs(settled)
    values = sorted(medians.values())
    above = [v for v in values if v > DRAFT_HS_MS]
    out['GoVersion'] = go_version(x_root)
    out['HSDesignCount'] = str(len(values))
    out['HSMedianRange'] = span(values, 1)
    out['HSThresholdMs'] = f'{threshold:g}'
    out['HSDraftMs'] = str(DRAFT_HS_MS)
    out['HSAboveDraftCount'] = str(len(above))
    out['HSAboveDraftRange'] = span(above, 1) if above else 'none'
    require(all(v < threshold for v in values), 'an H-S design median reaches the final threshold')
    lines = ['% Generated by x_main_paper_outputs.py; do not edit.',
             '% Hold per commit = mean hold share / mean achieved writes per second.',
             '% fsync share = mean WAL sync / (validate + WAL write + WAL sync + apply) per trial.',
             '% Memory share and R/free are arithmetic means of per-trial ratios (F1, F2, F3).',
             '% Memory share is approximate: manager counters include warmup; read operations do not.',
             '% Rounded prose endpoints average both design means, then round to .01 / 10,000.',
             '% Post hoc, untested fit: 1/(mean R/free) = intercept + slope * mean memory share;',
             '% four F1 means per design, two parameters; F3 capacity / reciprocal line prediction.']
    for name, body in (('TblReadGainRows', rows), ('TblLowDoseReadRows', low_rows),
                       ('TblHoldPerCommitRows', hold_rows), ('TblMemShareRows', mem_rows)):
        lines.append('\\gdef\\' + name + '{%\n' + '\n'.join(body) + '\n}%')
    lines += ['\\newcommand{\\' + k + '}{' + v + '}' for k, v in sorted(out.items())]
    return '\n'.join(lines) + '\n', out


def run(x1_path, mech_path, inter_path, follow_path, follow_dir, settled_path, macros_out, figures_dir,
        x_root=X):
    load = lambda p: json.loads(Path(p).read_text(encoding='utf-8'))
    x1, mech, inter, follow, settled = map(load, (x1_path, mech_path, inter_path, follow_path, settled_path))
    text, values = macro_text(x1, mech, inter, follow, Path(follow_dir), settled, x_root)
    macros_out.parent.mkdir(parents=True, exist_ok=True)
    figures_dir.mkdir(parents=True, exist_ok=True)
    figures.setup()
    figures.fig_penalty_vs_hold(x1, figures_dir, legacy=False, name='fig_penalty_no_legacy', formats=('pdf',))
    macros_out.write_text(text, encoding='utf-8', newline='\n')
    return values


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results', type=Path, default=X / 'analysis/x_readcost_results.json')
    parser.add_argument('--mechanism', type=Path, default=X / 'analysis/mechanism.json')
    parser.add_argument('--intervention', type=Path, default=X / 'analysis_intervention/x_intervention_results.json')
    parser.add_argument('--followup', type=Path, default=X / 'analysis_followup/x_followup_results.json')
    parser.add_argument('--followup-dir', type=Path, default=X / 'followup')
    parser.add_argument('--x-root', type=Path, default=X, help='folder holding each campaign platform.json')
    parser.add_argument('--settled', type=Path, default=X / 'analysis_settled/x_settled_results.json')
    parser.add_argument('--macros-out', type=Path, default=ROOT / 'paper/generated/mainpaper.tex')
    parser.add_argument('--figures-dir', type=Path, default=ROOT / 'paper/figures/x_readcost')
    a = parser.parse_args()
    run(a.results, a.mechanism, a.intervention, a.followup, a.followup_dir, a.settled,
        a.macros_out, a.figures_dir, a.x_root)


if __name__ == '__main__':
    main()
