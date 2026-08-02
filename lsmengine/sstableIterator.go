package lsmengine

import (
	"encoding/binary"
	"io"
	"os"
)

type SSTableIterator struct {
	File      *os.File
	EndOffset int64 // the exact byte where the data blocks stop and the index begins
}

func NewSSTableIterator(filename string) (*SSTableIterator, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	// reading footer to find where the data blocks end
	footer := make([]byte, 8)
	if _, err := file.ReadAt(footer, stat.Size()-8); err != nil {
		return nil, err
	}

	indexStartOffset := binary.LittleEndian.Uint32(footer[0:4])

	// go to the very beginning of the file for sequential reading
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	return &SSTableIterator{
		File:      file,
		EndOffset: int64(indexStartOffset),
	}, nil
}

func (it *SSTableIterator) Next() ([]byte, []byte, error) {
	currentPos, err := it.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, nil, err
	}

	// if we have reached the index block, we are done reading data
	if currentPos >= it.EndOffset {
		return nil, nil, io.EOF
	}

	var keyLen uint16
	if err := binary.Read(it.File, binary.LittleEndian, &keyLen); err != nil {
		return nil, nil, err
	}

	key := make([]byte, keyLen)
	if _, err := io.ReadFull(it.File, key); err != nil {
		return nil, nil, err
	}

	var valLen uint32
	if err := binary.Read(it.File, binary.LittleEndian, &valLen); err != nil {
		return nil, nil, err
	}

	val := make([]byte, valLen)
	if _, err := io.ReadFull(it.File, val); err != nil {
		return nil, nil, err
	}

	return key, val, nil
}

func (it *SSTableIterator) Close() error {
	return it.File.Close()
}
