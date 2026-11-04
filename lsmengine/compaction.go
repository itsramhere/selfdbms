package lsmengine

import (
	"bytes"
	"container/heap"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// replaceCompactedReaders replaces the input readers with newReader in current.
// Under INV-ORDER-1 and INV-COMPACT-1, compaction inputs must form a strictly contiguous
// prefix starting at index 0, preserving chronological precedence of all surviving readers.
// Any concurrently flushed SSTables appended after the compacted prefix are preserved.
func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader, newReader *SSTableReader) ([]*SSTableReader, error) {
	if len(inputs) == 0 {
		return current, nil
	}

	if newReader == nil {
		return nil, errors.New("compaction aborted: nil replacement reader")
	}

	// 1. Ensure each input reader appears exactly once (no duplicates) and is non-nil
	inputSet := make(map[*SSTableReader]bool, len(inputs))
	for _, r := range inputs {
		if r == nil {
			return nil, errors.New("compaction aborted: nil input reader")
		}
		if inputSet[r] {
			return nil, errors.New("compaction aborted: duplicate input reader in inputs")
		}
		inputSet[r] = true
	}

	// 2. Verify all inputs are present in current
	currentSet := make(map[*SSTableReader]bool, len(current))
	for _, r := range current {
		currentSet[r] = true
	}
	for _, r := range inputs {
		if !currentSet[r] {
			return nil, errors.New("compaction aborted: input reader is no longer active")
		}
	}

	// 3. Verify inputs preserve the full-prefix policy (must start at index 0)
	if current[0] != inputs[0] {
		return nil, errors.New("compaction aborted: inputs must start at index 0 to preserve full-prefix policy")
	}

	// 4. Verify inputs form a strictly contiguous range in current
	for j := 0; j < len(inputs); j++ {
		if current[j] != inputs[j] {
			return nil, errors.New("compaction aborted: inputs are not contiguous in current active readers")
		}
	}

	// 5. Construct updated reader set:
	// New reader is published at index 0, followed by all concurrently flushed readers.
	updated := make([]*SSTableReader, 0, len(current)-len(inputs)+1)
	updated = append(updated, newReader)
	updated = append(updated, current[len(inputs):]...)

	return updated, nil
}

