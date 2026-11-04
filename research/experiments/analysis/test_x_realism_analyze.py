"""Synthetic pre-data tests only; never launch a campaign or smoke."""
import copy
from datetime import datetime,timedelta,timezone
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
import uuid

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('x_realism_analyze',HERE/'x_realism_analyze.py')
a=importlib.util.module_from_spec(spec); spec.loader.exec_module(a)


def write_json(path,value):
    path.write_text(json.dumps(value),encoding='utf-8')


def write_ledger(root,rows):
    (root/'ledger.jsonl').write_text(''.join(json.dumps(e)+'\n' for e in rows),encoding='utf-8')


def accounting(n,late=0,failed=0):
    return dict(offered=n+late+failed,attempted=n+late+failed,completed_in_window=n,completed_late=late,
                failed=failed,abandoned=0,queued=0,outstanding_at_window_close=late)


def result(s):
    readers=s['reader_concurrency']
    # Exact q=2 for A, throughput gap A/Lease=.8, Lease=Snapshot.
    writes=800 if s['arm']=='A' else 1000
    reads=2*readers*writes
    v=dict(count=reads,mean_nanos=10000,p50_nanos=8000,p99_nanos=20000,max_nanos=30000)
    return dict(status='SUCCESS',trial_id=s['trial_id'],config=copy.deepcopy(s),git_revision='synthetic-revision',
                timestamps=dict(measured_window_ns=10000000000),
                operations=dict(read_ops_attempted=reads,read_ops_completed=reads,read_ops_completed_in_window=reads,read_ops_completed_late=0,
                                write_ops_attempted=writes+1,write_ops_completed=writes+1,write_ops_completed_in_window=writes,write_ops_completed_late=1,
                                validation_error_count=0,error_ops_count=0,write_conflict_aborts=0),
                throughput=dict(read_ops_per_sec=reads/10,write_ops_per_sec=writes/10),
                latency_read_nanos=dict(sample_count=reads,p50=10000,p95=20000,p99=30000),
                transaction_ledger=dict(reads=accounting(reads),writes=accounting(writes,1)),
                lsm_diagnostics=dict(initial_sstables=4,final_sstables=5,measured_compactions=0,compaction_errors=0,measured_flushes=1),
                sync_instrumentation=dict(read_critical_section_nanos={s['arm']:v},writer_lock_wait_nanos=dict(commit=dict(v,count=writes+1))))


def fixture(root):
    (root/'trials').mkdir()
    plan=dict(campaign_id='X-realism-v1',master_seed=a.SEED,settle_ms=20000,
              cells=[dict(id=c,spec=dict(a.nominal(c),cell=c)) for c in a.CELLS],trials=[])
    rows=[]; clock=datetime(2026,11,11,tzinfo=timezone.utc)
    for k in range(1,11):
        for i,c in enumerate(a.CELLS):
            for pos,arm in enumerate(a.settled.PERMUTATIONS[(k+i)%6]):
                tid=f'{c}_t{k:02d}_{arm}'
                h=hashlib.sha256(f'{a.SEED}|D_{c.split("_")[1]}|{k}'.encode()).digest()
                s=dict(a.nominal(c),cell=c,trial_id=tid,triplet=k,arm=arm,
                       seed=int.from_bytes(h[:8],'big',signed=True),order_position=pos+1,runlist_position=len(plan['trials'])+1)
                for key in ('diag_trace','inline_flush_check','lock_policy','persist_read_histogram','compaction_open_outside_lock'):
                    if not s[key]: del s[key]
                plan['trials'].append(s)
                t=dict(schema='i7.trial.v1',status='SUCCESS',trial_id=tid,block='X1',cell=c,triplet=k,arm=arm,
                       spec=copy.deepcopy(s),runtime=dict(exe_sha256='a'*64),result=result(s))
                write_json(root/'trials'/f'{tid}_a1.json',t)
                clock+=timedelta(seconds=20)
                rows.append(dict(event='STARTED',trial_id=tid,attempt=1,status='STARTED',exit_code=-1,start_utc=clock.isoformat()))
                clock+=timedelta(seconds=12.5)
                rows.append(dict(event='FINISHED',trial_id=tid,attempt=1,status='SUCCESS',exit_code=0,end_utc=clock.isoformat()))
    write_json(root/'plan.json',plan);write_ledger(root,rows)
    write_json(root/'platform.json',dict(campaign_id=plan['campaign_id'],runner_sha256='a'*64,
               plan_sha256=hashlib.sha256((root/'plan.json').read_bytes()).hexdigest(),git_head='synthetic-revision',goos='windows'))
    return plan,rows


