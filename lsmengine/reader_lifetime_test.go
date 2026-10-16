package lsmengine

import (
	"os"
	"sync"
	"testing"
	"time"
)

// Case C: Verify that a point query holding an old reader completes before that reader is closed or deleted.
func TestReaderLifetime_CaseC_ReadDuringRetirement(t *testing.T) {
	dir := "test_data_lifetime_case_c"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// Create 2 SSTables
	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 10, "val0", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"key1", 20, "val1", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}
	defer man.Close()

	readStarted := make(chan struct{})
	resumeRead := make(chan struct{})

	man.duringPointReadHook = func() {
		close(readStarted)
		<-resumeRead
	}

	readDone := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		val, err := man.Get("key1", 100)
		readDone <- val
		readErr <- err
	}()

	<-readStarted

	// Compaction starts in another goroutine while Get holds m.Mu.RLock()
	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- man.CompactAll()
	}()

	// Give compaction time to attempt acquiring m.Mu.Lock()
	time.Sleep(50 * time.Millisecond)

	// Verify that neither r0 nor r1 has been closed or unlinked yet
	if _, err := os.Stat(r0.File.Name()); err != nil {
		t.Fatalf("r0 file was unlinked prematurely while read was in-flight: %v", err)
	}
	if _, err := os.Stat(r1.File.Name()); err != nil {
		t.Fatalf("r1 file was unlinked prematurely while read was in-flight: %v", err)
	}

	// Now allow read to complete
	close(resumeRead)

	val := <-readDone
	if err := <-readErr; err != nil {
		t.Fatalf("Point read failed: %v", err)
	}
	if val != "val1" {
		t.Fatalf("Expected 'val1', got %q", val)
	}

	// Now compaction can proceed to publication and retirement
	if err := <-compactionDone; err != nil {
		t.Fatalf("Compaction failed: %v", err)
	}

	// After compaction finishes, r0 and r1 files must be retired and unlinked
	if _, err := os.Stat(r0.File.Name()); !os.IsNotExist(err) {
		t.Fatalf("Expected r0 to be unlinked after retirement, but it still exists")
	}
	if _, err := os.Stat(r1.File.Name()); !os.IsNotExist(err) {
		t.Fatalf("Expected r1 to be unlinked after retirement, but it still exists")
	}

	t.Logf("Case C PASS: Point read completed under RLock before reader retirement and unlinking")
}

// Case D: Failure paths during compaction
func TestReaderLifetime_CaseD_FailurePaths(t *testing.T) {
	dir := "test_data_lifetime_case_d"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	// Create 2 SSTables
	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "v0", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "v1", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}
	defer man.Close()

	// Corrupt r1 by truncating it to an invalid size to force NewSSTableIterator failure
	// Wait, we need r1 to fail in NewSSTableIterator or Next
	// Let's close r1 file and delete it to force NewSSTableIterator error
	r1File := r1.File.Name()
	r1.Close()
	os.Remove(r1File)

	err := man.CompactAll()
	if err == nil {
		t.Fatalf("Expected CompactAll to fail on missing file, but got nil")
	}

	// Verify that active readers were not mutated
	man.Mu.RLock()
	if len(man.ActiveSSreaders) != 2 {
		t.Fatalf("ActiveSSreaders mutated on iterator open error: %d", len(man.ActiveSSreaders))
	}
	man.Mu.RUnlock()

	t.Logf("Case D PASS: Iterator open failure safely aborted without mutating active readers")
}

// Case A: Two overlapping compactions
func TestReaderLifetime_CaseA_OverlappingCompactions(t *testing.T) {
	dir := "test_data_lifetime_case_a"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "v10", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "v20", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}
	defer man.Close()

	compAPaused := make(chan struct{})
	resumeCompA := make(chan struct{})

	// Hook triggers on Compaction A
	var hookOnce sync.Once
	man.duringCompactionMergeHook = func() {
		hookOnce.Do(func() {
			close(compAPaused)
			<-resumeCompA
		})
	}

	compADone := make(chan error, 1)
	go func() {
		compADone <- man.CompactAll()
	}()

	<-compAPaused

	// While Compaction A is paused, attempt Compaction B
	compBDone := make(chan error, 1)
	go func() {
		compBDone <- man.CompactAll()
	}()

	// Wait briefly to allow Compaction B to reach its merge
	time.Sleep(50 * time.Millisecond)

	// Release Compaction A
	close(resumeCompA)

	errA := <-compADone
	errB := <-compBDone

	if errA != nil {
		t.Fatalf("Compaction A failed: %v", errA)
	}
	if errB != nil {
		t.Fatalf("Compaction B failed: %v", errB)
	}

	// Verify both completed safely without data loss
	man.Mu.RLock()
	activeCount := len(man.ActiveSSreaders)
	man.Mu.RUnlock()

	if activeCount != 1 {
		t.Fatalf("Expected 1 merged SSTable after serialized compactions, got %d", activeCount)
	}

	val, err := man.Get("k1", 100)
	if err != nil {
		t.Fatalf("Get after overlapping compactions failed: %v", err)
	}
	if val != "v20" {
		t.Fatalf("Expected 'v20', got %q", val)
	}

	t.Logf("Case A PASS: Overlapping compactions safely serialized without reader conflicts or data loss")
}

// Case B: Shutdown during compaction
func TestReaderLifetime_CaseB_ShutdownDuringCompaction(t *testing.T) {
	dir := "test_data_lifetime_case_b"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "v10", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "v20", false},
	})

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}

	compPaused := make(chan struct{})
	resumeComp := make(chan struct{})

	man.duringCompactionMergeHook = func() {
		close(compPaused)
		<-resumeComp
	}

	compDone := make(chan error, 1)
	go func() {
		compDone <- man.CompactAll()
	}()

	<-compPaused

	// While compaction is paused in merge, invoke Manager.Close() in background
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- man.Close()
	}()

	// Verify Manager.Close() blocks on compactionMu and does not close readers prematurely
	time.Sleep(50 * time.Millisecond)

	man.Mu.RLock()
	if man.isClosed {
		man.Mu.RUnlock()
		t.Fatalf("Manager prematurely marked isClosed before compaction completed")
	}
	man.Mu.RUnlock()

	// Now resume compaction
	close(resumeComp)

	compErr := <-compDone
	if compErr != nil {
		t.Fatalf("Compaction failed during shutdown: %v", compErr)
	}

	closeErr := <-closeDone
	if closeErr != nil {
		t.Fatalf("Manager.Close failed: %v", closeErr)
	}

	// Verify manager state: isClosed is true and active readers slice is nil
	man.Mu.RLock()
	if !man.isClosed {
		t.Fatalf("Expected isClosed to be true after Close()")
	}
	if man.ActiveSSreaders != nil {
		t.Fatalf("Expected ActiveSSreaders to be nil after Close(), got %v", man.ActiveSSreaders)
	}
	man.Mu.RUnlock()

	t.Logf("Case B PASS: Shutdown safely coordinated with in-flight compaction without premature reader closure")
}



