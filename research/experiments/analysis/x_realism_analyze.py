#!/usr/bin/env python3
"""X-realism-v1 preregistered analysis. Reads artifacts; never launches trials."""
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
base = settled.base
require, load_json, number = base.require, base.load_json, base.number
SEED, SETTLE_MS, MARGIN = 20261111, 20000, math.log(1/.90)
ARMS = ('A', 'AMIN', 'B')
CELLS = [f'D1_r{r:02d}_w{w}_direct' for w in (1, 2, 4) for r in (4, 16)] + [f'D2_r{r:02d}_w1_buffered' for r in (4, 16)]
METRICS = ('R', 'W', 'q', 'hold', 'read_p50', 'read_p95', 'read_p99', 'commit_wait_mean',
           'commit_wait_p50', 'commit_wait_p99', 'commit_wait_max', 'flushes', 'conflict_aborts')


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    _, readers, writers, path = cell.split('_')
    return dict(settled.intervention.nominal(f'I1_{readers}_d0000'),
                read_path='direct' if path == 'direct' else 'block', writer_concurrency=int(writers[1:]),
                schema='i7.trial-spec.v1', lock_policy='', diag_trace=False,
                persist_read_histogram=False, inline_flush_check=False,
                compaction_open_outside_lock=False, compaction_trigger_interval_ms=0)


