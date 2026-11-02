package harness2

// Lock-free aggregation of the engine's synchronization instrumentation
// (Phase I7 engineering preflight). The collector implements
// lsmengine.SyncObserver; its callbacks run on reader, writer and flush-worker
// hot paths, so it uses only atomics — no mutex, no allocation.
//
// E/D aggregates retain their whole-trial scope. X trials reset after warmup,
// so their observation scope starts at the measurement window and includes the
// post-window drain. X additionally uses atomic histogram buckets for
// percentiles; old blocks retain the original commit/read/flush aggregation cost.

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
	case lsmengine.LockSiteCompactionStart:
		return 3
	case lsmengine.LockSiteCompactionSnapshotPublish:
		return 4
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
	case 3:
		return lsmengine.LockSiteCompactionStart.String()
	case 4:
		return lsmengine.LockSiteCompactionSnapshotPublish.String()
	}
	return "unknown"
}

// dist is one atomic distribution: count, sum, min, max. min uses 0 as the
// unset sentinel (durations are non-negative, so a stored 0 and "nothing
// stored" are equivalent for reporting).
type dist struct {
	histogram *[histogramBuckets]atomic.Int64
	count     atomic.Int64
	sum       atomic.Int64
	min       atomic.Int64
	max       atomic.Int64
}

func (d *dist) add(v int64) {
	if d.histogram != nil {
		d.histogram[histogramIndex(v)].Add(1)
	}
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

	wait         [5]dist
	waitHolders  [5]atomic.Int64 // sum of holder samples at arrival
	waitArrivals [5]atomic.Int64 // number of holder samples folded into the sum

	pub              [5]dist
	pubHolders       [5]atomic.Int64
	pubArrivals      [5]atomic.Int64
	compactionWall   dist
	compactionInputs dist
	retirementLag    dist
	retired          atomic.Int64
	retiredHighWater atomic.Int64
}

var _ lsmengine.SyncObserver = (*SyncCollector)(nil)

// NewSyncCollector returns an empty collector.
func NewSyncCollector() *SyncCollector {
	c := &SyncCollector{}
	// Compaction lifecycle percentiles are available whenever sync is attached.
	for i := 2; i < len(c.wait); i++ {
		c.wait[i].histogram = new([histogramBuckets]atomic.Int64)
		c.pub[i].histogram = new([histogramBuckets]atomic.Int64)
	}
	c.compactionWall.histogram = new([histogramBuckets]atomic.Int64)
	c.retirementLag.histogram = new([histogramBuckets]atomic.Int64)
	return c
}

// NewSyncHistogramCollector enables the X percentile view before observers run.
func NewSyncHistogramCollector() *SyncCollector {
	c := NewSyncCollector()
	for i := 0; i < 3; i++ {
		c.readCS[i].histogram = new([histogramBuckets]atomic.Int64)
	}
	for i := range c.wait {
		c.wait[i].histogram = new([histogramBuckets]atomic.Int64)
		c.pub[i].histogram = new([histogramBuckets]atomic.Int64)
	}
	c.compactionWall.histogram = new([histogramBuckets]atomic.Int64)
	c.retirementLag.histogram = new([histogramBuckets]atomic.Int64)
	return c
}

// reset clears observations without replacing atomics or histogram storage.
// PRECONDITION: no observer callbacks are active until reset has completed.
func (d *dist) reset() {
	d.count.Store(0)
	d.sum.Store(0)
	d.min.Store(0)
	d.max.Store(0)
	if d.histogram != nil {
		for i := range d.histogram {
			d.histogram[i].Store(0)
		}
	}
}

