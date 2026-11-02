"""Synthetic pre-data checks; no benchmark runner is invoked."""
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
_spec = importlib.util.spec_from_file_location('x_intervention_analyze', HERE / 'x_intervention_analyze.py')
a = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(a)


def write_json(path, value):
    path.write_text(json.dumps(value), encoding='utf-8')


def fixture(root):
    (root / 'trials').mkdir()
    plan = dict(campaign_id='X-intervention-v1', master_seed=20261103,
                cells=[dict(id=c, spec=a.nominal(c)) for c in a.CELLS], trials=[])
    events = []
    for cell in a.CELLS:
        r = a.nominal(cell)['reader_concurrency']
        for k in range(1, 6):
            seed = int.from_bytes(hashlib.sha256(f'20261103|{cell}|{k}'.encode()).digest()[:8], 'big', signed=True)
            for condition, arm in a.RUNS:
                tid = f'{cell}_{condition}_t{k:02d}_{arm}'
                s = dict(a.nominal(cell), trial_id=tid, cell=f'{cell}_{condition}', triplet=k,
                         arm=arm, seed=seed, inline_flush_check=condition == 'inline')
                plan['trials'].append(s)
                factor = k + 9
                commits = (10000 if arm == 'AMIN' else 5000 if condition == 'second' else 6660) * factor
                reads = (2 if condition == 'second' else 1) * r * commits if arm == 'A' else r * 5000 * factor
                wait = 500000 / factor
                hist = dict(mean_nanos=wait, p50_nanos=wait, p99_nanos=wait, count=commits,
                            max_nanos=wait)
                result = dict(status='SUCCESS', trial_id=tid, git_revision='synthetic-revision', config=copy.deepcopy(s),
                    operations=dict(read_ops_completed_in_window=reads, write_ops_completed_in_window=commits,
                                    read_ops_completed=reads + 10, validation_error_count=0),
                    timestamps=dict(measured_window_ns=10000000000),
                    throughput=dict(read_ops_per_sec=reads / 10, write_ops_per_sec=commits / 10),
                    latency_read_nanos=dict(p50=100, p95=100, p99=100, mean=100),
                    sync_instrumentation=dict(read_critical_section_nanos={arm: hist}, writer_lock_wait_nanos=dict(commit=hist)),
                    lsm_diagnostics=dict(publication_events_realized_in_window=0,
                                         publication_events_realized_late=0, publication_events_target=0))
                envelope = dict(schema='i7.trial.v1', trial_id=tid, block='X1', cell=s['cell'], triplet=k, arm=arm,
                                spec=copy.deepcopy(s), runtime=dict(exe_sha256='a' * 64), status='SUCCESS', result=result)
                write_json(root / 'trials' / f'{tid}_a1.json', envelope)
                events.extend([dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1),
                               dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0)])
    write_json(root / 'plan.json', plan)
    ledger(root, events)
    return plan, events


def ledger(root, events):
    (root / 'ledger.jsonl').write_text(''.join(json.dumps(e) + '\n' for e in events), encoding='utf-8')


