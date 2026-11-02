#!/usr/bin/env python3
"""Pre-registered X-compaction-v1 analysis; reads artifacts, never runs trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import sys

_spec = importlib.util.spec_from_file_location(
    'x_readcost_helpers', Path(__file__).with_name('x_readcost_analyze.py'))
base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(base)
require, load_json, number = base.require, base.load_json, base.number
ARMS = base.ARMS
SEED = 20261104
BASES = [f'C1_r{r:02d}_d{d:04d}' for r in (4, 16) for d in (0, 200, 2000)]
CELLS = [c + '_' + condition for c in BASES for condition in ('off', 'on')]
CORE = ('W', 'R', 'read_p99', 'commit_wait_p99')
SITES = ('compaction_start', 'compaction_publish', 'compaction_snapshot_publish')
DIST = ('count', 'mean_nanos', 'p99_nanos', 'max_nanos')
LIFECYCLE = tuple(f'{site}_{kind}_{field}' for site in SITES
                  for kind in ('hold', 'wait') for field in DIST) + tuple(
    f'{name}_{field}' for name in ('compaction_wall', 'retirement_lag') for field in DIST) + (
    'compaction_inputs_mean', 'compaction_inputs_max', 'retired_tables_high_water',
    'compactions', 'compactions_in_window', 'compaction_successes', 'compaction_no_ops', 'compaction_errors', 'final_sstables')
METRICS = CORE + LIFECYCLE


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    c, condition = cell.rsplit('_', 1)
    return dict(block='X3', reader_concurrency=int(c[4:6]), read_io_delay_us=int(c[8:]),
                read_path='block', writer_concurrency=1, memtable_threshold_bytes=16 << 10,
                measurement_duration_ms=10000, warmup_duration_ms=2000, cooldown_duration_ms=200,
                keyspace_cardinality=10000, initial_sstable_count=4, initial_keys_per_sstable=2500,
                expected_reader_set_size=4, target_read_hit_ratio=.95, read_distribution='uniform',
                wal_sync=True, instrument_sync=True, workload_id='X-readcost',
                compaction_trigger_interval_ms=250 if condition == 'on' else 0)


def check_spec(actual, expected, context):
    require(isinstance(actual, dict), f'invalid spec {context}')
    for key, value in expected.items():
        if key != 'data_dir':
            require(actual.get(key, 0 if key == 'read_io_delay_us' else None) == value,
                    f'{context}: {key} disagrees with plan')
    require(actual.get('inline_flush_check', False) is False, f'inline treatment {context}')
    for key in ('publication_events', 'flush_trigger_interval_ms', 'open_loop_rate_per_sec',
                'paced_write_rate_per_sec'):
        require(actual.get(key, 0) == 0, f'unplanned treatment {context}: {key}')


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-compaction-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and plan['master_seed'] >= 0, 'invalid master seed')
    require(len(plan['cells']) == 12 and {c['id'] for c in plan['cells']} == set(CELLS), 'invalid cells')
    for c in plan['cells']:
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 360, 'plan must contain 360 trials')
    seen, units = set(), {}
    for index, s in enumerate(plan['trials']):
        c, k, arm = s['cell'], s['triplet'], s['arm']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and arm in ARMS, 'invalid run')
        tid = f'{c}_t{k:02d}_{arm}'
        require(s['trial_id'] == tid and tid not in seen, 'invalid/duplicate trial ID')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        seed_hash = hashlib.sha256(f"{plan['master_seed']}|{c.rsplit('_', 1)[0]}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(seed_hash[:8], 'big', signed=True),
                f'incorrect paired seed {tid}')
        require(s['runlist_position'] == index + 1 and s['order_position'] == index % 3 + 1,
                f'invalid schedule position {tid}')
        require(k == index // 36 + 1, 'invalid round interleaving')
        unit = units.setdefault((c, k), [])
        unit.append(index)
    require(len(units) == 120 and all(len(v) == 3 and v == list(range(v[0], v[0] + 3))
                                    for v in units.values()), 'incomplete/noncontiguous triplet')


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(t['schema'] == 'i7.trial.v1' and t['trial_id'] == s['trial_id'], 'invalid envelope identity')
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    check_spec(r['config'], s, s['trial_id'])
    ops = r['operations']
    require(ops['validation_error_count'] == 0 and ops['read_ops_completed'] > 0
            and r['timestamps']['measured_window_ns'] > 0, 'invalid successful window')
    out = base.outcomes(t)
    out = {m: out[m] for m in CORE}
    sync, diag = r['sync_instrumentation'], r['lsm_diagnostics']
    require(diag.get('compaction_errors', 0) == 0, 'SUCCESS trial has compaction errors (engine fault)')
    def distribution(dist, context):
        require(isinstance(dist, dict), f'invalid distribution {context}')
        if dist:
            require(all(field in dist for field in DIST), f'incomplete distribution {context}')
            require(type(dist['count']) is int and dist['count'] > 0, f'invalid count {context}')
            for field in DIST:
                number(dist[field], context + '.' + field)
                require(dist[field] is not None, f'missing value {context}.{field}')
        return dist
    for site in SITES:
        for kind, group in (('hold', 'publication_nanos'), ('wait', 'writer_lock_wait_nanos')):
            dist = distribution(sync.get(group, {}).get(site, {}), group + '.' + site)
            for field in DIST:
                out[f'{site}_{kind}_{field}'] = dist.get(field)
    for name in ('compaction_wall', 'retirement_lag'):
        dist = distribution(sync.get(name + '_nanos', {}), name)
        for field in DIST:
            out[f'{name}_{field}'] = dist.get(field)
    inputs = sync.get('compaction_inputs', {})
    out.update(compaction_inputs_mean=inputs.get('mean'), compaction_inputs_max=inputs.get('max'),
               retired_tables_high_water=sync.get('retired_tables_high_water', 0),
               compactions=diag['measured_compactions'], compactions_in_window=diag.get('compactions_in_window', 0 if s['cell'].endswith('_off') else None),
               compaction_successes=diag.get('compaction_successes', 0),
               compaction_no_ops=diag.get('compaction_no_ops', 0),
               compaction_errors=diag.get('compaction_errors', 0), final_sstables=diag['final_sstables'])
    for key, value in out.items():
        number(value, key)
    # A missing lifecycle histogram means no such event, never a measured zero lag.
    if out['compactions']:
        require(out['compaction_wall_count'] == out['compactions']
                and out['compaction_inputs_mean'] is not None and out['compaction_inputs_max'] is not None,
                'compaction work without complete lifecycle instrumentation')
        site = 'compaction_snapshot_publish' if s['arm'] == 'B' else 'compaction_publish'
        require(out[f'{site}_hold_count'] is not None and out[f'{site}_wait_count'] is not None,
                'compaction work without publication instrumentation')
        if s['arm'] != 'B':
            require(out['compaction_start_hold_count'] is not None and out['compaction_start_wait_count'] is not None,
                    'compaction work without start instrumentation')
    return out


def load_campaign(root, allow_partial=False):
    root = Path(root)
    plan = load_json(root / 'plan.json')
    validate_plan(plan)
    specs_by_id = {s['trial_id']: s for s in plan['trials']}
    def checked_outcomes(t):
        s = specs_by_id[t['trial_id']]
        check_spec(t['spec'], s, t['trial_id'])
        for key in ('trial_id', 'block', 'cell', 'triplet', 'arm'):
            require(t[key] == s[key], f'envelope {key} mismatch')
        return trial_outcomes(t)
    specs, groups, records, meta = base.load_campaign(
        root, allow_partial, campaign_id='X-compaction-v1', config_reader=nominal,
        outcome_reader=checked_outcomes)
    ledger = [json.loads(line) for line in (root / 'ledger.jsonl').read_text(encoding='utf-8').splitlines()]
    require(all(1 <= e['attempt'] <= 2 for e in ledger), 'more than one retry')
    # A first failed attempt awaiting its authorized retry is incomplete data.
    latest = {}
    for e in ledger:
        latest[e['trial_id']] = max(latest.get(e['trial_id'], 0), e['attempt'])
    pending_retry = [tid for tid in meta['failed_trials'] if latest[tid] == 1]
    require(allow_partial or not pending_retry, 'retry not completed; use --allow-partial')
    meta['partial'] = meta['partial'] or bool(pending_retry)
    meta['pending_retry'] = pending_retry
    meta.update(master_seed=plan['master_seed'], bootstrap_seed=SEED)
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


def paired(logs, level=.95):
    return base.paired_stats(logs, level, seed=SEED)


def summary(values):
    defined = [v for v in values if v is not None]
    n = len(defined)
    mean = sum(defined) / n if n else None
    ci = None
    if n >= 2:
        sd = float(base.np.std(defined, ddof=1))
        half = float(base.stats.t.ppf(.975, n - 1)) * sd / math.sqrt(n)
        ci = [mean - half, mean + half]
    return dict(n=n, undefined_n=len(values) - n, mean=mean, t_ci_95=ci,
                min=min(defined) if n else None, max=max(defined) if n else None, values=values)


def ratios(pairs, level=.95, test=False):
    pairs = list(pairs)
    logs = [math.log(x) - math.log(y) for x, y in pairs
            if x is not None and y is not None and x > 0 and y > 0]
    out = base.tost(logs, seed=SEED) if test else paired(logs, level)
    out['undefined_n'] = len(pairs) - len(logs)
    return out


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells = {}
    for cell in CELLS:
        complete = {k: g for (c, k), g in sorted(groups.items())
                    if c == cell and all(tid in records for tid in g.values())}
        # The triplet is the unit for every cell summary, including lifecycle costs.
        def values(arm, metric):
            return [records[g[arm]][metric] for g in complete.values()]
        cs = dict(complete_triplets=list(complete), nominal=nominal(cell), arms={}, ratios={}, equivalence={})
        zero = [k for k, g in complete.items() if any(records[g[a]]['W'] == 0 for a in ARMS)]
        cs['zero_adjusted_W_triplets'] = zero
        for arm in ARMS:
            cs['arms'][arm] = {m: summary(values(arm, m)) for m in METRICS}
        for m in METRICS:
            cs['ratios'][m] = {}
            for a, b in (('A', 'AMIN'), ('A', 'B'), ('AMIN', 'B')):
                pairs = [(records[g[a]][m], records[g[b]][m]) for g in complete.values()]
                if m == 'W':
                    pairs = [(x + .1 if k in zero else x, y + .1 if k in zero else y)
                             for k, (x, y) in zip(complete, pairs)]
                cs['ratios'][m][a + '/' + b] = ratios(pairs)
                if m in ('W', 'R') and (a, b) == ('AMIN', 'B'):
                    cs['equivalence'][m] = ratios(pairs, test=True)
        cells[cell] = cs
    for condition in ('on', 'off'):
        for m in ('W', 'R'):
            family = {c: cells[c]['equivalence'][m]['p_tost'] for c in CELLS if c.endswith('_' + condition)}
            for c, p in base.holm(family).items():
                v = cells[c]['equivalence'][m]
                ci = v['t_ci_log']
                classification = 'inconclusive'
                if p is not None and p < .05:
                    classification = 'equivalent'
                elif ci and (ci[1] < -base.MARGIN or ci[0] > base.MARGIN):
                    classification = 'not_equivalent'
                v.update(holm_p=p, classification=classification, family=f'{condition}/{m}',
                         primary=condition == 'on')
    on_off, drift = {}, {}
    for c in BASES:
        on_off[c] = {}
        for arm in ARMS:
            on_off[c][arm] = {}
            for m in CORE:
                pairs, rounds, zero = [], [], []
                for k in range(1, 11):
                    off, on = groups[c + '_off', k], groups[c + '_on', k]
                    if all(g[a] in records for g in (off, on) for a in ARMS):
                        x, y = records[on[arm]][m], records[off[arm]][m]
                        if m == 'W' and any(records[g[a]]['W'] == 0 for g in (off, on) for a in ARMS):
                            x, y = x + .1, y + .1
                            zero.append(k)
                        pairs.append((x, y))
                        rounds.append(k)
                on_off[c][arm][m] = dict(**ratios(pairs), paired_rounds=rounds, zero_adjusted_rounds=zero)
    for c, cs in cells.items():
        drift[c] = {}
        for arm in ARMS:
            rows = [(k, records[groups[c, k][arm]]['R']) for k in cs['complete_triplets']]
            positive = [(k, math.log(r)) for k, r in rows if r > 0]
            slope = float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None
            drift[c][arm] = dict(round_R=rows, log_R_slope_per_round=slope)
    primary = [cells[c]['equivalence'][m]['classification'] for c in CELLS if c.endswith('_on') for m in ('W', 'R')]
    out.update(cells=cells, on_off=on_off, round_trend_R=drift,
               H_C=dict(predicted='all 12 equivalent', equivalent=primary.count('equivalent'), planned=12,
                        all_equivalent=all(v == 'equivalent' for v in primary) and not out['partial']),
               trials={tid: dict(cell=specs[tid]['cell'], triplet=specs[tid]['triplet'], arm=specs[tid]['arm'], **v)
                       for tid, v in sorted(records.items())},
               notes=['No cells are pooled. On and off have separate six-cell Holm families per outcome.',
                      'Lifecycle and synchronization summaries start at window reset and include drain through Close.',
                      'Compactions in-window count successful calls that did work and returned within the window; measured_compactions includes drain.',
                      'Missing histograms are structural absences. Undefined/nonpositive log pairs are counted; zeros remain in arithmetic summaries.',
                      'W zeros use +0.1 writes/s for the complete paired unit, as in X-readcost-v1 section 5.',
                      'Per-arm trial means have 95% t CIs; paired ratios have 95% paired t CIs. TOST uses 90% CIs.',
                      'Any primary outcome other than equivalent qualifies the earlier leasing claim and must be reported in the paper.',
                      'A/A-min open input iterators under Mu exclusively; B opens them outside all locks. This implementation asymmetry is unchanged.',
                      'No anchors. Round trends are descriptive, by cell and arm. Smoke data are excluded.'])
    return out


def tables(out):
    lines = ['# X-compaction-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## ' + title, '', '| ' + ' | '.join(headers) + ' |',
                      '| ' + ' | '.join(['---'] * len(headers)) + ' |'])
        lines.extend('| ' + ' | '.join(base.fmt(v).replace('|', '\\|') for v in row) + ' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'plan_sha256', 'ledger_sha256', 'runner_sha256', 'git_revision', 'counts', 'platform')])
    table('H-C and secondary reference (90% paired t CI, percentile bootstrap sensitivity)',
          ['Cell', 'Outcome', 'Result'], [(c, m, v) for c, cs in out['cells'].items() for m, v in cs['equivalence'].items()])
    table('On/off ratios (95% paired t CI)', ['Base', 'Arm', 'Outcome', 'Result'],
          [(c, a, m, v) for c, arms in out['on_off'].items() for a, metrics in arms.items() for m, v in metrics.items()])
    table('Per-arm outcomes and lifecycle costs (95% t CI across trial summaries)', ['Cell', 'Arm', 'Metric', 'Result'],
          [(c, a, m, v) for c, cs in out['cells'].items() for a, metrics in cs['arms'].items() for m, v in metrics.items()])
    table('Within-cell ratios (95% paired t CI)', ['Cell', 'Metric', 'Pair', 'Result'],
          [(c, m, p, v) for c, cs in out['cells'].items() for m, pairs in cs['ratios'].items() for p, v in pairs.items()])
    table('Round trends of R (descriptive)', ['Cell', 'Arm', 'Trend'],
          [(c, a, v) for c, arms in out['round_trend_R'].items() for a, v in arms.items()])
    table('Failures and exclusions', ['Kind', 'Details'],
          [(k, v) for k in ('missing_trials', 'failed_trials', 'failed_attempts_detail', 'dropped_triplets', 'pending_retry') for v in out[k]] +
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
        (args.output_dir / 'x_compaction_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir / 'x_compaction_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
