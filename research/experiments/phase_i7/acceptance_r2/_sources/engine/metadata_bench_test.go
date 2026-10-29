package lsmengine

// EXP-3 metadata microbenchmark suite (Phase I7, R1.2e §6 item 6).
//
// Purpose: quantify the cost of the engine's in-memory metadata structures —
// the memtable skip list, the frozen-memtable scan, the Approach A-min short
// read critical section, the Approach B snapshot pin, and the R1.2e §3.2
// active-holder counter — as keyspace cardinality grows. EXP-3 contributes
// ZERO disk runs to the campaign (R1.2e §5): every measured path here
// resolves inside memory and no SSTable is ever built or read. The WAL
// written during fixture construction is setup cost outside the timer.
//
// Execution policy: AUTHORED ONLY. Per the R2 engineering authorization these
// benchmarks are compile-verified (go vet / go test -run '^$' -bench '^$')
// but NOT executed. Execution is a performance measurement and belongs to the
// next authorization gate.
//
// Determinism: fixtures are built in a fixed key order; the hit/miss workload
// is a fixed stride permutation, so every compared arm touches the same
// logical sequence. No wall-clock assertion is made; distributions over b.N
// iterations are reported by the testing framework.

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// benchCardinalities are the EXP-3 cardinality sweep points (R3: does reader
// synchronization cost scale with metadata cardinality?).
var benchCardinalities = []int{1_000, 100_000, 1_000_000}

const benchReadTs = math.MaxUint64

