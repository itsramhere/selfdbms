"""Deterministic summaries of the exploratory fsync checks (no experiments run)."""
import argparse
import json
import math
from pathlib import Path

import numpy as np
from scipy import stats

X = Path(__file__).resolve().parents[1] / 'phase_i7' / 'X_READCOST'
MARGIN = math.log(1 / .90)
REFERENCE = {
    '72': ['S1_d2000_f20_closed', 'S1_d2000_f20_open'],
    '155': ['S1_d0200_f20_closed', 'S1_d0200_f20_open'],
    '183': ['S1_d0000_f20_closed', 'S1_d0000_f20_open'],
    '650-730': ['S2_d0200_w700_closed', 'S2_d0200_w700_open', 'S3_d0000_f80_closed'],
}


def burst_count(samples):
    """Maximal clusters of >=25 ms events; a gap of exactly 1 s splits."""
    times = sorted(t for t, ms in samples if ms >= 25)
    return sum(i == 0 or t - times[i - 1] >= 1 for i, t in enumerate(times))


def mean(values):
    return float(np.mean(values)) if len(values) else None


def median(values):
    return float(np.median(values)) if len(values) else None


def sd(values):
    return float(np.std(values, ddof=1)) if len(values) > 1 else None


def spread(values):
    return dict(median=median(values), min=min(values) if len(values) else None,
                max=max(values) if len(values) else None)


def ci_class(ci):
    if ci is None:
        return 'inconclusive'
    if ci[0] > -MARGIN and ci[1] < MARGIN:
        return 'equivalent'
    if ci[1] < -MARGIN or ci[0] > MARGIN:
        return 'not_equivalent'
    return 'inconclusive'


def paired_summary(numerators, denominators):
    if len(numerators) != len(denominators):
        raise ValueError('Unpaired ratio inputs')
    if any(v <= 0 for v in list(numerators) + list(denominators)):
        raise ValueError('Ratios require positive throughputs')
    logs = np.log(numerators) - np.log(denominators)
    center, sigma = mean(logs), sd(logs)
    ci = None
    if len(logs) > 1:
        half = float(stats.t.ppf(.95, len(logs) - 1)) * sigma / math.sqrt(len(logs))
        ci = [center - half, center + half]
    return dict(runs=len(logs), geometric_mean=math.exp(center) if center is not None else None,
                sd_log=sigma, ci90_log=ci, ci90=[math.exp(v) for v in ci] if ci else None,
                classification=ci_class(ci))


def event_summary(records):
    """Each record contains all >=25 ms events, plus full-population counts."""
    n = len(records)
    counts = [r['count_ge_25'] for r in records]
    bursts = [burst_count(r['slow']) for r in records]
    total = sum(r['n'] for r in records)
    return dict(runs=n, mean_achieved_rate=mean([r['n'] / r['window_s'] for r in records]),
                median_fsync_p50_ms=median([r['fsync_p50_ms'] for r in records]),
                any_ge_25=dict(k=sum(r['fsync_max_ms'] >= 25 for r in records), n=n),
                any_ge_45=dict(k=sum(r['fsync_max_ms'] >= 45 for r in records), n=n),
                total_ge_25=sum(counts), total_fsyncs=total,
                fraction_ge_25=sum(counts) / total if total else None,
                mean_window_share_ge_25_pct=mean([r['sum_ge_25_ms'] / (10 * r['window_s']) for r in records]),
                largest_fsync_ms=spread([r['fsync_max_ms'] for r in records]),
                bursts_per_run_median=median(bursts), bursts_total=sum(bursts))


def engine_events(result):
    diag = result['stall_diagnostics']
    hist = diag['phase_histograms']['wal_sync']['stats']
    samples = [(c['WalWriteDoneNs'] / 1e9, (c['WalSyncDoneNs'] - c['WalWriteDoneNs']) / 1e6)
               for c in diag['commits']]
    stored_max = max((ms for _, ms in samples), default=0)
    maximum = hist['max'] / 1e6
    return dict(n=diag['commit_count'], window_s=diag['window_ns'] / 1e9,
                fsync_p50_ms=hist['p50'] / 1e6, fsync_max_ms=maximum, slow=samples,
                count_ge_25=sum(ms >= 25 for _, ms in samples),
                sum_ge_25_ms=sum(ms for _, ms in samples if ms >= 25),
                disagreement=(maximum >= 25) != (stored_max >= 25))


