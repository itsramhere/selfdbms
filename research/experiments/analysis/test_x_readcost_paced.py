"""Paced manuscript arithmetic and presentation; no trials or inference run."""
import copy
from pathlib import Path
import unittest
from unittest.mock import patch

import x_readcost_paper_tables as p
import x_readcost_figures as f


def raw_trial(cell, arm, index, maximum, count=1830):
    return dict(trial_id=f'{cell}_{arm}_{index}', status='SUCCESS', cell=cell, arm=arm,
                result={'paced_load': {'write_service_nanos': {'max': maximum, 'sample_count': count},
                                       'write_service_late_nanos': {'max': 999_000_000}}})


def fixture():
    estimate = dict(geometric_mean_ratio=.920, t_ci=[.898, .942], classification='inconclusive',
                    holm_p=.35, median_ratio=.998, sd_log=.284)
    data = dict(counts={'successful': 360}, cells={}, trials={}, predictions=[{'hypothesis': 'H_P3', 'matches': False}],
                H_P1={}, H_P2={}, H_P3={}, H_P4={'W': {}, 'R': {}})
    trials = []
    for delay, low, high, reads in ((0, 183, 732, 12500), (200, 155, 619, 8000), (2000, 72, 286, 2850)):
        for fraction, rate in ((20, low), (80, high)):
            for mode in ('closed', 'open'):
                name = f'P1_d{delay:04d}_f{fraction}_{mode}'
                metrics = {metric: dict(mean=2_000_000, values=[100_000_000, 200_000_000],
                                        t_ci_95=[1_500_000, 2_500_000]) for metric in
                           ('write_response_p50', 'write_response_p99', 'write_service_p50', 'W', 'R')}
                metrics['achieved_fraction'] = dict(mean=.98, t_ci_95=[.97, .99])
                cell = dict(nominal=dict(read_io_delay_us=delay, paced_write_rate_per_sec=rate,
                                        open_loop_rate_per_sec=reads if mode == 'open' else 0,
                                        measurement_duration_ms=10000, reader_concurrency=16),
                            arms={a: copy.deepcopy(metrics) for a in p.ARMS},
                            saturated_trials={'A': 3}, equivalence={'W_A/AMIN': copy.deepcopy(estimate)},
                            ratios={m: {comparison: copy.deepcopy(estimate) for comparison in ('A/AMIN', 'AMIN/B')}
                                    for m in ('R', 'write_response_p50', 'write_service_p50', 'write_response_p99', 'read_response_p99')})
                data['cells'][name] = cell
                data['H_P2' if fraction == 20 else 'H_P3'][name] = copy.deepcopy(estimate)
                data['H_P4']['W'][name] = copy.deepcopy(estimate)
                if mode == 'closed':
                    data['H_P4']['R'][name] = copy.deepcopy(estimate)
                for arm in p.ARMS:
                    t = dict(cell=name, triplet=1, arm=arm, write_response_p99=100_000_000,
                             saturated=False, unissued_ticks=86)
                    data['trials'][name + arm] = t
                    trials.append(raw_trial(name, arm, 1, 53_000_000))
    for fraction in (20, 80):
        for mode in ('closed', 'open'):
            data['H_P1'][f'f{fraction}_{mode}'] = dict(L=123, p=.123, holm_p=.246)
    earlier = {'cells': {f'X1_r16_d{d:04d}': {'arms': {'A': {'W': {'mean': w}}}}
                         for d, w in ((200, 545.8), (2000, 111.0))}}
    probe = '\n'.join(f'rate 183 run {i}: n 1830 p50 1.0 p99 2.0 max {12.8 + i:.1f} ms;' for i in range(6))
    return data, trials, earlier, probe


class PacedTests(unittest.TestCase):
    def test_stall_band_uses_medians_of_trial_maxima_not_late_or_pooled_samples(self):
        rows = [raw_trial('cell', 'A', 1, 32_000_000, 1), raw_trial('cell', 'A', 2, 74_000_000, 10000),
                raw_trial('cell', 'AMIN', 1, 70_000_000), raw_trial('cell', 'AMIN', 2, 78_000_000)]
        self.assertEqual(p.paced_stall_summary(rows),
                         dict(cell_arm_median_ms=(53, 74), individual_trial_ms=(32, 78)))
        self.assertEqual(p.paced_stall_summary(list(reversed(rows))), p.paced_stall_summary(rows))
        self.assertAlmostEqual(p.ticks_during_stall(53, 183), 9.699)
        self.assertEqual(p.ticks_during_stall(1000, 183), 183)
        with self.assertRaises(ValueError):
            p.paced_stall_summary(rows + rows[:1])
        with self.assertRaises(ValueError):
            p.paced_stall_summary([raw_trial('cell', 'A', 1, 1, 0)])

    def test_tables_copy_frozen_ci_and_class_and_convert_nanoseconds(self):
        data, trials, earlier, probe = fixture()
        before = copy.deepcopy(data)
        with patch.object(p, 'write_body') as saved:
            p.generate_paced(data, trials, Path('.'), earlier, 1519, probe)
        self.assertEqual(data, before)
        directory, name, definitions = saved.call_args.args
        self.assertEqual(name, 'paced')
        text = '\n'.join(definitions)
        self.assertIn('0.920 [0.898, 0.942] & inconclusive & 2.000 & 2.000', text)
        self.assertIn(r'\newcommand{\PacedTotalTrials}{1,879}', text)
        self.assertIn(r'\newcommand{\PacedDesignFraction}{0.882}', text)
        self.assertIn(r'\newcommand{\PacedUnissuedAboveFlag}{86}', text)
        self.assertIn(r'\newcommand{\PacedLSTailMedians}{150}', text)
        self.assertIn(r'\gdef\TblPacedDescriptiveRows', text)

    def test_figure_closed_cells_and_service_units(self):
        data, _, _, _ = fixture()
        # A conspicuous open-cell change must not affect this closed-only plot.
        for name, cell in data['cells'].items():
            if name.endswith('_open'):
                cell['arms']['A']['write_service_p50']['mean'] = 999_000_000
        f.setup()
        with patch.object(f, 'save') as saved:
            f.fig_paced(data, Path('.'))
        fig = saved.call_args.args[0]
        self.addCleanup(f.plt.close, fig)
        self.assertEqual(len(fig.axes), 2)
        self.assertAlmostEqual(fig.get_size_inches()[0], 7.16)
        for container in fig.axes[1].containers:
            self.assertEqual(list(container.lines[0].get_ydata()), [2.])
        self.assertEqual(len(fig.axes[0].get_legend().get_texts()), 6)


if __name__ == '__main__':
    unittest.main()
