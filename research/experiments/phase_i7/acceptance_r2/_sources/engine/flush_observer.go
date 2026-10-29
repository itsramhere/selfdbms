package lsmengine

// FlushObserver receives callbacks during the discrete phases of a Memtable-to-SSTable flush.
// Implementations must be non-blocking and thread-safe.
type FlushObserver interface {
	// OnFlushRequested is called when a flush request is issued (TriggerFlush or checkAndTriggerFlush).
	OnFlushRequested()

	// OnFlushAdmitted is called when the request is evaluated and either accepted into the FIFO queue or rejected.
	OnFlushAdmitted(accepted bool, err error)

	// OnFlushWorkerBegin is called when the background flush worker dequeues the Memtable and starts processing.
	OnFlushWorkerBegin()

	// OnFlushBuildBegin is called when physical SSTable file construction begins.
	OnFlushBuildBegin()

	// OnFlushBuildEnd is called when physical SSTable file construction finishes.
	OnFlushBuildEnd(err error)

	// OnFlushPublishBegin is called immediately before acquiring synchronization to publish the new SSTable.
	// activeRLockHolders is the Manager's sampled count of active Manager.Mu
	// read holders at writer arrival (R1.2e §3.2). The sample is taken before
	// the publication Lock attempt and can overcount the true blocking set by
	// readers that are mid-acquisition or mid-release; see
	// lsmengine/sync_instrumentation.go for the sampling semantics.
	OnFlushPublishBegin(activeRLockHolders int)

	// OnFlushPublishEnd is called immediately after publication completes or fails.
	OnFlushPublishEnd(err error)

	// OnFlushComplete is called when the flush operation terminates (success or failure).
	OnFlushComplete(err error)
}
