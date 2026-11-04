"""Small synthetic raw/results fixture; no campaign execution."""
import copy
import hashlib
import json
import math
from pathlib import Path
import statistics as st
import shutil
import unittest
import uuid

import x_followup_paper_outputs as g


def fixture(directory):
    rows, envelopes = [], []
    for index, cell in enumerate(g.F1 + (g.F2, g.F3, g.F4)):
        for arm in (('A',) + g.ARMS if cell == g.F4 else g.ARMS):
            for rnd in (1, 2, 3):
                tid = f'{cell}_t{rnd:02d}_{arm}'
                free = .96 if cell == g.F3 else .8 - .13*index - .005*rnd
                read = 1000*free*(1 + rnd/100) * (1.01 if arm == 'AMIN' else 1)
                row = dict(cell=cell, arm=arm, round=rnd, R=read, W=183, flushes=int(cell == g.F2))
                spec = dict(trial_id=tid, cell=cell, arm=arm, triplet=rnd)
                result = dict(spec, status='SUCCESS', throughput=dict(read_ops_per_sec=read, write_ops_per_sec=183),
                              lsm_diagnostics=dict(measured_flushes=row['flushes']))
                if cell == g.F4:
                    hist = [dict(lower_ns=1000, count=5), dict(lower_ns=200000, count=1),
                            dict(lower_ns=600000, count=4)]
                    result.update(read_latency_histogram=hist, latency_read_nanos=dict(sample_count=10))
                    row['histogram'] = hist
                else:
                    hold = 1-free
                    row.update(hold_share=hold, free_share=free)
                    result.update(timestamps=dict(measured_window_ns=1000000000),
                        stall_diagnostics=dict(commit_count=10, phase_histograms={p: dict(stats=dict(
                            sample_count=10, mean=hold*1000000000/40))
                            for p in ('validate', 'wal_write', 'wal_sync', 'apply')}))
                envelopes.append(dict(spec, status='SUCCESS', spec=spec, result=result))
                rows.append(dict(trial_id=tid, **row))
    cells = {}
    for cell in g.F1 + (g.F2, g.F3, g.F4):
        arms = {}
        for arm in (('A',) + g.ARMS if cell == g.F4 else g.ARMS):
            selected = [r for r in rows if r['cell'] == cell and r['arm'] == arm]
            metrics = {m: dict(mean=st.mean(r[m] for r in selected)) for m in ('R', 'W', 'flushes')}
            if cell != g.F4:
                metrics.update({m: dict(mean=st.mean(r[m] for r in selected)) for m in ('free_share', 'hold_share')})
            else:
                metrics.update({region+'_share': dict(mean=v, t_ci=[v-.01, v+.01])
                                for region, v in zip(('fast', 'valley', 'blocked'), (.5, .1, .4))})
            arms[arm] = metrics
        cells[cell] = dict(arms=arms)
    data = dict(campaign_id='X-followup-v1', partial=False, trials={r['trial_id']: r for r in rows}, cells=cells,
                counts=dict(planned=len(rows), successful=len(rows)), master_seed=20261108, settle_ms=20000,
                git_revision='fc7998c', plan_sha256='394675b73698', runner_sha256='6d065832f639',
                gaps=dict(min_ms=20000, max_ms=22462),
                H_F1=dict(arms={a: dict(mean=.77, t_ci=[.73, .81]) for a in g.ARMS}),
                H_F2={a: dict(L=299, holm_p=1e-12) for a in g.ARMS},
                H_F3={c: dict(holm_p=.04) for c in g.F1[:3]},
                capacity={a: dict(F3_median_R=1000) for a in g.ARMS})
    trials = directory/'trials'
    trials.mkdir()
    for e in envelopes:
        (trials/(e['trial_id']+'_a1.json')).write_text(json.dumps(e), encoding='utf-8')
    results = directory/'results.json'
    results.write_text(json.dumps(data), encoding='utf-8')
    return results, data, rows


class FollowupPaperTests(unittest.TestCase):
    def test_paired_difference_uses_round_and_covariance(self):
        rows = []
        for arm in g.ARMS:
            for rnd, read, free in ((1, 2, 2), (2, 8, 4), (3, 12, 3)):
                rows += [dict(cell=g.F1[-1], arm=arm, round=rnd, R=10, free_share=.1),
                         dict(cell=g.F3, arm=arm, round=rnd, R=10*read, free_share=.1*free)]
        result = g.paired_gains(list(reversed(rows)))['AMIN']
        expected = [0, math.log(2), math.log(4)]
        mean, ci = g.mean_ci(expected)
        self.assertAlmostEqual(result['mean_difference'], mean)
        self.assertAlmostEqual(result['relative_gain'], 2)
        for actual, wanted in zip(result['difference_ci'], ci):
            self.assertAlmostEqual(actual, wanted)
        self.assertEqual([r['round'] for r in result['rounds']], [1, 2, 3])
        with self.assertRaisesRegex(ValueError, 'incomplete'):
            g.paired_gains(rows[:-1])

    def test_small_fixture_outputs_are_deterministic_and_raw_checked(self):
        directory = (g.ROOT/('.followup-paper-test-'+uuid.uuid4().hex)).resolve()
        self.assertEqual(directory.parent, g.ROOT.resolve())
        directory.mkdir()
        self.addCleanup(shutil.rmtree, directory)
        if directory.exists():
            results, data, rows = fixture(directory)
            macros, figures = directory/'followup.tex', directory/'figures'
            values = g.run(results, directory, macros, figures)
            self.assertEqual(values['FollowRatioLow'], '1.010 [1.010, 1.010]')
            self.assertEqual(values['FollowSlowMedianA'], '600')
            self.assertEqual(g.pool_histograms(rows, 'A'), [(1000, 15), (200000, 3), (600000, 12)])
            paths = [macros] + sorted(figures.glob('*'))
            hashes = [hashlib.sha256(p.read_bytes()).hexdigest() for p in paths]
            g.run(results, directory, macros, figures)
            self.assertEqual(hashes, [hashlib.sha256(p.read_bytes()).hexdigest() for p in paths])
            bad = copy.deepcopy(data)
            next(iter(bad['trials'].values()))['R'] *= 2
            with self.assertRaisesRegex(ValueError, 'raw/results mismatch'):
                g.load_raw(directory, bad)


if __name__ == '__main__':
    unittest.main()
