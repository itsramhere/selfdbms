#!/usr/bin/env python3
"""X-memtable-v1 preregistered analysis; reads artifacts, never launches trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import sys

_spec = importlib.util.spec_from_file_location(
    'x_followup_helpers', Path(__file__).with_name('x_followup_analyze.py'))
followup = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(followup)
settled, paced, base, compaction = followup.settled, followup.paced, followup.base, followup.compaction
require, load_json = base.require, base.load_json
SEED, SETTLE_MS = 20261112, 20000
RATES, DOMAINS, SYNCS = (183, 366, 549, 732), ('same', 'shadow'), ('on', 'off')
CELLS = [f'M_w{w:04d}_{d}_{s}' for w in RATES for d in DOMAINS for s in SYNCS]


def cell(rate, domain, sync):
    return f'M_w{rate:04d}_{domain}_{sync}'


def factors(c):
    _, rate, domain, sync = c.split('_')
    return int(rate[1:]), domain, sync


def nominal(c):
    require(c in CELLS, f'unknown cell {c}')
    rate, domain, sync = factors(c)
    return dict(followup.nominal(followup.F1[0]), paced_write_rate_per_sec=rate,
                write_key_domain='shadow' if domain == 'shadow' else '', wal_sync=sync == 'on')


def check_spec(actual, expected, context):
    followup.check_spec({**actual, 'write_key_domain': actual.get('write_key_domain', '')},
                        {**expected, 'write_key_domain': expected.get('write_key_domain', '')}, context)
    for key, default in (('lock_policy', ''), ('compaction_open_outside_lock', False)):
        require(actual.get(key, default) == expected.get(key, default), f'unplanned treatment {context}: {key}')


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-memtable-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2**64, 'invalid master seed')
    require(type(plan['settle_ms']) is int and plan['settle_ms'] == SETTLE_MS, 'invalid settle_ms')
    require([c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        require(c['spec']['cell'] == c['id'], 'cell spec identity mismatch')
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 160, 'plan must contain 160 trials')
    specs, groups = {}, {}
    for index, s in enumerate(plan['trials']):
        c, k, tid = s['cell'], s['triplet'], s['trial_id']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and s['arm'] == 'B', 'invalid run')
        require(tid == f'{c}_t{k:02d}_B' and tid not in specs, 'invalid/duplicate run')
        check_spec(s, nominal(c), tid)
        h = hashlib.sha256(f"{plan['master_seed']}|{c}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True), 'incorrect paired seed')
        require(s['runlist_position'] == index+1 and s['order_position'] == 1 and k == index//16+1,
                'invalid round/runlist/order position')
        specs[tid], groups[c, k] = s, {'B': tid}
    require(len(groups) == 160, 'missing cell-round units')
    # Go's PRNG permutations are checked directly in the driver unit test.
    return specs, groups


def trial_outcomes(t):
    out = followup.trial_outcomes(t)
    ops, diag = t['result']['operations'], t['result']['lsm_diagnostics']
    reads, mem = ops['read_ops_completed'], diag['measured_memtable_point_reads']
    require(type(reads) is int and reads >= 0 and type(mem) is int and 0 <= mem <= reads,
            'invalid memtable/read count')
    out.update(memtable_share=mem/reads if reads else None, memtable_reads=mem, read_ops_completed=reads)
    out['hold_per_commit_ns'] = sum(out['hold_phase_means_ns']) if out['commit_count'] else None
    out['hold_phases_ns'] = dict(zip(('validate', 'wal_write', 'wal_sync', 'apply'), out['hold_phase_means_ns']))
    return out


def load_campaign(root, allow_partial=False):
    root = Path(root)
    plan = load_json(root / 'plan.json')
    specs, groups = validate_plan(plan)
    rows, ledger = settled.ledger_rows(root), {}
    for e in rows:
        require(e['trial_id'] in specs and e['event'] in ('STARTED', 'FINISHED')
                and isinstance(e['status'], str) and type(e['exit_code']) is int, 'invalid ledger record')
        events = ledger.setdefault(e['trial_id'], {}).setdefault(e['attempt'], {})
        require(e['event'] not in events, 'duplicate ledger event')
        events[e['event']] = e
    records, missing, failed, failures, pending = {}, [], [], [], []
    hashes, revisions = set(), set()
    for tid, s in sorted(specs.items()):
        attempts = ledger.get(tid, {})
        if not attempts:
            missing.append(dict(trial_id=tid, reason='no ledger record'))
            continue
        for attempt, events in sorted(attempts.items()):
            final = events.get('FINISHED')
            success = final is not None and final['status'] == 'SUCCESS'
            path = root / 'trials' / f'{tid}_a{attempt}.json'
            require(not success or path.exists(), f'missing SUCCESS artifact {tid} a{attempt}')
            t = load_json(path) if path.exists() else None
            values = None
            if success:
                require(t['schema'] == 'i7.trial.v1' and t['status'] == 'SUCCESS' and final['exit_code'] == 0,
                        f'ledger/envelope mismatch {tid}')
                for key in ('trial_id', 'block', 'cell', 'triplet', 'arm'):
                    require(t[key] == s[key], f'envelope {key} mismatch {tid}')
                check_spec(t['spec'], s, tid)
                check_spec(t['result']['config'], s, tid)
                runner, rev = t['runtime']['exe_sha256'], t['result']['git_revision']
                require(isinstance(runner, str) and re.fullmatch('[0-9a-fA-F]{64}', runner), 'invalid runner hash')
                require(isinstance(rev, str) and rev, 'invalid revision')
                hashes.add(runner.lower()); revisions.add(rev)
                values = trial_outcomes(t)
            elif final is not None:
                failures.append(dict(trial_id=tid, attempt=attempt, status=final['status'], artifact_present=t is not None,
                                     error=(t.get('result') or {}).get('error') if t else None))
            if attempt == max(attempts):
                if final is None:
                    missing.append(dict(trial_id=tid, reason='latest attempt unfinished'))
                elif success:
                    records[tid] = values
                elif attempt == 1:
                    pending.append(tid)
                    missing.append(dict(trial_id=tid, reason='retry not completed'))
                else:
                    failed.append(tid)
    require(len(hashes) <= 1 and len(revisions) <= 1, 'mixed runners/revisions')
    require(allow_partial or not missing, f'missing trials ({len(missing)}); use --allow-partial')
    dropped = [dict(entry=c, round=k, unavailable_runs=[tid for tid in g.values() if tid not in records])
               for (c, k), g in sorted(groups.items()) if any(tid not in records for tid in g.values())]
    plan_hash = hashlib.sha256((root / 'plan.json').read_bytes()).hexdigest()
    platform_path = root / 'platform.json'
    platform = load_json(platform_path) if platform_path.exists() else None
    runner, rev = next(iter(hashes), None), next(iter(revisions), None)
    if platform is not None:
        require(platform['campaign_id'] == plan['campaign_id'] and platform['plan_sha256'] == plan_hash,
                'platform campaign/plan mismatch')
        require(not runner or platform['runner_sha256'].lower() == runner, 'platform runner mismatch')
        require(not rev or platform['git_head'] == rev, 'platform revision mismatch')
        runner = runner or platform['runner_sha256'].lower()
        rev = rev or platform['git_head']
    meta = dict(campaign_id=plan['campaign_id'], master_seed=plan['master_seed'], settle_ms=plan['settle_ms'],
                plan_sha256=plan_hash, ledger_sha256=hashlib.sha256((root / 'ledger.jsonl').read_bytes()).hexdigest(),
                runner_sha256=runner, git_revision=rev, platform=platform, partial=bool(missing),
                bootstrap_seed=SEED, bootstrap_resamples=10000,
                counts=dict(planned=len(specs), attempted=len(ledger), attempts=sum(map(len, ledger.values())),
                            successful=len(records), failed=len(failed), failed_attempts=len(failures),
                            missing=len(missing), dropped_units=len(dropped)),
                missing_trials=missing, failed_trials=failed, failed_attempts_detail=failures,
                pending_retry=pending, dropped_units=dropped, gaps=settled.gap_summary(rows, plan['settle_ms']))
    return specs, groups, records, meta


def arithmetic(values, level=.95):
    out = compaction.summary(values)
    defined = [v for v in values if v is not None]
    ci = None
    if len(defined) >= 2:
        n = len(defined)
        half = float(base.stats.t.ppf((1+level)/2, n-1))*float(base.np.std(defined, ddof=1))/math.sqrt(n)
        ci = [out['mean']-half, out['mean']+half]
        rng = base.np.random.default_rng(SEED)
        draws = base.np.asarray(defined)[rng.integers(0, n, size=(10000, n))].mean(axis=1)
        tail = (1-level)*50
        out['bootstrap_ci'] = base.np.percentile(draws, [tail, 100-tail]).tolist()
    else:
        out['bootstrap_ci'] = None
    out.update(level=level, t_ci=ci)
    return out



def supported_all(values):
    values = list(values)
    return False if False in values else None if None in values else True


def rows_for(specs, records, cells, k):
    tids = [f'{c}_t{k:02d}_B' for c in cells]
    return [records[tid] for tid in tids] if all(tid in records for tid in tids) else None


def ols(rows):
    if not rows or not all(v['R'] is not None and v['R'] > 0 and v['free_share'] > 0 for v in rows):
        return None
    x, y = base.np.log([v['free_share'] for v in rows]), base.np.log([v['R'] for v in rows])
    x, y = x-x.mean(), y-y.mean()
    return float(base.np.dot(x, y)/base.np.dot(x, x)) if float(base.np.dot(x, x)) > 0 else None


def collect(specs, records, cells, transform):
    values, rounds, excluded = [], [], []
    for k in range(1, 11):
        rows = rows_for(specs, records, cells, k)
        v = transform(rows) if rows is not None else None
        if v is None:
            excluded.append(dict(round=k, cells=cells, reason='incomplete unit' if rows is None else
                                 'undefined metric, nonpositive logarithm input or constant log free share'))
        else:
            values.append(v); rounds.append(k)
    return dict(values=values, rounds=rounds, excluded_rounds=excluded)


def ratio(unit, equivalence=False):
    out = base.tost(unit['values'], seed=SEED) if equivalence else base.paired_stats(unit['values'], .90, seed=SEED)
    out.update(rounds=unit['rounds'], excluded_rounds=unit['excluded_rounds'], log_values=unit['values'])
    return out


def positive_ratio(rows, metric='R'):
    return settled.positive_log(rows[0][metric], rows[1][metric])


def slope_predictions(specs, records):
    domains = {}
    for d in DOMAINS:
        unit = collect(specs, records, [cell(w, d, 'on') for w in RATES], ols)
        stat = arithmetic(unit['values'], .90)
        ci = stat['t_ci']
        domains[d] = dict(stat, rounds=unit['rounds'], excluded_rounds=unit['excluded_rounds'],
                          band=[.80, 1.25], supported=None if ci is None else .80 <= ci[0] and ci[1] <= 1.25)
    sm = {k: v for k, v in zip(domains['same']['rounds'], domains['same']['values'])}
    sh = {k: v for k, v in zip(domains['shadow']['rounds'], domains['shadow']['values'])}
    rounds = sorted(set(sm) & set(sh))
    diff = arithmetic([sh[k]-sm[k] for k in rounds], .90)
    ci = diff['t_ci']
    diff.update(rounds=rounds, excluded_rounds=[k for k in range(1, 11) if k not in rounds],
                supported=None if ci is None else ci[0] > 0)
    return dict(domains=domains, shadow_minus_same=diff,
                supported=supported_all((domains['shadow']['supported'], diff['supported'])))


def within_rate_slope(specs, records, domain):
    # Fixed rate intercepts: demean log R and log free share within each rate.
    # Resample round labels together across rates for the bootstrap sensitivity.
    points, excluded = [], []
    for w in RATES:
        c = cell(w, domain, 'on')
        for k in range(1, 11):
            rows = rows_for(specs, records, [c], k)
            v = rows[0] if rows else None
            if v is None or v['R'] is None or v['R'] <= 0 or v['free_share'] <= 0:
                excluded.append(dict(cell=c, round=k))
            else:
                points.append((w, k, math.log(v['free_share']), math.log(v['R'])))
    weights = base.np.eye(10, dtype=float)[base.np.random.default_rng(SEED).integers(0, 10, size=(10000, 10))].sum(axis=1)
    def fit(weights):
        numerator, denominator = base.np.zeros(len(weights)), base.np.zeros(len(weights))
        for w in RATES:
            rows = [(k, x, y) for rate, k, x, y in points if rate == w]
            if not rows:
                continue
            ks, xs, ys = zip(*rows)
            x, y = base.np.asarray(xs), base.np.asarray(ys)
            x, y = x-x.mean(), y-y.mean()
            counts = weights[:, base.np.asarray(ks)-1]
            n = counts.sum(axis=1)
            sx, sy = counts @ x, counts @ y
            valid = n > 0
            numerator[valid] += (counts @ (x*y))[valid]-sx[valid]*sy[valid]/n[valid]
            denominator[valid] += (counts @ (x*x))[valid]-sx[valid]**2/n[valid]
        valid = denominator > 0
        slopes = base.np.full(len(weights), base.np.nan)
        slopes[valid] = numerator[valid]/denominator[valid]
        return slopes
    estimate = fit(base.np.ones((1, 10)))[0]
    boot = fit(weights) if math.isfinite(estimate) else base.np.array([])
    defined = boot[base.np.isfinite(boot)]
    return dict(slope=float(estimate) if math.isfinite(estimate) else None, n=len(points), excluded=excluded, sync='on',
                bootstrap_ci=base.np.percentile(defined, [5, 95]).tolist() if len(defined) else None,
                bootstrap_undefined=int(len(boot)-len(defined)), level=.90, definition='OLS with a separate intercept per rate')


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells = {}
    metrics = ('R', 'W', 'hold_share', 'free_share', 'memtable_share', 'flushes',
               'hold_per_commit_ns', 'largest_fsync_ns', 'fsync_count_ge_25ms')
    for c in CELLS:
        rounds = [k for k in range(1, 11) if f'{c}_t{k:02d}_B' in records]
        values = [records[f'{c}_t{k:02d}_B'] for k in rounds]
        summaries = {m: arithmetic([v[m] for v in values]) for m in metrics}
        logs = [math.log(v['R']) if v['R'] is not None and v['R'] > 0 else None for v in values]
        summaries['log_R'] = arithmetic(logs)
        defined = [v for v in logs if v is not None]
        summaries['log_R'].update(sd=float(base.np.std(defined, ddof=1)) if len(defined) >= 2 else None,
                                   excluded_rounds=[k for k, v in zip(rounds, logs) if v is None])
        positive = [(k, v) for k, v in zip(rounds, logs) if v is not None]
        cells[c] = dict(nominal=nominal(c), rounds=rounds, metrics=summaries,
                        hold_phases_ns={p: arithmetic([v['hold_phases_ns'][p] if v['commit_count'] else None for v in values])
                                        for p in ('validate', 'wal_write', 'wal_sync', 'apply')},
                        round_R=[dict(round=k, R=v['R']) for k, v in zip(rounds, values)],
                        log_R_slope_per_round=float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None,
                        saturated_trials=sum(v['saturated'] for v in values))
    hm1 = slope_predictions(specs, records)
    trend_unit = collect(specs, records, [cell(w, 'same', 'off') for w in RATES],
                         lambda rows: [v['R'] for v in rows] if all(v['R'] is not None for v in rows) else None)
    trend = dict(followup.trend_test(trend_unit['values']), rounds=trend_unit['rounds'],
                 excluded_rounds=trend_unit['excluded_rounds'], rates=RATES, predicted_ranks=[1, 2, 3, 4])
    trend['supported'] = None if trend['p'] is None else trend['p'] < .05
    endpoints = lambda d: collect(specs, records, [cell(183, d, 'off'), cell(732, d, 'off')], positive_ratio)
    endpoints_same = ratio(endpoints('same'))
    ci = endpoints_same['t_ci_log']
    endpoints_same.update(threshold_log=math.log(.90), supported=None if ci is None else ci[1] < math.log(.90))
    hm2 = dict(page=trend, R_183_over_732=endpoints_same,
                supported=supported_all((trend['supported'], endpoints_same['supported'])))
    hm3 = ratio(endpoints('shadow'), True)
    paced.adjust_family({'shadow': hm3}, 'H-M3 (1)')
    hm3['supported'] = None if hm3['p_tost'] is None else hm3['classification'] == 'equivalent'
    capacities, hm4, capacity_ratios = {}, {}, {}
    for w in RATES:
        for d in DOMAINS:
            key = f'w{w:04d}_{d}'
            def matched(rows):
                on, off = rows
                c = off['R']/off['free_share'] if off['R'] is not None and off['R'] > 0 and off['free_share'] > 0 else None
                qlog = settled.positive_log(on['R'], c*on['free_share'] if c is not None else None)
                return qlog
            unit = collect(specs, records, [cell(w, d, 'on'), cell(w, d, 'off')], matched)
            hm4[key] = ratio(unit, True)
            cap = collect(specs, records, [cell(w, d, 'off')], lambda rows:
                          rows[0]['R']/rows[0]['free_share'] if rows[0]['R'] is not None and rows[0]['R'] > 0
                          and rows[0]['free_share'] > 0 else None)
            capacities[key] = dict(arithmetic(cap['values']), rounds=cap['rounds'], excluded_rounds=cap['excluded_rounds'])
            for sync in SYNCS:
                cells[cell(w, d, sync)]['matched_capacity'] = capacities[key]
                cells[cell(w, d, sync)]['Q'] = hm4[key] if sync == 'on' else dict(arithmetic([1. for _ in cap['values']]), rounds=cap['rounds'], excluded_rounds=cap['excluded_rounds'], definition='capacity-defining sync-off cell')
        unit = collect(specs, records, [cell(w, 'shadow', 'off'), cell(w, 'same', 'off')], lambda rows:
                       settled.positive_log(rows[0]['R']/rows[0]['free_share'], rows[1]['R']/rows[1]['free_share'])
                       if all(v['R'] is not None and v['R'] > 0 and v['free_share'] > 0 for v in rows) else None)
        capacity_ratios[str(w)] = ratio(unit)
    paced.adjust_family(hm4, 'H-M4 (8)')
    for v in hm4.values():
        v['supported'] = None if v['p_tost'] is None else v['classification'] == 'equivalent'
    violations = [dict(trial_id=tid, cell=specs[tid]['cell'], round=specs[tid]['triplet'], flushes=v['flushes'])
                  for tid, v in sorted(records.items()) if v['flushes'] > 0]
    shadow_checks = {c: dict(mean=cells[c]['metrics']['memtable_share']['mean'],
                             n=cells[c]['metrics']['memtable_share']['n']) for c in CELLS if factors(c)[1] == 'shadow'}
    for v in shadow_checks.values():
        v['supported'] = None if v['mean'] is None else v['mean'] < .001
    same_checks = {}
    for sync in SYNCS:
        means = [cells[cell(w, 'same', sync)]['metrics']['memtable_share']['mean'] for w in RATES]
        same_checks[sync] = dict(means=means, supported=None if None in means else all(a < b for a, b in zip(means, means[1:])))
    manipulation = supported_all([v['supported'] for v in shadow_checks.values()]+[v['supported'] for v in same_checks.values()])
    hx = dict(no_measured_flushes=not violations if records else None, flush_violations=violations,
               shadow_cells=shadow_checks, same_by_sync=same_checks, manipulation_supported=manipulation,
               rising_rule='strictly increasing arithmetic cell means across all four rates, separately for sync on and off',
               supported=supported_all((not violations if records else None, manipulation)))
    for v in (hm1, hm2, hm3):
        v['interpretable'] = manipulation
    hm4_supported = supported_all(v['supported'] for v in hm4.values())
    out.update(cells=cells, H_M1=hm1, H_M2=hm2, H_M3=hm3,
               H_M4=dict(combinations=hm4, supported=hm4_supported), H_X=hx,
               paper_claims=dict(memtable_explanation=supported_all(v['supported'] for v in (hm1, hm2, hm3, hx)),
                                  capacity_times_free_share_all=supported_all((supported_all(v['supported'] for v in (hm1, hm2, hm3, hx)), hm4_supported)),
                                  capacity_times_free_share_supported_combinations=[key for key, v in hm4.items() if v['supported']]),
               capacity=capacities, shadow_over_same_capacity=capacity_ratios,
               same_slope_context=dict(current=hm1['domains']['same'], X_followup_v1=dict(AMIN=.768, B=.779),
                                       earlier_values='context only, never pooled or used in a test'),
               pooled_within_rate_slopes={d: within_rate_slope(specs, records, d) for d in DOMAINS},
               trials={tid: dict(cell=specs[tid]['cell'], round=specs[tid]['triplet'], arm='B', **v) for tid, v in sorted(records.items())})
    exclusions = []
    for name, unit in [('H-M1 '+d, v) for d, v in hm1['domains'].items()]+[
            ('H-M1 paired difference', hm1['shadow_minus_same']), ('H-M2 Page', trend),
            ('H-M2 endpoint ratio', endpoints_same), ('H-M3', hm3)]+[
            ('H-M4 '+key, v) for key, v in hm4.items()]+[('capacity '+key, v) for key, v in capacities.items()]+[
            ('capacity ratio '+key, v) for key, v in capacity_ratios.items()]:
        if unit['excluded_rounds']:
            exclusions.append(dict(calculation=name, rounds=unit['excluded_rounds']))
    exclusions.extend(dict(calculation=c+' log R', rounds=v['metrics']['log_R']['excluded_rounds'])
                      for c, v in cells.items() if v['metrics']['log_R']['excluded_rounds'])
    out.update(outcome_exclusions=exclusions, notes=[
        'No outcome-based exclusions. Flush trials are kept. A failed cell-round is unavailable only to tests needing that cell.',
        'Latest ledger attempt is authoritative; one retry; missing trials require --allow-partial. Smoke artifacts outside the plan are never analyzed.',
        'Nonpositive R/free share excludes only affected log or capacity calculations. Page retains zeros. No read pseudocount is used.',
        'H-M1 uses OLS with intercept per round/domain, 90% Student t CI for mean slopes and paired shadow-minus-same differences.',
        'H-M2 uses one-sided Page method=auto and a paired 90% t CI for ln(R183/R732), wholly below ln(0.90).',
        'H-M3 uses the X-paced equivalence classes. H-M4 uses one Holm family of eight, including untestable members as p=1.',
        'H-X(c) operationalization: strictly increasing arithmetic cell means at each sync setting; no additional significance test was specified.',
        'Manipulation failure suspends interpretation of H-M1 to H-M3; raw decisions remain visible. Flush failure blocks the mechanism claim.',
        'Hold=(validate+wal_write+wal_sync+apply phase means)*commit_count/measured_window_ns; arrival-window selection includes drain. Free share is not clamped.',
        'Memtable share=measured_memtable_point_reads/read_ops_completed, undefined for zero reads; warmup has no writer.',
        'C=R_off/free_off; Q=R_on/(C*free_on), matched by round, rate and domain. Sync-off Q=1 by definition when C is defined.',
        'Hold phases are per-commit means. Largest fsync uses full phase histograms; >=25ms counts use retained traces and are undefined if traces dropped.',
        'Cell log R SD and log R slope per round describe spread and drift; no trend-based exclusions.',
        'Pooled within-rate slopes use sync-on trials with a separate intercept per rate. Capacity figure uses sync-off trials.',
        'Bootstrap sensitivity uses 10000 paired round resamples, seed 20261112, at each reported CI level.'])
    return out


def tables(out):
    lines = ['# X-memtable-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## '+title, '', '| '+' | '.join(headers)+' |', '| '+' | '.join(['---']*len(headers))+' |'])
        lines.extend('| '+' | '.join(base.fmt(v).replace('|', '\\|') for v in row)+' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'settle_ms', 'plan_sha256', 'ledger_sha256', 'runner_sha256',
           'git_revision', 'platform', 'counts', 'bootstrap_seed', 'bootstrap_resamples')])
    for key in ('H_M1', 'H_M2', 'H_M3', 'H_M4', 'H_X', 'paper_claims'):
        table(key, ['Field', 'Result'], out[key].items())
    table('Per-cell descriptive quantities', ['Cell', 'Metric', 'Summary'],
          [(c, m, {k: x for k, x in v.items() if k != 'values'}) for c, cs in out['cells'].items() for m, v in cs['metrics'].items()])
    table('Per-commit hold phases, drift and saturation', ['Cell', 'Result'],
          [(c, {k: cs[k] for k in ('hold_phases_ns', 'log_R_slope_per_round', 'round_R', 'saturated_trials')}) for c, cs in out['cells'].items()])
    table('Capacity and Q per cell', ['Cell', 'C', 'Q'],
          [(c, cs['matched_capacity'], cs['Q']) for c, cs in out['cells'].items()])
    for title, key in (('Matched capacity C', 'capacity'), ('Fit ratio Q (on over matched off)', 'H_M4'),
                       ('Shadow over same capacity', 'shadow_over_same_capacity'), ('Slope context', 'same_slope_context'),
                       ('Pooled within-rate slopes', 'pooled_within_rate_slopes'), ('Inter-attempt gaps', 'gaps')):
        table(title, ['Field', 'Result'], out[key].items())
    table('Failures, missing units and exclusions', ['Kind', 'Detail'], [(k, v) for k in
          ('missing_trials', 'failed_trials', 'failed_attempts_detail', 'pending_retry', 'dropped_units', 'outcome_exclusions') for v in out[k]])
    lines += ['![Capacity against memtable share](x_memtable_capacity.png)', '']
    lines.extend(out['notes'])
    return '\n'.join(lines)+'\n'


def figures(out, directory):
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    fig, ax = plt.subplots()
    for d, marker in zip(DOMAINS, ('o', 'x')):
        for w, color in zip(RATES, ('tab:blue', 'tab:orange', 'tab:green', 'tab:red')):
            rows = [v for v in out['trials'].values() if v['cell'] == cell(w, d, 'off') and
                    v['R'] is not None and v['R'] > 0 and v['free_share'] > 0 and v['memtable_share'] is not None]
            ax.scatter([v['memtable_share'] for v in rows], [v['R']/v['free_share'] for v in rows],
                       label=f'{d}, {w}/s', marker=marker, color=color)
    ax.set(xlabel='Memtable share (sync off)', ylabel='Capacity C = R_off / free_off (reads/s)')
    ax.legend(fontsize='small'); fig.tight_layout()
    fig.savefig(directory/'x_memtable_capacity.png', metadata={'Software': 'x_memtable_analyze'})
    plt.close(fig)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--data-dir', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--allow-partial', action='store_true')
    args = parser.parse_args()
    try:
        out = analyze(args.data_dir, args.allow_partial)
        encoded = json.dumps(out, sort_keys=True, indent=2, allow_nan=False)+'\n'
        args.output_dir.mkdir(parents=True, exist_ok=True)
        figures(out, args.output_dir)
        (args.output_dir/'x_memtable_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir/'x_memtable_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
