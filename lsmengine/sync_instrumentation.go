package lsmengine

// Non-invasive synchronization instrumentation (Phase I7 engineering preflight,
// R1.2e §3.2 and the R2 engineering authorization).
//
// Contract:
//
//  1. Zero production footprint. SyncObserver is nil unless a caller attaches
//     one, and every instrumented site pays exactly one nil-pointer check.
//     Attaching an observer never changes engine behavior, ordering, or
//     outcomes; observers must be non-blocking and lock-free.
//
//  2. Honest timestamps. Durations are only ever measured with a caller
//     supplied monotonic nanosecond clock. SetSyncObserver refuses an observer
//     without a clock so that coarse wall-clock time (on Windows time.Now
//     advances in ~0.5 ms steps) can never silently quantise microsecond-level
//     synchronization events to zero. The I7 harness injects the QPC clock,
//     which shares one monotonic timeline with the trial measurement window.
//
//  3. Active read-holder counting (R1.2e §3.2). activeRLockHolders counts
//     goroutines between rLock() and rUnlock() on Manager.Mu. Per the
//     pre-registered sketch the increment happens BEFORE Mu.RLock() and the
//     decrement AFTER Mu.RUnlock(). The counter therefore satisfies
//     counter >= (true holders) at every instant — it can overcount by readers
//     that have incremented but not yet acquired, and by readers that have
//     released the RWMutex but not yet decremented — and can never undercount.
//     It is a sampled diagnostic, not a linearizable snapshot: between a
//     writer's Load() of the counter and its Mu.Lock() the holder set may
//     change arbitrarily. These limitations are restated in the checkpoint
//     document that defines the EXP-2 arrival sampling semantics.
//
//  4. Scope. Reader-holder samples describe Manager.Mu only. Compaction
//     additionally observes the A/A-min input-open section and B's snapshotMu
//     publication. Lifecycle callbacks are optional and use the same clock.

import (
	"errors"
	"sync/atomic"
)

// SyncObserver receives synchronization instrumentation from the Manager.
// Implementations must be non-blocking, lock-free, allocation-free and
// thread-safe; they are invoked on the hot-path goroutines of readers,
// writers and flush workers.
type SyncObserver interface {
	// OnReadCriticalSection reports one completed read request and the duration
	// of its Manager.Mu read critical section (from RLock acquisition to
	// RUnlock release). mode identifies the read path observed:
	//   - SyncModeApproachA:     the critical section spans the entire read.
	//   - SyncModeApproachAMin:  the critical section spans only the
	//                            memory-tier probe + lease-capture window;
	//                            the SSTable scan runs unlocked afterwards.
	//   - SyncModeApproachB:     the critical section spans the memory-tier
	//                            probe + snapshot pin; the disk scan runs
	//                            unlocked afterwards.
	OnReadCriticalSection(mode SyncMode, durationNs int64)

	// OnWriterLockWait reports one writer arrival at Manager.Mu for writing.
	// waitNs is the interval between the arrival sample and completed
	// acquisition (including the uncontended fast path, so the observed wait
	// has a timer-overhead floor of roughly two clock reads).
	// holdersAtArrival is the activeRLockHolders sample taken immediately
	// before the Lock attempt (see the overcounting caveat above).
	OnWriterLockWait(site LockSite, waitNs int64, holdersAtArrival int32)

	// OnPublication reports one exclusive publication critical section (the
	// swap-in of a flushed or compacted SSTable). durationNs is the strict
	// exclusive hold time measured from acquisition to release;
	// holdersAtArrival is the arrival sample taken before the Lock attempt.
	OnPublication(site LockSite, durationNs int64, holdersAtArrival int32)
}

// LockSite identifies the writer path that produced an observation.
type LockSite int

const (
	// LockSiteCommit is Transaction.Commit's validation + apply critical section.
	LockSiteCommit LockSite = iota
	// LockSiteFlushPublish is the flush worker's SSTable publication section
	// in flushToDisk (all synchronization modes).
	LockSiteFlushPublish
	// LockSiteCompactionPublish is CompactAll's identity-preserving swap-in
	// for Approach A and A-min.
	LockSiteCompactionPublish
	// LockSiteCompactionStart covers A/A-min admission and input iterator opens.
	LockSiteCompactionStart
	// LockSiteCompactionSnapshotPublish covers B publication under snapshotMu.
	LockSiteCompactionSnapshotPublish
)

func (s LockSite) String() string {
	switch s {
	case LockSiteCommit:
		return "commit"
	case LockSiteFlushPublish:
		return "flush_publish"
	case LockSiteCompactionPublish:
		return "compaction_publish"
	case LockSiteCompactionStart:
		return "compaction_start"
	case LockSiteCompactionSnapshotPublish:
		return "compaction_snapshot_publish"
	default:
		return "unknown"
	}
}

// SetSyncObserver attaches instrumentation. clock must be a monotonic
// nanosecond function (the I7 harness injects the Windows QPC clock). Passing
// a nil observer detaches instrumentation. An observer without a clock is
// rejected: durations measured on a coarse wall clock would be silently
// quantised. The observer must be attached before concurrent engine use; the
// fields are otherwise read without synchronization on the hot paths.
func (m *Manager) SetSyncObserver(obs SyncObserver, clock func() int64) error {
	if obs != nil && clock == nil {
		return errors.New("sync instrumentation requires a monotonic nanosecond clock")
	}
	m.syncObserver = obs
	m.instrumentClock = clock
	m.commitTraceObserver, _ = obs.(CommitTraceObserver)
	return nil
}

