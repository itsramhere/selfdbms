package main

import (
	"syscall"
	"unsafe"
)

func freeDisk(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	ok, _, err := proc.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&available)), 0, 0)
	if ok == 0 {
		return 0, err
	}
	return available, nil
}
