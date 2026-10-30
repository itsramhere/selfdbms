// Package lsmengine implements an MVCC LSM engine with selectable synchronization
// strategies sharing one SSTable point-read path. Point reads default to range
// checks and one ReadAt per variable-size data block, with early termination;
// historical scans and an experiment-only block I/O delay are configurable.
// WAL records are written atomically with respect to other appends and synced
// by default; Config.DisableWALSync opts out of the per-record fsync.
package lsmengine
