"""Synthetic artifacts only; no engine, campaign trial or smoke is launched."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import subprocess
import shutil
import sys
import unittest
import uuid

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('x_locks_analyze', HERE/'x_locks_analyze.py')
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


def write_json(path, value):
    path.write_text(json.dumps(value), encoding='utf-8')


def write_ledger(root, rows):
    (root/'ledger.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in rows), encoding='utf-8')


def histogram(count, value=1000000):
    return dict(sample_count=count, min=value if count else 0, max=value if count else 0,
                mean=value if count else 0, p50=value if count else 0,
                p95=value if count >= 20 else None, p99=value if count >= 100 else None,
                p999=value if count >= 1000 else None)


def accounting(count, late=0):
    return dict(offered=count+late, attempted=count+late, completed_in_window=count, completed_late=late,
                failed=0, abandoned=0, queued=0, outstanding_at_window_close=late)


def result(s, commits=None, reads=None):
    policy, arm = s['cell'].rsplit('_', 1)[1], s['arm']
    if commits is None:
        commits = (1000 if policy == 'readerpref' else 4000) if arm == 'A' else 10000
    if reads is None:
        reads = s['reader_concurrency']*commits*(4 if policy == 'readerpref' else 2) if arm == 'A' else 200000
    return dict(status='SUCCESS', trial_id=s['trial_id'], config=copy.deepcopy(s), git_revision='synthetic-revision',
                timestamps=dict(measured_window_ns=10000000000),
                operations=dict(read_ops_completed=reads, read_ops_completed_in_window=reads, read_ops_completed_late=0,
                                write_ops_completed=commits+1, write_ops_completed_in_window=commits, write_ops_completed_late=1,
                                validation_error_count=0, error_ops_count=0),
                throughput=dict(read_ops_per_sec=reads/10, write_ops_per_sec=commits/10),
                latency_read_nanos=histogram(reads, 100000), latency_write_nanos=histogram(commits),
                latency_write_late_nanos=histogram(1, 2000000),
                transaction_ledger=dict(reads=accounting(reads), writes=accounting(commits, 1)),
                lsm_diagnostics=dict(initial_sstables=4, final_sstables=4, measured_compactions=0,
                                     compaction_errors=0, measured_flushes=0),
                sync_instrumentation=dict(writer_lock_wait_nanos=dict(commit=dict(
                    count=commits+1, mean_nanos=1000000, p50_nanos=1000000, p99_nanos=1000000))))


def fixture(root):
    (root/'trials').mkdir()
    plan = dict(campaign_id='X-locks-v1', master_seed=a.SEED, settle_ms=20000,
                cells=[dict(id=c, spec=dict(a.nominal(c), cell=c, schema='i7.trial-spec.v1')) for c in a.CELLS], trials=[])
    rows, clock = [], datetime(2026, 11, 9, tzinfo=timezone.utc)
    for k in range(1, 11):
        # Synthetic order exercises the analysis invariants; exact Go PRNG
        # permutations are independently covered by the Go driver tests.
        for entry in a.ENTRIES:
            for pos, (policy, arm) in enumerate(a.RUNS):
                cell = entry+'_'+policy
                tid = f'{cell}_t{k:02d}_{arm}'
                seed = hashlib.sha256(f'{a.SEED}|{entry}|{k}'.encode()).digest()
                s = dict(a.nominal(cell), schema='i7.trial-spec.v1', cell=cell, trial_id=tid, triplet=k,
                         arm=arm, seed=int.from_bytes(seed[:8], 'big', signed=True), order_position=pos+1,
                         runlist_position=len(plan['trials'])+1)
                # Match the driver's omitted default policy.
                if not s['lock_policy']:
                    del s['lock_policy']
                plan['trials'].append(s)
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block='X1', cell=cell,
                         triplet=k, arm=arm, spec=copy.deepcopy(s), runtime=dict(exe_sha256='a'*64), result=result(s))
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
    def test_frozen_q_decisions_with_zero_commits(self):
        for values, h2 in (([a.ABOVE, 2.11, 4], True),
                           ([a.ABOVE, 2.10, 4], False),
                           ([a.ABOVE, 2.09, 4], False),
                           ([a.ABOVE]*10, True)):
            with self.subTest(values=values):
                stat = a.arithmetic(values)
                self.assertIsNone(stat['t_ci'])
                self.assertFalse(a.q_decision(stat, primary=True))
                self.assertIs(a.q_decision(stat, primary=False), h2)

    def test_finite_q_ci_decisions_and_insufficient_evidence(self):
        for values, h1, h2 in (([2]*10, True, False),
                               ([3]*10, False, True),
                               ([1, 3]*5, False, False),
                               ([3], False, False),
                               ([], False, False)):
            with self.subTest(values=values):
                stat = a.arithmetic(values)
                self.assertIs(a.q_decision(stat, primary=True), h1)
                self.assertIs(a.q_decision(stat, primary=False), h2)

    def test_finite_q_ci_boundaries(self):
        # Test the decision boundaries directly, independently of floating-point
        # rounding in arithmetic CI construction for constant decimal samples.
        for ci, h1, h2 in (([1.90, 2.10], True, False),
                           ([1.89, 2.10], False, False),
                           ([1.90, 2.11], False, False),
                           ([2.10, 3], False, False),
                           ([2.11, 3], False, True)):
            with self.subTest(ci=ci):
                stat = dict(t_ci=ci)
                self.assertIs(a.q_decision(stat, primary=True), h1)
                self.assertIs(a.q_decision(stat, primary=False), h2)

    def test_q_campaign_decisions_never_inconclusive(self):
        for supported in (True, False):
            for partial in (True, False):
                out = a.prediction({'setting': dict(supported=supported)}, partial, decisive=True)
                self.assertIs(out['supported'], supported and not partial)
                self.assertEqual(out['verdict'], 'supported' if supported and not partial else 'not_supported')

    def test_arithmetic_mean_and_95_t_ci(self):
        values = [1.8, 2, 2.1, 2.2]
        stat = a.arithmetic(values)
        half = float(a.base.stats.t.ppf(.975, 3))*float(a.base.np.std(values, ddof=1))/2
        self.assertEqual(stat['mean'], sum(values)/4)
        self.assertAlmostEqual(stat['t_ci'][0], stat['mean']-half)
        self.assertEqual(stat, a.arithmetic(values))
        self.assertIsNone(a.arithmetic([2])['t_ci'])
        zero = a.arithmetic([2, a.ABOVE])
        self.assertEqual(zero['mean'], a.ABOVE)
        self.assertIsNone(zero['t_ci'])
        self.assertEqual(zero['n'], 2)

    def test_zero_replaces_only_zero_W(self):
        g = {k: {('readerpref', 'A'): 'x'+str(k), ('go', 'A'): 'y'+str(k)} for k in (1, 2, 3)}
        records = {tid: dict(W=0 if tid.startswith('x') else 10) for unit in g.values() for tid in unit.values()}
        stat = a.ratio(g, records, ('readerpref', 'A'), ('go', 'A'), 'W')
        self.assertAlmostEqual(stat['geometric_mean_ratio'], .01)
        self.assertEqual(stat['zero_adjusted_rounds'], [1, 2, 3])
        self.assertEqual(records['x1']['W'], 0)
        expected = a.base.paired_stats([math.log(.01)]*3, seed=a.SEED)
        for actual, target in zip(stat['bootstrap_ci'], expected['bootstrap_ci']):
            self.assertAlmostEqual(actual, target)

    def test_equivalence_classes_and_planned_holm_family(self):
        for ratio, expected in ((1, 'equivalent'), (.5, 'not_equivalent')):
            family = {str(i): a.base.tost([math.log(ratio)]*10, seed=a.SEED) for i in range(8)}
            a.paced.adjust_family(family, 'planned eight')
            self.assertTrue(all(v['classification'] == expected for v in family.values()))
        family = {'defined': dict(p_tost=.007, t_ci_log=[-.05, .05])}
        family.update({str(i): dict(p_tost=None, t_ci_log=None) for i in range(7)})
        a.paced.adjust_family(family, 'planned eight')
        self.assertAlmostEqual(family['defined']['holm_p'], .056)
        self.assertEqual(family['defined']['classification'], 'inconclusive')


class ArtifactTests(unittest.TestCase):
    def setUp(self):
        work = HERE.parents[2]/'_work'
        work.mkdir(exist_ok=True)
        self.root = work/('test_x_locks_'+uuid.uuid4().hex)
        self.root.mkdir()
        self.root.resolve().relative_to(work.resolve())
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.rows = fixture(self.root)

    def change(self, tid, edit):
        path = self.root/'trials'/f'{tid}_a1.json'
        value = json.loads(path.read_text(encoding='utf-8'))
        edit(value['result'])
        write_json(path, value)

    def test_all_predictions_descriptions_and_determinism(self):
        out = a.analyze(self.root)
        for name in ('H_L1', 'H_L2', 'H_L3'):
            self.assertEqual(out[name]['verdict'], 'supported')
            self.assertEqual(out[name]['supported_settings'], 8)
        self.assertEqual(out['H_L4']['verdict'], 'supported')
        self.assertEqual(out['H_L4']['phasefair']['planned'], 8)
        self.assertEqual(out['H_L4']['readerpref']['planned'], 4)
        self.assertEqual(out['counts']['successful'], 480)
        self.assertEqual(out['bootstrap_seed'], 20261109)
        self.assertEqual(out['gaps']['min_ms'], 20000)
        self.assertEqual(len(out['cells']), 24)
        for cs in out['cells'].values():
            self.assertEqual(cs['S']['n'], 10)
            self.assertEqual(cs['read_ratio_A_AMIN']['n'], 10)
            for arm in a.ARMS:
                for m in ('q', 'W', 'R', 'read_p50', 'read_p99', 'write_p50', 'write_p99', 'commit_wait_mean_ns'):
                    self.assertEqual(cs['arms'][arm][m]['n'], 10)
        self.assertEqual(out, a.analyze(self.root))
        json.dumps(out, allow_nan=False)
        self.assertIn('H_L4', a.tables(out))

    def test_zero_commits_are_retained_and_late_latency_is_separate(self):
        tid = a.ENTRIES[0]+'_readerpref_t01_A'
        s = next(s for s in self.plan['trials'] if s['trial_id'] == tid)
        self.change(tid, lambda r: r.update(result(s, commits=0, reads=10000)))
        out = a.analyze(self.root)
        v = out['trials'][tid]
        self.assertEqual(v['W'], 0)
        self.assertEqual(v['q'], a.ABOVE)
        self.assertIsNone(v['write_p50'])
        self.assertIsNone(v['write_p99'])
        self.assertEqual(v['write_latency_late']['sample_count'], 1)
        cs = out['cells'][s['cell']]
        self.assertEqual(cs['zero_commit_counts']['A'], 1)
        self.assertEqual(cs['arms']['A']['q']['n'], 10)
        self.assertEqual(cs['S']['n'], 10)
        self.assertEqual(cs['S']['zero_adjusted_rounds'], [1])
        self.assertEqual(out['H_L3']['settings'][a.ENTRIES[0]]['n'], 10)
        self.assertTrue(out['H_L2']['settings'][a.ENTRIES[0]]['supported'])
        self.assertEqual(out['H_L2']['verdict'], 'supported')
        self.assertEqual(out['counts']['failed'], 0)
        json.dumps(out, allow_nan=False)

    def test_missing_partial_and_whole_six_run_unit_drop(self):
        tid = a.ENTRIES[0]+'_go_t01_A'
        write_ledger(self.root, [e for e in self.rows if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertTrue(out['partial'])
        self.assertEqual(out['counts']['dropped_units'], 1)
        for policy in a.POLICIES:
            self.assertEqual(out['cells'][a.ENTRIES[0]+'_'+policy]['complete_rounds'], list(range(2, 11)))
        self.assertIn(a.ENTRIES[0]+'_readerpref_t01_AMIN', out['trials'])
        self.assertEqual(out['H_L1']['verdict'], 'not_supported')
        self.assertFalse(out['H_L1']['supported'])
        self.assertEqual(out['H_L2']['verdict'], 'not_supported')
        self.assertIn('PARTIAL DATA', a.tables(out))

    def test_frozen_zero_rule_rejects_H_L1_and_H_L2_with_finite_counterexample(self):
        for policy in ('phasefair', 'readerpref'):
            tid = a.ENTRIES[0]+'_'+policy+'_t01_A'
            s = next(s for s in self.plan['trials'] if s['trial_id'] == tid)
            self.change(tid, lambda r: r.update(result(s, commits=0, reads=10000)))
        tid = a.ENTRIES[0]+'_readerpref_t02_A'
        s = next(s for s in self.plan['trials'] if s['trial_id'] == tid)
        self.change(tid, lambda r: r.update(result(s, commits=1000, reads=2100*s['reader_concurrency'])))
        out = a.analyze(self.root)
        for name in ('H_L1', 'H_L2'):
            self.assertFalse(out[name]['settings'][a.ENTRIES[0]]['supported'])
            self.assertIsNone(out[name]['settings'][a.ENTRIES[0]]['t_ci'])
            self.assertEqual(out[name]['verdict'], 'not_supported')

    def test_all_zero_commit_setting_meets_H_L2(self):
        for s in self.plan['trials']:
            if s['cell'] == a.ENTRIES[0]+'_readerpref' and s['arm'] == 'A':
                self.change(s['trial_id'], lambda r: r.update(result(s, commits=0, reads=10000)))
        out = a.analyze(self.root)
        stat = out['H_L2']['settings'][a.ENTRIES[0]]
        self.assertEqual(stat['above_all_bounds_n'], 10)
        self.assertIsNone(stat['t_ci'])
        self.assertTrue(stat['supported'])
        self.assertEqual(out['H_L2']['verdict'], 'supported')

    def test_latest_failure_retry_and_missing_artifact(self):
        tid = a.ENTRIES[0]+'_go_t01_A'
        retry = [dict(e, attempt=2, status='FAILED', exit_code=1) for e in self.rows if e['trial_id'] == tid]
        clock = datetime.fromisoformat(self.rows[-1]['end_utc'])+timedelta(seconds=20)
        retry[0].update(status='STARTED', exit_code=-1, start_utc=clock.isoformat())
        retry[1]['end_utc'] = (clock+timedelta(seconds=12.5)).isoformat()
        write_ledger(self.root, self.rows+retry)
        out = a.analyze(self.root)
        self.assertEqual(out['failed_trials'], [tid])
        self.assertNotIn(tid, out['trials'])
        self.assertEqual(out['H_L3']['settings'][a.ENTRIES[0]]['n'], 9)
        self.assertEqual(len(out['dropped_units'][0]['unavailable_runs']), 1)
        write_ledger(self.root, self.rows+[dict(retry[0], attempt=3)])
        with self.assertRaisesRegex(ValueError, 'retry'):
            a.analyze(self.root)
        write_ledger(self.root, self.rows)
        (self.root/'trials'/f'{tid}_a1.json').unlink()
        with self.assertRaisesRegex(ValueError, 'missing SUCCESS artifact'):
            a.analyze(self.root, True)

    def test_first_failure_requires_retry(self):
        tid = a.ENTRIES[0]+'_phasefair_t01_AMIN'
        rows = [dict(e, status='FAILED', exit_code=1) if e['trial_id'] == tid and e['event'] == 'FINISHED' else e for e in self.rows]
        write_ledger(self.root, rows)
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertEqual(out['pending_retry'], [tid])

    def test_opposite_predictions_and_descriptive_low_d_lease(self):
        for s in self.plan['trials']:
            if s['arm'] == 'A' and s['cell'].endswith('_phasefair'):
                self.change(s['trial_id'], lambda r: r.update(result(s, commits=4000, reads=s['reader_concurrency']*4000)))
            if s['arm'] == 'AMIN' and s['cell'].endswith('_readerpref') and s['read_io_delay_us'] < 200:
                self.change(s['trial_id'], lambda r: r.update(result(s, commits=100)))
        out = a.analyze(self.root)
        self.assertEqual(out['H_L1']['verdict'], 'not_supported')
        self.assertEqual(out['H_L4']['readerpref']['verdict'], 'supported')
        self.assertEqual(len([cs for cs in out['cells'].values() if 'descriptive_W_AMIN_relative_go' in cs]), 4)

    def test_plan_treatments_seed_and_provenance_fail_closed(self):
        original = copy.deepcopy(self.plan)
        for edit in (lambda p: p.update(settle_ms=0),
                     lambda p: p['cells'][0]['spec'].update(lock_policy='readerpref'),
                     lambda p: p['trials'][0].update(seed=0),
                     lambda p: p['trials'][0].update(arm='B'),
                     lambda p: p['trials'][0].update(inline_flush_check=True),
                     lambda p: p['trials'][0].update(diag_trace=True),
                     lambda p: p['trials'][0].update(compaction_trigger_interval_ms=250),
                     lambda p: p['trials'][0].update(paced_write_rate_per_sec=100),
                     lambda p: p['trials'][0].update(order_position=6)):
            plan = copy.deepcopy(original); edit(plan)
            with self.assertRaises(ValueError):
                a.validate_plan(plan)
        self.change(self.plan['trials'][0]['trial_id'], lambda r: r.update(git_revision='other'))
        with self.assertRaisesRegex(ValueError, 'mixed runners/revisions'):
            a.analyze(self.root)

    def test_faults_and_metric_local_undefined(self):
        tid = self.plan['trials'][0]['trial_id']
        path = self.root/'trials'/f'{tid}_a1.json'
        original = json.loads(path.read_text(encoding='utf-8'))
        for edit in (lambda r: r['operations'].update(error_ops_count=1),
                     lambda r: r['lsm_diagnostics'].update(measured_compactions=1),
                     lambda r: r['transaction_ledger']['writes'].update(attempted=1),
                     lambda r: r['latency_write_nanos'].update(sample_count=1),
                     lambda r: r['throughput'].update(write_ops_per_sec=0)):
            value = copy.deepcopy(original); edit(value['result']); write_json(path, value)
            with self.assertRaises(ValueError):
                a.analyze(self.root)
        value = copy.deepcopy(original); value['result']['latency_write_nanos']['p99'] = None; write_json(path, value)
        out = a.analyze(self.root)
        self.assertEqual(out['cells'][self.plan['trials'][0]['cell']]['arms']['A']['write_p99']['undefined_n'], 1)
        self.assertEqual(out['H_L3']['settings'][a.ENTRIES[0]]['n'], 10)

    def test_cli_and_figures_are_byte_deterministic(self):
        outputs = []
        for i in range(2):
            directory = self.root/f'analysis{i}'
            run = subprocess.run([sys.executable, str(HERE/'x_locks_analyze.py'), '--data-dir', str(self.root),
                                  '--output-dir', str(directory)], capture_output=True, text=True)
            self.assertEqual(run.returncode, 0, run.stderr)
            outputs.append({p.name: p.read_bytes() for p in directory.iterdir()})
        self.assertEqual(outputs[0], outputs[1])
        self.assertEqual(set(outputs[0]), {'x_locks_results.json', 'x_locks_tables.md', 'x_locks_q.png', 'x_locks_s.png'})


if __name__ == '__main__':
    unittest.main()
