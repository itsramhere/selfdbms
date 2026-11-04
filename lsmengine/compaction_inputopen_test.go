package lsmengine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func inputopenFixture(t *testing.T, mode SyncMode, outside bool) *Manager {
	t.Helper()
	dir := t.TempDir()
	m, err := CreateManagerWithConfig(Config{Mode: mode, DataDir: dir,
		WalPath: filepath.Join(dir, "wal.log"), CompactionOpenOutsideLock: outside})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < 3; i++ {
		commitAndFlush(t, m, fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
	}
	return m
}

func inputopenWait(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("compaction did not finish")
	}
}

func TestCompactionInputOpenHold(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin} {
		for _, outside := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/outside=%v", mode, outside), func(t *testing.T) {
				m := inputopenFixture(t, mode, outside)
				obs := newRecordingObserver()
				clock := &stepClock{step: 100}
				if err := m.SetSyncObserver(obs, clock.Now); err != nil {
					t.Fatal(err)
				}
				calls := 0
				m.compactionIteratorOpenHook = func(path string) (*SSTableIterator, error) {
					calls++
					// TryLock tests the actual exclusive lock without timing or deadlock.
					unlocked := m.Mu.(*sync.RWMutex).TryLock()
					if unlocked {
						m.Mu.Unlock()
					}
					if unlocked != outside {
						t.Errorf("open lock released=%v, outside=%v", unlocked, outside)
					}
					obs.mu.Lock()
					ended := len(obs.pubs)
					obs.mu.Unlock()
					if (ended == 1) != outside {
						t.Errorf("start hold ended=%d", ended)
					}
					return NewSSTableIterator(path)
				}
				m.duringCompactionMergeHook = func() {
					if calls != 3 {
						t.Errorf("merge hook before inputs opened: %d", calls)
					}
					if !m.Mu.(*sync.RWMutex).TryLock() {
						t.Error("merge hook inside hold")
					} else {
						m.Mu.Unlock()
					}
				}
				if err := m.CompactAll(); err != nil {
					t.Fatal(err)
				}
				if m.ActiveSSTableCount() != 1 {
					t.Fatal("inputs not merged")
				}
				for i := 0; i < 3; i++ {
					v, err := m.Get(fmt.Sprintf("key%d", i), ^uint64(0))
					if err != nil || v != fmt.Sprintf("value%d", i) {
						t.Fatalf("merge key%d: %q, %v", i, v, err)
					}
				}
			})
		}
	}
}

func TestCompactionInputOpenConcurrentFlush(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin} {
		t.Run(mode.String(), func(t *testing.T) {
			m := inputopenFixture(t, mode, true)
			paused, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			m.compactionIteratorOpenHook = func(path string) (*SSTableIterator, error) {
				once.Do(func() { close(paused); <-resume })
				return NewSSTableIterator(path)
			}
			done := make(chan error, 1)
			go func() { done <- m.CompactAll() }()
			<-paused
			// Always release the compactor even if a subsequent assertion fails.
			var release sync.Once
			defer release.Do(func() { close(resume) })
			commitAndFlush(t, m, "new", "new-value")
			commitAndFlush(t, m, "key0", "newest")
			m.Mu.RLock()
			appended := append([]*SSTableReader(nil), m.ActiveSSreaders[3:]...)
			m.Mu.RUnlock()
			release.Do(func() { close(resume) })
			inputopenWait(t, done)
			m.Mu.RLock()
			preserved := len(m.ActiveSSreaders) == 3 && m.ActiveSSreaders[1] == appended[0] && m.ActiveSSreaders[2] == appended[1]
			m.Mu.RUnlock()
			if !preserved {
				t.Fatal("concurrent flush lost or reordered")
			}
			for key, want := range map[string]string{"new": "new-value", "key0": "newest", "key1": "value1", "key2": "value2"} {
				v, err := m.Get(key, ^uint64(0))
				if err != nil || v != want {
					t.Fatalf("%s: %q %v", key, v, err)
				}
			}
		})
	}
}

func TestCompactionInputOpenFailure(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin} {
		for _, outside := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/outside=%v", mode, outside), func(t *testing.T) {
				m := inputopenFixture(t, mode, outside)
				failure := errors.New("injected open failure")
				var opened *SSTableIterator
				m.compactionIteratorOpenHook = func(path string) (*SSTableIterator, error) {
					if opened != nil {
						return nil, failure
					}
					var err error
					opened, err = NewSSTableIterator(path)
					return opened, err
				}
				if err := m.CompactAll(); err != failure {
					t.Fatalf("error identity: %v", err)
				}
				if _, err := opened.File.ReadAt(make([]byte, 1), 0); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("opened iterator leaked: %v", err)
				}
				if !m.Mu.(*sync.RWMutex).TryLock() {
					t.Fatal("manager lock leaked")
				}
				m.Mu.Unlock()
				if m.ActiveSSTableCount() != 3 {
					t.Fatal("failed open changed inputs")
				}
				m.compactionIteratorOpenHook = nil
				commitAndFlush(t, m, "after-failure", "usable")
				if err := m.CompactAll(); err != nil {
					t.Fatal(err)
				}
				v, err := m.Get("after-failure", ^uint64(0))
				if err != nil || v != "usable" {
					t.Fatalf("manager unusable: %q %v", v, err)
				}
			})
		}
	}
}

