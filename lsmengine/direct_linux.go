//go:build linux

package lsmengine

import (
	"os"
	"syscall"
)

func openDirectFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECT|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func readDirectAt(raw syscall.RawConn, buf []byte, offset int64) (n int, err error) {
	controlErr := raw.Control(func(fd uintptr) {
		for {
			n, err = syscall.Pread(int(fd), buf, offset)
			if err != syscall.EINTR {
				break
			}
		}
	})
	if controlErr != nil {
		return 0, controlErr
	}
	return
}
