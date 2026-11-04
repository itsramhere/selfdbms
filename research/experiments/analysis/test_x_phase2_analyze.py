"""Phase 2 arithmetic, frozen boundaries, pairing and artifact reproducibility."""
import copy
import json
import math
from pathlib import Path
import subprocess
import sys
import shutil
import unittest
import uuid

import x_phase2_analyze as p


def trial(cell, group, arm, round_):
    r = int(cell.split('_r')[1].split('_')[0]) if '_r' in cell else 16
    dose = int(cell.split('_d')[1].split('_')[0]) if '_d' in cell else 0
    k = 1 if group == 'intervention_inline' else 2
    window = (10_000_000_000, 12_000_000_000)[round_ % 2]
    commits = 1000 + 10 * round_
    h = 1000.0 + 150 * dose + 10 * r
    # Nonconstant test x/S; exact M1 per-trial relation for fixture purposes.
    lease_w = commits / (window / 1e9)
    w = lease_w / (1 + k * h / (window / commits)) if arm == 'A' else lease_w
    tid = f'{cell}_t{round_:02d}_{arm}'
    spec = dict(trial_id=tid, cell=cell, arm=arm, triplet=round_,
                reader_concurrency=r, writer_concurrency=1, read_io_delay_us=dose,
                inline_flush_check=k == 1, seed=round_)
    latency = dict(min=100, p50=200, p95=1000, p99=2000, p999=4000, max=10000, mean=1200)
    hist = dict(stats=dict(sample_count=3), buckets=[dict(lower_ns=0, count=1),
                                                   dict(lower_ns=1000, count=2)])
    result = dict(trial_id=tid, cell=cell, arm=arm, status='SUCCESS', sync_mode='fixture_CoarseLock',
                  timestamps=dict(measured_window_ns=window),
                  operations=dict(write_ops_completed_in_window=commits, write_ops_completed_late=999999),
                  throughput=dict(write_ops_per_sec=w), latency_read_nanos=latency,
                  sync_instrumentation=dict(read_critical_section_nanos={
                      'fixture_CoarseLock': dict(mean_nanos=h, p95_nanos=2 * h),
                      'decoy': dict(mean_nanos=9999999999, p95_nanos=9999999999)}),
                  stall_diagnostics=dict(phase_histograms={phase: copy.deepcopy(hist) for phase in p.PHASES}))
    return dict(trial_id=tid, cell=cell, arm=arm, triplet=round_, spec=spec, result=result, status='SUCCESS')


def fixture():
    datasets = {s: [] for s in p.SOURCES}
    for group, (source, cells) in p.SELECTION.items():
        for cell in cells:
            n = 5 if group.startswith('intervention_') else 10
            datasets[source] += [trial(cell, group, arm, i) for i in range(1, n + 1) for arm in ('A', 'AMIN')]
    for cell in p.SETTLED:
        datasets['settled'] += [trial(cell, 'settled', arm, i)
                                for i in range(1, 11) for arm in ('A', 'AMIN')]
    return datasets