// benchManager builds a manager of the given mode with n keys resident in the
// active memtable. No flush is ever triggered: everything stays in memory.
func benchManager(b *testing.B, mode SyncMode, n int) (*Manager, func()) {
	b.Helper()
	dir := b.TempDir()
	man, err := CreateManagerWithConfig(Config{
		Mode:                   mode,
		WalPath:                filepath.Join(dir, "wal.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 1 << 30, // never auto-flush inside the benchmark
	})
	if err != nil {
		b.Fatalf("create manager: %v", err)
	}
	const perTx = 2000
	for base := 0; base < n; base += perTx {
		tx := man.Begin()
		for k := base; k < base+perTx && k < n; k++ {
			tx.Put(keyStr(k), string(valBytes(k)))
		}
		if err := tx.Commit(); err != nil {
			b.Fatalf("fixture commit: %v", err)
		}
	}
	return man, func() { _ = man.Close() }
}

func benchKey(i int) int   { return i }
func benchVal(i int) int64 { return int64(i) }

// benchStride returns the i-th key index of a fixed stride permutation over
// [0,n): the same sequence for every compared arm and cardinality.
func benchStride(n, i int) int {
	x := (i*1103515245 + 12345) & 0x7fffffff
	return x % n
}

// keyStr and valBytes format the fixed-width keys and values used by the suite.
func keyStr(i int) string { return fmt.Sprintf("key%08d", i) }
func valBytes(i int) []byte {
	return []byte(fmt.Sprintf("val%08d", i))
}

// BenchmarkMemtablePointRead measures point-read cost against the active
// memtable skip list as cardinality grows, for hits and misses, on the
// Approach A read path (whole read inside the read critical section).
func BenchmarkMemtablePointRead(b *testing.B) {
	for _, n := range benchCardinalities {
		for _, hit := range []bool{true, false} {
			name := fmt.Sprintf("cardinality_%d/hit=%v", n, hit)
			b.Run(name, func(b *testing.B) {
				man, cleanup := benchManager(b, SyncModeApproachA, n)
				defer cleanup()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var err error
					if hit {
						_, err = man.Get(keyStr(benchStride(n, i)), benchReadTs)
					} else {
						_, err = man.Get(fmt.Sprintf("miss_%d", i), benchReadTs)
					}
					if err != nil && !errors.Is(err, ErrNotFound) {
						b.Fatalf("get: %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkAMinReadCriticalSection measures the Approach A-min read path with
// a memtable hit: the short RLock window whose duration the R2
// instrumentation reports as OnReadCriticalSection. The SSTable lease path is
// intentionally absent (no disk in EXP-3); the metadata-only contention
// behavior is covered by BenchmarkConcurrentReaders.
func BenchmarkAMinReadCriticalSection(b *testing.B) {
	for _, n := range benchCardinalities {
		b.Run(fmt.Sprintf("cardinality_%d", n), func(b *testing.B) {
			man, cleanup := benchManager(b, SyncModeApproachAMin, n)
			defer cleanup()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := man.Get(keyStr(benchStride(n, i)), benchReadTs); err != nil {
					b.Fatalf("get: %v", err)
				}
			}
		})
	}
}

// BenchmarkApproachBSnapshotRead measures the Approach B read path with a
// memtable hit (RLock window + no snapshot pin) and, separately, the pin +
// release cycle on the empty initial snapshot (pure metadata, no disk
// readers).
func BenchmarkApproachBSnapshotRead(b *testing.B) {
	for _, n := range benchCardinalities {
		b.Run(fmt.Sprintf("cardinality_%d/memtable_hit", n), func(b *testing.B) {
			man, cleanup := benchManager(b, SyncModeApproachB, n)
			defer cleanup()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := man.Get(keyStr(benchStride(n, i)), benchReadTs); err != nil {
					b.Fatalf("get: %v", err)
				}
			}
		})
	}
	b.Run("snapshot_pin_release", func(b *testing.B) {
		man, cleanup := benchManager(b, SyncModeApproachB, 1_000)
		defer cleanup()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			snap, err := man.AcquireSnapshot()
			if err != nil {
				b.Fatalf("acquire snapshot: %v", err)
			}
			man.ReleaseSnapshot(snap)
		}
	})
}

// BenchmarkFrozenMemtableScanDepth measures the newest-to-oldest frozen
// memtable probe that a missing key pays before reaching the disk tier.
// Frozen memtables are installed directly (no flush, hence no SSTable), so
// the measured miss path is purely in-memory metadata traversal.
func BenchmarkFrozenMemtableScanDepth(b *testing.B) {
	for _, depth := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("frozen_%d", depth), func(b *testing.B) {
			man, cleanup := benchManager(b, SyncModeApproachA, 1_000)
			defer cleanup()
			// Install `depth` frozen memtables of 1000 keys each, newest last
			// (the read path probes Frozenmem newest-to-oldest).
			man.Mu.Lock()
			for f := 0; f < depth; f++ {
				mt := &Memtable{
					SK: &SkipList{
						headNode:     &Node{Forward: make([]*Node, 32)},
						maxLevel:     32,
						currentLevel: 1,
					},
				}
				for k := 0; k < 1000; k++ {
					encoded := EncodeInternalKey([]byte(fmt.Sprintf("key%08d_gen", f*1000+k)), uint64(f+1), false)
					mt.Put(encoded, valBytes(k))
				}
				man.Frozenmem = append(man.Frozenmem, mt)
			}
			man.Mu.Unlock()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Every probe misses every tier: the measured cost is the
				// metadata scan, not any payload.
				if _, err := man.Get(fmt.Sprintf("miss_%d", i), benchReadTs); !errors.Is(err, ErrNotFound) {
					b.Fatalf("expected miss, got %v", err)
				}
			}
		})
	}
}

// BenchmarkHolderCounterPair isolates the marginal cost the R1.2e §3.2
// active-holder counter adds to one read: the increment/decrement pair around
// an RLock/RUnlock cycle, relative to the same cycle without the counter.
// This benchmark exists to document the instrumentation's invasiveness
// boundary; it asserts nothing.
func BenchmarkHolderCounterPair(b *testing.B) {
	man, cleanup := benchManager(b, SyncModeApproachA, 1)
	defer cleanup()
	b.Run("with_counter", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			man.rLock()
			man.rUnlock()
		}
	})
	b.Run("without_counter", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			man.Mu.RLock()
			man.Mu.RUnlock()
		}
	})
}

// BenchmarkConcurrentReaders measures aggregate read throughput under r
// concurrent reader goroutines at fixed cardinality, per architecture: the
// in-memory counterpart of the EXP-1/EXP-2 cells. The contention structure is
// the measured object.
func BenchmarkConcurrentReaders(b *testing.B) {
	const cardinality = 100_000
	for _, mode := range []SyncMode{SyncModeApproachA, SyncModeApproachAMin, SyncModeApproachB} {
		for _, readers := range []int{1, 4} {
			b.Run(fmt.Sprintf("%s/readers_%d", mode, readers), func(b *testing.B) {
				man, cleanup := benchManager(b, mode, cardinality)
				defer cleanup()
				var firstErr atomic.Value
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						key := keyStr(benchStride(cardinality, i))
						i++
						if _, err := man.Get(key, benchReadTs); err != nil {
							firstErr.CompareAndSwap(nil, err)
							return
						}
					}
				})
				b.StopTimer()
				if v := firstErr.Load(); v != nil {
					b.Fatalf("reader failed: %v", v.(error))
				}
			})
		}
	}
}
