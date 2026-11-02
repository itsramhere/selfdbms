"""Synthetic, pre-data verification; never invokes a benchmark runner."""
import copy
import importlib.util
import json
import math
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
from unittest.mock import patch
import uuid

import numpy as np
from scipy import stats

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('x_readcost_analyze', HERE / 'x_readcost_analyze.py')
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


def write_json(path, obj):
    path.write_text(json.dumps(obj), encoding='utf-8')


def fixture(root, n=4, varied=False):
    (root / 'trials').mkdir()
    plan = dict(campaign_id='X-readcost-v1', cells=[dict(id=c, spec=a.cell_config(c)) for c in a.CELLS], trials=[])
    events = []
    for c in a.CELLS + ['ANCHOR']:
        for t in ([0, 5, 10] if c == 'ANCHOR' else range(n)):
            for arm in a.ARMS:
                tid = f'{c}_t{t:02d}_{arm}'
                s = dict(trial_id=tid, block='X1' if c == 'ANCHOR' else c[:2], cell=c,
                         triplet=t, arm=arm, seed=100 if c == 'ANCHOR' else t, **a.cell_config(c))
                plan['trials'].append(s)
                base = 100 * (1 + .02 * t)
                multiplier = {'A': .75, 'AMIN': 1.01, 'B': 1}[arm]
                tp = dict(write_ops_per_sec=base * multiplier, read_ops_per_sec=10 * base * multiplier)
                if c == 'ANCHOR':
                    tp['read_ops_per_sec'] = 1000 * math.exp(.02 * t)
                v = dict(mean_nanos=100., p50_nanos=80., p99_nanos=200., count=20, max_nanos=210.)
                record = dict(status='SUCCESS', cell=c, triplet=t, arm=arm, spec=copy.deepcopy(s),
                    runtime=dict(exe_sha256='a' * 64), result=dict(git_revision='revision', config=a.cell_config(c),
                    throughput=tp, latency_read_nanos=dict(p50=100., p95=200., p99=300., mean=150.),
                    sync_instrumentation=dict(read_critical_section_nanos={arm: v},
                        writer_lock_wait_nanos=dict(commit=v, flush_publish=v)),
                    lsm_diagnostics=dict(publication_events_realized_in_window=20,
                        publication_events_realized_late=0, publication_events_target=20)))
                if c in a.X2:
                    tp['write_ops_per_sec'] = 0.
                    record['result']['sync_instrumentation']['writer_lock_wait_nanos'].pop('commit')
                if varied and c != 'ANCHOR':
                    # Deterministic presentation fixture with non-degenerate CIs,
                    # near-overlapping A-min/B, and a broad dose response.
                    cfg = a.cell_config(c)
                    dose = cfg['read_io_delay_us'] if cfg['read_path'] == 'block' else 10000
                    r = cfg['reader_concurrency']
                    jitter = math.exp(.035 * math.sin(t + .6 * a.ARMS.index(arm)))
                    amin = 1.005 * math.exp(.025 * math.sin(.9 * t)) if arm == 'AMIN' else 1.
                    penalty = math.exp(-.05 - dose / 600) if arm == 'A' else amin
                    tp['write_ops_per_sec'] = (0. if c in a.X2 else
                        30000 / (1 + dose / 2500) * max(.003, penalty) * jitter)
                    tp['read_ops_per_sec'] = 1e6 / (1 + dose / 15) / (1 + .02 * r) * amin * jitter
                    lat = (9000 + 1000 * dose) * jitter
                    result = record['result']
                    result['latency_read_nanos'] = dict(p50=lat, p95=lat * 1.8, p99=lat * 2.5, mean=lat * 1.1)
                    cs = (9000 + 1000 * dose) * jitter if arm == 'A' else 2500 * amin * jitter
                    def histogram(mean, count=20):
                        return dict(mean_nanos=mean, p50_nanos=mean * .8, p99_nanos=mean * 2,
                                    count=count, max_nanos=mean * 2.1)
                    wait = {}
                    if c not in a.X2:
                        wait['commit'] = histogram(cs if arm == 'A' else 800 * jitter)
                    pubs = (max(0, 20 - int(dose / 600) - t % 3) if arm == 'A' else 20 - int(t % 9 == 0)) if c in a.X2 else (1 if t % 3 == 0 else 0)
                    if c in a.X2 or pubs:
                        pub_wait = (3000 + dose * 100000) * jitter if arm == 'A' else 2000 * amin * jitter
                        wait['flush_publish'] = histogram(pub_wait)
                    result['sync_instrumentation'] = dict(read_critical_section_nanos={arm: histogram(cs)},
                                                          writer_lock_wait_nanos=wait)
                    result['lsm_diagnostics'] = dict(publication_events_realized_in_window=pubs,
                        publication_events_realized_late=20 - pubs if c in a.X2 else 0,
                        publication_events_target=20 if c in a.X2 else 0)
                write_json(root / 'trials' / (tid + '_a1.json'), record)
                events.extend([dict(event='STARTED', trial_id=tid, attempt=1, status='STARTED', exit_code=-1),
                               dict(event='FINISHED', trial_id=tid, attempt=1, status='SUCCESS', exit_code=0)])
    write_json(root / 'plan.json', plan)
    ledger(root, events)
    return plan, events


