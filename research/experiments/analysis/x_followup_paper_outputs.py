#!/usr/bin/env python3
"""Deterministic paper outputs for X-followup-v1; never launches trials.

Frozen confirmatory estimates are copied from --results. Descriptive 90% paired
log-t intervals, including the fixed-rate F3/F1 comparison, use raw trials.
PDF timestamps are suppressed. Histogram mass stays at bucket lower bounds.
"""
import argparse
from collections import Counter
import json
import math
from pathlib import Path
import re
import statistics as st

from scipy.stats import t
import x_readcost_figures as style
from matplotlib.ticker import FixedLocator, FuncFormatter, NullFormatter

RATES = (183, 366, 549, 732)
F1 = tuple(f'F1_w{w:04d}_m16' for w in RATES)
F2, F3, F4 = 'F2_w0732_m01', 'F3_w0732_m16_syncoff', 'F4_r04_d0000'
ARMS = ('AMIN', 'B')
NAMES = {'A': 'CoarseLock', 'AMIN': 'Lease', 'B': 'Snapshot'}
SUFFIX = {'A': 'A', 'AMIN': 'L', 'B': 'S'}
ROOT = Path(__file__).resolve().parents[3]
X = ROOT / 'research/experiments/phase_i7/X_READCOST'


def require(ok, message):
    if not ok:
        raise ValueError(message)


def mean_ci(values, level=.90):
    require(len(values) >= 2 and all(math.isfinite(v) for v in values),
            'need at least two finite paired values')
    mean = st.mean(values)
    half = float(t.ppf((1 + level) / 2, len(values) - 1)) * st.stdev(values) / math.sqrt(len(values))
    return mean, (mean - half, mean + half)


def ratio(values):
    mean, ci = mean_ci(values)
    return math.exp(mean), tuple(math.exp(v) for v in ci)


def interval(value, ci, places=3):
    return f'{value:.{places}f} [{ci[0]:.{places}f}, {ci[1]:.{places}f}]'


def load_raw(directory, results):
    """Check the complete retained trial set against the serialized analysis."""
    rows = {}
    for path in sorted((directory / 'trials').glob('*_a*.json')):
        trial = json.loads(path.read_text(encoding='utf-8'))
        tid, r = trial['trial_id'], trial['result']
        require(trial['status'] == r['status'] == 'SUCCESS', 'unsuccessful trial: ' + tid)
        require(tid not in rows and tid == r['trial_id'], 'duplicate/mismatched trial: ' + tid)
        cell, arm, rnd = trial['cell'], trial['arm'], trial['triplet']
        require(all(trial[k] == trial['spec'][k] == r[k] for k in ('trial_id', 'cell', 'arm')),
                'envelope mismatch: ' + tid)
        row = dict(trial_id=tid, cell=cell, arm=arm, round=rnd,
                   R=r['throughput']['read_ops_per_sec'], W=r['throughput']['write_ops_per_sec'],
                   flushes=r['lsm_diagnostics']['measured_flushes'])
        if cell != F4:
            diag = r['stall_diagnostics']
            phases, count = diag['phase_histograms'], diag['commit_count']
            require(all(phases[p]['stats']['sample_count'] == count
                        for p in ('validate', 'wal_write', 'wal_sync', 'apply')), 'phase count mismatch')
            hold = sum(phases[p]['stats']['mean'] for p in ('validate', 'wal_write', 'wal_sync', 'apply'))
            hold *= count / r['timestamps']['measured_window_ns']
            row.update(hold_share=hold, free_share=1 - hold)
            require(0 < row['free_share'] <= 1 and row['R'] > 0, 'invalid logarithmic input')
        else:
            row['histogram'] = r['read_latency_histogram']
            require(sum(b['count'] for b in row['histogram']) == r['latency_read_nanos']['sample_count'],
                    'histogram count mismatch')
        expected = results['trials'][tid]
        for key in ('R', 'W', 'flushes', 'hold_share', 'free_share'):
            if key in row:
                require(math.isclose(row[key], expected[key], rel_tol=1e-12, abs_tol=1e-12),
                        'raw/results mismatch: ' + tid + ' ' + key)
        rows[tid] = row
    require(set(rows) == set(results['trials']), 'raw/results trial set mismatch')
    return list(rows.values())


