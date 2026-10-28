//go:build windows

package main

import (
	"syscall"
	"time"
	"unsafe"
)

// This host's Go time.Now() has millisecond-scale granularity, which is too
// coarse for per-operation latency samples. QueryPerformanceCounter is a
// CGO-free, monotonic, system-wide high-resolution clock (typically 100 ns
// ticks), so the measurement window uses it directly on Windows.

var (
	kernel32DLL  = syscall.NewLazyDLL("kernel32.dll")
	qpcProc      = kernel32DLL.NewProc("QueryPerformanceCounter")
	qpfProc      = kernel32DLL.NewProc("QueryPerformanceFrequency")
	qpcFrequency = queryQPCFrequency()
	qpcAvailable = qpcFrequency > 0
)

func queryQPCFrequency() int64 {
	if err := qpfProc.Find(); err != nil {
		return 0
	}
	var freq int64
	syscall.SyscallN(qpfProc.Addr(), uintptr(unsafe.Pointer(&freq)))
	if freq <= 0 {
		return 0
	}
	return freq
}

func clockStart() int64 {
	if !qpcAvailable {
		return time.Now().UnixNano()
	}
	var counter int64
	syscall.SyscallN(qpcProc.Addr(), uintptr(unsafe.Pointer(&counter)))
	return counter
}

func clockElapsedNanos(start int64) int64 {
	if !qpcAvailable {
		return time.Now().UnixNano() - start
	}
	var counter int64
	syscall.SyscallN(qpcProc.Addr(), uintptr(unsafe.Pointer(&counter)))
	return ticksToNanos(counter - start)
}

// ticksToNanos avoids multiplying the full counter by 1e9 (which would
// overflow); the split keeps every intermediate product in range.
func ticksToNanos(delta int64) int64 {
	whole := delta / qpcFrequency
	rem := delta % qpcFrequency
	return whole*1_000_000_000 + rem*1_000_000_000/qpcFrequency
}

func clockSource() string {
	if qpcAvailable {
		return "windows.QueryPerformanceCounter"
	}
	return "time.Now"
}

func clockResolutionNanos() int64 {
	if qpcAvailable {
		return 1_000_000_000 / qpcFrequency
	}
	return 1
}
