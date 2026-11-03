"""Synthetic X-settled checks; no engine or campaign runner is invoked."""
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import uuid

HERE = Path(__file__).resolve().parent


def module(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name+'.py'))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


a = module('x_settled_analyze')


def write_json(path, value):
    path.write_text(json.dumps(value), encoding='utf-8')


def write_ledger(root, rows):
    (root / 'ledger.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in rows), encoding='utf-8')


def histogram(value, count=1000):
    return dict(sample_count=count, min=value, max=value, mean=value, p50=value,
                p95=value if count >= 20 else None, p99=value if count >= 100 else None, p999=None)


def result(spec):
    c, arm = spec['cell'], spec['arm']
    count = 1000 if spec['writer_concurrency'] else 0
    offered = 0
    if spec['block'] == 'X4':
        period = int(1e9 / spec['paced_write_rate_per_sec'])
        offered = (10000000000+period-1)//period
        count = int(offered*.7) if c.startswith('S2_') and arm == 'A' else offered
    reads = 10000
    stat = dict(mean_nanos=100, p50_nanos=100, p99_nanos=100, count=1000, max_nanos=100)
    wait = dict(commit=stat) if count else {}
    pubs = 20 if spec['block'] == 'X2' else 0
    if pubs:
        wait['flush_publish'] = stat
    r = dict(status='SUCCESS', trial_id=spec['trial_id'], git_revision='synthetic-revision', config=copy.deepcopy(spec),
             operations=dict(read_ops_completed=reads, read_ops_completed_in_window=reads,
                             write_ops_completed=count, write_ops_completed_in_window=count,
                             write_ops_completed_late=0, validation_error_count=0, error_ops_count=0),
             timestamps=dict(measured_window_ns=10000000000),
             throughput=dict(read_ops_per_sec=reads/10, write_ops_per_sec=count/10),
             latency_read_nanos=histogram(100, reads),
             sync_instrumentation=dict(read_critical_section_nanos={arm: stat}, writer_lock_wait_nanos=wait),
             lsm_diagnostics=dict(initial_sstables=spec['initial_sstable_count'], final_sstables=spec['initial_sstable_count'],
                                  measured_compactions=0, measured_flushes=0, compaction_errors=0,
                                  publication_events_realized_in_window=pubs, publication_events_realized_late=0,
                                  publication_events_target=pubs))
    if spec['block'] != 'X4':
        return r
    def accounting(n, done):
        return dict(offered=n, attempted=done, completed_in_window=done, completed_late=0,
                    failed=0, abandoned=0, queued=n-done, outstanding_at_window_close=0)
    r['transaction_ledger'] = dict(reads=accounting(reads, reads), writes=accounting(offered, count))
    factor = {0: 1, 200: 2, 2000: 4}[spec['read_io_delay_us']] if arm == 'A' else 1
    p = dict(write_ticks_offered=offered, write_ticks_unissued=offered-count, read_arrivals_abandoned=0,
             max_read_lateness_ns=100000, write_service_nanos=histogram(1000000, count),
             write_response_nanos=histogram(1000000*factor, count),
             write_service_late_nanos=histogram(0, 0), write_response_late_nanos=histogram(0, 0))
    p['write_response_nanos'].update(mean=1200000*factor, p95=1500000*factor, p99=2000000*factor)
    if spec.get('open_loop_rate_per_sec', 0):
        p.update(read_response_nanos=histogram(150, reads), read_response_late_nanos=histogram(0, 0))
    r['paced_load'] = p
    phases = {phase: dict(stats=histogram(2000000 if arm == 'A' else 200000, count), buckets=[])
              for phase in a.PHASES}
    phases['wal_sync']['stats']['max'] = 30000000
    r['stall_diagnostics'] = dict(window_ns=10000000000, commit_count=count, phase_histograms=phases,
        commits=[dict(WalWriteDoneNs=1000, WalSyncDoneNs=15001000), dict(WalWriteDoneNs=2000, WalSyncDoneNs=30002000)],
        commits_dropped=0, heartbeat_dropped=0, heartbeat_gaps=[dict(start_ns=0, length_ns=12000000)], slowest_reads=[],
        runtime=dict(gc_count_delta=1, gc_pause_total_delta_ns=10000, gc_pauses=[dict(start_ns=0, length_ns=10000)],
                     gc_history_lost=False, sched_latencies_seconds=[], sched_pauses_total_gc_seconds=[], snapshot_end_ns=10000000000))
    return r


def fixture(root):
    (root / 'trials').mkdir()
    def nominal(c):
        return {k: v for k, v in a.nominal(c).items()
                if not ((k in a.OPTIONAL_ZERO and v == 0) or
                        (k in ('diag_trace', 'inline_flush_check') and v is False))}
    plan = dict(campaign_id='X-settled-v1', master_seed=a.SEED, settle_ms=20000,
                cells=[dict(id=c, spec=dict(nominal(c), cell=c)) for c in a.CELLS], trials=[])
    rows, clock = [], datetime(2026, 11, 7, tzinfo=timezone.utc)
    for k in range(1, 11):
        for i, entry in enumerate(a.ENTRIES[:16 if k <= 5 else 15]):
            order = a.run_keys(entry) if entry == a.INTER else a.PERMUTATIONS[(k+i)%6]
            for pos, key in enumerate(order):
                condition, arm = key if entry == a.INTER else (None, key)
                c = entry+'_'+condition if condition else entry
                tid = f'{c}_t{k:02d}_{arm}'
                seed = hashlib.sha256(f'{a.SEED}|{entry}|{k}'.encode()).digest()
                spec = dict(nominal(c), schema='i7.trial-spec.v1', cell=c, trial_id=tid, triplet=k, arm=arm,
                            seed=int.from_bytes(seed[:8], 'big', signed=True), order_position=pos+1,
                            runlist_position=len(plan['trials'])+1)
                # Non-optional zero fields in the Go spec are serialized.
                for field in ('compaction_trigger_interval_ms', 'read_io_delay_us'):
                    spec.setdefault(field, 0)
                plan['cells'][a.CELLS.index(c)]['spec'].setdefault('compaction_trigger_interval_ms', spec['compaction_trigger_interval_ms'])
                plan['cells'][a.CELLS.index(c)]['spec'].setdefault('read_io_delay_us', spec['read_io_delay_us'])
                plan['trials'].append(spec)
                t = dict(schema='i7.trial.v1', status='SUCCESS', trial_id=tid, block=spec['block'], cell=c,
                         triplet=k, arm=arm, spec=copy.deepcopy(spec), runtime=dict(exe_sha256='a'*64), result=result(spec))
                write_json(root / 'trials' / f'{tid}_a1.json', t)
                clock += timedelta(seconds=20)
                rows.append(dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1, start_utc=clock.isoformat()))
                clock += timedelta(seconds=12.5)
                rows.append(dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0, end_utc=clock.isoformat()))
    write_json(root / 'plan.json', plan)
    write_ledger(root, rows)
    return plan, rows


