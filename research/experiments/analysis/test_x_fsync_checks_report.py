"""Synthetic report tests; never invokes the engine or fsync driver."""
import copy
from contextlib import contextmanager
import importlib.util
import json
import math
from pathlib import Path
import shutil
import unittest
import uuid

import numpy as np
from scipy import stats

SPEC = importlib.util.spec_from_file_location('x_fsync_checks_report', Path(__file__).with_name('x_fsync_checks_report.py'))
a = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(a)


@contextmanager
def scratch_directory():
    # Ordinary inherited permissions work in the Windows workspace sandbox.
    root = Path(__file__).resolve().parent / ('fsync-test-' + uuid.uuid4().hex)
    root.mkdir()
    try:
        yield root
    finally:
        shutil.rmtree(root)


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value), encoding='utf-8')


def result(reads=100, writes=183, slow=False):
    ms = 25 if slow else 2
    return dict(status='SUCCESS', diagnostic=True, trial_id='synthetic', achieved=True,
                throughput=dict(read_ops_per_sec=reads, write_ops_per_sec=writes),
                paced_load=dict(write_service_nanos=dict(max=ms * 1e6),
                                write_response_nanos=dict(mean=3e6, p50=2e6, sample_count=10)),
                stall_diagnostics=dict(window_ns=1e10, commit_count=10, commits_dropped=0,
                    phase_histograms={k: dict(stats=dict(p50=2e6, max=ms * 1e6, mean=2e6))
                                      for k in ('validate', 'wal_write', 'wal_sync', 'apply')},
                    commits=[dict(WalWriteDoneNs=1000000, WalSyncDoneNs=1000000 + ms * 1e6)]))


def fixture(root):
    checks, settled = root / 'checks', root / 'settled'
    entries = []
    for block, rate in enumerate((72, 183), 1):
        entry = dict(part='bare', rate=rate, block=block)
        entries.append(entry)
        write(checks / 'bare' / f'r{rate:04d}_b{block:02d}.json',
              dict(entry, n=100, window_s=10, fsync_p50_ms=2, fsync_max_ms=45,
                   count_ge_25=2, sum_ge_25_ms=70, slow=[[0, 25], [.5, 45]], ac_line=1,
                   first_fsync_ms=25, order_position=block))
    for condition in ('on', 'off'):
        for arm in ('AMIN', 'B'):
            for rnd in (1, 2):
                tid = f'F_{condition}_{arm}_t{rnd:02d}'
                entries.append(dict(part='engine', condition=condition, arm=arm, round=rnd, id=tid))
                reads = (100 + rnd * (10 if condition == 'on' else 5)) * (1.01 if arm == 'AMIN' else 1)
                write(checks / 'engine' / (tid + '.json'), result(reads=reads, slow=rnd == 1))
                write(checks / 'engine' / (tid + '.meta.json'), dict(ac_line=1, exit_code=0))
    write(checks / 'plan.json', dict(runs=entries))
    for cells in a.REFERENCE.values():
        for cell in cells:
            for arm in ('A', 'AMIN', 'B'):
                for rnd in range(1, 11):
                    write(settled / 'trials' / f'{cell}_t{rnd:02d}_{arm}_a1.json',
                          dict(status='SUCCESS', spec={}, result=result(reads=100 + rnd, slow=rnd == 1)))
    return checks, settled


