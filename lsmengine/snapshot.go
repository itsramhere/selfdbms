package lsmengine

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// SnapshotState represents the logical lifecycle phase of an immutable SSTable reader-set snapshot.
type SnapshotState int32

const (
	SnapshotConstructing SnapshotState = iota
	SnapshotPublished
	SnapshotPinned
	SnapshotRetired
	SnapshotDrained
	SnapshotClosed
	SnapshotReclaimed
)

func (s SnapshotState) String() string {
	switch s {
	case SnapshotConstructing:
		return "Constructing"
	case SnapshotPublished:
		return "Published"
	case SnapshotPinned:
		return "Pinned"
	case SnapshotRetired:
		return "Retired"
	case SnapshotDrained:
		return "Drained"
	case SnapshotClosed:
		return "Closed"
	case SnapshotReclaimed:
		return "Reclaimed"
	default:
		return "Unknown"
	}
}

// SnapshotShutdownPhase represents the snapshot subsystem lifecycle phase during shutdown.
// Synchronized exclusively under Manager.snapshotMu.
type SnapshotShutdownPhase int32

const (
	SnapshotPhaseOpen SnapshotShutdownPhase = iota
	SnapshotPhaseDraining
	SnapshotPhaseClosed
)

func (s SnapshotShutdownPhase) String() string {
	switch s {
	case SnapshotPhaseOpen:
		return "Open"
	case SnapshotPhaseDraining:
		return "Draining"
	case SnapshotPhaseClosed:
		return "Closed"
	default:
		return "Unknown"
	}
}

// SharedReaderState represents the physical lifecycle phase of an SSTable reader and file descriptor.
type SharedReaderState int32

const (
	SharedReaderUnpublished SharedReaderState = iota
	SharedReaderActive
	SharedReaderObsolete
	SharedReaderClosing
	SharedReaderClosed
	SharedReaderUnlinking
	SharedReaderReclaimed
	SharedReaderClosedValid
	SharedReaderFailed
)

