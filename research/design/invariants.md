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
- **Current Status**: **Verified (Sub-phase I1.1a, Issue E-001)**.
- **Evidence / Justification**: Resolved by extracting unexported `latestValueLocked` on `Manager`. `Transaction.Commit` invokes `latestValueLocked` directly while holding `t.Man.Mu.Lock()`, eliminating the recursive mutex acquisition hazard. Public `LatestValue` acquires its own lock. Verified by `TestWriteWriteConflict`.

### INV-SYNC-3: Lock Acquisition Hierarchy
- **Statement**: If multiple mutexes are acquired, they must always follow the strict order: `m.compactionMu -> Manager.Mu -> m.flushQueueMu -> Wal.mu`.
- **Current Status**: **Verified (Sub-phases I1.3.3 & I1.3.8b)**.
- **Evidence / Justification**: In `Transaction.Commit`, `t.Man.Mu.Lock()` is held before calling `t.Man.Log.Append`, which acquires `w.mu.Lock()`. In `CompactAll`, `m.compactionMu.Lock()` is held before acquiring `m.Mu.Lock()`. In `Manager.Close`, `m.compactionMu.Lock()` is acquired before `m.Mu.Lock()`. In `enqueueFlushLocked`, `m.flushQueueMu.Lock()` is acquired while holding `m.Mu.Lock()`. No call path ever acquires locks in reverse order, and `m.flushQueueMu` is never held while requesting `m.Mu`. Verified by 66 passing tests on Windows and WSL2 `-race`.

---

## 2. SSTable Reader and File Lifetime Invariants

### INV-LIFETIME-1: Pin-Protected Reader Access
- **Statement**: An `SSTableReader` file descriptor shall never be closed or unlinked while any reader goroutine is executing I/O against it.
- **Current Status**: **Verified under Approach A (Sub-phase I1.3)**.
- **Evidence / Justification**: In Approach A, `Manager.Get` holds `m.Mu.RLock()` for the entire point lookup across `m.ActiveSSreaders`. Reader retirement in `CompactAll` and `Manager.Close` requires exclusive `m.Mu.Lock()`, which blocks until all concurrent point readers exit. Compactions are serialized by `m.compactionMu`. Verified by `TestReaderLifetime_CaseC_ReadDuringRetirement`.

### INV-LIFETIME-2: Safe Reader Retirement & Deletion
- **Statement**: An SSTable file shall only be unlinked from the filesystem after all active readers and iterators have released it and no new readers can discover it.
- **Current Status**: **Verified on Windows NTFS & Linux (Sub-phase I1.3.4)**.
- **Evidence / Justification**: `CompactAll` closes all SSTable iterators and retired reader file handles before invoking `os.Remove`. Verified by `TestFilesystem_CloseBeforeDelete`, with zero `ERROR_SHARING_VIOLATION` errors on Windows NTFS.

### INV-LIFETIME-3: Non-Destructive Compaction Publication
- **Statement**: Compaction publication must atomically retire only the subset of SSTables that participated in the compaction; it must never discard concurrently flushed SSTables.
- **Current Status**: **Verified (Sub-phases I1.2.2 & I1.3.6a)**.
- **Evidence / Justification**: Implemented in `replaceCompactedReaders` under `m.Mu.Lock()` with strict contiguous-prefix validation (`INV-COMPACT-1`) and non-nil replacement reader rejection (`D-010`). Any SSTables concurrently appended by the flush worker are preserved at their exact relative indices. Verified by `TestCompactionPublication_ConcurrentFlushOverlap` and `TestCompactionPublication_ValidReplacementSucceeds`.

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
- **Current Status**: **Verified and Guaranteed (Sub-phases I1.2.2, I1.3.0a, & I1.3.7c, Issues E-005 and E-008)**.
- **Evidence / Justification**: Point reads (`Manager.Get`) iterate backwards from `len-1` down to 0 and terminate on the first visible version. Guaranteed by: (1) dedicated managed FIFO flush worker (`m.flushChan`) ensuring older Memtables are built and appended before newer Memtables, and (2) `replaceCompactedReaders` contiguous-prefix replacement preserving chronological ordering of concurrently flushed readers. Verified by `TestFlushOrdering_Case1_TwoFlushesWithReversedTiming` through `Case6` and `TestCombinedValidation_CrossTierVersionAndTombstoneTransitions`.

### INV-COMPACT-1: Compaction Input Contiguity and Chronological Precedence
- **Statement**: Under the flat reader slice architecture (`m.ActiveSSreaders []*SSTableReader`) and first-matching-reader lookup (`Manager.Get`), SSTable compaction inputs must preserve the chronological precedence of all surviving readers. Compaction input selection must be strictly restricted to a contiguous prefix `[0..k]` of `m.ActiveSSreaders`. Noncontiguous input selection is strictly prohibited, as replacing noncontiguous inputs with a single merged table inverts relative reader order and causes older surviving intermediate readers to mask newer compacted updates (Issue E-005).
- **Scope Qualification**: This limitation applies specifically to this engine's flat-slice chronological search order where point lookups terminate on the first encountered match. It is not an impossibility theorem for LSM-trees in general; partitioned multi-level architectures (such as LevelDB/RocksDB L1+ levels) support arbitrary range compactions through disjoint key ranges or multi-way version-aware merges.
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
   - *Scope Qualification*: This lock hierarchy reduces lock inversion risks across the identified execution paths (`Transaction.Commit`, `CompactAll`, `Manager.Get`, `Manager.Close`), but does not mathematically eliminate all conceivable deadlocks across unanalyzed asynchronous routines.
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

