"""Synthetic pre-data checks. No benchmark runner is invoked."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
import uuid

HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location('x_tablecount_analyze', HERE / 'x_tablecount_analyze.py')
a = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(a)


def write_json(path, obj):
    path.write_text(json.dumps(obj), encoding='utf-8')


def write_ledger(root, events):
    (root / 'ledger.jsonl').write_text(''.join(json.dumps(e) + '\n' for e in events), encoding='utf-8')


def fixture(root):
    (root / 'trials').mkdir()
    plan = dict(campaign_id='X-tablecount-v1', master_seed=a.SEED,
                cells=[dict(id=c, spec=a.nominal(c)) for c in a.CELLS], trials=[])
    permutations = (('A', 'AMIN', 'B'), ('A', 'B', 'AMIN'), ('AMIN', 'A', 'B'),
                    ('AMIN', 'B', 'A'), ('B', 'A', 'AMIN'), ('B', 'AMIN', 'A'))
    events = []
    for k in range(1, 11):
        for index, c in enumerate(a.CELLS):
            for pos, arm in enumerate(permutations[(k + index) % 6]):
                tid = f'{c}_t{k:02d}_{arm}'
                h = hashlib.sha256(f'{a.SEED}|{c}|{k}'.encode()).digest()
                s = dict(a.nominal(c), trial_id=tid, cell=c, triplet=k, arm=arm,
                         seed=int.from_bytes(h[:8], 'big', signed=True), order_position=pos + 1,
                         runlist_position=len(plan['trials']) + 1)
                plan['trials'].append(s)
                n, w = s['initial_sstable_count'], s['writer_concurrency']
                v = dict(mean_nanos=100., p50_nanos=80., p99_nanos=200., count=20, max_nanos=210.)
                sync = dict(read_critical_section_nanos={arm: v},
                            writer_lock_wait_nanos=dict(commit=v) if w else {}, publication_nanos={})
                ratio = {4: 1.01, 16: .98, 64: .89, 256: .65}[n] if arm == 'AMIN' else .8 if arm == 'A' else 1
                rate = 1000 * (1 + .02 * k)
                diag = dict(initial_sstables=n, final_sstables=n, measured_compactions=0,
                            compaction_errors=0, publication_events_realized_in_window=0,
                            publication_events_realized_late=0, publication_events_target=0)
                r = dict(status='SUCCESS', trial_id=tid, git_revision='revision', config=copy.deepcopy(s),
                         operations=dict(validation_error_count=0, read_ops_completed=1000,
                                         write_ops_completed=100 if w else 0),
                         timestamps=dict(measured_window_ns=10000000000),
                         throughput=dict(write_ops_per_sec=100 * (1.01 if arm == 'AMIN' else 1) if w else 0,
                                         read_ops_per_sec=rate * ratio),
                         latency_read_nanos=dict(p50=100., p95=200., p99=300., mean=150.),
                         sync_instrumentation=sync, lsm_diagnostics=diag)
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block='X1', cell=c,
                         triplet=k, arm=arm, spec=copy.deepcopy(s), runtime=dict(exe_sha256='a' * 64), result=r)
                write_json(root / 'trials' / f'{tid}_a1.json', t)
                events.extend([dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1),
                               dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0)])
    write_json(root / 'plan.json', plan)
    write_ledger(root, events)
    return plan, events


class TablecountTests(unittest.TestCase):
    def setUp(self):
        self.root = HERE / ('.tablecount-test-' + uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.events = fixture(self.root)

    def test_trend_present_and_deterministic(self):
        out = a.analyze(self.root)
        self.assertTrue(all(v['decline_detected'] for v in out['H_T1'].values()))
        self.assertTrue(all(v['n'] == 10 for v in out['H_T1'].values()))
        self.assertEqual(out['counts']['successful'], 480)
        self.assertEqual(out['bootstrap_seed'], 20261105)
        self.assertTrue(all(v['matches'] for v in out['H_T2_predictions']))
        self.assertEqual(out['margin_crossings']['r04_w0']['R']['first_point_estimate_outside_margin'], 64)
        self.assertEqual(out['cells'][a.CELLS[0]]['arms']['A']['read_cs_mean']['mean'], 100)
        self.assertEqual(out, a.analyze(self.root))
        json.dumps(out, allow_nan=False)
        self.assertIn('Separate microbenchmark', a.tables(out))

    def test_no_trend(self):
        for s in self.plan['trials']:
            if s['arm'] == 'AMIN':
                path = self.root / 'trials' / f"{s['trial_id']}_a1.json"
                t = a.load_json(path)
                t['result']['throughput']['read_ops_per_sec'] = 1000 * (1 + .02 * s['triplet'])
                write_json(path, t)
        out = a.analyze(self.root)
        self.assertTrue(all(not v['decline_detected'] for v in out['H_T1'].values()))
        self.assertTrue(all(v['classification'] == 'equivalent' for v in out['H_T2']['R'].values()))

    def test_dropped_triplet(self):
        tid = self.plan['trials'][0]['trial_id']
        write_ledger(self.root, [e for e in self.events if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertEqual(len(out['dropped_triplets']), 1)
        self.assertEqual(out['cells'][a.CELLS[0]]['complete_triplets'], list(range(2, 11)))
        self.assertEqual(out['H_T1']['r04_w0']['rounds'], list(range(2, 11)))
        self.assertEqual(out['H_T1']['r04_w1']['n'], 10)
        self.assertEqual(out['cells'][a.CELLS[0]]['arms']['A']['R']['n'], 9)
        self.assertEqual(len([t for t in out['trials'].values() if t['cell'] == a.CELLS[0]]), 29)
        self.assertTrue(out['partial'])

    def test_read_only_has_no_W_tests(self):
        out = a.analyze(self.root)
        self.assertEqual(len(out['H_T2']['R']), 16)
        self.assertEqual(len(out['H_T2']['W']), 8)
        for c, cs in out['cells'].items():
            if c.endswith('_w0'):
                self.assertNotIn('W', cs['equivalence'])
                self.assertNotIn(c, out['H_T2']['W'])
                self.assertEqual(cs['ratios']['W'], {})
                self.assertEqual(cs['zero_adjusted_W_triplets'], [])
                self.assertIsNone(cs['arms']['B']['W']['mean'])
                self.assertEqual(cs['arms']['B']['W']['not_applicable_n'], 10)
                self.assertIsNone(cs['arms']['B']['commit_wait_p99']['mean'])

    def test_zero_rule_and_metric_exclusion(self):
        c = a.CELLS[1]
        path = self.root / 'trials' / f'{c}_t01_A_a1.json'
        t = a.load_json(path)
        t['result']['throughput']['write_ops_per_sec'] = 0
        t['result']['latency_read_nanos']['p99'] = 0
        write_json(path, t)
        out = a.analyze(self.root)
        cs = out['cells'][c]
        self.assertEqual(cs['zero_adjusted_W_triplets'], [1])
        self.assertEqual(cs['equivalence']['W']['n'], 10)
        self.assertAlmostEqual(cs['equivalence']['W']['geometric_mean_ratio'],
                               ((101.1 / 100.1) * 1.01 ** 9) ** .1)
        self.assertEqual(cs['ratios']['read_p99']['A/B']['undefined_n'], 1)
        self.assertTrue(any(v['metric'] == 'read_p99' for v in out['outcome_exclusions']))

    def test_provenance_and_unplanned_treatment_fail_closed(self):
        path = self.root / 'trials' / f"{self.plan['trials'][0]['trial_id']}_a1.json"
        t = a.load_json(path)
        t['result']['lsm_diagnostics']['measured_compactions'] = 1
        write_json(path, t)
        with self.assertRaisesRegex(ValueError, 'unplanned compaction'):
            a.analyze(self.root)
        t['result']['lsm_diagnostics']['measured_compactions'] = 0
        write_json(path, t)
        write_json(self.root / 'platform.json', dict(campaign_id='wrong', plan_sha256='bad'))
        with self.assertRaisesRegex(ValueError, 'platform'):
            a.analyze(self.root)

    def test_cli_and_separate_benchmark(self):
        (self.root / 'lifecycle_bench.txt').write_text(
            'BenchmarkLifecycle/AMIN/N4/g1-16 1 400 ns/op 0 B/op 0 allocs/op\n'
            'BenchmarkLifecyclePublication/B/N256-16 1000 500 ns/op 2400 B/op 2 allocs/op\n', encoding='utf-8')
        dest = self.root / 'output'
        args = [sys.executable, str(HERE / 'x_tablecount_analyze.py'), '--data-dir', str(self.root),
                '--output-dir', str(dest)]
        result = subprocess.run(args, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        out = a.load_json(dest / 'x_tablecount_results.json')
        self.assertEqual(len(out['microbenchmark']['measurements']), 2)
        self.assertTrue(out['microbenchmark']['measurements'][0]['compile_check_only'])
        self.assertFalse(out['microbenchmark']['measurements'][1]['compile_check_only'])
        self.assertEqual(out['microbenchmark']['measurements'][1]['allocs_per_op'], 2)
        original = (dest / 'x_tablecount_results.json').read_bytes()
        self.assertEqual(subprocess.run(args, capture_output=True).returncode, 0)
        self.assertEqual(original, (dest / 'x_tablecount_results.json').read_bytes())
        self.assertIn('H-T1', (dest / 'x_tablecount_tables.md').read_text(encoding='utf-8'))


if __name__ == '__main__':
    unittest.main()
