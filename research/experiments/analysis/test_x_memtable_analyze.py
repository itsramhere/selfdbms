"""Synthetic fixtures only: no runner, real campaign or smoke is launched."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
import uuid

HERE = Path(__file__).resolve().parent


def module(name):
    spec = importlib.util.spec_from_file_location(name, HERE/(name+'.py'))
    out = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(out)
    return out


a = module('x_memtable_analyze')
helpers = module('test_x_settled_analyze')
write_json, write_ledger = helpers.write_json, helpers.write_ledger


def fixture(root):
    (root/'trials').mkdir()
    plan = dict(campaign_id='X-memtable-v1', master_seed=a.SEED, settle_ms=20000,
                cells=[dict(id=c, spec=dict(a.nominal(c), cell=c, schema='i7.trial-spec.v1')) for c in a.CELLS], trials=[])
    rows, clock = [], datetime(2026, 11, 12, tzinfo=timezone.utc)
    for k in range(1, 11):
        # Go tests verify the seeded shuffle; fixtures exercise round invariants.
        for c in a.CELLS:
            rate, domain, sync = a.factors(c)
            tid = f'{c}_t{k:02d}_B'
            h = hashlib.sha256(f'{a.SEED}|{c}|{k}'.encode()).digest()
            s = dict(a.nominal(c), schema='i7.trial-spec.v1', cell=c, trial_id=tid, triplet=k, arm='B',
                     seed=int.from_bytes(h[:8], 'big', signed=True), order_position=1, runlist_position=len(plan['trials'])+1)
            # Exercise JSON omissions produced by Go for zero optional fields.
            s = {key: v for key, v in s.items() if not (v == 0 and key in a.settled.OPTIONAL_ZERO)
                 and not (v == '' and key == 'write_key_domain') and not (v is False and key == 'persist_read_histogram')}
            plan['trials'].append(s)
            r = helpers.result(s)
            count = r['stall_diagnostics']['commit_count']
            sync_ns = 1000000*(1+.005*k) if sync == 'on' else 0
            for phase, hist in r['stall_diagnostics']['phase_histograms'].items():
                hist['stats'] = helpers.histogram(sync_ns if phase == 'wal_sync' else 100, count)
            free = 1-count*(sync_ns+300)/1e10
            capacity = 100000*(1+.001*k)*(1+.1*a.RATES.index(rate) if domain == 'same' else 1)
            reads = int(capacity*free*10)
            r['throughput']['read_ops_per_sec'] = reads/10
            r['operations'].update(read_ops_completed=reads, read_ops_completed_in_window=reads)
            r['latency_read_nanos']['sample_count'] = reads
            ledger = r['transaction_ledger']['reads']
            for key in ('offered', 'attempted', 'completed_in_window'):
                ledger[key] = reads
            share = .08+.07*a.RATES.index(rate) if domain == 'same' else 0
            r['lsm_diagnostics']['measured_memtable_point_reads'] = int(reads*share)
            t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block=s['block'], cell=c, triplet=k,
                     arm='B', spec=copy.deepcopy(s), runtime=dict(exe_sha256='a'*64), result=r)
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
    def test_arithmetic_and_paired_uncertainty(self):
        values = [.81, .96, 1.02, 1.11]
        out = a.arithmetic(values, .90)
        half = a.base.stats.t.ppf(.95, 3)*a.base.np.std(values, ddof=1)/2
        self.assertAlmostEqual(out['t_ci'][0], sum(values)/4-half)
        draws = a.base.np.asarray(values)[a.base.np.random.default_rng(a.SEED).integers(0, 4, size=(10000, 4))].mean(axis=1)
        a.base.np.testing.assert_allclose(out['bootstrap_ci'], a.base.np.percentile(draws, [5, 95]))
        self.assertEqual(out, a.arithmetic(values, .90))
        self.assertIsNone(a.arithmetic([1])['t_ci'])
        unit = dict(values=[math.log(.8)]*10, rounds=list(range(1, 11)), excluded_rounds=[])
        self.assertAlmostEqual(a.ratio(unit)['geometric_mean_ratio'], .8)

    def test_slope_difference_is_paired(self):
        specs, records = {}, {}
        for k in range(1, 11):
            for d in a.DOMAINS:
                slope = .5+.1*k+(.2 if d == 'shadow' else 0)
                for w, free in zip(a.RATES, (.9, .8, .6, .4)):
                    records[f'{a.cell(w, d, "on")}_t{k:02d}_B'] = dict(R=10000*free**slope, free_share=free)
        out = a.slope_predictions(specs, records)
        self.assertAlmostEqual(out['shadow_minus_same']['mean'], .2)
        self.assertLess(out['shadow_minus_same']['t_ci'][1]-out['shadow_minus_same']['t_ci'][0], 1e-12)
        self.assertAlmostEqual(out['domains']['shadow']['mean'], 1.25)
        self.assertFalse(out['supported'])  # shadow CI exceeds the upper band
        del records[f'{a.cell(183, "same", "on")}_t01_B']
        out = a.slope_predictions(specs, records)
        self.assertEqual(out['domains']['shadow']['n'], 10)
        self.assertEqual(out['shadow_minus_same']['n'], 9)

    def test_page_direction_ties_and_holm_family(self):
        rows = [[1, 1, 2, 3]]*10
        got = a.followup.trend_test(rows)
        expected = a.base.stats.page_trend_test(rows, predicted_ranks=[1, 2, 3, 4], method='auto')
        self.assertEqual(got['p'], expected.pvalue)
        self.assertEqual(got['mean_ranks'], [1.5, 1.5, 3, 4])
        self.assertGreater(a.followup.trend_test([r[::-1] for r in rows])['p'], .95)
        family = {str(i): a.base.tost([.01*(-1)**k for k in range(10)]) for i in range(8)}
        family['0']['p_tost'] = .01
        family['1']['p_tost'] = None
        for i in range(2, 8):
            family[str(i)]['p_tost'] = .02*i
        a.paced.adjust_family(family, 'H-M4 (8)')
        self.assertAlmostEqual(family['0']['holm_p'], .08)
        self.assertEqual(family['0']['classification'], 'inconclusive')
        self.assertIsNone(family['1']['holm_p'])
        self.assertEqual(family['1']['classification'], 'inconclusive')

    def test_within_rate_fixed_intercepts(self):
        records = {}
        for rate in a.RATES:
            for k in range(1, 11):
                free = .6+.01*k
                records[f'{a.cell(rate, "same", "on")}_t{k:02d}_B'] = dict(free_share=free, R=rate*100*free**1.14)
        out = a.within_rate_slope({}, records, 'same')
        self.assertAlmostEqual(out['slope'], 1.14)
        a.base.np.testing.assert_allclose(out['bootstrap_ci'], [1.14, 1.14], atol=1e-10)
        self.assertIsNone(a.ols([dict(R=1, free_share=.5)]*4))


class MemtableTests(unittest.TestCase):
    def setUp(self):
        base_dir = HERE.parents[2]/'_work'/'memtable-tests'
        self.root = base_dir/uuid.uuid4().hex
        self.root.mkdir(parents=True)
        self.assertTrue(self.root.resolve().is_relative_to(base_dir.resolve()))
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.rows = fixture(self.root)

    def change(self, tid, fn):
        path = self.root/'trials'/f'{tid}_a1.json'
        t = a.load_json(path)
        fn(t['result'])
        write_json(path, t)

    def test_all_predictions_and_descriptive_outputs(self):
        out = a.analyze(self.root)
        for key in ('H_M1', 'H_M2', 'H_M3', 'H_M4', 'H_X'):
            self.assertTrue(out[key]['supported'], key)
        self.assertTrue(out['paper_claims']['memtable_explanation'])
        self.assertTrue(out['paper_claims']['capacity_times_free_share_all'])
        self.assertEqual(len(out['paper_claims']['capacity_times_free_share_supported_combinations']), 8)
        self.assertAlmostEqual(out['H_M1']['domains']['shadow']['mean'], 1, places=5)
        self.assertGreater(out['H_M1']['shadow_minus_same']['t_ci'][0], 0)
        self.assertEqual(out['H_M2']['page']['method'], a.base.stats.page_trend_test(
            [[1, 2, 3, 4]]*10, predicted_ranks=[1, 2, 3, 4], method='auto').method)
        self.assertLess(out['H_M2']['R_183_over_732']['t_ci_log'][1], math.log(.9))
        self.assertEqual(out['H_M3']['classification'], 'equivalent')
        self.assertEqual(len(out['H_M4']['combinations']), 8)
        for v in out['H_M4']['combinations'].values():
            self.assertEqual(v['classification'], 'equivalent')
            self.assertAlmostEqual(v['geometric_mean_ratio'], 1, places=5)
            self.assertTrue(all(abs(log) < 1e-5 for log in v['log_values']))
        self.assertEqual(out['counts']['successful'], 160)
        self.assertEqual(out['bootstrap_seed'], 20261112)
        self.assertEqual(out['bootstrap_resamples'], 10000)
        self.assertEqual(out['gaps']['min_ms'], 20000)
        self.assertEqual(out['outcome_exclusions'], [])
        self.assertEqual(out['same_slope_context']['X_followup_v1'], dict(AMIN=.768, B=.779))
        self.assertEqual(out['pooled_within_rate_slopes']['same']['n'], 40)
        self.assertAlmostEqual(out['shadow_over_same_capacity']['732']['geometric_mean_ratio'], 1/1.3, places=5)
        c = out['cells'][a.CELLS[0]]
        self.assertGreater(c['metrics']['log_R']['sd'], 0)
        self.assertIsNotNone(c['log_R_slope_per_round'])
        self.assertEqual(set(c['hold_phases_ns']), {'validate', 'wal_write', 'wal_sync', 'apply'})
        self.assertEqual(out, a.analyze(self.root))
        json.dumps(out, allow_nan=False)
        self.assertIn('H_M4', a.tables(out))

    def test_hold_memtable_formula_and_dropped_trace(self):
        tid = a.CELLS[0]+'_t01_B'
        self.change(tid, lambda r: r['stall_diagnostics'].update(commits_dropped=1))
        out = a.analyze(self.root)
        v = out['trials'][tid]
        self.assertAlmostEqual(v['hold_share'], sum(v['hold_phase_means_ns'])*v['commit_count']/v['hold_window_ns'])
        self.assertEqual(v['free_share'], 1-v['hold_share'])
        self.assertEqual(v['hold_per_commit_ns'], sum(v['hold_phases_ns'].values()))
        self.assertEqual(v['memtable_share'], v['memtable_reads']/v['read_ops_completed'])
        self.assertIsNone(v['fsync_count_ge_25ms'])
        self.assertIsNotNone(v['largest_fsync_ns'])
        self.assertEqual(out['cells'][a.CELLS[0]]['metrics']['fsync_count_ge_25ms']['undefined_n'], 1)

    def test_flush_kept_and_manipulation_gates(self):
        tid = a.cell(183, 'shadow', 'on')+'_t01_B'
        self.change(tid, lambda r: r['lsm_diagnostics'].update(measured_flushes=1))
        out = a.analyze(self.root)
        self.assertFalse(out['H_X']['supported'])
        self.assertTrue(out['H_M1']['interpretable'])
        self.assertEqual(out['H_M1']['domains']['shadow']['n'], 10)
        self.assertIn(tid, out['trials'])
        self.assertFalse(out['paper_claims']['memtable_explanation'])
        self.change(tid, lambda r: r['lsm_diagnostics'].update(measured_memtable_point_reads=r['operations']['read_ops_completed']))
        out = a.analyze(self.root)
        self.assertFalse(out['H_X']['manipulation_supported'])
        for key in ('H_M1', 'H_M2', 'H_M3'):
            self.assertFalse(out[key]['interpretable'])
        # Independently fail the same-domain rising-share check.
        self.change(tid, lambda r: r['lsm_diagnostics'].update(measured_memtable_point_reads=0))
        for k in range(1, 11):
            self.change(a.cell(732, 'same', 'off')+f'_t{k:02d}_B',
                        lambda r: r['lsm_diagnostics'].update(measured_memtable_point_reads=0))
        self.assertFalse(a.analyze(self.root)['H_X']['same_by_sync']['off']['supported'])

    def test_partial_and_failed_round_units(self):
        tid = a.cell(183, 'same', 'off')+'_t01_B'
        write_ledger(self.root, [e for e in self.rows if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertTrue(out['partial'])
        self.assertEqual(out['H_M1']['domains']['same']['n'], 10)
        self.assertEqual(out['H_M2']['page']['n'], 9)
        self.assertEqual(out['H_M2']['R_183_over_732']['n'], 9)
        self.assertEqual(out['H_M3']['n'], 10)
        self.assertEqual(out['H_M4']['combinations']['w0183_same']['n'], 9)
        self.assertEqual(out['H_M4']['combinations']['w0366_same']['n'], 10)
        self.assertIn('PARTIAL DATA', a.tables(out))
        self.assertTrue(out['outcome_exclusions'])
        self.assertEqual(len(out['dropped_units']), 1)

    def test_retry_latest_failure_and_pending(self):
        tid = a.CELLS[0]+'_t01_B'
        rows = copy.deepcopy(self.rows)
        rows[1].update(status='FAILED', exit_code=1)
        write_ledger(self.root, rows)
        out = a.analyze(self.root, True)
        self.assertEqual(out['pending_retry'], [tid])
        clock = datetime.fromisoformat(rows[-1]['end_utc'])+timedelta(seconds=20)
        retry = [dict(event='STARTED', trial_id=tid, attempt=2, status='STARTED', exit_code=-1, start_utc=clock.isoformat()),
                 dict(event='FINISHED', trial_id=tid, attempt=2, status='FAILED', exit_code=1,
                      end_utc=(clock+timedelta(seconds=12.5)).isoformat())]
        write_ledger(self.root, self.rows+retry)
        out = a.analyze(self.root)
        self.assertEqual(out['failed_trials'], [tid])
        self.assertNotIn(tid, out['trials'])
        self.assertEqual(out['H_M1']['domains']['same']['n'], 9)
        self.assertEqual(out['H_M1']['domains']['shadow']['n'], 10)
        self.assertEqual(out['H_M1']['shadow_minus_same']['n'], 9)
        self.assertEqual(out['counts']['failed_attempts'], 1)
        write_ledger(self.root, self.rows+[dict(retry[0], attempt=3)])
        with self.assertRaisesRegex(ValueError, 'retry'):
            a.analyze(self.root)
        retry[1].update(status='SUCCESS', exit_code=0)
        shutil.copyfile(self.root/'trials'/f'{tid}_a1.json', self.root/'trials'/f'{tid}_a2.json')
        write_ledger(self.root, rows+retry)
        out = a.analyze(self.root)
        self.assertEqual(out['counts']['successful'], 160)
        self.assertEqual(out['counts']['failed_attempts'], 1)

    def test_zero_reads_and_nonpositive_free_exclude_only_calculations(self):
        tid = a.cell(183, 'same', 'off')+'_t01_B'
        def zero(r):
            r['throughput']['read_ops_per_sec'] = 0
            r['operations'].update(read_ops_completed=0, read_ops_completed_in_window=0)
            r['lsm_diagnostics']['measured_memtable_point_reads'] = 0
        self.change(tid, zero)
        out = a.analyze(self.root)
        self.assertEqual(out['H_M2']['page']['n'], 10)
        self.assertEqual(out['H_M2']['R_183_over_732']['n'], 9)
        self.assertEqual(out['H_M4']['combinations']['w0183_same']['n'], 9)
        self.assertIsNone(out['trials'][tid]['memtable_share'])
        self.assertIn(tid, out['trials'])
        tid2 = a.cell(366, 'shadow', 'on')+'_t02_B'
        self.change(tid2, lambda r: r['stall_diagnostics']['phase_histograms']['wal_sync']['stats'].update(mean=1e10))
        out = a.analyze(self.root)
        self.assertLess(out['trials'][tid2]['free_share'], 0)
        self.assertEqual(out['H_M1']['domains']['shadow']['n'], 9)
        self.assertEqual(out['H_M4']['combinations']['w0366_shadow']['n'], 9)

    def test_opposite_hypotheses_and_classifications(self):
        for s in self.plan['trials']:
            rate, domain, sync = a.factors(s['cell'])
            if sync == 'on':
                self.change(s['trial_id'], lambda r: r['throughput'].update(read_ops_per_sec=100000))
            elif domain == 'same':
                self.change(s['trial_id'], lambda r: r['throughput'].update(read_ops_per_sec=100000))
            elif rate == 183:
                self.change(s['trial_id'], lambda r: r['throughput'].update(read_ops_per_sec=200000))
        out = a.analyze(self.root)
        self.assertFalse(out['H_M1']['supported'])
        self.assertFalse(out['H_M2']['supported'])
        self.assertFalse(out['H_M3']['supported'])
        self.assertEqual(out['H_M3']['classification'], 'not_equivalent')
        self.assertFalse(out['H_M4']['supported'])
        self.assertEqual(out['H_M4']['combinations']['w0183_shadow']['classification'], 'not_equivalent')
        self.assertFalse(out['paper_claims']['memtable_explanation'])

    def test_plan_and_treatment_fail_closed(self):
        for change in (lambda p: p.update(settle_ms=0), lambda p: p.update(master_seed=-1),
                       lambda p: p['cells'].reverse(), lambda p: p['cells'][0]['spec'].update(write_key_domain='shadow'),
                       lambda p: p['trials'][0].update(seed=0), lambda p: p['trials'][0].update(arm='A'),
                       lambda p: p['trials'][0].update(triplet=2), lambda p: p['trials'][0].update(order_position=2),
                       lambda p: p['trials'][0].update(runlist_position=2), lambda p: p['trials'][0].update(lock_policy='readerpref'),
                       lambda p: p['trials'][0].update(inline_flush_check=True), lambda p: p['trials'].pop()):
            plan = copy.deepcopy(self.plan); change(plan)
            with self.assertRaises(ValueError):
                a.validate_plan(plan)
        tid = a.cell(183, 'shadow', 'off')+'_t01_B'
        self.change(tid, lambda r: r['config'].update(write_key_domain=''))
        with self.assertRaisesRegex(ValueError, 'write_key_domain'):
            a.analyze(self.root)

    def test_provenance_missing_artifact_and_invalid_memtable_count(self):
        platform = a.load_json(self.root/'platform.json')
        write_json(self.root/'platform.json', dict(platform, plan_sha256='b'*64))
        with self.assertRaisesRegex(ValueError, 'platform'):
            a.analyze(self.root)
        write_json(self.root/'platform.json', platform)
        tid = a.CELLS[0]+'_t01_B'
        self.change(tid, lambda r: r['lsm_diagnostics'].update(measured_memtable_point_reads=-1))
        with self.assertRaisesRegex(ValueError, 'memtable/read count'):
            a.analyze(self.root)
        (self.root/'trials'/f'{tid}_a1.json').unlink()
        with self.assertRaisesRegex(ValueError, 'missing SUCCESS artifact'):
            a.analyze(self.root, True)

    def test_mixed_runner_and_revision_rejected(self):
        path = self.root/'trials'/f'{a.CELLS[0]}_t01_B_a1.json'
        t = a.load_json(path)
        t['runtime']['exe_sha256'] = 'b'*64
        write_json(path, t)
        with self.assertRaisesRegex(ValueError, 'mixed runners/revisions'):
            a.analyze(self.root)
        t['runtime']['exe_sha256'] = 'a'*64
        t['result']['git_revision'] = 'another-revision'
        write_json(path, t)
        with self.assertRaisesRegex(ValueError, 'mixed runners/revisions'):
            a.analyze(self.root)

    def test_empty_partial_is_undefined(self):
        write_ledger(self.root, [])
        out = a.analyze(self.root, True)
        self.assertEqual(out['counts']['successful'], 0)
        for key in ('H_M1', 'H_M2', 'H_M3', 'H_M4', 'H_X'):
            self.assertIsNone(out[key]['supported'], key)
        self.assertFalse(out['paper_claims']['capacity_times_free_share_supported_combinations'])
        self.assertIsNone(out['paper_claims']['memtable_explanation'])
        json.dumps(out, allow_nan=False)

    def test_cli_outputs_and_figures_are_deterministic(self):
        dest = self.root/'analysis'
        env = dict(os.environ, MPLCONFIGDIR=str(self.root/'matplotlib'))
        cmd = [sys.executable, str(HERE/'x_memtable_analyze.py'), '--data-dir', str(self.root), '--output-dir', str(dest)]
        first = subprocess.run(cmd, env=env, capture_output=True, text=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        files = sorted(dest.iterdir())
        self.assertEqual({f.name for f in files}, {'x_memtable_results.json', 'x_memtable_tables.md', 'x_memtable_capacity.png'})
        before = {f.name: f.read_bytes() for f in files}
        second = subprocess.run(cmd, env=env, capture_output=True, text=True)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(before, {f.name: f.read_bytes() for f in files})
        self.assertTrue(before['x_memtable_capacity.png'].startswith(b'\x89PNG'))
        write_ledger(self.root, self.rows[2:])
        partial = subprocess.run(cmd, env=env, capture_output=True, text=True)
        self.assertEqual(partial.returncode, 1)
        self.assertIn('FAIL-CLOSED', partial.stderr)
        partial = subprocess.run(cmd+['--allow-partial'], env=env, capture_output=True, text=True)
        self.assertEqual(partial.returncode, 0, partial.stderr)
        self.assertTrue(a.load_json(dest/'x_memtable_results.json')['partial'])


if __name__ == '__main__':
    unittest.main()