def paired_gains(rows):
    """Per round ln(R_F3/R_F1), ln(free_F3/free_F1), and their difference.

    Each arm's difference has a two-sided 90% paired Student t CI; exponentiate
    the mean and endpoints to report read gain over free-share gain.
    """
    out = {}
    for arm in ARMS:
        groups = {c: {} for c in (F1[-1], F3)}
        for row in rows:
            if row['arm'] == arm and row['cell'] in groups:
                require(row['round'] not in groups[row['cell']], 'duplicate cell/arm/round')
                groups[row['cell']][row['round']] = row
        on, off = groups[F1[-1]], groups[F3]
        require(set(on) == set(off) and len(on) >= 2, 'incomplete F3/F1 pairing')
        paired = []
        for rnd in sorted(on):
            read = math.log(off[rnd]['R'] / on[rnd]['R'])
            free = math.log(off[rnd]['free_share'] / on[rnd]['free_share'])
            paired.append(dict(round=rnd, log_read_gain=read, log_free_gain=free, difference=read-free))
        mean, ci = mean_ci([p['difference'] for p in paired])
        out[arm] = dict(rounds=paired, mean_difference=mean, difference_ci=ci,
                        read_gain=math.exp(st.mean(p['log_read_gain'] for p in paired)),
                        free_gain=math.exp(st.mean(p['log_free_gain'] for p in paired)),
                        relative_gain=math.exp(mean), relative_gain_ci=tuple(map(math.exp, ci)))
    return out


def read_ratios(rows):
    out = {}
    for cell in F1 + (F2, F3):
        arms = {a: {r['round']: r for r in rows if r['cell'] == cell and r['arm'] == a} for a in ARMS}
        require(set(arms['AMIN']) == set(arms['B']), 'incomplete read ratio pairing')
        logs = [math.log(arms['AMIN'][k]['R'] / arms['B'][k]['R']) for k in sorted(arms['AMIN'])]
        value, ci = ratio(logs)
        out[cell] = dict(value=value, ci=ci, sd=st.stdev(logs))
    return out


def pool_histograms(rows, arm):
    counts = Counter()
    for row in rows:
        if row['cell'] == F4 and row['arm'] == arm:
            previous = -1
            for b in row['histogram']:
                low, n = b['lower_ns'], b['count']
                require(type(low) is int and low > previous and type(n) is int and n >= 0,
                        'invalid histogram bucket')
                counts[low] += n
                previous = low
    require(sum(counts.values()) > 0, 'empty histogram')
    return sorted((low, n) for low, n in counts.items() if n)


def slow_median(hist):
    blocked = [(low, n) for low, n in hist if low >= 500000]
    total, acc = sum(n for _, n in blocked), 0
    require(total > 0, 'no blocked group')
    for low, n in blocked:
        acc += n
        if 2 * acc >= total:
            return low / 1000


