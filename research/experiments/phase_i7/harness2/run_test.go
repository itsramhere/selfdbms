package harness2

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"owndbms/research/experiments/harness"
)

func testEnv() Environment {
	return Environment{
		OS:             "windows",
		Kernel:         "10.0.26200",
		CPUModel:       "test",
		PhysicalCores:  6,
		LogicalCores:   12,
		GoVersion:      "go-test",
		GOMAXPROCS:     6,
		FilesystemType: "NTFS",
	}
}

func miniSpec(t *testing.T, block, cell, arm string) Spec {
	return Spec{
		Schema:                 "i7.trial-spec.v1",
		TrialID:                "mini_" + block + "_" + arm,
		Block:                  block,
		Cell:                   cell,
		Triplet:                1,
		Arm:                    arm,
		Seed:                   424242,
		WorkloadID:             "MINI",
		ReaderConcurrency:      2,
		WriterConcurrency:      1,
		MeasurementDurationMs:  200,
		WarmupDurationMs:       20,
		CooldownDurationMs:     0,
		KeyspaceCardinality:    60,
		InitialSSTableCount:    2,
		InitialKeysPerSSTable:  30,
		TargetReadHitRatio:     0.95,
		MemtableThresholdBytes: 64 << 20,
		DataDir:                t.TempDir(),
		ExpectedReaderSetSize:  2,
	}
}

// H-11: the logical request stream is a pure function of (seed, index), so every
// arm of a triplet issues the same request at the same logical position.
func TestKeyStreamIsSharedAcrossArms(t *testing.T) {
	a := NewKeyStream(999, 1000, 0.95)
	b := NewKeyStream(999, 1000, 0.95)
	for i := int64(0); i < 100000; i++ {
		ka, ha := a.At(i)
		kb, hb := b.At(i)
		if ka != kb || ha != hb {
			t.Fatalf("stream diverged at %d: %q/%v vs %q/%v", i, ka, ha, kb, hb)
		}
	}
	c := NewKeyStream(1000, 1000, 0.95)
	same := 0
	for i := int64(0); i < 1000; i++ {
		ka, _ := a.At(i)
		kc, _ := c.At(i)
		if ka == kc {
			same++
		}
	}
	if same > 5 {
		t.Fatalf("different seeds produced %d/1000 identical keys", same)
	}
}

func TestKeyStreamAbsentKeysAreOutsideKeyspace(t *testing.T) {
	ks := NewKeyStream(7, 500, 0.0) // every draw must be a miss
	misses := 0
	for i := int64(0); i < 5000; i++ {
		key, hit := ks.At(i)
		if hit {
			t.Fatalf("hit with hitRatio=0 at %d", i)
		}
		var idx int
		if _, err := sscanfKey(key, &idx); err != nil {
			t.Fatalf("unparseable key %q", key)
		}
		if idx <= 500 {
			t.Fatalf("absent key index %d is inside the keyspace (500)", idx)
		}
		misses++
	}
	if misses != 5000 {
		t.Fatalf("expected 5000 misses, got %d", misses)
	}
}

func sscanfKey(key string, out *int) (int, error) {
	const prefix = "user_key_"
	if !strings.HasPrefix(key, prefix) {
		return 0, fmt.Errorf("bad key format %q", key)
	}
	v, err := strconv.Atoi(key[len(prefix):])
	if err != nil {
		return 0, err
	}
	*out = v
	return 1, nil
}

func writeMarker(dir string) error {
	return os.WriteFile(filepath.Join(dir, "stale_attempt.sst"), []byte("x"), 0o644)
}

