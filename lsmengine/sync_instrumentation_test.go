package lsmengine

// Deterministic tests for the Phase I7 synchronization instrumentation.
//
// Timing assertions use a synthetic clock that advances by a fixed step on
// every read, so observed durations are exact integer multiples of the step
// and the tests are immune to scheduler noise. No wall-clock duration is ever
// asserted.

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stepClock advances by step on every call, producing exact durations.
type stepClock struct {
	now  atomic.Int64
	step int64
}

func (c *stepClock) Now() int64 { return c.now.Add(c.step) }

// rec is one writer-wait or publication observation.
type rec struct {
	site    LockSite
	waitNs  int64
	holders int32
}

// recordingObserver is a mutex-guarded SyncObserver for single-shot assertions.
type recordingObserver struct {
	mu          sync.Mutex
	readCS      map[SyncMode][]int64
	waits       []rec
	pubs        []rec
	publishHold []int

	publishBegan chan struct{}
	once         sync.Once
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{
		readCS:       map[SyncMode][]int64{},
		publishBegan: make(chan struct{}, 64),
	}
}

func (o *recordingObserver) OnReadCriticalSection(mode SyncMode, durationNs int64) {
	o.mu.Lock()
	o.readCS[mode] = append(o.readCS[mode], durationNs)
	o.mu.Unlock()
}

func (o *recordingObserver) OnWriterLockWait(site LockSite, waitNs int64, holders int32) {
	o.mu.Lock()
	o.waits = append(o.waits, rec{site: site, waitNs: waitNs, holders: holders})
	o.mu.Unlock()
}

func (o *recordingObserver) OnPublication(site LockSite, durationNs int64, holders int32) {
	o.mu.Lock()
	o.pubs = append(o.pubs, rec{site: site, waitNs: durationNs, holders: holders})
	o.mu.Unlock()
}

func (o *recordingObserver) OnFlushPublishBegin(activeRLockHolders int) {
	o.mu.Lock()
	o.publishHold = append(o.publishHold, activeRLockHolders)
	o.mu.Unlock()
	o.once.Do(func() { close(o.publishBegan) })
}

// Unused FlushObserver callbacks (recordingObserver doubles as one so tests
// can capture the publication arrival sample).
func (o *recordingObserver) OnFlushRequested()            {}
func (o *recordingObserver) OnFlushAdmitted(bool, error)  {}
func (o *recordingObserver) OnFlushWorkerBegin()          {}
func (o *recordingObserver) OnFlushBuildBegin()           {}
func (o *recordingObserver) OnFlushBuildEnd(error)        {}
func (o *recordingObserver) OnFlushPublishEnd(error)      {}
func (o *recordingObserver) OnFlushComplete(error)        {}

var _ SyncObserver = (*recordingObserver)(nil)
var _ FlushObserver = (*recordingObserver)(nil)

func newInstrumentedManager(t *testing.T, mode SyncMode, clock *stepClock) (*Manager, *recordingObserver) {
	t.Helper()
	man, err := CreateManagerWithConfig(Config{
		Mode:    mode,
		WalPath: filepath.Join(t.TempDir(), "wal.log"),
		DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	obs := newRecordingObserver()
	if err := man.SetSyncObserver(obs, clock.Now); err != nil {
		t.Fatalf("set sync observer: %v", err)
	}
	// The observer also listens to flush phases so tests can capture the
	// publication arrival sample.
	man.SetFlushObserver(obs)
	t.Cleanup(func() { _ = man.Close() })
	return man, obs
}

func waitForCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSetSyncObserverRequiresClock is the fail-closed error path: instrumentation
// without a monotonic clock must be refused, never silently degraded to a
// coarse wall clock.
func TestSetSyncObserverRequiresClock(t *testing.T) {
	man, err := CreateManagerWithConfig(Config{Mode: SyncModeApproachA, WalPath: filepath.Join(t.TempDir(), "wal.log")})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	defer func() { _ = man.Close() }()
	if err := man.SetSyncObserver(newRecordingObserver(), nil); err == nil {
		t.Fatal("expected rejection of an observer without a clock")
	}
	if man.syncObserver != nil {
		t.Fatal("rejected observer must not be attached")
	}
	// A nil observer with a nil clock is a valid detach call.
	if err := man.SetSyncObserver(nil, nil); err != nil {
		t.Fatalf("detach failed: %v", err)
	}
}

// TestActiveRLockHoldersAllModes verifies that the holder counter tracks
// readers parked inside the read critical section, for each of the three
// synchronization architectures, and returns to zero after they leave.
func TestActiveRLockHoldersAllModes(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			man, _ := newInstrumentedManager(t, mode, &stepClock{step: 1})
			tx := man.Begin()
			tx.Put("k0", "v0")
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}

			var enteredOnce sync.Once
			entered := make(chan struct{})
			release := make(chan struct{})
			man.duringMemtableReadHook = func() {
				enteredOnce.Do(func() { close(entered) })
				<-release
			}

			const readers = 4
			var wg sync.WaitGroup
			for i := 0; i < readers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, _ = man.Get(fmt.Sprintf("k%d", i), math.MaxUint64)
				}()
			}
			<-entered
			waitForCond(t, "holders to reach 4", func() bool { return man.ActiveRLockHolders() == readers })
			close(release)
			wg.Wait()
			waitForCond(t, "holders to return to 0", func() bool { return man.ActiveRLockHolders() == 0 })
		})
	}
}