func TestCompactionInputOpenLeasedReaderSurvivesRetirement(t *testing.T) {
	m := inputopenFixture(t, SyncModeApproachAMin, true)
	inputs := append([]*SSTableReader(nil), m.ActiveSSreaders...)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.duringPointReadHook = func() { once.Do(func() { close(paused); <-resume }) }
	done := make(chan error, 1)
	go func() {
		v, err := m.Get("key0", ^uint64(0))
		if err == nil && v != "value0" {
			err = fmt.Errorf("wrong value %q", v)
		}
		done <- err
	}()
	<-paused
	var release sync.Once
	defer func() { release.Do(func() { close(resume) }); inputopenWait(t, done) }()
	if err := m.CompactAll(); err != nil {
		t.Fatal(err)
	}
	for _, r := range inputs {
		if !r.retired.Load() || r.leaseCount.Load() != 1 {
			t.Fatal("input not retired with reader lease")
		}
		if _, err := r.File.ReadAt(make([]byte, 1), 0); err != nil {
			t.Fatalf("leased input closed: %v", err)
		}
	}
	release.Do(func() { close(resume) })
}

func TestCompactionInputOpenSnapshotUnaffected(t *testing.T) {
	m := inputopenFixture(t, SyncModeApproachB, true)
	m.compactionIteratorOpenHook = func(string) (*SSTableIterator, error) {
		t.Error("Snapshot used A/AMIN input-open treatment")
		return nil, errors.New("unexpected opener")
	}
	if err := m.CompactAll(); err != nil {
		t.Fatal(err)
	}
	if m.ActiveSSTableCount() != 1 {
		t.Fatal("Snapshot inputs not merged")
	}
	for i := 0; i < 3; i++ {
		v, err := m.Get(fmt.Sprintf("key%d", i), ^uint64(0))
		if err != nil || v != fmt.Sprintf("value%d", i) {
			t.Fatalf("Snapshot merge: %q %v", v, err)
		}
	}
}

func TestCompactionInputOpenReadStress(t *testing.T) {
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin} {
		for _, outside := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/outside=%v", mode, outside), func(t *testing.T) {
				m := inputopenFixture(t, mode, outside)
				stop, failures := make(chan struct{}), make(chan error, 4)
				var wg sync.WaitGroup
				var reads atomic.Int64
				ready := make(chan struct{}, 4)
				for i := 0; i < 4; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						ready <- struct{}{}
						for {
							select {
							case <-stop:
								return
							default:
							}
							for k := 0; k < 3; k++ {
								v, err := m.Get(fmt.Sprintf("key%d", k), ^uint64(0))
								if err != nil || v != fmt.Sprintf("value%d", k) {
									failures <- fmt.Errorf("read during compaction: %q %v", v, err)
									return
								}
								reads.Add(1)
							}
						}
					}()
				}
				defer func() {
					close(stop)
					wg.Wait()
					close(failures)
					for err := range failures {
						t.Error(err)
					}
				}()
				for i := 0; i < 4; i++ {
					<-ready
				}
				for i := 0; i < 12; i++ {
					prior := m.ActiveSSreaders[0].File.Name()
					// Existing output naming uses Windows wall-clock ticks. Avoid
					// reusing a live output name in this rapid synthetic churn;
					// the registered campaign spaces compactions by 250 ms.
					var previousTick int64
					if _, err := fmt.Sscanf(filepath.Base(prior), "data_compacted_%d.sst", &previousTick); err == nil {
						deadline := time.Now().Add(time.Second)
						for time.Now().UnixNano() <= previousTick {
							if time.Now().After(deadline) {
								t.Fatal("wall clock failed to advance")
							}
							time.Sleep(time.Millisecond)
						}
					}
					commitAndFlush(t, m, fmt.Sprintf("churn%d", i), "v")
					if err := m.CompactAll(); err != nil {
						t.Fatal(err)
					}
					if m.ActiveSSreaders[0].File.Name() == prior {
						t.Errorf("compaction reused input pathname: %s", prior)
					}
				}
				if reads.Load() == 0 {
					t.Fatal("no concurrent reads")
				}
			})
		}
	}
}