def condition_summary(records):
    reads = [r['throughput']['read_ops_per_sec'] for r in records]
    if any(r <= 0 for r in reads):
        raise ValueError('Log R requires positive read throughput')
    excess = [write_excess(r) for r in records]
    return dict(runs=len(records), mean_W=mean([r['throughput']['write_ops_per_sec'] for r in records]),
                R=spread(reads), sd_log_R=sd(np.log(reads)),
                wal_sync_p50_ms_median=median([engine_events(r)['fsync_p50_ms'] for r in records]),
                any_ge_25=dict(k=sum(engine_events(r)['fsync_max_ms'] >= 25 for r in records), n=len(records)),
                largest_write_service_ms=spread([r['paced_load']['write_service_nanos']['max'] / 1e6 for r in records]),
                write_excess_s=spread(excess),
                achieved=[dict(trial_id=r.get('trial_id'), achieved=r['achieved'])
                          for r in records if 'achieved' in r])


def write_excess(result):
    h = result['paced_load']['write_response_nanos']
    return (h['mean'] - h['p50']) * h['sample_count'] / 1e9


def hold_record(result):
    """Exclusive hold from phase means; burst time from stored >=25 ms fsyncs."""
    d = result['stall_diagnostics']
    hold = sum(d['phase_histograms'][k]['stats']['mean'] * d['commit_count'] / 1e9
               for k in ('validate', 'wal_write', 'wal_sync', 'apply'))
    window = d['window_ns'] / 1e9
    share = hold / window
    if not 0 <= share < 1:
        raise ValueError('Hold share must leave positive free time')
    return dict(hold_share_pct=100 * share, free_share=1 - share,
                burst_share_pct=engine_events(result)['sum_ge_25_ms'] / (10 * window),
                reads_per_sec=result['throughput']['read_ops_per_sec'])


def pearson(x, y):
    return (float(stats.pearsonr(x, y).statistic)
            if len(x) > 1 and len(set(x)) > 1 and len(set(y)) > 1 else None)


def hold_account(datasets):
    """AMIN/B pooled summaries and round-paired post hoc free-share adjustment."""
    off = {a: median([r['throughput']['read_ops_per_sec'] for (arm, _), r in datasets['off'].items()
                      if arm == a]) for a in ('AMIN', 'B')}
    out = {}
    for name, records in datasets.items():
        points = [dict(arm=a, round=r, **hold_record(v)) for (a, r), v in sorted(records.items())]
        hold = [p['hold_share_pct'] for p in points]
        burst = [p['burst_share_pct'] for p in points]
        xs = [math.log(p['free_share']) for p in points]
        ys = [math.log(p['reads_per_sec']) for p in points]
        summary = dict(n=len(points), hold_share_pct=spread(hold), burst_share_pct=spread(burst),
                       points=points, off_read_median_for_arm=off)
        if name != 'off':
            summary.update(log_R_log_free_r=pearson(xs, ys),
                           log_R_log_free_slope=float(stats.linregress(xs, ys).slope) if len(set(xs)) > 1 else None,
                           hold_burst_r=pearson(hold, burst),
                           mean_log_observed_over_predicted=mean([
                               math.log(p['reads_per_sec'] / (off[p['arm']] * p['free_share']))
                               for p in points if off[p['arm']] is not None]))
        rounds = sorted(r for a, r in records if a == 'AMIN' and ('B', r) in records)
        ra = [records['AMIN', r]['throughput']['read_ops_per_sec'] for r in rounds]
        rb = [records['B', r]['throughput']['read_ops_per_sec'] for r in rounds]
        fa = [hold_record(records['AMIN', r])['free_share'] for r in rounds]
        fb = [hold_record(records['B', r])['free_share'] for r in rounds]
        summary['paired'] = dict(rounds=rounds, raw=paired_summary(ra, rb),
            adjusted=paired_summary([r / f for r, f in zip(ra, fa)], [r / f for r, f in zip(rb, fb)]),
            raw_log_ratio_log_free_ratio_r=pearson([math.log(a / b) for a, b in zip(ra, rb)],
                                                  [math.log(a / b) for a, b in zip(fa, fb)]))
        out[name] = summary
    return out


