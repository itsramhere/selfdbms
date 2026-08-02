package lsmengine

import (
	"fmt"
	"os"
	"testing"
)

func BenchmarkSequentialWrites(b *testing.B) {
	// 1. Setup: Create a fresh directory for WAL and SSTables
	os.MkdirAll("bench_data", 0755)
	defer os.RemoveAll("bench_data")

	os.Chdir("bench_data")

	// 2. Initialize your Manager
	man := CreateManager()
	defer man.Close()

	// 3. Instruct Go to track memory allocations
	b.ReportAllocs()

	// 4. Reset the timer to exclude the setup time
	b.ResetTimer()

	// 5. The Benchmark Loop
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("key_%d", i)
		val := fmt.Sprintf("value_for_key_%d", i)

		// Transaction Logic
		txn := man.Begin()
		txn.Put(key, val)
		err := txn.Commit()
		if err != nil {
			b.Fatalf("Transaction failed to commit at iteration %d: %v", i, err)
		}
	}
}

func BenchmarkParallelWrites(b *testing.B) {
	os.MkdirAll("bench_data_parallel", 0755)
	defer os.RemoveAll("bench_data_parallel")

	os.Chdir("bench_data_parallel")

	man := CreateManager()
	defer man.Close()

	b.ReportAllocs()
	b.ResetTimer()

	// RunParallel spawns goroutines equal to GOMAXPROCS (12 on your Ryzen 7)
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			key := fmt.Sprintf("parallel_key_%d", i)
			val := fmt.Sprintf("parallel_value_%d", i)

			txn := man.Begin()
			txn.Put(key, val)
			_ = txn.Commit() // We ignore abort errors under heavy contention
		}
	})
}

func BenchmarkParallelReads(b *testing.B) {
	os.MkdirAll("bench_data_reads", 0755)
	defer os.RemoveAll("bench_data_reads")

	os.Chdir("bench_data_reads")

	man := CreateManager()
	defer man.Close()

	// 1. Pre-populate the database with 1000 keys
	numKeys := 1000
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("key_%d", i)
		txn := man.Begin()
		txn.Put(key, "benchmark_read_value_payload")
		_ = txn.Commit()
	}

	b.ReportAllocs()
	// 2. Reset timer: Do not penalize the read benchmark for the write setup!
	b.ResetTimer()

	// 3. Blast the database with concurrent read requests
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			// Cycle through the 1000 keys to simulate random access
			key := fmt.Sprintf("key_%d", i%numKeys)
			i++

			txn := man.Begin()
			_, err := txn.Get(key)
			if err != nil {
				b.Fatalf("Failed to read key: %v", err)
			}
			// No commit necessary for a purely read-only transaction
		}
	})
}

func BenchmarkSSTableReads(b *testing.B) {
	os.MkdirAll("bench_data_sstable", 0755)
	defer os.RemoveAll("bench_data_sstable")

	os.Chdir("bench_data_sstable")

	man := CreateManager()
	defer man.Close()

	numKeys := 1000
	// 1. Write the initial dataset
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("disk_key_%d", i)
		txn := man.Begin()
		txn.Put(key, "sst_value_payload_data")
		_ = txn.Commit()
	}

	// 2. Artificially isolate the Memtable and force a disk flush
	// This ensures the active Memtable is completely empty before we read.
	man.Mu.Lock()
	frozen := man.Mem
	man.Mem = &Memtable{
		SK: &SkipList{headNode: &Node{
			Forward: make([]*Node, 32),
		}, maxLevel: 32, currentLevel: 1},
		SizeBytes: 0,
	}
	man.Mu.Unlock()

	// Manually execute the background worker synchronously for the test
	man.flushToDisk(frozen)

	b.ReportAllocs()

	// 3. Reset the timer so write/flush time isn't counted
	b.ResetTimer()

	// 4. Blast the database with reads that will all trigger a cache miss
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("disk_key_%d", i%numKeys)
			i++

			txn := man.Begin()
			_, err := txn.Get(key)
			if err != nil {
				b.Fatalf("Failed to read key from SSTable: %v", err)
			}
		}
	})
}