func TestStatsPercentilesAndSufficiency(t *testing.T) {
	st := Stats(nil)
	if st.Count != 0 || st.P95Nanos != nil {
		t.Fatal("empty stats must be zero")
	}
	small := make([]int64, 10)
	for i := range small {
		small[i] = int64(i + 1)
	}
	st = Stats(small)
	if st.P95Nanos != nil {
		t.Fatal("p95 must be nil below 20 samples")
	}
	big := make([]int64, 1000)
	for i := range big {
		big[i] = int64(i + 1)
	}
	st = Stats(big)
	if st.P95Nanos == nil || st.P99Nanos == nil || st.P999Nanos == nil {
		t.Fatal("percentiles must be present at n=1000")
	}
	if *st.P95Nanos != 950 || *st.P99Nanos != 990 || *st.P999Nanos != 999 {
		t.Fatalf("nearest-rank mismatch: p95=%d p99=%d p999=%d", *st.P95Nanos, *st.P99Nanos, *st.P999Nanos)
	}
}

func TestShardMergePreservesSamples(t *testing.T) {
	s1 := NewShard(4, true, true)
	s2 := NewShard(4, true, true)
	for i := 0; i < 4; i++ {
		s1.RecordService(int64(i))
		s1.RecordResponse(int64(i * 2))
		s1.RecordIssue(int64(i), int64(i), int64(i+1))
	}
	for i := 4; i < 8; i++ {
		s2.RecordService(int64(i))
	}
	all := MergeSamples([]*Shard{s1, s2})
	if len(all) != 8 {
		t.Fatalf("expected 8 merged samples, got %d", len(all))
	}
	if len(MergeResponses([]*Shard{s1, s2})) != 4 {
		t.Fatal("response merge lost samples")
	}
	if len(MergeIssues([]*Shard{s1, s2})) != 4 {
		t.Fatal("issue merge lost samples")
	}
	if s1.Dropped != 0 || s2.Dropped != 0 {
		t.Fatal("unexpected drops")
	}
	// Capacity overflow is counted, not silently truncated.
	for i := 0; i < 100; i++ {
		s1.RecordService(1)
	}
	if s1.Dropped == 0 {
		t.Fatal("overflow must be counted in Dropped")
	}
}

func TestScheduleDeterministicAndRateAccurate(t *testing.T) {
	d := 200 * time.Millisecond
	a := BuildSchedule(11, 500, 1000, 0.95, d)
	b := BuildSchedule(11, 500, 1000, 0.95, d)
	if len(a) != len(b) {
		t.Fatalf("schedule length differs between identical seeds: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("schedule differs at %d", i)
		}
	}
	if len(a) < 50 || len(a) > 150 {
		t.Fatalf("expected ~100 arrivals at 500/s over 200ms, got %d", len(a))
	}
	for i := 1; i < len(a); i++ {
		if a[i].IntendedNs <= a[i-1].IntendedNs {
			t.Fatalf("arrivals not strictly increasing at %d", i)
		}
	}
	if a[len(a)-1].IntendedNs >= d.Nanoseconds() {
		t.Fatal("arrival scheduled at or beyond the window end")
	}
}

func TestConditionEventsBlockRatio(t *testing.T) {
	events := []EventRecord{{Index: 0, BeginNs: 1_000_000, EndNs: 21_000_000, HoldNs: 20_000_000}}
	issues := []Issue{
		{IntendedNs: 0, IssuedNs: 0, CompleteNs: 19_000_000},        // overlaps, waited out
		{IntendedNs: 0, IssuedNs: 25_000_000, CompleteNs: 30_000_000}, // starts after the window
		{IntendedNs: 0, IssuedNs: 2_000_000, CompleteNs: 4_000_000},  // inside, short
	}
	// delta = 0: the 30 ms read is outside [1 ms, 21 ms] and must be excluded.
	got := ConditionEvents(events, issues, 0)
	if len(got) != 1 {
		t.Fatalf("expected one conditioned record, got %d", len(got))
	}
	if got[0].OverlapReads != 2 {
		t.Fatalf("expected 2 overlapping reads, got %d", got[0].OverlapReads)
	}
	if got[0].MaxResponseNs != 19_000_000 {
		t.Fatalf("max response mismatch: %d", got[0].MaxResponseNs)
	}
	if got[0].BlockRatio < 0.9 || got[0].BlockRatio > 1.0 {
		t.Fatalf("block ratio should be ~0.95, got %f", got[0].BlockRatio)
	}
}

