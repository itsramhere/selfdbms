//go:build windows

package harness

import (
	"fmt"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL  = syscall.NewLazyDLL("kernel32.dll")
	procQPC      = kernel32DLL.NewProc("QueryPerformanceCounter")
	procQPF      = kernel32DLL.NewProc("QueryPerformanceFrequency")
	qpcFrequency int64
	qpcInitOnce  sync.Once
	qpcInitErr   error
)

func initQPC() error {
	qpcInitOnce.Do(func() {
		var freq int64
		r1, _, err := procQPF.Call(uintptr(unsafe.Pointer(&freq)))
		if r1 == 0 || freq <= 0 {
			qpcInitErr = fmt.Errorf("QueryPerformanceFrequency failed (return code %d): %w", r1, err)
			return
		}
		var testCount int64
		r2, _, err2 := procQPC.Call(uintptr(unsafe.Pointer(&testCount)))
		if r2 == 0 || testCount <= 0 {
			qpcInitErr = fmt.Errorf("QueryPerformanceCounter verification failed (return code %d): %w", r2, err2)
			return
		}
		atomic.StoreInt64(&qpcFrequency, freq)
	})
	return qpcInitErr
}

func init() {
	_ = initQPC()
}

// EnsurePrecisionTimer verifies that the high-resolution timer is operational.
// If QPF or QPC fails, it returns an explicit error and does not assume any fallback.
func EnsurePrecisionTimer() error {
	return initQPC()
}

// PrecisionTime represents a high-resolution monotonic timestamp backed by Windows QPC.
type PrecisionTime struct {
	count int64
}

// NowPrecision returns the current high-resolution monotonic timestamp.
func NowPrecision() PrecisionTime {
	var count int64
	r, _, err := procQPC.Call(uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		panic(fmt.Sprintf("QueryPerformanceCounter syscall failed: %v", err))
	}
	return PrecisionTime{count: count}
}

// SincePrecisionNanos calculates elapsed monotonic nanoseconds from start.
func SincePrecisionNanos(start PrecisionTime) int64 {
	var count int64
	r, _, err := procQPC.Call(uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		panic(fmt.Sprintf("QueryPerformanceCounter syscall failed: %v", err))
	}
	elapsedTicks := count - start.count
	if elapsedTicks <= 0 {
		return 0
	}
	freq := atomic.LoadInt64(&qpcFrequency)
	if freq <= 0 {
		panic("QueryPerformanceFrequency uninitialized or non-positive")
	}
	nanos, err := TicksToNanosChecked(elapsedTicks, freq)
	if err != nil {
		panic(fmt.Sprintf("SincePrecisionNanos failed: %v", err))
	}
	return nanos
}

// QPCFrequencyHz returns the active QueryPerformanceFrequency in Hz.
func QPCFrequencyHz() int64 {
	_ = initQPC()
	return atomic.LoadInt64(&qpcFrequency)
}
