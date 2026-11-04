package lsmengine

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"
	"unsafe"
)

const directAlignment = 4096

type directBuffer struct{ bytes []byte }

var directBuffers = sync.Pool{New: func() any { return &directBuffer{} }}

// directRange is independent of the filesystem. The last aligned read may
// extend beyond EOF; callers accept a short read only if all wanted bytes exist.
func directRange(start int64, size int) (offset int64, length, skip int, err error) {
	if start < 0 || size <= 0 || start > math.MaxInt64-int64(size)-(directAlignment-1) {
		return 0, 0, 0, errors.New("invalid direct I/O range")
	}
	offset = start &^ int64(directAlignment-1)
	end := (start + int64(size) + directAlignment - 1) &^ int64(directAlignment-1)
	if end-offset > int64(int(^uint(0)>>1))-(directAlignment-1) {
		return 0, 0, 0, errors.New("direct I/O range too large")
	}
	return offset, int(end - offset), int(start - offset), nil
}

func (b *directBuffer) aligned(size int) []byte {
	if cap(b.bytes) < size+directAlignment-1 {
		b.bytes = make([]byte, size+directAlignment-1)
	}
	raw := b.bytes[:cap(b.bytes)]
	skip := int((-uintptr(unsafe.Pointer(&raw[0]))) & (directAlignment - 1))
	return raw[skip : skip+size]
}

func (sst *SSTableReader) readDirectBlock(dst []byte, start int64) error {
	if sst.directFile == nil {
		return errors.New("direct I/O handle is not open; use NewSSTableReaderWithReadPath")
	}
	offset, length, skip, err := directRange(start, len(dst))
	if err != nil {
		return err
	}
	b := directBuffers.Get().(*directBuffer)
	defer directBuffers.Put(b)
	buf := b.aligned(length)
	n, err := readDirectAt(sst.directRaw, buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("direct I/O read %q: %w", sst.File.Name(), err)
	}
	if n < skip+len(dst) {
		return io.ErrUnexpectedEOF
	}
	copy(dst, buf[skip:skip+len(dst)])
	return nil
}

// NewSSTableReaderWithReadPath leaves the default constructor and its metadata
// reads untouched. Sync uses a writable cached handle because Windows requires
// write access for FlushFileBuffers. SSTables are immutable after publication.
func NewSSTableReaderWithReadPath(filename string, path ReadPathMode) (*SSTableReader, error) {
	return newSSTableReaderWithOpener(filename, path, openDirectFile)
}

func newSSTableReaderWithOpener(filename string, path ReadPathMode, opener func(string) (*os.File, error)) (*SSTableReader, error) {
	if path != ReadPathBlock && path != ReadPathLegacyScan && path != ReadPathDirect {
		return nil, fmt.Errorf("unknown read path mode: %v", path)
	}
	sst, err := NewSSTableReader(filename)
	if err != nil {
		return nil, err
	}
	if path == ReadPathDirect {
		syncFile, e := os.OpenFile(filename, os.O_RDWR, 0)
		if e == nil {
			e = errors.Join(syncFile.Sync(), syncFile.Close())
		}
		if e != nil {
			sst.Close()
			return nil, fmt.Errorf("direct I/O sync %q: %w", filename, e)
		}
		sst.directFile, err = opener(filename)
		if err == nil {
			sst.directRaw, err = sst.directFile.SyscallConn()
		}
		if err == nil {
			// Fail at open even on filesystems that accept the flag but reject
			// aligned reads. This also probes short, non-sector-sized files.
			var probe [1]byte
			err = sst.readDirectBlock(probe[:], 0)
		}
		if err != nil {
			sst.Close()
			return nil, fmt.Errorf("direct I/O open %q (no buffered fallback): %w", filename, err)
		}
	}
	sst.ReadPath = path
	return sst, nil
}
