#!/usr/bin/env python3
"""X-inputopen-v1 preregistered analysis. Reads artifacts; never launches trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import sys


def helper(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name+'.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


settled = helper('x_settled_analyze')
compaction = helper('x_compaction_analyze')
base = settled.base
require, load_json, number = base.require, base.load_json, base.number
SEED, SETTLE_MS = 20261110, 20000
TAIL_MARGIN, THROUGHPUT_MARGIN = math.log(1.5), math.log(1/.90)
ENTRIES = [f'O1_r{r:02d}_d{d:04d}' for r in (4, 16) for d in (0, 200, 2000)]
PATHS = ('default', 'aligned')
RUNS = [('default', 'A'), ('default', 'AMIN'), ('aligned', 'A'), ('aligned', 'AMIN'), ('default', 'B')]
CELLS = [e+'_'+p for e in ENTRIES for p in PATHS]
DIST, SITES = compaction.DIST, compaction.SITES
METRICS = compaction.METRICS + ('commit_wait_p50', 'commit_wait_max')


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    entry, path = cell.rsplit('_', 1)
    return dict(compaction.nominal('C1'+entry[2:]+'_on'),
                compaction_open_outside_lock=path == 'aligned',
                schema='i7.trial-spec.v1', lock_policy='', diag_trace=False,
                persist_read_histogram=False, inline_flush_check=False)


def check_spec(actual, expected, context):
    optional = dict(compaction_open_outside_lock=False, lock_policy='', diag_trace=False,
                    persist_read_histogram=False, inline_flush_check=False)
    normalized = dict(actual)
    for key, default in optional.items():
        normalized.setdefault(key, default)
        if type(default) is bool:
            require(type(normalized[key]) is bool, f'invalid boolean {context}: {key}')
    settled.check_spec(normalized, expected, context)
    for key, default in optional.items():
        require(normalized[key] == expected.get(key, default), f'unplanned treatment {context}: {key}')
    require(actual.get('schema') == 'i7.trial-spec.v1', f'invalid schema {context}')


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-inputopen-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2**64, 'invalid master seed')
    require(type(plan['settle_ms']) is int and plan['settle_ms'] == SETTLE_MS, 'invalid settle_ms')
    require([c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        require(c['spec']['cell'] == c['id'], 'cell identity mismatch')
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 300, 'plan must contain 300 trials')
    units, seen = {}, set()
    for index, s in enumerate(plan['trials']):
        c, k, arm, tid = s['cell'], s['triplet'], s['arm'], s['trial_id']
        require(c in CELLS and type(k) is int and 1 <= k <= 10, 'invalid cell/round')
        entry, path = c.rsplit('_', 1)
        require((path, arm) in RUNS, 'invalid run (B belongs only to default)')
        require(tid == f'{c}_t{k:02d}_{arm}' and tid not in seen, 'invalid/duplicate trial')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        h = hashlib.sha256(f"{plan['master_seed']}|{entry}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True), 'incorrect paired seed')
        require(s['runlist_position'] == index+1 and k == index//30+1, 'invalid round/runlist position')
        units.setdefault((entry, k), []).append((index, s))
    require(len(units) == 60, 'missing entry-round unit')
    for rows in units.values():
        require(len(rows) == 5 and {(s['cell'].rsplit('_', 1)[1], s['arm']) for _, s in rows} == set(RUNS), 'incomplete five-run unit')
        require([i for i, _ in rows] == list(range(rows[0][0], rows[0][0]+5)), 'noncontiguous unit')
        require([s['order_position'] for _, s in rows] == list(range(1, 6)), 'invalid order positions')
    # Exact Go PRNG order is independently checked by inputopen_test.go.
    return {s['trial_id']: s for s in plan['trials']}, {
        key: {(s['cell'].rsplit('_', 1)[1], s['arm']): s['trial_id'] for _, s in rows}
        for key, rows in units.items()}


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result identity/status')
    check_spec(s, nominal(s['cell']), s['trial_id'])
    ops, window = r['operations'], r['timestamps']['measured_window_ns']
    require(ops['validation_error_count'] == ops['error_ops_count'] == 0 and window > 0,
            'invalid successful window')
    require(ops['read_ops_completed'] > 0, 'invalid mixed read count')
    for cls, prefix in (('reads', 'read'), ('writes', 'write')):
        l = r['transaction_ledger'][cls]
        fields = ('offered', 'attempted', 'completed_in_window', 'completed_late', 'failed',
                  'abandoned', 'queued', 'outstanding_at_window_close')
        require(all(type(l[k]) is int and l[k] >= 0 for k in fields), 'invalid lifecycle count')
        require(l['offered'] == l['attempted'] == l['completed_in_window']+l['completed_late']+l['failed']
                and l['abandoned'] == l['queued'] == l['failed'] == 0
                and l['outstanding_at_window_close'] == l['completed_late'], 'ledger violation')
        for suffix in ('in_window', 'late'):
            require(l['completed_'+suffix] == ops[prefix+'_ops_completed_'+suffix], 'completion mismatch')
        require(ops[prefix+'_ops_completed'] == l['completed_in_window']+l['completed_late'], 'total completion mismatch')
    reads, writes = ops['read_ops_completed_in_window'], ops['write_ops_completed_in_window']
    out = dict(W=r['throughput']['write_ops_per_sec'], R=r['throughput']['read_ops_per_sec'])
    require(out['W'] is not None and out['R'] is not None
            and math.isclose(out['W'], writes/(window/1e9), rel_tol=1e-9, abs_tol=1e-9)
            and math.isclose(out['R'], reads/(window/1e9), rel_tol=1e-9, abs_tol=1e-9), 'throughput/count mismatch')
    h = r['latency_read_nanos']
    require(h['sample_count'] == reads, 'read histogram count mismatch')
    out['read_p99'] = h.get('p99') if reads >= 100 else None
    wait = r['sync_instrumentation']['writer_lock_wait_nanos'].get('commit', {})
    for key in ('count', 'p50_nanos', 'p99_nanos', 'max_nanos'):
        number(wait.get(key), 'commit wait.'+key)
    # Histogram count includes the post-window drain, as in X-compaction-v1.
    for metric, key in (('commit_wait_p50', 'p50_nanos'), ('commit_wait_p99', 'p99_nanos'), ('commit_wait_max', 'max_nanos')):
        out[metric] = wait.get(key) if wait.get('count', 0) else None
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
    require('smoke' not in root.name.lower(), 'mechanical smoke directory is excluded from analysis')
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
                                     error=(t.get('error') or (t.get('result') or {}).get('error')) if t else None,
                                     compaction_errors=((t.get('result') or {}).get('lsm_diagnostics') or {}).get('compaction_errors') if t else None))
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


def arithmetic(values):
    defined = [v for v in values if v is not None]
    n, ci, boot = len(defined), None, None
    mean = sum(defined)/n if n else None
    if n >= 2:
        half = float(base.stats.t.ppf(.975, n-1))*float(base.np.std(defined, ddof=1))/math.sqrt(n)
        ci = [mean-half, mean+half]
        rng = base.np.random.default_rng(SEED)
        draws = base.np.asarray(defined)[rng.integers(0, n, size=(10000, n))].mean(axis=1)
        boot = base.np.percentile(draws, [2.5, 97.5]).tolist()
    return dict(n=n, undefined_n=len(values)-n, mean=mean, t_ci=ci, bootstrap_ci=boot, level=.95, values=values)


def tost(logs, margin):
    """Paired TOST at an explicit margin; never mutates earlier analyzers."""
    out = base.paired_stats(logs, .90, seed=SEED)
    out.update(p_lower=None, p_upper=None, p_tost=None, margin_log=margin)
    if out['n'] < 2:
        return out
    mean, sd, n = out['mean_log'], out['sd_log'], out['n']
    if sd == 0:
        lower, upper = float(mean <= -margin), float(mean >= margin)
    else:
        se = sd/math.sqrt(n)
        lower = float(base.stats.t.sf((mean+margin)/se, n-1))
        upper = float(base.stats.t.cdf((mean-margin)/se, n-1))
    out.update(p_lower=lower, p_upper=upper, p_tost=max(lower, upper))
    return out


def adjust_family(family, name, margin):
    for entry, p in base.holm({e: v['p_tost'] for e, v in family.items()}).items():
        v = family[entry]
        ci = v['t_ci_log']
        inside = bool(ci and ci[0] > -margin and ci[1] < margin)
        classification = ('equivalent' if inside and p is not None and p < .05 else
                          'not_equivalent' if ci and (ci[1] < -margin or ci[0] > margin) else 'inconclusive')
        v.update(holm_p=p, family=name, classification=classification,
                 supported=None if v['p_tost'] is None else classification == 'equivalent')
    return family


def ratio(complete, records, numerator, denominator, metric, margin=None):
    logs, rounds, excluded, adjusted = [], [], [], []
    for k, g in complete.items():
        x, y = records[g[numerator]][metric], records[g[denominator]][metric]
        # X-paced section 5: add .1 to ALL runs in a unit containing any zero W.
        if metric == 'W' and any(records[tid]['W'] == 0 for tid in g.values()):
            x, y = x+.1, y+.1
            adjusted.append(k)
        if x is not None and y is not None and x > 0 and y > 0:
            logs.append(math.log(x)-math.log(y)); rounds.append(k)
        else:
            excluded.append(k)
    stat = tost(logs, margin) if margin is not None else base.paired_stats(logs, seed=SEED)
    return dict(stat, rounds=rounds, excluded_rounds=excluded, undefined_n=len(excluded),
                zero_adjusted_W_rounds=adjusted, numerator=list(numerator), denominator=list(denominator), metric=metric)


def verdict(tests, partial):
    if any(v is False for v in tests):
        return 'not_supported'
    if partial or not tests or any(v is None for v in tests):
        return 'inconclusive'
    return 'supported'


def prediction(settings, partial):
    tests = [v['supported'] for v in settings.values()]
    decision = verdict(tests, partial)
    return dict(settings=settings, supported=True if decision == 'supported' else False if decision == 'not_supported' else None,
                verdict=decision, supported_settings=sum(v is True for v in tests), planned=len(settings))


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells, h1, h2 = {}, {}, {}
    h3, h4 = {m: {} for m in ('W', 'R')}, {m: {} for m in ('W', 'R')}
    coarse, baseline, exclusions, trends = {}, {}, [], {}
    for entry in ENTRIES:
        complete = {k: g for (e, k), g in sorted(groups.items()) if e == entry and all(tid in records for tid in g.values())}
        summaries, drift = {}, {}
        for path, arm in RUNS:
            label = path+'/'+arm
            summaries[label] = {m: arithmetic([records[g[path, arm]][m] for g in complete.values()]) for m in METRICS}
            for m in METRICS:
                missing = [k for k, g in complete.items() if records[g[path, arm]][m] is None]
                if missing:
                    exclusions.append(dict(entry=entry, path=path, arm=arm, metric=m, rounds=missing,
                                           reason='undefined metric; observation retained'))
            rows = [(k, records[g[path, arm]]['R']) for k, g in complete.items()]
            positive = [(k, math.log(v)) for k, v in rows if v is not None and v > 0]
            drift[label] = dict(round_R=rows, log_R_slope_per_round=float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None)
        cells[entry] = dict(complete_rounds=list(complete), runs=summaries,
                            zero_adjusted_W_rounds=[k for k, g in complete.items() if any(records[tid]['W'] == 0 for tid in g.values())])
        trends[entry] = drift
        stat = ratio(complete, records, ('aligned', 'AMIN'), ('default', 'AMIN'), 'commit_wait_p99')
        h1[entry] = dict(stat, supported=None if stat['t_ci'] is None else stat['t_ci'][1] < 1)
        h2[entry] = ratio(complete, records, ('aligned', 'AMIN'), ('default', 'B'), 'commit_wait_p99', TAIL_MARGIN)
        for metric in ('W', 'R'):
            h3[metric][entry] = ratio(complete, records, ('aligned', 'AMIN'), ('default', 'AMIN'), metric, THROUGHPUT_MARGIN)
            h4[metric][entry] = ratio(complete, records, ('aligned', 'AMIN'), ('default', 'B'), metric, THROUGHPUT_MARGIN)
        coarse[entry] = {metric: ratio(complete, records, ('aligned', 'A'), ('default', 'A'), metric)
                         for metric in ('commit_wait_p99', 'W', 'R')}
        baseline[entry] = ratio(complete, records, ('default', 'AMIN'), ('default', 'B'), 'commit_wait_p99')
    adjust_family(h2, 'H-O2 tail (6)', TAIL_MARGIN)
    for hypothesis, families in (('H-O3', h3), ('H-O4', h4)):
        for metric, family in families.items():
            adjust_family(family, hypothesis+' '+metric+' (6)', THROUGHPUT_MARGIN)
    def combined(families):
        tests = [v['supported'] for f in families.values() for v in f.values()]
        return dict(verdict=verdict(tests, out['partial']),
                    **{metric: prediction(f, out['partial']) for metric, f in families.items()})
    out.update(cells=cells, H_O1=prediction(h1, out['partial']), H_O2=prediction(h2, out['partial']),
               H_O3=combined(h3), H_O4=combined(h4), coarse_aligned_relative_default=coarse,
               lease_default_relative_snapshot=baseline,
               across_campaign_reference=dict(campaign_id='X-compaction-v1', lease_snapshot_commit_wait_p99_range=[1.43, 7.69],
                                              label='Across-campaign descriptive comparison; new seeds and interface lock overhead'),
               tail_margin_log=TAIL_MARGIN, throughput_margin_log=THROUGHPUT_MARGIN,
               round_trend_R=trends, outcome_exclusions=exclusions,
               trials={tid: dict(cell=specs[tid]['cell'], entry=specs[tid]['cell'].rsplit('_', 1)[0],
                                path=specs[tid]['cell'].rsplit('_', 1)[1], round=specs[tid]['triplet'],
                                arm=specs[tid]['arm'], **v) for tid, v in sorted(records.items())},
               notes=[
                   'All summaries and tests use complete five-run entry-round units. Successful observations from dropped units remain in trials. No outcome-based exclusions or pooling.',
                   'H-O1 requires every setting\'s paired 95% log-t CI wholly below 1. H-O2 uses +/-ln(1.5), a 90% paired log-t CI and one Holm family of six. H-O3 and H-O4 each use separate write/read families of six at +/-ln(1/.90). Untestable planned tests count as p=1.',
                   'Equivalence classes: equivalent requires the 90% CI inside the margin and Holm TOST p<.05; not_equivalent requires the CI wholly outside the margin; otherwise inconclusive. Bootstrap sensitivity uses 10000 paired resamples, seed 20261110; it does not replace confirmatory t/TOST decisions.',
                   'W zeros follow X-paced section 5: +0.1 writes/s to every run of a zero-containing complete unit, only for log ratios. Raw summaries retain zero. Nonpositive/missing log pairs are listed, with no latency pseudocounts. Missing lifecycle histograms are undefined, not zero.',
                   'First-acquisition commit wait excludes the writer\'s own fsync and includes post-window drain; p50, p99 and max are in nanoseconds. R and W are in-window throughput. B only runs under default and already opens outside locks.',
                   'CoarseLock aligned/default ratios and all raw path/design summaries are descriptive with 95% CIs. Lease default/Snapshot is compared to the earlier 1.43 to 7.69 range descriptively across campaigns; the lock interface call was absent in X-compaction-v1.',
                   'H-O1 supported and H-O2 not supported means that input-open asymmetry explains part of the gap, not all of it. One host, one writer, merge-all 250 ms timer; bursts near 31.5 ms are uncontrolled.',
                   'R by round and log-R slopes are descriptive drift checks. Smoke directories are excluded. Latest attempts are authoritative, one retry is allowed, and second failures drop the entire unit. Missing identities require --allow-partial.'
               ])
    comparisons = [('H_O1', h1), ('H_O2', h2), ('Lease default/Snapshot', baseline),
                   *[(f'H-O3 {m}', f) for m, f in h3.items()], *[(f'H-O4 {m}', f) for m, f in h4.items()]]
    comparisons += [(f'CoarseLock aligned/default {m}', {e: f[m] for e, f in coarse.items()}) for m in ('W', 'R', 'commit_wait_p99')]
    for name, family in comparisons:
        for entry, stat in family.items():
            if stat['excluded_rounds']:
                exclusions.append(dict(entry=entry, comparison=name, metric=stat['metric'], rounds=stat['excluded_rounds'], reason='missing/nonpositive log pair'))
    o1, o2 = out['H_O1']['verdict'], out['H_O2']['verdict']
    out['interpretation'] = ('Input-open asymmetry explains part of the gap and not all of it.' if o1 == 'supported' and o2 == 'not_supported'
                             else 'Both preregistered tail predictions supported.' if o1 == o2 == 'supported'
                             else 'Tail predictions are not jointly supported; inspect all setting-level intervals and data completeness.')
    return out


def tables(out):
    lines = ['# X-inputopen-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## '+title, '', '| '+' | '.join(headers)+' |', '| '+' | '.join(['---']*len(headers))+' |'])
        lines.extend('| '+' | '.join(base.fmt(v).replace('|', '\\|') for v in row)+' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in (
        'campaign_id', 'master_seed', 'settle_ms', 'plan_sha256', 'ledger_sha256', 'runner_sha256',
        'git_revision', 'platform', 'counts', 'bootstrap_seed', 'bootstrap_resamples')])
    table('Predictions', ['Hypothesis', 'Result'], [(k, out[k]) for k in ('H_O1', 'H_O2', 'H_O3', 'H_O4')])
    lines += [out['interpretation'], '']
    table('Per-design and path summaries (latencies in ns; arithmetic 95% CI)', ['Setting', 'Path/arm', 'Metric', 'Summary'],
          [(e, run, m, {k: v for k, v in stat.items() if k != 'values'}) for e, cs in out['cells'].items()
           for run, metrics in cs['runs'].items() for m, stat in metrics.items()])
    table('CoarseLock aligned/default (paired 95% CI)', ['Setting', 'Ratios'], out['coarse_aligned_relative_default'].items())
    table('Lease default/Snapshot: across-campaign descriptive comparison', ['Setting', 'Paired 95% CI'], out['lease_default_relative_snapshot'].items())
    lines += [str(out['across_campaign_reference']), '']
    table('Drift (descriptive)', ['Setting', 'Path/arm', 'Trend'], [(e, r, v) for e, runs in out['round_trend_R'].items() for r, v in runs.items()])
    table('Zero-W adjustments', ['Setting', 'Rounds'], [(e, cs['zero_adjusted_W_rounds']) for e, cs in out['cells'].items()])
    table('Inter-attempt gaps', ['Field', 'Value'], out['gaps'].items())
    table('Failures, missing units and metric exclusions', ['Kind', 'Details'], [(k, v) for k in (
        'missing_trials', 'failed_trials', 'failed_attempts_detail', 'pending_retry', 'dropped_units', 'outcome_exclusions') for v in out[k]])
    lines += ['![Commit-wait p99 by setting and path](x_inputopen_commit_wait_p99.png)', '']
    lines.extend(out['notes'])
    return '\n'.join(lines)+'\n'


def figures(out, directory):
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    fig, ax = plt.subplots(figsize=(12, 5))
    for j, (path, arm) in enumerate(RUNS):
        x, y, low, high = [], [], [], []
        for i, entry in enumerate(ENTRIES):
            stat = out['cells'][entry]['runs'][path+'/'+arm]['commit_wait_p99']
            if stat['mean'] is None:
                continue
            v, ci = stat['mean']/1e6, stat['t_ci']
            x.append(i+(j-2)*.14); y.append(v)
            low.append(max(0, v-ci[0]/1e6) if ci else 0)
            high.append(max(0, ci[1]/1e6-v) if ci else 0)
        label = {'A':'CoarseLock', 'AMIN':'Lease', 'B':'Snapshot'}[arm]+' '+path
        ax.errorbar(x, y, yerr=[low, high], fmt='o', capsize=3, label=label)
    ax.set_xticks(range(6), [e[3:] for e in ENTRIES], rotation=25)
    ax.set(ylabel='Commit-wait p99 (ms; mean with 95% t CI)', xlabel='Setting (reader count and delay in us)',
           title='X-inputopen-v1'+(' — PARTIAL DATA' if out['partial'] else ''))
    ax.legend(fontsize=8); fig.tight_layout()
    fig.savefig(directory/'x_inputopen_commit_wait_p99.png', metadata={'Software':'x_inputopen_analyze'})
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
        (args.output_dir/'x_inputopen_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir/'x_inputopen_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
