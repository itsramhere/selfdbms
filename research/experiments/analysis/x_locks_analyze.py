#!/usr/bin/env python3
"""X-locks-v1 preregistered analysis; reads artifacts, never launches trials."""
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
base, paced = settled.base, settled.paced
require, load_json = base.require, base.load_json
SEED, SETTLE_MS = 20261109, 20000
ENTRIES = [f'L1_r{r:02d}_d{d:04d}' for r in (4, 16) for d in (0, 20, 200, 2000)]
POLICIES, ARMS = ('go', 'phasefair', 'readerpref'), ('A', 'AMIN')
RUNS = [(p, a) for p in POLICIES for a in ARMS]
CELLS = [f'{e}_{p}' for e in ENTRIES for p in POLICIES]
ABOVE = 'above_all_bounds'
METRICS = ('R', 'W', 'q', 'reads', 'commits', 'read_p50', 'read_p99', 'write_p50',
           'write_p99', 'commit_wait_mean_ns', 'commit_wait_p50', 'commit_wait_p99', 'flushes')


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    entry, policy = cell.rsplit('_', 1)
    return dict(settled.intervention.nominal('I1'+entry[2:]),
                lock_policy='' if policy == 'go' else policy,
                diag_trace=False, inline_flush_check=False, persist_read_histogram=False,
                compaction_trigger_interval_ms=0)


