#!/usr/bin/env python3
"""Pre-registered X-readcost-v1 analysis. Inputs are read-only; no trials run here."""
import argparse
import hashlib
import json
import math
from pathlib import Path
import re
import sys

import numpy as np
from scipy import stats

ARMS = ('A', 'AMIN', 'B')
SEED = 20261030
MARGIN = math.log(1 / .90)
X1 = [f'X1_r{r:02d}_{d}' for r in (4, 16)
      for d in ('d0000', 'd0020', 'd0200', 'd2000', 'legacy')]
X2 = [f'X2_r16_{d}' for d in ('d0000', 'd0200', 'd2000', 'legacy')]
X3 = [f'X3_r{r:02d}' for r in (1, 4, 16)]
CELLS = X1 + X2 + X3
METRICS = ('W', 'R', 'read_p50', 'read_p99', 'read_cs_mean', 'read_cs_p99',
           'commit_wait_p50', 'commit_wait_p99', 'publication_lock_wait_p50',
           'publication_lock_wait_p99')
PAIRS = (('A', 'AMIN'), ('A', 'B'), ('AMIN', 'B'))


def require(condition, message):
    if not condition:
        raise ValueError(message)


def paired_stats(d, level=.95, seed=SEED):
    """Summarize paired log-ratios; both CIs are on the ratio scale."""
    d = np.asarray(d, dtype=float)
    n = len(d)
    out = dict(n=n, level=level, geometric_mean_ratio=None, median_ratio=None,
               mean_log=None, sd_log=None, t_ci=None, t_ci_log=None, bootstrap_ci=None)
    if n:
        mean = float(np.mean(d))
        out.update(mean_log=mean, geometric_mean_ratio=math.exp(mean),
                   median_ratio=float(np.median(np.exp(d))))
    if n < 2:
        out['reason'] = 'fewer than 2 valid triplets'
        return out
    sd = float(np.std(d, ddof=1))
    half = float(stats.t.ppf((1 + level) / 2, n - 1)) * sd / math.sqrt(n)
    bounds = [mean - half, mean + half]
    rng = np.random.default_rng(seed)
    boot = np.mean(d[rng.integers(0, n, size=(10000, n))], axis=1)
    tail = (1 - level) * 50
    out.update(sd_log=sd, t_ci_log=bounds, t_ci=[math.exp(v) for v in bounds],
               bootstrap_ci=np.exp(np.percentile(boot, [tail, 100 - tail])).tolist())
    return out


def tost(d, seed=SEED):
    out = paired_stats(d, .90, seed)
    out.update(p_lower=None, p_upper=None, p_tost=None)
    if out['n'] < 2:
        return out
    mean, sd, n = out['mean_log'], out['sd_log'], out['n']
    if sd == 0:
        lower = float(mean <= -MARGIN)
        upper = float(mean >= MARGIN)
    else:
        se = sd / math.sqrt(n)
        lower = float(stats.t.sf((mean + MARGIN) / se, n - 1))
        upper = float(stats.t.cdf((mean - MARGIN) / se, n - 1))
    out.update(p_lower=lower, p_upper=upper, p_tost=max(lower, upper))
    return out


def holm(pvalues):
    """None is an untestable planned hypothesis, counted as p=1 in the family."""
    order = sorted(pvalues, key=lambda k: (1 if pvalues[k] is None else pvalues[k], k))
    result, running = {}, 0.
    for i, key in enumerate(order):
        p = 1 if pvalues[key] is None else pvalues[key]
        running = max(running, min(1., (len(order) - i) * p))
        result[key] = None if pvalues[key] is None else running
    return result