// TestReadCriticalSectionObservedAllModes verifies one observed read per mode
// with an exact synthetic duration.
func TestReadCriticalSectionObservedAllModes(t *testing.T) {
	const step = 100
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			clock := &stepClock{step: step}
			man, obs := newInstrumentedManager(t, mode, clock)
			tx := man.Begin()
			tx.Put("k0", "v0")
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
			obs.mu.Lock()
			baseReads := len(obs.readCS[mode])
			obs.mu.Unlock()

			if _, err := man.Get("k0", math.MaxUint64); err != nil {
				t.Fatalf("get: %v", err)
			}
			obs.mu.Lock()
			got := obs.readCS[mode]
			obs.mu.Unlock()
			if len(got) != baseReads+1 {
				t.Fatalf("expected %d observed read critical sections, got %d", baseReads+1, len(got))
			}
			d := got[len(got)-1]
			// start + end = two clock reads -> exactly one step of duration.
			if d != step {
				t.Fatalf("read critical section = %d ns, want exactly %d ns (synthetic clock step)", d, step)
			}
		})
	}
}

// TestWriterWaitAndPublicationObservedAllModes verifies the commit wait and
// the flush publication hold, with exact synthetic durations and a zero
// holder sample on a quiescent engine.
func TestWriterWaitAndPublicationObservedAllModes(t *testing.T) {
	const step = 100
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		t.Run(mode.String(), func(t *testing.T) {
			clock := &stepClock{step: step}
			man, obs := newInstrumentedManager(t, mode, clock)

			tx := man.Begin()
			tx.Put("k0", "v0")
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
			if err := man.TriggerFlush(); err != nil {
				t.Fatalf("trigger flush: %v", err)
			}
			if err := man.WaitForFlushCompletion(); err != nil {
				t.Fatalf("flush: %v", err)
			}

			obs.mu.Lock()
			defer obs.mu.Unlock()
			// Two writer arrivals are expected: the commit and the flush
			// worker's arrival at the publication lock.
			if len(obs.waits) != 2 {
				t.Fatalf("expected 2 writer waits (commit + flush publish), got %d: %+v", len(obs.waits), obs.waits)
			}
			commitWaits := 0
			for _, w := range obs.waits {
				switch w.site {
				case LockSiteCommit:
					commitWaits++
					if w.waitNs != step || w.holders != 0 {
						t.Fatalf("commit wait = %+v, want wait=%d holders=0", w, step)
					}
				case LockSiteFlushPublish:
					if w.waitNs != step || w.holders != 0 {
						t.Fatalf("flush publish wait = %+v, want wait=%d holders=0", w, step)
					}
				default:
					t.Fatalf("unexpected lock site %v", w.site)
				}
			}
			if commitWaits != 1 {
				t.Fatalf("expected exactly 1 commit wait, got %d", commitWaits)
			}
			if len(obs.pubs) != 1 {
				t.Fatalf("expected 1 publication, got %d: %+v", len(obs.pubs), obs.pubs)
			}
			p := obs.pubs[0]
			if p.site != LockSiteFlushPublish || p.waitNs != step || p.holders != 0 {
				t.Fatalf("publication = %+v, want site=flush_publish hold=%d holders=0", p, step)
			}
		})
	}
}

