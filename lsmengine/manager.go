package lsmengine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type EngineState int

const (
	EngineOpen EngineState = iota
	EngineClosing
	EngineClosed
)

type SyncMode int

const (
	// SyncModeApproachA represents the Manager-wide RWMutex baseline (Approach A).
	// Reader-compactor synchronization relies entirely on Manager.Mu (default sync.RWMutex),
	// serialized compaction lifetimes (compactionMu), and safe reader retirement.
	SyncModeApproachA SyncMode = iota

	// SyncModeApproachB represents atomic pointer reader snapshot swap (Approach B).
	// Readers acquire short-lived immutable reader-set snapshots without holding Manager.Mu across disk I/O.
	SyncModeApproachB

	// SyncModeApproachAMin keeps Approach A's single Manager.Mu RWMutex but releases
	// the read lock before any SSTable scan. Correctness is preserved by a per-table
	// lease (in-flight counter) taken while still holding RLock; superseded tables are
	// closed and unlinked only after their last lease is released.
	// It has no version chain, no snapshot descriptor and no cleanup worker.
	SyncModeApproachAMin
)

func (m SyncMode) String() string {
	switch m {
	case SyncModeApproachA:
		return "ApproachA_RWMutexBaseline"
	case SyncModeApproachB:
		return "ApproachB_ReaderSnapshot"
	case SyncModeApproachAMin:
		return "ApproachAMin_Lease"
	default:
		return "Unknown"
	}
}

var ErrNotFound = errors.New("key not found")
var ErrKeyDeleted = errors.New("key has been deleted")
var ErrClosed = errors.New("engine is closed")
var ErrUnsupportedSyncMode = errors.New("unsupported synchronization mode: Approach B is not implemented")

type Config struct {
	Mode                      SyncMode
	WalPath                   string
	DataDir                   string        // Optional directory for SSTables and WAL files
	MemtableThresholdBytes    int           // Optional flush threshold in bytes (default: 64MB if <= 0)
	ReadPath                  ReadPathMode  // Shared SSTable point-read path (default: block reads).
	ReadIODelay               time.Duration // Experiment-only delay per block read; ignored by legacy scans.
	CompactionOpenOutsideLock bool          // Experiment-only; false preserves opening under the start hold.
	// InlineFlushCheck is experiment-only; false keeps the existing commit path.
	InlineFlushCheck bool
	LockPolicy       string // Experiment-only: empty/go (default), phasefair, readerpref.
	DisableWALSync   bool   // Skip WAL fsync when true (default: sync each record).
}

func DefaultConfig() Config {
	return Config{
		Mode:    SyncModeApproachA,
		WalPath: "wal.log",
	}
}

type Manager struct {
	syncMode                    SyncMode
	readPath                    ReadPathMode
	readIODelay                 time.Duration
	disableWALSync              atomic.Bool
	compactionOpenOutsideLock   bool
	inlineFlushCheck            bool
	commitAcquisitionHook       func() // Test hook for the two commit-path acquisitions; nil otherwise.
	CurrentSeqNo                atomic.Uint64
	Mu                          RWLocker
	compactionMu                sync.Mutex
	isClosed                    bool
	state                       EngineState
	flushWg                     sync.WaitGroup
	flushErr                    error
	flushQueue                  []*Memtable
	flushQueueMu                sync.Mutex
	flushQueueCond              *sync.Cond
	flushDoneCond               *sync.Cond
	flushQueueClosed            bool
	isFlushing                  bool
	flushObserver               FlushObserver
	Mem                         *Memtable
	Log                         *Wal
	Frozenmem                   []*Memtable
	ActiveSSreaders             []*SSTableReader
	beforeCompactionPublishHook func()
	duringCompactionMergeHook   func()
	compactionIteratorOpenHook  func(string) (*SSTableIterator, error) // Test-only opener; nil uses NewSSTableIterator.
	duringPointReadHook         func()
	duringMemtableReadHook      func()
	beforeFlushPublishHook      func()
	beforeFlushWorkerBeginHook  func()
	flushBuildHook              func() error
	walRotationHook             func() error

	// Deterministic interleaving hooks for regression verification (nil in production builds).
	// afterReleaseSnapshotUnlockHook runs in ReleaseSnapshot immediately after m.snapshotMu
	// is released, exposing the window between the unlock and asynchronous cleanup launch.
	afterReleaseSnapshotUnlockHook func()
	// beforeAsyncCleanupDrainHook runs at the very start of a scheduled asynchronous
	// cleanup goroutine, before any pending cleanup is drained.
	beforeAsyncCleanupDrainHook func()

	// Approach B Snapshot Fields
	snapshotMu                sync.Mutex
	snapshotPhase             SnapshotShutdownPhase
	snapshotAdmissionClosed   atomic.Bool
	snapshotSeq               uint64
	currentSnapshot           *ReaderSnapshot
	retiredSnapshots          []*ReaderSnapshot
	snapshotDrainCond         *sync.Cond
	pendingCleanupReaders     []*SharedReader
	pendingShutdownCloses     []*SharedReader
	hasPendingCleanups        bool
	cleanupMu                 sync.Mutex
	cleanupWg                 sync.WaitGroup
	asyncCleanupErr           error
	compactionAdmissionClosed bool
	closeDone                 chan struct{}
	closeErr                  error

	// Harness & Diagnostic Instrumentation Fields (CP4 / Decision D-036)
	dataDir                string
	memtableThresholdBytes int
	flushesCompleted       atomic.Uint64
	compactionsCompleted   atomic.Uint64
	sstablePointReads      atomic.Uint64
	memtablePointReads     atomic.Uint64

	// Approach A-min fields (Decision D-I7-02). Inert for A and B.
	leasePool         sync.Pool // reuse []*SSTableReader read-set scratch buffers
	cleanupErrMu      sync.Mutex
	cleanupErrs       []error // reclamation errors surfaced by Manager.Close
	retiredReaders    []*SSTableReader
	leaseDrainTimeout time.Duration

	// Publication-injection gate (Phase I7 E2). Inert unless explicitly enabled.
	flushGateMu      sync.Mutex
	flushGateCond    *sync.Cond
	flushGateEnabled bool
	flushGateTokens  int

	// Synchronization instrumentation (Phase I7 engineering preflight). Nil /
	// inert unless SetSyncObserver attaches an observer; see
	// sync_instrumentation.go for the semantics contract.
	// activeRLockHolders is the one exception: it is maintained on every read
	// regardless of instrumentation state (pre-registered EXP-2 arrival
	// sampling), and costs one uncontended atomic add per read-side
	// lock/unlock pair.
	syncObserver        SyncObserver
	commitTraceObserver CommitTraceObserver
	instrumentClock     func() int64
	activeRLockHolders  atomic.Int32
}