def page_test(rows):
    if len(rows) < 3:
        return dict(n=len(rows), L=None, p=None, method=None, mean_ranks=None,
                    geometric_mean_S=None, reason='fewer than 3 complete triplets across four levels')
    data = np.asarray(rows)
    test = stats.page_trend_test(data, predicted_ranks=[4, 3, 2, 1], method='auto')
    return dict(n=len(rows), L=float(test.statistic), p=float(test.pvalue), method=test.method,
                mean_ranks=np.mean(stats.rankdata(data, axis=1), axis=0).tolist(),
                geometric_mean_S=np.exp(np.mean(np.log(data), axis=0)).tolist())


def load_json(path):
    try:
        return json.loads(path.read_text(encoding='utf-8'),
                          parse_constant=lambda s: (_ for _ in ()).throw(ValueError(s)))
    except (OSError, ValueError) as exc:
        raise ValueError(f'unreadable or invalid JSON {path}: {exc}') from exc


def cell_config(cell):
    if cell == 'ANCHOR':
        return dict(reader_concurrency=4, read_io_delay_us=0, read_path='block')
    match = re.fullmatch(r'X[123]_r(01|04|16)(?:_(d0000|d0020|d0200|d2000|legacy))?', cell)
    require(match is not None and cell in CELLS, f'unknown cell {cell}')
    r, dose = match.groups()
    return dict(reader_concurrency=int(r), read_io_delay_us=0 if dose in (None, 'legacy') else int(dose[1:]),
                read_path='legacy_scan' if dose == 'legacy' else 'block')


def number(value, name):
    require(value is None or (isinstance(value, (int, float)) and not isinstance(value, bool)
                             and math.isfinite(value) and value >= 0), f'invalid numeric outcome {name}')
    return value


def outcomes(t):
    r = t['result']
    tp, lat, sync = r['throughput'], r['latency_read_nanos'], r['sync_instrumentation']
    cs = sync['read_critical_section_nanos']
    require(isinstance(cs, dict) and len(cs) == 1, 'read critical section must have exactly one arm entry')
    cs = next(iter(cs.values()))
    wait = sync['writer_lock_wait_nanos']
    # The runner omits a histogram entirely when no such event occurred.
    commit, pub = wait.get('commit', {}), wait.get('flush_publish', {})
    diag = r['lsm_diagnostics']
    out = dict(W=tp.get('write_ops_per_sec'), R=tp.get('read_ops_per_sec'),
               read_p50=lat.get('p50'), read_p99=lat.get('p99'),
               read_cs_mean=cs.get('mean_nanos'), read_cs_p99=cs.get('p99_nanos'),
               commit_wait_p50=commit.get('p50_nanos'), commit_wait_p99=commit.get('p99_nanos'),
               publication_lock_wait_p50=pub.get('p50_nanos'),
               publication_lock_wait_p99=pub.get('p99_nanos'))
    for key, obj, fields in [('latency', lat, ('p95', 'mean')), ('cs', cs, ('p50_nanos', 'count')),
                             ('commit', commit, ('mean_nanos',)),
                             ('publication', pub, ('mean_nanos', 'count', 'max_nanos'))]:
        for f in fields if obj else ():
            require(f in obj, f'missing {key}.{f}')
            number(obj[f], f'{key}.{f}')
    for key in ('publication_events_realized_in_window', 'publication_events_realized_late', 'publication_events_target'):
        require(key in diag, f'missing {key}')
        number(diag[key], key)
    for key, value in out.items():
        number(value, key)
    # Missing event histograms are structural absences, not failed measurements.
    na = []
    realized = diag.get('publication_events_realized',
                        (diag['publication_events_realized_in_window'] or 0) +
                        (diag['publication_events_realized_late'] or 0))
    for metric in METRICS:
        if t['cell'] in X2 and metric in ('W', 'commit_wait_p50', 'commit_wait_p99'):
            if out[metric] in (None, 0):
                na.append(metric)
        if t['cell'] in X1 + X3 and metric.startswith('publication_lock_wait_'):
            if not realized and (not pub or pub.get('count') == 0) and out[metric] in (None, 0):
                na.append(metric)
    out['not_applicable'] = na
    out['publications'] = diag['publication_events_realized_in_window']
    return out