// CommitPhases records successful commit boundaries on instrumentClock's timeline.
// For multi-key commits the WAL boundaries describe the last key.
type CommitPhases struct {
	ArrivalNs, AcquiredNs, ValidatedNs, WalWriteDoneNs, WalSyncDoneNs, AppliedNs, ReleasedNs, FlushCheckDoneNs int64
}

// CommitTraceObserver optionally extends SyncObserver with commit timing.
// Callbacks have the same non-blocking, lock-free, allocation-free contract.
type CommitTraceObserver interface {
	OnCommitPhases(p CommitPhases)
}

// ActiveRLockHolders returns the current count of goroutines between rLock and
// rUnlock on Manager.Mu (see the overcounting caveat in this file's header).
func (m *Manager) ActiveRLockHolders() int32 {
	return m.activeRLockHolders.Load()
}

// rLock is the single read-path entry point for Manager.Mu. Per the
// pre-registered R1.2e sketch the holder count is incremented before the
// RLock, so the counter is a one-sided (never-undercounting) estimate.
func (m *Manager) rLock() {
	m.activeRLockHolders.Add(1)
	m.Mu.RLock()
}

// rUnlock is the single read-path exit point for Manager.Mu.
func (m *Manager) rUnlock() {
	m.Mu.RUnlock()
	m.activeRLockHolders.Add(-1)
}

// readCSStart returns a timestamp for critical-section timing, or 0 when no
// observer is attached (the value is then meaningless and never used).
func (m *Manager) readCSStart() int64 {
	if m.syncObserver == nil {
		return 0
	}
	return m.instrumentClock()
}

// rUnlockObserved releases the read lock and, when instrumentation is
// attached, reports the read critical-section duration since startNs.
// All read-path exits must use this or rUnlock; nothing may call
// m.Mu.RUnlock() directly, or the holder counter drifts.
func (m *Manager) rUnlockObserved(mode SyncMode, startNs int64) {
	if m.syncObserver != nil {
		d := m.instrumentClock() - startNs
		if d < 0 {
			d = 0
		}
		m.syncObserver.OnReadCriticalSection(mode, d)
	}
	m.rUnlock()
}

// writerLockObs is the arrival record for one writer Lock attempt. The zero
// value is inert: when no observer is attached both methods are no-ops and
// the site costs nothing beyond the nil check.
type writerLockObs struct {
	site     LockSite
	startNs  int64
	holders  int32
	observed bool
}

// writerLockArrival samples the active holder count and start time
// immediately before a writer lock attempt. snapshotMu sites have no Mu holder sample.
func (m *Manager) writerLockArrival(site LockSite) writerLockObs {
	if m.syncObserver == nil {
		return writerLockObs{}
	}
	holders := int32(0)
	if site != LockSiteCompactionSnapshotPublish {
		holders = m.activeRLockHolders.Load()
	}
	return writerLockObs{
		site:     site,
		startNs:  m.instrumentClock(),
		holders:  holders,
		observed: true,
	}
}

// acquired reports the wait once the observed lock has been acquired.
func (w *writerLockObs) acquired(m *Manager) {
	if !w.observed {
		return
	}
	w.acquiredAt(m, m.instrumentClock())
}

func (w *writerLockObs) acquiredAt(m *Manager, acquiredNs int64) {
	if !w.observed {
		return
	}
	d := acquiredNs - w.startNs
	if d < 0 {
		d = 0
	}
	m.syncObserver.OnWriterLockWait(w.site, d, w.holders)
}

// publicationObs times one exclusive publication hold. The zero value is inert.
type publicationObs struct {
	site     LockSite
	startNs  int64
	holders  int32
	observed bool
}

// beginPublication starts timing an exclusive publication hold immediately
// after Manager.Mu has been acquired for writing. holdersAtArrival must be
// the arrival sample from the writerLockArrival that preceded the Lock.
func (m *Manager) beginPublication(site LockSite, holdersAtArrival int32) publicationObs {
	if m.syncObserver == nil {
		return publicationObs{}
	}
	return publicationObs{
		site:     site,
		startNs:  m.instrumentClock(),
		holders:  holdersAtArrival,
		observed: true,
	}
}

// end reports the publication hold; call it while the lock is still held,
// immediately before the release.
func (p *publicationObs) end(m *Manager) {
	if !p.observed {
		return
	}
	d := m.instrumentClock() - p.startNs
	if d < 0 {
		d = 0
	}
	m.syncObserver.OnPublication(p.site, d, p.holders)
}

// CompactionObserver optionally extends SyncObserver with lifecycle observations.
// Callbacks have the same non-blocking, thread-safe contract as SyncObserver.
type CompactionObserver interface {
	OnCompaction(durationNs int64, inputs int)
	OnTableRetired()
	OnTableRemoved(lagNs int64)
}

// retirementObs is installed before retirement is visible to releasing readers.
// removed guards the observer only; it never participates in reclamation.
type retirementObs struct {
	observer CompactionObserver
	clock    func() int64
	startNs  int64
	removed  atomic.Bool
}

func (m *Manager) observeRetirement() *retirementObs {
	if m.syncObserver == nil {
		return nil
	}
	obs, ok := m.syncObserver.(CompactionObserver)
	if !ok {
		return nil
	}
	r := &retirementObs{observer: obs, clock: m.instrumentClock, startNs: m.instrumentClock()}
	obs.OnTableRetired()
	return r
}

func (r *retirementObs) removal() {
	if r == nil || !r.removed.CompareAndSwap(false, true) {
		return
	}
	r.observer.OnTableRemoved(r.clock() - r.startNs)
}

func (m *Manager) observeCompaction(startNs int64, inputs int) {
	if m.syncObserver != nil {
		if obs, ok := m.syncObserver.(CompactionObserver); ok {
			obs.OnCompaction(m.instrumentClock()-startNs, inputs)
		}
	}
}