func (s SharedReaderState) String() string {
	switch s {
	case SharedReaderUnpublished:
		return "Unpublished"
	case SharedReaderActive:
		return "Active"
	case SharedReaderObsolete:
		return "Obsolete"
	case SharedReaderClosing:
		return "Closing"
	case SharedReaderClosed:
		return "Closed"
	case SharedReaderUnlinking:
		return "Unlinking"
	case SharedReaderReclaimed:
		return "Reclaimed"
	case SharedReaderClosedValid:
		return "ClosedValid"
	case SharedReaderFailed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// Authoritative Synchronization Contract:
// Manager.snapshotMu is the sole authoritative synchronization guard for all compound
// lifecycle transitions, reference mutations, and cleanup scheduling on both ReaderSnapshot
// and SharedReader. Atomic fields (refCount, state, obsolete, pins) provide thread-safe
// read-only observability for diagnostics, test assertions, and metrics without holding locks;
// they do NOT provide compound atomicity for multi-field state transitions. All state changes
// (Active -> Obsolete, pins adjustment, refCount mutations, snapshot publication/retirement)
// MUST hold Manager.snapshotMu.

// SharedReader wraps an SSTableReader with an independent reference count, physical state, and safe retirement metadata.
// Multiple snapshots or compaction routines can concurrently hold references to the same SharedReader.
type SharedReader struct {
	Reader    *SSTableReader
	Path      string
	refCount  atomic.Int64 // Guarded by Manager.snapshotMu for compound transitions; atomic for read diagnostics
	state     atomic.Int32 // Stores SharedReaderState; guarded by Manager.snapshotMu for state transitions
	obsolete  atomic.Bool  // Guarded by Manager.snapshotMu; marked true post-commit
	closeOnce sync.Once
	closeErr  error
	unlinkErr error
}

// NewSharedReader constructs a new SharedReader around an open SSTableReader.
// Initial state is SharedReaderUnpublished, with 1 reference owned by the builder.
func NewSharedReader(reader *SSTableReader, path string) *SharedReader {
	sr := &SharedReader{
		Reader: reader,
		Path:   path,
	}
	sr.refCount.Store(1)
	sr.state.Store(int32(SharedReaderUnpublished))
	sr.obsolete.Store(false)
	return sr
}

// Ref increments the reference count of the shared reader.
// Prohibits reference resurrection after closure eligibility.
// In production code, caller MUST hold Manager.snapshotMu.
func (sr *SharedReader) Ref() {
	st := sr.State()
	if st == SharedReaderClosing || st == SharedReaderClosed ||
		st == SharedReaderUnlinking ||
		st == SharedReaderClosedValid || st == SharedReaderReclaimed ||
		st == SharedReaderFailed {
		panic("FATAL: prohibited reference resurrection on closing/reclaimed SharedReader")
	}
	sr.refCount.Add(1)
}

// RefCount returns the current reference count.
// Thread-safe read-only diagnostic accessor.
func (sr *SharedReader) RefCount() int64 {
	return sr.refCount.Load()
}

// State returns the physical lifecycle state of the shared reader.
// Thread-safe read-only diagnostic accessor.
func (sr *SharedReader) State() SharedReaderState {
	return SharedReaderState(sr.state.Load())
}

// SetState updates the physical lifecycle state of the shared reader.
// In production code, caller MUST hold Manager.snapshotMu.
func (sr *SharedReader) SetState(s SharedReaderState) {
	sr.state.Store(int32(s))
}

// IsObsolete returns whether the reader has been marked obsolete by a committed snapshot.
func (sr *SharedReader) IsObsolete() bool {
	return sr.obsolete.Load()
}

// MarkObsoleteLocked marks the reader obsolete under Manager.snapshotMu.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (sr *SharedReader) MarkObsoleteLocked() {
	sr.obsolete.Store(true)
	if sr.State() == SharedReaderActive {
		sr.SetState(SharedReaderObsolete)
	}
}

// UnrefLocked decrements the reference count under Manager.snapshotMu.
// Returns the updated reference count.
// Panics on reference count underflow.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (sr *SharedReader) UnrefLocked() int64 {
	curr := sr.refCount.Load()
	if curr <= 0 {
		panic("FATAL: SharedReader reference count underflow detected")
	}
	return sr.refCount.Add(-1)
}

// CloseError returns any error encountered during physical handle closure.
func (sr *SharedReader) CloseError() error {
	return sr.closeErr
}

// UnlinkError returns any error encountered during file unlinking.
func (sr *SharedReader) UnlinkError() error {
	return sr.unlinkErr
}

// ReaderSnapshot represents an immutable, point-in-time view of active SSTable readers.
// Once published, the readers slice is strictly read-only and never mutated.
type ReaderSnapshot struct {
	id        uint64
	state     atomic.Int32 // Stores SnapshotState; guarded by Manager.snapshotMu for lifecycle transitions
	readers   []*SharedReader
	pins      atomic.Int64 // Guarded by Manager.snapshotMu for pin/drain decisions; atomic for diagnostics
	retiredAt time.Time
}

// NewReaderSnapshot constructs a private, immutable ReaderSnapshot with given ID and readers.
func NewReaderSnapshot(id uint64, constituentReaders []*SharedReader) *ReaderSnapshot {
	readersCopy := make([]*SharedReader, len(constituentReaders))
	copy(readersCopy, constituentReaders)
	snap := &ReaderSnapshot{
		id:      id,
		readers: readersCopy,
	}
	snap.SetState(SnapshotConstructing)
	return snap
}

// ID returns the unique generation sequence of the snapshot.
func (rs *ReaderSnapshot) ID() uint64 {
	return rs.id
}

// State returns the current lifecycle state of the snapshot.
// Thread-safe read-only diagnostic accessor.
func (rs *ReaderSnapshot) State() SnapshotState {
	return SnapshotState(rs.state.Load())
}

