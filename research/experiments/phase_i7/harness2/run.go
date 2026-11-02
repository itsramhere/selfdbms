package harness2

// Trial executor for the four I7 driver modes.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"owndbms/lsmengine"
	"owndbms/research/experiments/harness"
)

const (
	defaultMemtable = 64 << 20
	keysPerEvent    = 64
	eventDeltaNs    = int64(10 * time.Millisecond)
	defaultWMaxMs   = 250
)

func modeForArm(arm string) (lsmengine.SyncMode, string, error) {
	switch arm {
	case "A", "ApproachA", "ApproachA_RWMutexBaseline":
		return lsmengine.SyncModeApproachA, "ApproachA_RWMutexBaseline", nil
	case "AMIN", "A-min", "ApproachAMin_Lease":
		return lsmengine.SyncModeApproachAMin, "ApproachAMin_Lease", nil
	case "B", "ApproachB", "ApproachB_ReaderSnapshot":
		return lsmengine.SyncModeApproachB, "ApproachB_ReaderSnapshot", nil
	}
	return 0, "", fmt.Errorf("unknown arm %q", arm)
}

// Run executes exactly one trial.
func Run(spec Spec, env Environment, gitRev string) (*TrialResult, error) {
	mode, err := spec.ResolveMode()
	if err != nil {
		return nil, err
	}
	lsmMode, syncName, err := modeForArm(spec.Arm)
	if err != nil {
		return nil, err
	}
	if spec.DataDir == "" {
		return nil, errors.New("spec.data_dir is required")
	}
	if err := ensureEmptyDir(spec.DataDir); err != nil {
		return nil, err
	}
	memtable := int(spec.MemtableThresholdBytes)
	if memtable <= 0 {
		memtable = defaultMemtable
	}
	keyspace := spec.KeyspaceCardinality
	if keyspace <= 0 {
		return nil, errors.New("spec.keyspace_cardinality must be > 0")
	}

	res := &TrialResult{
		SchemaVersion: "2.0.0",
		TrialID:       spec.TrialID,
		TrialIndex:    spec.RunlistPosition,
		WorkloadID:    spec.WorkloadID,
		Block:         spec.Block,
		Cell:          spec.Cell,
		Arm:           spec.Arm,
		SyncMode:      syncName,
		GitRevision:   gitRev,
		Environment:   env,
		Config:        spec,
		Status:        "SUCCESS",
	}

	if spec.IsX() {
		v := false
		res.FixtureWALSync = &v
	}
	readPath, _ := spec.ResolveReadPath() // ResolveMode already validated treatment.
	man, err := lsmengine.CreateManagerWithConfig(lsmengine.Config{
		Mode:                   lsmMode,
		ReadPath:               readPath,
		ReadIODelay:            time.Duration(spec.ReadIODelayUs) * time.Microsecond,
		DisableWALSync:         !spec.WalSync,
		InlineFlushCheck:       spec.InlineFlushCheck,
		WalPath:                filepath.Join(spec.DataDir, "wal.log"),
		DataDir:                spec.DataDir,
		MemtableThresholdBytes: defaultMemtable, // large during fixture construction
	})
	if err != nil {
		return nil, fmt.Errorf("create manager: %w", err)
	}

	// Optional synchronization instrumentation. The QPC clock keeps engine
	// timestamps on the same monotonic timeline as the trial window.
	var syncCollector *SyncCollector
	if spec.InstrumentSync {
		syncCollector = NewSyncCollector()
		if spec.IsX() {
			syncCollector = NewSyncHistogramCollector()
		}
		if err := man.SetSyncObserver(syncCollector, qpcElapsedNanos); err != nil {
			_ = man.Close()
			return nil, fmt.Errorf("attach sync instrumentation: %w", err)
		}
	}

	// Fixture durability is outside the measured treatment; re-enable the requested
	// policy before pre-freeze and warmup, after all fixture writers have stopped.
	man.SetWALSync(false)
	fixStart := time.Now()
	if err := buildFixture(man, spec, &res.FixtureManifest); err != nil {
		_ = man.Close()
		res.Status = "FAILED"
		res.Error = err.Error()
		return res, err
	}
	res.FixtureManifest.InitializationDurationMs = time.Since(fixStart).Milliseconds()
	man.SetWALSync(spec.WalSync)
	man.SetMemtableThresholdBytes(memtable)
	initialSSTables := man.ActiveSSTableCount()
	initialFlushes := man.FlushesCompleted()
	initialCompactions := man.CompactionsCompleted()

	stream := NewKeyStream(spec.Seed, keyspace, spec.TargetReadHitRatio)
	writeStream := NewKeyStream(spec.Seed^0x5f5f5f5f, keyspace, 1.0)

	// Pre-freeze the publication pool BEFORE the measurement window.
	// In E2, both pub_physical and pub_none pre-freeze spec.PublicationEvents memtables
	// so their initial workloads at t=0 are identical (same SSTables, same frozen memtables).
	events := spec.PublicationEvents
	if events > 0 {
		if err := preFreeze(man, spec, events); err != nil {
			_ = man.Close()
			res.Status = "FAILED"
			res.Error = err.Error()
			return res, err
		}
	}

	initialFrozen := man.PendingFlushes()
	isPhysical := spec.PublicationMode == string(PubPhysical) || spec.PublicationMode == "physical"
	targetEvents := 0
	if isPhysical && events > 0 {
		targetEvents = events
	}

	// Warmup (not measured).
	if spec.WarmupDurationMs > 0 {
		warmStop := make(chan struct{})
		var wg sync.WaitGroup
		warmStream := NewKeyStream(spec.Seed^0x1111, keyspace, spec.TargetReadHitRatio)
		stride := int64(maxInt(1, spec.ReaderConcurrency))
		for r := 0; r < spec.ReaderConcurrency; r++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				wID := int64(worker)
				iter := int64(0)
				for {
					select {
					case <-warmStop:
						return
					default:
						idx := wID + iter*stride
						k, _ := warmStream.At(idx)
						_, _ = man.Get(k, math.MaxUint64)
						iter++
					}
				}
			}(r)
		}
		time.Sleep(time.Duration(spec.WarmupDurationMs) * time.Millisecond)
		close(warmStop)
		wg.Wait()
	}

	// ---- measurement window ----
	// Warmup readers have joined and X2's pre-frozen pool is still gated, so
	// observer callbacks are idle here. X measures its synchronization dose from
	// this boundary through drain; retain the older blocks' whole-trial scope.
	if spec.IsX() && syncCollector != nil {
		syncCollector.Reset()
	}
	base := Now()
	res.Timestamps.StartISO = time.Now().UTC().Format(time.RFC3339Nano)
	res.Timestamps.WarmupDurationMs = spec.WarmupDurationMs
	res.Timestamps.MeasurementDurationMs = spec.MeasurementDurationMs

	var rec *EventRecorder
	if targetEvents > 0 {
		rec = NewEventRecorder(base)
		man.SetFlushObserver(rec)
	}

	window := time.Duration(spec.MeasurementDurationMs) * time.Millisecond
	if window <= 0 {
		window = defaultWindow
	}
	windowNs := window.Nanoseconds()
	trackIssues := targetEvents > 0
	trackResp := mode == ModeOpenLoop

	shards := make([]*Shard, 0, spec.ReaderConcurrency+spec.WriterConcurrency)
	var shardMu sync.Mutex
	newShard := func(cap int) *Shard {
		var sh *Shard
		if spec.IsX() {
			sh = NewHistogramShard()
		} else {
			sh = NewShard(cap, trackResp, trackIssues)
		}
		shardMu.Lock()
		shards = append(shards, sh)
		shardMu.Unlock()
		return sh
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// Per-worker sample capacity. Generous enough that a legitimate trial never
	// overflows; overflow is counted and fails the trial rather than silently
	// biasing the percentiles toward the first N samples.
	perShardCap := spec.MeasurementDurationMs * 100
	if perShardCap < 131072 {
		perShardCap = 131072
	}

	var sched []Arrival
	if mode == ModeOpenLoop {
		sched = BuildSchedule(spec.Seed, spec.OpenLoopRatePerSec, keyspace, spec.TargetReadHitRatio, window)
		res.Operations.ReadOpsScheduled = int64(len(sched))
	}

	var claim int64
	wMaxNs := int64(defaultWMaxMs) * int64(time.Millisecond)
	if spec.MaxLatenessMs > 0 {
		wMaxNs = int64(spec.MaxLatenessMs) * int64(time.Millisecond)
	}

	// Readers.
	for r := 0; r < spec.ReaderConcurrency; r++ {
		wg.Add(1)
		sh := newShard(perShardCap)
		go func(sh *Shard, worker int) {
			defer wg.Done()
			switch mode {
			case ModeOpenLoop:
				openLoopWorker(man, sh, sched, base, stop, &claim, wMaxNs, windowNs, spec, stream)
			default:
				closedLoopReader(man, sh, base, stop, windowNs, spec, stream, worker)
			}
		}(sh, r)
	}

	// Writers.
	switch mode {
	case ModePacedWrites:
		for w := 0; w < maxInt(1, spec.WriterConcurrency); w++ {
			wg.Add(1)
			sh := newShard(perShardCap)
			go func(sh *Shard) {
				defer wg.Done()
				pacedWriter(man, sh, base, stop, windowNs, spec, writeStream)
			}(sh)
		}
	case ModeClosedLoop, ModePublicationOnly:
		for w := 0; w < spec.WriterConcurrency; w++ {
			wg.Add(1)
			sh := newShard(perShardCap)
			go func(sh *Shard, worker int) {
				defer wg.Done()
				unpacedWriter(man, sh, stop, base, windowNs, spec, writeStream, worker)
			}(sh, w)
		}
	}

	// Compaction trigger.
	if spec.CompactionTriggerMs > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk := time.NewTicker(time.Duration(spec.CompactionTriggerMs) * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					_ = man.CompactAll()
				}
			}
		}()
	}

	// Publication injector: one release per scheduled instant, never blocking.
	// Midpoint schedule: intended event timestamps are strictly within [0, window).
	// E.g. for N=20, W=10s: 250ms, 750ms, ..., 9750ms (< 10000ms).
	var releasedEvents int64
	if targetEvents > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < targetEvents; i++ {
				intended := (int64(i)*2 + 1) * windowNs / (int64(targetEvents) * 2)
				SleepUntilElapsed(base, intended)
				select {
				case <-stop:
					return
				default:
				}
				atomic.AddInt64(&releasedEvents, 1)
				rec.MarkReleased(keysPerEvent)
				man.ReleaseOneFlush()
			}
		}()
	}

	measured := SleepWindow(base, window)
	close(stop)
	wg.Wait()
	res.Timestamps.MeasuredWindowNs = measured
	res.Timestamps.FinishISO = time.Now().UTC().Format(time.RFC3339Nano)

	// In physical mode, drain the released publications so the event log is complete.
	if targetEvents > 0 {
		man.DisableFlushInjectionGate()
		if err := man.WaitForFlushCompletion(); err != nil && res.Error == "" {
			res.Status = "FAILED"
			res.Error = "flush drain: " + err.Error()
		}
	}

	if spec.CooldownDurationMs > 0 {
		time.Sleep(time.Duration(spec.CooldownDurationMs) * time.Millisecond)
	}

	// ---- aggregate ----
	tot := Sum(shards)
	service := MergeSamples(shards)
	resp := MergeResponses(shards)
	res.LatencyRead = Stats(service)
	res.LatencyResponse = Stats(resp)

	serviceLate := MergeSamplesLate(shards)
	respLate := MergeResponsesLate(shards)
	if len(serviceLate) > 0 {
		res.LatencyReadLate = Stats(serviceLate)
	}
	if len(respLate) > 0 {
		res.LatencyResponseLate = Stats(respLate)
	}

	if spec.IsX() {
		res.LatencyRead = MergeHistograms(shards, false)
		res.LatencyReadLate = MergeHistograms(shards, true)
	}
	secs := float64(measured) / 1e9
	if secs <= 0 {
		secs = 1e-9
	}
	res.Operations.ReadOpsIssued = tot.Issued
	if mode != ModeOpenLoop {
		res.Operations.ReadOpsScheduled = tot.ReadsAttempted
		res.Operations.ReadOpsIssued = tot.ReadsAttempted
	}
	res.Operations.ReadOpsCompletedInWindow = tot.ReadsCompletedInWindow
	res.Operations.ReadOpsCompletedLate = tot.ReadsCompletedLate
	res.Operations.ReadOpsCompleted = tot.ReadsCompleted
	res.Operations.ReadOpsAbandoned = tot.Abandoned
	if mode == ModeOpenLoop {
		res.Operations.ReadOpsUnissued = res.Operations.ReadOpsScheduled - tot.ReadsCompletedInWindow - tot.ReadsCompletedLate - tot.Abandoned
	}
	res.Operations.ActualHitCount = tot.ReadsHit
	res.Operations.ActualMissCount = tot.ReadsMiss
	res.Operations.WriteOpsAttempted = tot.WritesAttempted
	res.Operations.WriteOpsCompletedInWindow = tot.WritesCompletedInWindow
	res.Operations.WriteOpsCompletedLate = tot.WritesCompletedLate
	res.Operations.WriteOpsCompleted = tot.WritesCompleted
	res.Operations.ErrorOpsCount = tot.ReadsError + tot.WritesError
	res.Operations.ValidationErrorCount = tot.ValidationErrors

	// Throughput is calculated strictly using completions inside the measured window.
	res.Throughput = ThroughputInfo{
		ReadOpsPerSec:  float64(tot.ReadsCompletedInWindow) / secs,
		WriteOpsPerSec: float64(tot.WritesCompletedInWindow) / secs,
		TotalOpsPerSec: float64(tot.ReadsCompletedInWindow+tot.WritesCompletedInWindow) / secs,
	}

	// ---- conserved transaction ledger ----
	scheduledReads := mode == ModeOpenLoop
	scheduledWrites := mode == ModePacedWrites
	if scheduledReads {
		res.Ledger.Reads = NewScheduledAccounting(
			res.Operations.ReadOpsScheduled, tot.ReadsAttempted,
			tot.ReadsCompletedInWindow, tot.ReadsCompletedLate, tot.ReadsError, tot.Abandoned)
	} else {
		res.Ledger.Reads = NewClosedLoopAccounting(
			tot.ReadsAttempted, tot.ReadsCompletedInWindow, tot.ReadsCompletedLate, tot.ReadsError)
	}
	if scheduledWrites {
		periodNs := int64(float64(time.Second) / spec.PacedWriteRatePerSec)
		offeredTicks := (windowNs + periodNs - 1) / periodNs
		res.Ledger.Writes = NewScheduledAccounting(
			offeredTicks, tot.WritesAttempted,
			tot.WritesCompletedInWindow, tot.WritesCompletedLate, tot.WritesError, 0)
	} else {
		res.Ledger.Writes = NewClosedLoopAccounting(
			tot.WritesAttempted, tot.WritesCompletedInWindow, tot.WritesCompletedLate, tot.WritesError)
	}
	if ledgerErr := res.Ledger.Validate(scheduledReads, scheduledWrites); ledgerErr != nil && res.Error == "" {
		res.Status = "FAILED"
		res.Error = "transaction ledger: " + ledgerErr.Error()
	}

	if syncCollector != nil {
		s := syncCollector.Summary()
		res.Sync = &s
	}

	finalFrozen := man.PendingFlushes()
	finalSSTables := man.ActiveSSTableCount()
	flushesCompleted := man.FlushesCompleted() - initialFlushes
	compactionsCompleted := man.CompactionsCompleted() - initialCompactions

	var topoNote string
	if isPhysical {
		topoNote = fmt.Sprintf("physical_pub: frozen %d->%d, sstables %d->%d, realized %d/%d",
			initialFrozen, finalFrozen, initialSSTables, finalSSTables, flushesCompleted, targetEvents)
	} else if spec.PublicationMode == string(PubNone) || spec.PublicationMode == "none" {
		topoNote = fmt.Sprintf("none_control: frozen %d->%d (static), sstables %d->%d (static), realized 0/0",
			initialFrozen, finalFrozen, initialSSTables, finalSSTables)
	}

	res.LSMDiagnostics = Diagnostics{
		InitialSSTables:           initialSSTables,
		FinalSSTables:             finalSSTables,
		InitialFrozenMemtables:    initialFrozen,
		FinalFrozenMemtables:      finalFrozen,
		PublicationEventsTarget:   targetEvents,
		PublicationEventsReleased: int(atomic.LoadInt64(&releasedEvents)),
		PublicationEventsRealized: flushesCompleted,
		TopologyNote:              topoNote,
		MeasuredFlushes:           flushesCompleted,
		MeasuredCompactions:       compactionsCompleted,
		MeasuredSSTableReads:      man.SSTablePointReads(),
		MeasuredMemtableReads:     man.MemtablePointReads(),
		SnapshotSeq:               man.SnapshotSeq(),
		ActiveSnapshotPins:        man.ActiveSnapshotPins(),
		RetiredSnapshots:          man.RetiredSnapshotCount(),
		PendingCleanupCount:       man.PendingCleanupCount(),
		LivePhysicalReaders:       man.LivePhysicalReaders(),
		PendingFlushesAtEnd:       man.PendingFlushes(),
	}

	if rec != nil {
		inWin, lateEvents := rec.Events(windowNs)
		res.Events = inWin
		res.EventsLate = lateEvents
		if !spec.IsX() {
			res.EventCond = ConditionEvents(res.Events, MergeIssues(shards), eventDeltaNs)
		}
		res.LSMDiagnostics.PublicationEventsRealizedInWindow = len(inWin)
		res.LSMDiagnostics.PublicationEventsRealizedLate = len(lateEvents)
		publicationTimingOutcome(res, spec, isPhysical, targetEvents, len(inWin), len(lateEvents))
	}

	// Shutdown. A non-nil error is a failed trial: for A-min a lease drain
	// timeout means the engine refused to close a still-leased descriptor.
	if err := man.Close(); err != nil {
		res.Status = "FAILED"
		res.Error = "close: " + err.Error()
	}
	res.LSMDiagnostics.PendingCleanupCount = man.PendingCleanupCount()

	if rmErr := os.RemoveAll(spec.DataDir); rmErr == nil {
		res.LSMDiagnostics.DataDirRemoved = true
	} else if res.Error == "" {
		res.Status = "FAILED"
		res.Error = "data dir removal: " + rmErr.Error()
	}

	if tot.ValidationErrors > 0 && res.Error == "" {
		res.Status = "FAILED"
		res.Error = fmt.Sprintf("%d validation error(s)", tot.ValidationErrors)
	}
	if tot.Dropped > 0 && res.Error == "" {
		res.Status = "FAILED"
		res.Error = fmt.Sprintf("%d latency sample(s) dropped (recorder capacity)", tot.Dropped)
	}
	return res, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ensureEmptyDir creates dir and refuses to run in a non-empty one, so a retry
