//go:build !windows

package lsmengine

import "time"

var hrClockStart = time.Now()

// hrNowNanos uses Go's monotonic clock, relative to package initialization.
func hrNowNanos() int64 {
	return time.Since(hrClockStart).Nanoseconds()
}
