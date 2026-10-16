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

// replaceCompactedReaders replaces the inputs in compactedSet with newReader,
// inserting newReader at the position of the first replaced reader, and preserving
// all other readers in their original relative chronological order.
func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader, newReader *SSTableReader) ([]*SSTableReader, error) {
	if len(inputs) == 0 {
		return current, nil
	}

	inputSet := make(map[*SSTableReader]bool, len(inputs))
	for _, r := range inputs {
		inputSet[r] = true
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

	updated := make([]*SSTableReader, 0, len(current)-len(inputs)+1)
	insertedNew := false

	for _, r := range current {
		if inputSet[r] {
			if !insertedNew {
				updated = append(updated, newReader)
				insertedNew = true
			}
			// Skip this compacted reader (it is replaced)
		} else {
			updated = append(updated, r)
		}
	}

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
