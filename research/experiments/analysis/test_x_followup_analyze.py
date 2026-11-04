"""Synthetic fixtures only: no runner, engine, trial or smoke is launched."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import subprocess
import shutil
import sys
import unittest
import uuid

HERE = Path(__file__).resolve().parent


def module(name):
    spec = importlib.util.spec_from_file_location(name, HERE/(name+'.py'))
    out = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(out)
    return out


a = module('x_followup_analyze')
helpers = module('test_x_settled_analyze')
write_json, write_ledger = helpers.write_json, helpers.write_ledger


def fixture(root):
    (root/'trials').mkdir()
    plan = dict(campaign_id='X-followup-v1', master_seed=a.SEED, settle_ms=20000,
                cells=[dict(id=c, spec=dict(a.nominal(c), cell=c, schema='i7.trial-spec.v1')) for c in a.CELLS], trials=[])
    rows, clock = [], datetime(2026, 11, 8, tzinfo=timezone.utc)
    for k in range(1, 11):
        # Synthetic cell order: validate_plan checks all schedule invariants
        # except Go's PRNG permutation, which the Go driver tests verify.
        for i, c in enumerate(a.CELLS):
            order = a.PERMUTATIONS[(k+i) % 6] if c == a.F4 else a.ARMS if (k+i) % 2 == 0 else a.ARMS[::-1]
            for pos, arm in enumerate(order):
                tid = f'{c}_t{k:02d}_{arm}'
                seed = hashlib.sha256(f'{a.SEED}|{c}|{k}'.encode()).digest()
                s = dict(a.nominal(c), schema='i7.trial-spec.v1', cell=c, trial_id=tid, triplet=k,
                         arm=arm, seed=int.from_bytes(seed[:8], 'big', signed=True), order_position=pos+1,
                         runlist_position=len(plan['trials'])+1)
                plan['trials'].append(s)
                r = helpers.result(s)
                if c != a.F4:
                    count = r['stall_diagnostics']['commit_count']
                    for phase, h in r['stall_diagnostics']['phase_histograms'].items():
                        value = (1000000 if s['wal_sync'] else 0) if phase == 'wal_sync' else 100
                        h['stats'] = helpers.histogram(value, count)
                    hold = count*(1000300 if s['wal_sync'] else 300)/10000000000
                    r['throughput']['read_ops_per_sec'] = 100000*(1-hold)*(1+k*.001)
                    if c == a.F2:
                        r['throughput']['read_ops_per_sec'] *= .9
                        r['lsm_diagnostics']['measured_flushes'] = 1
                else:
                    counts = (4500, 500, 5000) if arm == 'A' else (9500, 100, 400)
                    r['read_latency_histogram'] = [dict(lower_ns=low, count=n) for low, n in zip((44800, 299008, 1597440), counts)]
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block=s['block'], cell=c,
                         triplet=k, arm=arm, spec=copy.deepcopy(s), runtime=dict(exe_sha256='a'*64), result=r)
                write_json(root/'trials'/f'{tid}_a1.json', t)
                clock += timedelta(seconds=20)
                rows.append(dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1, start_utc=clock.isoformat()))
                clock += timedelta(seconds=12.5)
                rows.append(dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0, end_utc=clock.isoformat()))
    write_json(root/'plan.json', plan)
    write_ledger(root, rows)
    write_json(root/'platform.json', dict(campaign_id=plan['campaign_id'], runner_sha256='a'*64,
               plan_sha256=hashlib.sha256((root/'plan.json').read_bytes()).hexdigest(), git_head='synthetic-revision',
               power_scheme='synthetic', selftest=dict(schema='synthetic')))
    return plan, rows


class NumericTests(unittest.TestCase):
    def test_arithmetic_t_and_bootstrap(self):
        x = [.81, .96, 1.02, 1.11]
        out = a.arithmetic(x, .90)
        half = a.base.stats.t.ppf(.95, 3)*a.base.np.std(x, ddof=1)/2
        self.assertAlmostEqual(out['t_ci'][0], sum(x)/4-half)
        self.assertEqual(out, a.arithmetic(x, .90))
        self.assertIsNone(a.arithmetic([1])['t_ci'])

    def test_page_ties_and_direction(self):
        rows = [[1, 1, 2, 3]]*10
        out = a.trend_test(rows)
        expected = a.base.stats.page_trend_test(rows, predicted_ranks=[1, 2, 3, 4], method='auto')
        self.assertAlmostEqual(out['p'], expected.pvalue)
        self.assertEqual(out['mean_ranks'], [1.5, 1.5, 3, 4])
        self.assertGreater(a.trend_test([r[::-1] for r in rows])['p'], .95)
        self.assertIsNone(a.trend_test([])['p'])

    def test_histogram_boundaries_and_zero(self):
        h = a.histogram_regions([dict(lower_ns=v, count=1) for v in (199680, 200000, 499712, 500000)], 4)
        self.assertEqual(h['shares'], dict(fast=.25, valley=.50, blocked=.25))
        self.assertEqual(len(h['boundary_buckets']), 2)
        self.assertEqual(a.histogram_regions([dict(lower_ns=0, count=0)], 0)['shares']['fast'], None)
        for buckets, count in (([], 0), ([dict(lower_ns=0, count=-1)], -1),
                               ([dict(lower_ns=0, count=1)], 2),
                               ([dict(lower_ns=1, count=1), dict(lower_ns=0, count=1)], 2)):
            with self.assertRaises(ValueError):
                a.histogram_regions(buckets, count)


class FollowupTests(unittest.TestCase):
    def setUp(self):
        # mkdir avoids restrictive mkdtemp ACLs in the Windows sandbox.
        base = HERE.parents[2]/'_work'/'followup-tests'
        self.root = base/uuid.uuid4().hex
        self.root.mkdir(parents=True)
        self.assertTrue(self.root.resolve().is_relative_to(base.resolve()))
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.rows = fixture(self.root)

    def change(self, tid, fn):
        path = self.root/'trials'/f'{tid}_a1.json'
        t = a.load_json(path)
        fn(t['result'])
        write_json(path, t)

    def test_all_predictions_and_descriptives(self):
        out = a.analyze(self.root)
        self.assertTrue(out['H_F1']['supported'])
        for arm in a.ARMS:
            slope = out['H_F1']['arms'][arm]
            self.assertAlmostEqual(slope['mean'], 1)
            self.assertEqual(slope['rounds'], list(range(1, 11)))
            self.assertTrue(out['H_F2'][arm]['supported'])
            self.assertAlmostEqual(out['H_F2'][arm]['holm_p'], out['H_F2'][arm]['p']*2)
            self.assertAlmostEqual(out['F2_vs_F1'][arm]['read_ratio_F2_F1']['geometric_mean_ratio'], .9)
            self.assertAlmostEqual(out['capacity'][arm]['F1'][a.F1[0]]['undershoot_log']['mean'],
                                   out['capacity'][arm]['F1'][a.F1[3]]['undershoot_log']['mean'])
            self.assertEqual(out['cells'][a.F1[0]]['arms'][arm]['fsync_count_ge_25ms']['mean'], 1)
        self.assertEqual(len(out['H_F3']), 3)
        self.assertTrue(all(v['classification'] == 'equivalent' for v in out['H_F3'].values()))
        self.assertTrue(out['H_F4']['supported'])
        self.assertTrue(out['H_X']['supported'])
        self.assertEqual(out['H_F4']['regions']['fast']['mean'], .45)
        self.assertEqual(out['counts']['successful'], 150)
        self.assertEqual(out['gaps']['min_ms'], 20000)
        self.assertEqual(out['bootstrap_seed'], 20261108)
        self.assertEqual(out['cells'][a.F2]['arms']['AMIN']['flushes']['mean'], 1)
        self.assertEqual(out['F4_histograms']['B'][a.F4+'_t01_B']['count'], 10000)
        self.assertTrue(out['cells'][a.F1[0]]['arms']['B']['log_R']['sd'] > 0)
        self.assertEqual(out, a.analyze(self.root))
        json.dumps(out, allow_nan=False)
        self.assertIn('H_F4', a.tables(out))

    def test_hold_formula_and_missing_fsync_trace(self):
        tid = a.F1[0]+'_t01_AMIN'
        self.change(tid, lambda r: r['stall_diagnostics'].update(commits_dropped=1))
        out = a.analyze(self.root)
        v = out['trials'][tid]
        self.assertAlmostEqual(v['hold_share'], 1000300*v['commit_count']/v['hold_window_ns'])
        self.assertEqual(v['free_share'], 1-v['hold_share'])
        self.assertIsNone(v['fsync_count_ge_25ms'])
        self.assertIsNotNone(v['largest_fsync_ns'])

    def test_flush_kept_and_sensitivity_omits_round(self):
        tid = a.F3+'_t01_B'
        self.change(tid, lambda r: r['lsm_diagnostics'].update(measured_flushes=1))
        out = a.analyze(self.root)
        self.assertFalse(out['H_X']['supported'])
        self.assertEqual(out['H_X']['rounds'], [1])
        self.assertEqual(out['H_F1']['arms']['AMIN']['n'], 10)
        self.assertEqual(out['H_X']['H_F1_without_flush_rounds']['arms']['AMIN']['n'], 9)
        self.assertIn(tid, out['trials'])

    def test_missing_partial_and_unit_drop(self):
        tid = a.F1[0]+'_t01_AMIN'
        write_ledger(self.root, [e for e in self.rows if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertTrue(out['partial'])
        self.assertEqual(out['H_F1']['arms']['B']['n'], 9)
        self.assertIn(a.F1[0]+'_t01_B', out['trials'])
        self.assertEqual(out['cells'][a.F1[0]]['complete_rounds'], list(range(2, 11)))
        self.assertIn('PARTIAL DATA', a.tables(out))

    def test_latest_failure_and_retry_rules(self):
        tid = a.F1[0]+'_t01_AMIN'
        retry = [dict(e, attempt=2, status='FAILED', exit_code=1) for e in self.rows if e['trial_id'] == tid]
        retry[0]['status'], retry[0]['exit_code'] = 'STARTED', -1
        clock = datetime.fromisoformat(self.rows[-1]['end_utc'])+timedelta(seconds=20)
        retry[0]['start_utc'] = clock.isoformat()
        retry[1]['end_utc'] = (clock+timedelta(seconds=12.5)).isoformat()
        write_ledger(self.root, self.rows+retry)
        out = a.analyze(self.root)
        self.assertEqual(out['failed_trials'], [tid])
        self.assertNotIn(tid, out['trials'])
        self.assertEqual(out['H_F1']['arms']['B']['n'], 9)
        write_ledger(self.root, self.rows+[dict(retry[0], attempt=3)])
        with self.assertRaisesRegex(ValueError, 'retry'):
            a.analyze(self.root)

    def test_zero_reads_and_nonpositive_free_only_exclude_logs(self):
        tid = a.F1[0]+'_t01_AMIN'
        self.change(tid, lambda r: r['throughput'].update(read_ops_per_sec=0))
        out = a.analyze(self.root)
        self.assertEqual(out['H_F1']['arms']['AMIN']['n'], 9)
        self.assertEqual(out['H_F1']['arms']['B']['n'], 10)
        self.assertEqual(out['H_F2']['AMIN']['n'], 10)
        self.assertEqual(out['H_F3'][a.F1[0]]['undefined_n'], 1)
        self.assertIn(tid, out['trials'])
        self.change(a.F1[1]+'_t02_AMIN', lambda r: r['stall_diagnostics']['phase_histograms']['wal_sync']['stats'].update(mean=1e10))
        out = a.analyze(self.root)
        self.assertEqual(out['H_F1']['arms']['AMIN']['n'], 8)
        self.assertLess(out['trials'][a.F1[1]+'_t02_AMIN']['free_share'], 0)

    def test_opposite_predictions_and_undefined(self):
        for s in self.plan['trials']:
            tid, c = s['trial_id'], s['cell']
            if c in a.F1:
                self.change(tid, lambda r, k=s['triplet'], arm=s['arm']: r['throughput'].update(
                    read_ops_per_sec=10000*(math.exp((-1)**k) if arm == 'AMIN' else 1)))
            if c == a.F4 and s['arm'] == 'A':
                self.change(tid, lambda r: r.update(read_latency_histogram=[dict(lower_ns=299008, count=10000)]))
        out = a.analyze(self.root)
        self.assertFalse(out['H_F1']['supported'])
        self.assertFalse(out['H_F2']['AMIN']['supported'])
        self.assertEqual(out['H_F3'][a.F1[0]]['classification'], 'inconclusive')
        self.assertFalse(out['H_F4']['supported'])

    def test_plan_and_provenance_fail_closed(self):
        for change in (lambda p: p.update(settle_ms=0),
                       lambda p: p['cells'][0]['spec'].update(persist_read_histogram=True),
                       lambda p: p['trials'][0].update(seed=0),
                       lambda p: p['trials'][0].update(arm='A'),
                       lambda p: p['trials'][0].update(order_position=2)):
            p = copy.deepcopy(self.plan); change(p)
            with self.assertRaises(ValueError):
                a.validate_plan(p)
        platform = a.load_json(self.root/'platform.json')
        platform['plan_sha256'] = 'b'*64
        write_json(self.root/'platform.json', platform)
        with self.assertRaisesRegex(ValueError, 'platform'):
            a.analyze(self.root)

    def test_histogram_count_and_treatment_mismatch(self):
        self.change(a.F4+'_t01_A', lambda r: r['read_latency_histogram'][0].update(count=100))
        with self.assertRaisesRegex(ValueError, 'histogram/sample count'):
            a.analyze(self.root)

    def test_boundary_buckets_keep_frozen_lower_bound_decision(self):
        tid = a.F4+'_t01_A'
        self.change(tid, lambda r: r['read_latency_histogram'].append(dict(lower_ns=199680, count=1)))
        def repair(r):
            r['read_latency_histogram'][0]['count'] -= 1
            r['read_latency_histogram'].sort(key=lambda b: b['lower_ns'])
        self.change(tid, repair)
        out = a.analyze(self.root)
        self.assertEqual(out['H_F4']['boundary_trials'], [tid])
        self.assertTrue(out['H_F4']['supported'])
        self.assertEqual(out['H_F4']['bucket_convention'], 'lower_ns')
        self.assertTrue(out['F4_histograms']['A'][tid]['boundary_buckets'])
        for removed in ('conflict', 'exact_thresholds_resolved', 'ambiguous_A_trials', 'bucket_lower_bound_supported'):
            self.assertNotIn(removed, out['H_F4'])
        for k in range(1, 11):
            self.change(a.F4+f'_t{k:02d}_A', lambda r: r.update(
                read_latency_histogram=[dict(lower_ns=499712, count=10000)]))
        out = a.analyze(self.root)
        self.assertFalse(out['H_F4']['supported'])
        self.assertEqual(out['H_F4']['regions']['valley']['mean'], 1)
        self.assertEqual(out['H_F4']['regions']['blocked']['mean'], 0)
        self.assertEqual(len(out['H_F4']['boundary_trials']), 10)

    def test_clearly_not_equivalent_is_reported(self):
        for s in self.plan['trials']:
            if s['cell'] in a.F1 and s['arm'] == 'AMIN':
                self.change(s['trial_id'], lambda r: r['throughput'].update(read_ops_per_sec=2*r['throughput']['read_ops_per_sec']))
        out = a.analyze(self.root)
        self.assertEqual({v['classification'] for v in out['H_F3'].values()}, {'not_equivalent'})
        self.assertFalse(any(v['supported'] for v in out['H_F3'].values()))

    def test_cli_and_figures_are_deterministic(self):
        dest = self.root/'analysis'
        env = dict(os.environ, MPLCONFIGDIR=str(self.root/'matplotlib'))
        cmd = [sys.executable, str(HERE/'x_followup_analyze.py'), '--data-dir', str(self.root), '--output-dir', str(dest)]
        first = subprocess.run(cmd, env=env, capture_output=True, text=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        files = sorted(dest.iterdir())
        self.assertEqual(len(files), 4)
        before = {f.name: f.read_bytes() for f in files}
        second = subprocess.run(cmd, env=env, capture_output=True, text=True)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(before, {f.name: f.read_bytes() for f in files})


if __name__ == '__main__':
    unittest.main()