def macro_values(data, rows):
    gains, ratios = paired_gains(rows), read_ratios(rows)
    values = {}
    def put(name, value):
        values['Follow' + name] = str(value)
    settled_macros = (ROOT/'paper/generated/settled.tex').read_text(encoding='utf-8')
    prior = re.search(r'\\newcommand\{\\SettledTotalTrials\}\{([\d{},]+)\}', settled_macros)
    require(prior is not None, 'missing prior campaign total')
    prior_count = int(re.sub(r'\D', '', prior[1]))
    put('TotalTrials', f"{prior_count + data['counts']['successful']:,}".replace(',', '{,}'))
    put('CampaignWord', 'seven')
    # Frozen design constants and execution provenance, all kept out of prose.
    constants = dict(RateLow=183, RateSecond=366, RateThird=549, RateHigh=732,
        Readers=16, MemLarge=16, MemSmall=1, Cells=7, PacedCells=6, Rounds=10,
        Trials=data['counts']['planned'], Successes=data['counts']['successful'],
        Seed=data['master_seed'], Pause=f"{data['settle_ms']/1000:.0f}",
        Revision=data['git_revision'][:7], PlanPrefix=data['plan_sha256'][:12],
        RunnerPrefix=data['runner_sha256'][:12], GapMin=f"{data['gaps']['min_ms']/1000:.1f}",
        GapMax=f"{data['gaps']['max_ms']/1000:.1f}", CI=90, RegionCI=95,
        FastBound=200, BlockedBound=500, RegionMin='0.20', ValleyMax='0.10',
        Alpha='0.05', SlopeBand=r'\ensuremath{[0.80,1.25]}',
        Margin=r'\ensuremath{[0.90,1/0.90]}', PageFamily=2, RatioFamily=3,
        PageRanks='1, 2, 3, 4', One=1, Smoke=7, AbortedFinished=13, AbortedKilled=14,
        PThreshold='0.001')
    for name, value in constants.items():
        put(name, value)
    for arm in ARMS:
        suffix = SUFFIX[arm]
        slope = data['H_F1']['arms'][arm]
        put('Slope'+suffix, interval(slope['mean'], slope['t_ci']))
        put('Page'+suffix, f"{data['H_F2'][arm]['L']:.0f}")
        require(data['H_F2'][arm]['holm_p'] < .001, 'Page p threshold no longer holds')
        gain = gains[arm]
        put('ReadGain'+suffix, f"{gain['read_gain']:.3f}")
        put('FreeGain'+suffix, f"{gain['free_gain']:.3f}")
        put('RelativeGain'+suffix, interval(gain['relative_gain'], gain['relative_gain_ci']))
        for cell, word in zip(F1 + (F3,), ('Low', 'Second', 'Third', 'High', 'Off')):
            r = ratios[cell]
            put('Ratio'+word, interval(r['value'], r['ci']))
            put('SD'+word, f"{r['sd']:.3f}")
        flush = [r for r in rows if r['arm'] == arm and r['cell'] == F2]
        base = {r['round']: r for r in rows if r['arm'] == arm and r['cell'] == F1[-1]}
        put('SmallMemRatio'+suffix, interval(*ratio([math.log(r['R']/base[r['round']]['R']) for r in flush])))
        for cell, word in ((F1[0], 'Low'), (F1[-1], 'High')):
            summary = data['cells'][cell]['arms'][arm]
            put('Hold'+word+suffix, f"{summary['hold_share']['mean']:.3f}")
            put('Reads'+word+suffix, f"{summary['R']['mean']:.0f}")
            # Cell mean reads / (arm's F3 median reads * cell mean free share).
            scale = summary['R']['mean'] / (data['capacity'][arm]['F3_median_R'] * summary['free_share']['mean'])
            put('Capacity'+word+suffix, f'{scale:.3f}')
    for cell, word in zip(F1[:3], ('Low', 'Second', 'Third')):
        put('P'+word, f"{data['H_F3'][cell]['holm_p']:.3f}")
    medians = []
    region_rows = []
    for arm in ('A',) + ARMS:
        suffix = SUFFIX[arm]
        metrics = data['cells'][F4]['arms'][arm]
        shares = []
        for region in ('fast', 'valley', 'blocked'):
            v = metrics[region+'_share']
            places = 2 if region == 'valley' else 1
            formatted = interval(100*v['mean'], [100*x for x in v['t_ci']], places)
            put(region.title()+suffix, formatted + r'\%')
            put(region.title()+'Point'+suffix, f"{100*v['mean']:.{places}f}" + r'\%')
            shares.append(formatted)
        median = st.mean(slow_median([(b['lower_ns'], b['count']) for b in r['histogram'] if b['count']])
                         for r in rows if r['cell'] == F4 and r['arm'] == arm)
        medians.append(median)
        put('SlowMedian'+suffix, f'{median:.0f}')
        region_rows.append(NAMES[arm] + ' & ' + ' & '.join(shares) + r' \\')
    put('SlowLow', f'{min(medians):.0f}')
    put('SlowHigh', f'{max(medians):.0f}')
    put('RegionRows', '\n'.join(region_rows))
    # Equal-weight descriptive summaries across the two designs.
    for cell, word in ((F1[0], 'Low'), (F1[-1], 'High')):
        put('HoldAbout'+word, f"{100*st.mean(data['cells'][cell]['arms'][a]['hold_share']['mean'] for a in ARMS):.0f}")
        reads = st.mean(data['cells'][cell]['arms'][a]['R']['mean'] for a in ARMS)
        put('ReadsAbout'+word, f'{round(reads/1000)*1000:,}'.replace(',', '{,}'))
        scale = st.mean(data['cells'][cell]['arms'][a]['R']['mean'] /
                        (data['capacity'][a]['F3_median_R'] * data['cells'][cell]['arms'][a]['free_share']['mean'])
                        for a in ARMS)
        put('Scaling'+word, f'{100*abs(scale-1):.0f}')
    table = []
    for cell, rate in zip(F1 + (F2, F3), RATES + (732, 732)):
        condition = 'on, 16 MiB' if cell in F1 else 'on, 1 MiB' if cell == F2 else 'off, 16 MiB'
        for arm in ARMS:
            metrics = data['cells'][cell]['arms'][arm]
            table.append(f"{rate} & {condition} & {NAMES[arm]} & {metrics['R']['mean']:.0f} & "
                         f"{metrics['W']['mean']:.1f} & {metrics['hold_share']['mean']:.3f} & "
                         f"{metrics['flushes']['mean']:.0f} & {interval(ratios[cell]['value'], ratios[cell]['ci'])}" + r' \\')
    put('PacedRows', '\n'.join(table))
    return values, gains


