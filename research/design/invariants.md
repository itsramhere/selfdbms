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

### INV-MVCC-5: Cross-Tier Conflict Detection Safety
- **Statement**: For every key in a committing transaction's write set, conflict validation must inspect the latest committed sequence number across all tiers (Active Memtable $\to$ Frozen Memtables $\to$ Active SSTables) including both Puts and Tombstones. If any visible version has $Seq > T_{\text{read}}$, the transaction must abort.
- **Current Status**: **Verified (Sub-phases I1.1b & I1.2.0 Option A)**.
- **Evidence / Justification**: Implemented in `latestValueLocked` via Option A: iterates all `ActiveSSreaders` and tracks `maxSeq`, eliminating vulnerability to SSTable slice reordering (Issue E-004). Verified by `TestI1_2_0_SSTableOrderingAnomaly` and `TestE004_TxnConflict_NewestVersionNotInLastSSTable`.

### INV-MVCC-6: Monotonic Sequence Ordering of RAM vs Disk
- **Statement**: Any version stored in the active Memtable `m.Mem` has a sequence number strictly greater than all versions in frozen Memtables and on-disk SSTables. Any version in a frozen Memtable `m.Frozenmem[i]` has a sequence number strictly greater than all versions in `m.Frozenmem[i-1]` and all on-disk SSTables.
- **Current Status**: **Verified**.
- **Evidence / Justification**: Sequence numbers are atomically incremented on commit, and all writes append directly to `m.Mem`. Memtables only freeze after active writes stop. This guarantees that finding a key in RAM tiers allows safe early return in `latestValueLocked` without needing disk I/O.

### INV-ORDER-1: Chronological Ordering of ActiveSSreaders
- **Statement**: Elements in `m.ActiveSSreaders` must be ordered chronologically by creation timestamp/generation, from oldest at index 0 to newest at index `len-1`.
- **Current Status**: **Required for Manager.Get; Guaranteed by flushToDisk and protected publication in I1.2.2**.
- **Evidence / Justification**: Point reads (`Manager.Get`) iterate backwards from `len-1` down to 0 and terminate on the first visible version. Audit test `TestManagerGet_OrderingAudit` demonstrated that inverting `ActiveSSreaders` causes older versions to mask newer visible versions. Therefore, compaction publication must replace compacted inputs in-place to preserve chronological sequence.

### INV-COMPACT-1: Compaction Input Contiguity and Chronological Precedence
- **Statement**: Under the flat reader slice architecture (`m.ActiveSSreaders []*SSTableReader`) and first-matching-reader lookup (`Manager.Get`), SSTable compaction inputs must preserve the chronological precedence of all surviving readers. Compaction input selection must be strictly restricted to a contiguous prefix `[0..k]` of `m.ActiveSSreaders`. Noncontiguous input selection is strictly prohibited, as replacing noncontiguous inputs with a single merged table inverts relative reader order and causes older surviving intermediate readers to mask newer compacted updates (Issue E-005).
- **Current Status**: **Adopted and Enforced (Sub-phase I1.3.0a, Issue E-005)**.
- **Evidence / Justification**: Proved via counterexample in `TestCounterexample_NoncontiguousCompactionViolatesOrder` (`E-005/repro.md`). Defensively enforced in `replaceCompactedReaders`, which validates that input readers appear without duplicates, form a strictly contiguous prefix starting at index 0 matching the current full-prefix policy, and preserves all concurrently appended flushes in chronological order.


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

---

## 5. Approach A: Baseline RWMutex Protocol Specification (I1.1)

Under Approach A, synchronization relies entirely on `Manager.Mu (*sync.RWMutex)` protecting all engine state and file descriptor lifetimes:

### Protocol Rules:
1. **Point Reader Scope (`Manager.Get`)**:
   - Must hold `m.Mu.RLock()` across the entire multi-tier lookup: active `Mem`, `Frozenmem`, and `ActiveSSreaders`.
   - The open physical file descriptors (`sst.File`) remain valid for the duration of the read because any retirement/compaction publication requires `m.Mu.Lock()`, which blocks until all concurrent `RLock()` holders release the mutex.
2. **Commit Scope (`Transaction.Commit`)**:
   - Must hold `m.Mu.Lock()` across conflict validation, WAL append, and Memtable insertion.
   - Internal conflict lookup MUST use lock-free or caller-locked helpers (`latestValueLocked`) without attempting nested `RLock()` or `Lock()` on `m.Mu`.
3. **Lock Order Rule**:
   - Strict hierarchical order: `m.compactionMu` (Mutex) $\to$ `Manager.Mu` (Exclusive or Shared) $\to$ `Wal.mu` (Mutex).
   - `m.compactionMu` is never held while requesting another lock, except `Manager.Mu`.
   - `Wal.mu` is never held while requesting `Manager.Mu` or `m.compactionMu`.
4. **Compaction & Reader Lifetime Protocol (Verified Sub-phase I1.3)**:
   - Compaction merges a contiguous prefix snapshot $S_c \subseteq \text{ActiveSSreaders}$ starting at index 0 (`INV-COMPACT-1`).
   - Compactions are serialized across their entire lifetime via `m.compactionMu`, eliminating overlapping compaction races (`E-006`).
   - Under `m.Mu.Lock()`, publication computes in-place prefix replacement:
     $$\text{ActiveSSreaders}_{\text{new}} = \{ R_{\text{new}} \} \cup (\text{ActiveSSreaders}_{\text{current}} \setminus S_c)$$
     preserving any SSTables concurrently published by `flushToDisk` in strict chronological order (`INV-ORDER-1`).
   - Iterators used during merge and retired reader file handles must be closed before unlinking old files (`os.Remove`), preventing Windows `ERROR_SHARING_VIOLATION`.
   - `Manager.Close()` coordinates via `m.compactionMu` and `m.isClosed`, ensuring no active reader is closed while in use by compaction.

### INV-LIFETIME-4: Compaction Serialization and Shutdown Lifecycle Safety
- **Statement**: Only one compaction routine may execute against active SSTable readers at any time. Compaction lifetimes must be serialized from reader acquisition through file unlinking via `m.compactionMu`. Engine shutdown (`Manager.Close`) must coordinate with `m.compactionMu` and set `isClosed = true`, ensuring active reader descriptors are never invalidated while in-flight compaction merges are reading them.
- **Current Status**: **Adopted and Enforced (Sub-phase I1.3.3, Issue E-006)**.
- **Evidence / Justification**: Implemented in `lsmengine/compaction.go` and `lsmengine/manager.go` via `m.compactionMu` and `m.isClosed`. Verified by `TestReaderLifetime_CaseA_OverlappingCompactions`, `TestReaderLifetime_CaseB_ShutdownDuringCompaction`, and `TestFilesystem_CloseBeforeDelete`.