def check_spec(actual, expected, context):
    require(isinstance(actual, dict), f'invalid spec {context}')
    settled.check_spec(dict(actual, lock_policy=actual.get('lock_policy', ''),
                            persist_read_histogram=actual.get('persist_read_histogram', False)), expected, context)
    require(actual.get('schema') == 'i7.trial-spec.v1', f'invalid spec schema {context}')
    for key in ('diag_trace', 'inline_flush_check', 'persist_read_histogram'):
        require(type(actual.get(key, False)) is bool and actual.get(key, False) == expected.get(key, False),
                f'unplanned treatment {context}: {key}')
    require(actual.get('lock_policy', '') == expected.get('lock_policy', ''),
            f'{context}: lock_policy disagrees with plan')


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-locks-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2**64, 'invalid master seed')
    require(type(plan['settle_ms']) is int and plan['settle_ms'] == SETTLE_MS, 'invalid settle_ms')
    require([c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        require(c['spec']['cell'] == c['id'], 'cell spec identity mismatch')
        check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 480, 'plan must contain 480 trials')
    units, seen = {}, set()
    for index, s in enumerate(plan['trials']):
        c, k, arm, tid = s['cell'], s['triplet'], s['arm'], s['trial_id']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and arm in ARMS, 'invalid run')
        entry, policy = c.rsplit('_', 1)
        require(tid == f'{c}_t{k:02d}_{arm}' and tid not in seen, 'invalid/duplicate run')
        seen.add(tid)
        check_spec(s, nominal(c), tid)
        h = hashlib.sha256(f"{plan['master_seed']}|{entry}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True), 'incorrect paired seed')
        require(s['runlist_position'] == index+1 and k == index//48+1, 'invalid round/runlist position')
        units.setdefault((entry, k), []).append((index, s))
    require(len(units) == 80, 'missing entry-round units')
    for rows in units.values():
        require(len(rows) == 6 and {(s['cell'].rsplit('_', 1)[1], s['arm']) for _, s in rows} == set(RUNS),
                'incomplete/duplicate six-run unit')
        require([i for i, _ in rows] == list(range(rows[0][0], rows[0][0]+6)), 'noncontiguous unit')
        require([s['order_position'] for _, s in rows] == list(range(1, 7)), 'invalid order positions')
    # Exact Go PRNG permutations are tested in locks_test.go. Here we check all
    # round, grouping, membership, position, treatment and seed invariants.
    return {s['trial_id']: s for s in plan['trials']}, {
        key: {(s['cell'].rsplit('_', 1)[1], s['arm']): s['trial_id'] for _, s in rows}
        for key, rows in units.items()}


def latency(h, count, context):
    require(type(h['sample_count']) is int and h['sample_count'] == count, f'{context} sample count mismatch')
    for key in ('min', 'max', 'mean', 'p50', 'p95', 'p99', 'p999'):
        base.number(h.get(key), f'{context}.{key}')
    require(h.get('p50') is not None, f'missing {context}.p50')
    require(count >= 100 or h.get('p99') is None, f'{context} p99 below sample threshold')
    return (h['p50'], h.get('p99')) if count else (None, None)


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    # This is deliberately campaign-local: earlier analyzers and their zero
    # handling are untouched. A zero count is retained, never retried/excluded.
    require(s['cell'] in CELLS, 'zero-commit rule is X-locks-v1 only')
    check_spec(s, nominal(s['cell']), s['trial_id'])
    ops, diag, window = r['operations'], r['lsm_diagnostics'], r['timestamps']['measured_window_ns']
    require(ops['validation_error_count'] == 0 and ops['error_ops_count'] == 0 and window > 0,
            'invalid successful window')
    require(diag.get('compaction_errors', 0) == 0 and diag['measured_compactions'] == 0
            and diag.get('compaction_successes', 0) == 0, 'unplanned compaction or engine fault')
    require(diag['initial_sstables'] == 4 and diag['final_sstables'] >= 4, 'invalid table counts')
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
    reads, commits = ops['read_ops_completed_in_window'], ops['write_ops_completed_in_window']
    require(ops['read_ops_completed'] > 0, 'invalid mixed read count')
    read50, read99 = latency(r['latency_read_nanos'], reads, 'read')
    write50, write99 = latency(r['latency_write_nanos'], commits, 'write')
    latency(r['latency_write_late_nanos'], ops['write_ops_completed_late'], 'late write')
    wait = r['sync_instrumentation']['writer_lock_wait_nanos'].get('commit', {})
    for key in ('count', 'mean_nanos', 'p50_nanos', 'p99_nanos'):
        base.number(wait.get(key), 'first-acquisition wait.'+key)
    out = dict(reads=reads, commits=commits, q=reads/(s['reader_concurrency']*commits) if commits else ABOVE,
               zero_commits=commits == 0, R=r['throughput']['read_ops_per_sec'],
               W=r['throughput']['write_ops_per_sec'], read_p50=read50, read_p99=read99,
               write_p50=write50, write_p99=write99, flushes=diag['measured_flushes'],
               commit_wait_mean_ns=wait.get('mean_nanos'), commit_wait_p50=wait.get('p50_nanos'),
               commit_wait_p99=wait.get('p99_nanos'), commit_wait_count=wait.get('count', 0),
               measured_window_ns=window, write_latency_late=r['latency_write_late_nanos'])
    for metric in METRICS:
        if metric != 'q':
            base.number(out[metric], metric)
    require(out['R'] is not None and out['W'] is not None
            and math.isclose(out['R'], reads/(window/1e9), rel_tol=1e-9, abs_tol=1e-9)
            and math.isclose(out['W'], commits/(window/1e9), rel_tol=1e-9, abs_tol=1e-9), 'throughput/count mismatch')
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


def arithmetic(values):
    defined = [v for v in values if v is not None]
    out = dict(n=len(defined), undefined_n=len(values)-len(defined), values=values,
               mean=None, t_ci=None, bootstrap_ci=None, level=.95)
    if ABOVE in defined:
        out.update(mean=ABOVE, above_all_bounds_n=defined.count(ABOVE),
                   reason='Student t CI undefined with infinite q; no finite substitution or exclusion')
        return out
    if defined:
        out['mean'] = sum(defined)/len(defined)
    if len(defined) >= 2:
        n = len(defined)
        half = float(base.stats.t.ppf(.975, n-1))*float(base.np.std(defined, ddof=1))/math.sqrt(n)
        rng = base.np.random.default_rng(SEED)
        draws = base.np.asarray(defined)[rng.integers(0, n, size=(10000, n))].mean(axis=1)
        out.update(t_ci=[out['mean']-half, out['mean']+half],
                   bootstrap_ci=base.np.percentile(draws, [2.5, 97.5]).tolist())
    return out


def complete_units(groups, records, entry):
    return {k: g for (e, k), g in sorted(groups.items()) if e == entry and all(tid in records for tid in g.values())}


def ratio(complete, records, numerator, denominator, metric, test=False):
    logs, rounds, excluded, adjusted = [], [], [], []
    for k, g in complete.items():
        x, y = records[g[numerator]][metric], records[g[denominator]][metric]
        if metric == 'W' and (x == 0 or y == 0):
            # Section 3 overrides X-paced: ONLY zero W is replaced, rather
            # than adding .1 to every arm in a unit containing a zero.
            x, y = .1 if x == 0 else x, .1 if y == 0 else y
            adjusted.append(k)
        if x is not None and y is not None and x > 0 and y > 0:
            logs.append(math.log(x)-math.log(y)); rounds.append(k)
        else:
            excluded.append(k)
    stat = base.tost(logs, seed=SEED) if test else base.paired_stats(logs, seed=SEED)
    return dict(stat, rounds=rounds, excluded_rounds=excluded, undefined_n=len(excluded),
                zero_adjusted_rounds=adjusted, numerator=list(numerator), denominator=list(denominator))


def verdict(tests, partial):
    if any(v is False for v in tests):
        return 'not_supported'
    if partial or not tests or any(v is None for v in tests):
        return 'inconclusive'
    return 'supported'


def q_decision(stat, primary):
    """Frozen H-L1/H-L2 rule; insufficient finite CI evidence is not met."""
    if stat.get('above_all_bounds_n', 0):
        return False if primary else all(v > 2.10 for v in stat['values'] if v != ABOVE and v is not None)
    ci = stat['t_ci']
    return bool(ci and (1.90 <= ci[0] and ci[1] <= 2.10 if primary else ci[0] > 2.10))


def prediction(results, partial, decisive=False):
    tests = [v['supported'] for v in results.values()]
    if decisive:
        supported = bool(tests) and all(tests) and not partial
        decision = 'supported' if supported else 'not_supported'
    else:
        supported = all(tests) if all(v is not None for v in tests) and not partial else None
        decision = verdict(tests, partial)
    return dict(settings=results, supported=supported, verdict=decision,
                supported_settings=sum(v is True for v in tests), planned=len(tests))


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells, h1, h2, h3, h4phase, h4reader, overhead, exclusions, drift = {}, {}, {}, {}, {}, {}, {}, [], {}
    for entry in ENTRIES:
        complete = complete_units(groups, records, entry)
        for policy in POLICIES:
            cell = entry+'_'+policy
            summaries, zero, all_zero, trends = {}, {}, {}, {}
            for arm in ARMS:
                values = [records[g[policy, arm]] for g in complete.values()]
                summaries[arm] = {m: arithmetic([v[m] for v in values]) for m in METRICS}
                zero[arm] = [k for k, g in complete.items() if records[g[policy, arm]]['zero_commits']]
                all_zero[arm] = [tid for tid, v in sorted(records.items()) if specs[tid]['cell'] == cell
                                and specs[tid]['arm'] == arm and v['zero_commits']]
                for m in METRICS:
                    undefined = [k for k, g in complete.items() if records[g[policy, arm]][m] is None]
                    if undefined:
                        exclusions.append(dict(cell=cell, arm=arm, metric=m, rounds=undefined,
                                               reason='undefined metric; observation retained'))
                rows = [(k, records[g[policy, arm]]['R']) for k, g in complete.items()]
                positive = [(k, math.log(v)) for k, v in rows if v is not None and v > 0]
                trends[arm] = dict(round_R=rows, log_R_slope_per_round=float(base.np.polyfit(*zip(*positive), 1)[0])
                                   if len(positive) >= 2 else None)
            s = ratio(complete, records, (policy, 'A'), (policy, 'AMIN'), 'W')
            rr = ratio(complete, records, (policy, 'A'), (policy, 'AMIN'), 'R')
            for metric, result in (('S', s), ('R_A/AMIN', rr)):
                if result['excluded_rounds']:
                    exclusions.append(dict(cell=cell, metric=metric, rounds=result['excluded_rounds'],
                                           reason='missing/nonpositive log pair'))
            cells[cell] = dict(nominal=nominal(cell), complete_rounds=list(complete), arms=summaries,
                               S=s, read_ratio_A_AMIN=rr, zero_commit_rounds=zero,
                               zero_commit_counts={a: len(zero[a]) for a in ARMS},
                               zero_commit_successful_trials=all_zero,
                               zero_commit_successful_counts={a: len(all_zero[a]) for a in ARMS})
            drift[cell] = trends
        stat = cells[entry+'_phasefair']['arms']['A']['q']
        h1[entry] = dict(stat, bounds=[1.90, 2.10], rounds=list(complete),
                         supported=q_decision(stat, primary=True))
        stat = cells[entry+'_readerpref']['arms']['A']['q']
        h2[entry] = dict(stat, lower_bound=2.10, rounds=list(complete),
                         supported=q_decision(stat, primary=False))
        stat = ratio(complete, records, ('readerpref', 'A'), ('go', 'A'), 'W')
        h3[entry] = dict(stat, supported=None if stat['t_ci'] is None else stat['t_ci'][1] < 1)
        h4phase[entry] = ratio(complete, records, ('phasefair', 'AMIN'), ('go', 'AMIN'), 'W', test=True)
        stat = ratio(complete, records, ('readerpref', 'AMIN'), ('go', 'AMIN'), 'W', test=True)
        if nominal(entry+'_go')['read_io_delay_us'] >= 200:
            h4reader[entry] = stat
        else:
            cells[entry+'_readerpref']['descriptive_W_AMIN_relative_go'] = ratio(
                complete, records, ('readerpref', 'AMIN'), ('go', 'AMIN'), 'W')
        overhead[entry] = {m: ratio(complete, records, ('phasefair', 'A'), ('go', 'A'), m) for m in ('W', 'R')}
    paced.adjust_family(h4phase, 'H-L4 phasefair/go (8)')
    paced.adjust_family(h4reader, 'H-L4 readerpref/go d>=200us (4)')
    for family in (h4phase, h4reader):
        for v in family.values():
            v['supported'] = None if v['p_tost'] is None else v['classification'] == 'equivalent'
    out.update(cells=cells, H_L1=prediction(h1, out['partial'], decisive=True), H_L2=prediction(h2, out['partial'], decisive=True),
               H_L3=prediction(h3, out['partial']),
               H_L4=dict(phasefair=prediction(h4phase, out['partial']), readerpref=prediction(h4reader, out['partial']),
                         verdict=verdict([v['supported'] for f in (h4phase, h4reader) for v in f.values()], out['partial'])),
               phasefair_overhead_CL_relative_go=overhead, round_trend_R=drift,
               equivalence_margin_log=base.MARGIN, outcome_exclusions=exclusions,
               trials={tid: dict(cell=specs[tid]['cell'], entry=specs[tid]['cell'].rsplit('_', 1)[0],
                                policy=specs[tid]['cell'].rsplit('_', 1)[1], round=specs[tid]['triplet'],
                                arm=specs[tid]['arm'], **v) for tid, v in sorted(records.items())},
               notes=[
                   'Complete six-run entry-round units supply every summary and paired test. Successful dropped-unit observations remain in trials. No pooling or outcome-based exclusions.',
                   'q=reads/(r*commits), in-window. A zero-commit q is above_all_bounds, never dropped or assigned a finite value. Its arithmetic mean is above_all_bounds; a Student t CI with infinity is undefined. Frozen zero-commit rule: H-L1 is not met; H-L2 is met exactly when every finite q in that setting is strictly above 2.10 (including when all rounds have zero commits). Otherwise the finite 95% t CI rules apply. H-L1/H-L2 always return met/not-met decisions; insufficient CI evidence or partial campaign data cannot establish support.',
                   'Raw W=0 remains 0 in per-arm summaries. ONLY zero W is replaced by 0.1 writes/s in log ratios; positive W is unchanged, including its paired comparator. Adjusted rounds are listed. No latency pseudo-counts.',
                   'S=W_CL/W_Lease; read ratio=R_CL/R_Lease. Ratios use paired logs and 95% Student t CIs; per-arm means use arithmetic 95% Student t CIs. H-L1 band is [1.90,2.10]; H-L2 requires its 95% CI above 2.10; H-L3 requires its paired 95% CI below 1. Each requires all eight settings.',
                   'H-L4 uses 90% paired log-t CIs, TOST margin +/-ln(1/0.90), Holm family of eight phasefair/go and separate family of four readerpref/go at d>=200us. Untestable planned tests stay in the family as p=1. Below 200us readerpref Lease throughput is descriptive.',
                   'Bootstrap sensitivity uses 10000 paired round resamples, seed 20261109, at each corresponding CI level. Bootstrap never replaces the confirmatory t/TOST decisions.',
                   'First-acquisition commit wait is the existing commit lock-site instrumentation, reset at window start and including drain; it excludes the second flush-check acquisition. Latencies are nanoseconds. Write service spans Begin/Put/Commit through the flush check; in-window and late completions are separate. Missing p99 or zero-completion latency is undefined and counted.',
                   'Both new policies use sync.Mutex/sync.Cond and add overhead; CoarseLock equivalence to Go is not predicted. These are simple experimental implementations, not tuned published phase-fair algorithms.',
                   'The manager lock is called through an interface in every design and policy, adding a dynamic call absent from earlier campaigns; this campaign\'s go cells include it.',
                   'No anchors. R by round and log-R slopes are descriptive. Settling differs from the first campaign: cross-campaign comparisons are descriptive. One host, one writer, one Go runtime.',
                   'Smoke directories are excluded. Latest ledger attempts are authoritative. One failed attempt awaits retry; a second failure drops the whole entry-round. Missing trials require --allow-partial and explicit partial labeling.'
               ])
    return out


def tables(out):
    lines = ['# X-locks-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## '+title, '', '| '+' | '.join(headers)+' |',
                      '| '+' | '.join(['---']*len(headers))+' |'])
        lines.extend('| '+' | '.join(base.fmt(v).replace('|', '\\|') for v in row)+' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in (
        'campaign_id', 'master_seed', 'settle_ms', 'plan_sha256', 'ledger_sha256',
        'runner_sha256', 'git_revision', 'platform', 'counts', 'bootstrap_seed', 'bootstrap_resamples')])
    table('Predictions', ['Hypothesis', 'Result'], [(k, out[k]) for k in ('H_L1', 'H_L2', 'H_L3', 'H_L4')])
    table('Per-arm arithmetic summaries (latencies in ns)', ['Cell', 'Arm', 'Metric', 'Summary'],
          [(c, arm, m, {k: x for k, x in v.items() if k != 'values'}) for c, cs in out['cells'].items()
           for arm, metrics in cs['arms'].items() for m, v in metrics.items()])
    table('CoarseLock/Lease ratios (95% paired log-t CI)', ['Cell', 'S (W A/AMIN)', 'R A/AMIN'],
          [(c, cs['S'], cs['read_ratio_A_AMIN']) for c, cs in out['cells'].items()])
    table('Phasefair CoarseLock relative to Go (overhead caveat)', ['Setting', 'Ratios'], out['phasefair_overhead_CL_relative_go'].items())
    table('Readerpref Lease below 200us (descriptive)', ['Cell', 'W relative to Go'],
          [(c, cs['descriptive_W_AMIN_relative_go']) for c, cs in out['cells'].items() if 'descriptive_W_AMIN_relative_go' in cs])
    table('Zero-commit counts', ['Cell', 'Complete units', 'All successful observations', 'Rounds'],
          [(c, cs['zero_commit_counts'], cs['zero_commit_successful_counts'], cs['zero_commit_rounds']) for c, cs in out['cells'].items()])
    table('R drift (descriptive)', ['Cell', 'Arm', 'Trend'], [(c, a, v) for c, arms in out['round_trend_R'].items() for a, v in arms.items()])
    table('Inter-attempt gaps', ['Field', 'Value'], out['gaps'].items())
    table('Failures, missing units and metric exclusions', ['Kind', 'Details'], [(k, v) for k in (
        'missing_trials', 'failed_trials', 'failed_attempts_detail', 'pending_retry', 'dropped_units', 'outcome_exclusions') for v in out[k]])
    lines += ['![q by setting and policy](x_locks_q.png)', '', '![S by setting and policy](x_locks_s.png)', '']
    lines.extend(out['notes'])
    return '\n'.join(lines)+'\n'


