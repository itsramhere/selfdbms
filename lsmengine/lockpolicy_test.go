package lsmengine

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lockPolicyAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("lock policy lost wakeup/deadlock")
	}
}

func lockPolicyState(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("lock policy state not reached")
		}
		runtime.Gosched()
	}
}

func TestLockPolicyMutualExclusionStress(t *testing.T) {
	for _, policy := range []string{"phasefair", "readerpref"} {
		t.Run(policy, func(t *testing.T) {
			m, _ := newManagerLock(policy)
			var readers, writers, violations atomic.Int32
			var wg sync.WaitGroup
			value := 0 // deliberately non-atomic: also checked by -race
			for worker := 0; worker < 16; worker++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					for i := 0; i < 300; i++ {
						if (worker+i)%4 == 0 {
							m.Lock()
							if writers.Add(1) != 1 || readers.Load() != 0 {
								violations.Add(1)
							}
							value++
							runtime.Gosched()
							writers.Add(-1)
							m.Unlock()
						} else {
							m.RLock()
							readers.Add(1)
							if writers.Load() != 0 || value < 0 {
								violations.Add(1)
							}
							runtime.Gosched()
							readers.Add(-1)
							m.RUnlock()
						}
					}
				}(worker)
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			lockPolicyAwait(t, done)
			if violations.Load() != 0 || value != 1200 {
				t.Fatalf("violations=%d value=%d", violations.Load(), value)
			}
		})
	}
}

func TestLockPolicyNoLostWakeups(t *testing.T) {
	for _, policy := range []string{"phasefair", "readerpref"} {
		t.Run(policy, func(t *testing.T) {
			m, _ := newManagerLock(policy)
			for round := 0; round < 60; round++ {
				m.Lock()
				var wg sync.WaitGroup
				started := make(chan struct{}, 16)
				for i := 0; i < 16; i++ {
					wg.Add(1)
					go func(read bool) {
						defer wg.Done()
						started <- struct{}{}
						if read {
							m.RLock()
							runtime.Gosched()
							m.RUnlock()
						} else {
							m.Lock()
							runtime.Gosched()
							m.Unlock()
						}
					}(i%2 == 0)
				}
				for i := 0; i < 16; i++ {
					<-started
				}
				m.Unlock()
				done := make(chan struct{})
				go func() { wg.Wait(); close(done) }()
				lockPolicyAwait(t, done)
			}
		})
	}
}

func TestPhaseFairReaderBehindWaitingWriter(t *testing.T) {
	m := &phaseFairRWMutex{}
	m.RLock()
	wEntered, wRelease, wDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { m.Lock(); close(wEntered); <-wRelease; m.Unlock(); close(wDone) }()
	lockPolicyState(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.nextWriter == 1 })
	rEntered := make(chan struct{})
	go func() { m.RLock(); close(rEntered); m.RUnlock() }()
	lockPolicyState(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.waitingReaders == 1 })
	m.RUnlock()
	lockPolicyAwait(t, wEntered)
	select {
	case <-rEntered:
		t.Fatal("reader overtook waiting writer")
	default:
	}
	close(wRelease)
	lockPolicyAwait(t, rEntered)
	lockPolicyAwait(t, wDone)
}

func TestPhaseFairEntireReaderBatchBeforeNextWriter(t *testing.T) {
	m := &phaseFairRWMutex{}
	m.Lock()
	wEntered, wDone := make(chan struct{}), make(chan struct{})
	go func() { m.Lock(); close(wEntered); m.Unlock(); close(wDone) }()
	lockPolicyState(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.nextWriter == 2 })
	entered, release := make(chan struct{}, 32), make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.RLock(); entered <- struct{}{}; <-release; m.RUnlock() }()
	}
	lockPolicyState(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.waitingReaders == 32 })
	m.Unlock()
	for i := 0; i < 32; i++ {
		select {
		case <-entered:
		case <-wEntered:
			t.Fatal("writer overtook released reader batch")
		case <-time.After(10 * time.Second):
			t.Fatal("reader batch failed to enter")
		}
	}
	select {
	case <-wEntered:
		t.Fatal("writer entered with active readers")
	default:
	}
	close(release)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	lockPolicyAwait(t, done)
	lockPolicyAwait(t, wDone)
}

func TestPhaseFairWriterArrivalOrder(t *testing.T) {
	m := &phaseFairRWMutex{}
	m.RLock()
	order := make(chan int, 20)
	for i := 0; i < 20; i++ {
		go func(i int) { m.Lock(); order <- i; m.Unlock() }(i)
		lockPolicyState(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.nextWriter == uint64(i+1) })
	}
	m.RUnlock()
	for i := 0; i < 20; i++ {
		select {
		case got := <-order:
			if got != i {
				t.Fatalf("writer %d before %d", got, i)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("writer not awakened")
		}
	}
}