func TestResolveModeRejectsUnknownCombinations(t *testing.T) {
	cases := []Spec{
		{Block: "E7", Arm: "PEBBLE"},
		{Block: "E4", Arm: "A"},
		{Block: "E2", Arm: "A", PublicationMode: "sometimes"},
		{Block: "E3", Cell: "equal_r01", Arm: "A"},
		{Block: "XX", Arm: "A"},
	}
	for i, c := range cases {
		if _, err := c.ResolveMode(); err == nil {
			t.Fatalf("case %d should have been rejected", i)
		}
	}
	ok := []Spec{
		{Block: "E1", Arm: "A"},
		{Block: "E2", Arm: "A", PublicationMode: "physical"},
		{Block: "E3", Cell: "free_r04", Arm: "A"},
		{Block: "E3", Cell: "equal_r04", Arm: "A", PacedWriteRatePerSec: 200},
		{Block: "E4", Arm: "A", OpenLoopRatePerSec: 300},
	}
	for i, c := range ok {
		if _, err := c.ResolveMode(); err != nil {
			t.Fatalf("case %d should be accepted: %v", i, err)
		}
	}
}

func TestRunClosedLoopAllArms(t *testing.T) {
	for _, arm := range []string{"A", "AMIN", "B"} {
		t.Run(arm, func(t *testing.T) {
			spec := miniSpec(t, "E1", "mini_r02", arm)
			res, err := Run(spec, testEnv(), "test-rev")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.Status != "SUCCESS" {
				t.Fatalf("status %s: %s", res.Status, res.Error)
			}
			if res.Operations.ReadOpsCompleted == 0 {
				t.Fatal("no reads completed")
			}
			if res.Operations.WriteOpsCompleted == 0 {
				t.Fatal("no writes completed")
			}
			if !res.LSMDiagnostics.DataDirRemoved {
				t.Fatal("data dir not removed")
			}
			if res.LatencyRead.P50Nanos < 0 {
				t.Fatal("negative p50")
			}
		})
	}
}