def save(fig, directory, name):
    fig.savefig(directory / (name+'.pdf'), metadata={'CreationDate': None, 'ModDate': None})
    fig.savefig(directory / (name+'.png'), dpi=300, metadata={'Software': 'x_followup_paper_outputs'})
    style.plt.close(fig)


def figures(rows, directory):
    style.setup()
    fig, ax = style.plt.subplots(figsize=(3.5, 3.0))
    selected = [r for r in rows if r['cell'] in F1 + (F3,)]
    for arm in ARMS:
        s = style.STYLE[arm]
        points = [r for r in selected if r['arm'] == arm]
        ax.scatter([r['free_share'] for r in points], [r['R'] for r in points],
                   s=12, marker=s['marker'], facecolors='none', edgecolors=s['color'],
                   linewidths=.55, label=NAMES[arm], zorder=3)
    off = st.median(r['R'] for r in selected if r['cell'] == F3)
    off_free = st.median(r['free_share'] for r in selected if r['cell'] == F3)
    capacity = off / off_free
    reference, = ax.plot([.18, 1.02], [.18*capacity, 1.02*capacity], '--', color='#777777', linewidth=.8,
                         label='Slope 1 through fsync-off median')
    ax.set(xscale='log', yscale='log', xlabel='Free share', ylabel='Reads/s',
           xlim=(.18, 1.05), ylim=(85000, 760000))
    ax.xaxis.set_major_locator(FixedLocator([.2, .3, .5, .8, 1]))
    ax.xaxis.set_major_formatter(FuncFormatter(lambda v, _: f'{v:g}'))
    ax.yaxis.set_major_locator(FixedLocator([100000, 200000, 400000, 600000]))
    ax.yaxis.set_major_formatter(FuncFormatter(lambda v, _: f'{v/1000:.0f}k'))
    ax.xaxis.set_minor_formatter(NullFormatter())
    ax.yaxis.set_minor_formatter(NullFormatter())
    # Label fixed cluster locations with short leaders; avoid the trial clouds.
    positions = ((.88, 290000), (.49, 415000), (.32, 330000), (.235, 245000), (.83, 670000))
    annotations = []
    for cell, label, pos in zip(F1 + (F3,), tuple(f'{w} writes/s' for w in RATES)+('fsync off',), positions):
        cluster = [r for r in selected if r['cell'] == cell]
        center = (st.median(r['free_share'] for r in cluster), st.median(r['R'] for r in cluster))
        annotations.append(ax.annotate(label, xy=center, xytext=pos, ha='center', fontsize=6.5,
                            arrowprops=dict(arrowstyle='-', color='#888888', lw=.45)))
    legend = ax.legend(loc='lower right', fontsize=5.8, frameon=False, handlelength=1.8)
    style.decorate(ax)
    fig.tight_layout(pad=.5)
    # Check every text box against labels, the reference line, trial markers,
    # and the legend in display coordinates, with padding for visible strokes.
    fig.canvas.draw()
    # Annotation extents include leaders; use the Text base method for text only.
    from matplotlib.text import Text
    boxes = [Text.get_window_extent(a).frozen() for a in annotations]
    require(not any(b.overlaps(c) for i, b in enumerate(boxes) for c in boxes[i+1:]),
            'followup direct-label collision')
    reference_path = reference.get_path().transformed(reference.get_transform())
    point_centers = ax.transData.transform([(r['free_share'], r['R']) for r in selected])
    for annotation, box in zip(annotations, boxes):
        label = annotation.get_text()
        require(not reference_path.intersects_bbox(box.padded(1.5)),
                'followup label/reference-line collision: ' + label)
        marker_box = box.padded(fig.dpi / 72 * 2.5)
        require(not any(marker_box.contains(x, y) for x, y in point_centers),
                'followup label/trial-marker collision: ' + label)
        require(not box.padded(1.5).overlaps(legend.get_window_extent()),
                'followup label/legend collision: ' + label)
    save(fig, directory, 'fig_followup_reads')

    fig, ax = style.plt.subplots(figsize=(3.5, 2.6))
    for arm in ('A',) + ARMS:
        hist = pool_histograms(rows, arm)
        total, acc, xs, ys = sum(n for _, n in hist), 0, [], []
        require(hist[0][0] > 0, 'zero histogram mass needs explicit log display handling')
        for low, count in hist:
            acc += count
            xs.append(low/1000)
            ys.append(acc/total)
        s = style.STYLE[arm]
        ax.step(xs, ys, where='post', color=s['color'], linestyle=s['linestyle'],
                linewidth=1, label=NAMES[arm])
    for bound in (200, 500):
        ax.axvline(bound, color='#777777', linestyle=':', linewidth=.7)
    ax.set(xscale='log', xlabel='Read latency bucket lower bound (µs)', ylabel='Pooled read CDF', ylim=(0, 1.015))
    ax.legend(frameon=False, loc='upper left', fontsize=6.5)
    style.decorate(ax)
    fig.tight_layout(pad=.5)
    save(fig, directory, 'fig_read_cdf')