func (m *Manager) CompactAll() error {
	startNs := m.readCSStart()
	if m.syncMode == SyncModeApproachB {
		return m.compactAllApproachBObserved(startNs)
	}

	// Serialize all compaction lifetimes to prevent overlapping compaction races (E-006).
	// Lock hierarchy: compactionMu -> Manager.Mu -> Wal.mu
	m.compactionMu.Lock()
	defer m.compactionMu.Unlock()

	startArrival := m.writerLockArrival(LockSiteCompactionStart)
	m.Mu.Lock()
	startHold := m.beginPublication(LockSiteCompactionStart, startArrival.holders)
	startArrival.acquiredAt(m, startHold.startNs)
	if m.isClosed || m.state != EngineOpen {
		startHold.end(m)
		m.Mu.Unlock()
		return errors.New("compaction aborted: engine is closed")
	}

	if len(m.ActiveSSreaders) <= 1 {
		startHold.end(m)
		m.Mu.Unlock()
		return nil // Nothing to compact
	}

	// Snapshotting the files to compact.
	// Make a shallow copy of the slice so readersToCompact is immutable to subsequent appends on m.ActiveSSreaders.
	readersToCompact := make([]*SSTableReader, len(m.ActiveSSreaders))
	copy(readersToCompact, m.ActiveSSreaders)

	var filenames []string
	for _, r := range readersToCompact {
		filenames = append(filenames, r.File.Name())
	}

	// compactionMu prevents compaction and Close from retiring these inputs;
	// concurrent flush publication only appends to the copied prefix.
	if m.compactionOpenOutsideLock {
		startHold.end(m)
		m.Mu.Unlock()
	}
	var iterators []*SSTableIterator
	for _, fname := range filenames {
		var it *SSTableIterator
		var err error
		if m.compactionIteratorOpenHook != nil {
			it, err = m.compactionIteratorOpenHook(fname)
		} else {
			it, err = NewSSTableIterator(fname)
		}
		if err != nil {
			if !m.compactionOpenOutsideLock {
				startHold.end(m)
				m.Mu.Unlock()
			}
			for _, openedIt := range iterators {
				openedIt.Close()
			}
			return err
		}
		iterators = append(iterators, it)
	}
	if !m.compactionOpenOutsideLock {
		startHold.end(m)
		m.Mu.Unlock()
	}

	// Deterministic concurrency test hook: pause during merge outside m.Mu
	if m.duringCompactionMergeHook != nil {
		m.duringCompactionMergeHook()
	}

	minHeap := &CompactionHeap{}
	heap.Init(minHeap)

	for i, it := range iterators {
		k, v, err := it.Next()
		if err != nil && err != io.EOF {
			for _, openedIt := range iterators {
				openedIt.Close()
			}
			return err
		}
		if err == nil {
			heap.Push(minHeap, &HeapItem{
				InternalKey: k,
				Value:       v,
				IteratorIdx: i,
			})
		}
	}

	// New compacted SSTable
	outputDir := filepath.Dir(filenames[0])
	newFilename := filepath.Join(outputDir, fmt.Sprintf("data_compacted_%d.sst", time.Now().UnixNano()))
	newFile, err := os.Create(newFilename)
	if err != nil {
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return err
	}

	builder := &SSTableBuilder{
		File:        newFile,
		BlockBuffer: new(bytes.Buffer),
	}

	// K-way merge
	var lastUserKey []byte

	for minHeap.Len() > 0 {
		item := heap.Pop(minHeap).(*HeapItem)
		currentUserKey, _, isDelete := DecodeInternalKey(item.InternalKey)

		// Deduplication check: highest sequence number for currentUserKey pops first
		if lastUserKey == nil || !bytes.Equal(currentUserKey, lastUserKey) {
			if !isDelete {
				if err := builder.Add(item.InternalKey, item.Value); err != nil {
					builder.Close()
					os.Remove(newFilename)
					for _, openedIt := range iterators {
						openedIt.Close()
					}
					return err
				}
			}

			// Update lastUserKey to track deduplication
			lastUserKey = make([]byte, len(currentUserKey))
			copy(lastUserKey, currentUserKey)
		}

		nextKey, nextVal, err := iterators[item.IteratorIdx].Next()
		if err == nil {
			heap.Push(minHeap, &HeapItem{
				InternalKey: nextKey,
				Value:       nextVal,
				IteratorIdx: item.IteratorIdx,
			})
		}
	}

	if err := builder.Close(); err != nil {
		os.Remove(newFilename)
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return err
	}

	newReader, err := m.newSSTableReader(newFilename)
	if err != nil {
		os.Remove(newFilename)
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return err
	}

	// Deterministic concurrency test hook: pause immediately before publication
	if m.beforeCompactionPublishHook != nil {
		m.beforeCompactionPublishHook()
	}

	// Protected identity-preserving publication under write lock
	pubArrival := m.writerLockArrival(LockSiteCompactionPublish)
	m.Mu.Lock()
	pubArrival.acquired(m)
	pub := m.beginPublication(LockSiteCompactionPublish, pubArrival.holders)
	if m.isClosed || m.state != EngineOpen {
		pub.end(m)
		m.Mu.Unlock()
		newReader.Close()
		os.Remove(newFilename)
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return errors.New("compaction aborted: engine is closed")
	}

	updatedReaders, err := replaceCompactedReaders(m.ActiveSSreaders, readersToCompact, newReader)
	if err != nil {
		pub.end(m)
		m.Mu.Unlock()
		newReader.Close()
		os.Remove(newFilename)
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return err
	}
	m.ActiveSSreaders = updatedReaders
	m.compactionsCompleted.Add(1)

	// Approach A-min: the linearization point of supersession is here, inside the
	// same write-lock section that removed the inputs from ActiveSSreaders. After
	// this point no new lease can be granted to them (INV I-3/I-4).
	amin := m.syncMode == SyncModeApproachAMin
	if amin {
		for _, r := range readersToCompact {
			r.retirement = m.observeRetirement()
			r.retired.Store(true)
			// Track retired tables so shutdown can drain leases on them and
			// finish their reclamation (see Manager.countOutstandingLeasesLocked).
			m.retiredReaders = append(m.retiredReaders, r)
		}
		m.pruneRetiredReadersLocked()
	}
	if !amin {
		for _, r := range readersToCompact {
			r.retirement = m.observeRetirement()
		}
	}
	pub.end(m)
	m.Mu.Unlock()

	// Retirement and resource cleanup
	for _, it := range iterators {
		it.Close()
	}
	if amin {
		// Free only tables whose last in-flight reader has already left; the
		// remainder are reclaimed by their last lease release.
		for _, r := range readersToCompact {
			m.reclaimIfDrained(r)
		}
		m.observeCompaction(startNs, len(readersToCompact))
		return nil
	}
	for _, r := range readersToCompact {
		r.Close()
		if err := os.Remove(r.File.Name()); err == nil {
			r.retirement.removal()
		}
	}

	m.observeCompaction(startNs, len(readersToCompact))
	return nil
}