def check_spec(actual, expected, context):
    optional = dict(lock_policy='', diag_trace=False, persist_read_histogram=False,
                    inline_flush_check=False, compaction_open_outside_lock=False)
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
    require(plan['campaign_id'] == 'X-realism-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2**64, 'invalid master seed')
    require(type(plan['settle_ms']) is int and plan['settle_ms'] == SETTLE_MS, 'invalid settle_ms')
    require([c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        require(c['spec']['cell'] == c['id'], 'cell identity mismatch')
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 240, 'plan must contain 240 trials')
    units, seen = {}, set()
    for index, s in enumerate(plan['trials']):
        c, k, arm, tid = s['cell'], s['triplet'], s['arm'], s['trial_id']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and arm in ARMS, 'invalid cell/round/arm')
        require(tid == f'{c}_t{k:02d}_{arm}' and tid not in seen, 'invalid/duplicate trial')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        reader = c.split('_')[1]
        h = hashlib.sha256(f"{plan['master_seed']}|D_{reader}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True), 'incorrect paired seed')
        require(s['runlist_position'] == index+1 and k == index//24+1, 'invalid round/runlist position')
        require(s['order_position'] == index%3+1, 'invalid order positions')
        require(arm == settled.PERMUTATIONS[(k+CELLS.index(c))%6][index%3], 'incorrect arm permutation')
        units.setdefault((c, k), []).append((index, s))
    require(len(units) == 80, 'missing cell-round unit')
    for rows in units.values():
        require(len(rows) == 3 and {s['arm'] for _, s in rows} == set(ARMS), 'incomplete triplet')
        require([i for i, _ in rows] == list(range(rows[0][0], rows[0][0]+3)), 'noncontiguous unit')
    # Exact seeded Go cell shuffling is independently checked in realism_test.go.
    return {s['trial_id']: s for s in plan['trials']}, {
        key: {s['arm']: s['trial_id'] for _, s in rows} for key, rows in units.items()}


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result identity/status')
    ops, diag, window = r['operations'], r['lsm_diagnostics'], r['timestamps']['measured_window_ns']
    conflicts = ops['write_conflict_aborts']
    require(type(conflicts) is int and conflicts >= 0, 'invalid conflict abort count')
    require(ops['validation_error_count'] == 0 and ops['error_ops_count'] == conflicts and window > 0,
            'non-conflict operation fault or invalid successful window')
    require(diag.get('compaction_errors', 0) == diag['measured_compactions'] == diag.get('compaction_successes', 0) == 0,
            'unplanned compaction or engine fault')
    require(diag['initial_sstables'] == 4 and diag['final_sstables'] >= 4, 'invalid table counts')
    for cls, prefix in (('reads', 'read'), ('writes', 'write')):
        l = r['transaction_ledger'][cls]
        fields = ('offered', 'attempted', 'completed_in_window', 'completed_late', 'failed',
                  'abandoned', 'queued', 'outstanding_at_window_close')
        require(all(type(l[k]) is int and l[k] >= 0 for k in fields), 'invalid lifecycle count')
        require(l['offered'] == l['attempted'] == l['completed_in_window']+l['completed_late']+l['failed']
                and l['abandoned'] == l['queued'] == 0 and l['failed'] == (conflicts if cls == 'writes' else 0)
                and l['outstanding_at_window_close'] == l['completed_late'], 'ledger violation')
        for suffix in ('in_window', 'late'):
            require(l['completed_'+suffix] == ops[prefix+'_ops_completed_'+suffix], 'completion mismatch')
        require(ops[prefix+'_ops_completed'] == l['completed_in_window']+l['completed_late'], 'total completion mismatch')
        require(ops[prefix+'_ops_attempted'] == l['attempted'], 'attempt mismatch')
    reads, writes = ops['read_ops_completed_in_window'], ops['write_ops_completed_in_window']
    out = dict(R=r['throughput']['read_ops_per_sec'], W=r['throughput']['write_ops_per_sec'],
               q=reads/(s['reader_concurrency']*writes) if writes else None,
               zero_writes=writes == 0, reads=reads, commits=writes, conflict_aborts=conflicts,
               attempted_commits=ops['write_ops_attempted'],
               conflict_abort_share=conflicts/ops['write_ops_attempted'] if ops['write_ops_attempted'] else None,
               flushes=diag['measured_flushes'], measured_window_ns=window)
    require(out['R'] is not None and out['W'] is not None
            and math.isclose(out['R'], reads/(window/1e9), rel_tol=1e-9, abs_tol=1e-9)
            and math.isclose(out['W'], writes/(window/1e9), rel_tol=1e-9, abs_tol=1e-9), 'throughput/count mismatch')
    h = r['latency_read_nanos']
    require(h['sample_count'] == reads, 'read histogram count mismatch')
    for q, minimum in (('p50', 1), ('p95', 20), ('p99', 100)):
        out['read_'+q] = h.get(q) if reads >= minimum else None
    sync = r['sync_instrumentation']
    cs = sync['read_critical_section_nanos']
    require(isinstance(cs, dict) and len(cs) <= 1, 'invalid read critical section')
    cs = next(iter(cs.values()), {})
    out['hold'] = cs.get('mean_nanos') if cs.get('count', 0) else None
    wait = sync['writer_lock_wait_nanos'].get('commit', {})
    for metric, key in (('mean', 'mean_nanos'), ('p50', 'p50_nanos'), ('p99', 'p99_nanos'), ('max', 'max_nanos')):
        out['commit_wait_'+metric] = wait.get(key) if wait.get('count', 0) else None
    for key in METRICS:
        number(out[key], key)
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
    require(platform is not None, 'missing platform provenance')
    if platform.get('goos') == 'linux':
        require(all(platform.get(k) for k in ('kernel', 'file_system', 'device')), 'missing Linux kernel/filesystem/device')
        require('microsoft' not in platform['kernel'].lower() and 'wsl' not in platform['kernel'].lower(), 'WSL2 is rehearsal only, excluded from analysis')
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


def complete_units(groups, records, cells):
    """Require every arm of each participating cell, pairing by round."""
    return {k: {(c, a): groups[c, k][a] for c in cells for a in ARMS}
            for k in range(1, 11)
            if all(tid in records for c in cells for tid in groups[c, k].values())}


def ratio(units, records, numerator, denominator, metric, equivalence=False):
    logs, rounds, excluded, adjusted = [], [], [], []
    for k, g in units.items():
        x, y = records[g[numerator]][metric], records[g[denominator]][metric]
        if metric == 'W' and any(records[tid]['W'] == 0 for tid in g.values()):
            x, y = x+.1, y+.1
            adjusted.append(k)
        if x is not None and y is not None and x > 0 and y > 0:
            logs.append(math.log(x)-math.log(y)); rounds.append(k)
        else:
            excluded.append(k)
    stat = tost(logs, MARGIN) if equivalence else base.paired_stats(logs, seed=SEED)
    return dict(stat, rounds=rounds, excluded_rounds=excluded, undefined_n=len(excluded),
                zero_adjusted_W_rounds=adjusted, numerator=list(numerator), denominator=list(denominator), metric=metric)


def q_test(summary, band):
    ci = summary['t_ci']
    # A zero denominator must not disappear and narrow the confirmatory CI.
    supported = None if ci is None or summary['undefined_n'] else band[0] < ci[0] and ci[1] < band[1]
    return dict(summary, band=band, supported=supported)


def reference_holds(root, explicit=None):
    """Earlier observations are descriptive context, never enter any test."""
    candidate = Path(explicit) if explicit else Path(root).parent/'campaign'
    if not (candidate/'plan.json').exists():
        require(explicit is None, 'missing --readcost-dir plan')
        return dict(available=False, label='Across-campaign X-readcost-v1 context; never pooled',
                    reason='Earlier campaign unavailable; supply --readcost-dir to report hold against injected doses', cells={})
    _, groups, records, meta = base.load_campaign(candidate, False)
    require(meta['campaign_id'] == 'X-readcost-v1', 'wrong earlier campaign')
    cells = {}
    for readers in (4, 16):
        for dose in (0, 20, 200, 2000):
            cell = f'X1_r{readers:02d}_d{dose:04d}'
            values = [records[g['A']]['read_cs_mean'] for (c, _), g in sorted(groups.items())
                      if c == cell and all(tid in records for tid in g.values())]
            cells[cell] = dict(reader_concurrency=readers, injected_delay_us=dose, hold=arithmetic(values))
    return dict(available=True, label='Across-campaign X-readcost-v1 context; different seeds; never pooled',
                plan_sha256=meta['plan_sha256'], ledger_sha256=meta['ledger_sha256'], cells=cells)


def analyze(root, allow_partial=False, readcost_dir=None):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells, h1, h2, h3, h4, exclusions, trends = {}, {}, {}, {}, {}, [], {}
    for c in CELLS:
        units = complete_units(groups, records, [c])
        runs = {a: {m: arithmetic([records[g[c, a]][m] for g in units.values()]) for m in METRICS} for a in ARMS}
        for a in ARMS:
            for m in METRICS:
                missing = [k for k, g in units.items() if records[g[c, a]][m] is None]
                if missing:
                    exclusions.append(dict(cell=c, arm=a, metric=m, rounds=missing, reason='undefined metric; trial retained'))
        cells[c] = dict(complete_rounds=list(units), runs=runs,
                        S=ratio(units, records, (c, 'A'), (c, 'AMIN'), 'W'),
                        read_ratios={a+'/'+b:ratio(units, records, (c,a), (c,b), 'R') for a,b in (('A','AMIN'),('A','B'),('AMIN','B'))})
        trends[c] = {}
        for a in ARMS:
            rows = [(k, records[g[c,a]]['R']) for k,g in units.items()]
            positive = [(k,math.log(v)) for k,v in rows if v > 0]
            trends[c][a] = dict(round_R=rows, log_R_slope_per_round=float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive)>=2 else None)
        if c in CELLS[:2]:
            stat = cells[c]['S']
            h1[c+'/S'] = dict(stat, supported=None if stat['t_ci'] is None else stat['t_ci'][1] < 1)
            h1[c+'/q'] = q_test(runs['A']['q'], [1.90, 2.10])
        if c in CELLS[2:6]:
            h2[c] = q_test(runs['A']['q'], [1.80, 2.20])
        if c in CELLS[:6]:
            for m in ('R','W'):
                h3.setdefault(m,{})[c] = ratio(units, records, (c,'AMIN'), (c,'B'), m, True)
    for m, family in h3.items():
        adjust_family(family, 'H-D3 '+m+' (6)', MARGIN)
    path_ratios = {}
    for readers in (4,16):
        one = f'D1_r{readers:02d}_w1_direct'
        for w in (2,4):
            other = f'D1_r{readers:02d}_w{w}_direct'
            units = complete_units(groups, records, [one,other])
            h4[f'r{readers:02d}_w{w}/w1'] = ratio(units, records, (other,'AMIN'), (one,'AMIN'), 'W', True)
        buffered = f'D2_r{readers:02d}_w1_buffered'
        units = complete_units(groups, records, [one,buffered])
        path_ratios[f'r{readers:02d}'] = {a:{m:ratio(units, records, (one,a), (buffered,a), m) for m in ('R','W')} for a in ARMS}
    adjust_family(h4, 'H-D4 W (4)', MARGIN)
    # H-X covers every recorded attempt, including failures, earlier attempts,
    # late aborts and successful trials whose triplet was dropped elsewhere.
    aborts, unknown, attempts = [], [], []
    conflict_cells = {c: dict(conflict_aborts=0, attempted_commits=0, known_attempts=0,
                              unknown_attempts=[], missing_trials=[]) for c in CELLS}
    ledger = {}
    for row in settled.ledger_rows(Path(root)):
        ledger[row['trial_id'],row['attempt']] = row
    for (tid, attempt), row in sorted(ledger.items()):
        path = Path(root)/'trials'/f'{tid}_a{attempt}.json'
        t = load_json(path) if path.exists() else {}
        ops = (t.get('result') or {}).get('operations') or {}
        count, attempted = ops.get('write_conflict_aborts'), ops.get('write_ops_attempted')
        cell = specs[tid]['cell']
        summary = conflict_cells[cell]
        if count is None or attempted is None:
            item = dict(trial_id=tid, attempt=attempt, cell=cell)
            unknown.append(item)
            summary['unknown_attempts'].append(item)
        else:
            require(type(count) is int and type(attempted) is int and 0 <= count <= attempted,
                    'invalid attempt conflict/commit count')
            item = dict(trial_id=tid, attempt=attempt, cell=cell, conflict_aborts=count,
                        attempted_commits=attempted, abort_share=count/attempted if attempted else None)
            attempts.append(item)
            summary['conflict_aborts'] += count
            summary['attempted_commits'] += attempted
            summary['known_attempts'] += 1
            if count: aborts.append(dict(trial_id=tid,attempt=attempt,cell=cell,count=count))
    for missing in out['missing_trials']:
        conflict_cells[specs[missing['trial_id']]['cell']]['missing_trials'].append(missing['trial_id'])
    for cell, summary in conflict_cells.items():
        count, attempted = summary['conflict_aborts'], summary['attempted_commits']
        complete = not summary['unknown_attempts'] and not summary['missing_trials']
        summary.update(abort_share=count/attempted if attempted and complete else None,
                       observed_abort_share=count/attempted if attempted else None,
                       exceeds_one_percent=100*count > attempted if attempted and complete else None,
                       reporting_complete=complete)
        cells[cell]['conflict_abort_reporting'] = summary
    hx = dict(kind='descriptive_protocol_check', cells=conflict_cells, attempts=attempts,
              aborts=aborts, unknown_attempts=unknown, total_aborts=sum(v['count'] for v in aborts),
              scope='All recorded trial attempts, including retries, failures and late commits; pooled within each cell only.',
              disclosure_threshold=.01,
              cells_requiring_disclosure=[c for c, v in conflict_cells.items() if v['exceeds_one_percent'] is True])
    out.update(cells=cells, H_D1=prediction(h1,out['partial']), H_D2=prediction(h2,out['partial']),
               H_D3=dict(verdict=verdict([v['supported'] for f in h3.values() for v in f.values()],out['partial']),
                         **{m:prediction(f,out['partial']) for m,f in h3.items()}),
               H_D4=prediction(h4,out['partial']),
               H_X=hx,
               direct_relative_buffered=path_ratios, across_campaign_reference=reference_holds(root,readcost_dir),
               round_trend_R=trends, outcome_exclusions=exclusions,
               trials={tid:dict(cell=specs[tid]['cell'],round=specs[tid]['triplet'],arm=specs[tid]['arm'],**v) for tid,v in sorted(records.items())},
               margin_log=MARGIN,
               notes=[
                   'H-D1: two 95% paired log-t S intervals below 1 and two 95% arithmetic q intervals strictly within [1.90,2.10]. H-D2: four q intervals strictly within [1.80,2.20].',
                   'H-D3 uses separate Holm families of six for R and W; H-D4 one family of four paired by round. TOST margin +/-ln(1/.90), 90% paired log-t CIs; untestable planned tests count as p=1.',
                   'All cell summaries and tests require complete three-arm cell-rounds. Cross-cell ratios require both complete triplets. Successful observations from dropped units remain visible in trials; no outcome-based exclusions.',
                   'X-paced zero rule: +0.1 writes/s to all runs of a zero-containing comparison unit for W log ratios only. Raw summaries retain zeros. q with W=0 is undefined and makes the band decision inconclusive; never silently omit that denominator to claim support. No latency pseudocounts.',
                   'Hold is each trial mean read critical section, including drain. Commit wait also includes drain. Read p50/p95/p99 and R/W use in-window completions. Latencies are nanoseconds. W counts every writer commit once.',
                   'Bootstrap sensitivity uses 10000 paired resamples, seed 20261111. R by round/log-R slopes describe drift. Across-campaign hold context and Linux replication are never pooled.',
                   'H-X is descriptive: conflicts are not failures and trials are retained. Report aborts per trial attempt and the sum of aborts divided by attempted commits per cell (all recorded attempts, including retries and late completions). Strictly more than 1% requires disclosure beside that cell. Unknown counts or missing trials make the full-cell share and disclosure flag undefined; observed counts remain visible. W counts successful in-window commits only. Other engine/harness faults fail; latest attempt is authoritative for throughput; retry once then drop the complete cell-round.',
                   'Small uniform-read fixtures, one Windows SSD unless optional native Linux replication runs; not running replication is not a deviation. Device/controller caches and bursts near 31.5 ms remain uncontrolled.'
               ])
    sensitivity = []
    for name, family in [('H-D1 S',{k:v for k,v in h1.items() if k.endswith('/S')}),
                         *[('H-D3 '+m,f) for m,f in h3.items()], ('H-D4',h4)]:
        for key,v in family.items():
            ci, boot = v['t_ci'],v['bootstrap_ci']
            if ci and boot:
                decision = (lambda x: x[1]<1) if name=='H-D1 S' else (lambda x: x[0]>.90 and x[1]<1/.90)
                if decision(ci)!=decision(boot): sensitivity.append(dict(prediction=name,setting=key,t_ci=ci,bootstrap_ci=boot))
    for name, family in [('H-D1 q',{k:v for k,v in h1.items() if k.endswith('/q')}),('H-D2',h2)]:
        for key,v in family.items():
            if v['t_ci'] and v['bootstrap_ci']:
                band=v['band']; inside=lambda ci: band[0]<ci[0] and ci[1]<band[1]
                if inside(v['t_ci'])!=inside(v['bootstrap_ci']): sensitivity.append(dict(prediction=name,setting=key,t_ci=v['t_ci'],bootstrap_ci=v['bootstrap_ci']))
    out['bootstrap_sensitivity_disagreements']=sensitivity
    return out


def report(data_dir, allow_partial=False, replication_dir=None, readcost_dir=None):
    primary = analyze(data_dir,allow_partial,readcost_dir)
    if replication_dir is None:
        return primary
    require(Path(data_dir).resolve()!=Path(replication_dir).resolve(), 'replication directory must be separate')
    replication=analyze(replication_dir,allow_partial,readcost_dir)
    require(primary['plan_sha256']==replication['plan_sha256'], 'replication must use the same plan hash')
    require((primary.get('platform') or {}).get('goos')=='windows', 'primary must record Windows')
    platform=replication.get('platform') or {}
    require(platform.get('goos')=='linux' and platform.get('file_system') in ('ext4','xfs')
            and all(platform.get(k) for k in ('kernel','device')), 'replication must record Linux kernel, ext4/xfs and device')
    require('microsoft' not in platform['kernel'].lower() and 'wsl' not in platform['kernel'].lower(), 'WSL2 is rehearsal only, excluded from replication')
    return dict(campaign_id='X-realism-v1', pooling=False, primary=primary,replication=replication)


def tables(out):
    reports=[('Windows / primary',out['primary']),('Native Linux / replication',out['replication'])] if 'primary' in out else [('Primary',out)]
    lines=['# X-realism-v1', '', 'Replication is reported side by side, never pooled.', '']
    if len(reports)==2:
        lines+=['| Prediction | Windows | Native Linux |','| --- | --- | --- |']
        for h in ('H_D1','H_D2','H_D3','H_D4'):
            lines.append(f"| {h} | {reports[0][1][h]['verdict']} | {reports[1][1][h]['verdict']} |")
    for label,result in reports:
        lines+=['',f'## {label}', '', 'PARTIAL DATA' if result['partial'] else 'Complete ledger (terminal failures listed below).','']
        lines+=['| Prediction | Verdict |','| --- | --- |']+[f"| {h} | {result[h]['verdict']} |" for h in ('H_D1','H_D2','H_D3','H_D4')]
        lines+=['', 'H-X: conflict aborts are reported, not failures. Shares include all recorded attempts, retries and late commits. More than 1% must be disclosed beside the cell results.', '',
                '| Cell | S (95% paired CI) | q A (95% t CI) | Conflict aborts / attempted commits | Abort share | Paper disclosure (>1%) |',
                '| --- | --- | --- | --- | --- | --- |']
        for c,cs in result['cells'].items():
            hx = result['H_X']['cells'][c]
            share = 'undefined' if hx['abort_share'] is None else f"{100*hx['abort_share']:.6f}%"
            disclosure = 'unknown' if hx['exceeds_one_percent'] is None else 'REQUIRED: more than 1%' if hx['exceeds_one_percent'] else 'no'
            lines.append(f"| {c} | {cs['S']['geometric_mean_ratio']} {cs['S']['t_ci']} | {cs['runs']['A']['q']['mean']} {cs['runs']['A']['q']['t_ci']} | {hx['conflict_aborts']} / {hx['attempted_commits']} | {share} | {disclosure} |")
        lines+=['','| Cell | Arm | R | W | Hold ns | Read p50/p95/p99 ns | Commit wait mean/p50/p99/max ns | Flushes |','| --- | --- | --- | --- | --- | --- | --- | --- |']
        for c,cs in result['cells'].items():
            for arm,ms in cs['runs'].items():
                mean=lambda m: ms[m]['mean']
                lines.append(f"| {c} | {arm} | {mean('R')} | {mean('W')} | {mean('hold')} | {[mean('read_'+q) for q in ('p50','p95','p99')]} | {[mean('commit_wait_'+q) for q in ('mean','p50','p99','max')]} | {mean('flushes')} |")
        # Every interval, equivalence class, trial value and provenance item is
        # reviewable here as well as in machine-readable JSON.
        lines+=['','### Full predictions, descriptive comparisons and provenance','', '```json', json.dumps(result,sort_keys=True,indent=2,allow_nan=False),'```','']
    return '\n'.join(lines)+'\n'


def figures(out,directory):
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    reports=[('Windows',out['primary']),('Linux',out['replication'])] if 'primary' in out else [('Primary',out)]
    fig,axes=plt.subplots(2,len(reports),figsize=(13,7),squeeze=False,sharex=True)
    for column,(label,result) in enumerate(reports):
        for row,metric in enumerate(('S','q')):
            ax=axes[row,column]
            for i,c in enumerate(CELLS):
                v=result['cells'][c]['S'] if metric=='S' else result['cells'][c]['runs']['A']['q']
                y=v.get('geometric_mean_ratio') if metric=='S' else v['mean']
                if y is None: continue
                ci=v['t_ci'] or [y,y]
                ax.errorbar(i,y,yerr=[[max(0,y-ci[0])],[max(0,ci[1]-y)]],fmt='o',capsize=3,color='#2166ac')
            ax.axhline(1 if metric=='S' else 2,color='grey',linestyle=':')
            if metric=='q':
                for i,c in enumerate(CELLS[:6]):
                    lo,hi=(1.9,2.1) if c in CELLS[:2] else (1.8,2.2)
                    ax.fill_between([i-.35,i+.35],lo,hi,color='grey',alpha=.15)
            ax.set(title=label,ylabel='S = W A / W Lease (95% CI)' if metric=='S' else 'q A = R / (r W) (95% CI)')
            ax.set_xticks(range(8),CELLS,rotation=35,ha='right')
    fig.tight_layout()
    fig.savefig(directory/'x_realism_s_q.png',metadata={'Software':'x_realism_analyze'})
    plt.close(fig)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--data-dir',type=Path,required=True)
    parser.add_argument('--output-dir',type=Path,required=True)
    parser.add_argument('--allow-partial',action='store_true')
    parser.add_argument('--replication-dir',type=Path)
    parser.add_argument('--readcost-dir',type=Path,help='earlier X-readcost-v1 artifacts for across-campaign hold context (default: sibling campaign)')
    args=parser.parse_args()
    try:
        out=report(args.data_dir,args.allow_partial,args.replication_dir,args.readcost_dir)
        encoded=json.dumps(out,sort_keys=True,indent=2,allow_nan=False)+'\n'
        args.output_dir.mkdir(parents=True,exist_ok=True)
        figures(out,args.output_dir)
        (args.output_dir/'x_realism_results.json').write_text(encoded,encoding='utf-8',newline='\n')
        (args.output_dir/'x_realism_tables.md').write_text(tables(out),encoding='utf-8',newline='\n')
    except (ValueError,OSError,KeyError,TypeError,AttributeError,OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}',file=sys.stderr)
        return 1
    return 0


if __name__=='__main__':
    sys.exit(main())
