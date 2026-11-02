package harness2

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"owndbms/lsmengine"
)

func TestCompactionCollector(t *testing.T) {
	c := NewSyncHistogramCollector()
	for _, site := range []lsmengine.LockSite{lsmengine.LockSiteCompactionStart, lsmengine.LockSiteCompactionPublish, lsmengine.LockSiteCompactionSnapshotPublish} {
		c.OnWriterLockWait(site, 100, 0)
		c.OnPublication(site, 200, 0)
	}
	c.OnTableRetired()
	c.OnTableRetired()
	c.OnTableRemoved(400)
	c.OnCompaction(800, 2)
	s := c.Summary()
	for _, site := range []string{"compaction_start", "compaction_publish", "compaction_snapshot_publish"} {
		for _, st := range []SyncStats{s.PublicationNanos[site], s.WriterLockWaitNanos[site]} {
			if st.Count != 1 || st.MeanNanos <= 0 || st.P99Nanos == nil || *st.P99Nanos <= 0 || st.MaxNanos <= 0 {
				t.Fatal(st)
			}
		}
	}
	if s.CompactionWallNanos.Count != 1 || s.CompactionWallNanos.MeanNanos != 800 || s.CompactionWallNanos.P99Nanos == nil || s.CompactionInputs.Mean != 2 || s.CompactionInputs.Max != 2 || s.RetirementLagNanos.Count != 1 || s.RetiredTablesHighWater != 2 {
		t.Fatal(s)
	}
	c.Reset()
	s = c.Summary()
	if s.CompactionWallNanos != nil || s.CompactionInputs != nil || s.RetirementLagNanos != nil || s.RetiredTablesHighWater != 1 {
		t.Fatal("reset", s)
	}
	c.OnTableRemoved(500)
	if c.Summary().RetirementLagNanos.Count != 1 {
		t.Fatal("drain not included")
	}
	empty := NewSyncCollector().Summary()
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "compaction_") || strings.Contains(string(b), "retirement_") || strings.Contains(string(b), "retired_tables") {
		t.Fatal("empty metrics not omitted", string(b))
	}
}

func TestCompactionFaultPolicy(t *testing.T) {
	var c compactionCounts
	c.record(nil, false, true, false)
	c.record(nil, true, true, false)
	c.record(nil, true, false, true)
	c.record(lsmengine.ErrClosed, false, false, true)
	c.record(errors.New("compaction aborted: engine is closed"), false, false, true)
	if c.successes != 2 || c.noOps != 1 || c.inWindow != 1 || c.errors != 0 {
		t.Fatal(c)
	}
	c.record(errors.New("compaction IO fault"), false, false, true)
	c.record(lsmengine.ErrClosed, false, true, false)
	if c.errors != 2 {
		t.Fatal("non-shutdown errors not counted", c)
	}
	for _, block := range []string{"X1", "X2", "X3", "E1", "E2", "D1"} {
		res := &TrialResult{Status: "SUCCESS"}
		spec := Spec{Block: block}
		c.outcome(res, spec)
		if (res.Status == "FAILED") != spec.IsX() {
			t.Fatal("fault policy", block, res)
		}
		if spec.IsX() && res.Error != "compaction: compaction IO fault" {
			t.Fatal(res.Error)
		}
	}
}
