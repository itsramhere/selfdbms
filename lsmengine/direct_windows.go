//go:build windows

package lsmengine

import (
	"io"
	"os"
	"syscall"
)

func openDirectFile(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	const fileFlagNoBuffering = 0x20000000
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, fileFlagNoBuffering, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// One native operation is essential: os.File.ReadAt retries a short EOF read
// with a nonaligned offset/buffer, which NO_BUFFERING correctly rejects.
// A synchronous handle accepts the explicit OVERLAPPED offset; each operation
// completes before its buffer or stack OVERLAPPED can be reused.
func readDirectAt(raw syscall.RawConn, buf []byte, offset int64) (n int, err error) {
	var done uint32
	ov := syscall.Overlapped{Offset: uint32(offset), OffsetHigh: uint32(uint64(offset) >> 32)}
	controlErr := raw.Control(func(fd uintptr) { err = syscall.ReadFile(syscall.Handle(fd), buf, &done, &ov) })
	if controlErr != nil {
		return 0, controlErr
	}
	if err == syscall.ERROR_HANDLE_EOF {
		err = io.EOF
	}
	return int(done), err
}