def load_campaign(root, allow_partial, campaign_id='X-readcost-v1',
                  config_reader=cell_config, outcome_reader=outcomes):
    plan = load_json(root / 'plan.json')
    require(plan['campaign_id'] == campaign_id, 'wrong campaign_id')
    require(isinstance(plan['cells'], list) and isinstance(plan['trials'], list) and plan['trials'], 'invalid plan schema')
    cell_specs = {c['id']: c['spec'] for c in plan['cells']}
    specs, groups, seeds = {}, {}, {}
    for s in plan['trials']:
        tid = s['trial_id']
        require(isinstance(tid, str) and re.fullmatch(r'[A-Za-z0-9_-]+', tid), 'invalid trial_id')
        require(tid not in specs and s['arm'] in ARMS, f'duplicate trial or invalid arm {tid}')
        require(type(s['triplet']) is int and type(s['seed']) is int, f'invalid triplet/seed {tid}')
        cfg = config_reader(s['cell'])
        require(s['block'] == cfg.get('block', 'X1' if s['cell'] == 'ANCHOR' else s['cell'][:2]), f'block mismatch {tid}')
        require(s['cell'] == 'ANCHOR' or s['cell'] in cell_specs, f'missing planned cell {tid}')
        for k, v in cfg.items():
            require(s.get(k, cell_specs.get(s['cell'], {}).get(k, v)) == v, f'plan config mismatch {tid}: {k}')
        key = (s['cell'], s['triplet'])
        require(key not in seeds or seeds[key] == s['seed'], f'seeds differ across arms {key}')
        seeds[key] = s['seed']
        require(s['arm'] not in groups.setdefault(key, {}), f'duplicate arm {key}')
        groups[key][s['arm']] = tid
        specs[tid] = s
    require(all(set(g) == set(ARMS) for g in groups.values()), 'plan has incomplete triplet')
    ledger_path = root / 'ledger.jsonl'
    ledger = {}
    try:
        lines = ledger_path.read_text(encoding='utf-8').splitlines()
        for line in lines:
            e = json.loads(line)
            tid, attempt = e['trial_id'], e['attempt']
            require(tid in specs and type(attempt) is int and attempt >= 1, 'invalid ledger trial/attempt')
            require(e['event'] in ('STARTED', 'FINISHED') and isinstance(e['status'], str)
                    and type(e['exit_code']) is int, 'invalid ledger record')
            events = ledger.setdefault(tid, {}).setdefault(attempt, {})
            require(e['event'] not in events, f'duplicate ledger event {tid} a{attempt}')
            events[e['event']] = e
    except (OSError, ValueError, KeyError, TypeError) as exc:
        raise ValueError(f'invalid ledger: {exc}') from exc
    missing, failed, revisions, hashes, records = [], [], set(), set(), {}
    failed_attempts_detail = []
    for tid, s in sorted(specs.items()):
        attempts = ledger.get(tid, {})
        if not attempts:
            missing.append(dict(trial_id=tid, reason='no ledger record'))
            continue
        # Validate all recorded attempts, not just the selected attempt.
        latest = max(attempts)
        for attempt, events in sorted(attempts.items()):
            final = events.get('FINISHED')
            path = root / 'trials' / f'{tid}_a{attempt}.json'
            present = path.exists()
            success = final is not None and final['status'] == 'SUCCESS'
            require(not success or present, f'missing trial file for SUCCESS {tid} a{attempt}')
            t, values = None, None
            if present:
                t = load_json(path)
                require(isinstance(t, dict), f'invalid trial envelope: {tid}')
                require(isinstance(t.get('status'), str) and t['status'], f'invalid status: {tid}')
                for key in ('cell', 'triplet', 'arm'):
                    if success or key in t:
                        require(t.get(key) == s[key], f'trial {key} disagrees with plan: {tid}')
                trial_spec = t.get('spec', {})
                require(isinstance(trial_spec, dict), f'invalid spec: {tid}')
                if success or 'seed' in trial_spec:
                    require(trial_spec.get('seed') == s['seed'], f'trial seed disagrees with plan: {tid}')
                runtime = t.get('runtime', {})
                require(isinstance(runtime, dict), f'invalid runtime: {tid}')
                if success or 'exe_sha256' in runtime:
                    runner = runtime.get('exe_sha256')
                    require(isinstance(runner, str) and re.fullmatch('[0-9a-fA-F]{64}', runner), f'invalid runner hash {tid}')
                    hashes.add(runner)
                if success:
                    require(t['status'] == 'SUCCESS', f'ledger SUCCESS/trial status mismatch {tid}')
                    require(final['exit_code'] == 0, f'SUCCESS with nonzero exit {tid}')
                    rev = t['result']['git_revision']
                    require(isinstance(rev, str) and rev, f'invalid git revision {tid}')
                    revisions.add(rev)
                    cfg = t['result']['config']
                    for k, v in config_reader(s['cell']).items():
                        require(cfg[k] == s.get(k, cell_specs.get(s['cell'], {}).get(k, v)), f'config disagrees with plan {tid}: {k}')
                    values = outcome_reader(t)
            if final is not None and not success:
                detail = dict(trial_id=tid, attempt=attempt, status=final['status'], artifact_present=present)
                if t is not None:
                    detail['envelope_status'] = t['status']
                    if final['status'] != t['status']:
                        detail['note'] = 'Ledger status overrides the trial envelope; outcomes were not used.'
                failed_attempts_detail.append(detail)
            if attempt == latest:
                if not final:
                    missing.append(dict(trial_id=tid, reason='latest attempt has no FINISHED record'))
                elif not success:
                    failed.append(tid)
                else:
                    records[tid] = values
    require(len(revisions) <= 1, 'more than one git_revision across trials')
    require(len(hashes) <= 1, 'more than one exe_sha256 across trials')
    require(allow_partial or not missing, f'missing trials ({len(missing)}); use --allow-partial for missing trials only')
    dropped = [dict(cell=c, triplet=t, unavailable_arms=[a for a in ARMS if g[a] not in records])
               for (c, t), g in sorted(groups.items()) if any(g[a] not in records for a in ARMS)]
    meta = dict(campaign_id=plan['campaign_id'], partial=bool(missing), git_revision=next(iter(revisions), None),
                runner_sha256=next(iter(hashes), None), bootstrap_seed=SEED, bootstrap_resamples=10000,
                plan_sha256=hashlib.sha256((root / 'plan.json').read_bytes()).hexdigest(),
                ledger_sha256=hashlib.sha256(ledger_path.read_bytes()).hexdigest(),
                counts=dict(planned=len(specs), attempted=len(ledger), attempts=sum(map(len, ledger.values())),
                            retried=sum(len(v) > 1 for v in ledger.values()), failed=len(failed),
                            failed_attempts=sum(e['FINISHED']['status'] != 'SUCCESS'
                                for attempts in ledger.values() for e in attempts.values() if 'FINISHED' in e),
                            dropped=len(dropped), missing=len(missing), successful=len(records)),
                missing_trials=missing, failed_trials=failed, dropped_triplets=dropped,
                failed_attempts_detail=failed_attempts_detail)
    return specs, groups, records, meta