def original_fixture(root):
    directories = {}
    for name, test_name in [('readcost', 'readcost'), ('compaction', 'compaction'), ('tablecount', 'tablecount'),
                             ('intervention', 'intervention'), ('paced', 'paced')]:
        directory = root / name
        directory.mkdir()
        helper = module('test_x_'+test_name+'_analyze')
        if name == 'readcost':
            helper.fixture(directory, n=10)
        else:
            helper.fixture(directory)
        analysis = helper.a.analyze(directory)
        _, published_dir, filename = a.SOURCES[name]
        dest = root / published_dir
        dest.mkdir()
        write_json(dest / filename, analysis)
        directories[name] = directory
    return directories


class NumericTests(unittest.TestCase):
    def test_classification_boundaries(self):
        m = a.base.MARGIN
        self.assertEqual(a.ci_class([-m/2, m/2]), 'equivalent')
        for ci in ([-m, m/2], [-m/2, m], [-m*2, -m], [m, 2*m], [-2*m, 2*m]):
            self.assertEqual(a.ci_class(ci), 'inconclusive')
        self.assertEqual(a.ci_class([m+.01, m+.02]), 'not_equivalent')
        self.assertEqual(a.ci_class([-m-.02, -m-.01]), 'not_equivalent')
        self.assertEqual(a.welch([0]*10, [0]*10, m)['classification'], 'unchanged')
        self.assertEqual(a.welch([2*m]*10, [0]*10, m)['classification'], 'changed')
        self.assertEqual(a.welch([-1, 1]*5, [0]*10, m)['classification'], 'inconclusive')

    def test_welch_satterthwaite_student_t(self):
        x, y = [.1, .2, .8, .5], [-.1, .02, .1]
        got = a.welch(x, y, a.base.MARGIN)
        vx, vy = a.base.np.var(x, ddof=1)/len(x), a.base.np.var(y, ddof=1)/len(y)
        df = (vx+vy)**2/(vx*vx/(len(x)-1)+vy*vy/(len(y)-1))
        self.assertAlmostEqual(got['df'], df)
        half = a.base.stats.t.ppf(.95, df)*math.sqrt(vx+vy)
        self.assertAlmostEqual(got['t_ci_log'][1]-got['mean_difference_log'], half)

    def test_mechanism_band(self):
        for value in (1000000, 2000000, 3000000):
            self.assertTrue(a.mechanism_band([value]*10, 'A')['supported'])
        self.assertFalse(a.mechanism_band([999999]*10, 'A')['supported'])
        self.assertFalse(a.mechanism_band([3000001]*10, 'A')['supported'])
        self.assertTrue(a.mechanism_band([499999]*10, 'AMIN')['supported'])
        self.assertFalse(a.mechanism_band([500000]*10, 'AMIN')['supported'])
        self.assertIsNone(a.mechanism_band([2000000, None], 'A')['supported'])

    def test_page_ties(self):
        rows = [[1, 1, 2]]*10
        got = a.paced.page_test(rows)
        expected = a.base.stats.page_trend_test(rows, predicted_ranks=[1, 2, 3], method='auto')
        self.assertEqual(got['mean_ranks'], [1.5, 1.5, 3])
        self.assertAlmostEqual(got['p'], expected.pvalue)

    def test_contradiction_trigger_has_no_holm_and_requires_published_class(self):
        m = a.base.MARGIN
        self.assertTrue(a.contradicted_class('equivalent', [2*m, 3*m]))
        self.assertTrue(a.contradicted_class('not_equivalent', [-m/2, m/2]))
        for published in (None, 'inconclusive'):
            self.assertFalse(a.contradicted_class(published, [2*m, 3*m]))
            self.assertFalse(a.contradicted_class(published, [-m/2, m/2]))
        self.assertFalse(a.contradicted_class('not_equivalent', [-m, m]))
        self.assertFalse(a.contradicted_class('equivalent', [m, 3*m]))


class SettledTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.scratch = Path(tempfile.gettempdir()) / ('selfdbms-settled-tests-'+uuid.uuid4().hex)
        cls.scratch.mkdir()
        cls.addClassCleanup(shutil.rmtree, cls.scratch)
        cls.dirs = original_fixture(cls.scratch)
        cls.originals = a.load_originals(cls.dirs)

    def setUp(self):
        self.root = self.scratch / uuid.uuid4().hex
        self.root.mkdir()
        self.addCleanup(shutil.rmtree, self.root)
        self.plan, self.rows = fixture(self.root)

    def analyze(self, partial=False):
        return a.analyze(self.root, allow_partial=partial, originals=self.originals)

    def change(self, tid, change):
        path = self.root / 'trials' / f'{tid}_a1.json'
        t = a.load_json(path)
        change(t['result'])
        write_json(path, t)

    def test_predictions_and_determinism(self):
        out = self.analyze()
        self.assertTrue(all(v['supported'] for v in out['H_M1'].values()))
        self.assertTrue(all(v['classification'] == 'not_equivalent' and v['supported'] for v in out['H_M2'].values()))
        self.assertEqual(out['H_M3']['verdict'], 'supported')
        self.assertTrue(all(v['classification'] == 'not_equivalent' and v['supported'] for v in out['H_W'].values()))
        self.assertTrue(all(v['classification'] == 'equivalent' for v in out['H_R'].values()))
        self.assertEqual(out['H_S']['verdict'], 'supported')
        self.assertEqual(out['cells']['S2_d0200_w700_open']['saturated_trials']['A'], 10)
        self.assertEqual(out['cells']['S2_d0200_w700_open']['saturated_successful_trials']['A'], 10)
        trial = out['trials']['S1_d0000_f20_open_t01_A']
        self.assertEqual(trial['fsync_count_ge_10ms'], 2)
        self.assertEqual(trial['fsync_count_ge_25ms'], 1)
        self.assertAlmostEqual(trial['fsync_share_ge_10ms'], .0045)
        self.assertEqual({v['comparison']['classification'] for v in out['H_C']}, {'unchanged', 'changed'})
        self.assertTrue(out['rerun_triggers'])
        self.assertEqual(out['counts']['successful'], 470)
        self.assertEqual(out['gaps']['min_ms'], 20000)
        self.assertEqual(out['gaps']['first_attempt_below_settle'], [])
        self.assertTrue(all(v['status'] in ('PASS', 'no_published_ratio') for v in out['reproduction_checks']))
        self.assertEqual(out, self.analyze())
        json.dumps(out, allow_nan=False)
        self.assertIn('H-C: sentinels', a.tables(out))

    def test_inconclusive_and_dropped_four_run_unit(self):
        for s in self.plan['trials']:
            if s['cell'] in ('S3_d0000_f80_closed', 'R_X3_r16') and s['arm'] == 'AMIN':
                self.change(s['trial_id'], lambda r, k=s['triplet']: r['throughput'].update(read_ops_per_sec=1000*math.exp((-1)**k)))
        out = self.analyze()
        self.assertEqual(out['H_R']['R']['classification'], 'inconclusive')
        sentinel = next(v for v in out['H_C'] if v['cell'] == 'R_X3_r16' and v['metric'] == 'R')
        self.assertEqual(sentinel['comparison']['classification'], 'inconclusive')
        self.assertFalse(sentinel['rerun_trigger'])
        tid = a.INTER+'_inline_t01_A'
        write_ledger(self.root, [e for e in self.rows if e['trial_id'] != tid])
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            self.analyze()
        out = self.analyze(True)
        self.assertEqual(len(out['dropped_units']), 1)
        for condition in ('second', 'inline'):
            self.assertEqual(out['cells'][a.INTER+'_'+condition]['complete_rounds'], [2, 3, 4, 5])
        self.assertEqual(len([v for v in out['trials'].values() if v['cell'].startswith(a.INTER)]), 19)

    def test_gap_flag_and_plan_validation(self):
        starts = [e for e in self.rows if e['event'] == 'STARTED']
        starts[1]['start_utc'] = self.rows[1]['end_utc']
        write_ledger(self.root, self.rows)
        out = self.analyze()
        self.assertEqual(out['gaps']['min_ms'], 0)
        self.assertEqual(len(out['gaps']['first_attempt_below_settle']), 1)
        plan = copy.deepcopy(self.plan); plan['settle_ms'] = 0
        with self.assertRaisesRegex(ValueError, 'settle_ms'):
            a.validate_plan(plan)
        plan = copy.deepcopy(self.plan); plan['trials'][0]['diag_trace'] = False
        with self.assertRaisesRegex(ValueError, 'diag_trace'):
            a.validate_plan(plan)

    def test_whole_window_phases_and_trace_loss(self):
        tid = 'S1_d2000_f20_closed_t01_A'
        self.change(tid, lambda r: r['stall_diagnostics'].update(commits_dropped=1))
        out = self.analyze()
        v = out['trials'][tid]
        self.assertEqual(v['phase_lock_wait_p50'], 2000000)
        self.assertEqual(v['largest_fsync_ns'], 30000000)
        self.assertIsNone(v['fsync_count_ge_10ms'])
        self.assertIsNone(v['fsync_share_ge_25ms'])
        self.assertEqual(v['heartbeat_count_ge_10ms'], 1)
        self.assertEqual(v['gc_count'], 1)

    def test_latest_failed_attempt_overrides_stale_success(self):
        tid = self.plan['trials'][0]['trial_id']
        self.rows.extend([dict(event='STARTED', trial_id=tid, attempt=2, status='STARTED', exit_code=-1,
                               start_utc='2026-11-08T00:00:00+00:00'),
                          dict(event='FINISHED', trial_id=tid, attempt=2, status='FAILED', exit_code=1,
                               end_utc='2026-11-08T00:00:01+00:00')])
        write_ledger(self.root, self.rows)
        out = self.analyze()
        self.assertNotIn(tid, out['trials'])
        self.assertEqual(out['failed_trials'], [tid])
        self.assertEqual(out['cells'][a.PACED[0]]['complete_rounds'], list(range(2, 11)))
        self.assertEqual(out['H_M1']['closed']['n'], 9)

    def test_cli(self):
        args = [sys.executable, str(HERE/'x_settled_analyze.py'), '--data-dir', str(self.root),
                '--output-dir', str(self.root/'output')]
        for name, directory in self.dirs.items():
            args += ['--'+name+'-dir', str(directory)]
        run = subprocess.run(args, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertIn('ORIGINAL readcost', run.stdout)
        self.assertNotIn('MISMATCH', run.stdout)
        self.assertTrue((self.root/'output/x_settled_results.json').exists())
        self.assertTrue((self.root/'output/x_settled_tables.md').exists())


if __name__ == '__main__':
    unittest.main()
