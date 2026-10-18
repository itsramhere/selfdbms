package lsmengine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
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
	// Reader-compactor synchronization relies entirely on Manager.Mu (*sync.RWMutex),
	// serialized compaction lifetimes (compactionMu), and safe reader retirement.
	SyncModeApproachA SyncMode = iota

	// SyncModeApproachB represents atomic pointer reader snapshot swap (Approach B).
	// Readers acquire short-lived immutable reader-set snapshots without holding Manager.Mu across disk I/O.
	SyncModeApproachB
)

func (m SyncMode) String() string {
	switch m {
	case SyncModeApproachA:
		return "ApproachA_RWMutexBaseline"
	case SyncModeApproachB:
		return "ApproachB_ReaderSnapshot"
	default:
		return "Unknown"
	}
}

var ErrNotFound = errors.New("key not found")
var ErrKeyDeleted = errors.New("key has been deleted")
var ErrClosed = errors.New("engine is closed")
var ErrUnsupportedSyncMode = errors.New("unsupported synchronization mode: Approach B is not implemented")

type Config struct {
	Mode    SyncMode
	WalPath string
}

func DefaultConfig() Config {
	return Config{
		Mode:    SyncModeApproachA,
		WalPath: "wal.log",
	}
}

type Manager struct {
	syncMode                    SyncMode
	CurrentSeqNo                atomic.Uint64
	Mu                          *sync.RWMutex
	compactionMu                sync.Mutex
	isClosed                    bool
	state                       EngineState
	flushWg                     sync.WaitGroup
	flushErr                    error
	flushQueue                  []*Memtable
	flushQueueMu                sync.Mutex
	flushQueueCond              *sync.Cond
	flushQueueClosed            bool
	Mem                         *Memtable
	Log                         *Wal
	Frozenmem                   []*Memtable
	ActiveSSreaders             []*SSTableReader
	beforeCompactionPublishHook func()
	duringCompactionMergeHook   func()
	duringPointReadHook         func()
	duringMemtableReadHook      func()
	beforeFlushPublishHook      func()
	flushBuildHook              func() error
	walRotationHook             func() error

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
}

var sstableFileSeq atomic.Uint64

func CreateManagerWithConfig(cfg Config) (*Manager, error) {
	if cfg.Mode != SyncModeApproachA && cfg.Mode != SyncModeApproachB {
		return nil, fmt.Errorf("unknown synchronization mode: %v", cfg.Mode)
	}

	walPath := cfg.WalPath
	if walPath == "" {
		walPath = "wal.log"
	}

	man := new(Manager)
	man.syncMode = cfg.Mode
	man.CurrentSeqNo.Store(0)
	man.Mu = &sync.RWMutex{}
	man.closeDone = make(chan struct{})
	man.snapshotDrainCond = sync.NewCond(&man.snapshotMu)
	man.snapshotPhase = SnapshotPhaseOpen

	sk := &SkipList{
		headNode:     &Node{Forward: make([]*Node, 32)},
		maxLevel:     32,
		currentLevel: 1,
	}

	man.Mem = &Memtable{
		SK:        sk,
		SizeBytes: 0,
	}

	man.Log, _ = NewWal(walPath)
	man.initFlushWorkerLocked()

	if cfg.Mode == SyncModeApproachB {
		man.currentSnapshot = &ReaderSnapshot{
			id: atomic.AddUint64(&man.snapshotSeq, 1),
		}
		man.currentSnapshot.SetState(SnapshotPublished)
	}

	return man, nil
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
	if m.syncMode == SyncModeApproachB {
		return m.getApproachB(key, readTs)
	}

	byteKey := []byte(key)
	m.Mu.RLock()
	defer m.Mu.RUnlock()

	if m.state != EngineOpen {
		return "", ErrClosed
	}

	if m.duringMemtableReadHook != nil {
		m.duringMemtableReadHook()
	}

	sol, err := m.Mem.Get(byteKey, readTs)
	if err != nil {
		if errors.Is(err, ErrKeyDeleted) {
			return "Key is Deleted", err
		} else if errors.Is(err, ErrNotFound) {
		} else {
			return "", err
		}
	} else {
		return string(sol), nil
	}

	for i := len(m.Frozenmem) - 1; i >= 0; i-- {
		val, err := m.Frozenmem[i].Get(byteKey, readTs)
		if err != nil {
			if errors.Is(err, ErrKeyDeleted) {
				return " ", err
			} else if errors.Is(err, ErrNotFound) {
				continue
			} else {
				return "", err
			}
		} else {
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
				return " ", err
			} else if errors.Is(err, ErrNotFound) {
				continue
			} else {
				return "", err
			}
		} else {
			return string(val), nil
		}
	}

	return " ", ErrNotFound

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
	m.flushQueueMu.Unlock()
	m.Mu.Unlock()

	// Drain in-flight flushes outside m.Mu to prevent self-deadlock
	m.flushWg.Wait()

	m.Mu.Lock()
	defer m.Mu.Unlock()

	m.state = EngineClosed

	for _, reader := range m.ActiveSSreaders {
		if reader != nil {
			if err := reader.Close(); err != nil && m.flushErr == nil {
				m.flushErr = err
			}
		}
	}
	m.ActiveSSreaders = nil

	if m.Log != nil && m.Log.File != nil {
		if err := m.Log.File.Close(); err != nil && m.flushErr == nil {
			m.flushErr = err
		}
	}
	return m.flushErr
}