def summary(values):
    vals = [v for v in values if v is not None]
    pos = [v for v in vals if v > 0]
    geo = paired_stats(np.log(pos))
    return dict(n=len(vals), mean=float(np.mean(vals)) if vals else None,
                sd=float(np.std(vals, ddof=1)) if len(vals) >= 2 else None,
                min=min(vals) if vals else None, max=max(vals) if vals else None,
                geometric_mean=geo['geometric_mean_ratio'], t_ci=geo['t_ci'],
                bootstrap_ci=geo['bootstrap_ci'], geometric_n=len(pos),
                geometric_excluded=len(vals) - len(pos),
                reason=geo.get('reason') if len(pos) == len(vals) else 'nonpositive values excluded from geometric summary')


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    exclusions, cells, logs = [], {}, {}
    for cell in CELLS:
        cg = {t: g for (c, t), g in sorted(groups.items()) if c == cell}
        complete = {t: g for t, g in cg.items() if all(g[a] in records for a in ARMS)}
        adjusted = [t for t, g in complete.items() if cell not in X2 and any(records[g[a]]['W'] == 0 for a in ARMS)]
        cs = dict(n_complete=len(complete), complete_triplets=list(complete), zero_adjusted=bool(adjusted),
                  zero_adjusted_triplets=adjusted, nominal=cell_config(cell), arms={}, ratios={})
        for arm in ARMS:
            arm_records = [records[g[arm]] for g in cg.values() if g[arm] in records]
            cs['arms'][arm] = {}
            for m in METRICS:
                applicable = [v[m] for v in arm_records if m not in v['not_applicable']]
                value = summary(applicable)
                value['not_applicable_n'] = len(arm_records) - len(applicable)
                if arm_records and not applicable:
                    value['reason'] = 'not applicable'
                cs['arms'][arm][m] = value
            # Explicitly list undefined geometric observations in descriptive summaries.
            for t, g in cg.items():
                if g[arm] in records:
                    for m in METRICS:
                        if m not in records[g[arm]]['not_applicable'] and records[g[arm]][m] in (None, 0):
                            exclusions.append(dict(cell=cell, triplet=t, arm=arm, outcome=m,
                                                   scope='per-arm geometric summary', reason='missing or zero'))
            if cell in X2:
                vals = [v['publications'] for v in arm_records if v['publications'] is not None]
                cs['arms'][arm]['publications'] = dict(values=vals, n=len(vals),
                    mean=float(np.mean(vals)) if vals else None, min=min(vals) if vals else None,
                    median=float(np.median(vals)) if vals else None, max=max(vals) if vals else None)
        for m in METRICS:
            for a, b in PAIRS:
                ds = {}
                for t, g in complete.items():
                    if any(m in records[g[arm]]['not_applicable'] for arm in (a, b)):
                        continue
                    x, y = records[g[a]][m], records[g[b]][m]
                    if x is None or y is None or (m != 'W' and (x == 0 or y == 0)):
                        exclusions.append(dict(cell=cell, triplet=t, outcome=m, pair=f'{a}/{b}',
                                               scope='paired ratio', reason='missing outcome' if x is None or y is None else 'zero non-W outcome'))
                        continue
                    offset = .1 if m == 'W' and t in adjusted else 0
                    ds[t] = math.log(x + offset) - math.log(y + offset)
                logs[cell, m, a, b] = ds
                if m in ('W', 'R', 'read_p50', 'read_p99'):
                    cs['ratios'].setdefault(m, {})[f'{a}/{b}'] = paired_stats(list(ds.values()))
        cells[cell] = cs
    out['cells'], out['outcome_exclusions'] = cells, exclusions
    out['H1'] = {}
    for r in (4, 16):
        names = [f'X1_r{r:02d}_d{d:04d}' for d in (0, 20, 200, 2000)]
        ds = [logs[c, 'W', 'A', 'AMIN'] for c in names]
        common = sorted(set.intersection(*(set(d) for d in ds)))
        h = page_test([[math.exp(d[t]) for d in ds] for t in common])
        h.update(triplets=common, levels_us=[0, 20, 200, 2000], alpha=.05,
                 legacy=cells[f'X1_r{r:02d}_legacy']['ratios']['W']['A/AMIN'])
        out['H1'][str(r)] = h
    out['H2'] = {}
    for m, family in [('W', X1), ('R', X1 + X2), ('publication_lock_wait_p99', X2)]:
        results = {c: tost(list(logs[c, m, 'AMIN', 'B'].values())) for c in family}
        adjusted_p = holm({c: v['p_tost'] for c, v in results.items()})
        for c, v in results.items():
            p, ci = adjusted_p[c], v['t_ci_log']
            v['p_holm'] = p
            v['classification'] = ('equivalent' if p is not None and p < .05 else
                'not_equivalent' if ci is not None and (ci[1] < -MARGIN or ci[0] > MARGIN) else 'inconclusive')
        out['H2'][m] = results
    out['H3'] = {c: {m: paired_stats(list(logs[c, m, 'AMIN', 'B'].values())) for m in ('R', 'W')} for c in X3}
    drift = {}
    for arm in ARMS:
        def rate(t):
            g = groups.get(('ANCHOR', t), {})
            return records.get(g.get(arm), {}).get('R')
        start = rate(0)
        d = {}
        for t, name in ((5, 't05'), (10, 't10')):
            end = rate(t)
            ratio = end / start if start and end else None
            d[name] = dict(ratio=ratio, log_ratio=math.log(ratio) if ratio else None,
                           reason=None if ratio else 'missing or zero anchor R')
        drift[arm] = d
    log_d = drift['AMIN']['t10']['log_ratio']
    out['drift'] = dict(arms=drift, D=drift['AMIN']['t10']['ratio'], ln_D=log_d,
                        threat_to_validity=abs(log_d) > .10 if log_d is not None else None)
    out['notes'] = ['Publication-wait p99 over 20 events is close to the maximum.',
                    'Holm uses the full planned family; untestable hypotheses count as p=1.',
                    'Per-arm arithmetic summaries retain zeros and successful observations from incomplete triplets; geometric summaries exclude missing/zero values.',
                    'W+0.1 applies to all paired W ratios in a complete triplet with any zero arm; per-arm summaries remain unadjusted.',
                    'Not applicable (no per-triplet exclusion rows): X2 zero/missing W and commit waits; X1/X3 publication waits when no publication histogram/events exist. Positive observed values remain summarized and tested.',
                    'The ledger determines each attempt status. Failed FINISHED attempts may lack artifacts or result fields; only the latest attempt determines a trial outcome.',
                    'Latency and critical-section/wait outcomes are in nanoseconds; dose is nominal microseconds.',
                    'Smoke data are mechanical verification only, excluded from scientific campaign inference.']
    out['equivalence_margin_log'] = MARGIN
    return out


