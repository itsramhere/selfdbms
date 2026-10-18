//go:build !windows

package harness

import "time"

// PrecisionTime represents a monotonic timestamp.
type PrecisionTime int64

// NowPrecision returns the current timestamp in nanoseconds.
func NowPrecision() PrecisionTime {
	return PrecisionTime(time.Now().UnixNano())
}

// SincePrecisionNanos calculates elapsed nanoseconds from start.
func SincePrecisionNanos(start PrecisionTime) int64 {
	elapsed := time.Now().UnixNano() - int64(start)
	if elapsed <= 0 {
		return 0
	}
	return elapsed
}
