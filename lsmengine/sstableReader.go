package lsmengine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ReadPathMode selects the SSTable point-read implementation for every sync mode.
type ReadPathMode int

const (
	ReadPathBlock      ReadPathMode = iota // Range check, per-block ReadAt, and early stop (default).
	ReadPathLegacyScan                     // Historical unbuffered scan, without injected delay.
)

// SSTableReader uses block reads for point lookups by default. ReadPath and
// ReadIODelay must be configured before publishing the reader to other goroutines.
type SSTableReader struct {
	File          *os.File
	Index         []IndexEntry
	DataEndOffset int64
	ReadPath      ReadPathMode
	ReadIODelay   time.Duration // Experiment-only delay before each point-read data-block ReadAt.
	minUserKey    []byte
	maxUserKey    []byte
	blockReadHook func() // Test hook, configured before concurrent use; excludes open-time metadata I/O.

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
	opened := false
	defer func() {
		if !opened {
			file.Close()
		}
	}()

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
	if indexLength < 0 {
		return nil, errors.New("corrupted SSTable: invalid index offset")
	}

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

	sst := &SSTableReader{
		File:          file,
		Index:         index,
		DataEndOffset: int64(indexStartOffset),
	}
	if len(index) > 0 {
		if len(index[0].Key) < 8 {
			return nil, errors.New("corrupted SSTable: invalid internal key")
		}
		minKey, _, _ := DecodeInternalKey(index[0].Key)
		sst.minUserKey = bytes.Clone(minKey)
		// Load the last block once at open to cache the inclusive user-key range.
		// This metadata read has no experiment delay and is not a point-read I/O.
		lastBlock, err := sst.readBlock(len(index)-1, nil, false)
		if err != nil {
			return nil, err
		}
		for len(lastBlock) > 0 {
			var key []byte
			key, _, lastBlock, err = parseBlockRecord(lastBlock)
			if err != nil {
				return nil, err
			}
			maxKey, _, _ := DecodeInternalKey(key)
			sst.maxUserKey = bytes.Clone(maxKey)
		}
	}
	opened = true
	return sst, nil

}

func (sst *SSTableReader) Get(userKey []byte, readTs uint64) ([]byte, error) {
	if sst.ReadPath == ReadPathLegacyScan {
		return sst.getLegacyScan(userKey, readTs)
	}
	value, _, deleted, err := sst.lookupBlock(userKey, readTs)
	if err != nil {
		return nil, err
	}
	if deleted {
		return nil, ErrKeyDeleted
	}
	return value, nil
}

func (sst *SSTableReader) LatestValue(userKey []byte) (uint64, error) {
	if sst.ReadPath == ReadPathLegacyScan {
		return sst.latestValueLegacyScan(userKey)
	}
	_, seq, _, err := sst.lookupBlock(userKey, math.MaxUint64)
	return seq, err
}

// injectReadDelay uses a CPU-yielding spin for sub-millisecond waits because the
// OS cannot block precisely for microseconds. Longer waits block in the OS for
// all but the last 0.5 ms, then yield until the high-resolution deadline.
func injectReadDelay(d time.Duration) {
	if d <= 0 {
		return
	}
	deadline := hrNowNanos() + d.Nanoseconds()
	if d >= time.Millisecond {
		time.Sleep(d - 500*time.Microsecond)
	}
	for hrNowNanos() < deadline {
		runtime.Gosched()
	}
}

func (sst *SSTableReader) readBlock(i int, buf []byte, pointRead bool) ([]byte, error) {
	start := int64(sst.Index[i].Offset)
	end := sst.DataEndOffset
	if i+1 < len(sst.Index) {
		end = int64(sst.Index[i+1].Offset)
	}
	if start < 0 || end <= start || end > sst.DataEndOffset {
		return nil, errors.New("corrupted SSTable: invalid block offsets")
	}
	size := int(end - start)
	if cap(buf) < size {
		buf = make([]byte, size)
	} else {
		buf = buf[:size]
	}
	if pointRead {
		// Delay immediately before the ReadAt, within the caller's existing
		// lock, lease, or snapshot lifetime. Open-time metadata reads bypass it.
		injectReadDelay(sst.ReadIODelay)
		if sst.blockReadHook != nil {
			sst.blockReadHook()
		}
	}
	_, err := sst.File.ReadAt(buf, start)
	return buf, err
}

// parseBlockRecord returns slices into the call-local block buffer.
func parseBlockRecord(data []byte) (key, value, rest []byte, err error) {
	if len(data) < 2 {
		return nil, nil, nil, io.ErrUnexpectedEOF
	}
	keyLen := int(binary.LittleEndian.Uint16(data[:2]))
	data = data[2:]
	if keyLen < 8 {
		return nil, nil, nil, errors.New("corrupted SSTable: invalid internal key")
	}
	if len(data) < keyLen+4 {
		return nil, nil, nil, io.ErrUnexpectedEOF
	}
	key = data[:keyLen]
	valLen := uint64(binary.LittleEndian.Uint32(data[keyLen : keyLen+4]))
	data = data[keyLen+4:]
	if valLen > uint64(len(data)) {
		return nil, nil, nil, io.ErrUnexpectedEOF
	}
	return key, data[:int(valLen)], data[int(valLen):], nil
}

func (sst *SSTableReader) lookupBlock(userKey []byte, readTs uint64) ([]byte, uint64, bool, error) {
	if len(sst.Index) == 0 || bytes.Compare(userKey, sst.minUserKey) < 0 || bytes.Compare(userKey, sst.maxUserKey) > 0 {
		return nil, 0, false, ErrNotFound
	}
	seekKey := EncodeInternalKey(userKey, readTs, true)
	// As in the legacy path, start at the last first-key <= seekKey,
	// falling back to block zero if the seek key precedes the entire index.
	i := sort.Search(len(sst.Index), func(i int) bool {
		return CompareInternalKeys(sst.Index[i].Key, seekKey) > 0
	}) - 1
	if i < 0 {
		i = 0
	}
	var buf []byte
	for ; i < len(sst.Index); i++ {
		var err error
		buf, err = sst.readBlock(i, buf, true)
		if err != nil {
			return nil, 0, false, err
		}
		for data := buf; len(data) > 0; {
			var key, value []byte
			key, value, data, err = parseBlockRecord(data)
			if err != nil {
				return nil, 0, false, err
			}
			diskUserKey, seq, deleted := DecodeInternalKey(key)
			cmp := bytes.Compare(diskUserKey, userKey)
			if cmp > 0 {
				return nil, 0, false, ErrNotFound
			}
			if cmp == 0 && seq <= readTs {
				return value, seq, deleted, nil
			}
		}
		// Versions of a user key may span blocks. Continue until a visible
		// version is found, a greater user key is seen, or data ends.
	}
	return nil, 0, false, ErrNotFound
}

func (sst *SSTableReader) getLegacyScan(userKey []byte, readTs uint64) ([]byte, error) {
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

func (sst *SSTableReader) latestValueLegacyScan(userKey []byte) (uint64, error) {
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
