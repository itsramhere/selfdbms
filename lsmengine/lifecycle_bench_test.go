package lsmengine

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// lifecycleLeaseCycle mirrors only the capture/release sections of
// getApproachAMin. No memory-tier lookup, key comparison, search, I/O, or
// instrumentation is included. Tables stay active, so reclamation cannot occur.
func lifecycleLeaseCycle(m *Manager) {
	m.rLock()
	n := len(m.ActiveSSreaders)
	sc := m.acquireLeaseScratch(n)
	copy(sc.buf, m.ActiveSSreaders)
	for _, sr := range sc.buf {
		sr.lease()
	}
	m.rUnlock()
	for _, sr := range sc.buf {
		if sr.releaseLease() {
			panic("active benchmark table became reclaimable")
		}
	}
	m.releaseLeaseScratch(sc)
}

func lifecycleManager(n int) *Manager {
	m := &Manager{state: EngineOpen, Mu: &sync.RWMutex{}}
	readers := make([]*SharedReader, n)
	m.ActiveSSreaders = make([]*SSTableReader, n, n+1)
	for i := range readers {
		r := &SSTableReader{}
		m.ActiveSSreaders[i] = r
		readers[i] = NewSharedReader(r, "")
		readers[i].SetState(SharedReaderActive)
	}
	m.currentSnapshot = NewReaderSnapshot(1, readers)
	m.currentSnapshot.SetState(SnapshotPublished)
	m.snapshotSeq = 1
	return m
}

func BenchmarkLifecycle(b *testing.B) {
	for _, n := range []int{4, 16, 64, 256} {
		for _, workers := range []int{1, 16} {
			for _, arm := range []string{"AMIN", "B"} {
				b.Run(fmt.Sprintf("%s/N%d/g%d", arm, n, workers), func(b *testing.B) {
					// RunParallel uses SetParallelism * GOMAXPROCS workers. Keep
					// the worker count exact and allow those workers to run on CPUs.
					old := runtime.GOMAXPROCS(workers)
					defer runtime.GOMAXPROCS(old)
					b.SetParallelism(1)
					m := lifecycleManager(n)
					lifecycleLeaseCycle(m) // prime scratch storage outside the timer
					b.ReportAllocs()
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							if arm == "AMIN" {
								lifecycleLeaseCycle(m)
							} else {
								snap, err := m.AcquireSnapshot()
								if err != nil {
									b.Error(err)
									return
								}
								m.ReleaseSnapshot(snap)
							}
						}
					})
					b.StopTimer()
					if m.currentSnapshot.Pins() != 0 {
						b.Fatal("unbalanced pins")
					}
					for _, sr := range m.ActiveSSreaders {
						if sr.leaseCount.Load() != 0 {
							b.Fatal("unbalanced leases")
						}
					}
				})
			}
		}
	}
}

// One publication from N to N+1 active tables, without build, disk, observers,
// or readers. B uses the production publication method (including immediate
// retirement of the unpinned prior snapshot). A-min's three statements mirror
// the non-B publication suffix of flushToDisk; it has no standalone method.
// A-min has spare append capacity, representing amortized steady-state append.
// Setup/reset is untimed and N stays fixed across iterations.
func BenchmarkLifecyclePublication(b *testing.B) {
	for _, n := range []int{4, 16, 64, 256} {
		for _, arm := range []string{"AMIN", "B"} {
			b.Run(fmt.Sprintf("%s/N%d", arm, n), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				b.StopTimer()
				for i := 0; i < b.N; i++ {
					m := lifecycleManager(n)
					mem, reader := &Memtable{}, &SSTableReader{}
					m.Frozenmem = []*Memtable{mem}
					b.StartTimer()
					m.Mu.Lock()
					if arm == "B" {
						sr := NewSharedReader(reader, "")
						if err := m.publishFlushLockedApproachB(mem, sr); err != nil {
							b.Fatal(err)
						}
					} else {
						m.ActiveSSreaders = append(m.ActiveSSreaders, reader)
						m.removeFrozenMemtableLocked(mem)
						m.flushesCompleted.Add(1)
					}
					m.Mu.Unlock()
					b.StopTimer()
					if len(m.Frozenmem) != 0 || m.flushesCompleted.Load() != 1 {
						b.Fatal("publication failed")
					}
					if (arm == "B" && len(m.currentSnapshot.readers) != n+1) ||
						(arm == "AMIN" && len(m.ActiveSSreaders) != n+1) {
						b.Fatal("published table count")
					}
				}
			})
		}
	}
}