def fmt(v):
    if v is None:
        return 'NA'
    if isinstance(v, float):
        return f'{v:.6g}'
    if isinstance(v, (list, dict)):
        return json.dumps(v, sort_keys=True, ensure_ascii=True)
    return str(v)


def tables(out):
    lines = ['# X-readcost-v1 analysis', '']
    if out['partial']:
        lines[0:0] = ['> **PARTIAL DATA: missing trials; inferential results are incomplete.**', '']
    def table(title, headers, rows):
        lines.extend(['## ' + title, '', '| ' + ' | '.join(headers) + ' |',
                      '| ' + ' | '.join(['---'] * len(headers)) + ' |'])
        lines.extend('| ' + ' | '.join(fmt(v).replace('|', '\\|') for v in row) + ' |' for row in rows)
        lines.append('')
    table('Provenance and counts', ['Field', 'Value'], [(k, out[k]) for k in
        ('campaign_id', 'plan_sha256', 'ledger_sha256', 'git_revision', 'runner_sha256', 'counts')])
    table('H1: decreasing Page trend', ['r', 'n', 'L', 'p', 'method', 'mean ranks', 'geometric S', 'legacy S', 'reason'],
          [(r, h['n'], h['L'], h['p'], h['method'], h['mean_ranks'], h['geometric_mean_S'],
            h['legacy']['geometric_mean_ratio'], h.get('reason')) for r, h in out['H1'].items()])
    for m, family in out['H2'].items():
        table('H2: ' + m, ['Cell', 'n', 'A-min/B', '90% t CI', '90% bootstrap CI', 'p lower', 'p upper', 'p TOST', 'p Holm', 'Class', 'reason'],
              [(c, v['n'], v['geometric_mean_ratio'], v['t_ci'], v['bootstrap_ci'], v['p_lower'], v['p_upper'],
                v['p_tost'], v['p_holm'], v['classification'], v.get('reason')) for c, v in family.items()])
    table('H3: churn', ['Cell', 'Outcome', 'n', 'A-min/B', '95% t CI', '95% bootstrap CI', 'reason'],
          [(c, m, v['n'], v['geometric_mean_ratio'], v['t_ci'], v['bootstrap_ci'], v.get('reason'))
           for c, ms in out['H3'].items() for m, v in ms.items()])
    table('Per-cell summary (rates in ops/s; durations in ns)',
          ['Cell', 'Arm', 'Outcome', 'n', 'Mean', 'SD', 'Min', 'Max', 'Geometric mean', '95% t CI', '95% bootstrap CI', 'Geometric n', 'reason'],
          [(c, a, m, v['n'], v['mean'], v['sd'], v['min'], v['max'], v['geometric_mean'], v['t_ci'], v['bootstrap_ci'], v['geometric_n'], v['reason'])
           for c, cs in out['cells'].items() for a, ms in cs['arms'].items() for m, v in ms.items() if m in METRICS])
    table('Paired descriptive ratios', ['Cell', 'Outcome', 'Pair', 'n', 'Geometric ratio', '95% t CI', '95% bootstrap CI'],
          [(c, m, p, v['n'], v['geometric_mean_ratio'], v['t_ci'], v['bootstrap_ci']) for c, cs in out['cells'].items()
           for m, ps in cs['ratios'].items() for p, v in ps.items()])
    table('Measured dose', ['Cell', 'Path', 'Nominal delay (us)', 'Arm', 'Mean trial mean read CS (ns)', 'Mean trial p99 read CS (ns)'],
          [(c, cs['nominal']['read_path'], cs['nominal']['read_io_delay_us'], a, ms['read_cs_mean']['mean'], ms['read_cs_p99']['mean'])
           for c, cs in out['cells'].items() for a, ms in cs['arms'].items()])
    table('X2 publications out of 20', ['Cell', 'Arm', 'Values', 'Min', 'Median', 'Max'],
          [(c, a, v['values'], v['min'], v['median'], v['max']) for c in X2
           for a, ms in out['cells'][c]['arms'].items() for v in [ms['publications']]])
    table('Drift', ['Arm', 'Anchor', 'R / t00', 'log ratio'], [(a, t, v['ratio'], v['log_ratio'])
          for a, ts in out['drift']['arms'].items() for t, v in ts.items()])
    lines.extend(['Threat to validity: ' + fmt(out['drift']['threat_to_validity']), ''])
    table('Failed attempts', ['Trial', 'Attempt', 'Ledger status', 'Artifact present', 'Envelope status', 'Note'],
          [(v['trial_id'], v['attempt'], v['status'], v['artifact_present'], v.get('envelope_status'), v.get('note'))
           for v in out['failed_attempts_detail']])
    table('Exclusions and zero adjustments', ['Kind', 'Details'],
          [('missing', v) for v in out['missing_trials']] + [('failed', v) for v in out['failed_trials']] +
          [('dropped triplet', v) for v in out['dropped_triplets']] + [('outcome', v) for v in out['outcome_exclusions']] +
          [('zero-adjusted W', dict(cell=c, triplets=cs['zero_adjusted_triplets'])) for c, cs in out['cells'].items() if cs['zero_adjusted']])
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
        markdown = tables(out)
        args.output_dir.mkdir(parents=True, exist_ok=True)
        (args.output_dir / 'x_readcost_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir / 'x_readcost_tables.md').write_text(markdown, encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