var sstableFileSeq atomic.Uint64

func CreateManagerWithConfig(cfg Config) (*Manager, error) {
	if cfg.Mode != SyncModeApproachA && cfg.Mode != SyncModeApproachB && cfg.Mode != SyncModeApproachAMin {
		return nil, fmt.Errorf("unknown synchronization mode: %v", cfg.Mode)
	}
	if cfg.ReadPath != ReadPathBlock && cfg.ReadPath != ReadPathLegacyScan && cfg.ReadPath != ReadPathDirect {
		return nil, fmt.Errorf("unknown read path mode: %v", cfg.ReadPath)
	}
	lock, err := newManagerLock(cfg.LockPolicy)
	if err != nil {
		return nil, err
	}

	walPath := cfg.WalPath
	if walPath == "" {
		walPath = "wal.log"
	}
	if cfg.DataDir != "" && !filepath.IsAbs(walPath) && filepath.Dir(walPath) == "." {
		walPath = filepath.Join(cfg.DataDir, walPath)
	}

	man := new(Manager)
	man.syncMode = cfg.Mode
	man.readPath = cfg.ReadPath
	man.readIODelay = cfg.ReadIODelay
	man.disableWALSync.Store(cfg.DisableWALSync)
	man.inlineFlushCheck = cfg.InlineFlushCheck
	man.compactionOpenOutsideLock = cfg.CompactionOpenOutsideLock
	man.dataDir = cfg.DataDir
	man.memtableThresholdBytes = cfg.MemtableThresholdBytes
	man.CurrentSeqNo.Store(0)
	man.Mu = lock
	man.closeDone = make(chan struct{})
	man.snapshotDrainCond = sync.NewCond(&man.snapshotMu)
	man.snapshotPhase = SnapshotPhaseOpen
	man.leaseDrainTimeout = 5 * time.Second
	man.flushGateCond = sync.NewCond(&man.flushGateMu)

	sk := &SkipList{
		headNode:     &Node{Forward: make([]*Node, 32)},
		maxLevel:     32,
		currentLevel: 1,
	}

	man.Mem = &Memtable{
		SK:        sk,
		SizeBytes: 0,
	}

	man.Log, _ = man.newWal(walPath)
	man.initFlushWorkerLocked()

	if cfg.Mode == SyncModeApproachB {
		man.currentSnapshot = &ReaderSnapshot{
			id: atomic.AddUint64(&man.snapshotSeq, 1),
		}
		man.currentSnapshot.SetState(SnapshotPublished)
	}

	return man, nil
}

// newSSTableReader configures point reads before the reader is published.
func (m *Manager) newSSTableReader(filename string) (*SSTableReader, error) {
	reader, err := NewSSTableReaderWithReadPath(filename, m.readPath)
	if err != nil {
		return nil, err
	}
	reader.ReadPath = m.readPath
	reader.ReadIODelay = m.readIODelay
	return reader, nil
}

func (m *Manager) newWal(filename string) (*Wal, error) {
	wal, err := NewWal(filename)
	if err != nil {
		return nil, err
	}
	wal.DisableSync.Store(m.disableWALSync.Load())
	return wal, nil
}

// SetWALSync changes the policy for the current WAL and all subsequent rotations.
// Call between writer phases; the manager lock serializes WAL replacement while
// atomic flags keep Append's policy read race-free even during a toggle.
func (m *Manager) SetWALSync(enabled bool) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.disableWALSync.Store(!enabled)
	if m.Log != nil {
		m.Log.DisableSync.Store(!enabled)
	}
}

func CreateManager() *Manager {
	man, _ := CreateManagerWithConfig(DefaultConfig())
	return man
}

// SyncMode returns the active synchronization strategy configured on the manager.
func (m *Manager) SyncMode() SyncMode {
	return m.syncMode
}

func (m *Manager) Begin() *Transaction {
	t := &Transaction{}
	t.Man = m
	t.ReadTs = t.Man.CurrentSeqNo.Load()
	t.WriteSet = make(map[string]PendingWrite)
	return t
}