class ReportTests(unittest.TestCase):
    def test_paired_hold_adjustment_removes_known_free_ratio(self):
        on, off = {}, {}
        for (arm, rnd), free in {('AMIN', 1): .8, ('B', 1): .4,
                                 ('AMIN', 2): .6, ('B', 2): .5}.items():
            value = result(reads=1000 * free * (1.1 if arm == 'AMIN' else 1))
            for phase in value['stall_diagnostics']['phase_histograms'].values():
                phase['stats']['mean'] = (1 - free) * 1e9 / 4
            on[arm, rnd] = value
            off[arm, rnd] = result(reads=1000)
        hold = a.hold_account(dict(on=on, off=off))['on']
        self.assertAlmostEqual(hold['paired']['raw']['geometric_mean'], 1.1 * math.sqrt(2 * 1.2))
        self.assertAlmostEqual(hold['paired']['adjusted']['geometric_mean'], 1.1)
        self.assertAlmostEqual(hold['paired']['adjusted']['sd_log'], 0)
        np.testing.assert_allclose(hold['paired']['adjusted']['ci90'], [1.1, 1.1])
        self.assertAlmostEqual(hold['paired']['raw_log_ratio_log_free_ratio_r'], 1)
        self.assertAlmostEqual(hold['mean_log_observed_over_predicted'], math.log(1.1) / 2)

    def test_hold_and_first_fsync(self):
        with scratch_directory() as root:
            checks, settled = fixture(root)
            report = a.build_report(checks, settled, assert_settled_counts=False)
            self.assertEqual(report['bare']['72']['any_ge_25_excluding_first'], dict(k=1, n=1))
            self.assertEqual(report['bare']['72']['first_fsync_ge_25'], 1)
            self.assertEqual(report['bare']['72']['first_fsync_ms'], dict(median=25, min=25, max=25))
            self.assertEqual(report['burst_run_positions'], [1, 2])
            hold = report['hold_account']['on']
            self.assertEqual(hold['n'], 4)
            self.assertAlmostEqual(hold['hold_share_pct']['median'], .8)
            self.assertAlmostEqual(hold['burst_share_pct']['median'], .125)
            self.assertAlmostEqual(hold['paired']['raw']['geometric_mean'], 1.01)
            self.assertAlmostEqual(hold['paired']['adjusted']['geometric_mean'], 1.01)
        # Separate phase means and commit count, rather than p50 or stored count.
        value = result()
        value['stall_diagnostics']['phase_histograms']['apply']['stats']['mean'] = 6e6
        self.assertAlmostEqual(a.hold_record(value)['free_share'], .988)

    def test_bursts(self):
        self.assertEqual(a.burst_count([]), 0)
        self.assertEqual(a.burst_count([(0, 24.999), (1, 3)]), 0)
        self.assertEqual(a.burst_count([(0, 25)]), 1)
        self.assertEqual(a.burst_count([(0, 25), (.999, 30)]), 1)
        self.assertEqual(a.burst_count([(0, 25), (1, 25)]), 2)
        self.assertEqual(a.burst_count([(2, 25), (0, 25), (.5, 45), (3, 25)]), 3)

    def test_paired_ci_and_class(self):
        logs = np.array([-.02, 0, .02])
        summary = a.paired_summary(np.exp(logs), [1, 1, 1])
        half = stats.t.ppf(.95, 2) * .02 / math.sqrt(3)
        self.assertAlmostEqual(summary['geometric_mean'], 1)
        self.assertAlmostEqual(summary['sd_log'], .02)
        np.testing.assert_allclose(summary['ci90_log'], [-half, half], atol=1e-15)
        self.assertEqual(summary['classification'], 'equivalent')
        self.assertEqual(a.paired_summary([2, 2, 2], [1, 1, 1])['classification'], 'not_equivalent')
        self.assertEqual(a.ci_class([-a.MARGIN, a.MARGIN]), 'inconclusive')
        self.assertEqual(a.ci_class([a.MARGIN, .2]), 'inconclusive')
        self.assertEqual(a.paired_summary([1], [1])['classification'], 'inconclusive')

    def test_deterministic_and_visible_failures(self):
        with scratch_directory() as temp:
            root = Path(temp)
            checks, settled = fixture(root)
            bad = checks / 'engine' / 'F_off_B_t02.json'
            failed = result()
            failed['status'] = 'FAILED'
            write(bad, failed)
            missing = checks / 'engine' / 'F_off_AMIN_t02.json'
            missing.unlink()
            flagged = checks / 'engine' / 'F_on_AMIN_t01.json'
            value = json.loads(flagged.read_text())
            value['stall_diagnostics']['commits_dropped'] = 7
            value['stall_diagnostics']['commits'] = []
            write(flagged, value)
            write(flagged.with_suffix('.meta.json'), dict(ac_line=0, exit_code=0))
            original = copy.deepcopy(value)
            for output in (root / 'one', root / 'two'):
                report = a.write_report(checks, settled, output, assert_settled_counts=False)
            for name in ('report.json', 'report.md'):
                self.assertEqual((root / 'one' / name).read_bytes(), (root / 'two' / name).read_bytes())
            self.assertEqual(len(report['failed']), 2)
            self.assertTrue(any(f.get('commits_dropped') == 7 for f in report['flags']))
            self.assertTrue(any(f.get('ac_line') == 0 for f in report['flags']))
            self.assertTrue(any(f.get('histogram_stored_crossing_disagreement') for f in report['flags']))
            self.assertEqual(report['engine_on_off']['on']['AMIN']['any_ge_25']['k'], 1)
            self.assertEqual(json.loads(flagged.read_text()), original)
            self.assertEqual(report['bare']['72']['any_ge_45'], dict(k=1, n=1))
            self.assertEqual(report['bare']['72']['bursts_total'], 1)
            self.assertAlmostEqual(report['engine_on_off']['on']['B']['write_excess_s']['median'], .01)
            self.assertEqual(report['paired_ratios']['on']['rounds'], [1, 2])
            self.assertEqual(report['pooled_log_R']['on']['df'], 2)
            with self.assertRaisesRegex(AssertionError, 'Settled 155'):
                a.build_report(checks, settled)


if __name__ == '__main__':
    unittest.main()
