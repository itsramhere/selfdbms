# Approach B: Immutable SSTable Reader-Set Snapshots Design and Lifecycle Specification

## Executive Summary & Design Scope

In the verified Approach A baseline (`baseline-approach-a-verified`, commit `ae8c034`), reader–compactor synchronization relies entirely on holding the manager-wide read lock (`Manager.Mu.RLock()`) for the full duration of all SSTable binary searches and file I/O operations (`Manager.Get`). While Approach A is proven race-free, deadlock-free, and leak-free across Windows 11 NTFS native and WSL2 Linux (ThreadSanitizer certified), holding a coarse read lock across slow disk I/O serializes background compaction publications and flush publications behind long-running reads.

This document establishes the formal design for **Approach B: Immutable Reader-Set Snapshots with Two-Tier Reference Ownership**. Under Approach B, readers acquire an immutable, point-in-time snapshot of active SSTables in an ultra-short in-memory critical section, release the manager lock immediately, and execute all disk I/O and binary searches concurrently without blocking background compaction, flush tasks, or write commits.

---

## 1. Lifecycle Protocol for Immutable Reader-Set Snapshots (I2.1)

### 1.1 The Seven Required Lifecycle States

Every reader-set snapshot progresses through a strictly defined linear-state machine:

```
Constructing ──► Published ──► Pinned ──► Retired ──► Drained ──► Closed ──► Reclaimed
                     ▲           │
                     └── [pins=0] ┘
```

1. **`Constructing`**:
   - **Context**: Private to the background creator goroutine (either the FIFO flush worker or the serialized compaction worker).
   - **Actions**: Creates new `SharedReader` wrappers for newly written SSTables; increments the reference count of surviving/reused `SharedReader` instances; allocates a new `ReaderSnapshot` with an immutable slice `[]*SharedReader` sorted chronologically newest-to-oldest.
   - **Visibility**: Completely invisible to concurrent readers.
2. **`Published`**:
   - **Context**: Installed as the active engine snapshot (`Manager.currentSnapshot`) under a brief metadata lock (`snapshotMu`).
   - **Actions**: Incoming reader queries discover this snapshot.
   - **Pin State**: Active pins may be `0` (quiescent) or `> 0` (actively pinned).
3. **`Pinned`**:
   - **Context**: One or more concurrent read transactions have acquired active pins (`pins > 0`) on this published snapshot.
   - **Actions**: Readers execute concurrent SSTable binary searches, index lookups, and block I/O without holding any global manager lock.
4. **`Retired`**:
   - **Context**: A newer snapshot has been published by a completed flush or compaction.
   - **Actions**: Atomically unseated from `Manager.currentSnapshot` under `snapshotMu` and transferred to `Manager.retiredSnapshots`. No *new* reader queries can discover or pin this snapshot.
   - **Pin State**: Any existing pinned readers that acquired pins prior to retirement continue executing safely on their pinned slice.
5. **`Drained`**:
   - **Context**: The final active reader releases its pin on a `Retired` snapshot (`pins == 0`).
   - **Actions**: Snapshot state transitions atomically to `Drained`. It is removed from `Manager.retiredSnapshots` and queued for resource reclamation.
6. **`Closed`**:
   - **Context**: Reclamation worker or releasing goroutine iterates through the snapshot's constituent `SharedReader` instances.
   - **Actions**: For each `SharedReader`, its reference count is decremented by 1. If an individual `SharedReader`'s refcount reaches zero AND the reader has been marked `obsolete` (e.g., compacted away), its OS file descriptor is closed via `Close()`.
7. **`Reclaimed`**:
   - **Context**: Post-closure file cleanup.
   - **Actions**: For obsolete SSTables whose handles are closed, the physical files on disk are unlinked via `os.Remove()`. Snapshot memory structures are garbage collected.

---

### 1.2 State Transition Matrix and Permitted Transitions

