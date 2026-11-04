#!/usr/bin/env python3
"""Render paper figures from frozen campaign results JSON (no trial access)."""
import argparse
import json
import math
from pathlib import Path
import sys

import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt
from matplotlib.lines import Line2D
from matplotlib.ticker import FixedLocator, FormatStrFormatter, FuncFormatter, NullFormatter
import numpy as np

STYLE = {'A': dict(color='#2a78d6', marker='o', linestyle='-', label='CoarseLock'),
         'AMIN': dict(color='#eb6834', marker='s', linestyle='--', label='Lease'),
         'B': dict(color='#1baf7a', marker='^', linestyle=':', label='Snapshot')}
DODGE = {'A': -.08, 'AMIN': 0., 'B': .08}
LEVELS = ['0', '20 µs', '200 µs', '2 ms', 'legacy scan']
X = [0, 1, 2, 3, 4.6]
WARNED = set()


def warn(message):
    if message not in WARNED:
        print('WARNING: ' + message, file=sys.stderr)
        WARNED.add(message)


def setup():
    plt.rcParams.update({'font.size': 8, 'axes.labelsize': 8, 'legend.fontsize': 7,
                         'text.color': '#222222', 'axes.labelcolor': '#222222',
                         'xtick.color': '#222222', 'ytick.color': '#222222',
                         'lines.linewidth': 1.7, 'lines.markersize': 5,
                         'pdf.fonttype': 42, 'ps.fonttype': 42})


def decorate(ax):
    ax.grid(True, color='#dddddd', linewidth=.5, alpha=.6)
    ax.set_axisbelow(True)
    ax.spines[['top', 'right']].set_visible(False)


def save(fig, directory, name, formats=('pdf', 'png')):
    fig.tight_layout()
    if 'pdf' in formats:
        fig.savefig(directory / (name + '.pdf'), metadata={'CreationDate': None, 'ModDate': None})
    if 'png' in formats:
        fig.savefig(directory / (name + '.png'), dpi=300)
    plt.close(fig)


def point(ax, x, value, ci, style, horizontal=False, allow_zero=False):
    if value is None or not math.isfinite(value) or value < 0 or (value == 0 and not allow_zero):
        warn('omitting missing/nonpositive point')
        return False
    errors = None
    if ci is not None:
        errors = np.array([[max(0, value - ci[0])], [max(0, ci[1] - value)]])
    else:
        warn('null CI: showing point without interval')
    kw = {k: v for k, v in style.items() if k != 'label'}
    kw['linestyle'] = 'none'
    if horizontal:
        ax.errorbar(value, x, xerr=errors, capsize=2, **kw)
    else:
        ax.errorbar(x, value, yerr=errors, capsize=2, **kw)
    return True


def arm_summary(data, cell, arm, outcome):
    value = data.get('cells', {}).get(cell, {}).get('arms', {}).get(arm, {}).get(outcome)
    if value is None:
        warn(f'missing cell/outcome: {cell} {arm} {outcome}')
    return value or {}


def categorical_series(ax, xs, values, cis, style, connected, offset=0., scale=1., allow_zero=False):
    """Dodge both markers and bars; never connect the isolated legacy category."""
    positions = [x + offset for x in xs]
    shown = []
    for x, v, ci in zip(positions, values, cis):
        v = v * scale if v is not None else None
        ci = [bound * scale for bound in ci] if ci is not None else None
        plotted = point(ax, x, v, ci, style, allow_zero=allow_zero)
        shown.append(v if plotted else np.nan)
    line_style = {k: v for k, v in style.items() if k not in ('marker', 'label')}
    ax.plot(positions[:connected], shown[:connected], **line_style)
    # Only the endpoint of an actual line segment is eligible for a direct label.
    for i in range(connected - 1, 0, -1):
        if np.isfinite(shown[i]) and np.isfinite(shown[i - 1]):
            return positions[i], shown[i], style['label']
    return None