func (m *Manager) Get(key string, readTs uint64) (string, error) {
	switch m.syncMode {
	case SyncModeApproachB:
		return m.getApproachB(key, readTs)
	case SyncModeApproachAMin:
		return m.getApproachAMin(key, readTs)
	}

	byteKey := []byte(key)
	m.rLock()
	defer m.rUnlockObserved(SyncModeApproachA, m.readCSStart())

	if m.state != EngineOpen {
		return "", ErrClosed
	}

	if m.duringMemtableReadHook != nil {
		m.duringMemtableReadHook()
	}

	sol, err := m.Mem.Get(byteKey, readTs)
	if err != nil {
		if errors.Is(err, ErrKeyDeleted) {
			m.memtablePointReads.Add(1)
			return "Key is Deleted", err
		} else if errors.Is(err, ErrNotFound) {
		} else {
			return "", err
		}
	} else {
		m.memtablePointReads.Add(1)
		return string(sol), nil
	}

	for i := len(m.Frozenmem) - 1; i >= 0; i-- {
		val, err := m.Frozenmem[i].Get(byteKey, readTs)
		if err != nil {
			if errors.Is(err, ErrKeyDeleted) {
				m.memtablePointReads.Add(1)
				return " ", err
			} else if errors.Is(err, ErrNotFound) {
				continue
			} else {
				return "", err
			}
		} else {
			m.memtablePointReads.Add(1)
			return string(val), nil
		}
	}

	if m.duringPointReadHook != nil {
		m.duringPointReadHook()
	}

	for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
		if m.ActiveSSreaders[i] == nil {
			return "", errors.New("corrupted reader list: nil reader encountered")
		}
		val, err := m.ActiveSSreaders[i].Get(byteKey, readTs)
		if err != nil {
			if errors.Is(err, ErrKeyDeleted) {
				m.sstablePointReads.Add(1)
				return " ", err
			} else if errors.Is(err, ErrNotFound) {
				continue
			} else {
				return "", err
			}
		} else {
			m.sstablePointReads.Add(1)
			return string(val), nil
		}
	}

	return " ", ErrNotFound

}

// ---------------------------------------------------------------------------
// Approach A-min: one RWMutex; the SSTable scan runs outside the read critical
// section. Correctness rests on a per-table lease taken while RLock is held.
// Invariants and proofs: research/experiments/phase_i7/01_A_MIN_DESIGN.md.
// ---------------------------------------------------------------------------

// leaseScratch is a reusable read-set buffer. The pool holds *leaseScratch so
// that Pool.Put stores a pointer: putting a bare slice header into an interface
// would allocate one box per read and break decision M-7.
type leaseScratch struct {
	buf []*SSTableReader
}

// acquireLeaseScratch returns a scratch buffer whose slice has length n.
func (m *Manager) acquireLeaseScratch(n int) *leaseScratch {
	sc, _ := m.leasePool.Get().(*leaseScratch)
	if sc == nil {
		sc = &leaseScratch{}
	}
	if cap(sc.buf) < n {
		c := n
		if c < 8 {
			c = 8
		}
		sc.buf = make([]*SSTableReader, c)[:n]
	} else {
		sc.buf = sc.buf[:n]
	}
	return sc
}

// releaseLeaseScratch clears references and returns the buffer to the pool.
// The pointer clearing is required: retaining entries past len would keep
// superseded readers reachable and defeat reclamation.
func (m *Manager) releaseLeaseScratch(sc *leaseScratch) {
	if sc == nil || cap(sc.buf) == 0 || cap(sc.buf) > 8192 {
		return
	}
	for i := range sc.buf {
		sc.buf[i] = nil
	}
	sc.buf = sc.buf[:0]
	m.leasePool.Put(sc)
}

func (m *Manager) recordCleanupErr(err error) {
	if err == nil {
		return
	}
	m.cleanupErrMu.Lock()
	m.cleanupErrs = append(m.cleanupErrs, err)
	m.cleanupErrMu.Unlock()
}

func (m *Manager) drainCleanupErrs() error {
	m.cleanupErrMu.Lock()
	defer m.cleanupErrMu.Unlock()
	if len(m.cleanupErrs) == 0 {
		return nil
	}
	err := m.cleanupErrs[0]
	m.cleanupErrs = nil
	return err
}

// reclaimIfDrained frees a superseded table whose leases have all been released.
// Safe only after the table has been removed from ActiveSSreaders under m.Mu.
func (m *Manager) reclaimIfDrained(sr *SSTableReader) {
	if sr == nil || sr.leaseCount.Load() != 0 {
		return
	}
	if err := sr.reclaim(); err != nil {
		m.recordCleanupErr(err)
	}
}