class InterventionTests(unittest.TestCase):
    def setUp(self):
        # Inherit workspace permissions on Windows, as in the readcost tests.
        scratch = HERE.parents[2] / '_work'
        scratch = scratch.resolve()
        scratch.mkdir(exist_ok=True)
        self.root = scratch / ('intervention_test_' + uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        self.data = self.root / 'data'
        self.data.mkdir()
        self.plan, self.events = fixture(self.data)
        self.tid = 'I1_r04_d0000_second_t01_A'

    def artifact(self, tid=None):
        return self.data / 'trials' / f'{tid or self.tid}_a1.json'

    def test_all_predictions_hold_and_deterministic_cli(self):
        out = a.analyze(self.data)
        self.assertEqual([out[p]['verdict'] for p in ('P1', 'P2', 'P3')], ['supported'] * 3)
        self.assertEqual(out['counts']['planned'], 160)
        cs = out['cells']['I1_r04_d0000']
        self.assertEqual(cs['conditions']['second']['arms']['A']['q']['mean'], 2)
        self.assertEqual(cs['conditions']['inline']['arms']['A']['q']['mean'], 1)
        self.assertAlmostEqual(cs['P2']['ratio_of_read_ratios']['geometric_mean_ratio'], .666)
        self.assertAlmostEqual(cs['ratios_inline_over_second']['A']['W']['geometric_mean_ratio'], 1.332)
        self.assertEqual(out['trials'][self.tid]['reads'], 400000)
        self.assertEqual(out['trials'][self.tid]['commits'], 50000)
        self.assertLess(abs(cs['conditions']['inline']['cycle']['relative_error']), .002)
        output, tex = self.root / 'output', self.root / 'rows.tex'
        cmd = [sys.executable, str(HERE / 'x_intervention_analyze.py'), '--data-dir', str(self.data),
               '--output-dir', str(output), '--tex-out', str(tex)]
        subprocess.run(cmd, check=True, capture_output=True)
        paths = [output / 'x_intervention_results.json', output / 'x_intervention_tables.md', tex]
        first = [p.read_bytes() for p in paths]
        subprocess.run(cmd, check=True, capture_output=True)
        self.assertEqual(first, [p.read_bytes() for p in paths])
        text = tex.read_text()
        self.assertTrue(text.startswith('\\gdef\\TblInterventionRows{'))
        self.assertEqual(text.count(' \\\\'), 8)

    def test_p1_fails_one_cell_only(self):
        for k in range(1, 6):
            path = self.artifact(f'I1_r04_d0000_second_t{k:02d}_A')
            t = a.load_json(path)
            t['result']['operations']['read_ops_completed_in_window'] *= 2
            t['result']['operations']['read_ops_completed'] = t['result']['operations']['read_ops_completed_in_window']
            t['result']['throughput']['read_ops_per_sec'] *= 2
            write_json(path, t)
        out = a.analyze(self.data)
        self.assertEqual(out['P1']['verdict'], 'not_supported')
        self.assertEqual(out['P1']['supported_cells'], 15)

    def test_latest_failed_attempt_drops_whole_four_run_unit(self):
        # A stale successful first attempt must not override the latest ledger failure.
        self.events += [dict(event='STARTED', trial_id=self.tid, attempt=2, status='STARTED', exit_code=-1),
                        dict(event='FINISHED', trial_id=self.tid, attempt=2, status='TIMEOUT', exit_code=-1)]
        ledger(self.data, self.events)
        out = a.analyze(self.data)
        self.assertEqual(out['counts']['failed'], 1)
        self.assertEqual(out['counts']['dropped_units'], 1)
        self.assertEqual(out['failed_attempts'][0]['status'], 'TIMEOUT')
        self.assertEqual(out['dropped_units'][0]['round'], 1)
        cs = out['cells']['I1_r04_d0000']
        self.assertEqual(cs['complete_rounds'], [2, 3, 4, 5])
        for cv in cs['conditions'].values():
            for arm in a.ARMS:
                self.assertEqual(cv['arms'][arm]['q']['n'], 4)

    def test_successful_retry_uses_latest_and_lists_failed_attempt(self):
        original = a.load_json(self.artifact())
        write_json(self.data / 'trials' / f'{self.tid}_a2.json', original)
        for event in self.events:
            if event['trial_id'] == self.tid and event['event'] == 'FINISHED':
                event.update(status='FAILED', exit_code=1)
        self.events += [dict(event='STARTED', trial_id=self.tid, attempt=2, status='STARTED', exit_code=-1),
                        dict(event='FINISHED', trial_id=self.tid, attempt=2, status='SUCCESS', exit_code=0)]
        # Failed envelope may contain plausible values; the ledger overrides it.
        stale = copy.deepcopy(original)
        stale['result']['operations']['read_ops_completed_in_window'] *= 10
        write_json(self.artifact(), stale)
        ledger(self.data, self.events)
        out = a.analyze(self.data)
        self.assertEqual(out['counts']['dropped_units'], 0)
        self.assertEqual(out['counts']['retried'], 1)
        self.assertEqual(out['failed_attempts'][0]['envelope_status'], 'SUCCESS')
        self.assertEqual(out['P1']['verdict'], 'supported')

    def test_missing_requires_allow_partial(self):
        ledger(self.data, [e for e in self.events if e['trial_id'] != self.tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.data)
        out = a.analyze(self.data, True)
        self.assertTrue(out['partial'])
        self.assertEqual(out['P1']['verdict'], 'inconclusive')
        self.assertEqual(out['counts']['dropped_units'], 1)

    def test_corrupt_success_envelope_or_flag_fails_closed(self):
        path = self.artifact()
        original = a.load_json(path)
        t = copy.deepcopy(original)
        t['trial_id'] = 'wrong'
        write_json(path, t)
        with self.assertRaisesRegex(ValueError, 'mismatch'):
            a.analyze(self.data)
        t = copy.deepcopy(original)
        t['result']['config']['inline_flush_check'] = True
        write_json(path, t)
        with self.assertRaisesRegex(ValueError, 'inline_flush_check'):
            a.analyze(self.data)

    def test_zero_commits_are_data_and_not_a_trial_failure(self):
        path = self.artifact()
        t = a.load_json(path)
        t['result']['operations']['write_ops_completed_in_window'] = 0
        write_json(path, t)
        out = a.analyze(self.data)
        self.assertEqual(out['counts']['failed'], 0)
        self.assertEqual(out['counts']['dropped_units'], 0)
        self.assertIsNone(out['trials'][self.tid]['q'])
        self.assertEqual(out['P1']['verdict'], 'inconclusive')
        self.assertEqual(out['P3']['verdict'], 'inconclusive')

    def test_ci_matches_explicit_t_interval(self):
        logs = [.1, .2, .3, .4, .5]
        got = a.paired_stats(logs)
        half = float(a.base.stats.t.ppf(.975, 4)) * (.025 / 5) ** .5
        self.assertAlmostEqual(got['t_ci_log'][0], .3 - half)
        self.assertAlmostEqual(got['t_ci_log'][1], .3 + half)


if __name__ == '__main__':
    unittest.main()