class ModelTests(unittest.TestCase):
    def test_known_models(self):
        self.assertEqual(p.predictions(2, 4, 8, 2), {'M1': 2 / 3, 'M2': .5})
        self.assertEqual(p.predictions(2, 4, 8, 1), {'M1': .8, 'M2': 2 / 3})

    def test_cell_arithmetic_pairing_mode_discovery_and_own_window(self):
        cell = 'X1_r04_d0200'
        rows = [trial(cell, 'development', arm, i) for i in range(1, 11) for arm in ('A', 'AMIN')]
        for t in rows:
            i = t['triplet']
            t['result']['throughput']['write_ops_per_sec'] = (2 if i <= 5 else 9) if t['arm'] == 'A' else (1 if i <= 5 else 2)
            if t['arm'] == 'A':
                h = t['result']['sync_instrumentation']['read_critical_section_nanos']['fixture_CoarseLock']
                h.update(mean_nanos=i * 1000, p95_nanos=i * 3000)
        out = p.summarize_cell(cell, 'development', rows)
        self.assertEqual(out['h_ns'], 5500)
        self.assertEqual(out['h_p95_ns'], 16500)
        self.assertAlmostEqual(out['S'], 3)  # sqrt(2 * 4.5), not ratio of arithmetic means
        lease = [t for t in rows if t['arm'] == 'AMIN']
        expected = sum(t['result']['timestamps']['measured_window_ns'] /
                       t['result']['operations']['write_ops_completed_in_window'] for t in lease) / 10
        self.assertAlmostEqual(out['T_L_ns'], expected)
        self.assertEqual(out['k'], 2)
        self.assertEqual(out, p.summarize_cell(cell, 'development', list(reversed(rows))))
        bad = copy.deepcopy(rows)
        del bad[0]['result']['sync_instrumentation']['read_critical_section_nanos']['fixture_CoarseLock']
        with self.assertRaisesRegex(ValueError, 'missing CoarseLock mode key'):
            p.summarize_cell(cell, 'development', bad)
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            p.summarize_cell(cell, 'development', rows + [rows[0]])
        with self.assertRaisesRegex(ValueError, 'unpaired'):
            p.summarize_cell(cell, 'development', rows[:-1])
        bad = copy.deepcopy(rows)
        bad[1]['result']['operations']['write_ops_completed_in_window'] = 0
        with self.assertRaisesRegex(ValueError, 'Lease commits'):
            p.summarize_cell(cell, 'development', bad)

    def test_criterion1_inclusive_boundary_and_failure(self):
        b = p.BOUND
        for sign in (-1, 1):
            self.assertTrue(p.criteria([sign * b], [0])['criteria']['1']['passes'])
            self.assertFalse(p.criteria([sign * math.nextafter(b, math.inf)], [0])['criteria']['1']['passes'])
        c = p.criteria([0, b, 2 * b], [0])
        self.assertEqual(c['criteria']['1']['value'], b)
        self.assertTrue(c['collapses'])

    def test_criterion2_inclusive_double_boundary_and_failure(self):
        b = 2 * p.BOUND
        for sign in (-1, 1):
            c = p.criteria([0, 0, sign * b], [0])
            self.assertTrue(c['collapses'])
            c = p.criteria([0, 0, sign * math.nextafter(b, math.inf)], [0])
            self.assertEqual(c['failed_criteria'], ['2'])
            self.assertFalse(c['collapses'])

    def test_criterion3_signed_mean_inclusive_boundaries_and_failure(self):
        b = p.BOUND
        for sign in (-1, 1):
            self.assertTrue(p.criteria([0], [sign * b])['collapses'])
            c = p.criteria([0], [sign * math.nextafter(b, math.inf)])
            self.assertEqual(c['failed_criteria'], ['3'])
        self.assertTrue(p.criteria([0], [-5 * b, 5 * b])['collapses'])
        c = p.criteria([0], [])
        self.assertEqual(c['uncomputable_criteria'], ['3'])
        self.assertFalse(c['collapses'])

    def test_reader_pairing_uses_same_settings_excludes_r1_and_non_test(self):
        def cell(r, group='churn', set_='test', condition=0):
            return dict(r=r, group=group, set=set_, k=2,
                        settings=dict(reader_concurrency=r, condition=condition),
                        residuals=dict(M1=r / 100, M2=-r / 100))
        cells = {f'X3_r{r:02d}': cell(r) for r in (1, 4, 16)}
        cells.update({f'X1_r{r:02d}_d0200': cell(r, 'development', 'development') for r in (4, 16)})
        cells.update({f'C1_r{r:02d}_d0200_{c}': cell(r, 'compaction_' + c, condition=c)
                      for r in (4, 16) for c in ('on', 'off')})
        pairs = p.reader_pairs(cells)
        self.assertEqual(len(pairs), 3)
        self.assertEqual({(v['r4'], v['r16']) for v in pairs}, {
            ('X3_r04', 'X3_r16'), ('C1_r04_d0200_on', 'C1_r16_d0200_on'),
            ('C1_r04_d0200_off', 'C1_r16_d0200_off')})
        for pair in pairs:
            self.assertAlmostEqual(pair['differences']['M1'], .12)
            self.assertAlmostEqual(pair['differences']['M2'], -.12)
        cells['X3_r16']['settings']['condition'] = 1
        with self.assertRaisesRegex(ValueError, 'other settings'):
            p.reader_pairs(cells)


class HistogramTests(unittest.TestCase):
    def test_sum_counts_not_average_cdfs_zero_mass_retained(self):
        def hist(pairs):
            return dict(stats=dict(sample_count=sum(n for _, n in pairs)),
                        buckets=[dict(lower_ns=x, count=n) for x, n in pairs])
        hs = [hist([(0, 1), (10, 1), (20, 1)]), hist([(30, 2), (10, 3)])]
        pooled = p.pool_histograms(hs)
        self.assertEqual(pooled['sample_count'], 8)
        self.assertEqual(pooled['zero_count'], 1)
        self.assertEqual([b['lower_ns'] for b in pooled['buckets']], [0, 10, 20, 30])
        self.assertEqual([b['count'] for b in pooled['buckets']], [1, 4, 1, 2])
        self.assertEqual([b['cdf'] for b in pooled['buckets']], [.125, .625, .75, 1])
        self.assertEqual(pooled, p.pool_histograms(list(reversed(hs))))
        hs[0]['stats']['sample_count'] = 99
        with self.assertRaisesRegex(ValueError, 'sample_count'):
            p.pool_histograms(hs)


