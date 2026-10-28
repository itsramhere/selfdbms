package harness2

// H-06: publication event log and event-conditioned latency.
//
// The EventRecorder implements lsmengine.FlushObserver and timestamps the real
// publication critical section (begin -> end) on the QPC clock. Because the
// injector releases pre-frozen memtables, the recorded window contains no write
// commits: it is the publication, not a WAL fsync convoy (audit AF-3).

import (
	"sort"
	"sync"

	"owndbms/research/experiments/harness"
)

// EventRecorder captures publication windows relative to a window base.
type EventRecorder struct {
	base harness.PrecisionTime

	mu       sync.Mutex
	released []int64
	buildBeg []int64
	events   []EventRecord
	cur      EventRecord
	pending  bool
}

// NewEventRecorder starts recording relative to base.
func NewEventRecorder(base harness.PrecisionTime) *EventRecorder {
	return &EventRecorder{base: base}
}

func (r *EventRecorder) now() int64 { return harness.SincePrecisionNanos(r.base) }

// MarkReleased is called by the injector immediately before releasing one flush.
func (r *EventRecorder) MarkReleased(memtableKeys int) {
	r.mu.Lock()
	r.released = append(r.released, r.now())
	r.mu.Unlock()
}

// OnFlushRequested implements lsmengine.FlushObserver.
func (r *EventRecorder) OnFlushRequested() {}

// OnFlushAdmitted implements lsmengine.FlushObserver.
func (r *EventRecorder) OnFlushAdmitted(accepted bool, err error) {}

// OnFlushWorkerBegin marks the start of the build for the oldest release.
func (r *EventRecorder) OnFlushWorkerBegin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	var releasedAt int64
	if len(r.released) > 0 {
		releasedAt = r.released[0]
		r.released = r.released[1:]
	}
	begin := r.now()
	r.buildBeg = append(r.buildBeg, begin)
	build := int64(0)
	if releasedAt > 0 && begin > releasedAt {
		build = begin - releasedAt
	}
	r.cur = EventRecord{
		Index:      len(r.events),
		ReleasedNs: releasedAt,
		BuildNs:    build,
	}
	r.pending = true
}

// OnFlushBuildBegin implements lsmengine.FlushObserver.
func (r *EventRecorder) OnFlushBuildBegin() {}

// OnFlushBuildEnd implements lsmengine.FlushObserver.
func (r *EventRecorder) OnFlushBuildEnd(err error) {}

// OnFlushPublishBegin starts the publication critical section.
func (r *EventRecorder) OnFlushPublishBegin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur.BeginNs = r.now()
	r.cur.Index = len(r.events)
}

// OnFlushPublishEnd closes the publication window.
func (r *EventRecorder) OnFlushPublishEnd(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur.EndNs = r.now()
	r.cur.HoldNs = r.cur.EndNs - r.cur.BeginNs
	if r.cur.HoldNs < 0 {
		r.cur.HoldNs = 0
	}
	if r.pending && r.cur.EndNs > 0 {
		r.events = append(r.events, r.cur)
		r.pending = false
	}
}

// OnFlushComplete implements lsmengine.FlushObserver.
func (r *EventRecorder) OnFlushComplete(err error) {}

// Events partitions recorded events into in-window (EndNs <= windowNs) and late (EndNs > windowNs).
func (r *EventRecorder) Events(windowNs int64) (inWindow []EventRecord, late []EventRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if windowNs <= 0 || e.EndNs <= windowNs {
			e.InWindow = true
			inWindow = append(inWindow, e)
		} else {
			e.InWindow = false
			late = append(late, e)
		}
	}
	return inWindow, late
}

// ConditionEvents selects reads that overlap each publication window (± delta)
// and reports their response-latency distribution plus the hold and block ratio.
//
// block_ratio = max_response / hold is the quantity that distinguishes "the
// reader waited out the publication" (Approach A, ratio ~1) from "the reader was
// unaffected" (A-min / B, ratio ~0).
func ConditionEvents(events []EventRecord, issues []Issue, deltaNs int64) []EventConditioned {
	out := make([]EventConditioned, 0, len(events))
	for _, e := range events {
		lo := e.BeginNs - deltaNs
		hi := e.EndNs + deltaNs
		var resp []int64
		for _, is := range issues {
			// Overlap test in window-relative nanoseconds.
			if is.CompleteNs < lo || is.IssuedNs > hi {
				continue
			}
			resp = append(resp, is.ResponseNs())
		}
		ec := EventConditioned{
			EventIndex:   e.Index,
			OverlapReads: len(resp),
			HoldNs:       e.HoldNs,
		}
		if len(resp) > 0 {
			sort.Slice(resp, func(i, j int) bool { return resp[i] < resp[j] })
			ec.MaxResponseNs = resp[len(resp)-1]
			ec.P50ResponseNs = resp[(len(resp)-1)/2]
			if len(resp) >= 20 {
				v := resp[int(float64(len(resp))*0.95)-1]
				if v < 0 {
					v = 0
				}
				ec.P95ResponseNs = &v
			}
			if e.HoldNs > 0 {
				ec.BlockRatio = float64(ec.MaxResponseNs) / float64(e.HoldNs)
			}
		}
		out = append(out, ec)
	}
	return out
}
