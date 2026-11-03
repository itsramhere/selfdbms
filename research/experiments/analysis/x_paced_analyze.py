#!/usr/bin/env python3
"""Pre-registered X-paced-v1 analysis; reads artifacts, never runs trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import sys

_spec = importlib.util.spec_from_file_location(
    'x_tablecount_helpers', Path(__file__).with_name('x_tablecount_analyze.py'))
tablecount = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(tablecount)
compaction, base = tablecount.compaction, tablecount.base
require, load_json = base.require, base.load_json
ARMS, SEED = base.ARMS, 20261106
LEVELS = (0, 200, 2000)
CELLS = [f'P1_d{d:04d}_f{f:02d}_{reads}' for d in LEVELS for f in (20, 80) for reads in ('closed', 'open')]
RATES = {0: (183, 732, 12500), 200: (155, 619, 8000), 2000: (72, 286, 2850)}
METRICS = ('W', 'R', 'read_service_p50', 'read_service_p99', 'read_response_p50', 'read_response_p99',
           'write_service_p50', 'write_service_p99', 'write_response_p50', 'write_response_p99',
           'commit_wait_p50', 'commit_wait_p99', 'achieved_fraction', 'unissued_ticks',
           'offered_ticks', 'abandoned_reads', 'unissued_reads', 'max_read_lateness_ns', 'flushes')


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    _, dose, load, reads = cell.split('_')
    d, f = int(dose[1:]), int(load[1:])
    return dict(block='X4', reader_concurrency=16, writer_concurrency=1,
                read_path='block', read_io_delay_us=d, memtable_threshold_bytes=1 << 20,
                measurement_duration_ms=10000, warmup_duration_ms=2000, cooldown_duration_ms=200,
                keyspace_cardinality=10000, initial_sstable_count=4, initial_keys_per_sstable=2500,
                expected_reader_set_size=4, target_read_hit_ratio=.95, read_distribution='uniform',
                wal_sync=True, instrument_sync=True, workload_id='X-paced',
                compaction_trigger_interval_ms=0, max_lateness_ms=1000,
                paced_write_rate_per_sec=RATES[d][0 if f == 20 else 1],
                open_loop_rate_per_sec=RATES[d][2] if reads == 'open' else 0)


def check_spec(actual, expected, context):
    require(isinstance(actual, dict), f'invalid spec {context}')
    for key, value in expected.items():
        if key != 'data_dir':
            default = 0 if key in ('open_loop_rate_per_sec', 'read_io_delay_us') else None
            require(actual.get(key, default) == value, f'{context}: {key} disagrees with plan')
    require(actual.get('inline_flush_check', False) is False, f'inline treatment {context}')
    for key in ('publication_events', 'flush_trigger_interval_ms'):
        require(actual.get(key, 0) == 0, f'unplanned treatment {context}: {key}')


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-paced-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2 ** 64, 'invalid master seed')
    require(len(plan['cells']) == 12 and [c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 360, 'plan must contain 360 trials')
    seen, units = set(), {}
    permutations = (('A', 'AMIN', 'B'), ('A', 'B', 'AMIN'), ('AMIN', 'A', 'B'),
                    ('AMIN', 'B', 'A'), ('B', 'A', 'AMIN'), ('B', 'AMIN', 'A'))
    for index, s in enumerate(plan['trials']):
        c, k, arm = s['cell'], s['triplet'], s['arm']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and arm in ARMS, 'invalid run')
        tid = f'{c}_t{k:02d}_{arm}'
        require(s['trial_id'] == tid and tid not in seen, 'invalid/duplicate trial ID')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        h = hashlib.sha256(f"{plan['master_seed']}|{c}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True),
                f'incorrect paired seed {tid}')
        require(s['runlist_position'] == index + 1 and s['order_position'] == index % 3 + 1,
                f'invalid schedule position {tid}')
        require(k == index // 36 + 1, 'invalid round interleaving')
        require(arm == permutations[(k + CELLS.index(c)) % 6][index % 3], 'invalid arm permutation')
        units.setdefault((c, k), []).append(index)
    require(len(units) == 120 and all(len(v) == 3 and v == list(range(v[0], v[0] + 3))
                                    for v in units.values()), 'incomplete/noncontiguous triplet')


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(t['schema'] == 'i7.trial.v1' and t['trial_id'] == s['trial_id'], 'invalid envelope identity')
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    check_spec(r['config'], s, s['trial_id'])
    ops, diag, p = r['operations'], r['lsm_diagnostics'], r['paced_load']
    require(ops['validation_error_count'] == 0 and ops.get('error_ops_count', 0) == 0
            and r['timestamps']['measured_window_ns'] > 0,
            'invalid successful window')
    require(diag.get('compaction_errors', 0) == 0 and diag['measured_compactions'] == 0,
            'unplanned compaction or engine fault')
    require(diag['initial_sstables'] == 4 and diag['final_sstables'] >= 4, 'invalid table counts')
    for cls in ('reads', 'writes'):
        l = r['transaction_ledger'][cls]
        require(all(type(v) is int and v >= 0 for v in l.values()), 'invalid lifecycle count')
        require(l['attempted'] == l['completed_in_window'] + l['completed_late'] + l['failed']
                and l['offered'] == l['attempted'] + l['abandoned'] + l['queued']
                and l['outstanding_at_window_close'] == l['completed_late'], 'ledger violation')
    wl, rl = r['transaction_ledger']['writes'], r['transaction_ledger']['reads']
    period = int(1e9 / s['paced_write_rate_per_sec'])
    window = s['measurement_duration_ms'] * 1000000
    require(p['write_ticks_offered'] == wl['offered'] == (window + period - 1) // period
            and p['write_ticks_unissued'] == wl['queued']
            and p['read_arrivals_abandoned'] == rl['abandoned'], 'paced accounting mismatch')
    require(wl['completed_in_window'] == ops['write_ops_completed_in_window']
            and wl['completed_late'] == ops['write_ops_completed_late'], 'write completion mismatch')
    # Total abandonment can leave the read observer empty. It is an outcome,
    # so do not require the earlier campaign's nonempty read-CS histogram.
    tp = r['throughput']
    waits = r['sync_instrumentation']['writer_lock_wait_nanos'] or {}
    commit = waits.get('commit', {})
    out = dict(W=tp.get('write_ops_per_sec'), R=tp.get('read_ops_per_sec'),
               commit_wait_p50=commit.get('p50_nanos'), commit_wait_p99=commit.get('p99_nanos'))
    out.update(achieved_fraction=wl['completed_in_window'] / wl['offered'],
               offered_ticks=wl['offered'], unissued_ticks=wl['queued'],
               abandoned_reads=rl['abandoned'], unissued_reads=rl['queued'],
               max_read_lateness_ns=p['max_read_lateness_ns'], flushes=diag['measured_flushes'])
    out['not_applicable'] = []
    histograms = dict(read_service=r['latency_read_nanos'], write_service=p['write_service_nanos'],
                      write_response=p['write_response_nanos'])
    if s.get('open_loop_rate_per_sec', 0) > 0:
        histograms['read_response'] = p['read_response_nanos']
    else:
        require('read_response_nanos' not in p, 'closed readers have no response schedule')
        out['not_applicable'] += ['read_response_p50', 'read_response_p99']
        out.update(read_response_p50=None, read_response_p99=None)
    for name, h in histograms.items():
        # A zero-write trial cannot supply a write latency, even if malformed
        # input carries the zero-valued percentile of an empty histogram.
        for q in ('p50', 'p99'):
            out[f'{name}_{q}'] = h.get(q) if h['sample_count'] >= (100 if q == 'p99' else 1) else None
        if name.startswith('write') and wl['completed_in_window'] == 0:
            out[f'{name}_p50'] = out[f'{name}_p99'] = None
    out['saturated'] = out['achieved_fraction'] < .95
    out['write_response_p99_censored_from_below'] = out['saturated']
    out['write_response_p99_label'] = ('undefined' if out['write_response_p99'] is None else
                                      'censored_from_below' if out['saturated'] else 'observed')
    out['zero_writes'] = wl['completed_in_window'] == 0
    out['late_latency'] = {k: v for k, v in p.items() if '_late_nanos' in k}
    for m in METRICS:
        base.number(out[m], m)
    return out


def load_campaign(root, allow_partial=False):
    root = Path(root)
    plan = load_json(root / 'plan.json')
    validate_plan(plan)
    planned = {s['trial_id']: s for s in plan['trials']}
    def checked_outcomes(t):
        s = planned[t['trial_id']]
        check_spec(t['spec'], s, t['trial_id'])
        for key in ('trial_id', 'block', 'cell', 'triplet', 'arm'):
            require(t[key] == s[key], f'envelope {key} mismatch')
        return trial_outcomes(t)
    specs, groups, records, meta = base.load_campaign(
        root, allow_partial, campaign_id='X-paced-v1',
        config_reader=lambda c: {k: v for k, v in nominal(c).items()
                                 if k != 'open_loop_rate_per_sec' or v != 0},
        outcome_reader=checked_outcomes)
    ledger = [json.loads(line) for line in (root / 'ledger.jsonl').read_text(encoding='utf-8').splitlines()]
    require(all(1 <= e['attempt'] <= 2 for e in ledger), 'more than one retry')
    latest = {}
    for e in ledger:
        latest[e['trial_id']] = max(latest.get(e['trial_id'], 0), e['attempt'])
    pending = [tid for tid in meta['failed_trials'] if latest[tid] == 1]
    require(allow_partial or not pending, 'retry not completed; use --allow-partial')
    meta.update(partial=meta['partial'] or bool(pending), pending_retry=pending,
                master_seed=plan['master_seed'], bootstrap_seed=SEED)
    platform_path = root / 'platform.json'
    platform = load_json(platform_path) if platform_path.exists() else None
    if platform is not None:
        require(platform['campaign_id'] == plan['campaign_id'] and platform['plan_sha256'] == meta['plan_sha256'],
                'platform campaign/plan mismatch')
        require(not meta['runner_sha256'] or platform['runner_sha256'].lower() == meta['runner_sha256'].lower(),
                'platform runner mismatch')
        require(not meta['git_revision'] or platform['git_head'] == meta['git_revision'], 'platform git mismatch')
    meta['platform'] = platform
    for detail in meta['failed_attempts_detail']:
        path = root / 'trials' / f"{detail['trial_id']}_a{detail['attempt']}.json"
        if path.exists():
            r = load_json(path).get('result') or {}
            detail.update(error=r.get('error'), compaction_errors=r.get('lsm_diagnostics', {}).get('compaction_errors', 0))
    return specs, groups, records, meta



def ratios(pairs, test=False):
    pairs = list(pairs)
    logs = [math.log(x) - math.log(y) for x, y in pairs
            if x is not None and y is not None and x > 0 and y > 0]
    result = base.tost(logs, seed=SEED) if test else base.paired_stats(logs, seed=SEED)
    result['undefined_n'] = len(pairs) - len(logs)
    return result


def page_test(rows):
    # The imported readcost Page helper fixes four decreasing levels. X4 has
    # three increasing levels; use the same scipy method and tie handling.
    if len(rows) < 3:
        return dict(n=len(rows), L=None, p=None, method=None, mean_ranks=None,
                    reason='fewer than 3 complete rounds across three levels')
    data = base.np.asarray(rows)
    test = base.stats.page_trend_test(data, predicted_ranks=[1, 2, 3], method='auto')
    return dict(n=len(rows), L=float(test.statistic), p=float(test.pvalue), method=test.method,
                mean_ranks=base.np.mean(base.stats.rankdata(data, axis=1), axis=0).tolist())


def adjust_family(family, name):
    for c, p in base.holm({c: v['p_tost'] for c, v in family.items()}).items():
        v, ci = family[c], family[c]['t_ci_log']
        classification = ('equivalent' if p is not None and p < .05 else
                          'not_equivalent' if ci and (ci[1] < -base.MARGIN or ci[0] > base.MARGIN)
                          else 'inconclusive')
        v.update(holm_p=p, classification=classification, family=name,
                 numerator_lower=bool(ci and ci[1] < -base.MARGIN))
    return family


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells, logs, exclusions = {}, {}, []
    for c in CELLS:
        complete = {k: g for (cell, k), g in sorted(groups.items())
                    if cell == c and all(tid in records for tid in g.values())}
        zero = [k for k, g in complete.items() if any(records[g[a]]['W'] == 0 for a in ARMS)]
        cs = dict(complete_triplets=list(complete), nominal=nominal(c), arms={}, ratios={}, equivalence={},
                  zero_adjusted_W_triplets=zero, saturated_trials={}, saturated_successful_trials={})
        for arm in ARMS:
            values = [records[g[arm]] for g in complete.values()]
            cs['saturated_trials'][arm] = sum(v['saturated'] for v in values)
            cs['saturated_successful_trials'][arm] = sum(v['saturated'] for tid, v in records.items()
                                                        if specs[tid]['cell'] == c and specs[tid]['arm'] == arm)
            cs['arms'][arm] = {}
            for m in METRICS:
                applicable = [v[m] for v in values if m not in v['not_applicable']]
                cs['arms'][arm][m] = dict(**compaction.summary(applicable),
                                          not_applicable_n=len(values) - len(applicable))
                if m == 'write_response_p99':
                    censored = [k for k, g in complete.items() if records[g[arm]]['saturated']]
                    cs['arms'][arm][m].update(
                        censored_n=len(censored), censored_rounds=censored,
                        true_p99_summary=not censored,
                        label='observed completion p99; saturated trials censored from below')
            for k, g in complete.items():
                for m in METRICS:
                    if m not in records[g[arm]]['not_applicable'] and records[g[arm]][m] is None:
                        exclusions.append(dict(cell=c, triplet=k, arm=arm, metric=m,
                                               reason='undefined per-arm metric'))
        for m in METRICS:
            cs['ratios'][m] = {}
            if c.endswith('_closed') and m.startswith('read_response_'):
                continue
            for a, b in base.PAIRS:
                pairs, ds, censored = [], {}, []
                for k, g in complete.items():
                    x, y = records[g[a]][m], records[g[b]][m]
                    if m == 'W' and k in zero:
                        x, y = (x + .1 if x is not None else None), (y + .1 if y is not None else None)
                    pairs.append((x, y))
                    if m == 'write_response_p99' and any(records[g[arm]]['saturated'] for arm in (a, b)):
                        censored.append(k)
                    if x is not None and y is not None and x > 0 and y > 0:
                        ds[k] = math.log(x) - math.log(y)
                    else:
                        exclusions.append(dict(cell=c, triplet=k, metric=m, pair=a + '/' + b,
                                               reason='missing/nonpositive log pair'))
                v = ratios(pairs)
                if m == 'write_response_p99':
                    v.update(censored_rounds=censored,
                             label='observed completion p99 ratio; saturated components censored from below',
                             true_p99_ratio=not censored)
                cs['ratios'][m][a + '/' + b] = v
                logs[c, m, a, b] = ds
                if m == 'W' and (a, b) in (('A', 'AMIN'), ('AMIN', 'B')):
                    cs['equivalence'][m + '_' + a + '/' + b] = ratios(pairs, test=True)
                if m == 'R' and c.endswith('_closed') and (a, b) == ('AMIN', 'B'):
                    cs['equivalence']['R_AMIN/B'] = ratios(pairs, test=True)
        cells[c] = cs
    h1 = {}
    for f in (20, 80):
        for reads in ('closed', 'open'):
            names = [f'P1_d{d:04d}_f{f:02d}_{reads}' for d in LEVELS]
            ds = [logs[c, 'write_response_p99', 'A', 'AMIN'] for c in names]
            common = sorted(set.intersection(*(set(d) for d in ds)))
            censored = sorted(set(k for c in names for k in cells[c]['ratios']['write_response_p99']['A/AMIN']['censored_rounds']) & set(common))
            h1[f'f{f:02d}_{reads}'] = dict(
                **page_test([[math.exp(d[k]) for d in ds] for k in common]),
                rounds=common, levels=list(LEVELS), predicted_ranks=[1, 2, 3], alpha=.05,
                censored_rounds=censored, true_p99_inference=not censored,
                label='observed completion p99 trend; censored components are not true p99s')
    for key, p in base.holm({key: v['p'] for key, v in h1.items()}).items():
        h1[key].update(holm_p=p, rise_detected=p is not None and p < .05)
    h2 = adjust_family({c: cells[c]['equivalence']['W_A/AMIN'] for c in CELLS if '_f20_' in c}, 'H-P2')
    h3 = adjust_family({c: cells[c]['equivalence']['W_A/AMIN'] for c in CELLS if '_f80_' in c}, 'H-P3')
    h4 = {m: adjust_family({c: cells[c]['equivalence'][m + '_AMIN/B'] for c in CELLS
                           if m + '_AMIN/B' in cells[c]['equivalence']}, 'H-P4_' + m) for m in ('W', 'R')}
    predictions = []
    for name, family in [('H_P2', h2), ('H_P3', h3), ('H_P4_W', h4['W']), ('H_P4_R', h4['R'])]:
        for c, v in family.items():
            lower = name == 'H_P3' and nominal(c)['read_io_delay_us'] > 0
            predicted = 'not_equivalent' if lower else 'equivalent'
            predictions.append(dict(hypothesis=name, cell=c, predicted=predicted,
                                    predicted_A_lower=lower, found=v['classification'],
                                    matches=v['classification'] == predicted and (not lower or v['numerator_lower'])))
    drift = {}
    for c, cs in cells.items():
        drift[c] = {}
        for arm in ARMS:
            rows = [(k, records[groups[c, k][arm]]['R']) for k in cs['complete_triplets']]
            positive = [(k, math.log(v)) for k, v in rows if v is not None and v > 0]
            slope = float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None
            drift[c][arm] = dict(round_R=rows, log_R_slope_per_round=slope)
    out.update(cells=cells, H_P1=h1, H_P2=h2, H_P3=h3, H_P4=h4, predictions=predictions,
               equivalence_margin_log=base.MARGIN, round_trend_R=drift, outcome_exclusions=exclusions,
               trials={tid: dict(cell=specs[tid]['cell'], triplet=specs[tid]['triplet'], arm=specs[tid]['arm'], **v)
                       for tid, v in sorted(records.items())},
               notes=['H-P1: four one-sided Page tests, ordered d=0,200,2000, ranks 1,2,3; Holm across four.',
                      'Saturated means in-window writes / offered ticks < 0.95. Saturated write response p99 is censored from below: unissued ticks have no latency. Flagged statistics are completion-only summaries, never true p99s.',
                      'H-P2 and H-P3 each have six W A/AMIN tests. H-P4 has separate families: W AMIN/B in 12 cells, R AMIN/B in six closed cells. Untestable planned hypotheses remain p=1 in Holm.',
                      'Open cells have no R equivalence test; closed cells have no read response metric. AMIN/B latency ratios are descriptive, with no prediction.',
                      'No pooling. Complete triplets supply cell summaries and tests; successful dropped-unit observations remain in trial output.',
                      'W zeros use +0.1 writes/s for all arms of a complete paired triplet. Latency has no pseudo-count. Zero-write write latencies and missing p99s are undefined and counted.',
                      'Per-arm means have 95% t CIs. Positive ratios have 95% paired t CIs on logs. TOST uses 90% paired t CI, margin +/- ln(1/0.90), and 10000 paired bootstrap resamples, seed 20261106.',
                      'Scheduled shortfall, abandonment, saturation and zero writes are outcomes, not trial failures.',
                      'One writer, r=16. Open read rates were chosen before data at half of earlier A-min closed-loop capacity (rounded); scheduler and sleep accuracy limit achieved pacing. Report lateness.',
                      'At f=0.8,d=0 A is close to earlier capacity. Capacity may differ under open reads.',
                      'Instrumentation begins at the measured window and includes drain. No anchors; drift is descriptive.',
                      'Smoke data are excluded. Latest ledger attempts are authoritative. Missing trials require --allow-partial.'])
    return out


def tables(out):
    lines = ['# X-paced-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## ' + title, '', '| ' + ' | '.join(headers) + ' |',
                      '| ' + ' | '.join(['---'] * len(headers)) + ' |'])
        lines.extend('| ' + ' | '.join(base.fmt(v).replace('|', '\\|') for v in row) + ' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'plan_sha256', 'ledger_sha256', 'runner_sha256', 'git_revision', 'counts', 'platform')])
    table('H-P1: increasing A/AMIN write response p99', ['Load/readers', 'Result'], out['H_P1'].items())
    for name in ('H_P2', 'H_P3'):
        table(name + ': W A/AMIN equivalence', ['Cell', 'Result'], out[name].items())
    table('H-P4: leasing equivalence', ['Metric', 'Cell', 'Result'],
          [(m, c, v) for m, family in out['H_P4'].items() for c, v in family.items()])
    table('Predictions', ['Result'], [(v,) for v in out['predictions']])
    table('Per-arm metrics (latencies in nanoseconds)', ['Cell', 'Arm', 'Metric', 'Summary'],
          [(c, a, m, v) for c, cs in out['cells'].items() for a, metrics in cs['arms'].items() for m, v in metrics.items()])
    table('Saturated trial counts', ['Cell', 'Complete triplets', 'All successful trials'],
          [(c, cs['saturated_trials'], cs['saturated_successful_trials']) for c, cs in out['cells'].items()])
    table('Within-cell ratios (95% paired t CI)', ['Cell', 'Metric', 'Pair', 'Result'],
          [(c, m, p, v) for c, cs in out['cells'].items() for m, pairs in cs['ratios'].items() for p, v in pairs.items()])
    table('Round trends (descriptive)', ['Cell', 'Arm', 'Trend'],
          [(c, a, v) for c, arms in out['round_trend_R'].items() for a, v in arms.items()])
    table('Trial censoring and accounting', ['Trial', 'Outcome'], out['trials'].items())
    table('Failures and exclusions', ['Kind', 'Details'],
          [(k, v) for k in ('missing_trials', 'failed_trials', 'failed_attempts_detail', 'dropped_triplets',
                           'pending_retry', 'outcome_exclusions') for v in out[k]] +
          [('zero-adjusted W', dict(cell=c, triplets=cs['zero_adjusted_W_triplets']))
           for c, cs in out['cells'].items() if cs['zero_adjusted_W_triplets']])
    lines.extend(out['notes'])
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--data-dir', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--allow-partial', action='store_true')
    args = parser.parse_args()
    try:
        out = analyze(args.data_dir, args.allow_partial)
        encoded = json.dumps(out, sort_keys=True, indent=2, allow_nan=False) + '\n'
        args.output_dir.mkdir(parents=True, exist_ok=True)
        (args.output_dir / 'x_paced_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir / 'x_paced_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
