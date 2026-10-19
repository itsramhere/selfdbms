//go:build windows

package harness

import (
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL  = syscall.NewLazyDLL("kernel32.dll")
	procQPC      = kernel32DLL.NewProc("QueryPerformanceCounter")
	procQPF      = kernel32DLL.NewProc("QueryPerformanceFrequency")
	qpcFrequency int64
)

func init() {
	var freq int64
	procQPF.Call(uintptr(unsafe.Pointer(&freq)))
	if freq <= 0 {
		freq = 10_000_000 // fallback standard 10 MHz
	}
	atomic.StoreInt64(&qpcFrequency, freq)
}

// PrecisionTime represents a high-resolution monotonic timestamp backed by Windows QPC.
type PrecisionTime struct {
	count int64
}

// NowPrecision returns the current high-resolution monotonic timestamp.
func NowPrecision() PrecisionTime {
	var count int64
	procQPC.Call(uintptr(unsafe.Pointer(&count)))
	return PrecisionTime{count: count}
}

// SincePrecisionNanos calculates elapsed monotonic nanoseconds from start.
func SincePrecisionNanos(start PrecisionTime) int64 {
	var count int64
	procQPC.Call(uintptr(unsafe.Pointer(&count)))
	elapsedTicks := count - start.count
	if elapsedTicks <= 0 {
		return 0
	}
	freq := atomic.LoadInt64(&qpcFrequency)
	return (elapsedTicks * 1_000_000_000) / freq
}

// QPCFrequencyHz returns the active QueryPerformanceFrequency in Hz.
func QPCFrequencyHz() int64 {
	return atomic.LoadInt64(&qpcFrequency)
}