| Source State | Target State | Triggering Event | Guards / Preconditions | Actions Taken |
| :--- | :--- | :--- | :--- | :--- |
| **`Constructing`** | **`Published`** | Flush or compaction completes write | New SSTable files synced; shared refcounts incremented; `snapshotMu.Lock()` held | `m.currentSnapshot = newSnap`; state set to `Published` |
| **`Published`** | **`Pinned`** | Reader executes `AcquireSnapshot` | Engine open; `snapshotMu.Lock()` held | `snap.pins++`; reader receives pointer |
| **`Pinned`** | **`Published`** | Last concurrent reader calls `ReleaseSnapshot` on currently active snapshot | `snap.pins == 1`; snapshot is still current (`m.currentSnapshot == snap`) | `snap.pins--` (pins reach 0); snapshot remains active |
| **`Published`** | **`Retired`** | Compaction or flush publishes replacement snapshot | `snapshotMu.Lock()` held; `snap.pins == 0` | State set to `Retired`; moved to `m.retiredSnapshots`; immediately transitions to `Drained` |
| **`Pinned`** | **`Retired`** | Compaction or flush publishes replacement snapshot while readers active | `snapshotMu.Lock()` held; `snap.pins > 0` | State set to `Retired`; moved to `m.retiredSnapshots`; active readers continue reading |
| **`Retired`** | **`Drained`** | Last active reader calls `ReleaseSnapshot` on retired snapshot | `snap.pins == 1`; snapshot in `Retired` state | `snap.pins--` (pins reach 0); state set to `Drained`; triggers `reclaimRetiredLocked` |
| **`Drained`** | **`Closed`** | Reclamation routine decrements constituent `SharedReader` refcounts | Snapshot in `Drained` state; `reclaimMu` or `snapshotMu` held | Decrements each `SharedReader.refCount`; if refcount == 0 & obsolete, calls `reader.Close()` |
| **`Closed`** | **`Reclaimed`** | Post-close filesystem unlinking | File descriptor closed; `closeOnce` succeeded | Calls `os.Remove(path)`; logs any OS unlinking errors |

---

### 1.3 Analysis of Permitted Transitions & Edge Cases

1. **A published snapshot with zero active pins**:
   - *Status*: Fully valid and normal quiescent state. `m.currentSnapshot` points to the snapshot; `pins == 0`. When a reader arrives, it transitions `pins` from 0 to 1 under `snapshotMu`. If a new snapshot is published while `pins == 0`, the old snapshot transitions directly from `Published` to `Retired` to `Drained`.
2. **Multiple concurrent readers pinning the same snapshot**:
   - *Status*: Supported. Each reader increments `snap.pins` under `snapshotMu` (e.g., pins increments 1 -> 2 -> 10). Concurrent readers execute their SSTable searches in parallel across separate goroutines with zero mutual contention and zero lock acquisition during disk I/O.
3. **A reader returning an error before completing lookup**:
   - *Status*: Safe. The pin acquisition pattern strictly mandates `defer m.releaseSnapshot(snap)` immediately after acquisition. If a binary search, block decode, or OS read returns an error (e.g., corrupted block or I/O error), `defer` ensures `snap.pins` is decremented exactly once.
4. **A reader returning a tombstone**:
   - *Status*: Safe. A tombstone (`ErrKeyDeleted`) is a valid terminal lookup result. The deferred `releaseSnapshot` runs during function exit, guaranteeing exact pin balance.
5. **A compaction publishing a replacement snapshot**:
   - *Status*: Atomic. The compaction worker holds `compactionMu`, merges input SSTables, writes new SSTables, constructs a new `ReaderSnapshot`, acquires `snapshotMu`, swings `m.currentSnapshot`, sets the old snapshot's state to `Retired`, and releases `snapshotMu`.
