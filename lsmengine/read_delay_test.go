package lsmengine

import (
	"sort"
	"testing"
	"time"
)

func TestInjectReadDelayAccuracy(t *testing.T) {
	if raceEnabled {
		t.Skip("timing accuracy is not meaningful under race instrumentation")
	}
	for _, delay := range []time.Duration{20 * time.Microsecond, 200 * time.Microsecond, 2 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			count := 200
			if delay == 2*time.Millisecond {
				count = 50
			}
			samples := make([]int64, count)
			var total int64
			for i := range samples {
				start := hrNowNanos()
				injectReadDelay(delay)
				samples[i] = hrNowNanos() - start
				total += samples[i]
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			mean := float64(total) / float64(count)
			median := float64(samples[count/2-1]+samples[count/2]) / 2
			lower := float64(delay.Nanoseconds())
			upper := lower + float64(max(10*time.Microsecond, delay/10).Nanoseconds())
			t.Logf("nominal=%v samples=%d mean=%.3f us median=%.3f us allowed=[%.3f, %.3f] us",
				delay, count, mean/1e3, median/1e3, lower/1e3, upper/1e3)
			if mean < lower || mean > upper {
				t.Errorf("mean %.3f us outside allowed band", mean/1e3)
			}
			if median < lower || median > upper {
				t.Errorf("median %.3f us outside allowed band", median/1e3)
			}
		})
	}
}