def end_labels(ax, entries):
    """Label connected endpoints only, or omit the entire panel's direct labels.

    Separation is computed in log space on log axes, with a fixed minimum gap.
    A bounded nudge and short neutral connector prevent labels floating away.
    """
    entries = [e for e in entries if e is not None]
    if not entries:
        return []
    log = ax.get_yscale() == 'log'
    forward = math.log10 if log else float
    inverse = (lambda v: 10 ** v) if log else float
    low, high = [forward(y) for y in ax.get_ylim()]
    span = high - low
    gap = max(.04 if log else 0., .09 * span)
    ordered = sorted(entries, key=lambda e: e[1])
    original = np.array([forward(y) for _, y, _ in ordered])
    placed = original.copy()
    for i in range(1, len(placed)):
        placed[i] = max(placed[i], placed[i - 1] + gap)
    placed -= float(np.mean(placed - original))
    # Three indistinguishable arms generally cannot be labelled near their
    # markers without collisions: their legend remains the reliable key.
    if (np.any(np.abs(placed - original) > .65 * gap) or
            np.any(placed < low + .03 * span) or np.any(placed > high - .03 * span)):
        return []
    labels = []
    for (x, y, label), position in zip(ordered, placed):
        labels.append(ax.annotate(label, (x, y), xytext=(x + .16, inverse(position)),
                                  textcoords='data', color='#222222', fontsize=6,
                                  va='center', arrowprops=dict(arrowstyle='-', color='#999999', lw=.5)))
    return labels


def arm_legend(ax):
    ax.legend(handles=[Line2D([], [], **s) for s in STYLE.values()], loc='best')


def plain_number(value, _):
    return np.format_float_positional(value, trim='-')


def dose_plot(data, directory, outcome, name):
    fig, axes = plt.subplots(1, 2, figsize=(7.16, 2.9))
    for r, ax in zip((4, 16), axes):
        cells = [f'X1_r{r:02d}_{d}' for d in ('d0000', 'd0020', 'd0200', 'd2000', 'legacy')]
        endpoints = []
        for arm, style in STYLE.items():
            summaries = [arm_summary(data, c, arm, outcome) for c in cells]
            endpoints.append(categorical_series(ax, X,
                [s.get('geometric_mean') for s in summaries], [s.get('t_ci') for s in summaries],
                style, connected=4, offset=DODGE[arm]))
        ax.axvline(3.8, color='#aaaaaa', linewidth=.7, linestyle='--')
        ax.set(yscale='log', xticks=X, xticklabels=LEVELS,
               xlabel=f'Injected read delay (r = {r})', ylabel='Writes/s' if outcome == 'W' else 'Reads/s')
        ax.set_xlim(-.3, 5.1)
        ax.margins(y=.15)
        end_labels(ax, endpoints)
        arm_legend(ax)
        decorate(ax)
    save(fig, directory, name)


def ratio_plot(data, directory):
    fig, ax = plt.subplots(figsize=(3.5, 2.9))
    endpoints, handles = [], []
    # Reader-count series use neutral colours, leaving arm colours reserved.
    for r, color, marker, line, offset in ((4, '#333333', 'o', '-', -.04), (16, '#777777', 'D', '--', .04)):
        style = dict(color=color, marker=marker, linestyle=line, label=f'r = {r}')
        values = [data.get('cells', {}).get(f'X1_r{r:02d}_{d}', {}).get('ratios', {}).get('W', {}).get('A/AMIN', {})
                  for d in ('d0000', 'd0020', 'd0200', 'd2000', 'legacy')]
        endpoints.append(categorical_series(ax, X, [s.get('geometric_mean_ratio') for s in values],
                                            [s.get('t_ci') for s in values], style, 4, offset))
        handles.append(Line2D([], [], **style))
    ax.axhline(1, color='#555555', linewidth=1)
    ax.annotate('no penalty', xy=(.98, 1), xycoords=ax.get_yaxis_transform(),
                xytext=(0, 3), textcoords='offset points', ha='right', va='bottom', fontsize=7)
    ax.axvline(3.8, color='#aaaaaa', linewidth=.7, linestyle='--')
    ax.set(yscale='log', xticks=X, xticklabels=LEVELS, xlabel='Injected read delay', ylabel='Write ratio CoarseLock / Lease')
    ax.yaxis.set_major_formatter(FuncFormatter(plain_number))
    ax.yaxis.set_minor_formatter(NullFormatter())
    ax.set_xlim(-.3, 5.1)
    ax.margins(y=.15)
    end_labels(ax, endpoints)
    ax.legend(handles=handles)
    decorate(ax)
    save(fig, directory, 'fig_x1_ratio')