// Reset starts a new observation phase for either collector variant. The engine
// must be idle: atomic stores avoid copying live atomic values, but the sequence
// is not a linearizable reset against concurrent callbacks. Histogram pointers
// remain attached so subsequent observations retain the same percentile view.
func (c *SyncCollector) Reset() {
	for i := 0; i < 3; i++ {
		c.readCS[i].reset()
	}
	for i := range c.wait {
		c.wait[i].reset()
		c.pub[i].reset()
		c.waitHolders[i].Store(0)
		c.waitArrivals[i].Store(0)
		c.pubHolders[i].Store(0)
		c.pubArrivals[i].Store(0)
	}
	c.compactionWall.reset()
	c.compactionInputs.reset()
	c.retirementLag.reset()
	c.retiredHighWater.Store(c.retired.Load())
}

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
	P50Nanos             *int64  `json:"p50_nanos,omitempty"`
	P95Nanos             *int64  `json:"p95_nanos,omitempty"`
	P99Nanos             *int64  `json:"p99_nanos,omitempty"`
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
	CompactionWallNanos      *SyncStats           `json:"compaction_wall_nanos,omitempty"`
	CompactionInputs         *InputStats          `json:"compaction_inputs,omitempty"`
	RetirementLagNanos       *SyncStats           `json:"retirement_lag_nanos,omitempty"`
	RetiredTablesHighWater   int64                `json:"retired_tables_high_water,omitempty"`
}

func (d *dist) stats(holdersSum, arrivals int64) SyncStats {
	st := SyncStats{Count: d.count.Load(), MinNanos: d.min.Load(), MaxNanos: d.max.Load()}
	if st.Count > 0 {
		st.MeanNanos = float64(d.sum.Load()) / float64(st.Count)
	}
	if arrivals > 0 {
		st.MeanHoldersAtArrival = float64(holdersSum) / float64(arrivals)
	}
	if d.histogram != nil {
		var h Histogram
		for i := range h.buckets {
			h.buckets[i] = d.histogram[i].Load()
			h.count += h.buckets[i]
		}
		p50, p95, p99 := h.percentile(.5), h.percentile(.95), h.percentile(.99)
		st.P50Nanos, st.P95Nanos, st.P99Nanos = &p50, &p95, &p99
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
	}
	for i := range c.wait {
		if c.wait[i].count.Load() > 0 {
			out.WriterLockWaitNanos[siteSlotName(i)] = c.wait[i].stats(c.waitHolders[i].Load(), c.waitArrivals[i].Load())
		}
		if c.pub[i].count.Load() > 0 {
			out.PublicationNanos[siteSlotName(i)] = c.pub[i].stats(c.pubHolders[i].Load(), c.pubArrivals[i].Load())
		}
	}
	if c.compactionWall.count.Load() > 0 {
		st := c.compactionWall.stats(0, 0)
		out.CompactionWallNanos = &st
		out.CompactionInputs = &InputStats{Mean: float64(c.compactionInputs.sum.Load()) / float64(c.compactionInputs.count.Load()), Max: c.compactionInputs.max.Load()}
	}
	if c.retirementLag.count.Load() > 0 {
		st := c.retirementLag.stats(0, 0)
		out.RetirementLagNanos = &st
	}
	out.RetiredTablesHighWater = c.retiredHighWater.Load()
	return out
}

// InputStats describes the fan-in of successful compactions that did work.
type InputStats struct {
	Mean float64 `json:"mean"`
	Max  int64   `json:"max"`
}

func (c *SyncCollector) OnCompaction(durationNs int64, inputs int) {
	c.compactionWall.add(durationNs)
	c.compactionInputs.add(int64(inputs))
}

func (c *SyncCollector) OnTableRetired() {
	n := c.retired.Add(1)
	for {
		old := c.retiredHighWater.Load()
		if n <= old || c.retiredHighWater.CompareAndSwap(old, n) {
			break
		}
	}
}

func (c *SyncCollector) OnTableRemoved(lagNs int64) {
	c.retirementLag.add(lagNs)
	c.retired.Add(-1)
}

var _ lsmengine.CompactionObserver = (*SyncCollector)(nil)
