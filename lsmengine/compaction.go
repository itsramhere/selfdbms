package lsmengine

import (
	"bytes"
	"container/heap"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// replaceCompactedReaders replaces a contiguous prefix of inputs in current with newReader.
// Under INV-COMPACT-1 and INV-ORDER-1, inputs must:
// 1. Be non-empty and non-nil.
// 2. Contain no duplicates.
// 3. Match a strictly contiguous prefix of current starting at index 0.
// Any concurrently flushed SSTables appended after the compacted prefix are preserved
// in their exact chronological order.
func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader, newReader *SSTableReader) ([]*SSTableReader, error) {
	if len(inputs) == 0 {
		return current, nil
	}
	if newReader == nil {
		return nil, errors.New("compaction aborted: nil replacement reader")
	}
	if len(inputs) > len(current) {
		return nil, errors.New("compaction aborted: input reader is no longer active")
	}

	// Verify each input reader appears exactly once and no nil readers
	seenInputs := make(map[*SSTableReader]bool, len(inputs))
	for _, r := range inputs {
		if r == nil {
			return nil, errors.New("compaction aborted: nil input reader")
		}
		if seenInputs[r] {
			return nil, errors.New("compaction aborted: duplicate input reader")
		}
		seenInputs[r] = true
	}

	// Verify all inputs are present in current
	currentSet := make(map[*SSTableReader]bool, len(current))
	for _, r := range current {
		currentSet[r] = true
	}
	for _, r := range inputs {
		if !currentSet[r] {
			return nil, errors.New("compaction aborted: input reader is no longer active")
		}
	}

	// Find the position of inputs[0] in current
	startIdx := -1
	for i, r := range current {
		if r == inputs[0] {
			startIdx = i
			break
		}
	}
	if startIdx == -1 {
		return nil, errors.New("compaction aborted: input reader is no longer active")
	}

	// Under INV-COMPACT-1, compaction must preserve full-prefix selection policy (must start at index 0)
	if startIdx != 0 {
		return nil, errors.New("compaction aborted: inputs must form a prefix starting at index 0")
	}

	// Verify strictly contiguous range matching inputs
	for j, r := range inputs {
		if current[startIdx+j] != r {
			return nil, errors.New("compaction aborted: input readers are not strictly contiguous")
		}
	}

	// Build updated reader slice: [newReader, concurrentlyFlushed...]
	updated := make([]*SSTableReader, 0, len(current)-len(inputs)+1)
	updated = append(updated, newReader)
	updated = append(updated, current[len(inputs):]...)

	return updated, nil
}

func (m *Manager) CompactAll() error {
	m.Mu.Lock()
	if len(m.ActiveSSreaders) <= 1 {
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

	var iterators []*SSTableIterator
	for _, fname := range filenames {
		it, err := NewSSTableIterator(fname)
		if err != nil {
			m.Mu.Unlock()
			for _, openedIt := range iterators {
				openedIt.Close()
			}
			return err
		}
		iterators = append(iterators, it)
	}
	m.Mu.Unlock()

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
	newFilename := fmt.Sprintf("data_compacted_%d.sst", time.Now().UnixNano())
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

	newReader, err := NewSSTableReader(newFilename)
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
	m.Mu.Lock()
	updatedReaders, err := replaceCompactedReaders(m.ActiveSSreaders, readersToCompact, newReader)
	if err != nil {
		m.Mu.Unlock()
		newReader.Close()
		os.Remove(newFilename)
		for _, openedIt := range iterators {
			openedIt.Close()
		}
		return err
	}
	m.ActiveSSreaders = updatedReaders
	m.Mu.Unlock()

	// Retirement and resource cleanup
	for _, it := range iterators {
		it.Close()
	}
	for _, r := range readersToCompact {
		r.Close()
		os.Remove(r.File.Name())
	}

	return nil
}