def publication_plot(data, directory):
    fig, axes = plt.subplots(1, 2, figsize=(7.16, 2.9))
    xs = [0, 1, 2, 3.6]
    endpoints = [[], []]
    for panel, ax in enumerate(axes):
        for arm, style in STYLE.items():
            metric = 'publications' if panel == 0 else 'publication_lock_wait_p99'
            summaries = [arm_summary(data, 'X2_r16_' + d, arm, metric)
                         for d in ('d0000', 'd0200', 'd2000', 'legacy')]
            values = [s.get('mean' if panel == 0 else 'geometric_mean') for s in summaries]
            cis = [[s['min'], s['max']] if panel == 0 and s.get('min') is not None else s.get('t_ci')
                   for s in summaries]
            endpoints[panel].append(categorical_series(ax, xs, values, cis, style, connected=3,
                offset=DODGE[arm], scale=1. if panel == 0 else 1e-6, allow_zero=(panel == 0)))
        ax.set(xticks=xs, xticklabels=['0', '200 µs', '2 ms', 'legacy scan'], xlabel='Injected read delay (r = 16)')
        ax.set_xlim(-.3, 4.1)
        ax.axvline(2.8, color='#aaaaaa', linewidth=.7, linestyle='--')
        arm_legend(ax)
        decorate(ax)
    axes[0].set(ylabel='In-window publications (out of 20)', ylim=(-.5, 21.5), yticks=[0, 5, 10, 15, 20])
    axes[1].set(yscale='log', ylabel='Publication lock-wait p99 (ms)')
    axes[1].yaxis.set_major_formatter(FuncFormatter(plain_number))
    axes[1].yaxis.set_minor_formatter(NullFormatter())
    axes[1].margins(y=.15)
    for ax, labels in zip(axes, endpoints):
        end_labels(ax, labels)
    save(fig, directory, 'fig_x2_publication')


def cell_label(cell):
    parts = cell.split('_')
    reader = int(parts[1][1:])
    level = {'d0000': 'no delay', 'd0020': '20 µs', 'd0200': '200 µs',
             'd2000': '2 ms', 'legacy': 'legacy scan'}.get(parts[2] if len(parts) > 2 else '', 'no delay')
    return f'r={reader}, {level}'


def cell_order(item):
    cell = item[0]
    parts = cell.split('_')
    dose = parts[2] if len(parts) > 2 else 'd0000'
    return int(parts[1][1:]), 10000 if dose == 'legacy' else int(dose[1:])


def forest_rows(data, outcome):
    family = data.get('H2', {}).get(outcome, {})
    groups = [('Mixed (90% CI)', [(c, v) for c, v in family.items() if c.startswith('X1_')]),
              ('Publication (90% CI)', [(c, v) for c, v in family.items() if c.startswith('X2_')]),
              ('Churn (95% CI)', [(c, ms[outcome]) for c, ms in data.get('H3', {}).items() if outcome in ms])]
    rows = []
    for heading, values in groups:
        if values:
            rows.append((heading, None))
            rows.extend((cell_label(c), v) for c, v in sorted(values, key=cell_order))
    return rows


def forest(data, directory):
    panels = [forest_rows(data, m) for m in ('W', 'R')]
    fig, axes = plt.subplots(1, 2, figsize=(7.16, max(2., .19 * max(map(len, panels)) + .65)))
    for ax, outcome, rows in zip(axes, ('W', 'R'), panels):
        ax.axvspan(.90, 1.11, color='#dddddd', alpha=.6)
        ax.axvline(1, color='#777777', linewidth=.8)
        bounds = [.90, 1., 1.11]
        for i, (_, v) in enumerate(rows):
            if v is None:
                if i:
                    ax.axhline(i - .5, color='#aaaaaa', linewidth=.7)
                continue
            value, ci = v.get('geometric_mean_ratio'), v.get('t_ci')
            if point(ax, i, value, ci, STYLE['AMIN'], horizontal=True):
                bounds.append(value)
                if ci:
                    bounds.extend(x for x in ci if x > 0 and math.isfinite(x))
        low, high = min(bounds), max(bounds)
        padding = .08 * math.log(high / low)
        ax.set(xscale='log', xlim=(low * math.exp(-padding), high * math.exp(padding)),
               yticks=range(len(rows)), yticklabels=[label for label, _ in rows],
               ylim=(max(len(rows) - .5, .5), -.7),
               xlabel=f'{"Write" if outcome == "W" else "Read"} throughput: Lease / Snapshot')
        ax.xaxis.set_major_locator(FixedLocator([.90, 1., 1.11]))
        ax.xaxis.set_major_formatter(FormatStrFormatter('%.2f'))
        ax.xaxis.set_minor_formatter(NullFormatter())
        for tick, (_, value) in zip(ax.get_yticklabels(), rows):
            tick.set_fontsize(6.5 if value is not None else 7)
            tick.set_fontweight('normal' if value is not None else 'bold')
        decorate(ax)
    save(fig, directory, 'fig_amin_vs_b')


