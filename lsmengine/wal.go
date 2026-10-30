package lsmengine

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

type Wal struct {
	File        *os.File
	mu          sync.Mutex  // Protects concurrent writes to the file descriptor
	DisableSync atomic.Bool // False preserves fsync after every record.
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

// Append writes a KV pair in one write and syncs it unless DisableSync is set.
func (w *Wal) Append(internalKey []byte, value []byte) error {
	// Lock the file so multiple transactions committing at the same millisecond don't interleave their bytes
	w.mu.Lock()
	defer w.mu.Unlock()

	// metadata calculation (stateless)
	keyLen := uint16(len(internalKey))
	valLen := uint32(len(value))
	record := make([]byte, 10+len(internalKey)+len(value))
	binary.LittleEndian.PutUint16(record[4:6], keyLen)
	binary.LittleEndian.PutUint32(record[6:10], valLen)
	copy(record[10:], internalKey)
	copy(record[10+len(internalKey):], value)

	// checksum calculation (covers key length, key payload, value length, and value payload)
	hash := crc32.NewIEEE()
	hash.Write(record[4:6])
	hash.Write(record[10 : 10+len(internalKey)])
	hash.Write(record[6:10])
	hash.Write(record[10+len(internalKey):])
	binary.LittleEndian.PutUint32(record[0:4], hash.Sum32())

	// Preserve the header and checksum layout while issuing one record write.
	n, err := w.File.Write(record)
	if err != nil {
		return err
	}
	if n != len(record) {
		return io.ErrShortWrite
	}

	// force the OS to flush the page cache to physical disk
	if !w.DisableSync.Load() {
		return w.File.Sync()
	}

	return nil
}
