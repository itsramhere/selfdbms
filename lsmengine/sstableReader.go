package lsmengine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

type SSTableReader struct {
	File          *os.File
	Index         []IndexEntry
	DataEndOffset int64
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

func (sst *SSTableReader) Close() error {
	if sst.File != nil {
		return sst.File.Close()
	}
	return nil
}