def build_report(checks_dir, settled_dir, assert_settled_counts=True):
    """Disable the historical-count assertion explicitly only for synthetic fixtures."""
    checks_dir, settled_dir = Path(checks_dir), Path(settled_dir)
    report = dict(failed=[], flags=[], bare={}, engine_reference={}, engine_on_off={},
                  paired_ratios={}, pooled_log_R={}, settled_reference={})

    def load(path, diagnostic=False):
        label = ('checks/' + path.relative_to(checks_dir).as_posix() if path.is_relative_to(checks_dir)
                 else 'settled/' + path.relative_to(settled_dir).as_posix())
        try:
            value = json.loads(path.read_text(encoding='utf-8'))
        except (OSError, ValueError) as error:
            report['failed'].append(dict(file=label, reason=type(error).__name__))
            return None
        if diagnostic or 'result' in value:
            result = value.get('result', value)
            if value.get('status', result.get('status')) != 'SUCCESS' or result.get('status') != 'SUCCESS':
                report['failed'].append(dict(file=label, reason='non-SUCCESS'))
                return None
            dropped = result['stall_diagnostics']['commits_dropped']
            if dropped:
                report['flags'].append(dict(file=label, commits_dropped=dropped))
            events = engine_events(result)
            if events['disagreement']:
                report['flags'].append(dict(file=label, histogram_stored_crossing_disagreement=True))
            return result
        return value

    # The plan makes absent outputs visible, including a partially completed run.
    plan = load(checks_dir / 'plan.json')
    if plan is None:
        raise ValueError('Missing or invalid checks plan.json; cannot identify expected runs')
    bare, engine = [], {}
    for entry in plan['runs']:
        if entry['part'] == 'bare':
            path = checks_dir / 'bare' / f"r{entry['rate']:04d}_b{entry['block']:02d}.json"
            record = load(path)
            if record is not None:
                bare.append(record)
                if record['ac_line'] != 1:
                    report['flags'].append(dict(file='checks/' + path.relative_to(checks_dir).as_posix(), ac_line=record['ac_line']))
        else:
            path = checks_dir / 'engine' / (entry['id'] + '.json')
            meta = load(path.with_suffix('.meta.json'))
            record = load(path, diagnostic=True)
            if meta is not None:
                if meta['ac_line'] != 1:
                    report['flags'].append(dict(file='checks/engine/' + entry['id'] + '.meta.json', ac_line=meta['ac_line']))
                if meta['exit_code'] != 0:
                    report['failed'].append(dict(file='checks/engine/' + entry['id'] + '.meta.json',
                                                 reason='exit_code', exit_code=meta['exit_code']))
            if record is not None and meta is not None and meta['exit_code'] == 0:
                engine[entry['condition'], entry['arm'], entry['round']] = record
    for rate in sorted({r['rate'] for r in bare}):
        records = [r for r in bare if r['rate'] == rate]
        report['bare'][str(rate)] = dict(event_summary(records),
            any_ge_25_excluding_first=dict(k=sum(any(ms >= 25 and t > 0 for t, ms in r['slow']) for r in records), n=len(records)),
            first_fsync_ge_25=sum(r['first_fsync_ms'] >= 25 for r in records),
            first_fsync_ms=spread([r['first_fsync_ms'] for r in records]))
    report['burst_run_positions'] = sorted(r['order_position'] for r in bare
        if any(ms >= 25 and t > 0 for t, ms in r['slow']))
    report['bare_first_slow_ms'] = spread([r['first_fsync_ms'] for r in bare if r['first_fsync_ms'] >= 25])
    rates, bursts = [r['rate'] for r in bare], [burst_count(r['slow']) for r in bare]
    rho = stats.spearmanr(rates, bursts) if len(set(rates)) > 1 and len(set(bursts)) > 1 else None
    report['bare_spearman_rate_bursts'] = dict(r=float(rho.statistic), p=float(rho.pvalue)) if rho else dict(r=None, p=None)

    settled = {}
    expected = {'72': (6, 60), '155': (16, 60), '183': (28, 60), '650-730': (88, 90)}
    for rate, cells in REFERENCE.items():
        records = []
        for cell in cells:
            for arm in ('A', 'AMIN', 'B'):
                for rnd in range(1, 11):
                    result = load(settled_dir / 'trials' / f'{cell}_t{rnd:02d}_{arm}_a1.json', diagnostic=True)
                    if result is not None:
                        settled[cell, arm, rnd] = result
                        records.append(engine_events(result))
        summary = event_summary(records)
        summary['histogram_stored_crossing_disagreements'] = sum(r['disagreement'] for r in records)
        report['engine_reference'][rate] = summary
        if assert_settled_counts:
            actual = summary['any_ge_25']
            if (actual['k'], actual['n']) != expected[rate]:
                raise AssertionError(f'Settled {rate}: expected {expected[rate]}, got {actual}')

    residuals = {}
    for condition in ('on', 'off'):
        report['engine_on_off'][condition] = {}
        residuals[condition] = []
        groups = 0
        for arm in ('AMIN', 'B'):
            records = [v for (c, a, _), v in sorted(engine.items()) if (c, a) == (condition, arm)]
            report['engine_on_off'][condition][arm] = condition_summary(records)
            logs = np.log([r['throughput']['read_ops_per_sec'] for r in records])
            if len(logs):
                groups += 1
                residuals[condition].extend(logs - np.mean(logs))
        df = len(residuals[condition]) - groups
        variance = sum(v * v for v in residuals[condition]) / df if df > 0 else None
        report['pooled_log_R'][condition] = dict(df=df, sd=math.sqrt(variance) if variance is not None else None)
        rounds = sorted(r for c, a, r in engine if c == condition and a == 'AMIN' and (c, 'B', r) in engine)
        report['paired_ratios'][condition] = dict(rounds=rounds, numerator='AMIN', denominator='B')
        for metric, field in (('read', 'read_ops_per_sec'), ('write', 'write_ops_per_sec')):
            report['paired_ratios'][condition][metric] = paired_summary(
                [engine[condition, 'AMIN', r]['throughput'][field] for r in rounds],
                [engine[condition, 'B', r]['throughput'][field] for r in rounds])
    on, off = report['pooled_log_R']['on'], report['pooled_log_R']['off']
    ratio, ci = None, None
    if on['sd'] is not None and off['sd'] is not None and on['sd'] > 0:
        ratio = off['sd'] / on['sd']
        ci = [ratio / math.sqrt(float(stats.f.ppf(q, off['df'], on['df']))) for q in (.95, .05)]
    report['pooled_log_R']['sd_off_over_on'] = dict(ratio=ratio, ci90=ci, assumption='normality')
    on_records = [v for (c, _, _), v in sorted(engine.items()) if c == 'on']
    logs = [math.log(r['throughput']['read_ops_per_sec']) for r in on_records]
    excess = [write_excess(r) for r in on_records]
    pearson = stats.pearsonr(logs, excess) if len(logs) > 1 and len(set(logs)) > 1 and len(set(excess)) > 1 else None
    report['on_pearson_log_R_write_excess'] = dict(r=float(pearson.statistic) if pearson else None, runs=len(logs))
    for arm in ('AMIN', 'B'):
        records = [v for (cell, a, _), v in sorted(settled.items()) if cell == 'S3_d0000_f80_closed' and a == arm]
        report['settled_reference'][arm] = condition_summary(records)
    report['hold_account'] = hold_account({
        **{c: {(a, r): v for (condition, a, r), v in engine.items() if condition == c}
           for c in ('on', 'off')},
        'settled_S3': {(a, r): v for (cell, a, r), v in settled.items()
                       if cell == 'S3_d0000_f80_closed' and a in ('AMIN', 'B')}})
    return report