def run(results, data_dir, macros_out, figures_dir):
    data = json.loads(results.read_text(encoding='utf-8'))
    require(data['campaign_id'] == 'X-followup-v1' and not data['partial'], 'wrong/partial campaign')
    rows = load_raw(data_dir, data)
    values, gains = macro_values(data, rows)
    lines = ['% Generated by x_followup_paper_outputs.py; do not edit.',
             '% Descriptive ratios: 90% paired log-t CIs. Region shares: 95% t CIs.',
             '% Per-round F3/F1 logs and paired differences (unrounded):']
    for arm in ARMS:
        lines.append('% '+arm+' '+json.dumps(gains[arm], sort_keys=True, separators=(',', ':')))
    lines += ['\\newcommand{\\'+name+'}{'+value+'}' for name, value in sorted(values.items())]
    macros_out.parent.mkdir(parents=True, exist_ok=True)
    figures_dir.mkdir(parents=True, exist_ok=True)
    figures(rows, figures_dir)
    macros_out.write_text('\n'.join(lines)+'\n', encoding='utf-8', newline='\n')
    return values


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results', type=Path, default=X/'analysis_followup/x_followup_results.json')
    parser.add_argument('--data-dir', type=Path, default=X/'followup')
    parser.add_argument('--macros-out', type=Path, default=ROOT/'paper/generated/followup.tex')
    parser.add_argument('--figures-dir', type=Path, default=ROOT/'paper/figures/x_readcost')
    args = parser.parse_args()
    run(args.results, args.data_dir, args.macros_out, args.figures_dir)


if __name__ == '__main__':
    main()
