"""Synthetic pre-data verification only. No trial or smoke is launched."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import subprocess
import sys
import shutil
import uuid
import unittest

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('x_inputopen_analyze', HERE/'x_inputopen_analyze.py')
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


def write_json(path, value):
    path.write_text(json.dumps(value), encoding='utf-8')


def write_ledger(root, rows):
    (root/'ledger.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in rows), encoding='utf-8')


def accounting(n, late=0):
    return dict(offered=n+late, attempted=n+late, completed_in_window=n, completed_late=late,
                failed=0, abandoned=0, queued=0, outstanding_at_window_close=late)


def result(s):
    arm, path = s['arm'], s['cell'].rsplit('_', 1)[1]
    wait = 200 if path == 'default' and arm != 'B' else 100
    v = dict(count=3, mean_nanos=100, p50_nanos=80, p99_nanos=200, max_nanos=300)
    sites = ('compaction_snapshot_publish',) if arm == 'B' else ('compaction_start', 'compaction_publish')
    sync = dict(writer_lock_wait_nanos={site: copy.deepcopy(v) for site in sites},
                publication_nanos={site: copy.deepcopy(v) for site in sites},
                compaction_wall_nanos=copy.deepcopy(v), retirement_lag_nanos=dict(v, count=12),
                compaction_inputs=dict(count=3, mean=4, max=4), retired_tables_high_water=4)
    sync['writer_lock_wait_nanos']['commit'] = dict(count=1001, mean_nanos=wait, p50_nanos=wait/2,
                                                  p99_nanos=wait, max_nanos=wait*2)
    return dict(status='SUCCESS', trial_id=s['trial_id'], config=copy.deepcopy(s), git_revision='synthetic-revision',
                timestamps=dict(measured_window_ns=10000000000),
                operations=dict(read_ops_completed=1000, read_ops_completed_in_window=1000, read_ops_completed_late=0,
                                write_ops_completed=1001, write_ops_completed_in_window=1000, write_ops_completed_late=1,
                                validation_error_count=0, error_ops_count=0),
                throughput=dict(read_ops_per_sec=100, write_ops_per_sec=100),
                latency_read_nanos=dict(sample_count=1000, p99=10000),
                transaction_ledger=dict(reads=accounting(1000), writes=accounting(1000, 1)),
                lsm_diagnostics=dict(initial_sstables=4, final_sstables=1, measured_compactions=3,
                                     compactions_in_window=3, compaction_successes=3, compaction_no_ops=0,
                                     compaction_errors=0, measured_flushes=2), sync_instrumentation=sync)


def fixture(root):
    (root/'trials').mkdir()
    plan = dict(campaign_id='X-inputopen-v1', master_seed=a.SEED, settle_ms=20000,
                cells=[dict(id=c, spec=dict(a.nominal(c), cell=c)) for c in a.CELLS], trials=[])
    rows, clock = [], datetime(2026, 11, 10, tzinfo=timezone.utc)
    for k in range(1, 11):
        for entry in a.ENTRIES:
            for pos, (path, arm) in enumerate(a.RUNS):
                cell = entry+'_'+path
                tid = f'{cell}_t{k:02d}_{arm}'
                h = hashlib.sha256(f'{a.SEED}|{entry}|{k}'.encode()).digest()
                s = dict(a.nominal(cell), cell=cell, trial_id=tid, triplet=k, arm=arm,
                         seed=int.from_bytes(h[:8], 'big', signed=True), order_position=pos+1,
                         runlist_position=len(plan['trials'])+1)
                # Exercise the driver's absent false/default fields.
                for key in ('lock_policy', 'inline_flush_check', 'diag_trace', 'persist_read_histogram', 'compaction_open_outside_lock'):
                    if not s[key]:
                        del s[key]
                plan['trials'].append(s)
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block='X3', cell=cell,
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
    def test_smoke_directory_excluded_even_with_allow_partial(self):
        with self.assertRaisesRegex(ValueError, 'smoke directory'):
            a.load_campaign(Path('x-inputopen-v1-smoke'), True)

    def test_known_tail_margin_tost(self):
        logs = [math.log(1.2)+v for v in (-.04, -.03, -.02, -.01, 0, 0, .01, .02, .03, .04)]
        tail = a.tost(logs, math.log(1.5))
        se = float(a.base.np.std(logs, ddof=1))/math.sqrt(10)
        mean = sum(logs)/10
        expected = max(float(a.base.stats.t.sf((mean+math.log(1.5))/se, 9)),
                       float(a.base.stats.t.cdf((mean-math.log(1.5))/se, 9)))
        self.assertAlmostEqual(tail['p_tost'], expected, places=15)
        family = {str(i): copy.deepcopy(tail) for i in range(6)}
        a.adjust_family(family, 'tail', a.TAIL_MARGIN)
        self.assertTrue(all(v['classification'] == 'equivalent' for v in family.values()))
        narrow = a.tost(logs, a.THROUGHPUT_MARGIN)
        a.adjust_family({'one': narrow}, 'throughput', a.THROUGHPUT_MARGIN)
        self.assertEqual(narrow['classification'], 'not_equivalent')
        self.assertEqual(a.base.MARGIN, math.log(1/.90))

    def test_tost_boundaries_missing_and_holm(self):
        for logs, cls in (([0]*10, 'equivalent'), ([math.log(2)]*10, 'not_equivalent'),
                          ([a.TAIL_MARGIN]*10, 'inconclusive'), ([], 'inconclusive')):
            v = a.tost(logs, a.TAIL_MARGIN)
            a.adjust_family({'one':v}, 'tail', a.TAIL_MARGIN)
            self.assertEqual(v['classification'], cls)
        family = {str(i): dict(a.tost([0]*10, a.TAIL_MARGIN), p_tost=.01) for i in range(6)}
        family['5'] = a.tost([], a.TAIL_MARGIN)
        a.adjust_family(family, 'six with missing', a.TAIL_MARGIN)
        self.assertAlmostEqual(family['0']['holm_p'], .06)
        self.assertIsNone(family['5']['holm_p'])

    def test_zero_W_applies_to_complete_five_run_unit(self):
        keys = a.RUNS
        g = {1: {key:str(i) for i, key in enumerate(keys)}}
        records = {str(i):dict(W=0 if i == 0 else 10+i) for i in range(5)}
        v = a.ratio(g, records, keys[2], keys[1], 'W')
        self.assertAlmostEqual(v['geometric_mean_ratio'], 12.1/11.1)
        self.assertEqual(v['zero_adjusted_W_rounds'], [1])


class CampaignTests(unittest.TestCase):
    def setUp(self):
        self.root = HERE/('.inputopen-test-'+uuid.uuid4().hex)
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.rows = fixture(self.root)

    def artifact(self, entry=None, path='aligned', arm='AMIN', k=1):
        entry = entry or a.ENTRIES[0]
        return self.root/'trials'/f'{entry}_{path}_t{k:02d}_{arm}_a1.json'

    def test_all_predictions_descriptives_and_determinism(self):
        out = a.analyze(self.root)
        self.assertEqual(out, a.analyze(self.root))
        self.assertEqual(out['counts']['successful'], 300)
        self.assertEqual(out['bootstrap_seed'], 20261110)
        for h in ('H_O1', 'H_O2', 'H_O3', 'H_O4'):
            self.assertEqual(out[h]['verdict'], 'supported')
        for cs in out['cells'].values():
            self.assertEqual(cs['complete_rounds'], list(range(1, 11)))
            self.assertEqual(len(cs['runs']), 5)
            self.assertEqual(cs['runs']['aligned/AMIN']['commit_wait_p99']['mean'], 100)
            self.assertEqual(cs['runs']['default/B']['retired_tables_high_water']['mean'], 4)
            self.assertEqual(cs['runs']['default/B']['compaction_start_hold_mean_nanos']['undefined_n'], 10)
            self.assertEqual(cs['runs']['aligned/AMIN']['commit_wait_max']['mean'], 200)
            self.assertEqual(cs['runs']['aligned/AMIN']['compaction_start_hold_p99_nanos']['mean'], 200)
        self.assertAlmostEqual(out['lease_default_relative_snapshot'][a.ENTRIES[0]]['geometric_mean_ratio'], 2)
        self.assertEqual(out['across_campaign_reference']['lease_snapshot_commit_wait_p99_range'], [1.43, 7.69])
        self.assertIn('retirement_lag', a.tables(out))
        self.assertEqual(out['gaps']['min_ms'], 20000)
        json.dumps(out, allow_nan=False)

    def test_every_prediction_can_fail(self):
        # One setting suffices to defeat all-settings predictions.
        for k in range(1, 11):
            path = self.artifact(k=k)
            t = a.load_json(path)
            r = t['result']
            r['sync_instrumentation']['writer_lock_wait_nanos']['commit']['p99_nanos'] = 400
            r['operations'].update(read_ops_completed=500, read_ops_completed_in_window=500,
                                   write_ops_completed=501, write_ops_completed_in_window=500)
            r['throughput'].update(read_ops_per_sec=50, write_ops_per_sec=50)
            r['transaction_ledger'] = dict(reads=accounting(500), writes=accounting(500, 1))
            r['latency_read_nanos']['sample_count'] = 500
            write_json(path, t)
        out = a.analyze(self.root)
        for h in ('H_O1', 'H_O2', 'H_O3', 'H_O4'):
            self.assertEqual(out[h]['verdict'], 'not_supported')

    def test_primary_supported_but_gap_not_closed(self):
        for k in range(1, 11):
            for path, value in (('aligned', 200), ('default', 400)):
                p = self.artifact(path=path, k=k)
                t = a.load_json(p)
                t['result']['sync_instrumentation']['writer_lock_wait_nanos']['commit']['p99_nanos'] = value
                write_json(p, t)
        out = a.analyze(self.root)
        self.assertEqual(out['H_O1']['verdict'], 'supported')
        self.assertEqual(out['H_O2']['verdict'], 'not_supported')
        self.assertIn('part of the gap and not all', out['interpretation'])

    def test_partial_drops_entire_unit(self):
        tid = self.plan['trials'][0]['trial_id']
        write_ledger(self.root, [e for e in self.rows if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        out = a.analyze(self.root, True)
        self.assertTrue(out['partial'])
        self.assertEqual(len(out['dropped_units']), 1)
        self.assertEqual(out['cells'][a.ENTRIES[0]]['complete_rounds'], list(range(2, 11)))
        self.assertEqual(out['H_O1']['verdict'], 'inconclusive')
        self.assertIn(self.plan['trials'][1]['trial_id'], out['trials'])

    def test_retry_then_exhausted_failure_and_latest_attempt(self):
        tid = self.plan['trials'][0]['trial_id']
        for e in self.rows:
            if e['trial_id'] == tid and e['event'] == 'FINISHED':
                e.update(status='FAILED', exit_code=1)
        p = self.root/'trials'/f'{tid}_a1.json'
        t = a.load_json(p)
        t['status'] = t['result']['status'] = 'FAILED'
        t['result']['error'] = 'synthetic open fault'
        t['result']['lsm_diagnostics']['compaction_errors'] = 1
        write_json(p, t)
        write_ledger(self.root, self.rows)
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.root)
        partial = a.analyze(self.root, True)
        self.assertEqual(partial['pending_retry'], [tid])
        retry = [dict(self.rows[0], attempt=2), dict(self.rows[1], attempt=2)]
        retry[0]['start_utc'] = (datetime.fromisoformat(self.rows[-1]['end_utc'])+timedelta(seconds=20)).isoformat()
        retry[1]['end_utc'] = (datetime.fromisoformat(retry[0]['start_utc'])+timedelta(seconds=12.5)).isoformat()
        write_json(self.root/'trials'/f'{tid}_a2.json', t)
        write_ledger(self.root, self.rows+retry)
        out = a.analyze(self.root)
        self.assertFalse(out['partial'])
        self.assertEqual(out['failed_trials'], [tid])
        self.assertEqual(len(out['dropped_units']), 1)
        self.assertEqual(out['failed_attempts_detail'][0]['compaction_errors'], 1)
        # Unfinished latest attempt must supersede a prior artifact.
        write_ledger(self.root, self.rows+retry[:1])
        self.assertNotIn(tid, a.analyze(self.root, True)['trials'])
        write_ledger(self.root, self.rows+[dict(retry[0], attempt=3)])
        with self.assertRaisesRegex(ValueError, 'retry'):
            a.analyze(self.root, True)

    def test_undefined_zero_latency_and_zero_throughput(self):
        p = self.artifact()
        t = a.load_json(p)
        r = t['result']
        r['sync_instrumentation']['writer_lock_wait_nanos']['commit'] = dict(count=0, p50_nanos=0, p99_nanos=0, max_nanos=0)
        r['operations'].update(write_ops_completed=0, write_ops_completed_in_window=0, write_ops_completed_late=0)
        r['throughput']['write_ops_per_sec'] = 0
        r['transaction_ledger']['writes'] = accounting(0)
        write_json(p, t)
        out = a.analyze(self.root)
        run = out['cells'][a.ENTRIES[0]]['runs']['aligned/AMIN']
        self.assertEqual(run['W']['values'][0], 0)
        self.assertEqual(run['commit_wait_p99']['undefined_n'], 1)
        self.assertEqual(out['H_O1']['settings'][a.ENTRIES[0]]['excluded_rounds'], [1])
        self.assertEqual(out['H_O3']['W']['settings'][a.ENTRIES[0]]['zero_adjusted_W_rounds'], [1])

    def test_fail_closed_plan_treatment_seed_and_provenance(self):
        for mutate, message in (
            (lambda p: p.update(settle_ms=0), 'settle_ms'),
            (lambda p: p['trials'][0].update(compaction_open_outside_lock=True), 'compaction_open_outside_lock'),
            (lambda p: p['trials'][0].update(seed=0), 'seed'),
            (lambda p: p['trials'][2].update(arm='B'), 'B belongs'),
            (lambda p: p['trials'][0].update(lock_policy='readerpref'), 'lock_policy')):
            bad = copy.deepcopy(self.plan); mutate(bad)
            with self.assertRaisesRegex(ValueError, message):
                a.validate_plan(bad)
        p = self.artifact(); t = a.load_json(p)
        t['result']['lsm_diagnostics']['compaction_errors'] = 1
        write_json(p, t)
        with self.assertRaisesRegex(ValueError, 'compaction errors'):
            a.analyze(self.root)
        t['result']['lsm_diagnostics']['compaction_errors'] = 0
        t['runtime']['exe_sha256'] = 'b'*64
        write_json(p, t)
        with self.assertRaisesRegex(ValueError, 'mixed runners'):
            a.analyze(self.root)

    def test_cli_deterministic_figure_and_partial(self):
        dest = self.root/'output'
        cmd = [sys.executable, str(HERE/'x_inputopen_analyze.py'), '--data-dir', str(self.root), '--output-dir', str(dest)]
        run = subprocess.run(cmd, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        first = {p.name:p.read_bytes() for p in dest.iterdir()}
        self.assertEqual(set(first), {'x_inputopen_results.json', 'x_inputopen_tables.md', 'x_inputopen_commit_wait_p99.png'})
        run = subprocess.run(cmd, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(first, {p.name:p.read_bytes() for p in dest.iterdir()})
        tid = self.plan['trials'][0]['trial_id']
        write_ledger(self.root, [r for r in self.rows if r['trial_id'] != tid])
        self.assertNotEqual(subprocess.run(cmd, capture_output=True).returncode, 0)
        run = subprocess.run(cmd+['--allow-partial'], capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertTrue(a.load_json(dest/'x_inputopen_results.json')['partial'])
        self.assertIn('PARTIAL DATA', (dest/'x_inputopen_tables.md').read_text(encoding='utf-8'))


if __name__ == '__main__':
    unittest.main()