// drainActiveReaderLeases waits until no reader holds a lease on an active or a
// retired table. Called from Close after the engine state has left EngineOpen, so
// no new lease can be granted. A timeout is reported, never ignored: Close then
// fails closed and leaves leased descriptors untouched.
func (m *Manager) drainReaderLeases() error {
	timeout := m.leaseDrainTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		m.rLock()
		outstanding := m.countOutstandingLeasesLocked()
		m.rUnlock()
		if outstanding == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("reader lease drain timeout after %s: %d lease(s) outstanding; no leased descriptor was closed", timeout, outstanding)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// countOutstandingLeasesLocked counts leases over active AND retired tables.
// Retirement is precisely the case the previous implementation missed: a table
// superseded while a reader is scanning keeps its descriptor alive until the
// reader releases, so it must participate in the shutdown drain.
// PRECONDITION: m.Mu is held (read or write).
func (m *Manager) countOutstandingLeasesLocked() int32 {
	var n int32
	for _, r := range m.ActiveSSreaders {
		if r != nil {
			n += r.leaseCount.Load()
		}
	}
	for _, r := range m.retiredReaders {
		if r != nil {
			n += r.leaseCount.Load()
		}
	}
	return n
}

// pruneRetiredReadersLocked drops retired tables that have completed reclamation.
// The list is needed at shutdown to drain outstanding leases on superseded tables
// and to finish their reclamation, so it may only forget tables that are done.
// PRECONDITION: m.Mu must be held for writing.
func (m *Manager) pruneRetiredReadersLocked() {
	if len(m.retiredReaders) <= 64 {
		return
	}
	kept := m.retiredReaders[:0]
	for _, r := range m.retiredReaders {
		if r != nil && !r.reclaimDone.Load() {
			kept = append(kept, r)
		}
	}
	for i := len(kept); i < len(m.retiredReaders); i++ {
		m.retiredReaders[i] = nil
	}
	m.retiredReaders = kept
}

// getApproachAMin executes a point read with a short read critical section.
func (m *Manager) getApproachAMin(key string, readTs uint64) (string, error) {
	byteKey := []byte(key)

	// ---- one RLock window: memory tiers + read-set capture + leases ----
	m.rLock()
	csStart := m.readCSStart()
	if m.state != EngineOpen {
		m.rUnlockObserved(SyncModeApproachAMin, csStart)
		return "", ErrClosed
	}

	if m.duringMemtableReadHook != nil {
		m.duringMemtableReadHook()
	}

	sol, err := m.Mem.Get(byteKey, readTs)
	if err == nil {
		m.memtablePointReads.Add(1)
		m.rUnlockObserved(SyncModeApproachAMin, csStart)
		return string(sol), nil
	}
	if errors.Is(err, ErrKeyDeleted) {
		m.memtablePointReads.Add(1)
		m.rUnlockObserved(SyncModeApproachAMin, csStart)
		return "Key is Deleted", err
	}
	if !errors.Is(err, ErrNotFound) {
		m.rUnlockObserved(SyncModeApproachAMin, csStart)
		return "", err
	}

	for i := len(m.Frozenmem) - 1; i >= 0; i-- {
		val, ferr := m.Frozenmem[i].Get(byteKey, readTs)
		if ferr == nil {
			m.memtablePointReads.Add(1)
			m.rUnlockObserved(SyncModeApproachAMin, csStart)
			return string(val), nil
		}
		if errors.Is(ferr, ErrKeyDeleted) {
			m.memtablePointReads.Add(1)
			m.rUnlockObserved(SyncModeApproachAMin, csStart)
			return " ", ferr
		}
		if !errors.Is(ferr, ErrNotFound) {
			m.rUnlockObserved(SyncModeApproachAMin, csStart)
			return "", ferr
		}
	}

	n := len(m.ActiveSSreaders)
	if n == 0 {
		m.rUnlockObserved(SyncModeApproachAMin, csStart)
		return " ", ErrNotFound
	}

	sc := m.acquireLeaseScratch(n)
	buf := sc.buf
	copy(buf, m.ActiveSSreaders)
	for i := 0; i < n; i++ {
		buf[i].lease()
	}
	m.rUnlockObserved(SyncModeApproachAMin, csStart)

	// From here on: zero manager locks held; every captured table is leased.
	// There is deliberately no deferred closure: a single release path keeps the
	// read allocation-neutral (decision M-7) and makes lease balance syntactic.
	if m.duringPointReadHook != nil {
		m.duringPointReadHook()
	}

	val, serr := m.scanLeasedTables(buf[:n], byteKey, readTs)

	for i := 0; i < n; i++ {
		if buf[i].releaseLease() {
			if rerr := buf[i].reclaim(); rerr != nil {
				m.recordCleanupErr(rerr)
			}
		}
	}
	m.releaseLeaseScratch(sc)
	return val, serr
}

// scanLeasedTables probes a leased reader set newest-to-oldest with zero locks held.
// The caller owns the leases and must release them on return.
func (m *Manager) scanLeasedTables(buf []*SSTableReader, byteKey []byte, readTs uint64) (string, error) {
	for i := len(buf) - 1; i >= 0; i-- {
		val, serr := buf[i].Get(byteKey, readTs)
		if serr == nil {
			m.sstablePointReads.Add(1)
			return string(val), nil
		}
		if errors.Is(serr, ErrKeyDeleted) {
			m.sstablePointReads.Add(1)
			return " ", serr
		}
		if !errors.Is(serr, ErrNotFound) {
			return "", serr
		}
	}
	return " ", ErrNotFound
}

// ---------------------------------------------------------------------------
// Flush injection gate (Phase I7 E2: publication-only driver).
//
// When enabled, the background flush worker blocks after dequeuing a frozen
// memtable and before building it, until a token is released. This lets a
// harness publish previously frozen memtables at scheduled instants without
// injecting any writes into the measurement window. Disabled by default: the
// production path never waits here.
// ---------------------------------------------------------------------------

// initFlushGateLocked lazily creates the condition variable. Some tests build a
// Manager literal instead of calling CreateManagerWithConfig, so every gate
// entry point must tolerate a zero-valued Manager.
// PRECONDITION: m.flushGateMu is held.
func (m *Manager) initFlushGateLocked() {
	if m.flushGateCond == nil {
		m.flushGateCond = sync.NewCond(&m.flushGateMu)
	}
}

// EnableFlushInjectionGate arms the gate with zero tokens.
func (m *Manager) EnableFlushInjectionGate() {
	m.flushGateMu.Lock()
	defer m.flushGateMu.Unlock()
	m.initFlushGateLocked()
	m.flushGateEnabled = true
	m.flushGateTokens = 0
}

// ReleaseOneFlush admits exactly one pending publication.
func (m *Manager) ReleaseOneFlush() {
	m.flushGateMu.Lock()
	defer m.flushGateMu.Unlock()
	m.initFlushGateLocked()
	m.flushGateTokens++
	m.flushGateCond.Broadcast()
}

// DisableFlushInjectionGate opens the gate permanently and wakes the worker.
// Called by Close so shutdown can never block on the gate.
func (m *Manager) DisableFlushInjectionGate() {
	m.flushGateMu.Lock()
	defer m.flushGateMu.Unlock()
	m.initFlushGateLocked()
	m.flushGateEnabled = false
	m.flushGateCond.Broadcast()
}

// PendingFlushes reports queued plus in-flight flush tasks (diagnostics only).
func (m *Manager) PendingFlushes() int {
	m.flushQueueMu.Lock()
	defer m.flushQueueMu.Unlock()
	n := len(m.flushQueue)
	if m.isFlushing {
		n++
	}
	return n
}

func (m *Manager) waitFlushGate() {
	m.flushGateMu.Lock()
	defer m.flushGateMu.Unlock()
	m.initFlushGateLocked()
	for m.flushGateEnabled && m.flushGateTokens == 0 {
		m.flushGateCond.Wait()
	}
	if m.flushGateEnabled && m.flushGateTokens > 0 {
		m.flushGateTokens--
	}
}

func (m *Manager) Close() error {
	if m.syncMode == SyncModeApproachB {
		return m.closeApproachB()
	}

	m.compactionMu.Lock()
	defer m.compactionMu.Unlock()

	m.Mu.Lock()
	if m.state != EngineOpen {
		err := m.flushErr
		m.Mu.Unlock()
		return err
	}
	m.state = EngineClosing
	m.isClosed = true
	m.flushQueueMu.Lock()
	m.flushQueueClosed = true
	if m.flushQueueCond != nil {
		m.flushQueueCond.Broadcast()
	}
	if m.flushDoneCond != nil {
		m.flushDoneCond.Broadcast()
	}
	m.flushQueueMu.Unlock()
	m.Mu.Unlock()

	// Never let shutdown block on the publication-injection gate.
	m.DisableFlushInjectionGate()

	// Drain in-flight flushes outside m.Mu to prevent self-deadlock
	m.flushWg.Wait()

	// Approach A-min: readers may still be scanning outside m.Mu with a lease
	// held on an ACTIVE or a RETIRED table. No new lease can be granted (state
	// != EngineOpen), so wait for the outstanding ones. A timeout does NOT
	// license closing a leased descriptor: the loops below skip any reader that
	// still reports a lease and fail closed with an error instead.
	var drainErr error
	if m.syncMode == SyncModeApproachAMin {
		drainErr = m.drainReaderLeases()
	}

	m.Mu.Lock()
	m.state = EngineClosed
	active := m.ActiveSSreaders
	retired := m.retiredReaders
	m.ActiveSSreaders = nil
	m.retiredReaders = nil
	m.Mu.Unlock()

	// All descriptor closing happens outside m.Mu.
	var closeErr error
	note := func(err error) {
		if err != nil && closeErr == nil {
			closeErr = err
		}
	}
	note(drainErr)

	for _, reader := range active {
		if reader == nil {
			continue
		}
		if n := reader.leaseCount.Load(); n != 0 {
			note(fmt.Errorf("unsafe close refused: active table %s still has %d outstanding lease(s); descriptor left open", reader.File.Name(), n))
			continue
		}
		note(reader.Close())
	}

	if m.syncMode == SyncModeApproachAMin {
		for _, reader := range retired {
			if reader == nil {
				continue
			}
			if n := reader.leaseCount.Load(); n != 0 {
				note(fmt.Errorf("unsafe reclaim refused: retired table %s still has %d outstanding lease(s); descriptor left open", reader.File.Name(), n))
				continue
			}
			note(reader.reclaim())
		}
	}

	if m.Log != nil && m.Log.File != nil {
		note(m.Log.File.Close())
	}

	// Surface any deferred reclamation error (close/unlink of a superseded table).
	note(m.drainCleanupErrs())

	if m.flushErr != nil {
		return m.flushErr
	}
	m.flushErr = closeErr
	return m.flushErr
}

func (m *Manager) LatestValue(userKey []byte) (uint64, error) {
	m.rLock()
	defer m.rUnlock()
	return m.latestValueLocked(userKey)
}

func (m *Manager) latestValueLocked(userKey []byte) (uint64, error) {
	var maxSeq uint64
	var found bool

	// Tier 1: Check active Memtable.
	// Monotonic Invariant (INV-MVCC-6): m.Mem receives writes with monotonically increasing
	// sequence numbers that strictly postdate all frozen memtables and on-disk SSTables.
	seq, err := m.Mem.LatestValue(userKey)
	if err == nil {
		return seq, nil
	} else if !errors.Is(err, ErrNotFound) {
		return 0, err
	}

	// Tier 2: Check frozen Memtables (newest to oldest).
	// Frozen memtables are strictly newer than any on-disk SSTable.
	for i := len(m.Frozenmem) - 1; i >= 0; i-- {
		seq, err := m.Frozenmem[i].LatestValue(userKey)
		if err == nil {
			return seq, nil
		} else if !errors.Is(err, ErrNotFound) {
			return 0, err
		}
	}

	// Tier 3: Check all on-disk SSTables (Option A fix for Issue E-004).
	// To prevent missed write-write conflicts if SSTables are reordered (e.g. during
	// compaction publication, generation reorganization, or concurrent flushes),
	// examine all active SSTables and track the greatest committed sequence number.
	if m.syncMode == SyncModeApproachB {
		snap, err := m.acquireSnapshotLocked()
		if err != nil {
			return 0, fmt.Errorf("conflict validation snapshot acquisition failed: %w", err)
		}
		if snap == nil {
			return 0, errors.New("conflict validation failed: nil snapshot returned")
		}
		defer m.ReleaseSnapshot(snap)

		for _, sr := range snap.Readers() {
			if sr == nil || sr.Reader == nil {
				return 0, errors.New("corrupted snapshot: nil reader encountered during conflict validation")
			}
			seq, err := sr.Reader.LatestValue(userKey)
			if err == nil {
				if !found || seq > maxSeq {
					maxSeq = seq
					found = true
				}
			} else if errors.Is(err, ErrNotFound) {
				continue
			} else {
				return 0, err
			}
		}
	} else {
		for _, reader := range m.ActiveSSreaders {
			seq, err := reader.LatestValue(userKey)
			if err == nil {
				if !found || seq > maxSeq {
					maxSeq = seq
					found = true
				}
			} else if errors.Is(err, ErrNotFound) {
				continue
			} else {
				return 0, err
			}
		}
	}

	if found {
		return maxSeq, nil
	}

	return 0, ErrNotFound
}

func (m *Manager) initFlushWorkerLocked() {
	if m.flushQueueCond == nil {
		m.flushQueueCond = sync.NewCond(&m.flushQueueMu)
		m.flushDoneCond = sync.NewCond(&m.flushQueueMu)
		m.flushWg.Add(1)
		go m.flushLoop()
	}
}

func (m *Manager) flushLoop() {
	defer m.flushWg.Done()
	for {
		m.flushQueueMu.Lock()
		for len(m.flushQueue) == 0 && !m.flushQueueClosed {
			m.flushQueueCond.Wait()
		}
		if len(m.flushQueue) == 0 && m.flushQueueClosed {
			m.flushQueueMu.Unlock()
			return
		}
		mem := m.flushQueue[0]
		m.flushQueue[0] = nil // allow GC
		m.flushQueue = m.flushQueue[1:]
		if len(m.flushQueue) == 0 {
			m.flushQueue = nil
		}
		m.isFlushing = true
		m.flushQueueMu.Unlock()

		// Publication-injection gate: blocks only when a harness has armed it.
		m.waitFlushGate()

		if m.beforeFlushWorkerBeginHook != nil {
			m.beforeFlushWorkerBeginHook()
		}
		if m.flushObserver != nil {
			m.flushObserver.OnFlushWorkerBegin()
		}

		m.flushToDisk(mem)

		m.flushQueueMu.Lock()
		m.isFlushing = false
		if m.flushDoneCond != nil {
			m.flushDoneCond.Broadcast()
		}
		m.flushQueueMu.Unlock()

		// Option A (Fail-stop): Halt worker immediately upon encountering any fatal flush error.
		// Halting prevents subsequent generations from being published to ActiveSSreaders
		// while an older Memtable remains in Frozenmem, preserving strict chronological
		// precedence (INV-ORDER-1, INV-MVCC-6). All remaining queued Memtables remain in Frozenmem.
		m.rLock()
		err := m.flushErr
		m.rUnlock()
		if err != nil {
			return
		}
	}
}

// FlushErr returns the fatal background flush error, if any has occurred.
func (m *Manager) FlushErr() error {
	m.rLock()
	defer m.rUnlock()
	return m.flushErr
}

func (m *Manager) enqueueFlushLocked(mem *Memtable) {
	if m.flushErr != nil {
		return
	}
	m.initFlushWorkerLocked()
	m.flushQueueMu.Lock()
	m.flushQueue = append(m.flushQueue, mem)
	m.flushQueueCond.Signal()
	m.flushQueueMu.Unlock()
}

// EnqueueFlush enqueues a frozen Memtable into the FIFO flush queue.
// It assumes the caller has already added the Memtable to m.Frozenmem under m.Mu.Lock().
func (m *Manager) EnqueueFlush(mem *Memtable) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	if m.state != EngineOpen || m.flushErr != nil {
		return
	}
	m.enqueueFlushLocked(mem)
}

