#!/usr/bin/env python3
"""X-followup-v1 preregistered analysis; reads artifacts, never launches trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import sys

_spec = importlib.util.spec_from_file_location(
    'x_settled_helpers', Path(__file__).with_name('x_settled_analyze.py'))
settled = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(settled)
paced, base, compaction = settled.paced, settled.base, settled.compaction
require, load_json = base.require, base.load_json
SEED, SETTLE_MS = 20261108, 20000
F1 = [f'F1_w{w:04d}_m16' for w in (183, 366, 549, 732)]
F2, F3, F4 = 'F2_w0732_m01', 'F3_w0732_m16_syncoff', 'F4_r04_d0000'
CELLS = F1 + [F2, F3, F4]
ARMS = ('AMIN', 'B')
PERMUTATIONS = settled.PERMUTATIONS
OPTIONAL_ZERO = settled.OPTIONAL_ZERO


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    if cell == F4:
        return dict(block='X1', workload_id='X-readcost', reader_concurrency=4,
                    writer_concurrency=1, keyspace_cardinality=10000, initial_sstable_count=4,
                    initial_keys_per_sstable=2500, expected_reader_set_size=4,
                    target_read_hit_ratio=.95, read_distribution='uniform',
                    warmup_duration_ms=2000, measurement_duration_ms=10000, cooldown_duration_ms=200,
                    wal_sync=True, instrument_sync=True, read_path='block', read_io_delay_us=0,
                    memtable_threshold_bytes=1 << 20, compaction_trigger_interval_ms=0,
                    persist_read_histogram=True, diag_trace=False)
    out = dict(paced.nominal('P1_d0000_f80_closed'), diag_trace=True,
               persist_read_histogram=False, memtable_threshold_bytes=16 << 20)
    if cell in F1:
        out['paced_write_rate_per_sec'] = (183, 366, 549, 732)[F1.index(cell)]
    if cell == F2:
        out['memtable_threshold_bytes'] = 1 << 20
    if cell == F3:
        out['wal_sync'] = False
    return out


def arms(cell):
    return base.ARMS if cell == F4 else ARMS


def check_spec(actual, expected, context):
    # Reuse the frozen treatment checker, explicitly accounting for this new
    # optional flag without changing any earlier analysis module.
    settled.check_spec({**actual, 'persist_read_histogram': actual.get('persist_read_histogram', False)},
                       {**expected, 'persist_read_histogram': expected.get('persist_read_histogram', False)}, context)
    require(actual.get('schema') == 'i7.trial-spec.v1', f'invalid spec schema {context}')


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-followup-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2**64, 'invalid master seed')
    require(type(plan['settle_ms']) is int and plan['settle_ms'] == SETTLE_MS, 'invalid settle_ms')
    require([c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        require(c['spec']['cell'] == c['id'], 'cell spec identity mismatch')
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 150, 'plan must contain 150 trials')
    units, seen = {}, set()
    for index, s in enumerate(plan['trials']):
        c, k, arm, tid = s['cell'], s['triplet'], s['arm'], s['trial_id']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and arm in arms(c), 'invalid run')
        require(tid == f'{c}_t{k:02d}_{arm}' and tid not in seen, 'invalid/duplicate run')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        h = hashlib.sha256(f"{plan['master_seed']}|{c}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True), 'incorrect paired seed')
        require(s['runlist_position'] == index+1 and k == index//15+1, 'invalid round/runlist position')
        units.setdefault((c, k), []).append((index, s))
    require(len(units) == 70, 'missing cell-round units')
    for (c, k), rows in units.items():
        i = CELLS.index(c)
        order = PERMUTATIONS[(k+i) % 6] if c == F4 else ARMS if (k+i) % 2 == 0 else ARMS[::-1]
        require(tuple(s['arm'] for _, s in rows) == order, 'incorrect arm order/incomplete unit')
        require([i for i, _ in rows] == list(range(rows[0][0], rows[0][0]+len(rows))), 'noncontiguous unit')
        require([s['order_position'] for _, s in rows] == list(range(1, len(rows)+1)), 'invalid order positions')
    # Go's rand shuffle is tested against the driver directly; the analysis
    # checks round interleaving, contiguity, seeds and every arm order.
    return {s['trial_id']: s for s in plan['trials']}, {
        key: {s['arm']: s['trial_id'] for _, s in rows} for key, rows in units.items()}


def histogram_regions(buckets, expected_count):
    require(isinstance(buckets, list) and buckets, 'missing read histogram buckets')
    totals = dict(fast=0, valley=0, blocked=0)
    previous, total, boundary = -1, 0, []
    for i, b in enumerate(buckets):
        low, count = b['lower_ns'], b['count']
        require(type(low) is int and low > previous and type(count) is int and count >= 0,
                'invalid read histogram bucket')
        previous, total = low, total+count
        region = 'fast' if low < 200000 else 'valley' if low < 500000 else 'blocked'
        totals[region] += count
        # Exact upper boundary follows the existing power-of-two subdivision.
        width = 1 << max(0, low.bit_length()-8)
        upper = low+width
        for threshold in (200000, 500000):
            if count and low < threshold < upper:
                boundary.append(dict(lower_ns=low, upper_ns=upper, count=count, threshold_ns=threshold))
    require(total == expected_count, 'read histogram/sample count mismatch')
    return dict(shares={k: v/total if total else None for k, v in totals.items()},
                count=total, boundary_buckets=boundary, buckets=buckets,
                convention='regions assigned by lower_ns; boundary buckets are explicitly listed')


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    ops, diag, window = r['operations'], r['lsm_diagnostics'], r['timestamps']['measured_window_ns']
    require(ops['validation_error_count'] == 0 and ops.get('error_ops_count', 0) == 0 and window > 0,
            'invalid successful window')
    require(diag.get('compaction_errors', 0) == 0 and diag['measured_compactions'] == 0,
            'unplanned compaction or engine fault')
    if s['block'] == 'X4':
        out = paced.trial_outcomes(t)
        out.update(settled.stall_outcomes(r))
        d = r['stall_diagnostics']
        phases, count = d['phase_histograms'], d['commit_count']
        means = [phases[p]['stats']['mean'] for p in ('validate', 'wal_write', 'wal_sync', 'apply')]
        require(all(phases[p]['stats']['sample_count'] == count for p in ('validate', 'wal_write', 'wal_sync', 'apply')),
                'phase count mismatch')
        require(all(isinstance(v, (float, int)) and math.isfinite(v) and v >= 0 for v in means), 'invalid phase mean')
        hold = sum(means)*count/window
        out.update(hold_share=hold, free_share=1-hold, commit_count=count,
                   hold_window_ns=window, hold_phase_means_ns=means)
    else:
        require(ops['read_ops_completed'] > 0, 'invalid mixed read count')
        out = dict(R=r['throughput']['read_ops_per_sec'], W=r['throughput']['write_ops_per_sec'],
                   flushes=diag['measured_flushes'])
        h = r['latency_read_nanos']
        require(h['sample_count'] == ops['read_ops_completed_in_window'], 'read sample count mismatch')
        out['read_histogram'] = histogram_regions(r['read_latency_histogram'], h['sample_count'])
        for region, share in out['read_histogram']['shares'].items():
            out[region+'_share'] = share
    for key in ('R', 'W', 'flushes'):
        base.number(out[key], key)
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


def ratio_summary(unit, test=False, level=.95):
    v = base.tost(unit['logs'], seed=SEED) if test else base.paired_stats(unit['logs'], level, seed=SEED)
    return dict(v, **{k: x for k, x in unit.items() if k != 'logs'}, undefined_n=len(unit['excluded_rounds']))


def complete_units(groups, records, cell):
    return {k: g for (c, k), g in sorted(groups.items()) if c == cell and all(tid in records for tid in g.values())}


def slope_test(groups, records, omit_rounds=()):
    result = {}
    complete = [complete_units(groups, records, c) for c in F1]
    for arm in ARMS:
        slopes, rounds, excluded = [], [], []
        for k in range(1, 11):
            if k in omit_rounds:
                excluded.append(dict(round=k, reason='flush protocol sensitivity'))
                continue
            if not all(k in g for g in complete):
                excluded.append(dict(round=k, reason='incomplete F1 cell-round unit'))
                continue
            rows = [records[g[k][arm]] for g in complete]
            if not all(v['R'] is not None and v['R'] > 0 and v['free_share'] > 0 for v in rows):
                excluded.append(dict(round=k, reason='missing/nonpositive R or free share'))
                continue
            x = base.np.log([v['free_share'] for v in rows])
            y = base.np.log([v['R'] for v in rows])
            if float(base.np.ptp(x)) == 0:
                excluded.append(dict(round=k, reason='constant log free share'))
                continue
            slopes.append(float(base.np.dot(x-x.mean(), y-y.mean()) / base.np.dot(x-x.mean(), x-x.mean())))
            rounds.append(k)
        stat = arithmetic(slopes, .90)
        ci = stat['t_ci']
        result[arm] = dict(stat, rounds=rounds, excluded_rounds=excluded, band=[.80, 1.25],
                           supported=None if ci is None else .80 <= ci[0] and ci[1] <= 1.25)
    return dict(arms=result, supported=all(v['supported'] for v in result.values()) if
                all(v['supported'] is not None for v in result.values()) else None)


def trend_test(rows):
    if len(rows) < 3:
        return dict(n=len(rows), L=None, p=None, method=None, mean_ranks=None)
    data = base.np.asarray(rows)
    test = base.stats.page_trend_test(data, predicted_ranks=[1, 2, 3, 4], method='auto')
    return dict(n=len(rows), L=float(test.statistic), p=float(test.pvalue), method=test.method,
                mean_ranks=base.np.mean(base.stats.rankdata(data, axis=1), axis=0).tolist())


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells = {}
    for c in CELLS:
        complete = complete_units(groups, records, c)
        summaries = {}
        for arm in arms(c):
            values = [records[g[arm]] for g in complete.values()]
            metrics = ('R', 'W', 'flushes', 'fast_share', 'valley_share', 'blocked_share') if c == F4 else (
                'R', 'W', 'hold_share', 'free_share', 'flushes', 'largest_fsync_ns', 'fsync_count_ge_25ms')
            summaries[arm] = {m: arithmetic([v[m] for v in values]) for m in metrics}
            logs = [math.log(v['R']) if v['R'] is not None and v['R'] > 0 else None for v in values]
            summaries[arm]['log_R'] = arithmetic(logs)
            summaries[arm]['log_R']['excluded_rounds'] = [k for k, value in zip(complete, logs) if value is None]
            summaries[arm]['log_R']['sd'] = float(base.np.std([v for v in logs if v is not None], ddof=1)) if sum(v is not None for v in logs) >= 2 else None
            by_round = [(k, records[g[arm]]['R']) for k, g in complete.items()]
            positive = [(k, math.log(r)) for k, r in by_round if r is not None and r > 0]
            summaries[arm]['round_R'] = by_round
            summaries[arm]['log_R_slope_per_round'] = float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None
        unit = settled.unit_logs(groups, records, c, 'R', 'AMIN', 'B')
        cells[c] = dict(nominal=nominal(c), complete_rounds=list(complete), arms=summaries,
                        read_ratio_AMIN_B=ratio_summary(unit),
                        saturated_trials={arm: sum(records[g[arm]].get('saturated', False) for g in complete.values()) for arm in arms(c)})
        if c == F4:
            cells[c]['unavailable_metrics'] = dict(hold_share='no diag_trace in the frozen mixed specification',
                                                   free_share='no diag_trace in the frozen mixed specification',
                                                   largest_fsync_ns='no diag_trace in the frozen mixed specification',
                                                   fsync_count_ge_25ms='no diag_trace in the frozen mixed specification')
    violations = [dict(trial_id=tid, cell=specs[tid]['cell'], round=specs[tid]['triplet'],
                       arm=specs[tid]['arm'], flushes=v['flushes']) for tid, v in sorted(records.items())
                  if specs[tid]['cell'] in F1+[F3] and v['flushes'] > 0]
    flush_rounds = sorted({v['round'] for v in violations})
    hf1 = slope_test(groups, records)
    hf2 = {}
    maps = [complete_units(groups, records, c) for c in F1[::-1]]
    for arm in ARMS:
        rows, rounds, excluded = [], [], []
        for k in range(1, 11):
            if all(k in g for g in maps):
                row = [records[g[k][arm]]['R'] for g in maps]
                if all(v is not None for v in row):
                    rows.append(row); rounds.append(k)
                    continue
            excluded.append(k)
        hf2[arm] = dict(trend_test(rows), rounds=rounds, excluded_rounds=excluded,
                         cells=F1[::-1], predicted_ranks=[1, 2, 3, 4])
    for arm, p in base.holm({a: v['p'] for a, v in hf2.items()}).items():
        hf2[arm].update(holm_p=p, supported=None if p is None else p < .05, family='H-F2 (2)')
    hf3 = {c: ratio_summary(settled.unit_logs(groups, records, c, 'R', 'AMIN', 'B'), test=True) for c in F1[:3]}
    paced.adjust_family(hf3, 'H-F3 (3)')
    for v in hf3.values():
        v['supported'] = v['classification'] == 'equivalent'
    regions = {}
    for region in ('fast', 'valley', 'blocked'):
        stat = cells[F4]['arms']['A'][region+'_share']
        ci, threshold = stat['t_ci'], .10 if region == 'valley' else .20
        regions[region] = dict(stat, threshold=threshold, predicted='at_most' if region == 'valley' else 'at_least',
                               supported=None if ci is None else ci[1] <= threshold if region == 'valley' else ci[0] >= threshold)
    hf4 = dict(regions=regions, supported=all(v['supported'] for v in regions.values()) if
               all(v['supported'] is not None for v in regions.values()) else None,
               bucket_convention='lower_ns', boundary_trials=[tid for tid, v in sorted(records.items())
               if specs[tid]['cell'] == F4 and v['read_histogram']['boundary_buckets']])
    capacity = {}
    for arm in ARMS:
        values = cells[F3]['arms'][arm]['R']['values']
        off = float(base.np.median([v for v in values if v is not None])) if any(v is not None for v in values) else None
        comparisons = {}
        for c in F1:
            rows = []
            for k, g in complete_units(groups, records, c).items():
                v = records[g[arm]]
                predicted = off*v['free_share'] if off is not None else None
                undershoot = settled.positive_log(predicted, v['R'])
                rows.append(dict(round=k, R=v['R'], free_share=v['free_share'], R_off_times_free=predicted,
                                 undershoot_log=undershoot))
            comparisons[c] = dict(trials=rows, undershoot_log=arithmetic([v['undershoot_log'] for v in rows]),
                                  excluded_rounds=[v['round'] for v in rows if v['undershoot_log'] is None])
        means = [comparisons[c]['undershoot_log']['mean'] for c in F1]
        defined = [(w, v) for w, v in zip((183, 366, 549, 732), means) if v is not None]
        capacity[arm] = dict(F3_median_R=off, F1=comparisons,
                            undershoot_log_definition='ln(R_off * free_share / R)',
                            mean_log_undershoot_slope_per_write_per_sec=float(base.np.polyfit(*zip(*defined), 1)[0]) if len(defined) >= 2 else None,
                            mean_log_undershoot_range=max(v for _, v in defined)-min(v for _, v in defined) if defined else None,
                            inference='descriptive only; no constant-undershoot test or tolerance is preregistered')
    flush_comparison = {}
    g2, g1 = complete_units(groups, records, F2), complete_units(groups, records, F1[-1])
    for arm in ARMS:
        rounds = sorted(set(g2) & set(g1))
        rows = [dict(round=k, F2=records[g2[k][arm]], F1=records[g1[k][arm]]) for k in rounds]
        logs, excluded = [], []
        for row in rows:
            log = settled.positive_log(row['F2']['R'], row['F1']['R'])
            (excluded if log is None else logs).append(row['round'] if log is None else log)
        flush_comparison[arm] = dict(rounds=rounds, read_ratio_F2_F1=ratio_summary(dict(logs=logs, excluded_rounds=excluded)),
                                     hold_share={c: arithmetic([row[label]['hold_share'] for row in rows]) for c, label in ((F2, 'F2'), (F1[-1], 'F1'))},
                                     flushes={c: arithmetic([row[label]['flushes'] for row in rows]) for c, label in ((F2, 'F2'), (F1[-1], 'F1'))})
    histograms = {arm: {tid: v['read_histogram'] for tid, v in sorted(records.items())
                       if specs[tid]['cell'] == F4 and specs[tid]['arm'] == arm} for arm in base.ARMS}
    out.update(cells=cells, H_F1=hf1, H_F2=hf2, H_F3=hf3, H_F4=hf4,
               H_X=dict(supported=not violations, violations=violations, rounds=flush_rounds,
                        frozen_H_F1='H_F1 includes all complete rounds',
                        H_F1_without_flush_rounds=slope_test(groups, records, flush_rounds) if violations else None),
               capacity=capacity, F2_vs_F1=flush_comparison, F4_histograms=histograms,
               trials={tid: dict(cell=specs[tid]['cell'], round=specs[tid]['triplet'], arm=specs[tid]['arm'], **v) for tid, v in sorted(records.items())},
               outcome_exclusions=[dict(cell=c, metric='R_AMIN/B', rounds=cells[c]['read_ratio_AMIN_B']['excluded_rounds'])
                                   for c in CELLS if cells[c]['read_ratio_AMIN_B']['excluded_rounds']],
               notes=['Complete cell-round units supply cell summaries and paired tests; successful trials in dropped units remain visible.',
                      'No outcome-based exclusions. Nonpositive R/free share excludes only the affected logarithmic calculation; Page retains zeros.',
                      'H-F1 uses per-round OLS with intercept, then an arithmetic mean and two-sided 90% Student t CI. Both arms must pass; no multiplicity adjustment.',
                      'H-F2 Holm family=2; H-F3 Holm family=3; untestable hypotheses remain in their family as p=1.',
                      'H-F4 uses arithmetic mean trial shares, two-sided 95% Student t CIs, all three required. The frozen rule assigns histogram buckets to the region containing their lower bound; boundary buckets remain descriptive and do not suspend the decision.',
                      'Hold=(validate+wal_write+wal_sync+apply phase means)*commit_count/measured_window_ns. Phase arrival-window selection includes drain. Free share is not clamped.',
                      'F3 medians use complete units; capacity and undershoot constancy across rate are descriptive only. F1 at 732 writes/s has no equivalence prediction.',
                      'Largest fsync comes from the full phase histogram. >=25ms counts use retained traces and are undefined if traces were dropped.',
                      'Bootstrap sensitivity uses 10000 paired round resamples, seed 20261108, at the corresponding CI level.',
                      'Mixed F4 has no diag_trace and supplies no hold/free or largest-fsync duration; those quantities are unavailable, not inferred.'])
    return out


def tables(out):
    lines = ['# X-followup-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## '+title, '', '| '+' | '.join(headers)+' |',
                      '| '+' | '.join(['---']*len(headers))+' |'])
        lines.extend('| '+' | '.join(base.fmt(v).replace('|', '\\|') for v in row)+' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'settle_ms', 'plan_sha256', 'ledger_sha256',
           'runner_sha256', 'git_revision', 'platform', 'counts')])
    table('H_F1: primary, 90% Student t CI', ['Arm', 'n', 'Mean slope', 'CI', 'Bootstrap CI', 'Rounds', 'Excluded', 'Supported'],
          [(arm, v['n'], v['mean'], v['t_ci'], v['bootstrap_ci'], v['rounds'], v['excluded_rounds'], v['supported'])
           for arm, v in out['H_F1']['arms'].items()])
    table('H_F2: Page trend, Holm family of two', ['Arm', 'Result'], out['H_F2'].items())
    table('H_F3: read equivalence, Holm family of three', ['Cell', 'Result'], out['H_F3'].items())
    table('H_F4: arithmetic trial region shares, 95% Student t CI', ['Region', 'Result'], out['H_F4']['regions'].items())
    table('H_F4: frozen lower-bound decision and boundary description', ['Field', 'Value'], [(k, v) for k, v in out['H_F4'].items() if k != 'regions'])
    table('H_X: flush check and H-F1 sensitivity', ['Field', 'Value'], out['H_X'].items())
    table('Per-arm quantities', ['Cell', 'Arm', 'Metric', 'Summary'],
          [(c, arm, m, {k: x for k, x in v.items() if k != 'values'} if isinstance(v, dict) else v)
           for c, cs in out['cells'].items() for arm, metrics in cs['arms'].items() for m, v in metrics.items()])
    table('Read ratios (descriptive, including 732 writes/s)', ['Cell', 'Result'],
          [(c, cs['read_ratio_AMIN_B']) for c, cs in out['cells'].items()])
    table('R against F3 median times free share; undershoot across rate (descriptive)', ['Arm', 'Result'], out['capacity'].items())
    table('F2 against F1 at 732 writes/s', ['Arm', 'Result'], out['F2_vs_F1'].items())
    table('Unavailable diagnostics', ['Cell', 'Reason'],
          [(c, cs['unavailable_metrics']) for c, cs in out['cells'].items() if cs.get('unavailable_metrics')])
    table('Inter-attempt gaps', ['Field', 'Value'], out['gaps'].items())
    table('Failures and exclusions', ['Kind', 'Detail'], [(k, v) for k in
          ('missing_trials', 'failed_trials', 'failed_attempts_detail', 'pending_retry', 'dropped_units', 'outcome_exclusions') for v in out[k]])
    lines += ['## Full F4 histograms', '', 'Every trial histogram is in x_followup_results.json (F4_histograms).', '',
              '![F4 CDF](x_followup_f4_cdf.png)', '', '![Reads and free share](x_followup_reads_free_share.png)', '']
    lines.extend(out['notes'])
    return '\n'.join(lines)+'\n'


def figures(out, directory):
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    fig, ax = plt.subplots()
    for arm, style in zip(base.ARMS, ('-', '--', ':')):
        pooled = {}
        for h in out['F4_histograms'][arm].values():
            for b in h['buckets']:
                pooled[b['lower_ns']] = pooled.get(b['lower_ns'], 0)+b['count']
        nonzero = [(low, n) for low, n in sorted(pooled.items()) if n]
        total, acc, x, y = sum(n for _, n in nonzero), 0, [], []
        for low, n in nonzero:
            acc += n
            x.append(max(1, low)/1000); y.append(acc/total)
        ax.step(x, y, where='post', label=arm, linestyle=style)
    ax.set(xscale='log', xlabel='Read latency bucket lower bound (µs)', ylabel='Pooled measured-read CDF', ylim=(0, 1))
    ax.axvline(200, color='grey', linestyle=':'); ax.axvline(500, color='grey', linestyle=':')
    ax.legend(); fig.tight_layout()
    fig.savefig(directory/'x_followup_f4_cdf.png', metadata={'Software': 'x_followup_analyze'})
    plt.close(fig)
    fig, ax = plt.subplots()
    for arm, color in zip(ARMS, ('tab:blue', 'tab:orange')):
        for label, cell_set, marker in (('F1', F1, 'o'), ('F3 sync off', [F3], 'x')):
            rows = [v for v in out['trials'].values() if v['cell'] in cell_set and v['arm'] == arm]
            kwargs = dict(facecolors='none', edgecolors=color) if marker == 'o' else dict(color=color)
            ax.scatter([v['free_share'] for v in rows], [v['R'] for v in rows],
                       label=arm+' '+label, marker=marker, **kwargs)
        off = out['capacity'][arm]['F3_median_R']
        if off is not None:
            ax.plot([0, 1], [0, off], linestyle='--' if arm == 'AMIN' else ':', color=color, label=arm+' F3 median × free share')
    ax.set(xlabel='Free share', ylabel='Measured reads/s')
    ax.legend(); fig.tight_layout()
    fig.savefig(directory/'x_followup_reads_free_share.png', metadata={'Software': 'x_followup_analyze'})
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
        (args.output_dir/'x_followup_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir/'x_followup_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
