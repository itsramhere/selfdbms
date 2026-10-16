# Formal Invariants and Concurrency Contract

**Document Phase:** I0.3 — Baseline Reconnaissance  
**Target Git Branch:** `research/reader-compactor-sync`  
**Base Commit:** `08d3189069294e8168ebb5234a3bef1d37f1d649`  

This document codifies the exact correctness, lifetime, and safety invariants of the storage engine. Each invariant is explicitly classified as **Verified**, **Violated**, or **Unproven** in the current baseline codebase.

---

## 1. Concurrency and Synchronization Invariants

### INV-SYNC-1: SkipList Mutation Safety
- **Statement**: Active Memtable mutations (`Put`) and lookups (`Get`, `LatestValue`) never run concurrently without synchronization.
- **Current Status**: **Verified (conditional)**.
- **Evidence / Justification**: `Memtable.Put` is only invoked in `Transaction.Commit` while `Manager.Mu.Lock()` is held. `Memtable.Get` and `LatestValue` are only invoked while `Manager.Mu.RLock()` is held. SkipList is not lock-free, but mutual exclusion prevents concurrent mutation hazards.

### INV-SYNC-2: Re-entrant Lock Freedom
- **Statement**: No call path shall attempt to acquire an `RLock` or `Lock` on `Manager.Mu` while already holding a lock on the same mutex within the same goroutine.
- **Current Status**: **VIOLATED (CRITICAL)**.
- **Evidence / Justification**: In `Transaction.Commit` ([`lsmengine/transaction.go:56-60`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L56-L60)), `t.Man.Mu.Lock()` is acquired. Line 60 then invokes `t.Man.LatestValue(key)`, which attempts to acquire `m.Mu.RLock()` ([`lsmengine/manager.go:120`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L120)). Go's `sync.RWMutex` does not support re-entrant read locking while an exclusive write lock is active, causing immediate, deterministic self-deadlock.

### INV-SYNC-3: Lock Acquisition Hierarchy
- **Statement**: If multiple mutexes are acquired, they must always follow the strict order: `Manager.Mu -> Wal.mu`.
- **Current Status**: **Verified**.
- **Evidence / Justification**: In `Transaction.Commit`, `t.Man.Mu.Lock()` is held before calling `t.Man.Log.Append`, which acquires `w.mu.Lock()`. No call path acquires `Wal.mu` and then attempts to acquire `Manager.Mu`.

---

## 2. SSTable Reader and File Lifetime Invariants

### INV-LIFETIME-1: Pin-Protected Reader Access
- **Statement**: An `SSTableReader` file descriptor shall never be closed or unlinked while any reader goroutine is executing I/O against it.
- **Current Status**: **Unproven / Fragile in Baseline**.
- **Evidence / Justification**: In the baseline implementation, `Manager.Get` holds `m.Mu.RLock()` for the entire duration of `SSTableReader.Get()`. `CompactAll` acquires `m.Mu.Lock()` to overwrite `m.ActiveSSreaders`, which waits for in-flight `RLock()` holders to exit before replacing the slice. However, `r.Close()` and `os.Remove(r.File.Name())` in `CompactAll` execute without reference counting or explicit reader tracking. Under proposed Approach B (immutable snapshots without manager RLock), this invariant is completely unprotected without a pinning protocol.