func (m *Manager) LatestValue(userKey []byte) (uint64, error) {
	m.Mu.RLock()
	defer m.Mu.RUnlock()
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
		m.flushQueueMu.Unlock()

		m.flushToDisk(mem)

		// Option A (Fail-stop): Halt worker immediately upon encountering any fatal flush error.
		// Halting prevents subsequent generations from being published to ActiveSSreaders
		// while an older Memtable remains in Frozenmem, preserving strict chronological
		// precedence (INV-ORDER-1, INV-MVCC-6). All remaining queued Memtables remain in Frozenmem.
		m.Mu.RLock()
		err := m.flushErr
		m.Mu.RUnlock()
		if err != nil {
			return
		}
	}
}

// FlushErr returns the fatal background flush error, if any has occurred.
func (m *Manager) FlushErr() error {
	m.Mu.RLock()
	defer m.Mu.RUnlock()
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

// TriggerFlush freezes the active Memtable and enqueues it to the background FIFO flush worker.
func (m *Manager) TriggerFlush() error {
	m.Mu.Lock()
	defer m.Mu.Unlock()
	if m.state != EngineOpen {
		return ErrClosed
	}
	if m.flushErr != nil {
		return m.flushErr
	}
	if m.Mem == nil || (m.Mem.SizeBytes == 0 && m.Mem.SK.headNode.Forward[0] == nil) {
		return nil
	}

	// Rotate WAL first before altering in-memory state
	if m.Log != nil && m.Log.File != nil {
		if m.walRotationHook != nil {
			if err := m.walRotationHook(); err != nil {
				if m.flushErr == nil {
					m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
				}
				return fmt.Errorf("wal rotation failed: %w", err)
			}
		}

		newWalPath := fmt.Sprintf("wal_%d.log", time.Now().UnixNano())
		newWal, err := NewWal(newWalPath)
		if err != nil {
			if m.flushErr == nil {
				m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
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
	return nil
}

func (m *Manager) checkAndTriggerFlush() {
	m.Mu.Lock()
	if m.state != EngineOpen || m.flushErr != nil {
		m.Mu.Unlock()
		return
	}

	// 64MB threshold
	if m.Mem.SizeBytes < 64*1024*1024 {
		m.Mu.Unlock()
		return
	}

	// Rotate WAL first before altering in-memory state
	if m.Log != nil && m.Log.File != nil {
		if m.walRotationHook != nil {
			if err := m.walRotationHook(); err != nil {
				if m.flushErr == nil {
					m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
				}
				m.Mu.Unlock()
				return
			}
		}

		newWalPath := fmt.Sprintf("wal_%d.log", time.Now().UnixNano())
		newWal, err := NewWal(newWalPath)
		if err != nil {
			if m.flushErr == nil {
				m.flushErr = fmt.Errorf("wal rotation failed: %w", err)
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
	m.Mu.Unlock()
}

func (m *Manager) flushToDisk(mem *Memtable) {
	if m.flushBuildHook != nil {
		if err := m.flushBuildHook(); err != nil {
			m.Mu.Lock()
			if m.flushErr == nil {
				m.flushErr = err
			}
			m.Mu.Unlock()
			return
		}
	}

	filename := fmt.Sprintf("data_%d_%d.sst", time.Now().UnixNano(), sstableFileSeq.Add(1))
	file, err := os.Create(filename)
	if err != nil {
		m.Mu.Lock()
		if m.flushErr == nil {
			m.flushErr = err
		}
		m.Mu.Unlock()
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
		return
	}

	reader, err := NewSSTableReader(filename)
	if err != nil {
		os.Remove(filename)
		m.Mu.Lock()
		if m.flushErr == nil {
			m.flushErr = err
		}
		m.Mu.Unlock()
		return
	}

	if m.beforeFlushPublishHook != nil {
		m.beforeFlushPublishHook()
	}

	// Swap the newly built SSTable into the read path and remove the frozen memtable
	m.Mu.Lock()
	defer m.Mu.Unlock()

	// Prevent publication into an engine that is already fully CLOSED
	if m.state == EngineClosed {
		reader.Close()
		os.Remove(filename)
		return
	}

	if m.syncMode == SyncModeApproachB {
		sr := NewSharedReader(reader, filename)
		if err := m.publishFlushLockedApproachB(mem, sr); err != nil {
			if m.flushErr == nil {
				m.flushErr = err
			}
		}
		return
	}

	m.ActiveSSreaders = append(m.ActiveSSreaders, reader)
	m.removeFrozenMemtableLocked(mem)
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