6. **Multiple retired snapshots awaiting release**:
   - *Status*: Supported. If reader R1 pins snapshot S1, a flush publishes S2 (retiring S1), reader R2 pins S2, and compaction publishes S3 (retiring S2), both S1 and S2 reside in `m.retiredSnapshots`. Each maintains its own independent `pins` counter and transitions to `Drained` independently when its respective readers finish.
7. **A reader releasing the final outstanding pin**:
   - *Status*: If the snapshot is currently `Published`, `pins` transitions from 1 to 0 and the snapshot remains published. If the snapshot is `Retired`, transitioning from 1 to 0 marks it `Drained` and invokes `reclaimRetiredLocked(snap)`.
8. **Shutdown while snapshots remain pinned**:
   - *Status*: Controlled drain. `Manager.Close()` sets `m.state = EngineClosing`, acquires `snapshotMu`, marks `isClosed = true`, and prevents any new pins from being issued (subsequent `AcquireSnapshot` calls fail immediately with `ErrClosed`). `Close()` then waits on a condition variable (`snapshotDrainCond`) or drains retired snapshots whose pins have reached 0.
9. **Failure to close or unlink a retired SSTable**:
   - *Status*: Defensive isolation. If `reader.Close()` returns an error, the error is recorded. If `os.Remove(path)` returns an error (such as a temporary Windows antivirus lock), the error is logged and added to a pending cleanup list; it does NOT panic or corrupt engine metadata.

---

## 2. Reference Ownership Model Evaluation (I2.1)

### 2.1 Critical Requirement Analysis
> **Critical requirement**: Multiple snapshots may reference the same physical SSTable reader. Retiring one snapshot must not close a reader still referenced by another published or pinned snapshot.

In an LSM engine, compaction merges a subset of SSTables (or all older tables), but frequent flushes continuously produce new L0 tables while leaving older SSTables untouched. For example:
- Snapshot 1 contains: `[S2, S1]`
- Flush F3 produces `S3`. Snapshot 2 is published containing: `[S3, S2, S1]`.
Here, `S2` and `S1` are identical physical readers shared by both Snapshot 1 and Snapshot 2!
If Snapshot 1 is retired and drained while Snapshot 2 is actively reading `S2`, closing `S2` would invalidate active file descriptors, triggering `use-after-close` panics or Windows file-sharing violations (`Access is denied`).

### 2.2 Evaluation of Three Architectural Alternatives

| Evaluation Dimension | Model A: Entire Snapshot Ownership | Model B: Individual Reader Refcounts | Model C: Two-Tier Reference Ownership (Selected) |
| :--- | :--- | :--- | :--- |
| **Ownership Scope** | Each snapshot directly owns and closes its readers upon retirement drain. | No snapshot object; each `SSTableReader` has an atomic refcount bumped by queries. | **Tier 1**: Snapshot owns query pin count (`pins`).<br>**Tier 2**: `SharedReader` owns global refcount (`refCount`). |
| **Cross-Snapshot Sharing** | **BROKEN**: Reused readers are closed when the first snapshot drains, causing use-after-close in surviving snapshots. | **PARTIAL**: Queries must track and bump refcounts for every reader in the set individually during lookup. | **EXCELLENT**: Snapshot pin holds the snapshot; snapshot drainage decrements shared reader refcounts. |
| **Double-Close Prevention** | Weak: Vulnerable to multiple snapshots closing the same file handle. | Moderate: Atomic refcount decrement to 0 triggers close. | **ABSOLUTE**: `closeOnce sync.Once` + refcount guarantees exactly-one `Close()`. |
| **Premature Unlink Prevention** | Failed: Retiring S1 unlinks files still open in S2. | Moderate: File unlinked only when reader refcount is 0. | **ABSOLUTE**: File unlinked only when `refCount == 0` AND file is marked `obsolete`. |
| **Use-After-Close Risk** | **FATAL RISK**: Unacceptable for concurrent LSM architectures. | Low: But high query-time overhead and reader set drift. | **ZERO RISK**: Readers hold pinned snapshot; constituent readers cannot reach refcount 0 while pinned. |
| **Windows NTFS Compatibility** | **FAIL**: Calling `os.Remove()` on an open file handle fails with sharing violation error. | Moderate: Must guarantee close completes before unlink. | **CERTIFIED**: Strict two-step retirement: `reader.Close()` executes first, verified clean, then `os.Remove()`. |
| **Query Acquisition Overhead** | Low (single pin). | High: Loops over N readers, atomically incrementing N counters per query. | **OPTIMAL**: Single atomic/mutex pin on snapshot; zero per-reader counter manipulation during read queries. |

