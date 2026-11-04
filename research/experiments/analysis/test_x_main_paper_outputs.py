"""Small synthetic fixture for the main-paper outputs; no campaign execution."""
import copy
import hashlib
import json
import math
from pathlib import Path
import statistics as st
import shutil
import unittest
import uuid

import test_x_followup_paper_outputs as follow_fixture
import x_main_paper_outputs as g


def ratio(value, low, high):
    return dict(geometric_mean_ratio=value, t_ci=[low, high])


def x1_fixture():
    # (S, S CI, R ratio, R CI) per r/dose; the two-of-six cells are r=4 d0 and r=16 d20.
    plan = {'d0000': {4: (.988, .896, 1.089, 1.193, 1.089, 1.308), 16: (.937, .884, .993, 1.097, 1.041, 1.156)},
            'd0020': {4: (.926, .881, .974, 1.452, 1.380, 1.528), 16: (.944, .887, 1.004, 1.432, 1.350, 1.519)},
            'd0200': {4: (.758, .725, .793, 1.187, 1.136, 1.241), 16: (.705, .676, .736, 1.093, 1.048, 1.140)},
            'd2000': {4: (.361, .354, .368, .725, .710, .739), 16: (.311, .305, .316, .625, .614, .636)},
            'legacy': {4: (.004, .004, .005, .489, .468, .511), 16: (.002, .002, .002, .522, .501, .544)}}
    cells = {}
    for dose, by_r in plan.items():
        for r, v in by_r.items():
            hold = 10e3 * (1 + r) * (1 + len(cells))  # strictly increasing, in ns
            cells[g.x1_cell(r, dose)] = dict(
                ratios=dict(W={'A/AMIN': ratio(*v[:3])}, R={'A/AMIN': ratio(*v[3:])}),
                arms={'A': dict(read_cs_mean=dict(mean=hold))})
    return dict(cells=cells)


def mech_fixture(x1):
    cells = {}
    for name in x1['cells']:
        q = lambda value: dict(reads_per_reader_per_commit=dict(mean=value))
        cells[name] = dict(arms={'A': q(2.0), 'AMIN': q(1.5)})
    return dict(cells=cells)


def intervention_fixture():
    cells = {}
    for r in (4, 16):
        for d in (0, 20, 200, 2000):
            name = f'I1_r{r:02d}_d{d:04d}'
            low = d <= 20
            cl_w = ratio(1.2, 1.1, 1.3) if (d >= 200 or (r == 16 and d == 0)) else ratio(1.02, .95, 1.1)
            cells[name] = dict(
                conditions={'inline': dict(ratios_A_over_AMIN=dict(R=ratio(.97, .90, 1.05) if low else ratio(.8, .7, .9)))},
                ratios_inline_over_second={'A': dict(W=cl_w, R=ratio(.6, .5, .7)),
                                           'AMIN': dict(W=ratio(1.0, .9, 1.1), R=ratio(.7, .6, .8))})
    return dict(cells=cells)


def settled_fixture(above=3):
    trials = {f'S_{i}': dict(median_largest_fsync_ns=(7 + i if i >= above else 31.4 + .05 * i) * 1e6,
                             threshold_ns=40000000) for i in range(18)}
    return dict(H_S=dict(trials=trials, verdict='supported'))


def build(directory):
    results, data, _ = follow_fixture.fixture(directory)
    for path in sorted((directory / 'trials').glob('F[123]*_a*.json')):
        trial = json.loads(path.read_text(encoding='utf-8'))
        index = (g.follow_tables.F1 + (g.follow_tables.F2, g.follow_tables.F3)).index(trial['cell'])
        rnd = trial['triplet']
        # Distinct cell capacities keep the inverse-capacity correlation defined.
        trial['result']['throughput']['read_ops_per_sec'] *= 1 + index/10
        data['trials'][trial['trial_id']]['R'] *= 1 + index/10
        trial['result']['operations'] = dict(read_ops_completed=1000 + 100*rnd)
        trial['result']['lsm_diagnostics']['measured_memtable_point_reads'] = 100 + 40*index + 10*rnd
        path.write_text(json.dumps(trial), encoding='utf-8')
    for index, cell in enumerate(g.follow_tables.F1 + (g.follow_tables.F2, g.follow_tables.F3)):
        for arm in g.ARMS:
            data['cells'][cell]['arms'][arm]['R']['mean'] *= 1 + index/10
    results.write_text(json.dumps(data), encoding='utf-8')
    x1 = x1_fixture()
    paths = {}
    for name, obj in (('x1', x1), ('mech', mech_fixture(x1)), ('inter', intervention_fixture()),
                      ('settled', settled_fixture())):
        paths[name] = directory / (name + '.json')
        paths[name].write_text(json.dumps(obj), encoding='utf-8')
    return results, data, paths


