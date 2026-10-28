//go:build !windows

package main

import "time"

// Portable fallback: the runner is a Windows-native tool, but this keeps
// `go test ./...` and `go build` working on other hosts for development.

func clockStart() int64 {
	return time.Now().UnixNano()
}

func clockElapsedNanos(start int64) int64 {
	return time.Now().UnixNano() - start
}

func clockSource() string {
	return "time.Now"
}

func clockResolutionNanos() int64 {
	return 1
}
