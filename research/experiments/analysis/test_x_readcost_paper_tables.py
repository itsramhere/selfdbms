"""Small arithmetic checks; no engine execution or inferential recomputation."""
import copy
import unittest

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
