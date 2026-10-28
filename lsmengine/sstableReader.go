package lsmengine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type SSTableReader struct {
	File          *os.File
	Index         []IndexEntry
	DataEndOffset int64

	// Approach A-min lifetime bookkeeping (Decision D-I7-02).
	// leaseCount counts readers that may touch this table outside Manager.Mu.
	// retired is set exactly once, under Manager.Mu, at the instant the table is
	// removed from Manager.ActiveSSreaders. freeOnce guards close+unlink.
	// Approaches A and B never increment leaseCount, so these fields are inert there.
	leaseCount  atomic.Int32
	retired     atomic.Bool
	freeOnce    sync.Once
	reclaimDone atomic.Bool
	freeErr     error
}

// lease registers an in-flight reader. PRECONDITION: the caller holds
// Manager.Mu.RLock() and the table is still present in ActiveSSreaders.
func (sst *SSTableReader) lease() { sst.leaseCount.Add(1) }

// releaseLease drops an in-flight reader and reports whether this release made a
// retired table eligible for reclamation.
func (sst *SSTableReader) releaseLease() bool {
	return sst.leaseCount.Add(-1) == 0 && sst.retired.Load()
}

// reclaim closes the descriptor and unlinks the path exactly once.
// Close precedes unlink (required by Windows NTFS sharing semantics).
// If Close fails, unlinking is abandoned so that a live path is never deleted
// from under another handle.
func (sst *SSTableReader) reclaim() error {
	sst.freeOnce.Do(func() {
		defer sst.reclaimDone.Store(true)
		path := ""
		if sst.File != nil {
			path = sst.File.Name()
			if err := sst.File.Close(); err != nil {
				sst.freeErr = err
				return
			}
		}
		if path == "" {
			return
		}
		var unlinkErr error
		for attempt := 0; attempt < 3; attempt++ {
			unlinkErr = os.Remove(path)
			if unlinkErr == nil || errors.Is(unlinkErr, os.ErrNotExist) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		sst.freeErr = unlinkErr
	})
	return sst.freeErr
}

func NewSSTableReader(filename string) (*SSTableReader, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	fileStat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	// read footer i.e 8 last 8 bytes of the sst
	footer := make([]byte, 8)
	_, err = file.ReadAt(footer, fileStat.Size()-8)
	if err != nil {
		return nil, err
	}

	indexStartOffset := binary.LittleEndian.Uint32(footer[0:4])
	magicNumber := binary.LittleEndian.Uint32(footer[4:8])

	if magicNumber != 0x4C534D31 {
		return nil, errors.New("corrupted SSTable: magic number mismatch")
	}
	indexLength := (fileStat.Size() - 8) - int64(indexStartOffset)

	// read the entire index into RAM
	indexData := make([]byte, indexLength)
	if _, err := file.ReadAt(indexData, int64(indexStartOffset)); err != nil {
		return nil, err
	}

	var index []IndexEntry
	buf := bytes.NewReader(indexData)

	// loop through the in-memory buffer until it is empty
	for buf.Len() > 0 {
		var keyLen uint16
		if err := binary.Read(buf, binary.LittleEndian, &keyLen); err != nil {
			return nil, err
		}

		key := make([]byte, keyLen)
		if _, err := io.ReadFull(buf, key); err != nil {
			return nil, err
		}

		var offset uint32
		if err := binary.Read(buf, binary.LittleEndian, &offset); err != nil {
			return nil, err
		}

		index = append(index, IndexEntry{
			Key:    key,
			Offset: offset,
		})
	}

	return &SSTableReader{
		File:          file,
		Index:         index,
		DataEndOffset: int64(indexStartOffset),
	}, nil

}

func (sst *SSTableReader) Get(userKey []byte, readTs uint64) ([]byte, error) {
	if len(sst.Index) == 0 {
		return nil, ErrNotFound
	}

	// Construct a seek key using the internal key encoding.
	// This key sorts at the correct position in internal key order,
	// matching how data is actually sorted in the SSTable.
	seekKey := EncodeInternalKey(userKey, readTs, true)

	// Binary search using internal key comparison (raw bytes)
	low := 0
	high := len(sst.Index) - 1
	var targetOffset uint32
	foundBlock := false

	for low <= high {
		mid := low + (high-low)/2
		cmp := CompareInternalKeys(sst.Index[mid].Key, seekKey)

		if cmp == 0 {
			targetOffset = sst.Index[mid].Offset
			foundBlock = true
			break
		} else if cmp < 0 {
			targetOffset = sst.Index[mid].Offset
			foundBlock = true
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	if !foundBlock {
		// The seek key is before all index entries. The target key
		// could still be in the first block (the seek key sorts before
		// the data key when readTs > the key's seqNo due to inverted
		// sequence numbers). Fall back to scanning from the start.
		targetOffset = 0
	}

	blockLength := sst.DataEndOffset - int64(targetOffset)
	sr := io.NewSectionReader(sst.File, int64(targetOffset), blockLength)

	// Sequential Block Scan
	for {
		var keyLen uint16
		if err := binary.Read(sr, binary.LittleEndian, &keyLen); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		diskKey := make([]byte, keyLen)
		if _, err := io.ReadFull(sr, diskKey); err != nil {
			return nil, err
		}

		var valLen uint32
		if err := binary.Read(sr, binary.LittleEndian, &valLen); err != nil {
			return nil, err
		}

		diskVal := make([]byte, valLen)
		if _, err := io.ReadFull(sr, diskVal); err != nil {
			return nil, err
		}

		// Decode the disk key to check MVCC visibility
		diskUserKey, diskSeqNo, isDelete := DecodeInternalKey(diskKey)

		if bytes.Equal(diskUserKey, userKey) {
			if diskSeqNo <= readTs {
				if isDelete {
					return nil, ErrKeyDeleted
				}
				return diskVal, nil
			}
		}
	}

	return nil, ErrNotFound
}

func (sst *SSTableReader) LatestValue(userKey []byte) (uint64, error) {
	if len(sst.Index) == 0 {
		return 0, ErrNotFound
	}

	seekKey := EncodeInternalKey(userKey, math.MaxUint64, true)

	low := 0
	high := len(sst.Index) - 1
	var targetOffset uint32
	foundBlock := false

	for low <= high {
		mid := low + (high-low)/2
		cmp := CompareInternalKeys(sst.Index[mid].Key, seekKey)

		if cmp == 0 {
			targetOffset = sst.Index[mid].Offset
			foundBlock = true
			break
		} else if cmp < 0 {
			targetOffset = sst.Index[mid].Offset
			foundBlock = true
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	if !foundBlock {
		targetOffset = 0
	}

	blockLength := sst.DataEndOffset - int64(targetOffset)
	sr := io.NewSectionReader(sst.File, int64(targetOffset), blockLength)

	for {
		var keyLen uint16
		if err := binary.Read(sr, binary.LittleEndian, &keyLen); err != nil {
			if err == io.EOF {
				break
			}
			return 0, err
		}

		diskKey := make([]byte, keyLen)
		if _, err := io.ReadFull(sr, diskKey); err != nil {
			return 0, err
		}

		var valLen uint32
		if err := binary.Read(sr, binary.LittleEndian, &valLen); err != nil {
			return 0, err
		}

		diskVal := make([]byte, valLen)
		if _, err := io.ReadFull(sr, diskVal); err != nil {
			return 0, err
		}

		diskUserKey, diskSeqNo, _ := DecodeInternalKey(diskKey)

		if bytes.Equal(diskUserKey, userKey) {
			return diskSeqNo, nil
		}

		if bytes.Compare(diskUserKey, userKey) > 0 {
			break
		}
	}

	return 0, ErrNotFound
}

func (sst *SSTableReader) Close() error {
	if sst.File != nil {
		return sst.File.Close()
	}
	return nil
}
