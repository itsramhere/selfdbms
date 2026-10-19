package harness

import (
	"sync"
	"sync/atomic"
	"time"

	"owndbms/lsmengine"
)

// FlushTracker implements lsmengine.FlushObserver and collects high-resolution,
// decoupled operational metrics across all phases of flush admission, queueing,
// physical SSTable construction, and publication.
type FlushTracker struct {
	AttemptedRequests atomic.Int64 `json:"attempted_requests"`
	AcceptedRequests  atomic.Int64 `json:"accepted_requests"`
	RejectedRequests  atomic.Int64 `json:"rejected_requests"`
	StartedFlushes    atomic.Int64 `json:"started_flushes"`
	CompletedFlushes  atomic.Int64 `json:"completed_flushes"`
	FailedFlushes     atomic.Int64 `json:"failed_flushes"`
	DelayedRequests   atomic.Int64 `json:"delayed_requests"`

	AdmissionDurationNanos atomic.Int64 `json:"admission_duration_nanos"`
	QueueWaitDurationNanos atomic.Int64 `json:"queue_wait_duration_nanos"`
	PhysicalFlushNanos     atomic.Int64 `json:"physical_flush_nanos"`
	PublicationNanos       atomic.Int64 `json:"publication_nanos"`

	ReadsDuringPhysicalFlush atomic.Int64 `json:"reads_during_physical_flush"`
	ReadsDuringPublication   atomic.Int64 `json:"reads_during_publication"`
	ReadsDuringQueueWait     atomic.Int64 `json:"reads_during_queue_wait"`

	// Active phase indicators
	isPhysicalFlushActive atomic.Bool
	isPublicationActive   atomic.Bool

	// Timing queues
	mu           sync.Mutex
	requestTimes []time.Time
	admitTimes   []time.Time
	activeStart  time.Time
	publishStart time.Time
}

// NewFlushTracker initializes an empty FlushTracker.
func NewFlushTracker() *FlushTracker {
	return &FlushTracker{}
}

// Ensure FlushTracker implements lsmengine.FlushObserver.
var _ lsmengine.FlushObserver = (*FlushTracker)(nil)

// IsPhysicalFlushActive returns true only while the background worker is actively
// executing physical SSTable construction and publication.
func (ft *FlushTracker) IsPhysicalFlushActive() bool {
	return ft.isPhysicalFlushActive.Load()
}

// IsPublicationActive returns true only while the engine is executing SSTable publication.
func (ft *FlushTracker) IsPublicationActive() bool {
	return ft.isPublicationActive.Load()
}

// RecordPointRead records whether a point read overlapped physical flush or publication.
func (ft *FlushTracker) RecordPointRead() {
	if ft.isPublicationActive.Load() {
		ft.ReadsDuringPublication.Add(1)
	}
	if ft.isPhysicalFlushActive.Load() {
		ft.ReadsDuringPhysicalFlush.Add(1)
	} else {
		ft.mu.Lock()
		queued := len(ft.admitTimes) > 0
		ft.mu.Unlock()
		if queued {
			ft.ReadsDuringQueueWait.Add(1)
		}
	}
}

// OnFlushRequested is called when a flush request is issued.
func (ft *FlushTracker) OnFlushRequested() {
	ft.AttemptedRequests.Add(1)
	now := time.Now()
	ft.mu.Lock()
	ft.requestTimes = append(ft.requestTimes, now)
	ft.mu.Unlock()
}

// OnFlushAdmitted is called when the engine evaluates the flush request.
func (ft *FlushTracker) OnFlushAdmitted(accepted bool, err error) {
	now := time.Now()
	ft.mu.Lock()
	var reqTime time.Time
	if len(ft.requestTimes) > 0 {
		reqTime = ft.requestTimes[0]
		ft.requestTimes = ft.requestTimes[1:]
	} else {
		reqTime = now
	}

	admDur := now.Sub(reqTime).Nanoseconds()
	if admDur > 0 {
		ft.AdmissionDurationNanos.Add(admDur)
	}

	if accepted {
		ft.AcceptedRequests.Add(1)
		ft.admitTimes = append(ft.admitTimes, now)
	} else {
		ft.RejectedRequests.Add(1)
	}
	ft.mu.Unlock()
}

// OnFlushWorkerBegin is called when the background worker dequeues a Memtable to process.
func (ft *FlushTracker) OnFlushWorkerBegin() {
	ft.StartedFlushes.Add(1)
	now := time.Now()

	ft.mu.Lock()
	var admitTime time.Time
	if len(ft.admitTimes) > 0 {
		admitTime = ft.admitTimes[0]
		ft.admitTimes = ft.admitTimes[1:]
	} else {
		admitTime = now
	}
	ft.activeStart = now
	waitDur := now.Sub(admitTime).Nanoseconds()
	if waitDur > 0 {
		ft.QueueWaitDurationNanos.Add(waitDur)
	}
	ft.mu.Unlock()

	ft.isPhysicalFlushActive.Store(true)
}

// OnFlushBuildBegin is called when physical SSTable construction begins.
func (ft *FlushTracker) OnFlushBuildBegin() {
	// Physical flush is already active from OnFlushWorkerBegin
}

// OnFlushBuildEnd is called when physical SSTable construction finishes.
func (ft *FlushTracker) OnFlushBuildEnd(err error) {
	// Marker for SSTable build end
}

// OnFlushPublishBegin is called immediately before acquiring the publication critical section.
func (ft *FlushTracker) OnFlushPublishBegin() {
	ft.mu.Lock()
	ft.publishStart = time.Now()
	ft.mu.Unlock()
	ft.isPublicationActive.Store(true)
}

// OnFlushPublishEnd is called immediately after publication completes or fails.
func (ft *FlushTracker) OnFlushPublishEnd(err error) {
	now := time.Now()
	ft.mu.Lock()
	pubStart := ft.publishStart
	ft.mu.Unlock()

	ft.isPublicationActive.Store(false)
	pubDur := now.Sub(pubStart).Nanoseconds()
	if pubDur > 0 {
		ft.PublicationNanos.Add(pubDur)
	}
}

// OnFlushComplete is called when the entire flush operation finishes.
func (ft *FlushTracker) OnFlushComplete(err error) {
	now := time.Now()
	ft.mu.Lock()
	start := ft.activeStart
	ft.mu.Unlock()

	ft.isPhysicalFlushActive.Store(false)
	physDur := now.Sub(start).Nanoseconds()
	if physDur > 0 {
		ft.PhysicalFlushNanos.Add(physDur)
	}

	if err == nil {
		ft.CompletedFlushes.Add(1)
	} else {
		ft.FailedFlushes.Add(1)
	}
}
