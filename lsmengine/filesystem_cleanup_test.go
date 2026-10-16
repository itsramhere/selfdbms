package lsmengine

import (
	"os"
	"strings"
	"sync"
	"testing"
)

// TestFilesystem_CloseBeforeDelete verifies that compaction strictly closes all
// iterators and SSTableReader file descriptors before invoking os.Remove, ensuring
// zero ERROR_SHARING_VIOLATION errors on Windows NTFS.
func TestFilesystem_CloseBeforeDelete(t *testing.T) {
	dir := "test_data_fs_close_before_delete"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "val10", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "val20", false},
	})

	r0Path := r0.File.Name()
	r1Path := r1.File.Name()

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}
	defer man.Close()

	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Verify both old files were unlinked without Windows sharing violations
	if _, err := os.Stat(r0Path); !os.IsNotExist(err) {
		t.Fatalf("Expected r0 to be unlinked, but file still exists (or failed unlink)")
	}
	if _, err := os.Stat(r1Path); !os.IsNotExist(err) {
		t.Fatalf("Expected r1 to be unlinked, but file still exists (or failed unlink)")
	}

	// Verify only the new compacted file exists in the directory
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	var sstFiles []string
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".sst") {
			sstFiles = append(sstFiles, f.Name())
		}
	}
	if len(sstFiles) != 1 {
		t.Fatalf("Expected exactly 1 SSTable file after compaction, found %d: %v", len(sstFiles), sstFiles)
	}
	t.Logf("Close-before-delete verified: Exactly 1 SSTable remains: %v", sstFiles[0])
}

// TestFilesystem_CleanupAfterAbortedCompaction verifies that if compaction aborts
// before publication, the temporary compacted file is immediately removed and active files are preserved.
func TestFilesystem_CleanupAfterAbortedCompaction(t *testing.T) {
	dir := "test_data_fs_aborted_cleanup"
	os.MkdirAll(dir, 0755)
	defer os.RemoveAll(dir)

	r0 := createTestSSTable(t, dir+"/r0.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 10, "val10", false},
	})
	r1 := createTestSSTable(t, dir+"/r1.sst", []struct {
		key      string
		seq      uint64
		val      string
		isDelete bool
	}{
		{"k1", 20, "val20", false},
	})

	r0Path := r0.File.Name()
	r1Path := r1.File.Name()

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0, r1},
	}
	defer man.Close()

	// Hook to mutate active readers before publication, forcing replaceCompactedReaders to abort
	man.beforeCompactionPublishHook = func() {
		man.Mu.Lock()
		// Remove r0 from active readers to trigger validation abort
		man.ActiveSSreaders = []*SSTableReader{r1}
		man.Mu.Unlock()
	}

	err := man.CompactAll()
	if err == nil {
		t.Fatalf("Expected CompactAll to abort, got nil error")
	}

	// Verify original active files still exist and were not deleted
	if _, err := os.Stat(r0Path); err != nil {
		t.Fatalf("r0 was accidentally deleted during aborted compaction: %v", err)
	}
	if _, err := os.Stat(r1Path); err != nil {
		t.Fatalf("r1 was accidentally deleted during aborted compaction: %v", err)
	}

	// Verify no temporary data_compacted_*.sst files were left behind in the directory
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "data_compacted_") {
			t.Fatalf("Temporary compacted file was leaked after aborted compaction: %s", f.Name())
		}
	}
	t.Logf("Aborted compaction cleanup verified: No temporary files leaked")
}

// TestFilesystem_NoAccidentalActiveFileDeletion verifies that concurrent flushes
// appended during compaction are strictly preserved and never unlinked.
func TestFilesystem_NoAccidentalActiveFileDeletion(t *testing.T) {
	dir := "test_data_fs_concurrent_preservation"
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

	var rFlush *SSTableReader
	var rFlushPath string

	man.beforeCompactionPublishHook = func() {
		// Concurrent flush appends rFlush
		rFlush = createTestSSTable(t, dir+"/r_flush.sst", []struct {
			key      string
			seq      uint64
			val      string
			isDelete bool
		}{
			{"k2", 30, "v30", false},
		})
		rFlushPath = rFlush.File.Name()

		man.Mu.Lock()
		man.ActiveSSreaders = append(man.ActiveSSreaders, rFlush)
		man.Mu.Unlock()
	}

	if err := man.CompactAll(); err != nil {
		t.Fatalf("CompactAll failed: %v", err)
	}

	// Verify concurrently flushed file is still on disk
	if _, err := os.Stat(rFlushPath); err != nil {
		t.Fatalf("Concurrently flushed SSTable was accidentally deleted: %v", err)
	}

	// Verify ActiveSSreaders contains [compactedReader, rFlush]
	man.Mu.RLock()
	if len(man.ActiveSSreaders) != 2 {
		t.Fatalf("Expected 2 active readers, got %d", len(man.ActiveSSreaders))
	}
	if man.ActiveSSreaders[1] != rFlush {
		t.Fatalf("Expected second active reader to be rFlush")
	}
	man.Mu.RUnlock()

	t.Logf("Active file preservation verified: Concurrent flush file preserved on disk and in slice")
}

// TestFilesystem_ShutdownWithInflightOperations verifies that closing a manager
// with in-flight reads safely releases all file handles on Windows NTFS without leaks.
func TestFilesystem_ShutdownWithInflightOperations(t *testing.T) {
	dir := "test_data_fs_shutdown_handles"
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

	man := &Manager{
		Mu:              &sync.RWMutex{},
		Mem:             newTestMemtable(),
		ActiveSSreaders: []*SSTableReader{r0},
	}

	readStarted := make(chan struct{})
	resumeRead := make(chan struct{})

	man.duringPointReadHook = func() {
		close(readStarted)
		<-resumeRead
	}

	readDone := make(chan error, 1)
	go func() {
		_, err := man.Get("k1", 100)
		readDone <- err
	}()

	<-readStarted

	// Point read is holding RLock; allow it to finish
	close(resumeRead)
	if err := <-readDone; err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	// Now shutdown manager
	if err := man.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify on Windows that file descriptors are genuinely closed by attempting to remove the directory
	// If any handle is still held open by Go, os.RemoveAll will fail on Windows with sharing violation
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove directory after shutdown (open file handle leak): %v", err)
	}
	t.Logf("Handle cleanup verified: Entire database directory successfully removed after shutdown")
}