def fig_penalty_vs_hold(data, directory, legacy=True, name='fig_penalty_vs_hold', formats=('pdf', 'png')):
    """Penalty against measured hold. legacy=False drops the legacy scan points
    and refits the axes to the four injected doses; the data, styling and CIs
    are otherwise identical. Defaults reproduce the original figure exactly."""
    fig, axes = plt.subplots(1, 2, figsize=(7.16, 3.05), sharex=True)
    shown_bounds = {'W': [], 'R': [], 'hold': []}
    for r, color, marker, line in ((4, '#235e96', 'o', '-'), (16, '#6595bf', 'D', '--')):
        cells = [data.get('cells', {}).get(f'X1_r{r:02d}_{d}', {}) for d in ('d0000', 'd0020', 'd0200', 'd2000', 'legacy')]
        holds = [c.get('arms', {}).get('A', {}).get('read_cs_mean', {}).get('mean', math.nan) / 1e3 for c in cells]
        style = dict(color=color, marker=marker, linestyle=line, label=f'r = {r}')
        for ax, metric in zip(axes, ('W', 'R')):
            vals = [c.get('ratios', {}).get(metric, {}).get('A/AMIN', {}) for c in cells]
            categorical_series(ax, holds[:4], [v.get('geometric_mean_ratio') for v in vals[:4]],
                               [v.get('t_ci') for v in vals[:4]], style, 4)
            for v in vals[:4]:
                shown_bounds[metric].extend([v.get('geometric_mean_ratio'), *(v.get('t_ci') or [])])
            shown_bounds['hold'].extend(h for h in holds[:4] if math.isfinite(h))
            if legacy and math.isfinite(holds[4]) and point(ax, holds[4], vals[4].get('geometric_mean_ratio'), vals[4].get('t_ci'),
                                              dict(style, markerfacecolor='none')):
                ax.annotate('legacy scan', (holds[4], vals[4]['geometric_mean_ratio']),
                            xytext=(-8, 12), textcoords='offset points', ha='right', fontsize=6)
    specs = [('Write ratio CoarseLock / Lease', (.001, 1.5), [.001, .01, .1, 1]),
             ('Read ratio CoarseLock / Lease', (.4, 1.8), [.4, .5, 1, 1.5])]
    xticks, xlimits = [1, 10, 100, 1000, 10000, 100000, 1000000], (8, 1000000)
    if not legacy:
        # Refit to the injected doses, in log space with the original 8% margin style.
        def fit(values, candidates):
            values = [v for v in values if v is not None and math.isfinite(v) and v > 0]
            low, high = min(values + [1.]), max(values + [1.])
            pad = .08 * math.log(high / low)
            limits = (low * math.exp(-pad), high * math.exp(pad))
            return limits, [t for t in candidates if limits[0] <= t <= limits[1]]
        candidates = [.2, .25, .3, .4, .5, .6, .7, .8, 1, 1.25, 1.5, 2]
        specs = [(specs[i][0],) + fit(shown_bounds[m], candidates) for i, m in enumerate(('W', 'R'))]
        holds_shown = shown_bounds['hold']
        xlimits = (min(holds_shown) / 1.6, max(holds_shown) * 1.6)
        xticks = [t for t in (10, 100, 1000, 10000) if xlimits[0] <= t <= xlimits[1]]
    for ax, (ylabel, limits, ticks) in zip(axes, specs):
        ax.set(xscale='log', yscale='log', ylim=limits, ylabel=ylabel,
               xlabel='Measured mean CoarseLock read-lock hold (µs)')
        ax.axhline(1, color='#777777', lw=.8)
        ax.set_xticks(xticks)
        ax.set_xlim(*xlimits)
        ax.set_yticks(ticks)
        for axis in (ax.xaxis, ax.yaxis):
            axis.set_major_formatter(FuncFormatter(plain_number))
            axis.set_minor_formatter(NullFormatter())
        ax.tick_params(axis='x', labelsize=6)
        ax.legend(handles=[Line2D([], [], color=c, marker=m, linestyle=l, label=f'r = {r}')
                           for r, c, m, l in ((4, '#235e96', 'o', '-'), (16, '#6595bf', 'D', '--'))])
        decorate(ax)
    save(fig, directory, name, formats)