// SetState updates the lifecycle state of the snapshot.
// In production code, caller MUST hold Manager.snapshotMu.
func (rs *ReaderSnapshot) SetState(s SnapshotState) {
	rs.state.Store(int32(s))
}

// Pins returns the current active pin count of the snapshot.
// Thread-safe read-only diagnostic accessor.
func (rs *ReaderSnapshot) Pins() int64 {
	return rs.pins.Load()
}

// Readers returns a read-only slice view of the snapshot's constituent readers.
func (rs *ReaderSnapshot) Readers() []*SharedReader {
	return rs.readers
}

// AddPinLocked increments the active pin count on the snapshot.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (rs *ReaderSnapshot) AddPinLocked() int64 {
	newPins := rs.pins.Add(1)
	if rs.State() == SnapshotPublished {
		rs.SetState(SnapshotPinned)
	}
	return newPins
}

// ReleasePinLocked decrements the active pin count on the snapshot.
// Panics on pin underflow.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (rs *ReaderSnapshot) ReleasePinLocked() int64 {
	curr := rs.pins.Load()
	if curr <= 0 {
		panic("FATAL: snapshot pin underflow detected")
	}
	newPins := rs.pins.Add(-1)
	if newPins == 0 {
		if rs.State() == SnapshotPinned {
			rs.SetState(SnapshotPublished)
		}
	}
	return newPins
}

// ReaderSyncStrategy defines the internal strategy interface distinguishing Approach A
// (Manager-wide RWMutex) from Approach B (Immutable Snapshot Pinning).
type ReaderSyncStrategy interface {
	Name() string
	Mode() SyncMode
}

// acquireSnapshotLocked pins the current active snapshot.
// PRECONDITION: Manager.Mu (RLock or Lock) MUST be held by caller.
func (m *Manager) acquireSnapshotLocked() (*ReaderSnapshot, error) {
	if m.snapshotAdmissionClosed.Load() {
		return nil, ErrClosed
	}

	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()

	if m.snapshotPhase >= SnapshotPhaseDraining {
		return nil, ErrClosed
	}

	snap := m.currentSnapshot
	if snap == nil {
		snap = &ReaderSnapshot{
			id: atomic.AddUint64(&m.snapshotSeq, 1),
		}
		snap.SetState(SnapshotPublished)
		m.currentSnapshot = snap
	}

	snap.AddPinLocked()
	return snap, nil
}

// AcquireSnapshot acquires a pin on the current snapshot for point reads.
// Locks Manager.Mu.RLock() briefly to coordinate with memory tiers, pins snapshot, and releases Manager.Mu.
func (m *Manager) AcquireSnapshot() (*ReaderSnapshot, error) {
	m.Mu.RLock()
	defer m.Mu.RUnlock()
	return m.acquireSnapshotLocked()
}

// ReleaseSnapshot releases an active pin on a snapshot.
// PRECONDITION: Zero locks held at entry; acquires m.snapshotMu internally.
func (m *Manager) ReleaseSnapshot(snap *ReaderSnapshot) {
	if snap == nil {
		return
	}

	var shouldCleanup bool
	m.snapshotMu.Lock()
	defer func() {
		m.snapshotMu.Unlock()
		if shouldCleanup {
			go m.drainPendingCleanups()
		}
	}()

	remainingPins := snap.ReleasePinLocked()

	if remainingPins == 0 && snap.State() == SnapshotRetired {
		snap.SetState(SnapshotDrained)
		m.reclaimSnapshotLocked(snap)
		if m.snapshotDrainCond != nil {
			m.snapshotDrainCond.Broadcast()
		}
		shouldCleanup = m.hasPendingCleanups
	}
}

// retireSnapshotLocked moves an active snapshot to retiredSnapshots.
// If it has 0 active pins, it is immediately drained and reclaimed.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (m *Manager) retireSnapshotLocked(snap *ReaderSnapshot) {
	if snap == nil {
		return
	}
	snap.SetState(SnapshotRetired)
	snap.retiredAt = time.Now()

	if snap.Pins() == 0 {
		snap.SetState(SnapshotDrained)
		m.reclaimSnapshotLocked(snap)
	} else {
		m.retiredSnapshots = append(m.retiredSnapshots, snap)
	}
}