// TestPublicationArrivalSampleCountsBlockedReaders verifies the pre-registered
// R1.2e §3.2 arrival sampling: a reader parked inside its read critical
// section when the flush worker arrives at the publication lock must appear
// in the OnFlushPublishBegin sample.
//
// Deterministic staging: the flush worker is parked in beforeFlushPublishHook
// (which runs outside Manager.Mu) while the reader parks inside its read
// critical section; releasing the worker then makes it sample the holder
// counter before contending on Manager.Mu with the parked reader.
func TestPublicationArrivalSampleCountsBlockedReaders(t *testing.T) {
	clock := &stepClock{step: 100}
	man, obs := newInstrumentedManager(t, SyncModeApproachA, clock)

	tx := man.Begin()
	tx.Put("k0", "v0")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Park the flush worker immediately before the publication section.
	workerParked := make(chan struct{})
	releaseWorker := make(chan struct{})
	var parkOnce sync.Once
	man.beforeFlushPublishHook = func() {
		parkOnce.Do(func() { close(workerParked) })
		<-releaseWorker
	}
	if err := man.TriggerFlush(); err != nil {
		t.Fatalf("trigger flush: %v", err)
	}
	<-workerParked

	// Park one reader inside its read critical section.
	var enteredOnce sync.Once
	entered := make(chan struct{})
	releaseReader := make(chan struct{})
	man.duringMemtableReadHook = func() {
		enteredOnce.Do(func() { close(entered) })
		<-releaseReader
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		_, _ = man.Get("k1", math.MaxUint64)
	}()
	<-entered
	waitForCond(t, "holder to be counted", func() bool { return man.ActiveRLockHolders() == 1 })

	// Release the worker: it samples the holders at publication arrival, then
	// contends on Manager.Mu with the parked reader.
	close(releaseWorker)
	select {
	case <-obs.publishBegan:
	case <-time.After(5 * time.Second):
		t.Fatal("publication never began")
	}
	obs.mu.Lock()
	got := append([]int(nil), obs.publishHold...)
	obs.mu.Unlock()
	if len(got) == 0 {
		t.Fatal("OnFlushPublishBegin was not observed")
	}
	if got[len(got)-1] != 1 {
		t.Fatalf("arrival sample = %d, want 1 blocked reader", got[len(got)-1])
	}
	close(releaseReader)
	<-readerDone
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// TestCompactionPublicationObserved verifies the compaction publication hold
// observation for Approach A (identity-preserving swap-in).
func TestCompactionPublicationObserved(t *testing.T) {
	const step = 100
	clock := &stepClock{step: step}
	man, obs := newInstrumentedManager(t, SyncModeApproachA, clock)
	for i := 0; i < 3; i++ {
		tx := man.Begin()
		tx.Put(fmt.Sprintf("k%d", i), "v")
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		if err := man.TriggerFlush(); err != nil {
			t.Fatalf("flush %d: %v", i, err)
		}
	}
	if err := man.WaitForFlushCompletion(); err != nil {
		t.Fatalf("flushes: %v", err)
	}
	if err := man.CompactAll(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	var pubs []rec
	for _, p := range obs.pubs {
		if p.site == LockSiteCompactionPublish {
			pubs = append(pubs, p)
		}
	}
	if len(pubs) != 1 {
		t.Fatalf("expected exactly 1 compaction publication, got %d (%+v)", len(pubs), obs.pubs)
	}
	if pubs[0].waitNs != step {
		t.Fatalf("compaction publication hold = %d, want %d", pubs[0].waitNs, step)
	}
}

// TestHolderCounterNeverNegative guards the counter invariant under
// concurrent readers and writers with no instrumentation attached.
func TestHolderCounterNeverNegative(t *testing.T) {
	man, err := CreateManagerWithConfig(Config{Mode: SyncModeApproachAMin, WalPath: filepath.Join(t.TempDir(), "wal.log"), DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	defer func() { _ = man.Close() }()
	tx := man.Begin()
	tx.Put("k", "v")
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = man.Get("k", math.MaxUint64)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			wtx := man.Begin()
			wtx.Put("k", fmt.Sprintf("v%d", i))
			_ = wtx.Commit()
		}
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h := man.ActiveRLockHolders(); h < 0 {
			close(stop)
			wg.Wait()
			t.Fatalf("active holder counter went negative: %d", h)
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if h := man.ActiveRLockHolders(); h != 0 {
		t.Fatalf("holder counter did not drain to 0: %d", h)
	}
}