def stable(value):
    """Finite JSON values with fixed rounding, independent of output location/time."""
    if isinstance(value, dict):
        return {k: stable(v) for k, v in sorted(value.items())}
    if isinstance(value, (list, tuple)):
        return [stable(v) for v in value]
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError('Non-finite report value')
        return round(value, 12) or 0.0
    return value


def markdown(report):
    # One flat table per section; dotted columns retain units and full detail.
    def flatten(value, prefix=''):
        if isinstance(value, dict):
            out = {}
            for k, v in sorted(value.items()):
                out.update(flatten(v, prefix + '.' + k if prefix else k))
            return out
        return {prefix: value}

    def display(value):
        if value is None:
            return 'NA'
        if isinstance(value, (list, dict)):
            return json.dumps(value, sort_keys=True, separators=(',', ':'))
        return str(value)

    lines = ['# Fsync checks', '']
    for section, value in sorted(report.items()):
        lines.extend(['## ' + section, '', '| Field | Value |', '| --- | --- |'])
        for key, v in flatten(value).items():
            lines.append('| ' + key + ' | ' + display(v).replace('|', '\\|') + ' |')
        lines.append('')
    return '\n'.join(lines)


def write_report(checks_dir, settled_dir, output_dir, assert_settled_counts=True):
    report = stable(build_report(checks_dir, settled_dir, assert_settled_counts))
    output_dir = Path(output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    (output_dir / 'report.json').write_text(json.dumps(report, indent=2, sort_keys=True, allow_nan=False) + '\n', encoding='utf-8', newline='\n')
    (output_dir / 'report.md').write_text(markdown(report), encoding='utf-8', newline='\n')
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--checks-dir', type=Path, default=X / 'fsync_checks')
    parser.add_argument('--settled-dir', type=Path, default=X / 'settled')
    parser.add_argument('--output-dir', type=Path, default=X / 'fsync_checks' / 'report')
    args = parser.parse_args()
    report = write_report(args.checks_dir.resolve(), args.settled_dir.resolve(), args.output_dir.resolve())
    print(f"Report written; {len(report['failed'])} failed files, {len(report['flags'])} flags")


if __name__ == '__main__':
    main()
