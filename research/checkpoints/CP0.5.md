# CHECKPOINT CP0.5 — Baseline Invariants & Transaction Commit Deadlock Repair

**Commit:** `4ca8787`  
**Branch:** `research/reader-compactor-sync`  
**Date:** `2026-10-16`  

---

### Completed Sub-phases
- **I1.1 — Specify baseline locking and lifecycle invariants**: Defined Approach A baseline locking and lifecycle protocol in [`research/design/invariants.md`](../design/invariants.md). Codified reader lock scope, manager/WAL lock hierarchy (`Manager.Mu -> Wal.mu`), non-reentrant internal helper rules, and future compaction publication invariant.
- **I1.1a — Restore Transaction Commit Correctness (E-001)**: Extracted unexported helper [`latestValueLocked`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L125) on `Manager` operating under caller-held mutex, eliminating recursive `RLock` on `Manager.Mu` inside [`Transaction.Commit`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L56). Added regression test [`TestWriteWriteConflict`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/engine_test.go#L131). Validated 5/5 unit tests pass on Windows native and in WSL2 Linux/amd64 under `go test -race` with zero data races.

---

### Changed Files + Diffstat
```text
 lsmengine/engine_test.go        | 40 ++++++++++++++++++++++++++++++++++++++++
 lsmengine/manager.go            |  4 ++++
 lsmengine/transaction.go        |  4 ++--
 research/debug/E-001/after.log  | 27 +++++++++++++++++++++++++++
 research/debug/E-001/diff.patch | 49 +++++++++++++++++++++++++++++++++++++++++++++++++
 research/debug/E-001/repro.md   | 19 +++++++++++++++++--
 research/debug/TEST_MATRIX.md   | 34 +++++++++++++++++-----------------
 research/design/architecture.md |  4 ++--
 research/design/invariants.md   | 23 +++++++++++++++++++++++
 research/logs/DECISIONS.md      |  4 +++-
 research/logs/ISSUES.md         | 17 ++++++++++++-----
 research/logs/WORKLOG.md        |  2 ++
 12 files changed, 198 insertions(+), 29 deletions(-)
```

---

### Architecture & Behavior Changes

#### Exact Functions and Files Changed
1. [`lsmengine/manager.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L119-L139):
   - Refactored `Manager.LatestValue(userKey []byte)` to acquire `m.Mu.RLock()`, delegate to `m.latestValueLocked(userKey)`, and release the lock.
   - Extracted unexported `Manager.latestValueLocked(userKey []byte) (uint64, error)` which traverses `m.Mem` and `m.Frozenmem` assuming `m.Mu` is already held by the caller.
2. [`lsmengine/transaction.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L56-L65):
   - In `Transaction.Commit()`, replaced `t.Man.LatestValue([]byte(key))` with `t.Man.latestValueLocked([]byte(key))` inside the write-set conflict loop.
3. [`lsmengine/engine_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/engine_test.go#L131-L168):
   - Added `TestWriteWriteConflict` to verify that concurrent transactions on the same key trigger transaction abort and error `"Write-write conflict detected. Transaction aborted."`.

#### Lock Acquisition Graph Before and After

```
BEFORE (DEADLOCK - E-001):
Transaction.Commit()
 └──► Manager.Mu.Lock() [Exclusive write lock held]
       └──► Manager.LatestValue()
             └──► Manager.Mu.RLock() [BLOCKED: Non-reentrant RWMutex waits for writer to unlock]
                   └──► DEADLOCK (Goroutine blocks itself indefinitely)

AFTER (SAFE - I1.1a):
Transaction.Commit()
 └──► Manager.Mu.Lock() [Exclusive write lock held]
       ├──► Manager.latestValueLocked() [NO LOCK ACQUISITION: Reads Mem and Frozenmem directly]
       ├──► Wal.Append()
       │     └──► Wal.mu.Lock() -> [Write & Sync] -> Wal.mu.Unlock()
       ├──► Memtable.Put()
       └──► Manager.Mu.Unlock()
```

#### Why the Deadlock No Longer Occurs
In Go runtime, `sync.RWMutex` maintains an internal reader count. Calling `Lock()` decrements the reader count by $2^{30}$. Any subsequent call to `RLock()` observes a negative reader count and suspends the caller goroutine via `runtime_SemacquireRWMutexR` until the writer calls `Unlock()`. Because the writer is the exact same goroutine, it can never reach `Unlock()`.

By extracting `latestValueLocked`, `Transaction.Commit` inspects `m.Mem` and `m.Frozenmem` directly under the `t.Man.Mu.Lock()` it already holds. No secondary mutex acquisition is attempted, adhering to invariant `INV-SYNC-2`.

---

### Decisions Made
- **D-002** (2026-10-16): Amended implementation plan to introduce sub-phase `I1.1a` for `E-001` repair; codified Approach A baseline invariants in `design/invariants.md`.
- **D-003** (2026-10-16): Extracted unexported `latestValueLocked` helper called directly by `Transaction.Commit`, preserving the public `LatestValue` API.
- **D-004** (2026-10-16): Utilized existing WSL2 Ubuntu environment (Linux amd64, Go 1.27.1, GCC) for automated `-race` detector verification without altering Windows host toolchain.

---

### Issues Recorded & Resolved
- **E-001** (Severity: Correctness / Deadlock, Status: **FIXED**): Recursive lock acquisition in `Transaction.Commit`. Verified fixed by deterministic execution of `TestBasicPutGet`, `TestDeleteAndTombstone`, `TestWALRecovery`, and `TestWriteWriteConflict`.
- **E-002** (Severity: Environment / Tooling, Status: **RESOLVED**): Missing CGO toolchain on native Windows. Resolved by executing `-race` tests under WSL2.

---

### Exact Commands Executed + Pass/Fail Counts

1. **Windows Native (`go test -v ./lsmengine`)**:
   - `TestSkipListLevelDistribution`: PASS (0.00s)
   - `TestBasicPutGet`: PASS (0.05s)
   - `TestDeleteAndTombstone`: PASS (0.01s)
   - `TestWALRecovery`: PASS (0.09s)
   - `TestWriteWriteConflict`: PASS (0.00s)
   - **Result**: 5 passed, 0 failed, 0 skipped (exit code 0).

2. **WSL2 Ubuntu Linux amd64 (`/snap/bin/go test -race -v ./lsmengine`)**:
   - `TestSkipListLevelDistribution`: PASS (0.00s)
   - `TestBasicPutGet`: PASS (0.08s)
   - `TestDeleteAndTombstone`: PASS (0.04s)
   - `TestWALRecovery`: PASS (0.28s)
   - `TestWriteWriteConflict`: PASS (0.02s)
   - **Result**: 5 passed, 0 failed, 0 skipped, **0 data race warnings** (exit code 0).

---

### Evidence Paths
- Repro & Fix Evidence: [`research/debug/E-001/repro.md`](../debug/E-001/repro.md)
- Failing Baseline Trace: [`research/debug/E-001/before.log`](../debug/E-001/before.log)
- Passing Post-Fix Output: [`research/debug/E-001/after.log`](../debug/E-001/after.log)
- Code Patch: [`research/debug/E-001/diff.patch`](../debug/E-001/diff.patch)
- Updated Test Matrix: [`research/debug/TEST_MATRIX.md`](../debug/TEST_MATRIX.md)
- Invariants & Protocol: [`research/design/invariants.md`](../design/invariants.md)
- Updated Architecture: [`research/design/architecture.md`](../design/architecture.md)

---

### Remaining Correctness Risks (Prior to I1.2)
1. **Compaction Publication Data Loss Hazard (Deferred to I1.2)**: `CompactAll` currently overwrites `m.ActiveSSreaders = []*SSTableReader{newReader}`. Any SSTable concurrently appended by `flushToDisk` during compaction is permanently lost.
2. **Unsafe Reader Deletion on Windows (Deferred to I1.2 / I1.3)**: `CompactAll` closes readers and immediately calls `os.Remove(r.File.Name())` outside mutex protection. On Windows, if open iterators or file handles remain, `os.Remove` fails.
3. **Untested Compactor Concurrency**: While `Transaction.Commit` now functions, `CompactAll` has zero unit test coverage and has not been executed concurrently with readers.

---

### Questions for External Review (ChatGPT)
1. **Scope of Conflict Detection Across Tiers**: Currently, `latestValueLocked` traverses `m.Mem` and `m.Frozenmem`. In a heavy write workload where a conflicting write commits and flushes to an SSTable before the concurrent transaction commits, should `latestValueLocked` eventually search `ActiveSSreaders` as well, or is checking memory tiers sufficient given Memtable retention semantics?
2. **Identity-Based Reader Set Publication (I1.2)**: For Sub-phase I1.2, do you approve computing `newReaders = append(filterOut(currentReaders, compactedInputSet), newCompactedReader)` under `m.Mu.Lock()` to prevent erasing concurrently flushed tables?
3. **Race Detection Workflow**: Does executing `go test -race` through the WSL2 bridge provide sufficient race validation for Approach A milestones, while maintaining all performance and latency benchmarking on native Windows NTFS?

---

### Next Planned Sub-phase (DO NOT START UNTIL APPROVED)
**Phase I1, Sub-phase I1.2 — Repair protected publication.**
- Ensure `CompactAll` and `flushToDisk` publish changes to `m.ActiveSSreaders` using identity-based set subtraction, preserving concurrently flushed SSTables.
- Construct deterministic tests for reader set replacement under concurrent reads.
