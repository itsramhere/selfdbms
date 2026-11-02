"""Synthetic pre-data checks. No benchmark runner is invoked."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import shutil
import uuid
import unittest

HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location('x_compaction_analyze', HERE / 'x_compaction_analyze.py')
a = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(a)


def write_json(path, obj):
    path.write_text(json.dumps(obj), encoding='utf-8')


def write_ledger(root, events):
    (root / 'ledger.jsonl').write_text(''.join(json.dumps(e) + '\n' for e in events), encoding='utf-8')


def fixture(root):
    (root / 'trials').mkdir()
    plan = dict(campaign_id='X-compaction-v1', master_seed=a.SEED,
                cells=[dict(id=c, spec=a.nominal(c)) for c in a.CELLS], trials=[])
    events = []
    for k in range(1, 11):
        for c in a.CELLS:
            for pos, arm in enumerate(a.ARMS):
                tid = f'{c}_t{k:02d}_{arm}'
                h = hashlib.sha256(f"{a.SEED}|{c.rsplit('_', 1)[0]}|{k}".encode()).digest()
                s = dict(a.nominal(c), trial_id=tid, cell=c, triplet=k, arm=arm,
                         seed=int.from_bytes(h[:8], 'big', signed=True), order_position=pos + 1,
                         runlist_position=len(plan['trials']) + 1)
                plan['trials'].append(s)
                v = dict(mean_nanos=100., p50_nanos=80., p99_nanos=200., count=20, max_nanos=210.)
                on = c.endswith('_on')
                sync = dict(read_critical_section_nanos={arm: v}, writer_lock_wait_nanos=dict(commit=v),
                            publication_nanos={})
                if on:
                    sites = ['compaction_snapshot_publish'] if arm == 'B' else ['compaction_start', 'compaction_publish']
                    sync['writer_lock_wait_nanos'].update({site: v for site in sites})
                    sync['publication_nanos'].update({site: v for site in sites})
                    sync.update(compaction_wall_nanos=dict(v, count=3), retirement_lag_nanos=dict(v, count=12),
                                compaction_inputs=dict(mean=4., max=5), retired_tables_high_water=4)
                diag = dict(measured_compactions=3 if on else 0, compactions_in_window=2 if on else None,
                            compaction_successes=3 if on else 0, final_sstables=1 if on else 4,
                            publication_events_realized_in_window=20, publication_events_realized_late=0,
                            publication_events_target=0)
                rate = 100 * (1 + .02 * k) * (1.01 if arm == 'AMIN' else .8 if arm == 'A' else 1)
                r = dict(status='SUCCESS', trial_id=tid, git_revision='revision', config=copy.deepcopy(s),
                         operations=dict(validation_error_count=0, read_ops_completed=1000),
                         timestamps=dict(measured_window_ns=10000000000),
                         throughput=dict(write_ops_per_sec=rate, read_ops_per_sec=rate * 10),
                         latency_read_nanos=dict(p50=100., p95=200., p99=300., mean=150.),
                         sync_instrumentation=sync, lsm_diagnostics=diag)
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block='X3', cell=c,
                         triplet=k, arm=arm, spec=copy.deepcopy(s), runtime=dict(exe_sha256='a' * 64), result=r)
                write_json(root / 'trials' / f'{tid}_a1.json', t)
                events.extend([dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1),
                               dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0)])
    write_json(root / 'plan.json', plan)
    write_ledger(root, events)
    return plan, events


class CompactionTests(unittest.TestCase):
    def setUp(self):
        self.root = HERE / ('.compaction-test-' + uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.events = fixture(self.root)

    def test_all_equivalent_and_deterministic(self):
        out = a.analyze(self.root)
        self.assertTrue(out['H_C']['all_equivalent'])
        self.assertEqual(out['H_C']['equivalent'], 12)
        self.assertEqual(out['bootstrap_seed'], 20261104)
        self.assertEqual(out['counts']['successful'], 360)
        for cs in out['cells'].values():
            self.assertEqual(cs['complete_triplets'], list(range(1, 11)))
            self.assertTrue(all(v['classification'] == 'equivalent' for v in cs['equivalence'].values()))
        self.assertEqual(out, a.analyze(self.root))
        b = out['cells'][a.CELLS[1]]['arms']['B']
        self.assertEqual(b['compaction_inputs_mean']['mean'], 4)
        self.assertEqual(b['retirement_lag_count']['mean'], 12)
        self.assertEqual(b['compaction_snapshot_publish_hold_p99_nanos']['mean'], 200)
        self.assertEqual(b['compaction_start_hold_mean_nanos']['undefined_n'], 10)
        self.assertAlmostEqual(out['on_off'][a.BASES[0]]['AMIN']['W']['geometric_mean_ratio'], 1)
        self.assertIsNotNone(out['round_trend_R'][a.CELLS[0]]['AMIN']['log_R_slope_per_round'])
        self.assertIn('Failures and exclusions', a.tables(out))
        json.dumps(out, allow_nan=False)

    def test_one_non_equivalent_cell(self):
        cell = a.CELLS[1]
        for k in range(1, 11):
            path = self.root / 'trials' / f'{cell}_t{k:02d}_AMIN_a1.json'
            t = a.load_json(path)
            t['result']['throughput']['write_ops_per_sec'] *= .6
            write_json(path, t)
        out = a.analyze(self.root)
        self.assertEqual(out['cells'][cell]['equivalence']['W']['classification'], 'not_equivalent')
        self.assertEqual(out['cells'][cell]['equivalence']['R']['classification'], 'equivalent')
        self.assertEqual(out['H_C']['equivalent'], 11)
        self.assertFalse(out['H_C']['all_equivalent'])

    def test_dropped_triplet(self):
        tid = self.plan['trials'][0]['trial_id']
        write_ledger(self.root, [e for e in self.events if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertEqual(len(out['dropped_triplets']), 1)
        self.assertEqual(out['dropped_triplets'][0]['unavailable_arms'], ['A'])
        self.assertEqual(out['cells'][a.CELLS[0]]['complete_triplets'], list(range(2, 11)))
        self.assertEqual(out['on_off'][a.BASES[0]]['B']['W']['paired_rounds'], list(range(2, 11)))
        self.assertTrue(out['partial'])

    def test_compaction_error_is_fault(self):
        tid = self.plan['trials'][3]['trial_id']
        path = self.root / 'trials' / f'{tid}_a1.json'
        t = a.load_json(path)
        t['status'] = t['result']['status'] = 'FAILED'
        t['result']['error'] = 'compaction: synthetic IO fault'
        t['result']['lsm_diagnostics']['compaction_errors'] = 1
        write_json(path, t)
        write_json(self.root / 'trials' / f'{tid}_a2.json', t)
        for e in self.events:
            if e['trial_id'] == tid and e['event'] == 'FINISHED':
                e.update(status='FAILED', exit_code=1)
        self.events += [dict(event='STARTED', trial_id=tid, attempt=2, status='STARTED', exit_code=-1),
                        dict(event='FINISHED', trial_id=tid, attempt=2, status='FAILED', exit_code=1)]
        write_ledger(self.root, self.events)
        out = a.analyze(self.root)
        self.assertEqual(out['failed_trials'], [tid])
        self.assertEqual(len(out['dropped_triplets']), 1)
        self.assertNotIn(tid, out['trials'])
        self.assertTrue(all(v['compaction_errors'] == 1 for v in out['failed_attempts_detail']))
        # A malformed SUCCESS can never sneak an engine fault into inference.
        t['status'] = t['result']['status'] = 'SUCCESS'
        write_json(self.root / 'trials' / f'{tid}_a2.json', t)
        self.events[-1].update(status='SUCCESS', exit_code=0)
        write_ledger(self.root, self.events)
        with self.assertRaisesRegex(ValueError, 'compaction errors'):
            a.analyze(self.root)

    def test_latest_attempt_and_provenance_fail_closed(self):
        tid = self.plan['trials'][0]['trial_id']
        self.events += [dict(event='STARTED', trial_id=tid, attempt=2, status='STARTED', exit_code=-1)]
        write_ledger(self.root, self.events)
        out = a.analyze(self.root, True)
        self.assertNotIn(tid, out['trials'])
        self.assertEqual(len(out['dropped_triplets']), 1)
        self.assertEqual(out['counts']['retried'], 1)
        write_json(self.root / 'platform.json', dict(campaign_id='wrong', plan_sha256='bad'))
        with self.assertRaisesRegex(ValueError, 'platform'):
            a.analyze(self.root, True)

    def test_cli(self):
        dest = self.root / 'output'
        result = subprocess.run([sys.executable, str(HERE / 'x_compaction_analyze.py'),
                                 '--data-dir', str(self.root), '--output-dir', str(dest)],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        out = a.load_json(dest / 'x_compaction_results.json')
        self.assertTrue(out['H_C']['all_equivalent'])
        self.assertIn('Round trends', (dest / 'x_compaction_tables.md').read_text(encoding='utf-8'))


if __name__ == '__main__':
    unittest.main()
