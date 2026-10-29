package corrective_validation

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"owndbms/lsmengine"
	"owndbms/research/experiments/harness"
)

// deterministicPublicationObserver tracks exact publication lock timings.
type deterministicPublicationObserver struct {
	isPublishing     atomic.Bool
	publishBeginTime atomic.Int64
	publishEndTime   atomic.Int64
	lockHoldNanos    atomic.Int64
}

func (d *deterministicPublicationObserver) OnFlushRequested()            {}
func (d *deterministicPublicationObserver) OnFlushAdmitted(bool, error) {}
func (d *deterministicPublicationObserver) OnFlushWorkerBegin()          {}
func (d *deterministicPublicationObserver) OnFlushBuildBegin()           {}
func (d *deterministicPublicationObserver) OnFlushBuildEnd(error)        {}
func (d *deterministicPublicationObserver) OnFlushPublishBegin(activeRLockHolders int) {
	d.isPublishing.Store(true)
	d.publishBeginTime.Store(time.Now().UnixNano())
}
func (d *deterministicPublicationObserver) OnFlushPublishEnd(error) {
	end := time.Now().UnixNano()
	d.publishEndTime.Store(end)
	d.isPublishing.Store(false)
	begin := d.publishBeginTime.Load()
	if begin > 0 {
		d.lockHoldNanos.Store(end - begin)
	}
}
func (d *deterministicPublicationObserver) OnFlushComplete(error) {}

// TestT1_DeterministicPublicationBarrier isolates publication lock acquisition
// and reader blocking without introducing disk-work or scheduling variance.
func TestT1_DeterministicPublicationBarrier(t *testing.T) {
	for _, mode := range []struct {
		name string
		sync lsmengine.SyncMode
	}{
		{"ApproachA_RWMutexBaseline", lsmengine.SyncModeApproachA},
		{"ApproachB_ReaderSnapshot", lsmengine.SyncModeApproachB},
	} {
		t.Run(mode.name, func(t *testing.T) {
			dir := t.TempDir()
			man, err := lsmengine.CreateManagerWithConfig(lsmengine.Config{
				Mode:                   mode.sync,
				WalPath:                filepath.Join(dir, "wal.log"),
				DataDir:                dir,
				MemtableThresholdBytes: 64 * 1024 * 1024,
			})
			if err != nil {
				t.Fatalf("create manager: %v", err)
			}
			defer man.Close()

			// Populate initial fixture: 100 keys
			tx := man.Begin()
			for k := 0; k < 100; k++ {
				tx.Put(harness.FormatKey(k), "val_fixture_data_payload")
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("fixture commit failed: %v", err)
			}
			if err := man.TriggerFlush(); err != nil {
				t.Fatalf("initial flush failed: %v", err)
			}
			if err := man.WaitForFlushCompletion(); err != nil {
				t.Fatalf("initial flush wait failed: %v", err)
			}

			// Add observer
			obs := &deterministicPublicationObserver{}
			man.SetFlushObserver(obs)

			// Dirty memtable with a second commit so flush worker will build and publish
			tx2 := man.Begin()
			tx2.Put(harness.FormatKey(101), "val_new_commit")
			if err := tx2.Commit(); err != nil {
				t.Fatalf("dirty commit failed: %v", err)
			}

			workerInHook := make(chan struct{})
			unblockHook := make(chan struct{})
			readerHoldingAccess := make(chan struct{})
			unblockReader := make(chan struct{})
			readerFinished := make(chan struct{})

			// Pause background flush worker during build phase
			man.SetFlushBuildHook(func() error {
				close(workerInHook)
				<-unblockHook
				return nil
			})

			// Trigger flush: admits, freezes memtable, background worker starts
			if err := man.TriggerFlush(); err != nil {
				t.Fatalf("TriggerFlush failed: %v", err)
			}

			// Wait until worker is paused in build hook
			select {
			case <-workerInHook:
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for worker build hook")
			}

			// Launch reader that acquires access
			go func() {
				if mode.sync == lsmengine.SyncModeApproachA {
					man.Mu.RLock()
					close(readerHoldingAccess)
					<-unblockReader
					man.Mu.RUnlock()
				} else {
					snap, sErr := man.AcquireSnapshot()
					if sErr != nil {
						t.Errorf("acquire snapshot failed: %v", sErr)
						return
					}
					close(readerHoldingAccess)
					<-unblockReader
					man.ReleaseSnapshot(snap)
				}
				close(readerFinished)
			}()

			// Wait until reader is holding access
			<-readerHoldingAccess

			// Unblock flush worker to begin publication phase
			close(unblockHook)

			// Wait briefly for publication to start
			deadline := time.Now().Add(2 * time.Second)
			for !obs.isPublishing.Load() {
				if time.Now().After(deadline) {
					break // In Approach B, publication might finish instantaneously if uncontended
				}
				time.Sleep(1 * time.Millisecond)
			}

			// Measure an arriving reader attempting Get()
			arrivingReaderDone := make(chan struct{})
			var arrivingLatencyNanos int64
			go func() {
				t0 := time.Now()
				_, _ = man.Get(harness.FormatKey(10), 1000)
				arrivingLatencyNanos = time.Since(t0).Nanoseconds()
				close(arrivingReaderDone)
			}()

			time.Sleep(20 * time.Millisecond)

			// Release holding reader
			close(unblockReader)

			select {
			case <-readerFinished:
			case <-time.After(3 * time.Second):
				t.Fatalf("reader finished timed out")
			}

			select {
			case <-arrivingReaderDone:
			case <-time.After(3 * time.Second):
				t.Fatalf("arriving reader timed out")
			}

			_ = man.WaitForFlushCompletion()

			t.Logf("[%s] Arriving reader latency: %d ns, publication hold duration: %d ns",
				mode.name, arrivingLatencyNanos, obs.lockHoldNanos.Load())
		})
	}
}