class CampaignTests(unittest.TestCase):
    def setUp(self):
        self.root=HERE/('.realism-test-'+uuid.uuid4().hex);self.root.mkdir()
        self.addCleanup(shutil.rmtree,self.root)
        self.plan,self.rows=fixture(self.root)

    def artifact(self,c=None,arm='A',k=1):
        return self.root/'trials'/f'{c or a.CELLS[0]}_t{k:02d}_{arm}_a1.json'

    def test_all_predictions_descriptives_and_determinism(self):
        out=a.analyze(self.root)
        self.assertEqual(out,a.analyze(self.root))
        self.assertEqual(out['counts']['successful'],240)
        self.assertEqual(out['bootstrap_seed'],20261111)
        for h in ('H_D1','H_D2','H_D3','H_D4'): self.assertEqual(out[h]['verdict'],'supported',h)
        self.assertNotIn('verdict',out['H_X'])
        self.assertEqual(out['H_X']['cells_requiring_disclosure'],[])
        for cell in out['H_X']['cells'].values():
            self.assertEqual(cell['abort_share'],0)
            self.assertFalse(cell['exceeds_one_percent'])
        self.assertEqual(out['gaps']['min_ms'],20000)
        for c,cs in out['cells'].items():
            self.assertEqual(cs['complete_rounds'],list(range(1,11)))
            self.assertAlmostEqual(cs['S']['geometric_mean_ratio'],.8)
            self.assertEqual(cs['runs']['A']['q']['mean'],2)
            for metric in a.METRICS:self.assertIsNotNone(cs['runs']['A'][metric]['mean'])
        self.assertEqual(out['across_campaign_reference']['available'],False)
        self.assertIn('commit_wait_max',a.tables(out));json.dumps(out,allow_nan=False)
        for m in ('R','W'):
            self.assertEqual(len(out['H_D3'][m]['settings']),6)
            self.assertTrue(all(v['classification']=='equivalent' for v in out['H_D3'][m]['settings'].values()))
        self.assertEqual(len(out['H_D4']['settings']),4)
        self.assertEqual(len(out['direct_relative_buffered']),2)

    def test_earlier_hold_context_is_reported_without_pooling(self):
        oldtests=a.helper('test_x_readcost_analyze')
        earlier=self.root/'earlier';earlier.mkdir()
        oldtests.fixture(earlier)
        out=a.analyze(self.root,readcost_dir=earlier)
        context=out['across_campaign_reference']
        self.assertTrue(context['available'])
        self.assertEqual(len(context['cells']),8)
        self.assertEqual(context['cells']['X1_r04_d0200']['hold']['mean'],100)
        self.assertEqual(out['counts']['successful'],240)
        self.assertEqual(out['H_D1']['verdict'],'supported')

    def test_q_strict_bands(self):
        for band in ([1.9,2.1],[1.8,2.2]):
            for value,expected in ((band[0],False),(2,True),(band[1],False)):
                self.assertEqual(a.q_test(a.arithmetic([value]*10),band)['supported'],expected)
        self.assertIsNone(a.q_test(a.arithmetic([2,None]),[1.9,2.1])['supported'])

    def test_prediction_failures_and_conflict_kept(self):
        for k in range(1,11):
            p=self.artifact(arm='AMIN',k=k);t=a.load_json(p);r=t['result']
            r['throughput'].update(write_ops_per_sec=50,read_ops_per_sec=400)
            r['operations'].update(write_ops_attempted=502,write_ops_completed=501,write_ops_completed_in_window=500,
                                   read_ops_attempted=4000,read_ops_completed=4000,read_ops_completed_in_window=4000,
                                   error_ops_count=1,write_conflict_aborts=1)
            r['transaction_ledger']=dict(reads=accounting(4000),writes=accounting(500,1,1))
            r['latency_read_nanos']['sample_count']=4000
            write_json(p,t)
        out=a.analyze(self.root)
        for h in ('H_D1','H_D3','H_D4'):self.assertEqual(out[h]['verdict'],'not_supported',h)
        self.assertEqual(out['counts']['successful'],240)
        self.assertEqual(out['H_X']['total_aborts'],10)
        hx=out['H_X']['cells'][a.CELLS[0]]
        self.assertEqual(hx['attempted_commits'],23040)
        self.assertAlmostEqual(hx['abort_share'],10/23040)
        self.assertFalse(hx['exceeds_one_percent'])
        self.assertEqual(len(out['H_X']['attempts']),240)
        for k in range(1,11):
            p=self.artifact(c=a.CELLS[2],k=k);t=a.load_json(p)
            t['result']['operations'].update(read_ops_completed=8000,read_ops_completed_in_window=8000,read_ops_attempted=8000)
            t['result']['latency_read_nanos']['sample_count']=8000
            t['result']['throughput']['read_ops_per_sec']=800
            t['result']['transaction_ledger']['reads']=accounting(8000)
            write_json(p,t)
        self.assertEqual(a.analyze(self.root)['H_D2']['verdict'],'not_supported')

    def test_missing_drops_triplet_and_cross_cell_pairs(self):
        tid=self.plan['trials'][0]['trial_id'];write_ledger(self.root,[e for e in self.rows if e['trial_id']!=tid])
        with self.assertRaisesRegex(ValueError,'missing trials'):a.analyze(self.root)
        out=a.analyze(self.root,True)
        self.assertTrue(out['partial']);self.assertEqual(len(out['dropped_units']),1)
        self.assertEqual(out['cells'][a.CELLS[0]]['complete_rounds'],list(range(2,11)))
        self.assertEqual(out['H_D4']['settings']['r04_w2/w1']['rounds'],list(range(2,11)))
        self.assertEqual(out['cells'][a.CELLS[2]]['complete_rounds'],list(range(1,11)))
        self.assertEqual(out['H_D1']['verdict'],'inconclusive')

    def test_retry_latest_and_exhausted_failure(self):
        tid=self.plan['trials'][0]['trial_id'];p=self.artifact(arm=self.plan['trials'][0]['arm'])
        for e in self.rows:
            if e['trial_id']==tid and e['event']=='FINISHED':e.update(status='FAILED',exit_code=1)
        t=a.load_json(p);t['status']=t['result']['status']='FAILED';write_json(p,t)
        write_ledger(self.root,self.rows)
        self.assertEqual(a.analyze(self.root,True)['pending_retry'],[tid])
        retry=[dict(self.rows[0],attempt=2),dict(self.rows[1],attempt=2)]
        retry[0]['start_utc']=(datetime.fromisoformat(self.rows[-1]['end_utc'])+timedelta(seconds=20)).isoformat()
        retry[1]['end_utc']=(datetime.fromisoformat(retry[0]['start_utc'])+timedelta(seconds=12.5)).isoformat()
        write_json(self.root/'trials'/f'{tid}_a2.json',t);write_ledger(self.root,self.rows+retry)
        out=a.analyze(self.root)
        self.assertFalse(out['partial']);self.assertEqual(out['failed_trials'],[tid]);self.assertEqual(len(out['dropped_units']),1)
        write_ledger(self.root,self.rows+retry[:1]);self.assertNotIn(tid,a.analyze(self.root,True)['trials'])
        write_ledger(self.root,self.rows+[dict(retry[0],attempt=3)])
        with self.assertRaisesRegex(ValueError,'retry'):a.analyze(self.root,True)

    def test_zero_W_raw_retained_pseudocount_and_q_undefined(self):
        p=self.artifact();t=a.load_json(p);r=t['result']
        r['operations'].update(write_ops_attempted=0,write_ops_completed=0,write_ops_completed_in_window=0,write_ops_completed_late=0)
        r['throughput']['write_ops_per_sec']=0;r['transaction_ledger']['writes']=accounting(0)
        write_json(p,t);out=a.analyze(self.root)
        cs=out['cells'][a.CELLS[0]]
        self.assertEqual(cs['runs']['A']['W']['values'][0],0)
        self.assertEqual(cs['S']['zero_adjusted_W_rounds'],[1])
        self.assertEqual(cs['runs']['A']['q']['undefined_n'],1)
        self.assertIsNone(out['H_D1']['settings'][a.CELLS[0]+'/q']['supported'])
        v=a.ratio({1:{('a','A'):'a',('b','A'):'b'}},{'a':dict(W=0),'b':dict(W=100)},('a','A'),('b','A'),'W')
        self.assertAlmostEqual(v['geometric_mean_ratio'],.1/100.1)

    def test_hx_strict_one_percent_weighted_share_and_cell_disclosure(self):
        # Cell totals initially have 28030 attempts. Adding aborts increases
        # the denominator too; never average trial-level percentages.
        for k in range(1,11):
            p=self.artifact(k=k);t=a.load_json(p);r=t['result']
            count=29 if k<10 else 23  # 284 / 28314 > 1%
            r['operations'].update(write_conflict_aborts=count,error_ops_count=count,
                                   write_ops_attempted=801+count)
            r['transaction_ledger']['writes']=accounting(800,1,count)
            write_json(p,t)
        out=a.analyze(self.root);hx=out['H_X']['cells'][a.CELLS[0]]
        self.assertEqual(hx['conflict_aborts'],284)
        self.assertEqual(hx['attempted_commits'],28314)
        self.assertTrue(hx['exceeds_one_percent'])
        self.assertIn(a.CELLS[0],out['H_X']['cells_requiring_disclosure'])
        self.assertIn('REQUIRED: more than 1%',a.tables(out))
        self.assertEqual(out['counts']['successful'],240)
        self.assertEqual(out['trials'][a.CELLS[0]+'_t01_A']['W'],80)

    def test_hx_exact_boundary_zero_denominator_and_unknown_counts(self):
        # Use failed attempts for accounting-only fixtures; throughput analysis
        # never treats these as successful observations.
        tid=self.plan['trials'][0]['trial_id'];p=self.artifact(arm=self.plan['trials'][0]['arm'])
        t=a.load_json(p);t['status']=t['result']['status']='FAILED'
        for row in self.rows:
            if row['trial_id']==tid and row['event']=='FINISHED':row.update(status='FAILED',exit_code=1)
        retry=[dict(self.rows[0],attempt=2),dict(self.rows[1],attempt=2)]
        retry[0]['start_utc']=(datetime.fromisoformat(self.rows[-1]['end_utc'])+timedelta(seconds=20)).isoformat()
        retry[1]['end_utc']=(datetime.fromisoformat(retry[0]['start_utc'])+timedelta(seconds=12.5)).isoformat()
        # Other 29 trials contribute 27229 attempts. Two failure attempts add
        # 2771 attempts and 300 aborts, yielding exactly 300 / 30000 = 1%.
        t['result']['operations'].update(write_ops_attempted=2771,write_conflict_aborts=300)
        write_json(p,t)
        t2=copy.deepcopy(t);t2['result']['operations'].update(write_ops_attempted=0,write_conflict_aborts=0)
        write_json(self.root/'trials'/f'{tid}_a2.json',t2)
        write_ledger(self.root,self.rows+retry)
        out=a.analyze(self.root);hx=out['H_X']['cells'][a.CELLS[0]]
        self.assertEqual(hx['attempted_commits'],30000)
        self.assertEqual(hx['abort_share'],.01)
        self.assertFalse(hx['exceeds_one_percent'])
        retry_summary=next(v for v in out['H_X']['attempts'] if v['attempt']==2)
        self.assertIsNone(retry_summary['abort_share'])
        del t2['result']['operations']['write_ops_attempted']
        write_json(self.root/'trials'/f'{tid}_a2.json',t2)
        hx=a.analyze(self.root)['H_X']['cells'][a.CELLS[0]]
        self.assertIsNone(hx['abort_share']);self.assertIsNone(hx['exceeds_one_percent'])
        self.assertEqual(len(hx['unknown_attempts']),1)

    def test_hx_cell_with_no_attempted_commits(self):
        for k in range(1,11):
            for arm in a.ARMS:
                p=self.artifact(arm=arm,k=k);t=a.load_json(p);r=t['result']
                r['operations'].update(write_ops_attempted=0,write_ops_completed=0,
                                       write_ops_completed_in_window=0,write_ops_completed_late=0)
                r['throughput']['write_ops_per_sec']=0
                r['transaction_ledger']['writes']=accounting(0)
                write_json(p,t)
        hx=a.analyze(self.root)['H_X']['cells'][a.CELLS[0]]
        self.assertEqual(hx['attempted_commits'],0)
        self.assertEqual(hx['conflict_aborts'],0)
        self.assertIsNone(hx['abort_share'])
        self.assertIsNone(hx['exceeds_one_percent'])
        self.assertTrue(hx['reporting_complete'])

    def test_plan_spec_provenance_smoke_and_family_fail_closed(self):
        for key,value in (('seed',0),('writer_concurrency',3),('read_path','block'),('diag_trace',True),('lock_policy','readerpref')):
            bad=copy.deepcopy(self.plan);bad['trials'][0][key]=value
            with self.assertRaisesRegex(ValueError,key):a.validate_plan(bad)
        with self.assertRaisesRegex(ValueError,'smoke directory'):a.load_campaign(Path('realism-smoke'),True)
        p=self.artifact();t=a.load_json(p);t['runtime']['exe_sha256']='b'*64;write_json(p,t)
        with self.assertRaisesRegex(ValueError,'mixed runners'):a.analyze(self.root)
        family={str(i):dict(a.tost([0]*10,a.MARGIN),p_tost=.01) for i in range(6)}
        family['5']=a.tost([],a.MARGIN);a.adjust_family(family,'six',a.MARGIN)
        self.assertAlmostEqual(family['0']['holm_p'],.06)
        self.assertIsNone(family['5']['holm_p'])

    def test_cli_deterministic_and_replication_separate_not_pooled(self):
        rep=self.root/'linux';rep.mkdir();fixture(rep)
        platform=a.load_json(rep/'platform.json');platform.update(goos='linux',kernel='Linux 6.12 native',file_system='ext4',device='/dev/nvme0n1p2')
        write_json(rep/'platform.json',platform)
        out=a.report(self.root,replication_dir=rep)
        self.assertFalse(out['pooling']);self.assertEqual(out['primary']['counts']['successful'],240)
        self.assertEqual(out['replication']['counts']['successful'],240)
        text=a.tables(out);self.assertIn('| H_D1 | supported | supported |',text)
        dest=self.root/'output'
        cmd=[sys.executable,str(HERE/'x_realism_analyze.py'),'--data-dir',str(self.root),'--output-dir',str(dest),'--replication-dir',str(rep)]
        run=subprocess.run(cmd,capture_output=True,text=True);self.assertEqual(run.returncode,0,run.stderr)
        first={p.name:p.read_bytes() for p in dest.iterdir()}
        self.assertEqual(set(first),{'x_realism_results.json','x_realism_tables.md','x_realism_s_q.png'})
        run=subprocess.run(cmd,capture_output=True,text=True);self.assertEqual(run.returncode,0,run.stderr)
        self.assertEqual(first,{p.name:p.read_bytes() for p in dest.iterdir()})
        platform['kernel']='Linux 6.6 microsoft-standard-WSL2';write_json(rep/'platform.json',platform)
        with self.assertRaisesRegex(ValueError,'WSL2'):a.report(self.root,replication_dir=rep)
        platform.update(kernel='Linux native',plan_sha256='b'*64);write_json(rep/'platform.json',platform)
        with self.assertRaisesRegex(ValueError,'platform campaign/plan'):a.report(self.root,replication_dir=rep)


if __name__=='__main__':unittest.main()