def figures(out, directory):
    import matplotlib
    matplotlib.use('Agg')
    import matplotlib.pyplot as plt
    fig, axes = plt.subplots(2, 1, figsize=(11, 8), sharex=True)
    for ax, arm in zip(axes, ARMS):
        for j, policy in enumerate(POLICIES):
            x, y, low, high, infinity = [], [], [], [], []
            for i, entry in enumerate(ENTRIES):
                v = out['cells'][entry+'_'+policy]['arms'][arm]['q']
                pos = i+(j-1)*.2
                if v['mean'] == ABOVE:
                    infinity.append(pos)
                elif v['mean'] is not None:
                    x.append(pos); y.append(v['mean'])
                    ci = v['t_ci'] or [v['mean']]*2
                    low.append(max(0, v['mean']-ci[0])); high.append(max(0, ci[1]-v['mean']))
            line = ax.errorbar(x, y, yerr=[low, high], fmt='o', capsize=3, label=policy)
            for pos in infinity:
                ax.annotate('above all bounds', (pos, .96-j*.07), xycoords=('data', 'axes fraction'),
                            color=line[0].get_color(), fontsize=7, rotation=90, va='top')
        ax.set(ylabel='q = R / (r W)', title='CoarseLock (A)' if arm == 'A' else 'Lease (AMIN)')
        if arm == 'A':
            ax.axhspan(1.90, 2.10, color='grey', alpha=.15)
        ax.legend()
    axes[-1].set_xticks(range(8), [e[3:] for e in ENTRIES], rotation=30)
    axes[-1].set_xlabel('Setting (reader count and delay in us)')
    fig.tight_layout(); fig.savefig(directory/'x_locks_q.png', metadata={'Software': 'x_locks_analyze'}); plt.close(fig)
    fig, ax = plt.subplots(figsize=(11, 5))
    for j, policy in enumerate(POLICIES):
        x, y, low, high = [], [], [], []
        for i, entry in enumerate(ENTRIES):
            v = out['cells'][entry+'_'+policy]['S']
            if v['geometric_mean_ratio'] is None:
                continue
            value = v['geometric_mean_ratio']; ci = v['t_ci'] or [value]*2
            x.append(i+(j-1)*.2); y.append(value)
            low.append(max(0, value-ci[0])); high.append(max(0, ci[1]-value))
        ax.errorbar(x, y, yerr=[low, high], fmt='o', capsize=3, label=policy)
    ax.axhline(1, color='grey', linestyle=':')
    ax.set(ylabel='S = W CoarseLock / W Lease (95% paired CI)', xlabel='Setting (reader count and delay in us)')
    ax.set_xticks(range(8), [e[3:] for e in ENTRIES], rotation=30)
    ax.legend(); fig.tight_layout()
    fig.savefig(directory/'x_locks_s.png', metadata={'Software': 'x_locks_analyze'}); plt.close(fig)


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
        (args.output_dir/'x_locks_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir/'x_locks_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())

