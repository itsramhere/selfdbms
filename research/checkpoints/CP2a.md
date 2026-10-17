# Checkpoint CP2a — Hardened Immutable Snapshot Protocol & State Machines

## 1. Executive Summary & Milestone Certification

Checkpoint CP2a formally delivers the hardened architectural design and synchronization specification for **Approach B: Immutable SSTable Reader-Set Snapshots with Two-Tier Reference Ownership**, resolving all seven design-level correctness and synchronization gaps identified during external review of Checkpoint CP2.

### Summary of Completed Hardening Tasks (Sub-phase I2.6):
1. **Separate State Machines (I2.6.1)**:
   - Defined decoupled state machines:
     - Logical `ReaderSnapshot`: `Constructing` $\to$ `Published` $\to$ `Pinned` $\to$ `Retired` $\to$ `Drained` $\to$ `Reclaimed` (or `Aborted`).
     - Physical `SharedReader`: `Active` $\to$ `Obsolete` $\to$ `Closing` $\to$ `Closed` $\to$ `Unlinking` $\to$ `Reclaimed` (with `ClosedValid` on shutdown and `Failed` on I/O error).
   - Documented explicit reference ownership across current snapshots, pinned retired snapshots, constructing snapshots, compaction leases, and shutdown drains.
   - Resolved the race between reference release to zero, obsolete marking, and cleanup scheduling via a single leaf ownership lock (`m.snapshotMu`, Decision `D-024`).
2. **Coupled Atomic Freeze-to-Disk Publication (I2.6.2)**:
   - Serialized snapshot publication and `Frozenmem` removal within a single `Manager.Mu.Lock()` critical section (Decision `D-023`).
   - Established the exact linearization point: no reader can observe a state where a flushed mutation is absent in memory and not yet published on disk.
   - Designed a deterministic two-way barrier test proving continuous visibility (`INV-HANDOFF-1`).
3. **Authoritative Shutdown Locking & Pin Admission (I2.6.3)**:
   - Confirmed `Manager.Mu` as the sole authoritative lock for `m.state` and `m.isClosed`.
   - Introduced synchronized atomic flag `m.snapshotAdmissionClosed`, set under `Manager.Mu.Lock()` and `snapshotMu.Lock()` during shutdown, eliminating lock mismatches.
   - Enforced strict point-read admission: queries are admitted exclusively when `m.state == EngineOpen`.
   - Proved that `Close()` drops all locks while waiting for pins to drain, preventing circular wait with `ReleaseSnapshot`.
4. **Normal Runtime Reclamation & Windows NTFS File Safety (I2.6.4)**:
   - Replaced shutdown-contingent reclamation with immediate asynchronous runtime cleanup on the final pin release of retired snapshots.
   - Certified Windows NTFS safety: OS file descriptors are verified closed before calling `os.Remove(Path)`.
   - Established an explicit cleanup failure retry policy with exponential backoff.
5. **Reader Ordering & Empty-Snapshot Contracts (I2.6.5)**:
   - Codified chronological slice representation: oldest-to-newest (`index 0 = oldest`, `index len-1 = newest`), matching `ActiveSSreaders`.
   - Codified reverse chronological iteration in `Get` (`len-1 down to 0`), preserving `INV-ORDER-1` and compaction prefix rules (`INV-COMPACT-1`).
   - Codified empty database representation as an explicitly allocated non-nil slice `[]*SharedReader{}`, eliminating nil dereference risks.
6. **Corrected and Expanded Phase I3 Test Matrix (I2.6.6)**:
   - Corrected `TestApproachB_MultipleRetiredSnapshotsQueued` to hold pins on 5 distinct generations.
   - Expanded test plan from 10 to 15 deterministic concurrency specifications with named synchronization barriers.
7. **Scaffolding Refinements & Complete Lock Graph (I2.6.7)**:
   - Updated `lsmengine/snapshot.go` with `SharedReaderState` enum and atomic accessors (`State()`, `SetState()`, `Pins()`), preventing data races during state queries.
   - Codified full 6-level lock and wait hierarchy (`compactionMu -> Manager.Mu -> snapshotMu -> flushQueueMu -> Wal.mu` and `cleanupMu`).
   - Removed unsupported nanosecond estimates.

