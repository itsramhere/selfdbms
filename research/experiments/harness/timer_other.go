//go:build !windows

package harness

import "time"

// EnsurePrecisionTimer verifies that the high-resolution timer is operational on Linux/POSIX.
func EnsurePrecisionTimer() error {
	return nil
}

// PrecisionTime represents a monotonic timestamp backed by Go's monotonic runtime clock.
type PrecisionTime struct {
	t time.Time
}

// NowPrecision returns the current monotonic timestamp.
func NowPrecision() PrecisionTime {
	return PrecisionTime{t: time.Now()}
}

// SincePrecisionNanos calculates elapsed monotonic nanoseconds from start.
func SincePrecisionNanos(start PrecisionTime) int64 {
	d := time.Since(start.t)
	if d <= 0 {
		return 0
	}
	return d.Nanoseconds()
}