func TestReaderPrefAdmissionAndWriterDrain(t *testing.T) {
	m := &readerPrefRWMutex{}
	m.RLock()
	wDone := make(chan struct{})
	go func() { m.Lock(); m.Unlock(); close(wDone) }()
	lockPolicyState(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.waitingWriters == 1 })
	for i := 0; i < 50; i++ {
		rDone := make(chan struct{})
		go func() { m.RLock(); m.RUnlock(); close(rDone) }()
		lockPolicyAwait(t, rDone)
	}
	select {
	case <-wDone:
		t.Fatal("writer entered before readers drained")
	default:
	}
	m.RUnlock()
	lockPolicyAwait(t, wDone)
}

func TestLockPolicyConfig(t *testing.T) {
	for _, policy := range []string{"", "go", "phasefair", "readerpref"} {
		m, err := CreateManagerWithConfig(Config{DataDir: t.TempDir(), LockPolicy: policy})
		if err != nil {
			t.Fatal(err)
		}
		if policy == "" || policy == "go" {
			if _, ok := m.Mu.(*sync.RWMutex); !ok {
				t.Fatal("default lock changed")
			}
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := CreateManagerWithConfig(Config{LockPolicy: "unknown"}); err == nil || m != nil {
		t.Fatal("unknown policy accepted")
	}
	_ = Manager{Mu: &sync.RWMutex{}} // Existing test/consumer literals still compile.
}

// Track ownership by goroutine only in this test. Detect even recursive RLock
// without a waiting writer, which a normal execution might otherwise conceal.
type nonRecursiveManagerLock struct {
	RWLocker
	mu   sync.Mutex
	held map[uint64]bool
}

func lockPolicyGoroutine() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	id, err := strconv.ParseUint(strings.Fields(string(buf[:n]))[1], 10, 64)
	if err != nil {
		panic(err)
	}
	return id
}

func (m *nonRecursiveManagerLock) acquire(read bool) {
	id := lockPolicyGoroutine()
	m.mu.Lock()
	if m.held[id] {
		panic("recursive manager lock acquisition")
	}
	m.held[id] = true
	m.mu.Unlock()
	if read {
		m.RWLocker.RLock()
	} else {
		m.RWLocker.Lock()
	}
}
func (m *nonRecursiveManagerLock) release(read bool) {
	m.mu.Lock()
	delete(m.held, lockPolicyGoroutine())
	m.mu.Unlock()
	if read {
		m.RWLocker.RUnlock()
	} else {
		m.RWLocker.Unlock()
	}
}
func (m *nonRecursiveManagerLock) Lock()    { m.acquire(false) }
func (m *nonRecursiveManagerLock) RLock()   { m.acquire(true) }
func (m *nonRecursiveManagerLock) Unlock()  { m.release(false) }
func (m *nonRecursiveManagerLock) RUnlock() { m.release(true) }

func exerciseLockPolicyManager(t *testing.T, policy string, mode SyncMode, checkRecursive bool) {
	t.Helper()
	m, err := CreateManagerWithConfig(Config{Mode: mode, DataDir: t.TempDir(), LockPolicy: policy,
		DisableWALSync: true, MemtableThresholdBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if checkRecursive {
		m.Mu = &nonRecursiveManagerLock{RWLocker: m.Mu, held: make(map[uint64]bool)}
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				key := fmt.Sprintf("%d/%d", worker, i)
				tx := m.Begin()
				tx.Put(key, "value")
				if err := tx.Commit(); err != nil {
					t.Error(err)
					return
				}
				if got, err := m.Get(key, m.CurrentSeqNo.Load()); err != nil || got != "value" {
					t.Errorf("read %s = %q: %v", key, got, err)
					return
				}
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	lockPolicyAwait(t, done)
	if err := m.WaitForFlushCompletion(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompactAll(); err != nil {
		t.Fatal(err)
	}
	if m.FlushesCompleted() == 0 {
		t.Fatal("flush path not exercised")
	}
}

func TestLockPolicyManagerCommitsAndReads(t *testing.T) {
	for _, policy := range []string{"go", "phasefair", "readerpref"} {
		for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
			t.Run(policy+"/"+mode.String(), func(t *testing.T) { exerciseLockPolicyManager(t, policy, mode, false) })
		}
	}
}

func TestLockPolicyNoRecursiveUse(t *testing.T) {
	for _, policy := range []string{"go", "phasefair", "readerpref"} {
		for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
			t.Run(policy+"/"+mode.String(), func(t *testing.T) { exerciseLockPolicyManager(t, policy, mode, true) })
		}
	}
}
