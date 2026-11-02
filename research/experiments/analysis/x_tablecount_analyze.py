#!/usr/bin/env python3
"""Pre-registered X-tablecount-v1 analysis; reads artifacts, never runs trials."""
import argparse
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import sys

_spec = importlib.util.spec_from_file_location(
    'x_compaction_helpers', Path(__file__).with_name('x_compaction_analyze.py'))
compaction = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(compaction)
base = compaction.base
require, load_json = base.require, base.load_json
ARMS, METRICS = base.ARMS, base.METRICS
SEED = 20261105
LEVELS = (4, 16, 64, 256)
CELLS = [f'T1_n{n:03d}_r{r:02d}_w{w}' for n in LEVELS for r in (4, 16) for w in (0, 1)]


def nominal(cell):
    require(cell in CELLS, f'unknown cell {cell}')
    n, r, w = int(cell[4:7]), int(cell[9:11]), int(cell[-1])
    return dict(block='X1', reader_concurrency=r, writer_concurrency=w,
                read_path='block', read_io_delay_us=0, memtable_threshold_bytes=1 << 20,
                measurement_duration_ms=10000, warmup_duration_ms=2000, cooldown_duration_ms=200,
                keyspace_cardinality=10240, initial_sstable_count=n, initial_keys_per_sstable=10240 // n,
                expected_reader_set_size=n, target_read_hit_ratio=.95, read_distribution='uniform',
                wal_sync=True, instrument_sync=True, workload_id='X-tablecount',
                compaction_trigger_interval_ms=0)


