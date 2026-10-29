package harness2

// Lock-free aggregation of the engine's synchronization instrumentation
// (Phase I7 engineering preflight). The collector implements
// lsmengine.SyncObserver; its callbacks run on reader, writer and flush-worker
// hot paths, so it uses only atomics — no mutex, no allocation.
//
// Aggregates cover the whole trial lifecycle, including fixture construction
// and the post-window drain; the observation scope is the trial, not the
// measurement window. Percentiles are deliberately not computed online (a
// bounded sample buffer would either drop or lock); consumers get
// count/mean/min/max plus the mean holder sample at arrival.

import (
	"sync/atomic"

	"owndbms/lsmengine"
)

// modeIndex maps a SyncMode to a collector slot. Unknown modes are dropped.
func modeIndex(mode lsmengine.SyncMode) int {
	switch mode {
	case lsmengine.SyncModeApproachA:
		return 0
	case lsmengine.SyncModeApproachAMin:
		return 1
	case lsmengine.SyncModeApproachB:
		return 2
	}
	return -1
}

// siteIndex maps a LockSite to a collector slot. Unknown sites are dropped.
func siteIndex(site lsmengine.LockSite) int {
	switch site {
	case lsmengine.LockSiteCommit:
		return 0
	case lsmengine.LockSiteFlushPublish:
		return 1
	case lsmengine.LockSiteCompactionPublish:
		return 2
	}
	return -1
}

func modeSlotName(i int) string {
	switch i {
	case 0:
		return lsmengine.SyncModeApproachA.String()
	case 1:
		return lsmengine.SyncModeApproachAMin.String()
	case 2:
		return lsmengine.SyncModeApproachB.String()
	}
	return "unknown"
}

func siteSlotName(i int) string {
	switch i {
	case 0:
		return lsmengine.LockSiteCommit.String()
	case 1:
		return lsmengine.LockSiteFlushPublish.String()
	case 2:
		return lsmengine.LockSiteCompactionPublish.String()
	}
	return "unknown"
}

// dist is one atomic distribution: count, sum, min, max. min uses 0 as the
// unset sentinel (durations are non-negative, so a stored 0 and "nothing
// stored" are equivalent for reporting).
type dist struct {
	count atomic.Int64
	sum   atomic.Int64
	min   atomic.Int64
	max   atomic.Int64
}

func (d *dist) add(v int64) {
	d.count.Add(1)
	d.sum.Add(v)
	for {
		cur := d.min.Load()
		if cur != 0 && cur <= v {
			break
		}
		if d.min.CompareAndSwap(cur, v) {
			break
		}
	}
	for {
		cur := d.max.Load()
		if cur >= v {
			break
		}
		if d.max.CompareAndSwap(cur, v) {
			break
		}
	}
}

// SyncCollector accumulates engine synchronization observations.
type SyncCollector struct {
	readCS [3]dist

	wait         [3]dist
	waitHolders  [3]atomic.Int64 // sum of holder samples at arrival
	waitArrivals [3]atomic.Int64 // number of holder samples folded into the sum

	pub         [3]dist
	pubHolders  [3]atomic.Int64
	pubArrivals [3]atomic.Int64
}

var _ lsmengine.SyncObserver = (*SyncCollector)(nil)

// NewSyncCollector returns an empty collector.
func NewSyncCollector() *SyncCollector { return &SyncCollector{} }

// OnReadCriticalSection implements lsmengine.SyncObserver.
func (c *SyncCollector) OnReadCriticalSection(mode lsmengine.SyncMode, durationNs int64) {
	if i := modeIndex(mode); i >= 0 {
		c.readCS[i].add(durationNs)
	}
}

// OnWriterLockWait implements lsmengine.SyncObserver.
func (c *SyncCollector) OnWriterLockWait(site lsmengine.LockSite, waitNs int64, holdersAtArrival int32) {
	i := siteIndex(site)
	if i < 0 {
		return
	}
	c.wait[i].add(waitNs)
	c.waitHolders[i].Add(int64(holdersAtArrival))
	c.waitArrivals[i].Add(1)
}

// OnPublication implements lsmengine.SyncObserver.
func (c *SyncCollector) OnPublication(site lsmengine.LockSite, durationNs int64, holdersAtArrival int32) {
	i := siteIndex(site)
	if i < 0 {
		return
	}
	c.pub[i].add(durationNs)
	c.pubHolders[i].Add(int64(holdersAtArrival))
	c.pubArrivals[i].Add(1)
}

// SyncStats is one reported distribution.
type SyncStats struct {
	Count                int64   `json:"count"`
	MeanNanos            float64 `json:"mean_nanos"`
	MinNanos             int64   `json:"min_nanos"`
	MaxNanos             int64   `json:"max_nanos"`
	MeanHoldersAtArrival float64 `json:"mean_holders_at_arrival,omitempty"`
}

// SyncInstrumentationSummary is the per-trial instrumentation payload.
type SyncInstrumentationSummary struct {
	ReadCriticalSectionNanos map[string]SyncStats `json:"read_critical_section_nanos"`
	WriterLockWaitNanos      map[string]SyncStats `json:"writer_lock_wait_nanos"`
	PublicationNanos         map[string]SyncStats `json:"publication_nanos"`
}

func (d *dist) stats(holdersSum, arrivals int64) SyncStats {
	st := SyncStats{Count: d.count.Load(), MinNanos: d.min.Load(), MaxNanos: d.max.Load()}
	if st.Count > 0 {
		st.MeanNanos = float64(d.sum.Load()) / float64(st.Count)
	}
	if arrivals > 0 {
		st.MeanHoldersAtArrival = float64(holdersSum) / float64(arrivals)
	}
	return st
}

// Summary materialises the aggregated distributions. Empty distributions are
// omitted so downstream consumers never confuse "zero observations" with
// "instrumentation absent" (absence is signalled by a nil summary).
func (c *SyncCollector) Summary() SyncInstrumentationSummary {
	out := SyncInstrumentationSummary{
		ReadCriticalSectionNanos: map[string]SyncStats{},
		WriterLockWaitNanos:      map[string]SyncStats{},
		PublicationNanos:         map[string]SyncStats{},
	}
	for i := 0; i < 3; i++ {
		if c.readCS[i].count.Load() > 0 {
			out.ReadCriticalSectionNanos[modeSlotName(i)] = c.readCS[i].stats(0, 0)
		}
		if c.wait[i].count.Load() > 0 {
			out.WriterLockWaitNanos[siteSlotName(i)] = c.wait[i].stats(c.waitHolders[i].Load(), c.waitArrivals[i].Load())
		}
		if c.pub[i].count.Load() > 0 {
			out.PublicationNanos[siteSlotName(i)] = c.pub[i].stats(c.pubHolders[i].Load(), c.pubArrivals[i].Load())
		}
	}
	return out
}