class MainPaperTests(unittest.TestCase):
    def setUp(self):
        self.directory = (g.ROOT / ('.main-paper-test-' + uuid.uuid4().hex)).resolve()
        self.assertEqual(self.directory.parent, g.ROOT.resolve())
        self.directory.mkdir()
        self.addCleanup(shutil.rmtree, self.directory)
        self.results, self.data, self.paths = build(self.directory)
        for name in g.CAMPAIGN_DIRS:
            (self.directory / name).mkdir()
            (self.directory / name / 'platform.json').write_text(
                json.dumps(dict(selftest=dict(runtime=dict(go_version='go1.26.3')))), encoding='utf-8')

    def generate(self, out='mainpaper.tex'):
        return g.run(self.paths['x1'], self.paths['mech'], self.paths['inter'], self.results,
                     self.directory, self.paths['settled'], self.directory / out, self.directory / 'figures', self.directory)

    def test_values_and_determinism(self):
        values = self.generate()
        text = (self.directory / 'mainpaper.tex').read_text(encoding='utf-8')
        self.assertEqual(text.count(' & '), 6 * 6 + 4 * 2 + 4 * 4 + 5 * 4)
        self.assertEqual(values['LowDoseReadPoints'], '0.970, 0.970, 0.970, 0.970')
        self.assertEqual(values['GainCellFourZeroS'], '0.988 [0.896, 1.089]')
        self.assertEqual(values['GainCellSixteenTwentyR'], '1.432 [1.350, 1.519]')
        self.assertEqual(values['HSAboveDraftCount'], '3')
        self.assertEqual(values['HSMedianRange'], '10.0--31.5')
        self.assertEqual(values['HSAboveDraftRange'], '31.4--31.5')
        self.assertEqual(values['HSThresholdMs'], '40')
        self.assertEqual(values['GoVersion'], '1.26.3')
        # Fixture: four equal phases, so the WAL sync share is one quarter.
        self.assertEqual(values['FsyncShareRange'], '25.0--25.0')
        # hold_share / W in ms for the fixture's first F1 cell: (1 - free) / 183.
        self.assertEqual(values['HoldMsLeaseLow'], f"{1000 * (1 - (.8 - .005 * 2)) / 183:.2f}")
        self.assertIn('\\midrule', text)
        self.assertTrue((self.directory / 'figures/fig_penalty_no_legacy.pdf').exists())
        self.assertFalse((self.directory / 'figures/fig_penalty_no_legacy.png').exists())
        paths = [self.directory / 'mainpaper.tex', self.directory / 'figures/fig_penalty_no_legacy.pdf']
        before = [hashlib.sha256(p.read_bytes()).hexdigest() for p in paths]
        self.generate()
        self.assertEqual(before, [hashlib.sha256(p.read_bytes()).hexdigest() for p in paths])

    def test_memory_means_use_per_trial_ratios_including_f2(self):
        means = g.memtable_values(self.data, self.directory)
        for index, cell in enumerate(g.follow_tables.F1 + (g.follow_tables.F2, g.follow_tables.F3)):
            for arm in g.ARMS:
                rows = [v for v in self.data['trials'].values() if v['cell'] == cell and v['arm'] == arm]
                shares = [(100 + 40*index + 10*r) / (1000 + 100*r) for r in (1, 2, 3)]
                capacity = st.mean(v['R']/v['free_share'] for v in rows)
                self.assertAlmostEqual(means[cell, arm][0], st.mean(shares))
                self.assertAlmostEqual(means[cell, arm][1], capacity)
                if cell != g.follow_tables.F3:  # F3 deliberately has constant free share.
                    self.assertFalse(math.isclose(capacity, st.mean(v['R'] for v in rows) /
                                                 st.mean(v['free_share'] for v in rows), rel_tol=1e-8))
                if index:
                    self.assertNotAlmostEqual(means[cell, arm][0],
                                              st.mean(100 + 40*index + 10*r for r in (1, 2, 3)) /
                                              st.mean(1000 + 100*r for r in (1, 2, 3)))
        rows, values = g.memtable_outputs(means)
        self.assertEqual(len(rows), 5)
        self.assertEqual(rows[0], '183 & 0.100 & 0.100 & 1{,}030 & 1{,}020' + r' \\')
        self.assertEqual(values['MemFitParameterCount'], '2')
        self.assertEqual(values['MemFitCellCount'], '4')

    def test_memory_fit_uses_inverse_capacity_and_f3_is_outside_fit(self):
        # A known line: cost = 3e-6 - 2e-6 * share. F3 has 80% of its predicted capacity.
        means = {(c, a): (share, 1/(3e-6 - 2e-6*share))
                 for c, share in zip(g.follow_tables.F1, (.1, .2, .3, .4)) for a in g.ARMS}
        for a in g.ARMS:
            means[g.follow_tables.F3, a] = (.5, .8/(3e-6 - 2e-6*.5))
        _, values = g.memtable_outputs(means)
        for suffix in ('L', 'S'):
            self.assertEqual(values['MemFitPearson' + suffix], '-1.000')
            self.assertEqual(values['MemFitOffRatio' + suffix], '0.800')
        self.assertEqual(values['MemShareAboutLow'], '0.10')
        self.assertEqual(values['MemShareAboutHigh'], '0.40')
        self.assertEqual(values['MemCapacityAboutLow'], '360{,}000')
        self.assertEqual(values['MemCapacityAboutHigh'], '450{,}000')

    def test_memory_provenance_and_denominator_are_checked(self):
        bad = copy.deepcopy(self.data)
        bad['trials'][next(iter(bad['trials']))]['R'] *= 2
        with self.assertRaisesRegex(ValueError, 'raw/results mismatch'):
            g.memtable_values(bad, self.directory)
        bad = copy.deepcopy(self.data)
        bad['trials'][next(iter(bad['trials']))]['round'] = 99
        with self.assertRaisesRegex(ValueError, 'metadata mismatch'):
            g.memtable_values(bad, self.directory)
        path = next((self.directory / 'trials').glob('F2_*_a*.json'))
        trial = json.loads(path.read_text(encoding='utf-8'))
        trial['result']['operations']['read_ops_completed'] = 0
        path.write_text(json.dumps(trial), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'invalid memtable share'):
            g.memtable_values(self.data, self.directory)

    def test_prose_claims_are_asserted(self):
        x1 = json.loads(self.paths['x1'].read_text(encoding='utf-8'))
        x1['cells'][g.x1_cell(4, 'd0200')]['ratios']['R']['A/AMIN']['geometric_mean_ratio'] = .9
        self.paths['x1'].write_text(json.dumps(x1), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'six read-gain cells'):
            self.generate()
        x1 = x1_fixture()
        x1['cells'][g.x1_cell(4, 'd0020')]['ratios']['W']['A/AMIN']['t_ci'] = [.9, 1.1]
        self.paths['x1'].write_text(json.dumps(x1), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'two-of-six'):
            self.generate()
        self.paths['x1'].write_text(json.dumps(x1_fixture()), encoding='utf-8')
        inter = intervention_fixture()
        inter['cells']['I1_r04_d0000']['conditions']['inline']['ratios_A_over_AMIN']['R']['t_ci'] = [.8, .9]
        self.paths['inter'].write_text(json.dumps(inter), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'low-dose read ratio'):
            self.generate()
        self.paths['inter'].write_text(json.dumps(intervention_fixture()), encoding='utf-8')
        settled = settled_fixture()
        settled['H_S']['trials']['S_0']['median_largest_fsync_ns'] = 41e6
        self.paths['settled'].write_text(json.dumps(settled), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'reaches the final threshold'):
            self.generate()

    def test_mixed_go_versions_fail(self):
        (self.directory / 'paced' / 'platform.json').write_text(
            json.dumps(dict(selftest=dict(runtime=dict(go_version='go1.25.0')))), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'different Go toolchains'):
            self.generate()

    def test_missing_f1_trace_fails(self):
        for p in (self.directory / 'trials').glob('F1_w0183*'):
            p.unlink()
        with self.assertRaisesRegex(ValueError, 'F1 trials'):
            self.generate()


if __name__ == '__main__':
    unittest.main()
