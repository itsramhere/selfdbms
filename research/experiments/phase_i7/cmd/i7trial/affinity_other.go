//go:build !windows

package main

import "fmt"

// The I7 campaign is Windows-native; these stubs keep `go build ./...` working
// on other platforms for CI and for contributors.

func processAffinityMask() (uint64, error) { return 0, nil }

func setProcessAffinityMask(mask uint64) error {
	return fmt.Errorf("process affinity control is only implemented on windows")
}

func maskCores(mask uint64) []int { return nil }