// SetFlushObserver sets the lifecycle observer for flush events.
func (m *Manager) SetFlushObserver(observer FlushObserver) {
	m.flushObserver = observer
}

// SetBeforeFlushWorkerBeginHook sets a hook called by the flush worker right after
// dequeuing a Memtable before OnFlushWorkerBegin, used for deterministic testing.
func (m *Manager) SetBeforeFlushWorkerBeginHook(hook func()) {
	m.beforeFlushWorkerBeginHook = hook
}

// SetFlushBuildHook sets a hook called when building the SSTable file, used for deterministic testing.
func (m *Manager) SetFlushBuildHook(hook func() error) {
	m.flushBuildHook = hook
}

// WaitForFlushCompletion blocks until all queued and in-progress flushes complete.
func (m *Manager) WaitForFlushCompletion() error {
	m.flushQueueMu.Lock()
	defer m.flushQueueMu.Unlock()
	for (len(m.flushQueue) > 0 || m.isFlushing) && !m.flushQueueClosed {
		m.rLock()
		err := m.flushErr
		m.rUnlock()
		if err != nil {
			return err
		}
		if m.flushDoneCond != nil {
			m.flushDoneCond.Wait()
		} else {
			break
		}
	}
	m.rLock()
	defer m.rUnlock()
	return m.flushErr
}

