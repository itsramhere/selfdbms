//go:build windows

package main

import "owndbms/research/experiments/harness"

func platformQPCFrequency() any { return harness.QPCFrequencyHz() }