def ledger(root, events):
    (root / 'ledger.jsonl').write_text(''.join(json.dumps(e) + '\n' for e in events), encoding='utf-8')


class StatisticsTests(unittest.TestCase):
    def test_paired_and_tost_explicit_formulas(self):
        # Independently calculate sample variance, SE, quantiles and tail probabilities.
        d = [math.log(x / y) for x, y in zip([9., 11., 12., 10.], [10., 10., 10., 10.])]
        mean = sum(d) / 4
        sd = math.sqrt(sum((v - mean) ** 2 for v in d) / 3)
        se = sd / 2
        ci = [math.exp(mean + sign * stats.t.ppf(.975, 3) * se) for sign in (-1, 1)]
        got = a.paired_stats(d)
        self.assertAlmostEqual(got['sd_log'], sd)
        self.assertAlmostEqual(got['geometric_mean_ratio'], math.exp(mean))
        np.testing.assert_allclose(got['t_ci'], ci)
        got = a.tost(d)
        margin = math.log(1 / .90)
        pl = 1 - stats.t.cdf((mean + margin) / se, 3)
        pu = stats.t.cdf((mean - margin) / se, 3)
        self.assertAlmostEqual(got['p_lower'], pl)
        self.assertAlmostEqual(got['p_upper'], pu)
        self.assertAlmostEqual(got['p_tost'], max(pl, pu))
        np.testing.assert_allclose(got['t_ci'], [math.exp(mean + sign * stats.t.ppf(.95, 3) * se) for sign in (-1, 1)])
        self.assertEqual(got['bootstrap_ci'], a.tost(d)['bootstrap_ci'])
        for d in ([], [0.]):
            self.assertIsNone(a.paired_stats(d)['t_ci'])
            self.assertIn('reason', a.paired_stats(d))

    def test_holm_monotonic(self):
        self.assertEqual(a.holm(dict(a=.01, b=.011, c=.04, d=.5)), dict(a=.04, b=.04, c=.08, d=.5))
        self.assertEqual(a.holm(dict(a=.01, b=None))['a'], .02)

    def test_page(self):
        good = a.page_test([[4, 3, 2, 1]] * 10)
        self.assertEqual(good['L'], 300)
        self.assertLess(good['p'], .001)
        self.assertEqual(good['mean_ranks'], [4., 3., 2., 1.])
        # Balanced shuffled rows: no consistent trend remains.
        shuffled = [[4, 3, 2, 1], [1, 2, 3, 4], [2, 4, 1, 3], [3, 1, 4, 2]] * 3
        self.assertGreater(a.page_test(shuffled)['p'], .05)
        self.assertIsNone(a.page_test([[4, 3, 2, 1]])['L'])

    def test_zero_sd_tost(self):
        self.assertEqual(a.tost([0., 0.])['p_tost'], 0)
        self.assertEqual(a.tost([.2, .2])['p_tost'], 1)
        self.assertEqual(a.tost([a.MARGIN] * 3)['p_tost'], 1)


