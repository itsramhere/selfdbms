package harness

import (
	"errors"
	"math"
	"math/big"
)

var (
	// ErrInvalidFrequency is returned when timer frequency is non-positive.
	ErrInvalidFrequency = errors.New("invalid timer frequency: must be strictly positive")
	// ErrNegativeTicks is returned when elapsed ticks is negative.
	ErrNegativeTicks = errors.New("invalid timer ticks: cannot be negative")
	// ErrTimerOverflow is returned when tick-to-nanosecond conversion exceeds int64 range.
	ErrTimerOverflow = errors.New("timer tick conversion exceeds int64 nanosecond range")
)

const maxDirectTicks = math.MaxInt64 / 1_000_000_000

// TicksToNanosChecked converts timer tick count to nanoseconds at the given frequency in Hz.
// It returns an explicit error on non-positive frequency, negative ticks, or integer overflow,
// guaranteeing that no corrupted or clamped duration is ever silently produced.
func TicksToNanosChecked(ticks, freq int64) (int64, error) {
	if freq <= 0 {
		return 0, ErrInvalidFrequency
	}
	if ticks < 0 {
		return 0, ErrNegativeTicks
	}
	if ticks == 0 {
		return 0, nil
	}

	if ticks <= maxDirectTicks {
		return (ticks * 1_000_000_000) / freq, nil
	}

	sec := ticks / freq
	if sec > maxDirectTicks {
		return 0, ErrTimerOverflow
	}
	secNanos := sec * 1_000_000_000

	rem := ticks % freq
	var remNanos int64
	if rem <= maxDirectTicks {
		remNanos = (rem * 1_000_000_000) / freq
	} else {
		var bRem, b1e9, bFreq, bProd, bRes big.Int
		bRem.SetInt64(rem)
		b1e9.SetInt64(1_000_000_000)
		bFreq.SetInt64(freq)
		bProd.Mul(&bRem, &b1e9)
		bRes.Div(&bProd, &bFreq)
		if !bRes.IsInt64() {
			return 0, ErrTimerOverflow
		}
		remNanos = bRes.Int64()
	}

	if secNanos > math.MaxInt64-remNanos {
		return 0, ErrTimerOverflow
	}
	return secNanos + remNanos, nil
}

// TicksToNanos converts timer tick count to nanoseconds at the given frequency in Hz.
// It panics if the arguments are invalid or overflow occurs, eliminating silent fallbacks.
func TicksToNanos(ticks, freq int64) int64 {
	nanos, err := TicksToNanosChecked(ticks, freq)
	if err != nil {
		panic(err)
	}
	return nanos
}