// can never inherit a previous attempt's SSTables or WAL.
func ensureEmptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("data dir %s is not empty (%d entries); archive or clear it first", dir, len(entries))
	}
	return nil
}

// buildFixture populates InitialSSTableCount tables exactly as the frozen harness
// does, then verifies boundary keys.
func buildFixture(man *lsmengine.Manager, spec Spec, fix *harness.FixtureManifest) error {
	fix.ExpectedSSTableCount = spec.InitialSSTableCount
	if spec.InitialSSTableCount > 0 {
		keysPerTable := spec.InitialKeysPerSSTable
		if keysPerTable*spec.InitialSSTableCount < spec.KeyspaceCardinality {
			keysPerTable = (spec.KeyspaceCardinality + spec.InitialSSTableCount - 1) / spec.InitialSSTableCount
		}
		curr := 0
		for t := 0; t < spec.InitialSSTableCount; t++ {
			tx := man.Begin()
			if tx == nil {
				return errors.New("fixture: Begin returned nil")
			}
			for k := 0; k < keysPerTable && curr < spec.KeyspaceCardinality; k++ {
				tx.Put(harness.FormatKey(curr), harness.FormatVal(curr, 128))
				fix.CommittedKeys++
				curr++
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("fixture commit: %w", err)
			}
			if err := man.TriggerFlush(); err != nil {
				return fmt.Errorf("fixture flush: %w", err)
			}
			if err := man.WaitForFlushCompletion(); err != nil {
				return fmt.Errorf("fixture flush wait: %w", err)
			}
		}
	}
	fix.ActualSSTableCount = man.ActiveSSTableCount()
	if fix.ActualSSTableCount != fix.ExpectedSSTableCount {
		return fmt.Errorf("fixture table count: expected %d, got %d", fix.ExpectedSSTableCount, fix.ActualSSTableCount)
	}
	for _, idx := range []int{0, spec.KeyspaceCardinality / 2, spec.KeyspaceCardinality - 1} {
		if idx < 0 || idx >= spec.KeyspaceCardinality {
			continue
		}
		v, err := man.Get(harness.FormatKey(idx), math.MaxUint64)
		if err != nil {
			return fmt.Errorf("fixture readback %d: %w", idx, err)
		}
		if len(v) == 0 {
			return fmt.Errorf("fixture readback %d: empty value", idx)
		}
	}
	fix.ValidationReadbackStatus = "PASSED"
	return nil
}

