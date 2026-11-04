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
    def test_settled_interval_reexpresses_frozen_log_width_without_mutating(self):
        from scipy.stats import t
        half = float(t.ppf(.975, 3)) * .1
        source = dict(level=.95, n=4, mean_log=math.log(2), geometric_mean_ratio=2,
                      t_ci_log=[math.log(2) - half, math.log(2) + half])
        source['t_ci'] = [math.exp(v) for v in source['t_ci_log']]
        original = copy.deepcopy(source)
        result = p.settled_ratio90(source)
        self.assertEqual(source, original)
        self.assertEqual(result['geometric_mean_ratio'], 2)
        self.assertAlmostEqual(result['t_ci'][0], math.exp(math.log(2) - float(t.ppf(.95, 3)) * .1))
        self.assertIs(p.settled_ratio90(result), result)
        with self.assertRaisesRegex(ValueError, 'disagree'):
            p.settled_ratio90(dict(source, geometric_mean_ratio=3))

    def test_generate_settled_small_frozen_fixture_and_raw_validation(self):
        # Two rounds per arm; frozen estimates intentionally differ from a
        # ratio of means, so this catches accidental inferential recomputation.
        eq = dict(level=.9, geometric_mean_ratio=1.234, t_ci=[1.1, 1.4], sd_log=.2, holm_p=.03)
        cells, raw = {}, []
        names = [f'S1_d{d:04d}_f20_{m}' for d in (0, 200, 2000) for m in ('closed', 'open')]
        names += ['S2_d0200_w700_closed', 'S2_d0200_w700_open', 'S3_d0000_f80_closed']
        for c in names:
            arms = {}
            for a in p.ARMS:
                vals = [2e6, 4e6] if a == 'A' else [1e6, 2e6]
                arms[a] = {f'phase_{k}_p50': dict(values=vals, mean=sum(vals) / 2)
                           for k in ('lock_wait', 'flush_check', 'total')}
                arms[a].update({k: dict(values=vals) for k in ('write_response_p50', 'write_service_p50')})
                for i, v in enumerate(vals, 1):
                    raw.append(dict(trial_id=f'{c}_t{i}_{a}', cell=c, arm=a, triplet=i, status='SUCCESS',
                        result=dict(throughput=dict(read_ops_per_sec=10000 + i),
                            paced_load={k: dict(p50=v, p99=v * 4, max=v * 10) for k in ('write_response_nanos', 'write_service_nanos')},
                            stall_diagnostics=dict(phase_histograms={k: dict(stats=dict(p50=v, max=v * 2))
                                for k in ('lock_wait', 'flush_check', 'total', 'wal_sync')},
                                commits=[dict(index=1, WalWriteDoneNs=100, WalSyncDoneNs=100 + v)]))))
            cells[c] = dict(complete_rounds=[1, 2], nominal=dict(paced_write_rate_per_sec=72), arms=arms,
                            ratios={m: {'A/AMIN': eq} for m in ('write_response_p50', 'write_response_p99')})
        compaction = []
        for a in ('AMIN', 'B'):
            for c, target in (('C1_r16_d0200_on', compaction), ('R_C1_r16_d0200_on', raw)):
                target.append(dict(trial_id=c + '_' + a, cell=c, arm=a, status='SUCCESS',
                    result=dict(sync_instrumentation=dict(writer_lock_wait_nanos=dict(commit=dict(p99_nanos=123000))))))
        data = dict(counts=dict(successful=len(raw)), cells=cells, gaps=dict(min_ms=20003, max_ms=20030),
            plan_sha256='abcdef123456789', runner_sha256='987654321abcfff', master_seed=7,
            H_M1={m: dict(L=12, p=.02, holm_p=.04) for m in ('closed', 'open')},
            H_M2={c: eq for c in names if c.startswith('S1_') and 'd0000' not in c},
            H_M3=dict(bands={a + '_' + k: dict(mean=2000000, t_ci_95=[1000000, 3000000])
                            for a in ('A', 'AMIN') for k in ('lock_wait', 'flush_check')}),
            H_W={c: eq for c in names if c.startswith('S2_')}, H_R=dict(R=eq, W=eq),
            H_S=dict(trials={'one': dict(median_largest_fsync_ns=5000000)}),
            H_C=[dict(cell='R_C1_r16_d0200_on', metric='commit_wait_p99', original=eq, new=eq,
                      published_class='inconclusive', classification_contradicted=False,
                      comparison=dict(classification='inconclusive'))],
            across_campaign_paced=[dict(cell=c, metric='write_response_p99', pair='A/AMIN', original=eq, new=eq)
                                   for c in names if 'd2000' in c])
        paced = [dict(t, cell=t['cell'].replace('S1_', 'P1_')) for t in raw if t['cell'].startswith('S1_')]
        earlier = dict(cells={'X1_r16_d0200': dict(arms={'A': dict(W=dict(mean=500))})})
        original = copy.deepcopy(data)
        with patch.object(p, 'write_body') as saved, patch.object(p, 'settled_stall_facts', return_value={}), \
                patch.object(p, 'settled_check_facts', return_value={}):
            facts = p.generate_settled(data, raw, Path('.'), earlier, 100, paced, compaction, {}, {}, {}, '')
            first = saved.call_args.args[2]
            self.assertEqual(facts['SettledTrialCount'], '56')
            self.assertEqual(facts['SettledTotalTrials'], '156')
            self.assertEqual(facts['SettledMedianZeroClosed'], '1.234 [1.100, 1.400]')
            self.assertEqual(facts['SettledPublishedClasses'], '1')
            self.assertIn('3.000 & 3.000 & 1.500 & 1.500', facts['TblSettledRows'])
            self.assertEqual(len(facts['TblSettledRows'].splitlines()), 10)
            self.assertEqual(first, sorted(first, key=lambda row: row.split('\\')[2].split('}')[0].split('{')[0]))
            p.generate_settled(data, list(reversed(raw)), Path('.'), earlier, 100, paced, compaction, {}, {}, {}, '')
            self.assertEqual(first, saved.call_args.args[2])
            self.assertEqual(data, original)
            damaged = copy.deepcopy(raw)
            damaged[0]['result']['stall_diagnostics']['phase_histograms']['total']['stats']['p50'] += 1
            with self.assertRaisesRegex(ValueError, 'phase differs'):
                p.generate_settled(data, damaged, Path('.'), earlier, 100, paced, compaction, {}, {}, {}, '')

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
