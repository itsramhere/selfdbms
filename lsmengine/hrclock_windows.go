//go:build windows

package lsmengine

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	hrKernel32  = syscall.NewLazyDLL("kernel32.dll")
	hrQPC       = hrKernel32.NewProc("QueryPerformanceCounter")
	hrQPF       = hrKernel32.NewProc("QueryPerformanceFrequency")
	hrFrequency = func() int64 {
		var frequency int64
		result, _, err := hrQPF.Call(uintptr(unsafe.Pointer(&frequency)))
		if result == 0 || frequency <= 0 {
			panic(fmt.Sprintf("QueryPerformanceFrequency failed: %v", err))
		}
		return frequency
	}()
)

// hrNowNanos returns high-resolution monotonic nanoseconds from Windows QPC.
func hrNowNanos() int64 {
	var ticks int64
	result, _, err := hrQPC.Call(uintptr(unsafe.Pointer(&ticks)))
	if result == 0 {
		panic(fmt.Sprintf("QueryPerformanceCounter failed: %v", err))
	}
	// Split whole seconds from the remainder instead of multiplying the full
	// tick count by 1e9, which would overflow after a relatively short uptime.
	seconds, remainder := ticks/hrFrequency, ticks%hrFrequency
	return seconds*1e9 + remainder*1e9/hrFrequency
}