// reclaimSnapshotLocked dereferences constituent readers of a drained snapshot.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (m *Manager) reclaimSnapshotLocked(snap *ReaderSnapshot) {
	snap.SetState(SnapshotReclaimed)

	// Dereference each constituent reader
	for _, sr := range snap.readers {
		sr.UnrefLocked()
		m.checkCleanupEligibilityLocked(sr)
	}

	// Remove from retiredSnapshots slice
	for i, s := range m.retiredSnapshots {
		if s == snap {
			m.retiredSnapshots = append(m.retiredSnapshots[:i], m.retiredSnapshots[i+1:]...)
			break
		}
	}
}

// countTotalActivePinsLocked counts pins across current and retired snapshots.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (m *Manager) countTotalActivePinsLocked() int64 {
	var total int64
	if m.currentSnapshot != nil {
		total += m.currentSnapshot.Pins()
	}
	for _, snap := range m.retiredSnapshots {
		total += snap.Pins()
	}
	return total
}

// checkCleanupEligibilityLocked evaluates whether a SharedReader has become eligible
// for handle closure or physical unlinking.
// PRECONDITION: m.snapshotMu MUST be held by the caller.
// Invariant: ZERO access to Manager.Mu-guarded mutable state (e.g. m.state).
func (m *Manager) checkCleanupEligibilityLocked(sr *SharedReader) {
	if sr == nil {
		return
	}

	refs := sr.refCount.Load()
	isObsolete := sr.obsolete.Load()
	currentState := sr.State()

	// Prohibit reference resurrection or duplicate queueing if already terminal/closing
	if currentState == SharedReaderClosing || currentState == SharedReaderClosed ||
		currentState == SharedReaderClosedValid || currentState == SharedReaderReclaimed ||
		currentState == SharedReaderFailed {
		return
	}

	// Case A: Obsolete superseded table whose references have all drained to zero.
	// Eligible for handle closure followed by physical unlinking.
	if refs == 0 && isObsolete {
		if currentState == SharedReaderActive || currentState == SharedReaderObsolete {
			sr.SetState(SharedReaderClosing)
			m.pendingCleanupReaders = append(m.pendingCleanupReaders, sr)
			m.hasPendingCleanups = true
		}
		return
	}

	// Case B: Engine shutdown with valid surviving table.
	// Inspected strictly via snapshotMu-guarded m.snapshotPhase (never Manager.Mu state).
	// The published snapshot reference was released (refs == 0), but the table is NOT obsolete.
	// Eligible for handle closure, but physical file MUST be preserved on disk!
	if m.snapshotPhase >= SnapshotPhaseDraining && refs == 0 && !isObsolete {
		if currentState == SharedReaderActive {
			sr.SetState(SharedReaderClosedValid)
			m.pendingShutdownCloses = append(m.pendingShutdownCloses, sr)
		}
		return
	}

	// Case C: Abandoned unpublished table (e.g. failed snapshot construction or build rollback).
	// Never published to active snapshots (isObsolete == false); refs == 0.
	// Initial builder reference relinquished via rollback; must be closed and deleted.
	if refs == 0 && currentState == SharedReaderUnpublished {
		sr.SetState(SharedReaderClosing)
		m.pendingCleanupReaders = append(m.pendingCleanupReaders, sr)
		m.hasPendingCleanups = true
		return
	}
}

// rollbackUnpublishedReader releases the builder's initial reference on an unpublished SSTable reader
// when construction or publication fails.
// PRECONDITION: Zero locks required at entry; acquires m.snapshotMu internally.
func (m *Manager) rollbackUnpublishedReader(sr *SharedReader) {
	if sr == nil {
		return
	}
	m.snapshotMu.Lock()
	sr.UnrefLocked()
	m.checkCleanupEligibilityLocked(sr)
	m.snapshotMu.Unlock()

	// Drain immediately
	m.drainPendingCleanups()
}