### INV-LIFETIME-5: Coordinated Flush Shutdown Drain & FIFO Publication Ordering
- **Statement**: Memtables must be flushed and published into `m.ActiveSSreaders` strictly in chronological order of their creation ($F_0 \to F_1 \to F_2$), and engine shutdown (`Manager.Close`) must coordinate with all in-flight and queued flushes via a deadlock-free drain protocol (`EngineOpen -> EngineClosing -> EngineClosed`). Background flushes must never publish into a closed manager or leak file handles, and pending mutations must drain before final reader and WAL teardown.
- **Current Status**: **Adopted and Enforced (Sub-phases I1.3.7b, I1.3.7c, & I1.3.8b, Issues E-007, E-008, E-009)**.
- **Evidence / Justification**: Implemented in `lsmengine/manager.go` via dedicated FIFO worker, unbounded `flushQueue` slice with `flushQueueCond`, atomic `sstableFileSeq`, 4-phase deadlock-free drain protocol (`m.flushWg`), and post-close mutation rejection (`ErrClosed`). Verified by 9 shutdown tests (`flush_shutdown_test.go`), 6 ordering tests (`flush_ordering_test.go`), 5 flush admission tests (`flush_deadlock_repro_test.go`), and 6 combined validation tests (`combined_lifecycle_validation_test.go`) on Windows native and WSL2 with `-race` (zero data races).

### INV-QUEUE-1: Unbounded Flush Admission & Circular Wait Freedom
- **Statement**: Enqueueing a frozen Memtable under `Manager.Mu.Lock()` must never block on queue capacity or worker progress. The pending flush queue must decouple write admission from disk I/O latency, eliminating circular wait deadlocks with `Manager.Mu` (Issue E-009). The background flush worker must dequeue tasks in exact generation order, and pending Memtables must remain continuously queryable in `m.Frozenmem` until SSTable publication.
- **Current Status**: **Adopted and Enforced (Sub-phase I1.3.8b, Issue E-009)**.
- **Evidence / Justification**: Implemented in `lsmengine/manager.go` via `m.flushQueue []*Memtable`, `m.flushQueueMu sync.Mutex`, and `m.flushQueueCond *sync.Cond` (D-015). Enqueueing is an $O(1)$ non-blocking slice append under `m.flushQueueMu`. Worker acquires `m.Mu.Lock()` exclusively during SSTable publication and `Frozenmem` retirement. Verified by 5 comprehensive tests in `lsmengine/flush_deadlock_repro_test.go` on Windows native and WSL2 with `-race` (zero data races).

### INV-MUTABLE-1: Frozen Memtable Immutability
- **Statement**: Once an active Memtable is frozen and appended to `m.Frozenmem` during flush initiation, it is permanently sealed against further mutations. No goroutine or transaction may ever invoke `Put` on a frozen Memtable.
- **Current Status**: **Adopted and Verified (Sub-phase I1.4.1 & I1.4.2)**.
- **Evidence / Justification**: Audited in `research/design/memory_tier_concurrency_audit.md`. In `checkAndTriggerFlush()` and `TriggerFlush()`, `m.Mem` is reassigned to a new instance under `m.Mu.Lock()`. Point reads under `m.Mu.RLock()` and background worker iteration in `flushToDisk()` access frozen Memtables exclusively as concurrent readers of immutable data. Verified by 10 tests in `lsmengine/memtable_concurrency_test.go` on Windows and WSL2 `-race`.

### INV-HANDOFF-1: Zero-Loss Memory-to-Disk Handoff
- **Statement**: For every committed user key $K$, $K$ must remain continuously discoverable across the handoff from memory to disk: $\forall K, \; K \in \text{m.Frozenmem} \cup \text{m.ActiveSSreaders}$. There must be zero observable instant in time where a key transitioning from a frozen Memtable to an on-disk SSTable is unreachable by point reads.
- **Current Status**: **Adopted and Verified (Sub-phase I1.4.2 & I1.4.3)**.
- **Evidence / Justification**: In `flushToDisk()`, the newly opened `SSTableReader` is appended to `m.ActiveSSreaders` and the flushed `mem` is removed from `m.Frozenmem` within the exact same critical section under `m.Mu.Lock()`. Point readers holding `m.Mu.RLock()` always see either the pre-state (in `Frozenmem`) or the post-state (in `ActiveSSreaders`). Verified by `TestMemtable_Case9_FrozenToSSTableHandoffZeroLoss` (188,217 consecutive reads without a single missing value) and `TestCrossTierOrdering_ReferenceModelOracle` (104 validated points against reference oracle).


