#!/usr/bin/env python3
"""X-settled-v1 preregistered analysis. Reads artifacts; never launches trials."""
import argparse
from datetime import datetime
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import sys


def sibling(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


paced = sibling('x_paced_analyze')
intervention = sibling('x_intervention_analyze')
tablecount, compaction, base = paced.tablecount, paced.compaction, paced.base
require, load_json = base.require, base.load_json
SEED, SETTLE_MS = 20261107, 20000
S1 = [f'S1_d{d:04d}_f20_{reads}' for d in (0, 200, 2000) for reads in ('closed', 'open')]
PACED = S1 + ['S2_d0200_w700_closed', 'S2_d0200_w700_open', 'S3_d0000_f80_closed']
SENTINELS = ['R_X1_r16_d0020', 'R_X1_r16_d2000', 'R_X2_r16_d0200', 'R_X3_r16',
             'R_C1_r16_d0200_on', 'R_T1_n256_r16_w0', 'R_I1_r16_d0200']
ENTRIES = PACED + SENTINELS
INTER = ENTRIES[-1]
CELLS = ENTRIES[:-1] + [INTER + '_second', INTER + '_inline']
ARMS = base.ARMS
PERMUTATIONS = (('A', 'AMIN', 'B'), ('A', 'B', 'AMIN'), ('AMIN', 'A', 'B'),
                ('AMIN', 'B', 'A'), ('B', 'A', 'AMIN'), ('B', 'AMIN', 'A'))
PHASES = ('lock_wait', 'validate', 'wal_write', 'wal_sync', 'apply', 'flush_check', 'total')
X4_METRICS = paced.METRICS + ('write_response_p95', 'read_response_p95') + tuple(
    'phase_' + p + '_p50' for p in PHASES) + (
    'largest_fsync_ns', 'fsync_count_ge_10ms', 'fsync_count_ge_25ms',
    'fsync_share_ge_10ms', 'fsync_share_ge_25ms', 'heartbeat_count_ge_10ms',
    'gc_pause_total_ns', 'gc_count', 'write_excess_ns')


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    if cell in PACED:
        if cell.startswith('S1_'):
            source = 'P1_' + cell[3:]
        elif cell.startswith('S2_'):
            source = 'P1_d0200_f20_' + cell.rsplit('_', 1)[1]
        else:
            source = 'P1_d0000_f80_closed'
        out = dict(paced.nominal(source), diag_trace=True)
        if cell.startswith('S2_'):
            out['paced_write_rate_per_sec'] = 700
        return out
    original = cell[2:]
    if original.startswith('C1_'):
        out = compaction.nominal(original)
    elif original.startswith('T1_'):
        out = tablecount.nominal(original)
    elif original.startswith('I1_'):
        parent, condition = original.rsplit('_', 1)
        out = dict(intervention.nominal(parent), inline_flush_check=condition == 'inline',
                   compaction_trigger_interval_ms=0)
    else:
        out = dict(intervention.nominal('I1_r16_d0200'), **base.cell_config(original))
        out.update(block=original[:2], workload_id='X-readcost', compaction_trigger_interval_ms=0)
        if original.startswith('X2_'):
            out.update(writer_concurrency=0, memtable_threshold_bytes=64 << 20,
                       publication_events=20, publication_mode='physical')
        elif original.startswith('X3_'):
            out['memtable_threshold_bytes'] = 16 << 10
    return dict(out, diag_trace=False)


OPTIONAL_ZERO = ('open_loop_rate_per_sec', 'paced_write_rate_per_sec', 'max_lateness_ms',
                 'read_io_delay_us', 'publication_events')


def check_spec(actual, expected, context):
    require(isinstance(actual, dict), f'invalid spec {context}')
    for key, value in expected.items():
        if key != 'data_dir':
            default = False if key in ('diag_trace', 'inline_flush_check') else 0 if key in OPTIONAL_ZERO else None
            require(actual.get(key, default) == value, f'{context}: {key} disagrees with plan')
    for key in OPTIONAL_ZERO + ('inline_flush_check', 'diag_trace', 'publication_mode'):
        default = False if key in ('diag_trace', 'inline_flush_check') else '' if key == 'publication_mode' else 0
        require(actual.get(key, default) == expected.get(key, default), f'unplanned treatment {context}: {key}')
    require(actual.get('flush_trigger_interval_ms', 0) == 0, f'unplanned flush schedule {context}')


def entry_id(cell):
    return INTER if cell.startswith(INTER + '_') else cell


def run_keys(entry):
    return [(condition, arm) for condition in ('second', 'inline') for arm in ('A', 'AMIN')] if entry == INTER else list(ARMS)


def run_key(spec):
    return (spec['cell'].rsplit('_', 1)[1], spec['arm']) if entry_id(spec['cell']) == INTER else spec['arm']


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-settled-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2**64, 'invalid master seed')
    require(type(plan['settle_ms']) is int and plan['settle_ms'] == SETTLE_MS, 'invalid settle_ms')
    require(len(plan['cells']) == 17 and [c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        require(c['spec']['cell'] == c['id'], 'cell spec identity mismatch')
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 470, 'plan must contain 470 trials')
    units, seen = {}, set()
    for index, s in enumerate(plan['trials']):
        c, k, arm, tid = s['cell'], s['triplet'], s['arm'], s['trial_id']
        require(c in CELLS and type(k) is int and 1 <= k <= (5 if entry_id(c) == INTER else 10), 'invalid run')
        entry = entry_id(c)
        require(run_key(s) in run_keys(entry) and tid == f'{c}_t{k:02d}_{arm}' and tid not in seen, 'invalid/duplicate run')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        seed = hashlib.sha256(f"{plan['master_seed']}|{entry}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(seed[:8], 'big', signed=True), 'incorrect paired seed')
        require(s['runlist_position'] == index + 1, 'invalid runlist position')
        require(k == (index // 49 + 1 if index < 245 else 6 + (index - 245) // 45), 'invalid round interleaving')
        unit = units.setdefault((entry, k), [])
        unit.append((index, s))
    require(len(units) == 155, 'missing cell-round units')
    for (entry, k), rows in units.items():
        require(len(rows) == len(run_keys(entry)) and {run_key(s) for _, s in rows} == set(run_keys(entry)), 'incomplete unit')
        require([i for i, _ in rows] == list(range(rows[0][0], rows[0][0] + len(rows))), 'noncontiguous unit')
        require([s['order_position'] for _, s in rows] == list(range(1, len(rows)+1)), 'invalid order positions')
        if entry != INTER:
            require(tuple(s['arm'] for _, s in rows) == PERMUTATIONS[(k + ENTRIES.index(entry)) % 6], 'incorrect arm order')
    return {s['trial_id']: s for s in plan['trials']}, {
        key: {run_key(s): s['trial_id'] for _, s in rows} for key, rows in units.items()}


def stall_outcomes(r):
    d, p = r['stall_diagnostics'], r['paced_load']
    phases = d['phase_histograms']
    out = {'phase_' + name + '_p50': phases[name]['stats']['p50'] if phases[name]['stats']['sample_count'] else None
           for name in PHASES}
    out['largest_fsync_ns'] = phases['wal_sync']['stats']['max'] if phases['wal_sync']['stats']['sample_count'] else None
    require(d['window_ns'] > 0 and d['commit_count'] == phases['total']['stats']['sample_count'], 'invalid phase window/count')
    # Every >=10ms fsync implies a >=3ms total commit, so the trace retains it
    # unless capacity was exhausted. Never infer threshold counts from p50s or
    # rounded histogram buckets; dropped traces make these quantities undefined.
    fsyncs = [c['WalSyncDoneNs'] - c['WalWriteDoneNs'] for c in d['commits']]
    for threshold in (10, 25):
        values = [v for v in fsyncs if v >= threshold * 1000000]
        out[f'fsync_count_ge_{threshold}ms'] = len(values) if not d['commits_dropped'] else None
        out[f'fsync_share_ge_{threshold}ms'] = sum(values) / d['window_ns'] if not d['commits_dropped'] else None
    gaps = [g for g in d['heartbeat_gaps'] if g['length_ns'] >= 10000000]
    out.update(heartbeat_count_ge_10ms=len(gaps) if not d['heartbeat_dropped'] else None,
               gc_pause_total_ns=d['runtime']['gc_pause_total_delta_ns'], gc_count=d['runtime']['gc_count_delta'])
    h = p['write_response_nanos']
    out['write_excess_ns'] = (h['mean'] - h['p50']) * h['sample_count'] if h['sample_count'] else None
    out['stall_detail'] = dict(phase_histograms=phases, commits_dropped=d['commits_dropped'],
                              heartbeat_dropped=d['heartbeat_dropped'], heartbeat_gaps_ge_10ms=gaps,
                              runtime=d['runtime'], slowest_reads=d['slowest_reads'],
                              commit_window='arrivals inside nominal window, including their drain')
    return out


def ledger_rows(root):
    rows = [json.loads(line) for line in (Path(root) / 'ledger.jsonl').read_text(encoding='utf-8').splitlines()]
    require(all(type(e['attempt']) is int and 1 <= e['attempt'] <= 2 for e in rows), 'invalid attempt/more than one retry')
    return rows


def gap_summary(rows, settle_ms):
    previous, gaps, flags, unpaired = None, [], [], []
    for e in rows:
        if e['event'] == 'STARTED':
            if previous is None:
                unpaired.append(dict(trial_id=e['trial_id'], attempt=e['attempt'], reason='no preceding FINISHED timestamp'))
            else:
                start = datetime.fromisoformat(e['start_utc'].replace('Z', '+00:00'))
                end = datetime.fromisoformat(previous['end_utc'].replace('Z', '+00:00'))
                gap = (start - end).total_seconds() * 1000
                require(gap >= 0, 'negative inter-attempt gap')
                v = dict(trial_id=e['trial_id'], attempt=e['attempt'], previous_trial_id=previous['trial_id'], gap_ms=gap,
                         below_settle=gap < settle_ms, first_attempt=e['attempt'] == 1)
                gaps.append(v)
                if v['first_attempt'] and v['below_settle']:
                    flags.append(v)
            previous = None
        elif e['event'] == 'FINISHED':
            previous = e
    values = [v['gap_ms'] for v in gaps]
    return dict(n=len(values), min_ms=min(values) if values else None,
                median_ms=float(base.np.median(values)) if values else None,
                max_ms=max(values) if values else None, mean_ms=sum(values)/len(values) if values else None,
                gaps=gaps, first_attempt_below_settle=flags, unpaired_starts=unpaired,
                first_trial_pause='not observable from ledger; verified by driver unit tests')


def load_campaign(root, allow_partial=False):
    root = Path(root)
    plan = load_json(root / 'plan.json')
    specs, groups = validate_plan(plan)
    rows, ledger = ledger_rows(root), {}
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
    meta = dict(campaign_id=plan['campaign_id'], master_seed=plan['master_seed'], settle_ms=plan['settle_ms'],
                plan_sha256=plan_hash, ledger_sha256=hashlib.sha256((root / 'ledger.jsonl').read_bytes()).hexdigest(),
                runner_sha256=runner, git_revision=rev, platform=platform, partial=bool(missing),
                bootstrap_seed=SEED, bootstrap_resamples=10000,
                counts=dict(planned=len(specs), attempted=len(ledger), attempts=sum(map(len, ledger.values())),
                            successful=len(records), failed=len(failed), failed_attempts=len(failures),
                            missing=len(missing), dropped_units=len(dropped)),
                missing_trials=missing, failed_trials=failed, failed_attempts_detail=failures,
                pending_retry=pending, dropped_units=dropped, gaps=gap_summary(rows, plan['settle_ms']))
    return specs, groups, records, meta


def positive_log(x, y):
    return math.log(x) - math.log(y) if x is not None and y is not None and x > 0 and y > 0 else None


def unit_logs(groups, records, entry, metric, a, b, intervention_source=False):
    logs, excluded, adjusted, rounds = [], [], [], []
    for (cell, k), g in sorted(groups.items()):
        if cell != entry or any(tid not in records for tid in g.values()):
            continue
        x, y = records[g[a]].get(metric), records[g[b]].get(metric)
        # Intervention's original analysis has no pseudocount. Other campaigns
        # apply +0.1 to every arm in a triplet containing zero W.
        if metric == 'W' and not intervention_source and any(records[tid].get('W') == 0 for tid in g.values()):
            x, y = (x + .1 if x is not None else None), (y + .1 if y is not None else None)
            adjusted.append(k)
        value = positive_log(x, y)
        if value is None:
            excluded.append(k)
        else:
            logs.append(value); rounds.append(k)
    return dict(logs=logs, rounds=rounds, excluded_rounds=excluded, zero_adjusted_W_rounds=adjusted)


def ratio_summary(unit, test=False, level=.95):
    v = base.tost(unit['logs'], seed=SEED) if test else base.paired_stats(unit['logs'], level, seed=SEED)
    return dict(v, **{k: x for k, x in unit.items() if k != 'logs'}, undefined_n=len(unit['excluded_rounds']))


def ci_class(ci, margin=base.MARGIN):
    if ci is None:
        return 'inconclusive'
    if ci[0] > -margin and ci[1] < margin:
        return 'equivalent'
    if ci[1] < -margin or ci[0] > margin:
        return 'not_equivalent'
    return 'inconclusive'


def contradicted_class(published_class, new_ci):
    new_class = ci_class(new_ci)
    return ((published_class == 'equivalent' and new_class == 'not_equivalent') or
            (published_class == 'not_equivalent' and new_class == 'equivalent'))


def welch(new, original, margin):
    out = dict(n_new=len(new), n_original=len(original), margin_log=margin,
               mean_difference_log=None, df=None, t_ci_log=None, classification='inconclusive')
    if not new or not original:
        return out
    delta = float(base.np.mean(new) - base.np.mean(original))
    out['mean_difference_log'] = delta
    if min(len(new), len(original)) < 2:
        return out
    a = float(base.np.var(new, ddof=1)) / len(new)
    b = float(base.np.var(original, ddof=1)) / len(original)
    df = (a+b)**2 / (a*a/(len(new)-1) + b*b/(len(original)-1)) if a+b else len(new)+len(original)-2
    half = float(base.stats.t.ppf(.95, df)) * math.sqrt(a+b)
    ci = [delta-half, delta+half]
    cls = ci_class(ci, margin)
    out.update(df=df, standard_error=math.sqrt(a+b), t_ci_log=ci,
               classification={'equivalent': 'unchanged', 'not_equivalent': 'changed', 'inconclusive': 'inconclusive'}[cls],
               method='Welch-Satterthwaite, two-sided 90% Student t CI')
    return out


def mechanism_band(values, arm):
    out = compaction.summary(values)
    ci = out['t_ci_95']
    supported = None if ci is None or out['undefined_n'] else (
        1000000 <= ci[0] and ci[1] <= 3000000 if arm == 'A' else ci[1] < 500000)
    return dict(out, band_ns=[1000000, 3000000] if arm == 'A' else [None, 500000],
                upper_exclusive=arm != 'A', supported=supported)


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    require(r['operations']['validation_error_count'] == 0 and r['operations'].get('error_ops_count', 0) == 0
            and r['timestamps']['measured_window_ns'] > 0, 'invalid successful window')
    require(r['lsm_diagnostics'].get('compaction_errors', 0) == 0, 'compaction engine fault')
    if s['block'] == 'X4':
        out = paced.trial_outcomes(t)
        out.update(stall_outcomes(r))
        for name in ('write_response', 'read_response'):
            h = r['paced_load'].get(name + '_nanos')
            out[name + '_p95'] = h.get('p95') if h and h['sample_count'] >= 20 else None
        if not s.get('open_loop_rate_per_sec', 0):
            out['not_applicable'].append('read_response_p95')
    elif s['cell'].startswith('R_C1_'):
        out = compaction.trial_outcomes(t)
    elif s['cell'].startswith('R_T1_'):
        out = tablecount.trial_outcomes(t)
    elif s['cell'].startswith(INTER):
        out = dict(intervention.trial_outcomes(t, s), read_p99=r['latency_read_nanos'].get('p99'), not_applicable=[])
    else:
        adjusted = dict(t, cell=s['cell'][2:])
        out = base.outcomes(adjusted)
    if s['block'] != 'X4':
        out['publications'] = r['lsm_diagnostics']['publication_events_realized_in_window']
    for key, value in out.items():
        if key not in ('not_applicable', 'stall_detail', 'late_latency', 'recorded_throughput',
                       'write_response_p99_label') and not isinstance(value, (dict, list, bool)):
            if key == 'write_excess_ns':
                require(value is None or math.isfinite(value), 'invalid write excess')
            else:
                base.number(value, key)
    return out


SOURCES = {
    'readcost': (base, 'analysis', 'x_readcost_results.json'),
    'compaction': (compaction, 'analysis_compaction', 'x_compaction_results.json'),
    'tablecount': (tablecount, 'analysis_tablecount', 'x_tablecount_results.json'),
    'intervention': (intervention, 'analysis_intervention', 'x_intervention_results.json'),
    'paced': (paced, 'analysis_paced', 'x_paced_results.json'),
}


def load_originals(directories, allow_partial=False):
    originals = {}
    for name, (module, analysis_dir, filename) in SOURCES.items():
        root = Path(directories[name])
        specs, groups, records, meta = module.load_campaign(root, allow_partial)
        # The intervention helper defines W and R using counts / 10s and
        # intentionally has no latency comparison. Extract its read p99 from
        # the same latest successful artifacts, never from a stale attempt.
        if name in ('intervention', 'paced'):
            latest = {}
            for e in ledger_rows(root):
                latest[e['trial_id']] = max(latest.get(e['trial_id'], 0), e['attempt'])
            enriched = {}
            for tid, v in records.items():
                raw = load_json(root / 'trials' / f'{tid}_a{latest[tid]}.json')['result']
                enriched[tid] = dict(v)
                if name == 'intervention':
                    enriched[tid]['read_p99'] = raw['latency_read_nanos'].get('p99')
                else:
                    for field in ('read_response', 'write_response'):
                        h = raw['paced_load'].get(field+'_nanos')
                        enriched[tid][field+'_p95'] = h.get('p95') if h and h['sample_count'] >= 20 else None
            records = enriched
        published_path = root.parent / analysis_dir / filename
        published = load_json(published_path) if published_path.exists() else None
        if published:
            require(published['plan_sha256'] == meta['plan_sha256'] and published['ledger_sha256'] == meta['ledger_sha256'],
                    f'{name} published summary refers to different plan/ledger')
        originals[name] = dict(specs=specs, groups=groups, records=records, meta=meta, published=published,
                               published_path=str(published_path),
                               published_sha256=hashlib.sha256(published_path.read_bytes()).hexdigest() if published else None)
    return originals


def sentinel_metrics():
    rows = []
    for cell in SENTINELS:
        source = ('compaction' if cell.startswith('R_C1') else 'tablecount' if cell.startswith('R_T1')
                  else 'intervention' if cell == INTER else 'readcost')
        if cell == INTER:
            for arm in ('A', 'AMIN'):
                for metric in ('W', 'R', 'read_p99'):
                    rows.append((cell, source, metric, ('inline', arm), ('second', arm)))
            continue
        metrics = [('W', 'AMIN', 'B'), ('R', 'AMIN', 'B')]
        if cell.startswith('R_X1'):
            metrics = [('W', 'A', 'AMIN'), ('R', 'A', 'AMIN')] + metrics
        elif cell.startswith('R_X2'):
            metrics = [('R', 'AMIN', 'B'), ('publication_lock_wait_p99', 'AMIN', 'B')]
        elif source == 'compaction':
            metrics.append(('commit_wait_p99', 'AMIN', 'B'))
        elif source == 'tablecount':
            metrics = [('R', 'AMIN', 'B')]
        rows.extend((cell, source, m, a, b) for m, a, b in metrics)
    return rows


def published_ratio(original, source, cell, metric, a, b):
    published = original['published']
    if published is None:
        return None, None
    cs = published['cells'][cell]
    if source == 'intervention':
        return cs['ratios_inline_over_second'][a[1]].get(metric), None
    value = cs['ratios'].get(metric, {}).get(a + '/' + b)
    eq = None
    if (a, b) == ('AMIN', 'B'):
        if source == 'readcost':
            eq = published['H2'].get(metric, {}).get(cell)
        else:
            eq = cs.get('equivalence', {}).get(metric)
    if value is None and eq is not None:
        value = eq
    return value, eq.get('classification') if eq else None


def sentinel_analysis(groups, records, originals):
    rows, checks, triggers = [], [], []
    for cell, source, metric, a, b in sentinel_metrics():
        original = originals[source]
        old_cell = cell[2:]
        new = unit_logs(groups, records, cell, metric, a, b, source == 'intervention')
        old = unit_logs(original['groups'], original['records'], old_cell, metric, a, b, source == 'intervention')
        old_stat, new_stat = ratio_summary(old), ratio_summary(new, level=.90)
        published, published_class = published_ratio(original, source, old_cell, metric, a, b)
        if published is None:
            status = 'no_published_ratio' if original['published'] else 'published_summary_missing'
            delta = None
        else:
            delta = (old_stat['mean_log'] - published['mean_log']
                     if old_stat['mean_log'] is not None and published['mean_log'] is not None else None)
            status = 'PASS' if old_stat['n'] == published['n'] and (
                abs(delta) <= 1e-12 if delta is not None else old_stat['mean_log'] == published['mean_log']) else 'MISMATCH'
        pair = f'{a}/{b}' if source != 'intervention' else f'inline/second ({a[1]})'
        checks.append(dict(source=source, cell=old_cell, metric=metric, pair=pair, status=status,
                           reproduced_n=old_stat['n'], reproduced_mean_log=old_stat['mean_log'],
                           published_n=published['n'] if published else None,
                           published_mean_log=published['mean_log'] if published else None, difference=delta))
        margin = base.MARGIN if metric in ('W', 'R') else math.log(1.5)
        comparison = welch(new['logs'], old['logs'], margin)
        new_class = ci_class(new_stat['t_ci_log'])
        contradicted = contradicted_class(published_class, new_stat['t_ci_log'])
        rerun = comparison['classification'] == 'changed' or contradicted
        row = dict(cell=cell, source=source, metric=metric, pair=pair, original=old_stat, new=new_stat,
                   comparison=comparison, published_class=published_class,
                   new_unadjusted_ci_class=new_class if published_class else None,
                   classification_contradicted=contradicted, rerun_trigger=rerun)
        rows.append(row)
        if rerun:
            triggers.append(dict(source=source, cell=cell, metric=metric, pair=pair,
                                 changed=comparison['classification'] == 'changed', contradicted=contradicted))
    return rows, checks, triggers


def reproduction_check(original, source, cell, metric, a, b, stat):
    pub, _ = published_ratio(original, source, cell, metric, a, b)
    delta = stat['mean_log'] - pub['mean_log'] if pub and stat['mean_log'] is not None and pub['mean_log'] is not None else None
    status = ('published_summary_missing' if original['published'] is None else 'no_published_ratio' if pub is None else
              'PASS' if stat['n'] == pub['n'] and (abs(delta) <= 1e-12 if delta is not None else stat['mean_log'] == pub['mean_log'])
              else 'MISMATCH')
    return dict(source=source, cell=cell, metric=metric, pair=a+'/'+b, status=status,
                reproduced_n=stat['n'], reproduced_mean_log=stat['mean_log'],
                published_n=pub['n'] if pub else None, published_mean_log=pub['mean_log'] if pub else None,
                difference=delta)


RATIO_METRICS = ('W', 'R', 'read_service_p50', 'read_service_p99', 'read_response_p50', 'read_response_p95',
                 'read_response_p99', 'write_service_p50', 'write_service_p99', 'write_response_p50',
                 'write_response_p95', 'write_response_p99', 'commit_wait_p50', 'commit_wait_p99')


def analyze(root, directories=None, allow_partial=False, originals=None):
    specs, groups, records, out = load_campaign(root, allow_partial)
    if originals is None:
        require(directories is not None, 'original campaign directories are required')
        originals = load_originals(directories, allow_partial)
    cells, units, exclusions = {}, {}, []
    for c in CELLS:
        entry = entry_id(c)
        complete = {k: g for (cell, k), g in sorted(groups.items()) if cell == entry and all(tid in records for tid in g.values())}
        arms = ('A', 'AMIN') if entry == INTER else ARMS
        cs = dict(nominal=nominal(c), complete_rounds=list(complete), arms={}, ratios={},
                  saturated_trials={}, saturated_successful_trials={})
        for arm in arms:
            key = (c.rsplit('_', 1)[1], arm) if entry == INTER else arm
            values = [records[g[key]] for g in complete.values()]
            metrics = X4_METRICS if c in PACED else sorted({m for v in values for m, x in v.items()
                                                         if (x is None or isinstance(x, (int, float))) and not isinstance(x, bool)})
            cs['arms'][arm] = {}
            for m in metrics:
                applicable = [v.get(m) for v in values if m not in v.get('not_applicable', [])]
                stat = dict(compaction.summary(applicable), not_applicable_n=len(values)-len(applicable))
                if m == 'write_response_p99':
                    censored = [k for k, g in complete.items() if records[g[key]]['saturated']]
                    stat.update(censored_rounds=censored, censored_n=len(censored), true_p99_summary=not censored,
                                label='observed completion p99; saturated trials censored from below')
                cs['arms'][arm][m] = stat
            if c in PACED:
                cs['saturated_trials'][arm] = sum(v['saturated'] for v in values)
                cs['saturated_successful_trials'][arm] = sum(v['saturated'] for tid, v in records.items()
                                                            if specs[tid]['cell'] == c and specs[tid]['arm'] == arm)
        if entry != INTER:
            for m in RATIO_METRICS:
                if c not in PACED and m not in ('W', 'R', 'commit_wait_p50', 'commit_wait_p99'):
                    continue
                if not cs['nominal']['writer_concurrency'] and (m == 'W' or m.startswith('commit_')):
                    continue
                if c in PACED and c.endswith('_closed') and m.startswith('read_response_'):
                    continue
                cs['ratios'][m] = {}
                for a, b in base.PAIRS:
                    unit = unit_logs(groups, records, entry, m, a, b)
                    units[c, m, a, b] = unit
                    stat = ratio_summary(unit)
                    if m == 'write_response_p99':
                        censored = [k for k, g in complete.items() if any(records[g[arm]]['saturated'] for arm in (a, b))]
                        stat.update(censored_rounds=censored, true_p99_ratio=not censored,
                                    label='completion-only p99 ratio; censored components need not bound the true ratio')
                    cs['ratios'][m][a+'/'+b] = stat
                    exclusions.extend(dict(cell=c, metric=m, pair=a+'/'+b, round=k, reason='missing/nonpositive log pair')
                                      for k in unit['excluded_rounds'])
        cells[c] = cs
    h_m1 = {}
    for reads in ('closed', 'open'):
        names = [f'S1_d{d:04d}_f20_{reads}' for d in (0, 200, 2000)]
        maps = [dict(zip(units[c, 'write_response_p50', 'A', 'AMIN']['rounds'],
                         units[c, 'write_response_p50', 'A', 'AMIN']['logs'])) for c in names]
        common = sorted(set.intersection(*(set(d) for d in maps)))
        h_m1[reads] = dict(paced.page_test([[math.exp(d[k]) for d in maps] for k in common]),
                          rounds=common, levels=[0, 200, 2000], predicted_ranks=[1, 2, 3], alpha=.05)
    for name, p in base.holm({k: v['p'] for k, v in h_m1.items()}).items():
        h_m1[name].update(holm_p=p, supported=p is not None and p < .05)
    def family(requests, name, predicted):
        result = {key: ratio_summary(units[c, m, a, b], test=True) for key, c, m, a, b in requests}
        paced.adjust_family(result, name)
        for v in result.values():
            ci = v['t_ci_log']
            v.update(predicted=predicted, numerator_higher=bool(ci and ci[0] > base.MARGIN))
            v['supported'] = (v['classification'] == 'equivalent' if predicted == 'equivalent' else
                              v['classification'] == 'not_equivalent' and
                              (v['numerator_higher'] if predicted == 'higher' else v['numerator_lower']))
        return result
    h_m2 = family([(c, c, 'write_response_p50', 'A', 'AMIN') for c in S1 if 'd0000' not in c], 'H-M2', 'higher')
    h_w = family([(c, c, 'W', 'A', 'AMIN') for c in PACED if c.startswith('S2_')], 'H-W', 'lower')
    h_r = family([(m, 'S3_d0000_f80_closed', m, 'AMIN', 'B') for m in ('R', 'W')], 'H-R', 'equivalent')
    mechanism = {}
    c = 'S1_d2000_f20_closed'
    complete = [(k, g) for (entry, k), g in sorted(groups.items()) if entry == c and all(tid in records for tid in g.values())]
    for arm in ('A', 'AMIN'):
        for phase in ('lock_wait', 'flush_check'):
            mechanism[arm+'_'+phase] = mechanism_band([records[g[arm]]['phase_'+phase+'_p50'] for _, g in complete], arm)
    h_m3 = dict(cell=c, rounds=[k for k, _ in complete], bands=mechanism,
                verdict=intervention.verdict([v['supported'] for v in mechanism.values()]))
    h_s = {}
    for c in S1:
        for arm in ARMS:
            values = cells[c]['arms'][arm]['largest_fsync_ns']['values']
            defined = [v for v in values if v is not None]
            median = float(base.np.median(defined)) if defined else None
            h_s[c+'_'+arm] = dict(cell=c, arm=arm, n=len(defined), undefined_n=len(values)-len(defined),
                                  median_largest_fsync_ns=median, threshold_ns=40000000,
                                  supported=median < 40000000 if median is not None and len(defined) == len(values) else None)
    sentinel, checks, triggers = sentinel_analysis(groups, records, originals)
    cross = []
    original = originals['paced']
    for c in S1 + ['S3_d0000_f80_closed']:
        old_cell = 'P1_' + c[3:]
        for m in RATIO_METRICS:
            if m not in cells[c]['ratios']:
                continue
            for a, b in base.PAIRS:
                old = ratio_summary(unit_logs(original['groups'], original['records'], old_cell, m, a, b))
                new = cells[c]['ratios'][m][a+'/'+b]
                cross.append(dict(cell=c, original_cell=old_cell, metric=m, pair=a+'/'+b, new=new, original=old,
                                  label='across-campaign, descriptive; diag_trace and date/binary/seeds also differ'))
                checks.append(reproduction_check(original, 'paced', old_cell, m, a, b, old))
    drift, scatter = {}, []
    for c, cs in cells.items():
        drift[c] = {}
        for arm in cs['arms']:
            key = (c.rsplit('_', 1)[1], arm) if entry_id(c) == INTER else arm
            rows = [(k, records[groups[entry_id(c), k][key]]['R']) for k in cs['complete_rounds']]
            positive = [(k, math.log(v)) for k, v in rows if v is not None and v > 0]
            slope = float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None
            drift[c][arm] = dict(round_R=rows, log_R_slope_per_round=slope)
    for tid, v in sorted(records.items()):
        if specs[tid]['cell'] == 'S3_d0000_f80_closed':
            scatter.append(dict(trial_id=tid, arm=specs[tid]['arm'], round=specs[tid]['triplet'], R=v['R'],
                                log_R=math.log(v['R']) if v['R'] and v['R'] > 0 else None,
                                write_excess_ns=v['write_excess_ns'],
                                definition='write response (mean - p50) * in-window sample_count'))
    publications = {}
    for c in CELLS:
        if c.startswith('R_X2_'):
            original = originals['readcost']
            complete = [g for (entry, _), g in sorted(original['groups'].items()) if entry == c[2:]
                        and all(tid in original['records'] for tid in g.values())]
            publications[c] = dict(new={arm: cells[c]['arms'][arm].get('publications') for arm in ARMS},
                                   original={arm: compaction.summary([original['records'][g[arm]]['publications']
                                                                     for g in complete]) for arm in ARMS})
    out.update(cells=cells, H_M1=h_m1, H_M2=h_m2, H_M3=h_m3, H_W=h_w, H_R=h_r,
               H_S=dict(trials=h_s, verdict=intervention.verdict([v['supported'] for v in h_s.values()])),
               H_C=sentinel, rerun_triggers=triggers, reproduction_checks=checks,
               originals={name: dict(provenance=v['meta'], published_path=v['published_path'],
                                     published_sha256=v['published_sha256']) for name, v in originals.items()},
               across_campaign_paced=cross, log_reads_vs_write_excess=scatter, round_trend_R=drift,
               publication_counts=publications, outcome_exclusions=exclusions,
               trials={tid: dict(cell=specs[tid]['cell'], round=specs[tid]['triplet'], arm=specs[tid]['arm'], **v)
                       for tid, v in sorted(records.items())},
               notes=['No pooling. Complete triplets or complete four-run intervention rounds supply summaries and paired tests.',
                      'H-M1 Holm family=2; H-M2=4; H-W=2; H-R=2. Sentinels have no Holm correction.',
                      'H-C: independent new-minus-original mean paired logs, 90% Welch-Satterthwaite Student t CI. Published equivalence classes alone permit the unadjusted 90% paired-CI contradiction trigger.',
                      'W zero rules follow the original source for sentinels; X4 follows X-paced (+0.1 to all arms of the zero-containing triplet). No latency pseudocount.',
                      'Saturated completion p99s are censored from below. Ratios involving them need not bound true p99 ratios.',
                      'Phase p50s and largest fsync use whole phase histograms, not the selected slow/first-commit traces. Phase histograms select commits by arrival in the nominal window and include their drain.',
                      'Fsync >=10/25ms counts and sums use retained traces; each such commit necessarily exceeds the 3ms retention threshold. Dropped traces make counts/shares undefined. Shares are full fsync durations divided by nominal window.',
                      'Heartbeat and GC details retain their recorded coverage and loss flags. Undefined diagnostics remain visible and counted.',
                      'Log reads means natural log of recorded R, plotted/tabulated against write response excess (mean-p50)*count. This is descriptive, with no regression test.',
                      'S1/S3 vs paced comparisons are across-campaign and descriptive, with no new hypothesis. The pause is confounded with date, binary, seeds and X4 diagnostic instrumentation.',
                      'Published summaries are located in sibling analysis directories, as in the supplied X_READCOST layout. Missing published ratios/classes are reported, never invented.',
                      'First-trial pause cannot be inferred from ledger gaps; driver tests cover it. Gap flags concern first attempts below settle_ms; retry gaps are also listed.',
                      'No trials or reruns are launched by this analysis. Rerun triggers are reports for the operator.'])
    return out


def tables(out):
    lines = ['# X-settled-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## '+title, '', '| '+' | '.join(headers)+' |', '| '+' | '.join(['---']*len(headers))+' |'])
        lines.extend('| '+' | '.join(base.fmt(v).replace('|', '\\|') for v in row)+' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'settle_ms', 'plan_sha256', 'ledger_sha256', 'runner_sha256', 'git_revision', 'platform', 'counts')])
    for key in ('H_M1', 'H_M2', 'H_W', 'H_R'):
        table(key, ['Test', 'Result'], out[key].items())
    table('H-M3: mechanism', ['Field', 'Value'], out['H_M3'].items())
    table('H-S: fsync protocol check', ['Field', 'Value'], out['H_S'].items())
    table('H-C: sentinels, no Holm correction', ['Result'], [(v,) for v in out['H_C']])
    table('Rerun triggers (reports only)', ['Trigger'], [(v,) for v in out['rerun_triggers']])
    table('Original reproduction checks', ['Check'], [(v,) for v in out['reproduction_checks']])
    table('Inter-attempt gaps', ['Field', 'Value'], out['gaps'].items())
    table('Per-arm quantities (latencies in ns)', ['Cell', 'Arm', 'Metric', 'Summary'],
          [(c, a, m, v) for c, cs in out['cells'].items() for a, metrics in cs['arms'].items() for m, v in metrics.items()])
    table('Paired ratios (95% log t CIs)', ['Cell', 'Metric', 'Pair', 'Result'],
          [(c, m, p, v) for c, cs in out['cells'].items() for m, pairs in cs['ratios'].items() for p, v in pairs.items()])
    table('Saturated trial counts', ['Cell', 'Complete rounds', 'All successful trials'],
          [(c, cs['saturated_trials'], cs['saturated_successful_trials']) for c, cs in out['cells'].items() if c in PACED])
    table('Publication in-window counts', ['Cell', 'Counts'], out['publication_counts'].items())
    table('Reads by round and drift', ['Cell', 'Arm', 'Result'],
          [(c, a, v) for c, arms in out['round_trend_R'].items() for a, v in arms.items()])
    table('S3 log reads versus write excess', ['Point'], [(v,) for v in out['log_reads_vs_write_excess']])
    table('Across-campaign paced comparisons (descriptive)', ['Comparison'], [(v,) for v in out['across_campaign_paced']])
    table('Trial diagnostics, censoring, heartbeat and GC', ['Trial', 'Result'], out['trials'].items())
    table('Failures and exclusions', ['Kind', 'Detail'], [(k, v) for k in
          ('missing_trials', 'failed_trials', 'failed_attempts_detail', 'pending_retry', 'dropped_units', 'outcome_exclusions') for v in out[k]])
    lines.extend(out['notes'])
    return '\n'.join(lines)+'\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--data-dir', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    for name in SOURCES:
        parser.add_argument('--'+name+'-dir', type=Path, required=True)
    parser.add_argument('--allow-partial', action='store_true')
    args = parser.parse_args()
    try:
        out = analyze(args.data_dir, {name: getattr(args, name+'_dir') for name in SOURCES}, args.allow_partial)
        encoded = json.dumps(out, sort_keys=True, indent=2, allow_nan=False)+'\n'
        args.output_dir.mkdir(parents=True, exist_ok=True)
        (args.output_dir / 'x_settled_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir / 'x_settled_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
        for check in out['reproduction_checks']:
            print(f"ORIGINAL {check['source']} {check['cell']} {check['metric']} {check['pair']}: "
                  f"{check['status']} raw={check['reproduced_mean_log']} published={check['published_mean_log']}")
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
