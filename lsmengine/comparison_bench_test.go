package lsmengine

import (
	"fmt"
	"os"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// BenchmarkBoltDBSequentialWrites measures B+Tree write throughput.
// Notice how B+Tree inplace page updates compare to LSM synchronous WAL appends.
func BenchmarkBoltDBSequentialWrites(b *testing.B) {
	os.MkdirAll("bench_data_bolt", 0755)
	defer os.RemoveAll("bench_data_bolt")

	db, err := bolt.Open("bench_data_bolt/bolt.db", 0600, nil)
	if err != nil {
		b.Fatalf("Failed to open bolt db: %v", err)
	}
	defer db.Close()

	// Initialize the default bucket
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("TestBucket"))
		return err
	})
	if err != nil {
		b.Fatalf("Failed to create bucket: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key_%d", i))
		val := []byte("benchmark_payload_value_data")

		// Each write opens and commits an explicit disk transaction
		err := db.Update(func(tx *bolt.Tx) error {
			bkt := tx.Bucket([]byte("TestBucket"))
			return bkt.Put(key, val)
		})
		if err != nil {
			b.Fatalf("Bolt write failed: %v", err)
		}
	}
}

// BenchmarkBoltDBParallelReads measures B+Tree point-query lookup speed.
func BenchmarkBoltDBParallelReads(b *testing.B) {
	os.MkdirAll("bench_data_bolt_reads", 0755)
	defer os.RemoveAll("bench_data_bolt_reads")

	db, err := bolt.Open("bench_data_bolt_reads/bolt.db", 0600, nil)
	if err != nil {
		b.Fatalf("Failed to open bolt db: %v", err)
	}
	defer db.Close()

	numKeys := 1000
	err = db.Update(func(tx *bolt.Tx) error {
		bkt, err := tx.CreateBucketIfNotExists([]byte("TestBucket"))
		if err != nil {
			return err
		}
		for i := 0; i < numKeys; i++ {
			k := []byte(fmt.Sprintf("key_%d", i))
			v := []byte("benchmark_payload_value_data")
			if err := bkt.Put(k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("Setup failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := []byte(fmt.Sprintf("key_%d", i%numKeys))
			i++

			err := db.View(func(tx *bolt.Tx) error {
				bkt := tx.Bucket([]byte("TestBucket"))
				val := bkt.Get(key)
				if val == nil {
					return fmt.Errorf("key not found")
				}
				return nil
			})
			if err != nil {
				b.Fatalf("Bolt read failed: %v", err)
			}
		}
	})
}