// TriggerFlush freezes the active Memtable and enqueues it to the background FIFO flush worker.
func (m *Manager) TriggerFlush() error {
	if m.flushObserver != nil {
		m.flushObserver.OnFlushRequested()
	}
	m.Mu.Lock()
	defer m.Mu.Unlock()
	if m.state != EngineOpen {
		if m.flushObserver != nil {
			m.flushObserver.OnFlushAdmitted(false, ErrClosed)
		}
		return ErrClosed
	}
	if m.flushErr != nil {
		if m.flushObserver != nil {
			m.flushObserver.OnFlushAdmitted(false, m.flushErr)
		}
		return m.flushErr
	}
	if m.Mem == nil || (m.Mem.SizeBytes == 0 && m.Mem.SK.headNode.Forward[0] == nil) {
		if m.flushObserver != nil {
			m.flushObserver.OnFlushAdmitted(false, nil)
		}
		return nil
	}

	// Rotate WAL first before altering in-memory state
	if m.Log != nil && m.Log.File != nil {
		if m.walRotationHook != nil {
			if err := m.walRotationHook(); err != nil {
				if m.flushErr == nil {
					m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
				}
				if m.flushObserver != nil {
					m.flushObserver.OnFlushAdmitted(false, m.flushErr)
				}
				return fmt.Errorf("wal rotation failed: %w", err)
			}
		}

		newWalPath := fmt.Sprintf("wal_%d.log", time.Now().UnixNano())
		if m.dataDir != "" {
			newWalPath = filepath.Join(m.dataDir, newWalPath)
		}
		newWal, err := m.newWal(newWalPath)
		if err != nil {
			if m.flushErr == nil {
				m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
			}
			if m.flushObserver != nil {
				m.flushObserver.OnFlushAdmitted(false, m.flushErr)
			}
			return fmt.Errorf("wal rotation failed: %w", err)
		}
		m.Log.File.Close()
		m.Log = newWal
	}

	frozen := m.Mem
	m.Frozenmem = append(m.Frozenmem, frozen)

	sk := &SkipList{
		headNode: &Node{
			Forward: make([]*Node, 32),
		},
		maxLevel:     32,
		currentLevel: 1,
	}
	m.Mem = &Memtable{
		SK:        sk,
		SizeBytes: 0,
	}

	m.enqueueFlushLocked(frozen)
	if m.flushObserver != nil {
		m.flushObserver.OnFlushAdmitted(true, nil)
	}
	return nil
}

