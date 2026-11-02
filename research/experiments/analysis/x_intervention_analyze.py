#!/usr/bin/env python3
"""Frozen X-intervention-v1 analysis; reads artifacts and never launches trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import statistics
import sys

# Share numeric/envelope outcome checks and the existing Student t distribution.
_spec = importlib.util.spec_from_file_location(
    'x_readcost_helpers', Path(__file__).with_name('x_readcost_analyze.py'))
base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(base)
require, load_json, fmt = base.require, base.load_json, base.fmt
ARMS = ('A', 'AMIN')
CONDITIONS = ('second', 'inline')
CELLS = [f'I1_r{r:02d}_d{d:04d}' for r in (4, 16) for d in (0, 20, 200, 2000)]
RUNS = [(c, a) for c in CONDITIONS for a in ARMS]
METRICS = ('q', 'W', 'R', 'T', 'commit_wait_seconds', 'reads', 'commits')


def paired_stats(logs):
    """Two-sided 95% Student t CI; no resampling or outcome adjustments."""
    n = len(logs)
    out = dict(n=n, mean_log=None, geometric_mean_ratio=None, t_ci_log=None, t_ci=None)
    if n:
        mean = statistics.mean(logs)
        out.update(mean_log=mean, geometric_mean_ratio=math.exp(mean))
    if n >= 2:
        half = float(base.stats.t.ppf(.975, n - 1)) * statistics.stdev(logs) / math.sqrt(n)
        bounds = [mean - half, mean + half]
        out.update(t_ci_log=bounds, t_ci=[math.exp(v) for v in bounds])
    else:
        out['reason'] = 'fewer than 2 defined paired observations'
    return out


def summary(values):
    defined = [v for v in values if v is not None]
    positive = [v for v in defined if v > 0]
    geo = paired_stats([math.log(v) for v in positive])
    return dict(n=len(values), defined_n=len(defined), undefined_n=len(values) - len(defined),
                mean=statistics.mean(defined) if defined else None,
                values=values, geometric_mean=geo['geometric_mean_ratio'],
                geometric_n=len(positive), geometric_undefined_n=len(values) - len(positive),
                t_ci=geo['t_ci'])


def log_ratio(x, y):
    return math.log(x) - math.log(y) if x is not None and y is not None and x > 0 and y > 0 else None


def ratio_stats(pairs):
    logs = [log_ratio(x, y) for x, y in pairs]
    out = paired_stats([v for v in logs if v is not None])
    out['undefined_n'] = len(logs) - out['n']
    return out


def verdict(tests, partial=False):
    if any(v is False for v in tests):
        return 'not_supported'
    if not tests or partial or any(v is None for v in tests):
        return 'inconclusive'
    return 'supported'


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    return dict(reader_concurrency=int(cell[4:6]), read_io_delay_us=int(cell[8:]),
                read_path='block', writer_concurrency=1, memtable_threshold_bytes=1 << 20,
                measurement_duration_ms=10000, warmup_duration_ms=2000, cooldown_duration_ms=200,
                keyspace_cardinality=10000, initial_sstable_count=4, initial_keys_per_sstable=2500,
                expected_reader_set_size=4, target_read_hit_ratio=.95, read_distribution='uniform',
                wal_sync=True, instrument_sync=True, block='X1', workload_id='X-readcost')


def field(obj, key):
    # The Go spec omits zero dose and false InlineFlushCheck.
    return obj.get(key, False if key == 'inline_flush_check' else 0 if key == 'read_io_delay_us' else None)


def check_spec(actual, expected, context):
    require(isinstance(actual, dict), f'invalid spec/config: {context}')
    require(type(field(actual, 'inline_flush_check')) is bool, f'invalid inline_flush_check: {context}')
    for key, value in expected.items():
        if key != 'data_dir':
            require(field(actual, key) == value, f'{context}: {key} disagrees with plan')
    require(field(actual, 'inline_flush_check') == field(expected, 'inline_flush_check'),
            f'{context}: inline_flush_check disagrees with plan')


def trial_outcomes(t, s):
    base.outcomes(t)  # Existing histogram, latency, diagnostic and numeric checks.
    r = t['result']
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    ops = r['operations']
    require(ops['validation_error_count'] == 0, 'SUCCESS with validation errors')
    require(ops['read_ops_completed'] > 0 and r['timestamps']['measured_window_ns'] > 0,
            'invalid successful window')
    reads, commits = ops['read_ops_completed_in_window'], ops['write_ops_completed_in_window']
    for key, value in [('reads', reads), ('commits', commits)]:
        require(type(value) is int and value >= 0, f'invalid in-window {key}')
    wait = r['sync_instrumentation']['writer_lock_wait_nanos'].get('commit', {}).get('mean_nanos')
    base.number(wait, 'commit.mean_nanos')
    return dict(reads=reads, commits=commits, q=reads / (s['reader_concurrency'] * commits) if commits else None,
                W=commits / 10, R=reads / 10, T=10 / commits if commits else None,
                commit_wait_seconds=wait / 1e9 if wait is not None else None,
                recorded_throughput=r['throughput'], measured_window_ns=r['timestamps']['measured_window_ns'])


def load_campaign(root, allow_partial=False):
    root = Path(root)
    plan = load_json(root / 'plan.json')
    require(plan['campaign_id'] == 'X-intervention-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and plan['master_seed'] >= 0, 'invalid master seed')
    require(len(plan['cells']) == 8 and {c['id'] for c in plan['cells']} == set(CELLS), 'invalid cells')
    for c in plan['cells']:
        check_spec(c['spec'], nominal(c['id']), c['id'])
    specs, groups = {}, {}
    require(len(plan['trials']) == 160, 'plan must contain 160 trials')
    for s in plan['trials']:
        cell, condition = s['cell'].rsplit('_', 1)
        arm, k, tid = s['arm'], s['triplet'], s['trial_id']
        require(cell in CELLS and condition in CONDITIONS and arm in ARMS, 'invalid run')
        require(type(k) is int and 1 <= k <= 5 and type(s['seed']) is int, 'invalid round/seed')
        require(tid == f'{cell}_{condition}_t{k:02d}_{arm}' and tid not in specs, 'invalid/duplicate trial ID')
        expected = dict(nominal(cell), inline_flush_check=condition == 'inline')
        check_spec(s, expected, tid)
        seed_hash = hashlib.sha256(f"{plan['master_seed']}|{cell}|{k}".encode()).digest()
        require(s['seed'] == int.from_bytes(seed_hash[:8], 'big', signed=True), f'incorrect paired seed {tid}')
        g = groups.setdefault((cell, k), {})
        require((condition, arm) not in g, 'duplicate run in cell-round')
        g[condition, arm] = tid
        specs[tid] = s
    require(len(groups) == 40 and all(set(g) == set(RUNS) for g in groups.values()), 'incomplete design')
    ledger_path = root / 'ledger.jsonl'
    ledger = {}
    for line in ledger_path.read_text(encoding='utf-8').splitlines():
        e = json.loads(line)
        tid, attempt = e['trial_id'], e['attempt']
        require(tid in specs and type(attempt) is int and 1 <= attempt <= 2, 'invalid ledger trial/attempt')
        require(e['event'] in ('STARTED', 'FINISHED') and isinstance(e['status'], str)
                and type(e['exit_code']) is int, 'invalid ledger record')
        events = ledger.setdefault(tid, {}).setdefault(attempt, {})
        require(e['event'] not in events, 'duplicate ledger event')
        events[e['event']] = e
    records, missing, failed, failed_attempts = {}, [], [], []
    revisions, hashes = set(), set()
    for tid, s in sorted(specs.items()):
        attempts = ledger.get(tid, {})
        if not attempts:
            missing.append(dict(trial_id=tid, reason='no ledger record'))
            continue
        latest = max(attempts)
        for attempt, events in sorted(attempts.items()):
            final = events.get('FINISHED')
            success = final is not None and final['status'] == 'SUCCESS'
            path = root / 'trials' / f'{tid}_a{attempt}.json'
            present = path.exists()
            require(not success or present, f'missing SUCCESS artifact {tid} a{attempt}')
            values = None
            t = load_json(path) if present else None
            if present:
                require(isinstance(t, dict) and isinstance(t.get('status'), str) and t['status'], f'invalid envelope {tid}')
                for key in ('trial_id', 'cell', 'triplet', 'arm', 'block'):
                    if success or key in t:
                        require(t.get(key) == s[key], f'envelope {key} mismatch {tid}')
                trial_spec = t.get('spec', {})
                require(isinstance(trial_spec, dict), f'invalid spec {tid}')
                if success or 'seed' in trial_spec:
                    require(trial_spec.get('seed') == s['seed'], f'envelope seed mismatch {tid}')
                runtime = t.get('runtime', {})
                require(isinstance(runtime, dict), f'invalid runtime {tid}')
                if success or 'exe_sha256' in runtime:
                    runner = runtime.get('exe_sha256')
                    require(isinstance(runner, str) and re.fullmatch('[0-9a-fA-F]{64}', runner), 'invalid runner hash')
                    hashes.add(runner.lower())
            if success:
                require(t['schema'] == 'i7.trial.v1' and t['status'] == 'SUCCESS'
                        and final['exit_code'] == 0, f'ledger/envelope mismatch {tid}')
                check_spec(t['spec'], s, f'envelope {tid}')
                check_spec(t['result']['config'], s, f'result {tid}')
                rev = t['result']['git_revision']
                require(isinstance(rev, str) and rev, 'invalid git revision')
                revisions.add(rev)
                values = trial_outcomes(t, s)
            if final is not None and not success:
                failed_attempts.append(dict(trial_id=tid, attempt=attempt, status=final['status'],
                                            artifact_present=present, envelope_status=t.get('status') if t else None))
            if attempt == latest:
                if final is None:
                    missing.append(dict(trial_id=tid, reason='latest attempt unfinished'))
                elif success:
                    records[tid] = values
                elif attempt < 2:
                    missing.append(dict(trial_id=tid, reason='retry not yet completed'))
                else:
                    failed.append(tid)
    require(len(revisions) <= 1 and len(hashes) <= 1, 'mixed git revisions or runners')
    require(allow_partial or not missing, f'missing trials ({len(missing)}); use --allow-partial')
    dropped = [dict(cell=c, round=k, unavailable_runs=[g[run] for run in RUNS if g[run] not in records])
               for (c, k), g in sorted(groups.items()) if any(tid not in records for tid in g.values())]
    plan_hash = hashlib.sha256((root / 'plan.json').read_bytes()).hexdigest()
    platform_path = root / 'platform.json'
    platform = load_json(platform_path) if platform_path.exists() else None
    runner, revision = next(iter(hashes), None), next(iter(revisions), None)
    if platform is not None:
        require(platform['campaign_id'] == plan['campaign_id'] and platform['plan_sha256'] == plan_hash,
                'platform campaign/plan mismatch')
        require(not runner or platform['runner_sha256'].lower() == runner, 'platform runner mismatch')
        require(not revision or platform['git_head'] == revision, 'platform git mismatch')
    out = dict(campaign_id=plan['campaign_id'], master_seed=plan['master_seed'], plan_sha256=plan_hash,
               ledger_sha256=hashlib.sha256(ledger_path.read_bytes()).hexdigest(),
               runner_sha256=runner, git_revision=revision, platform=platform, partial=bool(missing),
               counts=dict(planned=len(specs), attempted=len(ledger), attempts=sum(map(len, ledger.values())),
                           retried=sum(len(v) > 1 for v in ledger.values()), successful=len(records),
                           failed=len(failed), failed_attempts=len(failed_attempts), missing=len(missing),
                           dropped_units=len(dropped)),
               missing_trials=missing, failed_trials=failed, failed_attempts=failed_attempts, dropped_units=dropped)
    return specs, groups, records, out


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    out['trials'] = {tid: dict(cell=specs[tid]['cell'], round=specs[tid]['triplet'], arm=specs[tid]['arm'], **v)
                     for tid, v in sorted(records.items())}
    cells, p1, p2, cycle_tests, improvement_tests = {}, [], [], [], []
    for cell in CELLS:
        complete = {k: g for (c, k), g in sorted(groups.items())
                    if c == cell and all(tid in records for tid in g.values())}
        def values(condition, arm, metric):
            return [records[g[condition, arm]][metric] for g in complete.values()]
        cs = dict(n_complete=len(complete), complete_rounds=list(complete), nominal=nominal(cell), conditions={})
        for condition in CONDITIONS:
            arms = {arm: {m: summary(values(condition, arm, m)) for m in METRICS} for arm in ARMS}
            ratios = {m: ratio_stats(zip(values(condition, 'A', m), values(condition, 'AMIN', m))) for m in ('R', 'W', 'T')}
            q = arms['A']['q']
            bounds = [1.90, 2.10] if condition == 'second' else [.90, 1.10]
            ok = bounds[0] <= q['mean'] <= bounds[1] if q['defined_n'] and not q['undefined_n'] else None
            p1.append(ok)
            means = [arms[a][m]['mean'] for a, m in [('A', 'T'), ('AMIN', 'T'), ('A', 'commit_wait_seconds')]]
            defined = all(arms[a][m]['defined_n'] and not arms[a][m]['undefined_n']
                          for a, m in [('A', 'T'), ('AMIN', 'T'), ('A', 'commit_wait_seconds')])
            actual, amin, wait = means
            multiplier = 2 if condition == 'second' else 1
            predicted = amin + multiplier * wait if defined else None
            error = (predicted - actual) / actual if predicted is not None and actual else None
            cycle_ok = abs(error) <= .15 if error is not None else None
            cycle_tests.append(cycle_ok)
            cs['conditions'][condition] = dict(arms=arms, ratios_A_over_AMIN=ratios,
                P1=dict(bounds=bounds, supported=ok),
                cycle=dict(observed_T_A=actual, T_AMIN=amin, w_A=wait, waits=multiplier,
                           predicted_T_A=predicted, relative_error=error, supported=cycle_ok))
        cross = {}
        for arm in ARMS:
            cross[arm] = {m: ratio_stats(zip(values('inline', arm, m), values('second', arm, m))) for m in ('W', 'R', 'T', 'q')}
        cs['ratios_inline_over_second'] = cross
        logs = []
        for g in complete.values():
            rs = [records[g[c, a]]['R'] for c, a in RUNS]
            second, inline = log_ratio(rs[0], rs[1]), log_ratio(rs[2], rs[3])
            if second is not None and inline is not None:
                logs.append(inline - second)
        double = paired_stats(logs)
        double['undefined_n'] = len(complete) - len(logs)
        dose = cs['nominal']['read_io_delay_us']
        eligible = dose in (0, 20, 200)
        ci = double['t_ci_log']
        ok = ci[1] < 0 if ci and not double['undefined_n'] else None
        cs['P2'] = dict(applicable=eligible, ratio_of_read_ratios=double, supported=ok if eligible else None)
        if eligible:
            p2.append(ok)
        eligible = dose in (200, 2000)
        stat = cross['A']['W']
        ok = stat['t_ci'][0] > 1 if stat['t_ci'] and not stat['undefined_n'] else None
        cs['P3_writer_improvement'] = dict(applicable=eligible, supported=ok if eligible else None)
        if eligible:
            improvement_tests.append(ok)
        cells[cell] = cs
    out.update(cells=cells, P1=dict(verdict=verdict(p1, out['partial']), supported_cells=sum(v is True for v in p1), planned=16),
               P2=dict(verdict=verdict(p2, out['partial']), supported_cells=sum(v is True for v in p2), planned=6,
                       note='No multiplicity correction; n=5 planned; descriptive strength only.'),
               P3=dict(verdict=verdict(cycle_tests + improvement_tests, out['partial']),
                       cycle_verdict=verdict(cycle_tests, out['partial']),
                       writer_improvement_verdict=verdict(improvement_tests, out['partial'])))
    out['notes'] = ['All summaries and paired tests use complete four-run cell-round units.',
                    'Trial-level successful outcomes retain observations from dropped units for inspection.',
                    'q condition-cell means are arithmetic. Cycle predictions use arithmetic mean times and waits in seconds.',
                    'W and R use the fixed 10-second window; recorded throughput and measured window are also retained.',
                    'Zeros remain data; undefined logs, q or T are counted and make the affected prediction inconclusive.',
                    'If P1 fails, the two-acquisition explanation was not confirmed. Partial support is partial.',
                    'Mechanical smoke data are excluded from scientific inference.']
    return out


def row_values(cell, cs):
    second, inline = [cs['conditions'][c] for c in CONDITIONS]
    return [cs['nominal']['reader_concurrency'], cs['nominal']['read_io_delay_us'],
            second['arms']['A']['q']['mean'], inline['arms']['A']['q']['mean'],
            second['arms']['A']['W']['geometric_mean'], inline['arms']['A']['W']['geometric_mean'],
            second['ratios_A_over_AMIN']['R'], inline['ratios_A_over_AMIN']['R']]


def ratio_text(value):
    point, ci = value['geometric_mean_ratio'], value['t_ci']
    return f'{fmt(point)} [{fmt(ci[0])}, {fmt(ci[1])}]' if ci else f'{fmt(point)} [NA]'


def tex_rows(out):
    rows = []
    for cell, cs in out['cells'].items():
        vals = row_values(cell, cs)
        rows.append(' & '.join([fmt(v) for v in vals[:6]] + [ratio_text(v) for v in vals[6:]]) + r' \\')
    return '\\gdef\\TblInterventionRows{\n' + '\n'.join(rows) + '\n}\n'


def tables(out):
    lines = ['# X-intervention-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: missing trials; inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## ' + title, '', '| ' + ' | '.join(headers) + ' |',
                      '| ' + ' | '.join(['---'] * len(headers)) + ' |'])
        lines.extend('| ' + ' | '.join(fmt(v).replace('|', '\\|') for v in row) + ' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'plan_sha256', 'ledger_sha256', 'runner_sha256', 'git_revision', 'counts', 'platform')])
    table('Predictions', ['Prediction', 'Result'], [(p, out[p]) for p in ('P1', 'P2', 'P3')])
    table('Cell results (W geometric means; read ratios with 95% paired t CI)',
          ['r', 'd (us)', 'q A second', 'q A inline', 'W A second', 'W A inline', 'R A/AMIN second', 'R A/AMIN inline'],
          [row_values(c, cs)[:6] + [ratio_text(v) for v in row_values(c, cs)[6:]] for c, cs in out['cells'].items()])
    table('Per-condition predictions and cycle model (seconds)', ['Cell', 'Condition', 'P1', 'Cycle'],
          [(c, condition, v['P1'], v['cycle']) for c, cs in out['cells'].items() for condition, v in cs['conditions'].items()])
    table('Paired predictions', ['Cell', 'Complete rounds', 'P2', 'P3 writer improvement', 'Inline/second ratios'],
          [(c, cs['complete_rounds'], cs['P2'], cs['P3_writer_improvement'], cs['ratios_inline_over_second']) for c, cs in out['cells'].items()])
    table('All outcomes (counts in-window; times in seconds)', ['Cell', 'Condition', 'Arm', 'Metric', 'Summary'],
          [(c, condition, a, m, v) for c, cs in out['cells'].items() for condition, cv in cs['conditions'].items()
           for a, metrics in cv['arms'].items() for m, v in metrics.items()])
    table('Within-condition paired ratios', ['Cell', 'Condition', 'Outcome', 'A/AMIN'],
          [(c, condition, m, v) for c, cs in out['cells'].items() for condition, cv in cs['conditions'].items()
           for m, v in cv['ratios_A_over_AMIN'].items()])
    table('Failures and dropped units', ['Kind', 'Details'],
          [(k, v) for k in ('missing_trials', 'failed_trials', 'failed_attempts', 'dropped_units') for v in out[k]])
    lines.extend(out['notes'])
    return '\n'.join(lines) + '\n'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--data-dir', type=Path, required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--allow-partial', action='store_true')
    parser.add_argument('--tex-out', type=Path)
    args = parser.parse_args()
    try:
        out = analyze(args.data_dir, args.allow_partial)
        encoded = json.dumps(out, sort_keys=True, indent=2, allow_nan=False) + '\n'
        args.output_dir.mkdir(parents=True, exist_ok=True)
        (args.output_dir / 'x_intervention_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir / 'x_intervention_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
        if args.tex_out:
            args.tex_out.parent.mkdir(parents=True, exist_ok=True)
            args.tex_out.write_text(tex_rows(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