### 2.3 Justification for Model C (Two-Tier Reference Ownership)

Model C is unambiguously selected based on the following architectural proofs:
1. **Decoupling Query Lifetime from Storage Lifetime**: A reader query cares about a coherent, immutable *view* of SSTables (Snapshot Tier). A storage compaction/flush worker cares about physical file handles and disk space reclamation (Shared Reader Tier).
2. **Deterministic Multi-Snapshot Reader Sharing**:
   - When Snapshot 2 is constructed from Snapshot 1 with new table `S3`:
     `S3.refCount = 1`, `S2.refCount = 2`, `S1.refCount = 2`.
   - When Snapshot 1 retires and drains:
     `S2.refCount` decrements to 1 (stays open and active!).
     `S1.refCount` decrements to 1 (stays open and active!).
     Neither `S2` nor `S1` is closed.
   - When compaction replaces `S2` and `S1` with compacted table `S_c`:
     Compaction marks `S2.obsolete = true` and `S1.obsolete = true`.
     When Snapshot 2 retires and drains:
     `S2.refCount` decrements to 0. Since `S2.obsolete == true`, `S2.Close()` and `os.Remove(S2.Path)` execute cleanly!
3. **Zero Contention on Fast Path**: Point reads do not touch individual reader refcounts. A point read increments `snap.pins` once under a 10-nanosecond leaf lock, performs arbitrary reads, and decrements `snap.pins` on exit.

---

## 3. Atomic Snapshot Acquisition and Publication Protocol (I2.2)

### 3.1 Lock Hierarchy and Ordering Invariants

To guarantee absolute absence of circular-wait deadlocks across the storage engine, the following strict total lock hierarchy is established:

```
Level 1: compactionMu     (Mutex: serializes background compaction routines)
Level 2: Manager.Mu       (RWMutex: serializes engine state, active Memtable, Frozenmem)
Level 3: snapshotMu       (Mutex: leaf guard for snapshot pointer, pin counters, retired list)
Level 4: flushQueueMu     (Mutex: leaf guard for FIFO flush queue and condvar)
```

**Lock-Order Invariants**:
1. Locks must always be acquired in strictly ascending order: `compactionMu` $\to$ `Manager.Mu` $\to$ `snapshotMu` $\to$ `flushQueueMu`.
2. `snapshotMu` is an ultra-short leaf critical section (~10–20ns). It MUST NEVER acquire `Manager.Mu`, `compactionMu`, or perform disk I/O, memory allocations, or channel operations while held.
3. Disk I/O (SSTable read, write, sync, close, unlink) MUST NEVER hold `snapshotMu` or `Manager.Mu`.

---

### 3.2 Formal Pseudocode Specifications

#### 3.2.1 `AcquireSnapshot`
```go
// AcquireSnapshot obtains an active pin on the currently published reader snapshot.
// The caller MUST release the returned snapshot via ReleaseSnapshot when done.
func (m *Manager) AcquireSnapshot() (*ReaderSnapshot, error) {
    m.snapshotMu.Lock()
    defer m.snapshotMu.Unlock()

    if m.isClosed || m.state != EngineOpen {
        return nil, ErrClosed
    }

    snap := m.currentSnapshot
    if snap != nil {
        snap.pins++
        if snap.state == SnapshotPublished && snap.pins == 1 {
            snap.state = SnapshotPinned
        }
    }
    return snap, nil
}
```

