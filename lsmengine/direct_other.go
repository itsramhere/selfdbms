//go:build !windows && !linux

package lsmengine

import (
	"errors"
	"os"
	"syscall"
)

func openDirectFile(string) (*os.File, error) {
	return nil, errors.New("direct I/O is supported only on Windows and Linux")
}

func readDirectAt(raw syscall.RawConn, buf []byte, offset int64) (int, error) {
	return 0, errors.New("direct I/O is unsupported")
}
