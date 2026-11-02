#!/usr/bin/env python3
"""Render paper figures from x_readcost_results.json only (no trial access)."""
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

STYLE = {'A': dict(color='#2a78d6', marker='o', linestyle='-', label='A'),
         'AMIN': dict(color='#eb6834', marker='s', linestyle='--', label='A-min'),
         'B': dict(color='#1baf7a', marker='^', linestyle=':', label='B')}
DODGE = {'A': -.08, 'AMIN': 0., 'B': .08}
LEVELS = ['0', '20 µs', '200 µs', '2 ms', 'legacy']
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


def save(fig, directory, name):
    fig.tight_layout()
    fig.savefig(directory / (name + '.pdf'), metadata={'CreationDate': None, 'ModDate': None})
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
               xlabel=f'Nominal read delay (r = {r})', ylabel='Writes/s' if outcome == 'W' else 'Reads/s')
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
    ax.set(yscale='log', xticks=X, xticklabels=LEVELS, xlabel='Nominal read delay', ylabel='Write ratio A / A-min')
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
        ax.set(xticks=xs, xticklabels=['0', '200 µs', '2 ms', 'legacy'], xlabel='Nominal read delay (r = 16)')
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
             'd2000': '2 ms', 'legacy': 'legacy'}.get(parts[2] if len(parts) > 2 else '', 'no delay')
    return f'r={reader}, {level}'


def cell_order(item):
    cell = item[0]
    parts = cell.split('_')
    dose = parts[2] if len(parts) > 2 else 'd0000'
    return int(parts[1][1:]), 10000 if dose == 'legacy' else int(dose[1:])


def forest_rows(data, outcome):
    family = data.get('H2', {}).get(outcome, {})
    groups = [('X1 mixed (90% CI)', [(c, v) for c, v in family.items() if c.startswith('X1_')]),
              ('X2 publication (90% CI)', [(c, v) for c, v in family.items() if c.startswith('X2_')]),
              ('X3 churn (95% CI)', [(c, ms[outcome]) for c, ms in data.get('H3', {}).items() if outcome in ms])]
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
               xlabel=f'{"Write" if outcome == "W" else "Read"} throughput: A-min / B')
        ax.xaxis.set_major_locator(FixedLocator([.90, 1., 1.11]))
        ax.xaxis.set_major_formatter(FormatStrFormatter('%.2f'))
        ax.xaxis.set_minor_formatter(NullFormatter())
        for tick, (_, value) in zip(ax.get_yticklabels(), rows):
            tick.set_fontsize(6.5 if value is not None else 7)
            tick.set_fontweight('normal' if value is not None else 'bold')
        decorate(ax)
    save(fig, directory, 'fig_amin_vs_b')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    data = json.loads(args.results.read_text(encoding='utf-8'))
    args.output_dir.mkdir(parents=True, exist_ok=True)
    setup()
    dose_plot(data, args.output_dir, 'W', 'fig_x1_writes')
    dose_plot(data, args.output_dir, 'R', 'fig_x1_reads')
    ratio_plot(data, args.output_dir)
    publication_plot(data, args.output_dir)
    forest(data, args.output_dir)
    return 0


if __name__ == '__main__':
    sys.exit(main())