// evaluateShutdownReaderCleanupLocked checks cleanup eligibility for all retired snapshot readers on shutdown.
// PRECONDITION: Manager.snapshotMu MUST be held by caller.
func (m *Manager) evaluateShutdownReaderCleanupLocked() {
	for _, snap := range m.retiredSnapshots {
		for _, sr := range snap.readers {
			m.checkCleanupEligibilityLocked(sr)
		}
	}
}

// drainPendingCleanups executes handle closures and unlinks outside all engine locks.
func (m *Manager) drainPendingCleanups() error {
	m.cleanupMu.Lock()
	defer m.cleanupMu.Unlock()

	var firstErr error

	// Step 1: Drain pending shutdown closes (valid database files surviving shutdown)
	m.snapshotMu.Lock()
	shutdownCloses := make([]*SharedReader, len(m.pendingShutdownCloses))
	copy(shutdownCloses, m.pendingShutdownCloses)
	m.pendingShutdownCloses = nil
	m.snapshotMu.Unlock()

	for _, sr := range shutdownCloses {
		sr.closeOnce.Do(func() {
			if sr.Reader != nil {
				sr.closeErr = sr.Reader.Close()
			}
		})
		if sr.closeErr != nil {
			sr.SetState(SharedReaderFailed)
			if firstErr == nil {
				firstErr = sr.closeErr
			}
		} else {
			sr.SetState(SharedReaderClosedValid)
		}
	}

	// Step 2: Drain pending cleanups (obsolete or unpublished abandoned tables)
	m.snapshotMu.Lock()
	cleanups := make([]*SharedReader, len(m.pendingCleanupReaders))
	copy(cleanups, m.pendingCleanupReaders)
	m.pendingCleanupReaders = nil
	m.hasPendingCleanups = false
	m.snapshotMu.Unlock()

	for _, sr := range cleanups {
		if err := m.drainSingleReaderCleanup(sr); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// drainSingleReaderCleanup closes handle and unlinks a single SharedReader.
func (m *Manager) drainSingleReaderCleanup(sr *SharedReader) error {
	if sr == nil {
		return nil
	}

	// Phase 1: Physical File Handle Close (Exactly-once via closeOnce)
	sr.closeOnce.Do(func() {
		if sr.Reader != nil {
			sr.closeErr = sr.Reader.Close()
		}
	})

	if sr.closeErr != nil {
		sr.SetState(SharedReaderFailed)
		return sr.closeErr // Terminal failure; unlinking strictly aborted!
	}
	sr.SetState(SharedReaderClosed)

	// Phase 2: Physical File Unlink (Executed ONLY if handle close succeeded)
	if sr.Path != "" {
		sr.SetState(SharedReaderUnlinking)
		var removeErr error
		for attempt := 0; attempt < 3; attempt++ {
			removeErr = os.Remove(sr.Path)
			if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if removeErr != nil {
			sr.unlinkErr = removeErr
			sr.SetState(SharedReaderFailed)
			return removeErr
		}
	}

	sr.SetState(SharedReaderReclaimed)
	return nil
}

// publishFlushLockedApproachB publishes a flushed SSTable into a new ReaderSnapshot.
// PRECONDITION: Manager.Mu.Lock() is held by the caller (flushLoop / flushToDisk).
func (m *Manager) publishFlushLockedApproachB(mem *Memtable, newSR *SharedReader) error {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()

	// Permitted in EngineOpen and EngineClosing (INV-SNAP-7 / D-025).
	// Rejected strictly when EngineClosed.
	if m.state == EngineClosed {
		newSR.SetState(SharedReaderClosing)
		m.pendingCleanupReaders = append(m.pendingCleanupReaders, newSR)
		m.hasPendingCleanups = true
		return ErrClosed
	}

	// 1. Construct new reader slice privately: copy existing readers, append newSR
	oldSnap := m.currentSnapshot
	var newReaders []*SharedReader
	if oldSnap != nil && len(oldSnap.readers) > 0 {
		newReaders = make([]*SharedReader, len(oldSnap.readers)+1)
		copy(newReaders, oldSnap.readers)
		for _, sr := range oldSnap.readers {
			sr.Ref() // Increment refCount for shared reader copied to new snapshot
		}
		newReaders[len(oldSnap.readers)] = newSR
	} else {
		newReaders = []*SharedReader{newSR}
	}

	// Transfer builder ownership: transition newSR from Unpublished to Active
	newSR.SetState(SharedReaderActive)

	// 2. Allocate and publish new immutable snapshot
	newSnap := &ReaderSnapshot{
		id:      atomic.AddUint64(&m.snapshotSeq, 1),
		readers: newReaders,
	}
	newSnap.SetState(SnapshotPublished)
	m.currentSnapshot = newSnap

	// 3. Retire old snapshot
	if oldSnap != nil {
		m.retireSnapshotLocked(oldSnap)
	}

	// 4. Atomically remove corresponding flushed Memtable from Frozenmem
	// LINEARIZATION POINT: Key is continuously discoverable in memory or snapshot.
	m.removeFrozenMemtableLocked(mem)

	return nil
}

// getApproachB executes a point read under Approach B.
// Inspects active and frozen Memtables under short Manager.Mu.RLock(), pins the active snapshot,
// releases Manager.Mu, and performs all SSTable block reads and binary searches with ZERO locks held.
func (m *Manager) getApproachB(key string, readTs uint64) (string, error) {
	byteKey := []byte(key)

	// Step 1: Memory Tier Inspection + Snapshot Pinning under short m.Mu.RLock()
	m.Mu.RLock()
	if m.state == EngineClosed {
		m.Mu.RUnlock()
		return "", ErrClosed
	}

	if m.duringMemtableReadHook != nil {
		m.duringMemtableReadHook()
	}

	// 1a. Check active Memtable
	sol, err := m.Mem.Get(byteKey, readTs)
	if err == nil {
		m.Mu.RUnlock()
		return string(sol), nil
	} else if err == ErrKeyDeleted {
		m.Mu.RUnlock()
		return "Key is Deleted", err
	} else if !errors.Is(err, ErrNotFound) {
		m.Mu.RUnlock()
		return "", err
	}

	// 1b. Check frozen Memtables (newest to oldest)
	for i := len(m.Frozenmem) - 1; i >= 0; i-- {
		val, err := m.Frozenmem[i].Get(byteKey, readTs)
		if err == nil {
			m.Mu.RUnlock()
			return string(val), nil
		} else if err == ErrKeyDeleted {
			m.Mu.RUnlock()
			return " ", err
		} else if !errors.Is(err, ErrNotFound) {
			m.Mu.RUnlock()
			return "", err
		}
	}

	// 1c. Pin current disk snapshot before releasing m.Mu!
	snap, err := m.acquireSnapshotLocked()
	m.Mu.RUnlock() // CRUCIAL: Release Manager.Mu before ANY slow disk I/O!

	if err != nil {
		return "", err
	}
	defer m.ReleaseSnapshot(snap)

	if m.duringPointReadHook != nil {
		m.duringPointReadHook()
	}

	// Step 2: Disk Tier Inspection using pinned snapshot (ZERO locks held!)
	// Search constituent readers in reverse chronological order (newest to oldest).
	readers := snap.Readers()
	for i := len(readers) - 1; i >= 0; i-- {
		sr := readers[i]
		if sr == nil || sr.Reader == nil {
			continue
		}
		val, err := sr.Reader.Get(byteKey, readTs)
		if err == nil {
			return string(val), nil
		} else if err == ErrKeyDeleted {
			return " ", err
		} else if errors.Is(err, ErrNotFound) {
			continue
		} else {
			return "", err
		}
	}

	return " ", ErrNotFound
}

// closeApproachB executes a coordinated shutdown under Approach B.
func (m *Manager) closeApproachB() error {
	// 1. Initiate shutdown or wait for in-progress shutdown to complete (CP2b-V.2 / INV-SNAP-8)
	m.Mu.Lock()
	if m.state != EngineOpen {
		m.Mu.Unlock()
		// Concurrent callers wait outside ALL locks until teardown finishes!
		<-m.closeDone
		m.Mu.Lock()
		err := m.closeErr
		m.Mu.Unlock()
		return err
	}
	m.state = EngineClosing
	m.isClosed = true

	// Close query pin admission under snapshotMu
	m.snapshotMu.Lock()
	m.snapshotPhase = SnapshotPhaseDraining
	m.snapshotAdmissionClosed.Store(true)
	m.snapshotMu.Unlock()
	m.Mu.Unlock() // Crucial: Release Manager.Mu before drain waits!

	// 2. Coordinate background compaction (Choice B, CP2b-V.3)
	m.compactionMu.Lock()
	m.compactionAdmissionClosed = true
	m.compactionMu.Unlock() // Crucial: Release compactionMu BEFORE waiting on flushes/pins!

	// 3. Drain previously admitted flushes
	m.flushQueueMu.Lock()
	m.flushQueueClosed = true
	if m.flushQueueCond != nil {
		m.flushQueueCond.Broadcast()
	}
	m.flushQueueMu.Unlock()
	m.flushWg.Wait() // Waits for admitted flushes (publishFlushLocked succeeds in EngineClosing!)

	// 4. Retire current published snapshot and wait for query pins
	m.snapshotMu.Lock()
	if m.currentSnapshot != nil {
		m.retireSnapshotLocked(m.currentSnapshot)
		m.currentSnapshot = nil
	}

	// Wait for in-flight pins to reach zero across all retired snapshots.
	for m.countTotalActivePinsLocked() > 0 {
		m.snapshotDrainCond.Wait()
	}

	// 5. All pins drained: evaluate cleanup eligibility for all readers
	m.evaluateShutdownReaderCleanupLocked()
	m.snapshotPhase = SnapshotPhaseClosed
	m.snapshotMu.Unlock()

	// 6. Execute physical handle closures and unlinks outside all locks
	cleanupErr := m.drainPendingCleanups()

	// 7. Close WAL file outside all locks
	var walErr error
	if m.Log != nil && m.Log.File != nil {
		walErr = m.Log.File.Close()
	}

	// 8. Determine unified final error and transition to EngineClosed
	var finalErr error
	if m.flushErr != nil {
		finalErr = m.flushErr
	} else if cleanupErr != nil {
		finalErr = cleanupErr
	} else {
		finalErr = walErr
	}

	m.Mu.Lock()
	m.state = EngineClosed
	m.closeErr = finalErr
	m.Mu.Unlock()

	// 9. Unblock all waiting concurrent Close() callers
	close(m.closeDone)

	return finalErr
}

// SnapshotSeq returns the current snapshot generation sequence.
func (m *Manager) SnapshotSeq() uint64 {
	return atomic.LoadUint64(&m.snapshotSeq)
}

// ActiveSnapshotPins returns the total active pins across all snapshots.
func (m *Manager) ActiveSnapshotPins() int64 {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	return m.countTotalActivePinsLocked()
}

// RetiredSnapshotCount returns the count of retired snapshots currently draining.
func (m *Manager) RetiredSnapshotCount() int {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	return len(m.retiredSnapshots)
}

// PendingCleanupCount returns the count of readers queued for physical cleanup.
func (m *Manager) PendingCleanupCount() int {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	return len(m.pendingCleanupReaders) + len(m.pendingShutdownCloses)
}

// LivePhysicalReaders returns the number of SSTable readers in the current snapshot.
func (m *Manager) LivePhysicalReaders() int {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	if m.currentSnapshot == nil {
		return 0
	}
	return len(m.currentSnapshot.readers)
}