func (m *Manager) compactAllApproachBObserved(startNs int64) error {
	m.compactionMu.Lock()
	defer m.compactionMu.Unlock()

	m.Mu.Lock()
	if m.isClosed || m.state != EngineOpen || m.compactionAdmissionClosed {
		m.Mu.Unlock()
		return errors.New("compaction aborted: engine is closed")
	}
	m.Mu.Unlock()

	// 1. Acquire snapshot and lease compaction input readers under snapshotMu
	m.snapshotMu.Lock()
	if m.currentSnapshot == nil || len(m.currentSnapshot.readers) <= 1 {
		m.snapshotMu.Unlock()
		return nil // Nothing to compact
	}

	snap := m.currentSnapshot
	readersToCompact := make([]*SharedReader, len(snap.readers))
	copy(readersToCompact, snap.readers)
	for _, sr := range readersToCompact {
		sr.Ref() // Add compaction lease
	}
	m.snapshotMu.Unlock()

	// 2. Open iterators outside all locks
	var filenames []string
	for _, r := range readersToCompact {
		filenames = append(filenames, r.Path)
	}

	var iterators []*SSTableIterator
	for _, fname := range filenames {
		it, err := NewSSTableIterator(fname)
		if err != nil {
			// Rollback leases on iterator failure
			m.snapshotMu.Lock()
			for _, sr := range readersToCompact {
				sr.UnrefLocked()
				m.checkCleanupEligibilityLocked(sr)
			}
			m.snapshotMu.Unlock()
			for _, openedIt := range iterators {
				openedIt.Close()
			}
			return err
		}
		iterators = append(iterators, it)
	}

	// 3. Build replacement SSTable via k-way merge outside all locks
	outputDir := filepath.Dir(filenames[0])
	newFilename := filepath.Join(outputDir, fmt.Sprintf("data_compacted_%d.sst", time.Now().UnixNano()))
	file, err := os.Create(newFilename)
	if err != nil {
		m.snapshotMu.Lock()
		for _, sr := range readersToCompact {
			sr.UnrefLocked()
			m.checkCleanupEligibilityLocked(sr)
		}
		m.snapshotMu.Unlock()
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return err
	}

	builder := &SSTableBuilder{
		File:        file,
		BlockBuffer: new(bytes.Buffer),
	}

	minHeap := &CompactionHeap{}
	heap.Init(minHeap)

	for i, it := range iterators {
		k, v, err := it.Next()
		if err != nil && !errors.Is(err, io.EOF) {
			for _, openedIt := range iterators {
				openedIt.Close()
			}
			return err
		}
		if err == nil {
			heap.Push(minHeap, &HeapItem{
				InternalKey: k,
				Value:       v,
				IteratorIdx: i,
			})
		}
	}

	var lastUserKey []byte

	for minHeap.Len() > 0 {
		item := heap.Pop(minHeap).(*HeapItem)
		currentUserKey, _, isDelete := DecodeInternalKey(item.InternalKey)

		// Deduplication check: highest sequence number for currentUserKey pops first
		if lastUserKey == nil || !bytes.Equal(currentUserKey, lastUserKey) {
			if !isDelete {
				if err := builder.Add(item.InternalKey, item.Value); err != nil {
					builder.Close()
					os.Remove(newFilename)
					for _, openedIt := range iterators {
						openedIt.Close()
					}
					return err
				}
			}

			lastUserKey = make([]byte, len(currentUserKey))
			copy(lastUserKey, currentUserKey)
		}

		// Advance iterator that supplied popped item
		k, v, err := iterators[item.IteratorIdx].Next()
		if err == nil {
			heap.Push(minHeap, &HeapItem{
				InternalKey: k,
				Value:       v,
				IteratorIdx: item.IteratorIdx,
			})
		} else if err != nil && !errors.Is(err, io.EOF) {
			builder.Close()
			os.Remove(newFilename)
			for _, openedIt := range iterators {
				openedIt.Close()
			}
			return err
		}
	}

	for _, openedIt := range iterators {
		openedIt.Close()
	}

	if err := builder.Close(); err != nil {
		os.Remove(newFilename)
		m.snapshotMu.Lock()
		for _, sr := range readersToCompact {
			sr.UnrefLocked()
			m.checkCleanupEligibilityLocked(sr)
		}
		m.snapshotMu.Unlock()
		return err
	}

	newReader, err := m.newSSTableReader(newFilename)
	if err != nil {
		os.Remove(newFilename)
		m.snapshotMu.Lock()
		for _, sr := range readersToCompact {
			sr.UnrefLocked()
			m.checkCleanupEligibilityLocked(sr)
		}
		m.snapshotMu.Unlock()
		return err
	}

	newSR := NewSharedReader(newReader, newFilename)

	if m.beforeCompactionPublishHook != nil {
		m.beforeCompactionPublishHook()
	}

	// 4. Protected publication under snapshotMu
	arrival := m.writerLockArrival(LockSiteCompactionSnapshotPublish)
	m.snapshotMu.Lock()
	pub := m.beginPublication(LockSiteCompactionSnapshotPublish, 0)
	arrival.acquiredAt(m, pub.startNs)
	if m.snapshotPhase >= SnapshotPhaseClosed {
		pub.end(m)
		m.snapshotMu.Unlock()
		m.rollbackUnpublishedReader(newSR)
		m.snapshotMu.Lock()
		for _, sr := range readersToCompact {
			sr.UnrefLocked()
			m.checkCleanupEligibilityLocked(sr)
		}
		m.snapshotMu.Unlock()
		return errors.New("compaction aborted: engine is closed")
	}

	// Validate that readersToCompact still forms a contiguous prefix of m.currentSnapshot.readers
	currSnap := m.currentSnapshot
	if currSnap == nil || len(currSnap.readers) < len(readersToCompact) {
		pub.end(m)
		m.snapshotMu.Unlock()
		m.rollbackUnpublishedReader(newSR)
		m.snapshotMu.Lock()
		for _, sr := range readersToCompact {
			sr.UnrefLocked()
			m.checkCleanupEligibilityLocked(sr)
		}
		m.snapshotMu.Unlock()
		return errors.New("compaction input validation failed: snapshot changed")
	}

	for i := range readersToCompact {
		if currSnap.readers[i] != readersToCompact[i] {
			pub.end(m)
			m.snapshotMu.Unlock()
			m.rollbackUnpublishedReader(newSR)
			m.snapshotMu.Lock()
			for _, sr := range readersToCompact {
				sr.UnrefLocked()
				m.checkCleanupEligibilityLocked(sr)
			}
			m.snapshotMu.Unlock()
			return fmt.Errorf("compaction input reader mismatch at index %d", i)
		}
	}

	// Reconstruct new reader slice: [newSR, concurrentFlushes...]
	concurrentFlushes := currSnap.readers[len(readersToCompact):]
	var newReaders []*SharedReader
	if newSR != nil {
		newReaders = append(newReaders, newSR)
		newSR.SetState(SharedReaderActive)
	}
	for _, sr := range concurrentFlushes {
		sr.Ref()
		newReaders = append(newReaders, sr)
	}

	// Publish new snapshot and retire old snapshot
	newSnap := &ReaderSnapshot{
		id:      atomic.AddUint64(&m.snapshotSeq, 1),
		readers: newReaders,
	}
	newSnap.SetState(SnapshotPublished)
	m.currentSnapshot = newSnap
	m.compactionsCompleted.Add(1)

	m.retireSnapshotLocked(currSnap)

	// Mark superseded input readers obsolete
	for _, sr := range readersToCompact {
		sr.retirement = m.observeRetirement()
		sr.MarkObsoleteLocked()
		sr.UnrefLocked() // Drop compaction lease
		m.checkCleanupEligibilityLocked(sr)
	}

	// Trigger asynchronous cleanup for any now-eligible obsolete readers.
	// Registered under m.snapshotMu so the positive cleanupWg.Add is strictly ordered
	// before closeApproachB's final cleanupWg.Wait() (CP3a-V.2 / E-013).
	m.scheduleAsyncCleanupLocked()

	pub.end(m)
	m.snapshotMu.Unlock()

	m.observeCompaction(startNs, len(readersToCompact))
	return nil
}

func (m *Manager) compactAllApproachB() error { return m.compactAllApproachBObserved(m.readCSStart()) }