### INV-LIFETIME-2: Safe Reader Retirement & Deletion
- **Statement**: An SSTable file shall only be unlinked from the filesystem after all active readers have released it and no new readers can discover it.
- **Current Status**: **VIOLATED on Windows**.
- **Evidence / Justification**: In `CompactAll` ([`lsmengine/compaction.go:114`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go#L114)), `os.Remove(r.File.Name())` is invoked immediately after `r.Close()`. However, `iterators` also hold open file handles to the same files ([`lsmengine/compaction.go:28`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go#L28)). On Windows NTFS, if an iterator or external handle remains open, `os.Remove` fails with an access violation error.

### INV-LIFETIME-3: Non-Destructive Compaction Publication
- **Statement**: Compaction publication must atomically retire only the subset of SSTables that participated in the compaction; it must never discard concurrently flushed SSTables.
- **Current Status**: **VIOLATED (DATA LOSS HAZARD)**.
- **Evidence / Justification**: In `CompactAll` ([`lsmengine/compaction.go:106`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go#L106)), the active reader set is unconditionally overwritten: `m.ActiveSSreaders = []*SSTableReader{newReader}`. If `flushToDisk()` completes during the compaction merge and appends a newly flushed SSTable to `m.ActiveSSreaders`, that newly flushed SSTable is overwritten and permanently lost from the active read path.

---

## 3. Versioning, MVCC, and Tombstone Invariants

### INV-MVCC-1: Monotonic Sequence Number Allocation
- **Statement**: Every committing transaction receives a strictly monotonically increasing sequence number `AllocatedSeqNo`.
- **Current Status**: **Verified**.
- **Evidence / Justification**: `t.Man.CurrentSeqNo.Add(1)` uses atomic increment. Under `t.Man.Mu.Lock()`, sequence numbers are assigned in commit order.

### INV-MVCC-2: Key Version Inversion Ordering
- **Statement**: For any user key $K$ and versions $V_1, V_2$ with sequence numbers $S_1 > S_2$, $EncodeInternalKey(K, S_1, D_1)$ must sort strictly before $EncodeInternalKey(K, S_2, D_2)$.
- **Current Status**: **Verified**.
- **Evidence / Justification**: Key encoding bitwise inverts the big-endian representation of `(seqNo << 8) | isDelete`. Higher sequence numbers produce smaller bitwise-inverted byte slices, ensuring descending version order in both the SkipList and SSTable index.

### INV-MVCC-3: Point Lookup Visibility
- **Statement**: A point read at timestamp $T_{read}$ shall return the value of the highest sequence number $S \le T_{read}$ for key $K$, or `ErrKeyDeleted` if the newest visible version is a tombstone, or `ErrNotFound` if no version with $S \le T_{read}$ exists.
- **Current Status**: **Verified**.
- **Evidence / Justification**: Both `Memtable.Get` and `SSTableReader.Get` encode a seek key with `readTs` and find the first candidate with `candidateSeqNo <= readTs`. If `isDelete` is set, `ErrKeyDeleted` is returned immediately, suppressing older versions.

### INV-MVCC-4: Cross-Tier Lookup Precedence
- **Statement**: Version lookups must proceed in strict chronological precedence: Active Memtable $\to$ Frozen Memtables (newest to oldest) $\to$ SSTables (newest to oldest).
- **Current Status**: **Verified (conditional on lock safety)**.
- **Evidence / Justification**: `Manager.Get` executes checks in order: `m.Mem.Get` -> `m.Frozenmem` reverse loop -> `m.ActiveSSreaders` reverse loop. A match at an earlier tier terminates the search.

---

## 4. Durability & Recovery Invariants

### INV-DUR-1: Write-Ahead Log Before Memory
- **Statement**: No mutation is visible in `Memtable` until it has been written to the WAL and synced to disk.
- **Current Status**: **Verified**.
- **Evidence / Justification**: In `Transaction.Commit`, `t.Man.Log.Append` (which executes `w.File.Sync()`) must succeed before `t.Man.Mem.Put` is called.

### INV-DUR-2: Crash Recovery Replay
- **Statement**: Replaying the WAL from disk restores all committed transactions up to the last successful `Sync()`.
- **Current Status**: **Unproven / Incomplete across Rotations**.
- **Evidence / Justification**: `RecoverFromWAL` recovers transactions from a single specified file path. However, `checkAndTriggerFlush` rotates WALs to `wal_<timestamp>.log` without tracking active WAL manifests. Replay across multiple rotated logs is unhandled.