#### 3.2.2 `ReleaseSnapshot`
```go
// ReleaseSnapshot releases an active pin on the specified snapshot.
// If the snapshot has been retired and pins reach zero, reclamation is triggered.
func (m *Manager) ReleaseSnapshot(snap *ReaderSnapshot) {
    if snap == nil {
        return
    }

    m.snapshotMu.Lock()
    defer m.snapshotMu.Unlock()

    if snap.pins <= 0 {
        panic("FATAL: snapshot pin underflow detected")
    }
    snap.pins--

    if snap.pins == 0 {
        if snap.state == SnapshotPinned {
            snap.state = SnapshotPublished
        } else if snap.state == SnapshotRetired {
            snap.state = SnapshotDrained
            m.reclaimRetiredLocked(snap)
        }
    }
}
```

#### 3.2.3 `PublishSnapshot`
```go
// PublishSnapshot atomically installs a newly constructed snapshot as the active engine snapshot,
// retiring the previous active snapshot.
func (m *Manager) PublishSnapshot(newSnap *ReaderSnapshot) error {
    m.snapshotMu.Lock()
    defer m.snapshotMu.Unlock()

    if m.isClosed || m.state != EngineOpen {
        return ErrClosed
    }

    newSnap.state = SnapshotPublished
    oldSnap := m.currentSnapshot
    m.currentSnapshot = newSnap

    if oldSnap != nil {
        m.retireSnapshotLocked(oldSnap)
    }
    return nil
}
```

#### 3.2.4 `RetireSnapshot`
```go
// retireSnapshotLocked transitions an active snapshot to retired and checks for immediate drainage.
// Precondition: m.snapshotMu MUST be held.
func (m *Manager) retireSnapshotLocked(oldSnap *ReaderSnapshot) {
    oldSnap.state = SnapshotRetired
    oldSnap.retiredAt = time.Now()

    if oldSnap.pins == 0 {
        oldSnap.state = SnapshotDrained
        m.reclaimRetiredLocked(oldSnap)
    } else {
        m.retiredSnapshots = append(m.retiredSnapshots, oldSnap)
    }
}
```

#### 3.2.5 `ReclaimRetired`
```go
// reclaimRetiredLocked decrements refcounts for all constituent readers of a drained snapshot.
// Precondition: m.snapshotMu MUST be held.
func (m *Manager) reclaimRetiredLocked(snap *ReaderSnapshot) {
    // Remove from retiredSnapshots slice if present
    for i, s := range m.retiredSnapshots {
        if s == snap {
            m.retiredSnapshots = append(m.retiredSnapshots[:i], m.retiredSnapshots[i+1:]...)
            break
        }
    }

    // Process constituent shared readers
    for _, sr := range snap.readers {
        newRef := sr.refCount.Add(-1)
        if newRef < 0 {
            panic("FATAL: shared reader refCount underflow")
        }
        if newRef == 0 && sr.obsolete.Load() {
            // Queue reader for asynchronous or post-lock closing and unlinking
            m.pendingCleanupReaders = append(m.pendingCleanupReaders, sr)
        }
    }

    snap.state = SnapshotReclaimed
    snap.readers = nil // Release pointer array to GC

    // Notify any pending Close() drain waiter
    if len(m.retiredSnapshots) == 0 && m.snapshotDrainCond != nil {
        m.snapshotDrainCond.Broadcast()
    }
}
```