def fig_intervention(data, directory):
    fig, axes = plt.subplots(2, 2, figsize=(7.16, 5.0), sharex=True)
    for col, r in enumerate((4, 16)):
        cells = [data['cells'][f'I1_r{r:02d}_d{d:04d}'] for d in (0, 20, 200, 2000)]
        for condition, line, hollow, offset in (('second', '-', False, -.045), ('inline', '--', True, .045)):
            for a in ('A', 'AMIN'):
                style = dict(STYLE[a], linestyle=line)
                if hollow:
                    style['markerfacecolor'] = 'white'
                qs = [c['conditions'][condition]['arms'][a]['q']['mean'] for c in cells]
                categorical_series(axes[0, col], range(4), qs, [[v, v] for v in qs], style, 4,
                                   offset=offset + DODGE[a] / 2)
            vals = [c['conditions'][condition]['ratios_A_over_AMIN']['R'] for c in cells]
            style = dict(color='#235e96', marker='o', linestyle=line, label=condition)
            if hollow:
                style['markerfacecolor'] = 'white'
            categorical_series(axes[1, col], range(4), [v['geometric_mean_ratio'] for v in vals],
                               [v['t_ci'] for v in vals], style, 4, offset=offset)
        axes[0, col].set(title=f'r = {r}', ylim=(.8, 2.2), ylabel='Reads per reader per commit, q')
        axes[1, col].set(ylabel='Read ratio CoarseLock / Lease', xlabel='Injected read delay',
                         xticks=range(4), xticklabels=LEVELS[:4])
        axes[1, col].axhline(1, color='#777777', lw=.8)
    handles = [Line2D([], [], **{**STYLE[a], 'linestyle': line,
               'markerfacecolor': 'white' if hollow else STYLE[a]['color'],
               'label': STYLE[a]['label'] + ', ' + label})
               for a in ('A', 'AMIN') for line, hollow, label in
               (('-', False, 'two-acquisition'), ('--', True, 'one-acquisition'))]
    axes[0, 0].legend(handles=handles, fontsize=6, loc='center right')
    axes[1, 0].legend(handles=[Line2D([], [], color='#235e96', marker='o', linestyle=l,
                          markerfacecolor=f, label=label) for l, f, label in
                          (('-', '#235e96', 'two-acquisition'), ('--', 'white', 'one-acquisition'))], fontsize=6)
    for ax in axes.flat:
        decorate(ax)
    save(fig, directory, 'fig_intervention')


def fig_compaction(data, directory):
    fig, axes = plt.subplots(1, 2, figsize=(7.16, 3.15))
    cells = [(c, v) for c, v in sorted(data['cells'].items()) if c.endswith('_on')]
    labels = [f'r={v["nominal"]["reader_concurrency"]}\n{v["nominal"]["read_io_delay_us"]} µs' for _, v in cells]
    axes[0].axhspan(.90, 1 / .90, color='#dddddd', alpha=.6)
    axes[0].axhline(1, color='#777777', lw=.8)
    for i, (_, c) in enumerate(cells):
        v = c['equivalence']['R']
        point(axes[0], i, v['geometric_mean_ratio'], v['t_ci'], STYLE['AMIN'])
        for a in STYLE:
            v = c['arms'][a]['commit_wait_p99']
            point(axes[1], i + DODGE[a], v['mean'] / 1e3,
                  [b / 1e3 for b in v['t_ci_95']], STYLE[a])
    axes[0].set(ylabel='Read throughput: Lease / Snapshot', ylim=(.88, 1.13))
    axes[1].set(yscale='log', ylabel='Commit-wait p99 (µs)')
    arm_legend(axes[1])
    for ax in axes:
        ax.set(xticks=range(6), xticklabels=labels, xlabel='Compaction on: readers and injected delay')
        ax.tick_params(axis='x', labelsize=6)
        decorate(ax)
    save(fig, directory, 'fig_compaction')