func TestRunPublicationOnlyDriver(t *testing.T) {
	spec := miniSpec(t, "E2", "pub_physical", "A")
	spec.PublicationMode = "physical"
	spec.PublicationEvents = 4
	spec.WriterConcurrency = 0
	spec.ReaderConcurrency = 2
	res, err := Run(spec, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	if len(res.Events) != 4 {
		t.Fatalf("expected 4 publication events, got %d", len(res.Events))
	}
	for i, e := range res.Events {
		if e.BeginNs <= 0 || e.EndNs < e.BeginNs {
			t.Fatalf("event %d has an invalid window: %+v", i, e)
		}
	}
	if len(res.EventCond) != 4 {
		t.Fatalf("expected 4 conditioned records, got %d", len(res.EventCond))
	}
	// The driver must not inject writes into the window.
	if res.Operations.WriteOpsAttempted != 0 {
		t.Fatalf("publication-only driver attempted %d writes", res.Operations.WriteOpsAttempted)
	}
}

func TestRunPacedWriterMatchesTargetRate(t *testing.T) {
	spec := miniSpec(t, "E3", "equal_r02", "A")
	spec.PacedWriteRatePerSec = 50 // 50/s over 200ms => ~10
	spec.WriterConcurrency = 1
	spec.CompactionTriggerMs = 0
	res, err := Run(spec, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	w := res.Operations.WriteOpsAttempted
	if w < 6 || w > 14 {
		t.Fatalf("paced writer attempted %d writes, expected ~10", w)
	}
}

func TestRunOpenLoopAccounting(t *testing.T) {
	spec := miniSpec(t, "E4", "L1", "A")
	spec.OpenLoopRatePerSec = 200 // 200/s over 200ms => ~40
	spec.ReaderConcurrency = 4
	spec.WriterConcurrency = 0
	res, err := Run(spec, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	o := res.Operations
	sum := o.ReadOpsCompletedInWindow + o.ReadOpsCompletedLate + o.ReadOpsAbandoned + o.ReadOpsUnissued
	if o.ReadOpsScheduled == 0 {
		t.Fatal("no arrivals scheduled")
	}
	if sum != o.ReadOpsScheduled {
		t.Fatalf("accounting does not close: inWindow(%d)+late(%d)+abandoned(%d)+unissued(%d)=%d, scheduled=%d",
			o.ReadOpsCompletedInWindow, o.ReadOpsCompletedLate, o.ReadOpsAbandoned, o.ReadOpsUnissued, sum, o.ReadOpsScheduled)
	}
	if o.ReadOpsCompletedInWindow+o.ReadOpsCompletedLate != o.ReadOpsCompleted {
		t.Fatalf("completed mismatch: inWindow(%d)+late(%d) != completed(%d)",
			o.ReadOpsCompletedInWindow, o.ReadOpsCompletedLate, o.ReadOpsCompleted)
	}
	if o.ReadOpsIssued == 0 {
		t.Fatal("no requests issued")
	}
	if res.LatencyResponse.Count == 0 {
		t.Fatal("no response-latency samples recorded")
	}
	if res.LatencyResponse.P50Nanos < res.LatencyRead.P50Nanos {
		t.Fatal("response latency should be >= service latency")
	}
}

// TestE2_PhysicalVsNoneModeComparison verifies that:
// 1. intended publication timestamps are strictly within [0, window);
// 2. pub_physical realizes exactly the scheduled publications;
// 3. pub_none has zero released/realized publications;
// 4. both start with identical initial workloads (initial frozen memtables);
// 5. topology differences are explicitly measured and reported in diagnostics.
func TestE2_PhysicalVsNoneModeComparison(t *testing.T) {
	// 1. Check the 10-second 20-event manifest schedule bounds.
	window10s := 10 * time.Second
	events20 := 20
	windowNs := window10s.Nanoseconds()
	for i := 0; i < events20; i++ {
		intended := (int64(i)*2 + 1) * windowNs / (int64(events20) * 2)
		if intended <= 0 {
			t.Fatalf("event %d intended timestamp %d <= 0", i, intended)
		}
		if intended >= windowNs {
			t.Fatalf("event %d intended timestamp %d >= window %d", i, intended, windowNs)
		}
	}
	// First event at 250ms, last at 9750ms:
	first := (int64(0)*2 + 1) * windowNs / (int64(events20) * 2)
	last := (int64(19)*2 + 1) * windowNs / (int64(events20) * 2)
	if first != 250*int64(time.Millisecond) {
		t.Fatalf("first event expected 250ms, got %d ns", first)
	}
	if last != 9750*int64(time.Millisecond) {
		t.Fatalf("last event expected 9750ms, got %d ns", last)
	}

	// 2. Mode execution comparison
	// PHYSICAL MODE
	specPhys := miniSpec(t, "E2", "pub_physical", "A")
	specPhys.PublicationMode = "physical"
	specPhys.PublicationEvents = 4
	specPhys.MeasurementDurationMs = 400
	specPhys.WriterConcurrency = 0
	specPhys.ReaderConcurrency = 2

	resPhys, err := Run(specPhys, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run physical: %v", err)
	}
	if resPhys.Status != "SUCCESS" {
		t.Fatalf("status physical %s: %s", resPhys.Status, resPhys.Error)
	}
	diagPhys := resPhys.LSMDiagnostics
	if diagPhys.PublicationEventsTarget != 4 {
		t.Fatalf("physical target events expected 4, got %d", diagPhys.PublicationEventsTarget)
	}
	if diagPhys.PublicationEventsReleased != 4 {
		t.Fatalf("physical released events expected 4, got %d", diagPhys.PublicationEventsReleased)
	}
	if diagPhys.PublicationEventsRealized != 4 {
		t.Fatalf("physical realized events expected 4, got %d", diagPhys.PublicationEventsRealized)
	}
	if diagPhys.InitialFrozenMemtables != 4 {
		t.Fatalf("physical initial frozen memtables expected 4, got %d", diagPhys.InitialFrozenMemtables)
	}
	if diagPhys.FinalFrozenMemtables != 0 {
		t.Fatalf("physical final frozen memtables expected 0, got %d", diagPhys.FinalFrozenMemtables)
	}
	if diagPhys.InitialSSTables != 2 {
		t.Fatalf("physical initial sstables expected 2, got %d", diagPhys.InitialSSTables)
	}
	if diagPhys.FinalSSTables != 6 {
		t.Fatalf("physical final sstables expected 6 (2 initial + 4 flushes), got %d", diagPhys.FinalSSTables)
	}
	if len(resPhys.Events) != 4 {
		t.Fatalf("physical event log expected 4 records, got %d", len(resPhys.Events))
	}
	if resPhys.Operations.WriteOpsAttempted != 0 {
		t.Fatalf("physical writes attempted expected 0, got %d", resPhys.Operations.WriteOpsAttempted)
	}

	// NONE MODE (control: identical initial workload, zero releases during window)
	specNone := miniSpec(t, "E2", "pub_none", "A")
	specNone.PublicationMode = "none"
	specNone.PublicationEvents = 4 // runlist specifies 4 pre-frozen events
	specNone.MeasurementDurationMs = 400
	specNone.WriterConcurrency = 0
	specNone.ReaderConcurrency = 2

	resNone, err := Run(specNone, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run none: %v", err)
	}
	if resNone.Status != "SUCCESS" {
		t.Fatalf("status none %s: %s", resNone.Status, resNone.Error)
	}
	diagNone := resNone.LSMDiagnostics
	if diagNone.PublicationEventsTarget != 0 {
		t.Fatalf("none target events expected 0, got %d", diagNone.PublicationEventsTarget)
	}
	if diagNone.PublicationEventsReleased != 0 {
		t.Fatalf("none released events expected 0, got %d", diagNone.PublicationEventsReleased)
	}
	if diagNone.PublicationEventsRealized != 0 {
		t.Fatalf("none realized events expected 0, got %d", diagNone.PublicationEventsRealized)
	}
	if diagNone.InitialFrozenMemtables != 4 {
		t.Fatalf("none initial frozen memtables expected 4 (matched workload), got %d", diagNone.InitialFrozenMemtables)
	}
	if diagNone.FinalFrozenMemtables != 4 {
		t.Fatalf("none final frozen memtables expected 4 (unreleased), got %d", diagNone.FinalFrozenMemtables)
	}
	if diagNone.InitialSSTables != 2 {
		t.Fatalf("none initial sstables expected 2, got %d", diagNone.InitialSSTables)
	}
	if diagNone.FinalSSTables != 2 {
		t.Fatalf("none final sstables expected 2 (static), got %d", diagNone.FinalSSTables)
	}
	if len(resNone.Events) != 0 {
		t.Fatalf("none event log expected 0 records, got %d", len(resNone.Events))
	}
	if resNone.Operations.WriteOpsAttempted != 0 {
		t.Fatalf("none writes attempted expected 0, got %d", resNone.Operations.WriteOpsAttempted)
	}
	if diagNone.TopologyNote == "" || !strings.Contains(diagNone.TopologyNote, "none_control") {
		t.Fatalf("none topology note missing: %q", diagNone.TopologyNote)
	}
}

// TestE4_OpenLoopOverloadAndLateAccounting verifies:
// 1. lateness recheck immediately after waking;
// 2. exact conservation: scheduled == completed_in_window + completed_late + abandoned + unissued;
// 3. throughput strictly uses completions inside the window;
// 4. late completions are tracked separately and do not pollute within-window latency.
func TestE4_OpenLoopOverloadAndLateAccounting(t *testing.T) {
	spec := miniSpec(t, "E4", "overload", "A")
	spec.OpenLoopRatePerSec = 2000 // high rate to induce queuing & abandonment
	spec.ReaderConcurrency = 1
	spec.WriterConcurrency = 0
	spec.MaxLatenessMs = 5 // tight deadline: waking delay or backlog drops requests
	spec.MeasurementDurationMs = 200

	res, err := Run(spec, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run overload: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	o := res.Operations
	sum := o.ReadOpsCompletedInWindow + o.ReadOpsCompletedLate + o.ReadOpsAbandoned + o.ReadOpsUnissued
	if sum != o.ReadOpsScheduled {
		t.Fatalf("conservation identity failed: inWindow(%d) + late(%d) + abandoned(%d) + unissued(%d) = %d != scheduled(%d)",
			o.ReadOpsCompletedInWindow, o.ReadOpsCompletedLate, o.ReadOpsAbandoned, o.ReadOpsUnissued, sum, o.ReadOpsScheduled)
	}
	if o.ReadOpsCompletedInWindow+o.ReadOpsCompletedLate != o.ReadOpsCompleted {
		t.Fatalf("total completed mismatch: inWindow(%d) + late(%d) != completed(%d)",
			o.ReadOpsCompletedInWindow, o.ReadOpsCompletedLate, o.ReadOpsCompleted)
	}
	if o.ReadOpsAbandoned == 0 {
		t.Fatal("expected abandoned requests under overload with 5ms max lateness")
	}

	// Verify throughput calculation strictly uses ReadOpsCompletedInWindow:
	secs := float64(res.Timestamps.MeasuredWindowNs) / 1e9
	expectedTps := float64(o.ReadOpsCompletedInWindow) / secs
	diff := res.Throughput.ReadOpsPerSec - expectedTps
	if diff < -0.01 || diff > 0.01 {
		t.Fatalf("throughput mismatch: got %.2f, expected %.2f (from %d in-window completions)",
			res.Throughput.ReadOpsPerSec, expectedTps, o.ReadOpsCompletedInWindow)
	}

	// Verify sample count matches in-window completions:
	if res.LatencyRead.Count != o.ReadOpsCompletedInWindow {
		t.Fatalf("latency read sample count %d != inWindow completions %d",
			res.LatencyRead.Count, o.ReadOpsCompletedInWindow)
	}
	if res.LatencyResponse.Count != o.ReadOpsCompletedInWindow {
		t.Fatalf("latency response sample count %d != inWindow completions %d",
			res.LatencyResponse.Count, o.ReadOpsCompletedInWindow)
	}
	if o.ReadOpsCompletedLate > 0 {
		if res.LatencyReadLate.Count != o.ReadOpsCompletedLate {
			t.Fatalf("late latency read sample count %d != late completions %d",
				res.LatencyReadLate.Count, o.ReadOpsCompletedLate)
		}
	}
}

func TestRunAllMissLengthensReadPath(t *testing.T) {
	spec := miniSpec(t, "E5", "hit0", "A")
	spec.TargetReadHitRatio = 0.0
	res, err := Run(spec, testEnv(), "test-rev")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "SUCCESS" {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	if res.Operations.ActualMissCount == 0 {
		t.Fatal("expected misses with hitRatio=0")
	}
	if res.Operations.ValidationErrorCount != 0 {
		t.Fatalf("misses must not be validation errors: %d", res.Operations.ValidationErrorCount)
	}
}

func TestRunRefusesDirtyDataDir(t *testing.T) {
	spec := miniSpec(t, "E1", "mini_r02", "A")
	// Populate the directory to simulate a stale attempt.
	if err := writeMarker(spec.DataDir); err != nil {
		t.Fatalf("marker: %v", err)
	}
	if _, err := Run(spec, testEnv(), "test-rev"); err == nil {
		t.Fatal("Run must refuse a non-empty data directory")
	}
}

func TestHarnessEnvironmentTypeCompatible(t *testing.T) {
	// Guard against accidental divergence from the frozen schema types.
	var _ harness.EnvironmentInfo = testEnv()
}
