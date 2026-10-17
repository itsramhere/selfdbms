package lsmengine

import (
	"sync"
	"sync/atomic"
	"time"
)

// SnapshotState represents the lifecycle phase of an immutable SSTable reader-set snapshot.
type SnapshotState int

const (
	SnapshotConstructing SnapshotState = iota
	SnapshotPublished
	SnapshotPinned
	SnapshotRetired
	SnapshotDrained
	SnapshotClosed
	SnapshotReclaimed
)

func (s SnapshotState) String() string {
	switch s {
	case SnapshotConstructing:
		return "Constructing"
	case SnapshotPublished:
		return "Published"
	case SnapshotPinned:
		return "Pinned"
	case SnapshotRetired:
		return "Retired"
	case SnapshotDrained:
		return "Drained"
	case SnapshotClosed:
		return "Closed"
	case SnapshotReclaimed:
		return "Reclaimed"
	default:
		return "Unknown"
	}
}

// SharedReader wraps an SSTableReader with an independent reference count and safe retirement metadata.
// Multiple snapshots or compaction routines can concurrently hold references to the same SharedReader.
type SharedReader struct {
	Reader    *SSTableReader
	Path      string
	refCount  atomic.Int64
	obsolete  atomic.Bool // Marked for deletion once refCount reaches zero
	closeOnce sync.Once
	closeErr  error
}

// Ref increments the reference count of the shared reader.
func (sr *SharedReader) Ref() {
	sr.refCount.Add(1)
}

// RefCount returns the current reference count.
func (sr *SharedReader) RefCount() int64 {
	return sr.refCount.Load()
}

// ReaderSnapshot represents an immutable, point-in-time view of active SSTable readers.
// Once published, the readers slice is strictly read-only and never mutated.
type ReaderSnapshot struct {
	id        uint64
	state     SnapshotState
	readers   []*SharedReader
	pins      int64
	retiredAt time.Time
}

// State returns the current lifecycle state of the snapshot.
func (rs *ReaderSnapshot) State() SnapshotState {
	return rs.state
}

// Pins returns the current active pin count of the snapshot.
func (rs *ReaderSnapshot) Pins() int64 {
	return rs.pins
}

// ReaderSyncStrategy defines the internal strategy interface distinguishing Approach A
// (Manager-wide RWMutex) from Approach B (Immutable Snapshot Pinning).
type ReaderSyncStrategy interface {
	Name() string
	Mode() SyncMode
}
