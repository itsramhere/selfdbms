package lsmengine

import (
	"fmt"
	"sync"
)

// RWLocker is the manager lock contract. Locks must not be copied after use;
// callers must not acquire recursively or upgrade/downgrade a held lock.
type RWLocker interface {
	Lock()
	Unlock()
	RLock()
	RUnlock()
}

func newManagerLock(policy string) (RWLocker, error) {
	switch policy {
	case "", "go":
		return &sync.RWMutex{}, nil
	case "phasefair":
		return &phaseFairRWMutex{}, nil
	case "readerpref":
		return &readerPrefRWMutex{}, nil
	default:
		return nil, fmt.Errorf("unknown lock policy: %q", policy)
	}
}

// phaseFairRWMutex alternates queued reader batches and FIFO writers. At writer
// release, readerLimit freezes the batch and readerReservations prevents the
// next writer overtaking even readers that have not yet returned from Wait.
// These simple experimental locks are not tuned published phase-fair algorithms.
// The zero value is ready for use.
type phaseFairRWMutex struct {
	mu                 sync.Mutex
	cond               *sync.Cond
	readers            int
	writer             bool
	nextWriter         uint64
	servingWriter      uint64
	nextReader         uint64
	readerLimit        uint64
	waitingReaders     int
	readerReservations int
}

func (m *phaseFairRWMutex) init() {
	if m.cond == nil {
		m.cond = sync.NewCond(&m.mu)
	}
}

func (m *phaseFairRWMutex) Lock() {
	m.mu.Lock()
	m.init()
	ticket := m.nextWriter
	m.nextWriter++
	for m.writer || m.readers != 0 || m.readerReservations != 0 || ticket != m.servingWriter {
		m.cond.Wait()
	}
	m.writer = true
	m.mu.Unlock()
}

func (m *phaseFairRWMutex) Unlock() {
	m.mu.Lock()
	if !m.writer {
		m.mu.Unlock()
		panic("phasefair: Unlock of unlocked lock")
	}
	m.writer = false
	m.servingWriter++
	m.readerLimit = m.nextReader
	m.readerReservations = m.waitingReaders
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (m *phaseFairRWMutex) RLock() {
	m.mu.Lock()
	m.init()
	ticket := m.nextReader
	m.nextReader++
	m.waitingReaders++
	for m.writer || (m.nextWriter != m.servingWriter && ticket >= m.readerLimit) {
		m.cond.Wait()
	}
	m.waitingReaders--
	if ticket < m.readerLimit {
		m.readerReservations--
	}
	m.readers++
	m.mu.Unlock()
}

func (m *phaseFairRWMutex) RUnlock() {
	m.mu.Lock()
	if m.readers == 0 {
		m.mu.Unlock()
		panic("phasefair: RUnlock of unlocked lock")
	}
	m.readers--
	if m.readers == 0 {
		m.cond.Broadcast()
	}
	m.mu.Unlock()
}

// readerPrefRWMutex admits readers whenever no writer holds the lock, including
// while writers wait. Writers may starve until the reader population drains.
// The zero value is ready for use; writer ordering is deliberately unspecified.
type readerPrefRWMutex struct {
	mu             sync.Mutex
	cond           *sync.Cond
	readers        int
	writer         bool
	waitingWriters int
}

func (m *readerPrefRWMutex) init() {
	if m.cond == nil {
		m.cond = sync.NewCond(&m.mu)
	}
}

func (m *readerPrefRWMutex) Lock() {
	m.mu.Lock()
	m.init()
	m.waitingWriters++
	for m.writer || m.readers != 0 {
		m.cond.Wait()
	}
	m.waitingWriters--
	m.writer = true
	m.mu.Unlock()
}

func (m *readerPrefRWMutex) Unlock() {
	m.mu.Lock()
	if !m.writer {
		m.mu.Unlock()
		panic("readerpref: Unlock of unlocked lock")
	}
	m.writer = false
	m.cond.Broadcast()
	m.mu.Unlock()
}

func (m *readerPrefRWMutex) RLock() {
	m.mu.Lock()
	m.init()
	for m.writer {
		m.cond.Wait()
	}
	m.readers++
	m.mu.Unlock()
}

func (m *readerPrefRWMutex) RUnlock() {
	m.mu.Lock()
	if m.readers == 0 {
		m.mu.Unlock()
		panic("readerpref: RUnlock of unlocked lock")
	}
	m.readers--
	if m.readers == 0 {
		m.cond.Broadcast()
	}
	m.mu.Unlock()
}