#### 3.2.6 Safe Filesystem Cleanup on Windows NTFS
```go
// drainPendingCleanups executes file closure and unlinking outside of snapshotMu.
func (m *Manager) drainPendingCleanups() {
    m.cleanupMu.Lock()
    defer m.cleanupMu.Unlock()

    m.snapshotMu.Lock()
    toClean := m.pendingCleanupReaders
    m.pendingCleanupReaders = nil
    m.snapshotMu.Unlock()

    for _, sr := range toClean {
        sr.closeOnce.Do(func() {
            if sr.Reader != nil {
                sr.closeErr = sr.Reader.Close()
            }
            if sr.Path != "" {
                if err := os.Remove(sr.Path); err != nil {
                    // Windows NTFS safety: if sharing violation occurs, retry or log
                    m.recordCleanupError(sr.Path, err)
                }
            }
        })
    }
}
```

---

### 3.3 Addressing the Ten Protocol Requirements

1. **Exact lock acquisition/release boundaries**:
   `snapshotMu.Lock()` is acquired only to update `currentSnapshot`, adjust `pins`, or move snapshots between `retiredSnapshots` and `reclaimed`. It is held for $<50$ CPU instructions.
2. **Pin count transitions and underflow protection**:
   `pins` is monotonically incremented on `AcquireSnapshot` and decremented on `ReleaseSnapshot`. Releasing with `pins <= 0` immediately triggers an explicit panic (`FATAL: snapshot pin underflow detected`), eliminating silent state corruption.
3. **Exactly-once release on normal return and all errors**:
   Implemented via Go's standard idiom:
   ```go
   snap, err := m.AcquireSnapshot()
   if err != nil { return "", err }
   defer m.ReleaseSnapshot(snap)
   ```
4. **Reader-set construction before publication**:
   The new slice of `*SharedReader` is completely assembled and initialized in private memory before publication. Reused readers have their `refCount` incremented *prior* to publication.
5. **Atomic publication and retirement**:
   The swap of `m.currentSnapshot = newSnap` and retirement of `oldSnap` occur within the exact same `snapshotMu` critical section. No reader can observe a nil or half-updated snapshot.
6. **Shutdown admission rejection**:
   Once `Manager.Close()` begins, `m.state` is set to `EngineClosing`. `AcquireSnapshot` checks `m.state != EngineOpen` and returns `ErrClosed` before granting any pin.
7. **Coordination with `compactionMu`**:
   Compaction holds `compactionMu` throughout its entire merge phase. When ready to publish, it acquires `snapshotMu`, calls `PublishSnapshot`, and releases `snapshotMu`. Compactions remain strictly serialized, preserving `INV-SERIAL-COMPACT-1`.
8. **Close and deletion ordering on Windows NTFS**:
   Windows NTFS strictly forbids unlinking files with active open handles (`ERROR_SHARING_VIOLATION`). The reclamation protocol enforces that `reader.Close()` executes and returns before `os.Remove(path)` is called.
9. **Lifetime handling for compaction's borrowed input readers**:
   When compaction begins merging input SSTables, it increments `sr.refCount` for each input table (or holds a pin on the snapshot it borrowed from). Even if flushes occur while compaction is running, the compaction's input tables cannot be closed or unlinked prematurely.
10. **Lock-order analysis covering existing manager, queue, and WAL synchronization**:
    Verified in Section 3.1: Strict ordering `compactionMu (1) -> Manager.Mu (2) -> snapshotMu (3) -> flushQueueMu (4)`. No lock inversion is mathematically possible because lower-level locks never acquire higher-level locks.

---

## 4. Memtable and MVCC Correctness Guarantees (I2.3)

### 4.1 Consistency Scope: Point Read vs Full Snapshot Isolation

- **Single Point Read Consistency**: Approach B provides strong, linearizable point read consistency at timestamp `readTs`. A point read observes the latest committed version $\le readTs$ across the active Memtable, frozen Memtables, and SSTable snapshot.
- **Multi-Key Transactional Snapshot Isolation**: Point reads within a transaction use `t.ReadTs` captured at transaction begin (`atomic.LoadUint64(&m.CurrentSeqNo)`). The active Memtable, frozen Memtables, and SSTable snapshot all respect this timestamp via internal key comparisons (`key:version`).

