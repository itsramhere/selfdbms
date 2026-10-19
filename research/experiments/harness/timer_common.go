package harness

import (
	"math"
	"math/big"
)

// TicksToNanos converts timer tick count to nanoseconds at the given frequency in Hz.
// It explicitly guards against integer overflow for large tick intervals.
func TicksToNanos(ticks, freq int64) int64 {
	if ticks <= 0 || freq <= 0 {
		return 0
	}
	const maxDirectTicks = math.MaxInt64 / 1_000_000_000
	if ticks <= maxDirectTicks {
		return (ticks * 1_000_000_000) / freq
	}
	sec := ticks / freq
	rem := ticks % freq
	if rem <= maxDirectTicks {
		return sec*1_000_000_000 + (rem*1_000_000_000)/freq
	}
	var bTicks, b1e9, bFreq, bProd, bRes big.Int
	bTicks.SetInt64(ticks)
	b1e9.SetInt64(1_000_000_000)
	bFreq.SetInt64(freq)
	bProd.Mul(&bTicks, &b1e9)
	bRes.Div(&bProd, &bFreq)
	if bRes.IsInt64() {
		return bRes.Int64()
	}
	return math.MaxInt64
}
