package harness2

// Clock and scheduling helpers.
//
// All I7 timing uses the QPC clock exposed by the frozen harness
// (harness.NowPrecision / SincePrecisionNanos). time.Now is *not* usable here:
// on the target Windows host its monotonic clock advances in ~400-500 us steps,
// which would quantise sub-millisecond queueing delay to zero and make an
// open-loop measurement meaningless.

import (
	"math"
	"runtime"
	"time"

	"owndbms/research/experiments/harness"
)

// spinWindow is how long before a deadline the waiter stops sleeping and spins.
// time.Sleep overshoots a 1 ms request by ~0.5 ms on the target host, so the last
// stretch must be spun to keep arrival error in the tens of microseconds.
const spinWindow = 1500 * time.Microsecond

// Now returns a QPC timestamp.
func Now() harness.PrecisionTime { return harness.NowPrecision() }

// qpcElapsedNanos is the monotonic nanosecond clock injected into the engine's
// synchronization instrumentation. It reads the same QPC counter the trial
// window uses (base count 0, so the value is nanoseconds since the counter
// origin), keeping engine-side durations on one timeline with the harness.
func qpcElapsedNanos() int64 {
	return harness.SincePrecisionNanos(harness.PrecisionTime{})
}

// ElapsedNs returns nanoseconds since base.
func ElapsedNs(base harness.PrecisionTime) int64 {
	return harness.SincePrecisionNanos(base)
}

// SleepUntilElapsed blocks until the QPC clock has advanced to targetNs since
// base, spinning the final spinWindow.
func SleepUntilElapsed(base harness.PrecisionTime, targetNs int64) {
	for {
		rem := targetNs - harness.SincePrecisionNanos(base)
		if rem <= 0 {
			return
		}
		if rem > int64(spinWindow) {
			time.Sleep(time.Duration(rem) - spinWindow)
			continue
		}
		runtime.Gosched()
	}
}

// SleepWindow advances the coarse part of a measurement window. It returns the
// QPC-measured elapsed time, which is what the analysis uses; the wall-clock
// sleep is only a scheduling device.
func SleepWindow(base harness.PrecisionTime, duration time.Duration) int64 {
	target := duration.Nanoseconds()
	for {
		el := harness.SincePrecisionNanos(base)
		if el >= target {
			return el
		}
		rem := target - el
		if rem > int64(2*spinWindow) {
			time.Sleep(time.Duration(rem) - spinWindow)
			continue
		}
		runtime.Gosched()
	}
}

// Exponential inter-arrival schedule, generated deterministically from the
// triplet seed. The schedule is identical in every arm of a triplet, so all
// arms face the same offered load and the same keys.
type Arrival struct {
	IntendedNs int64 // offset from window start
	Key        string
	ExpectHit  bool
}

// BuildSchedule returns the arrivals whose intended time falls inside duration.
func BuildSchedule(tripletSeed int64, ratePerSec float64, keyspace int, hitRatio float64, duration time.Duration) []Arrival {
	if ratePerSec <= 0 || duration <= 0 {
		return nil
	}
	stream := NewKeyStream(tripletSeed, keyspace, hitRatio)
	var out []Arrival
	var t float64
	i := int64(0)
	limit := duration.Nanoseconds()
	// Cap the schedule so a pathological rate cannot exhaust memory.
	const maxArrivals = 2_000_000
	for i < maxArrivals {
		h := stream.digest('s', i)
		// 53-bit uniform from the digest.
		u := float64(uint64(h[0])<<45|uint64(h[1])<<37|uint64(h[2])<<29|uint64(h[3])<<21|
			uint64(h[4])<<13|uint64(h[5])<<5|uint64(h[6])>>3) / float64(1<<53)
		if u <= 0 {
			u = 1e-12
		}
		if u >= 1 {
			u = 1 - 1e-12
		}
		t += -math.Log(1-u) / ratePerSec
		ns := int64(t * 1e9)
		if ns >= limit {
			break
		}
		key, hit := stream.At(i)
		out = append(out, Arrival{IntendedNs: ns, Key: key, ExpectHit: hit})
		i++
	}
	return out
}
