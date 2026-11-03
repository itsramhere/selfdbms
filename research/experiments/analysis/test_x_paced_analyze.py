"""Synthetic pre-data checks. No benchmark runner is invoked."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import shutil
import uuid
import subprocess
import sys
import unittest

HERE = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location('x_paced_analyze', HERE / 'x_paced_analyze.py')
a = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(a)


def write_json(path, obj):
    path.write_text(json.dumps(obj), encoding='utf-8')


def write_ledger(root, events):
    (root / 'ledger.jsonl').write_text(''.join(json.dumps(e) + '\n' for e in events), encoding='utf-8')


def fixture(root):
    (root / 'trials').mkdir()
    def serialized_nominal(c):
        return {k: v for k, v in a.nominal(c).items() if k != 'open_loop_rate_per_sec' or v != 0}
    plan = dict(campaign_id='X-paced-v1', master_seed=a.SEED,
                cells=[dict(id=c, spec=serialized_nominal(c)) for c in a.CELLS], trials=[])
    permutations = (('A', 'AMIN', 'B'), ('A', 'B', 'AMIN'), ('AMIN', 'A', 'B'),
                    ('AMIN', 'B', 'A'), ('B', 'A', 'AMIN'), ('B', 'AMIN', 'A'))
    events = []
    for k in range(1, 11):
        for index, c in enumerate(a.CELLS):
            for pos, arm in enumerate(permutations[(k + index) % 6]):
                tid = f'{c}_t{k:02d}_{arm}'
                h = hashlib.sha256(f'{a.SEED}|{c}|{k}'.encode()).digest()
                s = dict(serialized_nominal(c), trial_id=tid, cell=c, triplet=k, arm=arm,
                         seed=int.from_bytes(h[:8], 'big', signed=True), order_position=pos + 1,
                         runlist_position=len(plan['trials']) + 1)
                plan['trials'].append(s)
                d, f = s['read_io_delay_us'], int(c.split('_')[2][1:])
                v = dict(mean_nanos=100., p50_nanos=80., p99_nanos=200., count=200, max_nanos=210.)
                sync = dict(read_critical_section_nanos={arm: v},
                            writer_lock_wait_nanos=dict(commit=v), publication_nanos={})
                period = int(1e9 / s['paced_write_rate_per_sec'])
                offered = (10000000000 + period - 1) // period
                completed = offered // 2 if arm == 'A' and f == 80 and d > 0 else offered
                ratio = {0: 1, 200: 2, 2000: 4}[d] if arm == 'A' else 1
                diag = dict(initial_sstables=4, final_sstables=4, measured_compactions=0, measured_flushes=0,
                            compaction_errors=0, publication_events_realized_in_window=0,
                            publication_events_realized_late=0, publication_events_target=0)
                def accounting(n, done):
                    return dict(offered=n, attempted=done, completed_in_window=done,
                                completed_late=0, failed=0, abandoned=0, queued=n-done,
                                outstanding_at_window_close=0)
                def histogram(value, count=200):
                    return dict(sample_count=count, p50=value, p95=value, p99=value, mean=value)
                paced = dict(write_ticks_offered=offered, write_ticks_unissued=offered-completed,
                             read_arrivals_abandoned=0, max_read_lateness_ns=0,
                             write_service_nanos=histogram(100), write_response_nanos=histogram(100*ratio),
                             write_service_late_nanos=histogram(0, 0), write_response_late_nanos=histogram(0, 0))
                if c.endswith('_open'):
                    paced['read_response_nanos'] = histogram(120)
                    paced['read_response_late_nanos'] = histogram(0, 0)
                r = dict(status='SUCCESS', trial_id=tid, git_revision='revision', config=copy.deepcopy(s),
                         operations=dict(validation_error_count=0, read_ops_completed=1000,
                                         write_ops_completed=completed, write_ops_completed_in_window=completed,
                                         write_ops_completed_late=0),
                         timestamps=dict(measured_window_ns=10000000000), paced_load=paced,
                         transaction_ledger=dict(reads=accounting(1000, 1000), writes=accounting(offered, completed)),
                         throughput=dict(write_ops_per_sec=completed/10, read_ops_per_sec=1000*(1+.02*k)),
                         latency_read_nanos=histogram(100), sync_instrumentation=sync, lsm_diagnostics=diag)
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block='X4', cell=c,
                         triplet=k, arm=arm, spec=copy.deepcopy(s), runtime=dict(exe_sha256='a' * 64), result=r)
                write_json(root / 'trials' / f'{tid}_a1.json', t)
                events.extend([dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1),
                               dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0)])
    write_json(root / 'plan.json', plan)
    write_ledger(root, events)
    return plan, events


class PacedTests(unittest.TestCase):
    def setUp(self):
        # mkdir avoids Python 3.13's mode-0700 TemporaryDirectory ACL issue
        # in the Windows sandbox, while retaining an external, unique temp dir.
        self.root = Path(tempfile.gettempdir()) / ('selfdbms-paced-analysis-' + uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.events = fixture(self.root)

    def change(self, tid, change):
        path = self.root / 'trials' / f'{tid}_a1.json'
        t = a.load_json(path)
        change(t['result'])
        write_json(path, t)

    def test_trend_present_and_deterministic(self):
        out = a.analyze(self.root)
        self.assertTrue(all(v['rise_detected'] for v in out['H_P1'].values()))
        self.assertTrue(all(v['n'] == 10 for v in out['H_P1'].values()))
        self.assertEqual(out['counts']['successful'], 360)
        self.assertEqual(out['bootstrap_seed'], 20261106)
        self.assertTrue(all(v['matches'] for v in out['predictions']))
        self.assertEqual(out, a.analyze(self.root))
        json.dumps(out, allow_nan=False)
        self.assertIn('Saturated trial counts', a.tables(out))

    def test_no_trend(self):
        for s in self.plan['trials']:
            if s['arm'] == 'A':
                self.change(s['trial_id'], lambda r: r['paced_load']['write_response_nanos'].update(p99=100))
        out = a.analyze(self.root)
        self.assertTrue(all(not v['rise_detected'] for v in out['H_P1'].values()))

    def test_dropped_triplet(self):
        tid = self.plan['trials'][0]['trial_id']
        write_ledger(self.root, [e for e in self.events if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertEqual(len(out['dropped_triplets']), 1)
        self.assertEqual(out['cells'][a.CELLS[0]]['complete_triplets'], list(range(2, 11)))
        self.assertEqual(out['H_P1']['f20_closed']['rounds'], list(range(2, 11)))
        self.assertEqual(out['H_P1']['f20_open']['n'], 10)
        self.assertEqual(out['cells'][a.CELLS[0]]['arms']['A']['R']['n'], 9)
        self.assertEqual(len([t for t in out['trials'].values() if t['cell'] == a.CELLS[0]]), 29)
        self.assertTrue(out['partial'])

    def test_closed_has_no_read_response_metric(self):
        out = a.analyze(self.root)
        for c, cs in out['cells'].items():
            if c.endswith('_closed'):
                self.assertEqual(cs['ratios']['read_response_p99'], {})
                self.assertIsNone(cs['arms']['B']['read_response_p99']['mean'])
                self.assertEqual(cs['arms']['B']['read_response_p99']['not_applicable_n'], 10)
            else:
                self.assertEqual(cs['arms']['B']['read_response_p99']['mean'], 120)

    def test_saturation_and_censoring(self):
        out = a.analyze(self.root)
        for tid, v in out['trials'].items():
            saturated = '_f80_' in tid and 'd0000' not in tid and v['arm'] == 'A'
            self.assertEqual(v['saturated'], saturated)
            self.assertEqual(v['write_response_p99_censored_from_below'], saturated)
            self.assertEqual(v['write_response_p99_label'], 'censored_from_below' if saturated else 'observed')
        self.assertEqual(out['cells']['P1_d0200_f80_open']['saturated_trials']['A'], 10)
        summary = out['cells']['P1_d0200_f80_open']['arms']['A']['write_response_p99']
        self.assertEqual(summary['censored_n'], 10)
        self.assertFalse(summary['true_p99_summary'])
        self.assertFalse(out['H_P1']['f80_open']['true_p99_inference'])
        self.assertTrue(out['H_P1']['f20_open']['true_p99_inference'])
        # Exact boundary is not saturated; undefined p99 remains undefined.
        tid = self.plan['trials'][0]['trial_id']
        self.change(tid, lambda r: r['paced_load']['write_response_nanos'].update(p99=None))
        out = a.analyze(self.root)
        self.assertEqual(out['trials'][tid]['write_response_p99_label'], 'undefined')
        self.assertEqual(out['H_P1']['f20_closed']['n'], 9)

    def test_open_has_no_R_equivalence(self):
        out = a.analyze(self.root)
        self.assertEqual(len(out['H_P2']), 6)
        self.assertEqual(len(out['H_P3']), 6)
        self.assertEqual(len(out['H_P4']['W']), 12)
        self.assertEqual(len(out['H_P4']['R']), 6)
        for c, cs in out['cells'].items():
            if c.endswith('_open'):
                self.assertNotIn('R_AMIN/B', cs['equivalence'])
                self.assertNotIn(c, out['H_P4']['R'])

    def test_zero_writes_no_latency_pseudocount(self):
        tid = f'{a.CELLS[0]}_t01_A'
        def zero(r):
            w = r['transaction_ledger']['writes']
            w.update(attempted=0, completed_in_window=0, queued=w['offered'])
            r['operations'].update(write_ops_completed_in_window=0, write_ops_completed=0)
            r['paced_load']['write_ticks_unissued'] = w['offered']
            r['throughput']['write_ops_per_sec'] = 0
        self.change(tid, zero)
        out = a.analyze(self.root)
        self.assertEqual(out['cells'][a.CELLS[0]]['zero_adjusted_W_triplets'], [1])
        self.assertIsNone(out['trials'][tid]['write_response_p99'])
        self.assertTrue(out['trials'][tid]['zero_writes'])
        self.assertEqual(out['cells'][a.CELLS[0]]['ratios']['write_response_p99']['A/AMIN']['undefined_n'], 1)
        self.assertEqual(out['H_P2'][a.CELLS[0]]['n'], 10)

    def test_total_read_abandonment_is_data(self):
        tid = f'{a.CELLS[1]}_t01_A'
        def abandon(r):
            rl = r['transaction_ledger']['reads']
            rl.update(attempted=0, completed_in_window=0, abandoned=rl['offered'])
            r['operations']['read_ops_completed'] = 0
            r['throughput']['read_ops_per_sec'] = 0
            r['paced_load'].update(read_arrivals_abandoned=rl['offered'], max_read_lateness_ns=1000000001)
            r['paced_load']['read_response_nanos'].update(sample_count=0, p99=None)
            r['latency_read_nanos'].update(sample_count=0, p99=None)
            r['sync_instrumentation']['read_critical_section_nanos'] = {}
        self.change(tid, abandon)
        out = a.analyze(self.root)
        trial = out['trials'][tid]
        self.assertIsNone(trial['read_response_p99'])
        self.assertIsNone(trial['read_service_p50'])
        self.assertEqual(trial['abandoned_reads'], 1000)
        self.assertEqual(trial['R'], 0)
        self.assertEqual(out['counts']['failed'], 0)

    def test_unplanned_treatment_fails_closed(self):
        tid = self.plan['trials'][0]['trial_id']
        self.change(tid, lambda r: r['lsm_diagnostics'].update(measured_compactions=1))
        with self.assertRaisesRegex(ValueError, 'unplanned compaction'):
            a.analyze(self.root)

    def test_cli_deterministic(self):
        dest = self.root / 'output'
        args = [sys.executable, str(HERE / 'x_paced_analyze.py'), '--data-dir', str(self.root),
                '--output-dir', str(dest)]
        result = subprocess.run(args, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        original = (dest / 'x_paced_results.json').read_bytes()
        self.assertEqual(subprocess.run(args, capture_output=True).returncode, 0)
        self.assertEqual(original, (dest / 'x_paced_results.json').read_bytes())
        self.assertIn('H-P1', (dest / 'x_paced_tables.md').read_text(encoding='utf-8'))


if __name__ == '__main__':
    unittest.main()