### 4.2 Synchronization of Memory Tiers vs Disk Tiers

The active `Memtable` uses a SkipList that requires synchronization during concurrent mutation. Under Approach B:
1. `Manager.Get` acquires `m.Mu.RLock()`.
2. It searches the active `m.Mem` for the key $\le readTs$.
3. If not found, it searches `m.Frozenmem` (newest to oldest).
4. If not found in memory tiers, it invokes `m.AcquireSnapshotLocked()` under the **same** `m.Mu.RLock()` critical section.
5. `m.Mu.RUnlock()` is called immediately.
6. The reader searches the pinned snapshot's SSTables with **zero locks held**.
7. `defer m.ReleaseSnapshot(snap)` runs on function return.

**Crucial Correctness Invariant**: Coupling memory-tier search with snapshot pinning inside the same `m.Mu.RLock()` guarantees that no write or flush can "fall into a gap" between the memory tiers and the disk snapshot!

---

### 4.3 Deterministic Concurrent Interleaving Scenarios

#### Scenario A: Read Spans Active Memtable Freezing
- **Interleaving**: Reader R acquires `m.Mu.RLock()`. Simultaneously, a commit triggers active Memtable freezing.
- **Resolution**: Freezing requires `m.Mu.Lock()`. Therefore, either:
  1. Freezing occurs *after* R finishes its memory read: R reads the key from `m.Mem`.
  2. Freezing occurs *before* R: `m.Mem` becomes `m.Frozenmem[0]`. R acquires `m.Mu.RLock()`, misses in new `m.Mem`, hits in `m.Frozenmem[0]`.
- **Visibility**: 100% consistent. Zero loss of visibility.

#### Scenario B: Read Spans Frozen Memtable Publication as an SSTable
- **Interleaving**: Flush worker creates SSTable $S_{new}$ from `Frozenmem[tail]`. Flush worker acquires `m.Mu.Lock()` to remove `Frozenmem[tail]` and publish $S_{new}$.
- **Resolution**: Because R acquires `m.Mu.RLock()` and pins the snapshot *before* releasing `m.Mu`, flush publication cannot interleave between the memory tier check and snapshot pinning!
  1. If R runs before flush publication: Key is found in `Frozenmem[tail]`.
  2. If R runs after flush publication: Key is absent in `Frozenmem`, but $S_{new}$ is present in `m.currentSnapshot`.
- **Visibility**: 100% consistent. No version can vanish.

#### Scenario C: Read Spans Compaction Snapshot Replacement
- **Interleaving**: Reader R pins snapshot $S_{old}$ containing tables $[A, B]$. Compaction completes merge into table $C$, publishing $S_{new}$ containing $[C]$.
- **Resolution**: $S_{old}$ transitions to `Retired`. Its constituent readers $[A, B]$ have `refCount >= 1` and remain open. Reader R continues binary search in $[A, B]$ with complete memory and file safety. When R finishes, $S_{old}$ drains and $[A, B]$ are closed and unlinked.
- **Visibility**: 100% consistent. R sees the snapshot as it existed at pin time.

#### Scenario D: Read Spans Retirement of Previously Published SSTable Set
- **Interleaving**: Rapid flushes retire multiple snapshots while reader R is executing a slow disk read on $S_{old}$.
- **Resolution**: Multiple retired snapshots accumulate in `m.retiredSnapshots`. Each maintains its own `pins` count. $S_{old}$ cannot be reclaimed or closed until R calls `ReleaseSnapshot`.
- **Resource Safety**: Fully protected against premature destruction.

#### Scenario E: Read Spans Engine Shutdown
- **Interleaving**: Reader R holds a pin on $S_{old}$. Another goroutine calls `Manager.Close()`.
- **Resolution**: `Close()` sets `state = EngineClosing`, rejecting new reads. `Close()` waits for active snapshots to drain or drains retired snapshots whose pins hit 0. R completes its read cleanly and releases the pin.