// memtableAtThresholdLocked requires Manager.Mu to be held.
func (m *Manager) memtableAtThresholdLocked() bool {
	threshold := m.memtableThresholdBytes
	if threshold <= 0 {
		threshold = 64 * 1024 * 1024
	}
	return m.Mem.SizeBytes >= int64(threshold)
}

func (m *Manager) checkAndTriggerFlush() {
	m.Mu.Lock()
	if m.commitAcquisitionHook != nil {
		m.commitAcquisitionHook()
	}
	if m.state != EngineOpen || m.flushErr != nil {
		m.Mu.Unlock()
		return
	}

	if !m.memtableAtThresholdLocked() {
		m.Mu.Unlock()
		return
	}

	if m.flushObserver != nil {
		m.flushObserver.OnFlushRequested()
	}

	// Rotate WAL first before altering in-memory state
	if m.Log != nil && m.Log.File != nil {
		if m.walRotationHook != nil {
			if err := m.walRotationHook(); err != nil {
				if m.flushErr == nil {
					m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
				}
				if m.flushObserver != nil {
					m.flushObserver.OnFlushAdmitted(false, m.flushErr)
				}
				m.Mu.Unlock()
				return
			}
		}

		newWalPath := fmt.Sprintf("wal_%d.log", time.Now().UnixNano())
		if m.dataDir != "" {
			newWalPath = filepath.Join(m.dataDir, newWalPath)
		}
		newWal, err := m.newWal(newWalPath)
		if err != nil {
			if m.flushErr == nil {
				m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
			}
			if m.flushObserver != nil {
				m.flushObserver.OnFlushAdmitted(false, m.flushErr)
			}
			m.Mu.Unlock()
			return
		}
		m.Log.File.Close()
		m.Log = newWal
	}

	frozen := m.Mem
	m.Frozenmem = append(m.Frozenmem, frozen)

	// Reset Active Memtable
	sk := &SkipList{
		headNode: &Node{
			Forward: make([]*Node, 32),
		},
		maxLevel:     32,
		currentLevel: 1,
	}
	m.Mem = &Memtable{
		SK:        sk,
		SizeBytes: 0,
	}

	m.enqueueFlushLocked(frozen)
	if m.flushObserver != nil {
		m.flushObserver.OnFlushAdmitted(true, nil)
	}
	m.Mu.Unlock()
}

func (m *Manager) flushToDisk(mem *Memtable) {
	if m.flushObserver != nil {
		m.flushObserver.OnFlushBuildBegin()
	}
	if m.flushBuildHook != nil {
		if err := m.flushBuildHook(); err != nil {
			m.Mu.Lock()
			if m.flushErr == nil {
				m.flushErr = err
			}
			m.Mu.Unlock()
			if m.flushObserver != nil {
				m.flushObserver.OnFlushBuildEnd(err)
				m.flushObserver.OnFlushComplete(err)
			}
			return
		}
	}

	filename := fmt.Sprintf("data_%d_%d.sst", time.Now().UnixNano(), sstableFileSeq.Add(1))
	if m.dataDir != "" {
		filename = filepath.Join(m.dataDir, filename)
	}
	file, err := os.Create(filename)
	if err != nil {
		m.Mu.Lock()
		if m.flushErr == nil {
			m.flushErr = err
		}
		m.Mu.Unlock()
		if m.flushObserver != nil {
			m.flushObserver.OnFlushBuildEnd(err)
			m.flushObserver.OnFlushComplete(err)
		}
		return
	}

	builder := &SSTableBuilder{
		File:        file,
		BlockBuffer: new(bytes.Buffer),
	}

	// Iterate through level 0 of the SkipList to get all elements in order
	curr := mem.SK.headNode.Forward[0]
	for curr != nil {
		builder.Add(curr.Key, curr.Value)
		curr = curr.Forward[0]
	}

	if err := builder.Close(); err != nil {
		os.Remove(filename)
		m.Mu.Lock()
		if m.flushErr == nil {
			m.flushErr = err
		}
		m.Mu.Unlock()
		if m.flushObserver != nil {
			m.flushObserver.OnFlushBuildEnd(err)
			m.flushObserver.OnFlushComplete(err)
		}
		return
	}

	reader, err := m.newSSTableReader(filename)
	if err != nil {
		os.Remove(filename)
		m.Mu.Lock()
		if m.flushErr == nil {
			m.flushErr = err
		}
		m.Mu.Unlock()
		if m.flushObserver != nil {
			m.flushObserver.OnFlushBuildEnd(err)
			m.flushObserver.OnFlushComplete(err)
		}
		return
	}

	if m.flushObserver != nil {
		m.flushObserver.OnFlushBuildEnd(nil)
	}

	if m.beforeFlushPublishHook != nil {
		m.beforeFlushPublishHook()
	}

	// Writer arrival at the publication lock. The pre-registered R1.2e §3.2
	// arrival sample (active RLock holders) is taken here, before Mu.Lock.
	lockArrival := m.writerLockArrival(LockSiteFlushPublish)
	if m.flushObserver != nil {
		holders := int(m.activeRLockHolders.Load())
		if lockArrival.observed {
			holders = int(lockArrival.holders)
		}
		m.flushObserver.OnFlushPublishBegin(holders)
	}

	// Swap the newly built SSTable into the read path and remove the frozen memtable
	m.Mu.Lock()
	lockArrival.acquired(m)
	pub := m.beginPublication(LockSiteFlushPublish, lockArrival.holders)
	defer func() {
		pub.end(m)
		m.Mu.Unlock()
	}()

	// Prevent publication into an engine that is already fully CLOSED
	if m.state == EngineClosed {
		reader.Close()
		os.Remove(filename)
		if m.flushObserver != nil {
			m.flushObserver.OnFlushPublishEnd(ErrClosed)
			m.flushObserver.OnFlushComplete(ErrClosed)
		}
		return
	}

	if m.syncMode == SyncModeApproachB {
		sr := NewSharedReader(reader, filename)
		var pubErr error
		if err := m.publishFlushLockedApproachB(mem, sr); err != nil {
			if m.flushErr == nil {
				m.flushErr = err
			}
			pubErr = err
		}
		if m.flushObserver != nil {
			m.flushObserver.OnFlushPublishEnd(pubErr)
			m.flushObserver.OnFlushComplete(pubErr)
		}
		return
	}

	m.ActiveSSreaders = append(m.ActiveSSreaders, reader)
	m.removeFrozenMemtableLocked(mem)
	m.flushesCompleted.Add(1)
	if m.flushObserver != nil {
		m.flushObserver.OnFlushPublishEnd(nil)
		m.flushObserver.OnFlushComplete(nil)
	}
}