---

## 2. Invariant Inventory at Checkpoint CP2a

| Invariant ID | Domain | Statement Summary | Status at CP2a |
| :--- | :--- | :--- | :--- |
| `INV-ORDER-1` | Ordering | SSTable readers queried in reverse chronological order (newest to oldest). | **VERIFIED** |
| `INV-COMPACT-1` | Compaction | Compaction inputs must span a contiguous prefix starting at index 0. | **VERIFIED** |
| `INV-SERIAL-COMPACT-1`| Compaction | Only one background compaction may execute at any given time (`compactionMu`). | **VERIFIED** |
| `INV-FLUSH-FIFO-1` | Flush | Memtables enqueued and flushed strictly in chronological freeze order. | **VERIFIED** |
| `INV-FLUSH-FAIL-1` | Flush | Fail-stop flush processing: fatal error halts flush worker, preserving `Frozenmem` precedence. | **VERIFIED** |
| `INV-MUTABLE-1` | Memory Tier | Frozen Memtables are permanently sealed against mutations. | **VERIFIED** |
| `INV-HANDOFF-1` | Memory-to-Disk | Continuous key version visibility during memory-to-disk handoff. | **VERIFIED** |
| `INV-QUEUE-1` | Queue | Flush enqueueing is an $O(1)$ non-blocking slice append under `flushQueueMu`. | **VERIFIED** |
| `INV-LIFETIME-5` | Shutdown | 4-phase deadlock-free shutdown drain protocol (`Open -> Closing -> Closed`). | **VERIFIED** |
| `INV-APPROACH-A` | Mode Seam | Typed `SyncMode` configuration; defaults to Approach A; rejects Approach B until Phase I3. | **VERIFIED** |
| `INV-SNAP-1` | Approach B | Published reader snapshots are strictly immutable. | **FORMALIZED** |
| `INV-SNAP-2` | Approach B | Pre-release snapshot pinning: pin must be held before releasing metadata lock. | **FORMALIZED** |
| `INV-SNAP-3` | Approach B | Two-tier reader lifetime: physical file closed/unlinked only when `refCount == 0` & obsolete. | **FORMALIZED** |
| `INV-SNAP-4` | Approach B | Monotonic pin underflow protection: releasing with `pins <= 0` triggers fatal panic. | **FORMALIZED** |
| `INV-SNAP-5` | Approach B | Memory tier read coupled with disk snapshot pin under short `Manager.Mu.RLock()`. | **FORMALIZED** |
| `INV-SNAP-6` | Approach B | Obsolete marking post-commit only; two-phase cleanup; shutdown preserves valid files. | **FORMALIZED (I2.6)** |

Total Invariants: **29 invariants** (23 Approach A operational, 6 Approach B formalized).

---

## 3. Decision Records (D-020 through D-024)

- **`D-020`**: Two-Tier Reference Ownership for Reader Snapshots (Model C).
- **`D-021`**: Mutex-Protected Leaf Critical Section for Snapshot Pinning.
- **`D-022`**: Combined In-Memory Lookup and Disk Snapshot Pinning under `Manager.Mu.RLock()`.
- **`D-023`**: Authoritative Shutdown Locking & Coupled Freeze-to-Snapshot Publication (`Manager.Mu.Lock()` + `snapshotMu.Lock()`).
- **`D-024`**: Explicit Multi-State `SharedReader` Lifecycle and Two-Phase Cleanup with Retry Policy.

---

## 4. Dual-Platform Verification Status

- **Windows 11 NTFS native (`go1.26.3`)**: **106/106 unit tests PASS (6.75s)**, `go vet ./...` clean.
- **WSL2 Ubuntu Linux (`go1.27.1` with `-race`)**: **106/106 unit tests PASS (12.24s)**, **0 data races, 0 deadlocks**, `go vet ./...` clean.
- Total Test Inventory: **106 unit tests + 6 benchmarks = 112 runnable tests**.
- Working tree: clean on branch `research/approach-b-snapshots`.