// preFreeze queues `events` frozen memtables behind the injection gate.
func preFreeze(man *lsmengine.Manager, spec Spec, events int) error {
	man.EnableFlushInjectionGate()
	for i := 0; i < events; i++ {
		tx := man.Begin()
		if tx == nil {
			return errors.New("pre-freeze: Begin returned nil")
		}
		for k := 0; k < keysPerEvent; k++ {
			idx := (i*keysPerEvent + k) % spec.KeyspaceCardinality
			tx.Put(harness.FormatKey(idx), harness.FormatVal(idx, 128))
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("pre-freeze commit: %w", err)
		}
		if err := man.TriggerFlush(); err != nil {
			return fmt.Errorf("pre-freeze flush: %w", err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for man.PendingFlushes() < events {
		if time.Now().After(deadline) {
			return fmt.Errorf("pre-freeze: only %d of %d memtables queued", man.PendingFlushes(), events)
		}
		time.Sleep(time.Millisecond)
	}
	return nil
}

func recordRead(sh *Shard, v string, err error, expectHit bool, inWindow bool) {
	sh.ReadsAttempted++
	sh.Issued++
	if inWindow {
		sh.ReadsCompletedInWindow++
	} else {
		sh.ReadsCompletedLate++
	}
	switch {
	case err == nil:
		sh.ReadsCompleted++
		if expectHit {
			sh.ReadsHit++
			if len(v) == 0 {
				sh.ValidationErrors++
			}
		} else {
			sh.ValidationErrors++ // a hit was not supposed to exist
		}
	case errors.Is(err, lsmengine.ErrNotFound):
		sh.ReadsCompleted++
		sh.ReadsMiss++
		if expectHit {
			sh.ValidationErrors++
		}
	default:
		sh.ReadsError++
	}
}

func closedLoopReader(man *lsmengine.Manager, sh *Shard, base harness.PrecisionTime, stop <-chan struct{}, windowNs int64, spec Spec, stream *KeyStream, worker int) {
	stride := int64(maxInt(1, spec.ReaderConcurrency))
	wID := int64(worker)
	iter := int64(0)
	for {
		select {
		case <-stop:
			return
		default:
		}
		idx := wID + iter*stride
		key, expect := stream.At(idx)
		t0 := ElapsedNs(base)
		v, err := man.Get(key, math.MaxUint64)
		done := ElapsedNs(base)
		d := done - t0
		inWindow := done <= windowNs
		if inWindow {
			sh.RecordService(d)
			sh.RecordIssue(t0, t0, done)
		} else {
			sh.RecordServiceLate(d)
			sh.RecordIssueLate(t0, t0, done)
		}
		recordRead(sh, v, err, expect, inWindow)
		iter++
	}
}

func unpacedWriter(man *lsmengine.Manager, sh *Shard, stop <-chan struct{}, base harness.PrecisionTime, windowNs int64, spec Spec, stream *KeyStream, worker int) {
	stride := int64(maxInt(1, spec.WriterConcurrency))
	wID := int64(worker)
	iter := int64(0)
	for {
		select {
		case <-stop:
			return
		default:
		}
		idx := wID + iter*stride
		key, _ := stream.At(idx)
		val := harness.FormatVal(int(idx), 128)
		sh.WritesAttempted++
		tx := man.Begin()
		if tx == nil {
			sh.WritesError++
			iter++
			continue
		}
		tx.Put(key, val)
		if err := tx.Commit(); err != nil {
			sh.WritesError++
		} else {
			sh.WritesCompleted++
			done := ElapsedNs(base)
			if done <= windowNs {
				sh.WritesCompletedInWindow++
			} else {
				sh.WritesCompletedLate++
			}
		}
		iter++
	}
}

// pacedWriter issues writes on an absolute QPC schedule at W_paced.
func pacedWriter(man *lsmengine.Manager, sh *Shard, base harness.PrecisionTime, stop <-chan struct{}, windowNs int64, spec Spec, stream *KeyStream) {
	periodNs := int64(float64(time.Second) / spec.PacedWriteRatePerSec)
	i := int64(0)
	for {
		target := i * periodNs
		if target >= windowNs {
			return
		}
		SleepUntilElapsed(base, target)
		select {
		case <-stop:
			return
		default:
		}
		key, _ := stream.At(i)
		val := harness.FormatVal(int(i), 128)
		sh.WritesAttempted++
		tx := man.Begin()
		if tx == nil {
			sh.WritesError++
		} else {
			tx.Put(key, val)
			if err := tx.Commit(); err != nil {
				sh.WritesError++
			} else {
				sh.WritesCompleted++
				done := ElapsedNs(base)
				if done <= windowNs {
					sh.WritesCompletedInWindow++
				} else {
					sh.WritesCompletedLate++
				}
			}
		}
		i++
	}
}

// openLoopWorker claims arrivals from the shared schedule and records
// scheduled/issued/abandoned accounting. The one atomic on this path is the
// claim counter (one per request, ~500/s), not a per-sample lock.
func openLoopWorker(man *lsmengine.Manager, sh *Shard, sched []Arrival, base harness.PrecisionTime, stop <-chan struct{}, claim *int64, wMaxNs int64, windowNs int64, spec Spec, stream *KeyStream) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		i := atomic.AddInt64(claim, 1) - 1
		if i >= int64(len(sched)) {
			return
		}
		a := sched[i]
		now := ElapsedNs(base)
		if now > a.IntendedNs+wMaxNs {
			sh.Abandoned++
			continue
		}
		SleepUntilElapsed(base, a.IntendedNs)
		issued := ElapsedNs(base)
		// Recheck lateness immediately after waking, before starting the request.
		if issued > a.IntendedNs+wMaxNs {
			sh.Abandoned++
			continue
		}
		if late := issued - a.IntendedNs; late > sh.MaxLatenessNs {
			sh.MaxLatenessNs = late
		}
		v, err := man.Get(a.Key, math.MaxUint64)
		done := ElapsedNs(base)
		inWindow := done <= windowNs
		if inWindow {
			sh.RecordService(done - issued)
			sh.RecordResponse(done - a.IntendedNs)
			sh.RecordIssue(a.IntendedNs, issued, done)
		} else {
			sh.RecordServiceLate(done - issued)
			sh.RecordResponseLate(done - a.IntendedNs)
			sh.RecordIssueLate(a.IntendedNs, issued, done)
		}
		recordRead(sh, v, err, a.ExpectHit, inWindow)
	}
}

// X2 measures publication shortfall as an outcome. Preserve any existing fault;
// E2's timing contract and failure wording remain exactly as before.
func publicationTimingOutcome(res *TrialResult, spec Spec, physical bool, target, inWindow, late int) {
	if spec.Block != string(BlockX2) && physical && inWindow != target && res.Error == "" {
		res.Status = "FAILED"
		res.Error = fmt.Sprintf("E2 publication timing: expected %d in-window realized events, got %d (%d late)", target, inWindow, late)
	}
}
