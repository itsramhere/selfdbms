//go:build !windows

package main

// Non-Windows precision timestamps use Go's monotonic runtime clock, not QPC.
func platformQPCFrequency() any { return nil }