func (m *Manager) removeFrozenMemtableLocked(mem *Memtable) {
	for i, f := range m.Frozenmem {
		if f == mem {
			m.Frozenmem = append(m.Frozenmem[:i], m.Frozenmem[i+1:]...)
			break
		}
	}
}

func (m *Manager) RecoverFromWAL(walPath string) error {
	file, err := os.Open(walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	var highestSeqNo uint64 = 0

	// reusable buffer for calculating checksums
	checksumBuf := new(bytes.Buffer)

	for {
		checksumBuf.Reset()

		// read the saved CRC32 Checksum
		var savedCRC uint32
		if err := binary.Read(file, binary.LittleEndian, &savedCRC); err != nil {
			if err == io.EOF {
				break // end of the log
			}
			return err
		}

		var keyLen uint16 // key Length
		if err := binary.Read(file, binary.LittleEndian, &keyLen); err != nil {
			return errors.New("WAL corruption: failed to read key length")
		}
		binary.Write(checksumBuf, binary.LittleEndian, keyLen)

		var valLen uint32 // value Length
		if err := binary.Read(file, binary.LittleEndian, &valLen); err != nil {
			return errors.New("WAL corruption: failed to read value length")
		}

		key := make([]byte, keyLen)
		if _, err := io.ReadFull(file, key); err != nil { // read the key
			return errors.New("WAL corruption: failed to read key payload")
		}
		checksumBuf.Write(key)

		binary.Write(checksumBuf, binary.LittleEndian, valLen)

		val := make([]byte, valLen)
		if _, err := io.ReadFull(file, val); err != nil { // value
			return errors.New("WAL corruption: failed to read value payload")
		}
		checksumBuf.Write(val)

		calculatedCRC := crc32.ChecksumIEEE(checksumBuf.Bytes())
		if calculatedCRC != savedCRC {
			break
		}

		// sequence Number
		_, seqNo, _ := DecodeInternalKey(key)
		if seqNo > highestSeqNo {
			highestSeqNo = seqNo
		}

		// restore in Memtable
		m.Mem.Put(key, val)
		m.Mem.SizeBytes += int64(len(key) + len(val))
	}

	// sync the Manager's global ID generator
	m.CurrentSeqNo.Store(highestSeqNo)

	return nil
}

// FlushesCompleted returns the total number of successful Memtable-to-SSTable flush publications.
func (m *Manager) FlushesCompleted() uint64 {
	return m.flushesCompleted.Load()
}

// CompactionsCompleted returns the total number of successful SSTable compaction publications.
func (m *Manager) CompactionsCompleted() uint64 {
	return m.compactionsCompleted.Load()
}

// SSTablePointReads returns the total number of point reads resolved in the SSTable disk tier.
func (m *Manager) SSTablePointReads() uint64 {
	return m.sstablePointReads.Load()
}

// MemtablePointReads returns the total number of point reads resolved in the active or frozen Memtables.
func (m *Manager) MemtablePointReads() uint64 {
	return m.memtablePointReads.Load()
}

// ActiveSSTableCount returns the current count of active SSTable readers.
func (m *Manager) ActiveSSTableCount() int {
	if m.syncMode == SyncModeApproachB {
		m.snapshotMu.Lock()
		defer m.snapshotMu.Unlock()
		if m.currentSnapshot == nil {
			return 0
		}
		return len(m.currentSnapshot.readers)
	}
	m.rLock()
	defer m.rUnlock()
	return len(m.ActiveSSreaders)
}

// SetMemtableThresholdBytes updates the automatic flush threshold dynamically.
func (m *Manager) SetMemtableThresholdBytes(threshold int) {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	m.memtableThresholdBytes = threshold
}
