package lsmengine

import (
	"sync"
	"sync/atomic"
	"time"
)

// SnapshotState represents the logical lifecycle phase of an immutable SSTable reader-set snapshot.
type SnapshotState int32

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

// SharedReaderState represents the physical lifecycle phase of an SSTable reader and file descriptor.
type SharedReaderState int32

const (
	SharedReaderActive SharedReaderState = iota
	SharedReaderObsolete
	SharedReaderClosing
	SharedReaderClosed
	SharedReaderUnlinking
	SharedReaderReclaimed
	SharedReaderClosedValid
	SharedReaderFailed
)

func (s SharedReaderState) String() string {
	switch s {
	case SharedReaderActive:
		return "Active"
	case SharedReaderObsolete:
		return "Obsolete"
	case SharedReaderClosing:
		return "Closing"
	case SharedReaderClosed:
		return "Closed"
	case SharedReaderUnlinking:
		return "Unlinking"
	case SharedReaderReclaimed:
		return "Reclaimed"
	case SharedReaderClosedValid:
		return "ClosedValid"
	case SharedReaderFailed:
		return "Failed"
	default:
		return "Unknown"
	}
}

// SharedReader wraps an SSTableReader with an independent reference count, physical state, and safe retirement metadata.
// Multiple snapshots or compaction routines can concurrently hold references to the same SharedReader.
type SharedReader struct {
	Reader    *SSTableReader
	Path      string
	refCount  atomic.Int64
	state     atomic.Int32 // Stores SharedReaderState
	obsolete  atomic.Bool  // Marked for deletion once refCount reaches zero
	closeOnce sync.Once
	closeErr  error
	unlinkErr error
}

// Ref increments the reference count of the shared reader.
func (sr *SharedReader) Ref() {
	sr.refCount.Add(1)
}

// RefCount returns the current reference count.
func (sr *SharedReader) RefCount() int64 {
	return sr.refCount.Load()
}

// State returns the physical lifecycle state of the shared reader.
func (sr *SharedReader) State() SharedReaderState {
	return SharedReaderState(sr.state.Load())
}

// SetState updates the physical lifecycle state of the shared reader.
func (sr *SharedReader) SetState(s SharedReaderState) {
	sr.state.Store(int32(s))
}

// ReaderSnapshot represents an immutable, point-in-time view of active SSTable readers.
// Once published, the readers slice is strictly read-only and never mutated.
type ReaderSnapshot struct {
	id        uint64
	state     atomic.Int32 // Stores SnapshotState
	readers   []*SharedReader
	pins      atomic.Int64
	retiredAt time.Time
}

// State returns the current lifecycle state of the snapshot.
// Safe for concurrent calls without caller lock.
func (rs *ReaderSnapshot) State() SnapshotState {
	return SnapshotState(rs.state.Load())
}

// SetState updates the lifecycle state of the snapshot.
func (rs *ReaderSnapshot) SetState(s SnapshotState) {
	rs.state.Store(int32(s))
}

// Pins returns the current active pin count of the snapshot.
// Safe for concurrent calls without caller lock.
func (rs *ReaderSnapshot) Pins() int64 {
	return rs.pins.Load()
}

// ReaderSyncStrategy defines the internal strategy interface distinguishing Approach A
// (Manager-wide RWMutex) from Approach B (Immutable Snapshot Pinning).
type ReaderSyncStrategy interface {
	Name() string
	Mode() SyncMode
}
