package lsmengine

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"sync"
)

type Wal struct {
	File *os.File
	mu   sync.Mutex // Protects concurrent writes to the file descriptor
}

// opens the WAL file once with append-only permissions.
func NewWal(filename string) (*Wal, error) {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}

	return &Wal{
		File: file,
	}, nil
}

// Append writes a KV pair to disk safely and returns an error if it fails.
func (w *Wal) Append(internalKey []byte, value []byte) error {
	// Lock the file so multiple transactions committing at the same millisecond don't interleave their bytes
	w.mu.Lock()
	defer w.mu.Unlock()

	// metadata calculation (stateless)
	keyLen := uint16(len(internalKey))
	valLen := uint32(len(value))

	// checksum calculation (covers key length, key payload, value length, and value payload)
	hash := crc32.NewIEEE()
	binary.Write(hash, binary.LittleEndian, keyLen)
	hash.Write(internalKey)
	binary.Write(hash, binary.LittleEndian, valLen)
	hash.Write(value)
	checksum := hash.Sum32()

	// writing the header
	if err := binary.Write(w.File, binary.LittleEndian, checksum); err != nil {
		return err
	}
	if err := binary.Write(w.File, binary.LittleEndian, keyLen); err != nil {
		return err
	}
	if err := binary.Write(w.File, binary.LittleEndian, valLen); err != nil {
		return err
	}

	// write the payload
	if _, err := w.File.Write(internalKey); err != nil {
		return err
	}
	if _, err := w.File.Write(value); err != nil {
		return err
	}

	// force the OS to flush the page cache to physical disk
	if err := w.File.Sync(); err != nil {
		return err
	}

	return nil
}