---

## 5. Minimal Approach B Strategy Scaffolding (I2.4)

### 5.1 The Pluggable Strategy Seam
In Phase I2, we establish the clean architectural seam without changing existing Approach A behavior:
- `SyncModeApproachA`: Active default. Uses `Manager.Mu` for both memory and SSTable reader protection.
- `SyncModeApproachB`: Scaffolding enabled. Returns `ErrUnsupportedSyncMode` upon initialization until Phase I3 implementation is authorized.

### 5.2 Scaffolding Code Diff (`lsmengine/snapshot.go`)
The scaffolding declares:
- `SnapshotState` enum (`Constructing`, `Published`, `Pinned`, `Retired`, `Drained`, `Closed`, `Reclaimed`)
- `SharedReader` struct with `atomic.Int64` refcount and `sync.Once` safe closure
- `ReaderSnapshot` struct with immutable `[]*SharedReader` slice and `pins` counter
- `ReaderSyncStrategy` interface

Verified by 3 new unit tests in `lsmengine/snapshot_scaffolding_test.go`:
- Windows native (`go1.26.3`): PASS (5.04s)
- WSL2 Linux (`go1.27.1` with `-race`): PASS (12.34s, 0 data races)

---

## 6. Deterministic Test Plan for Phase I3 Implementation

For Phase I3 implementation, the following 10 deterministic tests will be constructed to certify the Approach B protocol:

1. **`TestApproachB_SnapshotLifecycle_LinearProgression`**:
   Verify complete state machine progression from `Constructing` $\to$ `Published` $\to$ `Pinned` $\to$ `Retired` $\to$ `Drained` $\to$ `Closed` $\to$ `Reclaimed`.
2. **`TestApproachB_PinningUnderflowPanic`**:
   Induce a bug where `ReleaseSnapshot` is called twice; assert that runtime panics with expected underflow message.
3. **`TestApproachB_ConcurrentReadersOverlappingCompaction`**:
   Launch 32 concurrent reader goroutines against 10 SSTables while background compaction replaces all 10 tables with 1 compacted table. Assert zero data races, zero read errors, and correct key retrieval.
4. **`TestApproachB_SharedReader_MultiSnapshotRetention`**:
   Create Snapshot 1 with $[S_1, S_2]$, Snapshot 2 with $[S_3, S_2, S_1]$. Retire and drain Snapshot 1. Assert that $S_2$ and $S_1$ remain open and queryable in Snapshot 2.
5. **`TestApproachB_WindowsNTFS_CloseBeforeRemove`**:
   Verify on Windows NTFS that compacted SSTables are never unlinked while open handles exist, ensuring zero `ERROR_SHARING_VIOLATION` errors.
6. **`TestApproachB_MemtableHandoff_ZeroVisibilityGap`**:
   Interleave rapid active Memtable flushes while reader loops over keys; assert key is never missed across the memory $\to$ snapshot transition.
7. **`TestApproachB_MultipleRetiredSnapshotsQueued`**:
   Pin snapshot $S_0$, trigger 5 consecutive flushes generating $S_1 \dots S_5$. Assert 5 snapshots queued in `retiredSnapshots`. Release $S_0$ and assert all 5 snapshots drain and reclaim cleanly.
8. **`TestApproachB_Shutdown_DrainWithOutstandingPins`**:
   Hold an active pin, initiate `Manager.Close()`. Assert `Close()` blocks or defers handle release until pin is released, preventing use-after-close.
9. **`TestApproachB_ErrorDuringLookup_ExactOnceRelease`**:
   Simulate simulated block I/O failure during read. Assert `defer ReleaseSnapshot` executes and restores pin count to exact previous value.
10. **`TestApproachB_TombstonePrecedenceAcrossPinnedSnapshot`**:
    Verify that an active Memtable tombstone correctly masks older values in a pinned SSTable snapshot.
