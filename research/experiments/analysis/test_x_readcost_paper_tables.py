"""Small arithmetic checks; no engine execution or inferential recomputation."""
import copy
import math
from pathlib import Path
import unittest
from unittest.mock import patch

import x_readcost_paper_tables as p


def trial(arm, index, reads, commits, wait_ns):
    return dict(status='SUCCESS', trial_id=f'X1_r04_d0000_t{index}_{arm}',
                cell='X1_r04_d0000', arm=arm, triplet=index,
                spec=dict(reader_concurrency=4), result=dict(
                    operations=dict(read_ops_completed_in_window=reads,
                                    write_ops_completed_in_window=commits,
                                    read_ops_completed_late=999999,
                                    write_ops_completed_late=999999),
                    timestamps=dict(measured_window_ns=1_000_000_000),
                    lsm_diagnostics=dict(measured_flushes=3),
                    sync_instrumentation=dict(writer_lock_wait_nanos=dict(commit=dict(mean_nanos=wait_ns)))))


class MechanismTests(unittest.TestCase):
    def test_summary_percent_and_sensitivity_boundaries(self):
        self.assertEqual(p.percent_change(.9261103918684351), r'-7.4\%')
        self.assertEqual(p.percent_change(1.452), r'+45.2\%')
        self.assertEqual(p.percent_change(.0016963173749270188), r'-99.8\%')
        family = {str(i): {'t_ci': ci} for i, ci in enumerate(
            [[.95, 1 / .95], [.94999, 1.0], [1.0, 1 / .95 + .00001], [.98, 1.01]])}
        self.assertEqual(p.sensitivity_count(family), 2)

    def test_coverage_keeps_anchors_and_sums_compactions(self):
        rows = [trial('A', 1, 1, 1, 1), trial('B', 2, 1, 1, 1)]
        for t, f, c in zip(rows, (0, 7), (2, 3)):
            t['result']['lsm_diagnostics'].update(measured_flushes=f, measured_compactions=c)
        rows[0]['cell'] = 'ANCHOR'
        self.assertEqual(p.coverage(rows), (2, 3.5, 7, 5))

    def test_microbench_uses_mean_of_counts_and_ignores_compile_checks(self):
        rows = [dict(benchmark='BenchmarkLifecycle/AMIN/N4/g1-12', ns_per_op=n,
                     allocs_per_op=0, iterations=i, compile_check_only=check)
                for n, i, check in [(20, 100, False), (30, 1, False), (1000, 1, True)]]
        self.assertEqual(p.microbench_means({'microbenchmark': {'measurements': rows}})
                         ['BenchmarkLifecycle/AMIN/N4/g1']['ns_per_op'], 25)

    def test_new_tables_copy_intervals_classes_and_scale_units(self):
        eq = dict(geometric_mean_ratio=.975, t_ci=[.95, 1.01], classification='equivalent')
        arms = {a: dict(read_cs_mean={'mean': 1200},
                        commit_wait_p99={'mean': 22000},
                        retirement_lag_mean_nanos={'mean': 5000000}) for a in p.ARMS}
        cell = dict(nominal=dict(initial_sstable_count=4, reader_concurrency=4, writer_concurrency=0),
                    arms=arms, equivalence={'R': eq, 'W': eq},
                    ratios={m: {'A/AMIN': eq} for m in ('W', 'R')})
        with patch.object(p, 'write_body') as saved:
            p.generate_compaction({'cells': {'C1_r04_d0200_on': cell}}, Path('.'))
        text = '\n'.join(saved.call_args_list[0].args[2])
        self.assertIn('0.975 [0.950, 1.010] & equivalent', text)
        self.assertIn('22.0 & 22.0 & 22.0 & 5.00 & 5.00 & 5.00', text)
        self.assertEqual(p.dose('C1_r04_d0200_on'), r'200\,\(\mu\)s')

    def test_tablecount_units_no_writer_and_benchmark_means(self):
        eq = dict(geometric_mean_ratio=.7, t_ci=[.68, .72], classification='not_equivalent')
        cell = dict(nominal=dict(initial_sstable_count=256, reader_concurrency=16, writer_concurrency=0),
                    equivalence={'R': eq}, arms={a: {'read_cs_mean': {'mean': 8500}}
                                                  for a in p.ARMS})
        measurements = []
        for n in (4, 16, 64, 256):
            for a in ('AMIN', 'B'):
                names = [f'BenchmarkLifecycle/{a}/N{n}/g{g}' for g in (1, 16)]
                names += [f'BenchmarkLifecyclePublication/{a}/N{n}']
                for name in names:
                    measurements += [dict(benchmark=name + '-12', ns_per_op=x,
                                          allocs_per_op=3, compile_check_only=False)
                                     for x in (10, 20, 30)]
        with patch.object(p, 'write_body') as saved:
            p.generate_tablecount(dict(cells={'cell': cell},
                                       microbenchmark={'measurements': measurements}), Path('.'))
        self.assertIn('0.700 [0.680, 0.720] & not equivalent & -- & -- & 8.500',
                      saved.call_args_list[0].args[2][0])
        self.assertEqual(saved.call_args_list[1].args[2][0],
                         '4 & 20.0 & 20.0 & 20.0 & 20.0 & 20.0 & 20.0 & 3 & 3' + r' \\')

    def test_paired_geometric_p99_ratio_is_not_arithmetic_aggregation(self):
        # Ratios 2 and 4.5 have geometric mean 3, arithmetic mean 3.25;
        # the ratio of arithmetic means is 11/3. None may be interchanged.
        self.assertAlmostEqual(p.descriptive_geometric_ratio([(2, 1), (9, 2)]), 3)
        for pairs in ([], [(0, 1)], [(1, -1)], [(math.inf, 1)]):
            with self.assertRaises(ValueError):
                p.descriptive_geometric_ratio(pairs)

    def test_per_trial_mean_window_counts_and_model(self):
        rows = [trial('A', 1, 800, 100, 2_000_000),
                trial('A', 2, 600, 50, 4_000_000),
                trial('AMIN', 1, 400, 200, 1),
                trial('AMIN', 2, 400, 100, 1)]
        # Anchors must not be pooled into the cell; late counts must not enter q.
        anchor = copy.deepcopy(rows[0])
        anchor.update(cell='ANCHOR', trial_id='ANCHOR')
        out = p.mechanism(rows + [anchor])['cells']['X1_r04_d0000']
        self.assertEqual(out['arms']['A']['reads_per_reader_per_commit']['mean'], 2.5)
        self.assertEqual(out['arms']['A']['cycle_ms']['mean'], 15)
        self.assertEqual(out['cycle_model']['predicted_ms'], 13.5)
        self.assertAlmostEqual(out['cycle_model']['error_percent'], -10)
        self.assertEqual(p.mechanism(list(reversed(rows))), p.mechanism(rows))

    def test_zero_and_duplicate_are_errors(self):
        with self.assertRaisesRegex(ValueError, 'zero in-window'):
            p.mechanism([trial('A', 1, 1, 0, 1)])
        row = trial('A', 1, 1, 1, 1)
        with self.assertRaisesRegex(ValueError, 'duplicate'):
            p.mechanism([row, row])


if __name__ == '__main__':
    unittest.main()
