package lsmengine

import (
	"bytes"
	"container/heap"
	"fmt"
	"io"
	"os"
	"time"
)

func (m *Manager) CompactAll() error {
	m.Mu.Lock()
	if len(m.ActiveSSreaders) <= 1 {
		m.Mu.Unlock()
		return nil // Nothing to compact
	}

	// snapshotting the files to compact
	readersToCompact := m.ActiveSSreaders
	var filenames []string
	for _, r := range readersToCompact {
		filenames = append(filenames, r.File.Name())
	}

	var iterators []*SSTableIterator
	for _, fname := range filenames {
		it, err := NewSSTableIterator(fname)
		if err != nil {
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

	// new compacted SSTable
	newFilename := fmt.Sprintf("data_compacted_%d.sst", time.Now().UnixNano())
	newFile, err := os.Create(newFilename)
	if err != nil {
		return err
	}

	builder := &SSTableBuilder{
		File:        newFile,
		BlockBuffer: new(bytes.Buffer),
	}

	// 5. k way merge
	var lastUserKey []byte

	for minHeap.Len() > 0 {
		item := heap.Pop(minHeap).(*HeapItem)

		currentUserKey, _, isDelete := DecodeInternalKey(item.InternalKey)

		// Deduplication check

		if lastUserKey == nil || !bytes.Equal(currentUserKey, lastUserKey) {
			if !isDelete {
				builder.Add(item.InternalKey, item.Value)
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
		return err
	}

	newReader, err := NewSSTableReader(newFilename)
	if err != nil {
		return err
	}

	// replacing old files with the new compacted file
	m.Mu.Lock()
	m.ActiveSSreaders = []*SSTableReader{newReader}
	m.Mu.Unlock()

	for _, it := range iterators {
		it.Close()
	}
	for _, r := range readersToCompact {
		r.Close()
		os.Remove(r.File.Name())
	}

	return nil
}