def validate_plan(plan):
    require(plan['campaign_id'] == 'X-tablecount-v1', 'wrong campaign_id')
    require(type(plan['master_seed']) is int and 0 <= plan['master_seed'] < 2 ** 64, 'invalid master seed')
    require(len(plan['cells']) == 16 and [c['id'] for c in plan['cells']] == CELLS, 'invalid cells/order')
    for c in plan['cells']:
        compaction.check_spec(c['spec'], nominal(c['id']), c['id'])
    require(len(plan['trials']) == 480, 'plan must contain 480 trials')
    seen, units = set(), {}
    permutations = (('A', 'AMIN', 'B'), ('A', 'B', 'AMIN'), ('AMIN', 'A', 'B'),
                    ('AMIN', 'B', 'A'), ('B', 'A', 'AMIN'), ('B', 'AMIN', 'A'))
    for index, s in enumerate(plan['trials']):
        c, k, arm = s['cell'], s['triplet'], s['arm']
        require(c in CELLS and type(k) is int and 1 <= k <= 10 and arm in ARMS, 'invalid run')
        tid = f'{c}_t{k:02d}_{arm}'
        require(s['trial_id'] == tid and tid not in seen, 'invalid/duplicate trial ID')
        seen.add(tid)
        compaction.check_spec(s, nominal(c), tid)
        h = hashlib.sha256(f"{plan['master_seed']}|{c}|{k}".encode()).digest()
        require(type(s['seed']) is int and s['seed'] == int.from_bytes(h[:8], 'big', signed=True),
                f'incorrect paired seed {tid}')
        require(s['runlist_position'] == index + 1 and s['order_position'] == index % 3 + 1,
                f'invalid schedule position {tid}')
        require(k == index // 48 + 1, 'invalid round interleaving')
        require(arm == permutations[(k + CELLS.index(c)) % 6][index % 3], 'invalid arm permutation')
        units.setdefault((c, k), []).append(index)
    require(len(units) == 160 and all(len(v) == 3 and v == list(range(v[0], v[0] + 3))
                                    for v in units.values()), 'incomplete/noncontiguous triplet')


def trial_outcomes(t):
    r, s = t['result'], t['spec']
    require(t['schema'] == 'i7.trial.v1' and t['trial_id'] == s['trial_id'], 'invalid envelope identity')
    require(r['status'] == 'SUCCESS' and r['trial_id'] == s['trial_id'], 'invalid result status/identity')
    compaction.check_spec(r['config'], s, s['trial_id'])
    require(r['operations']['validation_error_count'] == 0 and r['operations']['read_ops_completed'] > 0
            and r['timestamps']['measured_window_ns'] > 0, 'invalid successful window')
    diag = r['lsm_diagnostics']
    require(diag.get('compaction_errors', 0) == 0 and diag['measured_compactions'] == 0,
            'unplanned compaction or engine fault')
    require(diag['initial_sstables'] == s['initial_sstable_count'], 'initial table count mismatch')
    require(diag['final_sstables'] >= diag['initial_sstables'], 'table retirement without compaction')
    out = base.outcomes(t)
    if s['writer_concurrency'] == 0:
        require(out['W'] in (None, 0) and r['operations']['write_ops_completed'] == 0,
                'write activity in read-only cell')
        require(diag['final_sstables'] == diag['initial_sstables'], 'read-only table count changed')
        for m in ('W', 'commit_wait_p50', 'commit_wait_p99'):
            out[m] = None
            out['not_applicable'].append(m)
    if not r['sync_instrumentation']['writer_lock_wait_nanos'].get('flush_publish'):
        out['not_applicable'] += ['publication_lock_wait_p50', 'publication_lock_wait_p99']
    out['initial_sstables'], out['final_sstables'] = diag['initial_sstables'], diag['final_sstables']
    return out


def load_campaign(root, allow_partial=False):
    root = Path(root)
    plan = load_json(root / 'plan.json')
    validate_plan(plan)
    planned = {s['trial_id']: s for s in plan['trials']}
    def checked_outcomes(t):
        s = planned[t['trial_id']]
        compaction.check_spec(t['spec'], s, t['trial_id'])
        for key in ('trial_id', 'block', 'cell', 'triplet', 'arm'):
            require(t[key] == s[key], f'envelope {key} mismatch')
        return trial_outcomes(t)
    specs, groups, records, meta = base.load_campaign(
        root, allow_partial, campaign_id='X-tablecount-v1', config_reader=nominal,
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


def microbenchmarks(root):
    """Optional standard Go benchmark text, kept wholly outside campaign tests."""
    path = Path(root) / 'lifecycle_bench.txt'
    out = dict(source=None, sha256=None, measurements=[], scope='separate non-campaign measurement')
    if not path.exists():
        out['status'] = 'not supplied; no microbenchmark measurements inferred from campaign data'
        return out
    out.update(source=path.name, sha256=hashlib.sha256(path.read_bytes()).hexdigest(), status='supplied')
    pattern = re.compile(r'^(BenchmarkLifecycle(?:Publication)?/\S+)\s+(\d+)\s+([\d.eE+\-]+) ns/op\s+'
                         r'([\d.eE+\-]+) B/op\s+([\d.eE+\-]+) allocs/op\s*$')
    for line in path.read_text(encoding='utf-8').splitlines():
        match = pattern.fullmatch(line)
        if match:
            name, iterations, ns, size, allocs = match.groups()
            row = dict(benchmark=name, iterations=int(iterations), ns_per_op=float(ns),
                       bytes_per_op=float(size), allocs_per_op=float(allocs), compile_check_only=int(iterations) == 1)
            for field in ('ns_per_op', 'bytes_per_op', 'allocs_per_op'):
                base.number(row[field], field)
            out['measurements'].append(row)
    require(out['measurements'], 'lifecycle_bench.txt contains no recognized benchmark rows')
    return out


def analyze(root, allow_partial=False):
    specs, groups, records, out = load_campaign(root, allow_partial)
    cells, logs, exclusions = {}, {}, []
    for c in CELLS:
        complete = {k: g for (cell, k), g in sorted(groups.items())
                    if cell == c and all(tid in records for tid in g.values())}
        w = nominal(c)['writer_concurrency']
        zero = [k for k, g in complete.items() if w and any(records[g[a]]['W'] == 0 for a in ARMS)]
        cs = dict(complete_triplets=list(complete), nominal=nominal(c), arms={}, ratios={}, equivalence={},
                  zero_adjusted_W_triplets=zero)
        for arm in ARMS:
            cs['arms'][arm] = {}
            for m in METRICS + ('initial_sstables', 'final_sstables'):
                applicable = [records[g[arm]][m] for g in complete.values()
                              if m not in records[g[arm]]['not_applicable']]
                cs['arms'][arm][m] = dict(**compaction.summary(applicable),
                                          not_applicable_n=len(complete) - len(applicable))
        for m in METRICS:
            cs['ratios'][m] = {}
            if not w and m in ('W', 'commit_wait_p50', 'commit_wait_p99'):
                continue
            for a, b in base.PAIRS:
                pairs, ds = [], {}
                for k, g in complete.items():
                    if any(m in records[g[arm]]['not_applicable'] for arm in (a, b)):
                        continue
                    x, y = records[g[a]][m], records[g[b]][m]
                    if m == 'W' and k in zero:
                        x, y = (x + .1 if x is not None else None), (y + .1 if y is not None else None)
                    pairs.append((x, y))
                    if x is not None and y is not None and x > 0 and y > 0:
                        ds[k] = math.log(x) - math.log(y)
                    else:
                        exclusions.append(dict(cell=c, triplet=k, metric=m, pair=a + '/' + b,
                                               reason='missing/nonpositive log pair'))
                cs['ratios'][m][a + '/' + b] = ratios(pairs)
                logs[c, m, a, b] = ds
                if m in ('R', 'W') and (a, b) == ('AMIN', 'B'):
                    cs['equivalence'][m] = ratios(pairs, test=True)
        cells[c] = cs
    h1, h2 = {}, {}
    for r in (4, 16):
        for w in (0, 1):
            names = [f'T1_n{n:03d}_r{r:02d}_w{w}' for n in LEVELS]
            ds = [logs[c, 'R', 'AMIN', 'B'] for c in names]
            common = sorted(set.intersection(*(set(d) for d in ds)))
            h1[f'r{r:02d}_w{w}'] = dict(**base.page_test([[math.exp(d[k]) for d in ds] for k in common]),
                                      rounds=common, levels=list(LEVELS), predicted_ranks=[4, 3, 2, 1], alpha=.05)
    for key, p in base.holm({key: v['p'] for key, v in h1.items()}).items():
        h1[key].update(holm_p=p, decline_detected=p is not None and p < .05)
    for m in ('R', 'W'):
        family = {c: cells[c]['equivalence'][m] for c in CELLS if m in cells[c]['equivalence']}
        for c, p in base.holm({c: v['p_tost'] for c, v in family.items()}).items():
            v = family[c]
            ci = v['t_ci_log']
            classification = ('equivalent' if p is not None and p < .05 else
                              'not_equivalent' if ci and (ci[1] < -base.MARGIN or ci[0] > base.MARGIN)
                              else 'inconclusive')
            v.update(holm_p=p, classification=classification, family=m)
        h2[m] = family
    crossings, drift = {}, {}
    for r in (4, 16):
        for w in (0, 1):
            crossing = {}
            for m in (('R', 'W') if w else ('R',)):
                results = [(n, h2[m][f'T1_n{n:03d}_r{r:02d}_w{w}']) for n in LEVELS]
                outside = [n for n, v in results if v['mean_log'] is not None and abs(v['mean_log']) > base.MARGIN]
                classified = [n for n, v in results if v['classification'] == 'not_equivalent']
                crossing[m] = dict(first_point_estimate_outside_margin=min(outside) if outside else None,
                                   first_not_equivalent=min(classified) if classified else None)
            crossings[f'r{r:02d}_w{w}'] = crossing
    for c, cs in cells.items():
        drift[c] = {}
        for arm in ARMS:
            rows = [(k, records[groups[c, k][arm]]['R']) for k in cs['complete_triplets']]
            positive = [(k, math.log(v)) for k, v in rows if v is not None and v > 0]
            slope = float(base.np.polyfit(*zip(*positive), 1)[0]) if len(positive) >= 2 else None
            drift[c][arm] = dict(round_R=rows, log_R_slope_per_round=slope)
    predictions = []
    for c in CELLS:
        n, w = nominal(c)['initial_sstable_count'], nominal(c)['writer_concurrency']
        for m, v in cells[c]['equivalence'].items():
            predicted = 'equivalent' if n == 4 else 'not_equivalent' if n == 256 and w == 0 else None
            if predicted:
                predictions.append(dict(cell=c, metric=m, predicted=predicted, found=v['classification'],
                                        matches=v['classification'] == predicted))
    out.update(cells=cells, H_T1=h1, H_T2=h2, H_T2_predictions=predictions,
               equivalence_margin_log=base.MARGIN, margin_crossings=crossings, round_trend_R=drift,
               outcome_exclusions=exclusions, microbenchmark=microbenchmarks(root),
               trials={tid: dict(cell=specs[tid]['cell'], triplet=specs[tid]['triplet'], arm=specs[tid]['arm'], **v)
                       for tid, v in sorted(records.items())},
               notes=['Four one-sided Page tests, ordered N=4,16,64,256, predicted ranks 4,3,2,1; Holm across four.',
                      'H-T2 uses separate Holm families: R in 16 cells, W in eight writer cells. Untestable tests count as p=1.',
                      'No cells are pooled. Complete triplets supply cell summaries and tests. Successful dropped-unit observations remain in trial output.',
                      'W and commit waits are NA in read-only cells; no W hypothesis or pseudo-count exists there.',
                      'W zeros use +0.1 writes/s for all arms of the complete paired triplet. Other undefined/nonpositive log pairs are listed.',
                      'Per-arm means have 95% t CIs. Ratios have 95% paired t CIs. TOST uses 90% paired t and 10000-resample percentile bootstrap sensitivity.',
                      'Crossings are the first tested N outside the ratio margin by geometric mean, and separately by not_equivalent classification; no interpolation.',
                      'All arms pay up to N in-memory range comparisons; these run under the read lock only in A. Campaign throughput does not isolate lifecycle cost.',
                      'Fixed keyspace means fewer keys per table at higher N. Initial/final table counts expose any writer flushes.',
                      'Synchronization instrumentation includes drain after window reset. No anchors; round trends are descriptive.',
                      'Microbenchmarks are separate non-campaign measurements. Iterations=1 rows are compile checks only, not performance evidence.',
                      'If no decline exists, the paper must say leasing\'s per-table cost was not detectable here. Other outcomes are reported as found.',
                      'Smoke data are excluded. Missing trials require --allow-partial; latest ledger attempts are authoritative.'])
    return out


def tables(out):
    lines = ['# X-tablecount-v1 analysis', '']
    if out['partial']:
        lines += ['> PARTIAL DATA: inference is incomplete.', '']
    def table(title, headers, rows):
        lines.extend(['## ' + title, '', '| ' + ' | '.join(headers) + ' |',
                      '| ' + ' | '.join(['---'] * len(headers)) + ' |'])
        lines.extend('| ' + ' | '.join(base.fmt(v).replace('|', '\\|') for v in row) + ' |' for row in rows)
        lines.append('')
    table('Provenance', ['Field', 'Value'], [(k, out[k]) for k in
          ('campaign_id', 'master_seed', 'plan_sha256', 'ledger_sha256', 'runner_sha256', 'git_revision', 'counts', 'platform')])
    table('H-T1: declining AMIN/B read throughput', ['Readers/writers', 'Result'], out['H_T1'].items())
    table('H-T2: equivalence (90% paired t CI, bootstrap sensitivity)', ['Outcome', 'Cell', 'Result'],
          [(m, c, v) for m, family in out['H_T2'].items() for c, v in family.items()])
    table('H-T2 predictions', ['Cell', 'Outcome', 'Prediction', 'Found'],
          [(v['cell'], v['metric'], v['predicted'], v['found']) for v in out['H_T2_predictions']])
    table('First margin crossings (descriptive)', ['Readers/writers', 'Result'], out['margin_crossings'].items())
    table('Per-arm throughput, latency, critical section and waits (nanoseconds)', ['Cell', 'Arm', 'Metric', 'Summary'],
          [(c, a, m, v) for c, cs in out['cells'].items() for a, metrics in cs['arms'].items() for m, v in metrics.items()])
    table('Within-cell ratios (95% paired t CI)', ['Cell', 'Metric', 'Pair', 'Result'],
          [(c, m, p, v) for c, cs in out['cells'].items() for m, pairs in cs['ratios'].items() for p, v in pairs.items()])
    table('Round trends (descriptive)', ['Cell', 'Arm', 'Trend'],
          [(c, a, v) for c, arms in out['round_trend_R'].items() for a, v in arms.items()])
    table('Separate microbenchmark (ns/op, allocs/op)', ['Field', 'Value'], out['microbenchmark'].items())
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
        (args.output_dir / 'x_tablecount_results.json').write_text(encoded, encoding='utf-8', newline='\n')
        (args.output_dir / 'x_tablecount_tables.md').write_text(tables(out), encoding='utf-8', newline='\n')
    except (ValueError, OSError, KeyError, TypeError, AttributeError, OverflowError) as exc:
        print(f'FAIL-CLOSED ERROR: {exc}', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