def fig_tablecount(data, directory):
    from x_readcost_paper_tables import microbench_means
    fig, axes = plt.subplots(1, 2, figsize=(7.16, 3.35))
    ns = [4, 16, 64, 256]
    axes[0].axhspan(.90, 1 / .90, color='#dddddd', alpha=.6)
    axes[0].axhline(1, color='#777777', lw=.8)
    left_handles, right_handles = [], []
    for r, color, marker in ((4, '#235e96', 'o'), (16, '#6595bf', 'D')):
        for w, line in ((0, '-'), (1, '--')):
            style = dict(color=color, marker=marker, linestyle=line, label=f'r={r}, ' + ('read-only' if w == 0 else 'free writer'))
            vals = [data['cells'][f'T1_n{n:03d}_r{r:02d}_w{w}']['equivalence']['R'] for n in ns]
            categorical_series(axes[0], ns, [v['geometric_mean_ratio'] for v in vals], [v['t_ci'] for v in vals], style, 4)
            left_handles.append(Line2D([], [], **style))
    bench = microbench_means(data)
    for a in ('AMIN', 'B'):
        for g, line in ((1, '-'), (16, '--')):
            style = dict(STYLE[a], linestyle=line, label=f'{STYLE[a]["label"]}, {g} goroutine' + ('s' if g > 1 else ''))
            values = [bench[f'BenchmarkLifecycle/{a}/N{n}/g{g}']['ns_per_op'] for n in ns]
            axes[1].plot(ns, values, **style)
            right_handles.append(Line2D([], [], **style))
    axes[0].set(ylabel='Read throughput: Lease / Snapshot', ylim=(.6, 1.16))
    axes[1].set(yscale='log', ylabel='Microbenchmark read cycle (ns)')
    for ax, handles in zip(axes, (left_handles, right_handles)):
        ax.set(xscale='log', xlabel='Initial SSTable count, N', xticks=ns)
        ax.xaxis.set_major_formatter(FuncFormatter(plain_number))
        ax.xaxis.set_minor_formatter(NullFormatter())
        ax.legend(handles=handles, fontsize=6, loc='best')
        decorate(ax)
    save(fig, directory, 'fig_tablecount')


def fig_paced(data, directory):
    """Closed-reader cells only; per-arm arithmetic means and frozen 95% CIs."""
    fig, axes = plt.subplots(1, 2, figsize=(7.16, 3.3))
    handles = []
    for fraction, line, hollow, shift in ((20, '-', False, -.025), (80, '--', True, .025)):
        cells = [data['cells'][f'P1_d{d:04d}_f{fraction}_closed'] for d in (0, 200, 2000)]
        for arm in STYLE:
            style = dict(STYLE[arm], linestyle=line,
                         label=f'{STYLE[arm]["label"]}, f = {fraction / 100:.1f}')
            if hollow:
                style['markerfacecolor'] = 'white'
            handles.append(Line2D([], [], **style))
            for ax, metric, scale in zip(axes, ('achieved_fraction', 'write_service_p50'), (1., 1e-6)):
                values = [c['arms'][arm][metric] for c in cells]
                categorical_series(ax, range(3), [v['mean'] for v in values],
                                   [v['t_ci_95'] for v in values], style, 3,
                                   offset=shift + .45 * DODGE[arm], scale=scale)
    axes[0].axhline(.95, color='#777777', linewidth=.8, linestyle=':')
    axes[0].set(title='(a) Completed writes / offered ticks', ylabel='Achieved write fraction', ylim=(.30, 1.04))
    axes[1].set(title='(b) Write service time', ylabel='Write service p50 (ms)')
    axes[0].legend(handles=handles, fontsize=6, loc='lower left', ncol=2)
    for ax in axes:
        ax.set(xticks=range(3), xticklabels=['0', '200 \u00b5s', '2 ms'], xlabel='Injected read delay')
        ax.set_xlim(-.15, 2.15)
        decorate(ax)
    save(fig, directory, 'fig_paced')


