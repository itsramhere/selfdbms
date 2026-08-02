package lsmengine

import (
	"bytes"
	"encoding/binary"
	"os"
)

type IndexEntry struct {
	Key    []byte
	Offset uint32
}

type SSTableBuilder struct {
	File          *os.File
	CurrentOffset uint32 // Tracks total bytes written to the file so far

	BlockBuffer *bytes.Buffer // Accumulates KV pairs in memory until it hits 4KB
	Index       []IndexEntry  // The Table of Contents we build as we create blocks
}

// adds sst contents from memory to disk when size exceeds 4kB
func (sst *SSTableBuilder) Add(internalKey []byte, value []byte) error {
	if sst.BlockBuffer.Len() == 0 {
		keyCopy := make([]byte, len(internalKey))
		copy(keyCopy, internalKey)

		sst.Index = append(sst.Index, IndexEntry{
			Key:    keyCopy,
			Offset: sst.CurrentOffset,
		})
	}

	keyLen := uint16(len(internalKey))
	valLen := uint32(len(value))

	// writing key length + key, then value length + value
	if err := binary.Write(sst.BlockBuffer, binary.LittleEndian, keyLen); err != nil {
		return err
	}
	sst.BlockBuffer.Write(internalKey)

	if err := binary.Write(sst.BlockBuffer, binary.LittleEndian, valLen); err != nil {
		return err
	}
	sst.BlockBuffer.Write(value)

	// check if > 4kB
	if sst.BlockBuffer.Len() >= 4096 {
		n, err := sst.File.Write(sst.BlockBuffer.Bytes())
		if err != nil {
			return err
		}
		sst.CurrentOffset += uint32(n) // add no. of bytes written in this buffer to the offset
		sst.BlockBuffer.Reset()
	}

	return nil
}

// closes the file
func (sst *SSTableBuilder) Close() error {
	// flush the Residual Buffer
	if sst.BlockBuffer.Len() > 0 {
		n, err := sst.File.Write(sst.BlockBuffer.Bytes())
		if err != nil {
			return err
		}
		sst.CurrentOffset += uint32(n)
	}

	indexStartOffset := sst.CurrentOffset

	// serialize and write the Index
	for _, idx := range sst.Index {
		keyLen := uint16(len(idx.Key))
		if err := binary.Write(sst.File, binary.LittleEndian, keyLen); err != nil {
			return err
		}

		if _, err := sst.File.Write(idx.Key); err != nil {
			return err
		}

		if err := binary.Write(sst.File, binary.LittleEndian, idx.Offset); err != nil {
			return err
		}
	}

	// writing the gooter
	if err := binary.Write(sst.File, binary.LittleEndian, indexStartOffset); err != nil {
		return err
	}

	magicNumber := uint32(0x4C534D31)
	if err := binary.Write(sst.File, binary.LittleEndian, magicNumber); err != nil {
		return err
	}

	if err := sst.File.Sync(); err != nil {
		return err
	}

	return sst.File.Close()
}