class IntegrationTests(unittest.TestCase):
    def test_macros_use_result_values_and_count_weighted_histogram(self):
        data = p.analyze(fixture())
        data['counts'].update(test=3, reader_pairs=7)
        data['cells'] = {
            'low': dict(set='test', k=1, residuals=dict(M1=-.1)),
            'edge': dict(set='test', k=1, residuals=dict(M1=.125)),
            'outside': dict(set='test', k=2, residuals=dict(M1=.126)),
            'excluded': dict(set='development', k=1, residuals=dict(M1=0)),
        }
        one = data['verdicts']['M1']['criteria']
        one['1'].update(value=.0126, bound=.125)
        one['2'].update(value=.3336, bound=.25)
        one['3']['value'] = -.0406
        data['verdicts']['M2']['criteria']['1']['value'] = .2346
        data['verdicts']['M2']['criteria']['2']['value'] = .4566
        data['spearman']['rho'] = -.876
        # Exact 3 ms is excluded; exact 90% chooses that bucket, not the next.
        data['commit_cdfs']['S1_d2000_f20_closed']['A']['phases']['flush_check'] = dict(
            sample_count=10, buckets=[dict(lower_ns=4_096_000, count=1),
                                      dict(lower_ns=3_000_000, count=3),
                                      dict(lower_ns=2_000_000, count=6)])
        self.assertEqual(p.macro_values(data), dict(
            PhaseTwoTestCells='3', PhaseTwoReaderPairs='7',
            PhaseTwoMOneMedian='0.013', PhaseTwoMOneMax='0.334', PhaseTwoMOneReader=r'\(-0.041\)',
            PhaseTwoMTwoMedian='0.235', PhaseTwoMTwoMax='0.457',
            PhaseTwoMarginBound='0.125', PhaseTwoMaxBound='0.250', PhaseTwoRho=r'\(-0.88\)',
            PhaseTwoWithinMargin='2', PhaseTwoKOneWithin='2', PhaseTwoKOneCells='2',
            PhaseTwoFlushLowShare='60', PhaseTwoFlushHighNote='3.0'))

    def test_fixture_selection_and_quantities(self):
        datasets = fixture()
        out = p.analyze(datasets)
        self.assertEqual(out['counts']['test'], 39)
        self.assertEqual(out['counts']['reader_pairs'], 19)
        self.assertEqual(out['counts']['k1_only'], 8)
        self.assertEqual(out['counts']['by_group'], dict(churn=3, compaction_off=6, compaction_on=6,
            development=8, intervention_inline=8, intervention_second=8, legacy=2, tablecount_writer=8))
        self.assertEqual(out['read_quantiles']['A']['mean_above_p95_trials'], 10)
        self.assertEqual(out['commit_cdfs'][p.SETTLED[0]]['A']['phases']['total']['sample_count'], 30)
        self.assertEqual(out, p.analyze({k: list(reversed(v)) for k, v in datasets.items()}))
        # No absent cell may silently fall out of the test set.
        datasets['tablecount'] = [t for t in datasets['tablecount'] if t['cell'] != 'T1_n004_r04_w1']
        with self.assertRaisesRegex(ValueError, 'expected 10 rounds'):
            p.analyze(datasets)

    def test_fixed_float_format_and_sorted_keys(self):
        self.assertEqual(p.fixed_json(dict(z=.1, a=1.0)), '{\n  "a": 1.000000000000,\n  "z": 0.100000000000\n}')
        with self.assertRaises(ValueError):
            p.fixed_json(math.nan)

    def test_cli_rerun_byte_identical_synthetic_fixture(self):
        # Keep temporary fixture writes inside the repository's writable root.
        base = Path(__file__).resolve().parent
        # Python 3.13 mkdir(mode=0o700), used by TemporaryDirectory, denies this
        # managed Windows sandbox access. Default directory permissions work.
        root = (base / ('phase2_fixture_' + uuid.uuid4().hex)).resolve()
        self.assertTrue(root.is_relative_to(base))
        root.mkdir()
        try:
            for source, rows in fixture().items():
                directory = root / source / 'trials'
                directory.mkdir(parents=True)
                for t in rows:
                    (directory / (t['trial_id'] + '_a1.json')).write_text(json.dumps(t), encoding='utf-8')
            output, paper = root / 'analysis_phase2', root / 'paper_figures'
            macros = root / 'generated/phase2.tex'
            cmd = [sys.executable, str(Path(p.__file__).resolve()), '--root', str(root),
                   '--output-dir', str(output), '--paper-figures', str(paper), '--macros-out', str(macros)]
            subprocess.run(cmd, check=True, capture_output=True, text=True)
            paths = sorted(output.iterdir()) + sorted(paper.iterdir()) + [macros]
            first = {path.relative_to(root).as_posix(): path.read_bytes() for path in paths}
            self.assertEqual(len(first), 12)
            values = p.macro_values(json.loads((output / 'phase2_results.json').read_text(encoding='utf-8')))
            expected = '% Generated by x_phase2_analyze.py; do not edit.\n'
            expected += ''.join('\\newcommand{\\' + name + '}{' + value + '}\n'
                                for name, value in sorted(values.items()))
            self.assertEqual(macros.read_bytes(), expected.encode('utf-8'))
            subprocess.run(cmd, check=True, capture_output=True, text=True)
            self.assertEqual(first, {path.relative_to(root).as_posix(): path.read_bytes() for path in paths})
            for name in p.FIGURES:
                self.assertEqual((output / (name + '.pdf')).read_bytes(), (paper / (name + '.pdf')).read_bytes())
        finally:
            # Resolve and verify the generated target before recursive cleanup.
            self.assertTrue(root.resolve().is_relative_to(base))
            shutil.rmtree(root)


if __name__ == '__main__':
    unittest.main()