def fig_hold_reads(report, directory):
    """Post hoc hold account, from report points read from frozen raw evidence.

    Shapes encode dataset; fill encodes arm. Reference lines use each arm's
    fsync-off median (not a fitted slope or a hold-normalized off capacity).
    This function writes only fig_hold_reads.pdf and its PNG preview.
    """
    fig, ax = plt.subplots(figsize=(3.5, 3.1))
    datasets = (('settled_S3', 'Settled S3', 'o'), ('on', 'Fsync on', 's'),
                ('off', 'Fsync off', '^'))
    for dataset, label, marker in datasets:
        points = report['hold_account'][dataset]['points']
        for arm in ('AMIN', 'B'):
            rows = [p for p in points if p['arm'] == arm]
            ax.scatter([p['free_share'] for p in rows], [p['reads_per_sec'] for p in rows],
                       marker=marker, s=22, facecolors='#333333' if arm == 'AMIN' else 'white',
                       edgecolors='#333333', linewidths=.7, zorder=3)
    xs = np.geomspace(.18, 1, 100)
    medians = report['hold_account']['off']['off_read_median_for_arm']
    for arm, line in (('AMIN', '-'), ('B', '--')):
        ax.plot(xs, medians[arm] * xs, color='#777777', linestyle=line, linewidth=.9)
    ax.text(.03, .96, r'$R = R_{\mathrm{off}}\,\times\,\mathrm{free}$',
            transform=ax.transAxes, va='top', fontsize=7)
    ax.set(xscale='log', yscale='log', xlabel='Free share (1 − exclusive hold share)',
           ylabel='Reads/s', xlim=(.18, 1.06), ylim=(70000, 540000))
    ax.set_xticks([.2, .3, .5, 1])
    ax.set_yticks([100000, 200000, 400000])
    ax.xaxis.set_major_formatter(FuncFormatter(plain_number))
    ax.yaxis.set_major_formatter(FuncFormatter(lambda x, _: f'{x / 1000:.0f}k'))
    for axis in (ax.xaxis, ax.yaxis):
        axis.set_minor_formatter(NullFormatter())
    handles = [Line2D([], [], color='#333333', marker=m, markerfacecolor='#aaaaaa',
                      linestyle='none', label=label) for _, label, m in datasets]
    handles += [Line2D([], [], color='#333333', marker='o', linestyle='none',
                       markerfacecolor=fill, label=label) for fill, label in
                (('#333333', 'Lease'), ('white', 'Snapshot'))]
    ax.legend(handles=handles, loc='lower right', fontsize=6, framealpha=.95)
    decorate(ax)
    save(fig, directory, 'fig_hold_reads')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results', type=Path)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--fsync-report', type=Path)
    parser.add_argument('--hold-only', action='store_true', help='Write only the hold/read figure')
    for name in ('intervention-results', 'compaction-results', 'tablecount-results', 'paced-results'):
        parser.add_argument('--' + name, type=Path)
    args = parser.parse_args()
    if args.hold_only:
        if not args.fsync_report:
            parser.error('--hold-only requires --fsync-report')
        args.output_dir.mkdir(parents=True, exist_ok=True)
        setup()
        fig_hold_reads(json.loads(args.fsync_report.read_text(encoding='utf-8')), args.output_dir)
        return 0
    if not args.results:
        parser.error('--results is required unless --hold-only is used')
    data = json.loads(args.results.read_text(encoding='utf-8'))
    args.output_dir.mkdir(parents=True, exist_ok=True)
    setup()
    dose_plot(data, args.output_dir, 'W', 'fig_x1_writes')
    dose_plot(data, args.output_dir, 'R', 'fig_x1_reads')
    ratio_plot(data, args.output_dir)
    publication_plot(data, args.output_dir)
    forest(data, args.output_dir)
    fig_penalty_vs_hold(data, args.output_dir)
    for path, renderer in ((args.intervention_results, fig_intervention),
                           (args.compaction_results, fig_compaction),
                           (args.tablecount_results, fig_tablecount),
                           (args.paced_results, fig_paced)):
        if path:
            renderer(json.loads(path.read_text(encoding='utf-8')), args.output_dir)
    if args.fsync_report:
        fig_hold_reads(json.loads(args.fsync_report.read_text(encoding='utf-8')), args.output_dir)
    return 0


if __name__ == '__main__':
    sys.exit(main())
