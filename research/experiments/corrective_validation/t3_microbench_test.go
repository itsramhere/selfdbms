package corrective_validation

import (
	"path/filepath"
	"testing"

	"owndbms/lsmengine"
	"owndbms/research/experiments/harness"
)

// BenchmarkReaderSync_ApproachA_vs_ApproachB benchmarks pure synchronization overhead
// between Approach A (m.Mu.RLock/RUnlock) and Approach B (AcquireSnapshot/ReleaseSnapshot).
func BenchmarkReaderSync_ApproachA_vs_ApproachB(b *testing.B) {
	dir := b.TempDir()
	manA, err := lsmengine.CreateManagerWithConfig(lsmengine.Config{
		Mode:                   lsmengine.SyncModeApproachA,
		WalPath:                filepath.Join(dir, "walA.log"),
		DataDir:                dir,
		MemtableThresholdBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		b.Fatalf("create manager A: %v", err)
	}
	defer manA.Close()

	// Initial key
	txA := manA.Begin()
	txA.Put(harness.FormatKey(1), "val1")
	_ = txA.Commit()

	dirB := b.TempDir()
	manB, err := lsmengine.CreateManagerWithConfig(lsmengine.Config{
		Mode:                   lsmengine.SyncModeApproachB,
		WalPath:                filepath.Join(dirB, "walB.log"),
		DataDir:                dirB,
		MemtableThresholdBytes: 64 * 1024 * 1024,
	})
	if err != nil {
		b.Fatalf("create manager B: %v", err)
	}
	defer manB.Close()

	txB := manB.Begin()
	txB.Put(harness.FormatKey(1), "val1")
	_ = txB.Commit()

	b.Run("ApproachA_RLockRUnlock", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			manA.Mu.RLock()
			manA.Mu.RUnlock()
		}
	})

	b.Run("ApproachB_SnapshotAcquireRelease", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			snap, err := manB.AcquireSnapshot()
			if err != nil {
				b.Fatalf("acquire failed: %v", err)
			}
			manB.ReleaseSnapshot(snap)
		}
	})
}