class CampaignTests(unittest.TestCase):
    def setUp(self):
        scratch = HERE.parent / 'phase_i7' / 'X_READCOST' / 'analysis_smoke'
        scratch.mkdir(parents=True, exist_ok=True)
        # Python 3.13's Windows mode=0o700 temp directories exclude the sandbox
        # identity on this host. Use a uniquely named temporary directory with
        # inherited workspace permissions instead.
        self.root = scratch / ('test_' + uuid.uuid4().hex)
        self.root.mkdir()
        self.root.resolve().relative_to(scratch.resolve())
        self.addCleanup(shutil.rmtree, self.root)
        self.data = self.root / 'data'
        self.data.mkdir()
        self.plan, self.events = fixture(self.data)
        self.output = self.root / 'output'
        self.tid = 'X1_r04_d0000_t00_A'

    def run_cli(self, *extra, output=None):
        return subprocess.run([sys.executable, str(HERE / 'x_readcost_analyze.py'),
            '--data-dir', str(self.data), '--output-dir', str(output or self.output), *extra],
            capture_output=True, text=True)

    def trial(self, tid=None, attempt=1):
        return self.data / 'trials' / f'{tid or self.tid}_a{attempt}.json'

    def mutate(self, fn, tid=None):
        p = self.trial(tid)
        obj = json.loads(p.read_text())
        fn(obj)
        write_json(p, obj)

    def test_zero_rule_all_pairs_and_other_zeros(self):
        self.mutate(lambda t: t['result']['throughput'].update(write_ops_per_sec=0, read_ops_per_sec=0))
        got = a.analyze(self.data)
        cell = got['cells']['X1_r04_d0000']
        self.assertTrue(cell['zero_adjusted'])
        self.assertEqual(cell['zero_adjusted_triplets'], [0])
        expected = [math.log((101 + (.1 if t == 0 else 0)) / (100 + (.1 if t == 0 else 0))) for t in range(4)]
        self.assertAlmostEqual(cell['ratios']['W']['AMIN/B']['mean_log'], sum(expected) / 4)
        self.assertEqual(cell['ratios']['R']['A/B']['n'], 3)
        self.assertTrue(any(e.get('pair') == 'A/B' and e['outcome'] == 'R' for e in got['outcome_exclusions']))
        self.assertEqual(cell['arms']['A']['W']['n'], 4)
        self.assertEqual(cell['arms']['A']['W']['geometric_n'], 3)

    def test_missing_outcome_reported(self):
        self.mutate(lambda t: t['result']['latency_read_nanos'].pop('p50'))
        self.mutate(lambda t: t['result']['sync_instrumentation']['writer_lock_wait_nanos'].clear())
        got = a.analyze(self.data)
        self.assertEqual(got['cells']['X1_r04_d0000']['ratios']['read_p50']['A/B']['n'], 3)
        self.assertTrue(any(e['outcome'] == 'read_p50' for e in got['outcome_exclusions']))
        self.assertTrue(any(e['outcome'] == 'publication_lock_wait_p99' for e in got['outcome_exclusions']))

    def test_h2_families_and_classification(self):
        got = a.analyze(self.data)
        self.assertEqual([len(got['H2'][m]) for m in ('W', 'R', 'publication_lock_wait_p99')], [10, 14, 4])
        cell = 'X1_r04_d0000'
        self.assertEqual(got['H2']['W'][cell]['classification'], 'equivalent')
        for t in range(4):
            self.mutate(lambda v: v['result']['throughput'].update(write_ops_per_sec=200.),
                        tid=f'{cell}_t{t:02d}_AMIN')
        self.assertEqual(a.analyze(self.data)['H2']['W'][cell]['classification'], 'not_equivalent')
        for t in range(4):
            self.mutate(lambda v, t=t: v['result']['throughput'].update(write_ops_per_sec=50. if t % 2 else 200.),
                        tid=f'{cell}_t{t:02d}_AMIN')
        self.assertEqual(a.analyze(self.data)['H2']['W'][cell]['classification'], 'inconclusive')

    def test_failed_and_retry_latest(self):
        self.mutate(lambda t: t.update(status='FAILED'))
        for e in self.events:
            if e['trial_id'] == self.tid and e['event'] == 'FINISHED':
                e.update(status='FAILED', exit_code=1)
        ledger(self.data, self.events)
        got = a.analyze(self.data)
        self.assertEqual(got['counts']['failed'], 1)
        self.assertEqual(got['cells']['X1_r04_d0000']['n_complete'], 3)
        self.assertEqual(got['H1']['4']['n'], 3)
        self.assertEqual(got['H1']['16']['n'], 4)
        self.assertFalse(got['partial'])
        t = json.loads(self.trial().read_text())
        t['status'] = 'SUCCESS'
        t['result']['throughput']['write_ops_per_sec'] = 999
        write_json(self.trial(attempt=2), t)
        self.events.extend([dict(event='STARTED', trial_id=self.tid, attempt=2, status='STARTED', exit_code=-1),
                            dict(event='FINISHED', trial_id=self.tid, attempt=2, status='SUCCESS', exit_code=0)])
        ledger(self.data, self.events)
        got = a.analyze(self.data)
        self.assertEqual(got['counts']['retried'], 1)
        self.assertEqual(got['counts']['failed'], 0)
        self.assertEqual(got['cells']['X1_r04_d0000']['n_complete'], 4)
        self.assertEqual(got['cells']['X1_r04_d0000']['arms']['A']['W']['max'], 999)

    def finish(self, status, attempt=1):
        if attempt == 1:
            for e in self.events:
                if e['trial_id'] == self.tid and e['event'] == 'FINISHED':
                    e.update(status=status, exit_code=0 if status == 'SUCCESS' else -1)
        else:
            self.events.extend([
                dict(event='STARTED', trial_id=self.tid, attempt=attempt, status='STARTED', exit_code=-1),
                dict(event='FINISHED', trial_id=self.tid, attempt=attempt, status=status,
                     exit_code=0 if status == 'SUCCESS' else -1)])
        ledger(self.data, self.events)

    def successful_cli_result(self):
        done = self.run_cli()
        self.assertEqual(done.returncode, 0, done.stderr)
        return json.loads((self.output / 'x_readcost_results.json').read_text())

    def test_timeout_without_artifact_then_successful_retry(self):
        t = json.loads(self.trial().read_text())
        self.trial().unlink()
        self.finish('TIMEOUT')
        t['result']['throughput']['write_ops_per_sec'] = 999.
        write_json(self.trial(attempt=2), t)
        self.finish('SUCCESS', 2)
        got = self.successful_cli_result()
        self.assertEqual(got['counts']['retried'], 1)
        self.assertEqual(got['counts']['failed'], 0)
        self.assertEqual(got['counts']['failed_attempts'], 1)
        self.assertEqual(got['cells']['X1_r04_d0000']['arms']['A']['W']['max'], 999.)
        self.assertEqual(got['failed_attempts_detail'], [dict(trial_id=self.tid, attempt=1,
                                                            status='TIMEOUT', artifact_present=False)])

    def test_both_attempts_failed_without_artifacts(self):
        self.trial().unlink()
        self.finish('TIMEOUT')
        self.finish('INTERRUPTED', 2)
        got = self.successful_cli_result()
        self.assertFalse(got['partial'])
        self.assertEqual(got['failed_trials'], [self.tid])
        self.assertEqual(got['counts']['failed_attempts'], 2)
        self.assertEqual(got['counts']['retried'], 1)
        self.assertEqual(got['cells']['X1_r04_d0000']['n_complete'], 3)
        self.assertEqual(got['dropped_triplets'], [dict(cell='X1_r04_d0000', triplet=0, unavailable_arms=['A'])])
        self.assertEqual([v['status'] for v in got['failed_attempts_detail']], ['TIMEOUT', 'INTERRUPTED'])
        self.assertFalse(any(v['artifact_present'] for v in got['failed_attempts_detail']))
        self.assertIn('INTERRUPTED', (self.output / 'x_readcost_tables.md').read_text())

    def test_failed_envelope_without_result(self):
        self.finish('FAILED')
        for envelope in (dict(status='FAILED', error='crash'),
                         dict(status='FAILED', error='engine fault', cell='X1_r04_d0000', triplet=0,
                              arm='A', spec=dict(seed=0), runtime=dict(exe_sha256='a' * 64))):
            write_json(self.trial(), envelope)
            got = a.analyze(self.data)
            self.assertEqual(got['failed_trials'], [self.tid])
            self.assertTrue(got['failed_attempts_detail'][0]['artifact_present'])
        self.successful_cli_result()
        self.mutate(lambda v: v.update(cell='ANCHOR'))
        with self.assertRaisesRegex(ValueError, 'cell disagrees'):
            a.analyze(self.data)

    def test_non_success_ledger_overrides_success_envelope(self):
        self.finish('TIMEOUT')
        # Rejected outcomes/config/revision must not enter provenance or analysis.
        self.mutate(lambda v: v['result'].update(git_revision='ignored failed revision', config={}))
        got = a.analyze(self.data)
        self.assertEqual(got['failed_trials'], [self.tid])
        detail = got['failed_attempts_detail'][0]
        self.assertEqual(detail['envelope_status'], 'SUCCESS')
        self.assertIn('overrides', detail['note'])
        self.assertEqual(got['git_revision'], 'revision')
        self.assertEqual(got['cells']['X1_r04_d0000']['n_complete'], 3)

    def test_success_ledger_requires_success_artifact(self):
        self.mutate(lambda v: v.update(status='FAILED'))
        with self.assertRaisesRegex(ValueError, 'status mismatch'):
            a.analyze(self.data)
        self.trial().unlink()
        self.assertEqual(self.run_cli('--allow-partial').returncode, 1)

    def test_structurally_inapplicable_outcomes(self):
        # X2 W=0/commit absent are already represented by the fixture.
        # X1/X3 event-free trials have no publication histogram.
        for cell in a.X1 + a.X3:
            for t in range(4):
                for arm in a.ARMS:
                    def no_publication(v):
                        v['result']['sync_instrumentation']['writer_lock_wait_nanos'].pop('flush_publish')
                        v['result']['lsm_diagnostics'].update(publication_events_realized_in_window=0,
                                                            publication_events_realized_late=0)
                    self.mutate(no_publication, tid=f'{cell}_t{t:02d}_{arm}')
        got = a.analyze(self.data)
        self.assertEqual(got['outcome_exclusions'], [])
        for cell in a.X2:
            self.assertFalse(got['cells'][cell]['zero_adjusted'])
            self.assertEqual(got['cells'][cell]['zero_adjusted_triplets'], [])
            self.assertEqual(got['cells'][cell]['arms']['A']['W']['reason'], 'not applicable')
            self.assertEqual(got['cells'][cell]['ratios']['W']['A/B']['n'], 0)
        # Positive, actually observed values remain eligible, even in X2.
        for arm in a.ARMS:
            self.mutate(lambda v: v['result']['throughput'].update(write_ops_per_sec=5.),
                        tid=f'X2_r16_d0000_t00_{arm}')
            def observed(v):
                v['result']['sync_instrumentation']['writer_lock_wait_nanos']['flush_publish'] = dict(
                    p50_nanos=2., p99_nanos=5., mean_nanos=3., count=1, max_nanos=5.)
                v['result']['lsm_diagnostics']['publication_events_realized_in_window'] = 1
            self.mutate(observed, tid=f'X3_r01_t00_{arm}')
        got = a.analyze(self.data)
        self.assertEqual(got['cells']['X2_r16_d0000']['ratios']['W']['A/B']['n'], 1)
        self.assertEqual(got['cells']['X3_r01']['arms']['A']['publication_lock_wait_p99']['mean'], 5.)
        self.assertEqual(got['outcome_exclusions'], [])
        # A missing field in an existing histogram with events is genuine missing data.
        self.mutate(lambda v: v['result']['sync_instrumentation']['writer_lock_wait_nanos']['flush_publish'].pop('p99_nanos'),
                    tid='X3_r01_t00_A')
        got = a.analyze(self.data)
        self.assertTrue(any(v['outcome'] == 'publication_lock_wait_p99' for v in got['outcome_exclusions']))

    def test_anchor_isolated_and_drift(self):
        got = a.analyze(self.data)
        self.assertEqual(got['cells']['X1_r04_d0000']['n_complete'], 4)
        self.assertEqual(got['cells']['X1_r04_d0000']['arms']['AMIN']['R']['n'], 4)
        self.assertAlmostEqual(got['drift']['ln_D'], .2)
        self.assertTrue(got['drift']['threat_to_validity'])
        self.assertAlmostEqual(got['drift']['arms']['AMIN']['t05']['ratio'], math.exp(.1))

    def test_allow_partial_only_missing(self):
        ledger(self.data, [e for e in self.events if e['trial_id'] != self.tid])
        self.assertNotEqual(self.run_cli().returncode, 0)
        done = self.run_cli('--allow-partial')
        self.assertEqual(done.returncode, 0, done.stderr)
        self.assertTrue(json.loads((self.output / 'x_readcost_results.json').read_text())['partial'])
        self.assertTrue((self.output / 'x_readcost_tables.md').read_text().startswith('> **PARTIAL'))
        self.mutate(lambda t: t.update(cell='ANCHOR'), tid='X1_r04_d0000_t01_A')
        self.assertNotEqual(self.run_cli('--allow-partial').returncode, 0)

    def test_unfinished_latest_attempt_does_not_reuse_old_success(self):
        self.events.append(dict(event='STARTED', trial_id=self.tid, attempt=2,
                                status='STARTED', exit_code=-1))
        ledger(self.data, self.events)
        with self.assertRaisesRegex(ValueError, 'missing trials'):
            a.analyze(self.data)
        got = a.analyze(self.data, True)
        self.assertTrue(got['partial'])
        self.assertEqual(got['cells']['X1_r04_d0000']['n_complete'], 3)

    def test_fail_closed_cases(self):
        original_plan = (self.data / 'plan.json').read_bytes()
        original_trial = self.trial().read_bytes()
        cases = [
            ('wrong campaign', lambda: self.change_plan(lambda p: p.update(campaign_id='wrong'))),
            ('no ledger', lambda: ledger(self.data, [e for e in self.events if e['trial_id'] != self.tid])),
            ('invalid JSON', lambda: self.trial().write_text('{')),
            ('schema', lambda: self.mutate(lambda t: t.pop('runtime'))),
            ('revision', lambda: self.mutate(lambda t: t['result'].update(git_revision='other'))),
            ('runner', lambda: self.mutate(lambda t: t['runtime'].update(exe_sha256='b' * 64))),
            ('plan seeds', lambda: self.change_plan(lambda p: p['trials'][0].update(seed=999))),
            ('trial seed', lambda: self.mutate(lambda t: t['spec'].update(seed=999))),
            ('cell', lambda: self.mutate(lambda t: t.update(cell='ANCHOR'))),
            ('triplet', lambda: self.mutate(lambda t: t.update(triplet=9))),
            ('arm', lambda: self.mutate(lambda t: t.update(arm='B'))),
            ('delay', lambda: self.mutate(lambda t: t['result']['config'].update(read_io_delay_us=20))),
            ('path', lambda: self.mutate(lambda t: t['result']['config'].update(read_path='legacy_scan'))),
            ('readers', lambda: self.mutate(lambda t: t['result']['config'].update(reader_concurrency=16))),
            ('bad number', lambda: self.mutate(lambda t: t['result']['throughput'].update(read_ops_per_sec=-1))),
            ('missing file', lambda: self.trial().unlink()),
            ('unreadable file', self.unreadable_file),
        ]
        for name, change in cases:
            with self.subTest(name=name):
                (self.data / 'plan.json').write_bytes(original_plan)
                self.trial().write_bytes(original_trial)
                ledger(self.data, self.events)
                change()
                run = self.run_cli()
                self.assertEqual(run.returncode, 1, run.stderr)
                self.assertIn('FAIL-CLOSED ERROR', run.stderr)
                self.assertFalse(self.output.exists())
                if name != 'no ledger':
                    self.assertEqual(self.run_cli('--allow-partial').returncode, 1)

    def unreadable_file(self):
        self.trial().unlink()
        self.trial().mkdir()  # Opening a directory as a JSON file fails on every OS.

    def change_plan(self, fn):
        p = json.loads((self.data / 'plan.json').read_text())
        fn(p)
        write_json(self.data / 'plan.json', p)

    def test_output_directory_determinism_and_figures(self):
        before = {p.relative_to(self.data): p.read_bytes() for p in self.data.rglob('*') if p.is_file()}
        one = self.run_cli()
        self.assertEqual(one.returncode, 0, one.stderr)
        other = self.root / 'elsewhere' / 'nested'
        two = self.run_cli(output=other)
        self.assertEqual(two.returncode, 0, two.stderr)
        for name in ('x_readcost_results.json', 'x_readcost_tables.md'):
            self.assertEqual((self.output / name).read_bytes(), (other / name).read_bytes())
        self.assertEqual(before, {p.relative_to(self.data): p.read_bytes() for p in self.data.rglob('*') if p.is_file()})
        figs = self.root / 'figures'
        run = subprocess.run([sys.executable, str(HERE / 'x_readcost_figures.py'), '--results',
             str(self.output / 'x_readcost_results.json'), '--output-dir', str(figs)], capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        for name in ('fig_x1_writes', 'fig_x1_reads', 'fig_x1_ratio', 'fig_x2_publication', 'fig_amin_vs_b'):
            for ext in ('pdf', 'png'):
                self.assertGreater((figs / f'{name}.{ext}').stat().st_size, 1000)
        # Deliberately remove cells and CIs: plotting remains usable.
        data = json.loads((self.output / 'x_readcost_results.json').read_text())
        data['cells'].pop('X1_r04_d0000')
        data['cells']['X1_r04_d0020']['arms']['A']['W']['t_ci'] = None
        write_json(other / 'sparse.json', data)
        run = subprocess.run([sys.executable, str(HERE / 'x_readcost_figures.py'), '--results',
             str(other / 'sparse.json'), '--output-dir', str(figs)], capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertIn('WARNING', run.stderr)


class FigureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        module_spec = importlib.util.spec_from_file_location('x_readcost_figures', HERE / 'x_readcost_figures.py')
        cls.f = importlib.util.module_from_spec(module_spec)
        module_spec.loader.exec_module(cls.f)
        cls.f.setup()

    def setUp(self):
        self.addCleanup(self.f.plt.close, 'all')

    def capture(self, fn, data, *args):
        with patch.object(self.f, 'save') as saved:
            fn(data, Path('.'), *args)
        return saved.call_args.args[0]

    def test_dodge_and_connected_labels(self):
        f = self.f
        fig, ax = f.plt.subplots()
        ax.set(yscale='log', ylim=(.1, 10))
        endpoints = []
        for arm, style in f.STYLE.items():
            end = f.categorical_series(ax, f.X, [1.] * 5, [[.9, 1.1]] * 5,
                                       style, connected=4, offset=f.DODGE[arm])
            self.assertEqual(end, (3 + f.DODGE[arm], 1., style['label']))
            endpoints.append(end)
        self.assertEqual(f.end_labels(ax, endpoints), [])  # Overlapping labels: use legend.
        for i, arm in enumerate(f.STYLE):
            containers = ax.containers[i * 5:(i + 1) * 5]
            for container, x in zip(containers, f.X):
                self.assertAlmostEqual(container.lines[0].get_xdata()[0], x + f.DODGE[arm])
        self.assertIsNone(f.categorical_series(ax, f.X, [None] * 4 + [1.], [None] * 5,
                                              f.STYLE['A'], connected=4))
        # Two separable labels stay within the fixed, bounded log-space nudge.
        labels = f.end_labels(ax, [(3., .5, 'r=4'), (3.08, 2., 'r=16')])
        self.assertEqual(len(labels), 2)
        self.assertEqual([v.xy for v in labels], [(3., .5), (3.08, 2.)])

    def test_forest_groups_ticks_and_ci_limits(self):
        f = self.f
        data = dict(H2=dict(W={'X1_r04_d0000': dict(geometric_mean_ratio=.6, t_ci=[.4, .8])},
                            R={'X1_r16_d2000': dict(geometric_mean_ratio=1.8, t_ci=[1.2, 2.2]),
                               'X2_r16_legacy': dict(geometric_mean_ratio=1., t_ci=None)}),
                    H3={'X3_r01': {m: dict(geometric_mean_ratio=1., t_ci=[.95, 1.05]) for m in ('W', 'R')}})
        fig = self.capture(f.forest, data)
        self.assertAlmostEqual(fig.get_size_inches()[0], 7.16)
        self.assertLess(fig.get_size_inches()[1], 6.5)
        for ax in fig.axes:
            self.assertEqual(list(ax.get_xticks()), [.90, 1., 1.11])
            self.assertEqual([t.get_text() for t in ax.get_xticklabels()], ['0.90', '1.00', '1.11'])
            labels = [t.get_text() for t in ax.get_yticklabels()]
            self.assertFalse(any('X1_r' in s or '(90%)' in s for s in labels))
            self.assertIn('X1 mixed (90% CI)', labels)
            self.assertIn('X3 churn (95% CI)', labels)
        self.assertLess(fig.axes[0].get_xlim()[0], .4)
        self.assertGreater(fig.axes[1].get_xlim()[1], 2.2)
        self.assertIn('X2 publication (90% CI)', [t.get_text() for t in fig.axes[1].get_yticklabels()])
        self.assertIn('r=16, 2 ms', [t.get_text() for t in fig.axes[1].get_yticklabels()])

    def test_publication_units_ticks_and_zero_omission(self):
        f = self.f
        data = dict(cells={c: dict(arms={arm: dict(
            publications=dict(mean=15., min=10, max=20),
            publication_lock_wait_p99=dict(geometric_mean=2e6, t_ci=[1e6, 3e6]))
            for arm in a.ARMS}) for c in a.X2})
        fig = self.capture(f.publication_plot, data)
        self.assertEqual(list(fig.axes[0].get_yticks()), [0, 5, 10, 15, 20])
        ax = fig.axes[1]
        self.assertEqual(ax.get_yscale(), 'log')
        self.assertTrue(ax.get_ylabel().endswith('(ms)'))
        self.assertEqual(ax.yaxis.get_major_formatter()(.001, 0), '0.001')
        self.assertEqual(ax.yaxis.get_major_formatter()(1000., 0), '1000')
        for container in ax.containers:
            self.assertEqual(list(container.lines[0].get_ydata()), [2.])
        count = len(ax.containers)
        self.assertFalse(f.point(ax, 0, 0., None, f.STYLE['A']))
        self.assertFalse(f.point(ax, 0, None, None, f.STYLE['A']))
        self.assertEqual(len(ax.containers), count)

    def test_ratio_isolated_legacy_has_no_direct_labels(self):
        f = self.f
        data = dict(cells={f'X1_r{r:02d}_legacy': dict(ratios={'W': {'A/AMIN':
                        dict(geometric_mean_ratio=.5, t_ci=[.4, .6])}}) for r in (4, 16)})
        fig = self.capture(f.ratio_plot, data)
        ax = fig.axes[0]
        self.assertEqual([t.get_text() for t in ax.texts], ['no penalty'])
        self.assertIsNotNone(ax.get_legend())
        self.assertEqual(ax.get_yscale(), 'log')


if __name__ == '__main__':
    unittest.main()
