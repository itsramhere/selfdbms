//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modKernel32          = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentProc   = modKernel32.NewProc("GetCurrentProcess")
	procGetAffinityMask  = modKernel32.NewProc("GetProcessAffinityMask")
	procSetAffinityMask  = modKernel32.NewProc("SetProcessAffinityMask")
)

// processAffinityMask returns the current process affinity mask.
// Read-only: I7 never changes affinity from inside the trial process.
func processAffinityMask() (uint64, error) {
	h, _, _ := procGetCurrentProc.Call()
	var procMask, sysMask uintptr
	ok, _, err := procGetAffinityMask.Call(h, uintptr(unsafe.Pointer(&procMask)), uintptr(unsafe.Pointer(&sysMask)))
	if ok == 0 {
		if err != nil && err.Error() != "The operation completed successfully." {
			return 0, err
		}
		return 0, fmt.Errorf("GetProcessAffinityMask failed")
	}
	return uint64(procMask), nil
}

// setProcessAffinityMask exists so the launcher can be validated from Go if
// needed; the campaign itself uses the PowerShell launcher.
func setProcessAffinityMask(mask uint64) error {
	if mask == 0 {
		return fmt.Errorf("zero affinity mask")
	}
	h, _, _ := procGetCurrentProc.Call()
	ok, _, err := procSetAffinityMask.Call(h, uintptr(mask))
	if ok == 0 {
		if err != nil && err.Error() != "The operation completed successfully." {
			return err
		}
		return fmt.Errorf("SetProcessAffinityMask failed")
	}
	return nil
}

func maskCores(mask uint64) []int {
	cores := make([]int, 0, 16)
	for i := 0; i < 64; i++ {
		if mask&(1<<uint(i)) != 0 {
			cores = append(cores, i)
		}
	}
	return cores
}
