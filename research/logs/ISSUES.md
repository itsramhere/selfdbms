# Issue and Anomaly Records (E-###)

This document tracks every bug, failure, unexpected behavior, race condition, leak, or test failure discovered during implementation and debugging.

Format:
`ID | severity | symptom | minimal reproduction | expected/actual | trace/log | root-cause hypothesis | experiments | actual root cause | fix | regression test | status | commit`

---

### E-001: Self-Deadlock on Transaction.Commit via Recursive RLock in Manager.LatestValue

- **ID**: `E-001`
- **Detected**: 2026-10-16, commit `08d3189`, baseline mode, Windows 11 amd64
- **Sub-phase**: I0.4
- **Severity**: Deadlock / Correctness (P0 blocker)
- **Symptom**: Every test or benchmark invoking `Transaction.Commit` hangs indefinitely and times out.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestBasicPutGet -timeout 5s
  ```
- **Expected / Actual**:
  - *Expected*: Transaction successfully commits key-value mutations to WAL and active Memtable.
  - *Actual*: Panics on test timeout with goroutine blocked in `sync.(*RWMutex).RLock()`.
- **Trace / Log**:
  ```
  goroutine 21 [sync.RWMutex.RLock]:
  sync.runtime_SemacquireRWMutexR(...)
  sync.(*RWMutex).RLock(...)
  owndbms/lsmengine.(*Manager).LatestValue(...)
      lsmengine/manager.go:120 +0x73
  owndbms/lsmengine.(*Transaction).Commit(...)
      lsmengine/transaction.go:60 +0xb4
  owndbms/lsmengine.TestBasicPutGet(...)
      lsmengine/engine_test.go:50 +0x216
  ```
- **Hypotheses Considered**:
  1. *Contention with background flush*: Rejected. Test is strictly single-threaded; no flush was triggered.
  2. *Recursive locking on non-reentrant sync.RWMutex*: Confirmed. `Transaction.Commit` acquires `t.Man.Mu.Lock()` on line 56, and then calls `t.Man.LatestValue()` on line 60, which attempts to acquire `m.Mu.RLock()`. In Go, `sync.RWMutex` is not re-entrant; an active writer attempting to acquire `RLock()` blocks indefinitely.
- **Fix**: Extracted internal helper `m.latestValueLocked(userKey)` in `lsmengine/manager.go` which assumes `m.Mu` is already held. Updated `Transaction.Commit` in `lsmengine/transaction.go` to call `latestValueLocked` directly. Public `LatestValue` continues to acquire `m.Mu.RLock()` and delegates to `latestValueLocked`.
- **Regression Test**: `TestBasicPutGet`, `TestDeleteAndTombstone`, `TestWALRecovery`, and newly added `TestWriteWriteConflict` in `lsmengine/engine_test.go`.
- **Validation**: 
  - Windows Native: `go test -v ./lsmengine` (5/5 PASS, 0.97s).
  - WSL2 Linux: `/snap/bin/go test -race -v ./lsmengine` (5/5 PASS, 0 race warnings, 1.46s).
- **Status**: Fixed (Sub-phase I1.1a).
- **Residual Risk**: `CompactAll` publication and file retirement logic remains uncoordinated (scheduled for future sub-phases I1.2/I1.3).
- **Evidence Path**: [`research/debug/E-001/after.log`](../debug/E-001/after.log), [`research/debug/E-001/diff.patch`](../debug/E-001/diff.patch), [`research/debug/E-001/repro.md`](../debug/E-001/repro.md)
- **Commit**: `85775ba`

---

### E-002: Missing CGO / C Toolchain for `go test -race` on Windows

- **ID**: `E-002`
- **Detected**: 2026-10-16, commit `08d3189`, Windows 11 amd64
- **Sub-phase**: I0.4
- **Severity**: Environment / Benchmark / Tooling
- **Symptom**: Executing `go test -race` fails immediately on Windows with `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
- **Minimal Reproduction**:
  ```powershell
  go test -race ./lsmengine
  ```
- **Expected / Actual**:
  - *Expected*: Tests execute with thread sanitizer instrumentation.
  - *Actual*: Tool fails because no C compiler (`gcc` or `clang`) is available in host Windows PATH.
- **Actual Root Cause**: Host Windows environment lacks a C compiler required by Go's CGO runtime for the race detector.
- **Resolution**: Resolved via Decision 3. WSL2 Ubuntu Linux environment provides Go 1.27.1 and GCC (`/usr/bin/gcc`), enabling native `-race` execution against the codebase mounted at `/mnt/c/Users/ramst/Documents/Projects/selfdbms` without altering the Windows host toolchain.
- **Validation**: `wsl bash -lc "cd /mnt/c/Users/ramst/Documents/Projects/selfdbms && /snap/bin/go test -race -v ./lsmengine"` executes with 0 race warnings.
- **Status**: Resolved (via WSL2 test execution).
- **Residual Risk**: Automated CI or scripts running purely in PowerShell on native Windows cannot run `-race` unless invoked through WSL2.
- **Commit**: `85775ba`

---

### E-003: Cross-Tier Write Conflict Detection Failure After Memtable Flush

- **ID**: `E-003`
- **Detected**: 2026-10-16, commit `4ca8787`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.1b
- **Severity**: Correctness / Data Integrity (P0 Blocker)
- **Symptom**: When a conflicting transaction's committed version (Put or Delete) is flushed from Memtable to an SSTable, concurrent uncommitted transactions fail to detect the write-write conflict and commit successfully, silently overwriting or resurrecting the key.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestCrossTierWriteConflict
  ```
- **Expected / Actual**:
  - *Expected*: Transaction aborts with `"Write-write conflict detected. Transaction aborted."`.
  - *Actual*: Transaction committed without detecting conflict with SSTable-resident version.
- **Trace / Log**:
  ```
  === RUN   TestCrossTierWriteConflict_FlushedSSTable
      cross_tier_conflict_test.go:72: DEFECT REPRODUCED: T1 committed without detecting conflict with SSTable-resident version!
  --- FAIL: TestCrossTierWriteConflict_FlushedSSTable (0.09s)
  ```
- **Hypotheses Considered**:
  1. *Compaction deduplication error*: Rejected. Compaction was not involved; issue occurs on single flush.
  2. *Missing tier inspection in `latestValueLocked`*: Confirmed. `latestValueLocked` only inspected `m.Mem` and `m.Frozenmem`, returning `ErrNotFound` as soon as the key reached an SSTable.
- **Actual Root Cause**: Conflict detection logic in `latestValueLocked` was not cross-tier; it omitted `m.ActiveSSreaders`.
- **Fix**: Implemented `LatestValue(userKey []byte) (uint64, error)` on `SSTableReader` using index seek `EncodeInternalKey(userKey, math.MaxUint64, true)`. Updated `latestValueLocked` to scan `m.ActiveSSreaders` in reverse order (`len(m.ActiveSSreaders)-1` down to 0) if absent from memory tiers.
- **Regression Test**: `TestCrossTierWriteConflict_FlushedSSTable`, `TestCrossTierWriteConflict_FlushedTombstone`, `TestCrossTierWriteConflict_NonConflicting` in `lsmengine/cross_tier_conflict_test.go`.
- **Validation**:
  - Windows Native: 8/8 tests PASS (1.27s).
  - WSL2 Linux: 8/8 tests PASS under `-race` with 0 race warnings (1.71s, repeated 3x).
- **Status**: Fixed (Sub-phase I1.1b).
- **Residual Risk**: If SSTables are merged by `CompactAll`, `CompactAll`'s publication path must preserve all active tables and not overwrite concurrently flushed tables (addressed in I1.2).
- **Evidence Path**: [`research/debug/E-003/before.log`](../debug/E-003/before.log), [`research/debug/E-003/after.log`](../debug/E-003/after.log), [`research/debug/E-003/diff.patch`](../debug/E-003/diff.patch), [`research/debug/E-003/repro.md`](../debug/E-003/repro.md)
- **Commit**: `4a4264f`

---

### E-004: `latestValueLocked` Fails to Identify Greatest Sequence Number when SSTable Ordering Changes

- **ID**: `E-004`
- **Detected**: 2026-10-16, commit `fbe9d81`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.2.0 (Invariant Verification)
- **Severity**: Correctness / Concurrency Hazard (P1)
- **Symptom**: If `ActiveSSreaders` contains SSTables whose order deviates from strict chronological sequence (e.g., during compaction publication if a compacted table is published out of place, or if multi-level SSTables are placed in arbitrary order), `latestValueLocked` returns the sequence number from the SSTable with the highest slice index instead of the greatest sequence number across all SSTables. A concurrent transaction with $ReadTs$ between the returned sequence number and the true committed sequence number will fail to detect the write-write conflict and commit, silently overwriting committed data.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestI1_2_0_SSTableOrderingAnomaly
  ```
- **Expected / Actual**:
  - *Expected*: Conflict detector returns greatest sequence number (50) regardless of SSTable slice ordering.
  - *Actual*: Conflict detector returns sequence number (10) from the table at the highest slice index (`m.ActiveSSreaders[len-1]`), failing to check older index positions.
- **Trace / Log**:
  ```
  === RUN   TestI1_2_0_SSTableOrderingAnomaly
      i1_2_0_invariants_test.go:341: FAIL: conflict detector returned seq 10 instead of greatest committed sequence 50 when SSTable ordering changed
  --- FAIL: TestI1_2_0_SSTableOrderingAnomaly (0.01s)
  FAIL
  FAIL	owndbms/lsmengine	0.929s
  FAIL
  ```
- **Hypotheses Considered**:
  1. *Early-return optimization in `latestValueLocked` assumes strict chronological ordering*: Confirmed. `latestValueLocked` returns on the first match (`return seq, nil`), which is valid ONLY if `ActiveSSreaders` is guaranteed to be chronologically ordered.
- **Fix**: Implemented Option A approved by reviewer: `latestValueLocked` now iterates across all `ActiveSSreaders` tracking `maxSeq` while preserving RAM monotonic early-returns (INV-MVCC-6).
- **Regression Tests**: `TestI1_2_0_SSTableOrderingAnomaly`, `TestE004_TxnConflict_NewestVersionNotInLastSSTable` in `lsmengine/i1_2_0_invariants_test.go`.
- **Validation**:
  - Windows Native: 16/16 tests PASS (1.26s).
  - WSL2 Linux: 16/16 tests PASS under `-race` with 0 race warnings (1.77s).
- **Status**: FIXED (Option A implemented).
- **Residual Risk**: Point-read `Manager.Get` still requires chronological ordering (`INV-ORDER-1`), which must be preserved during compaction publication in Sub-phase I1.2.2.
- **Evidence Path**: [`research/debug/E-004/repro.md`](../debug/E-004/repro.md), [`research/debug/E-004/before.log`](../debug/E-004/before.log), [`research/debug/E-004/after.log`](../debug/E-004/after.log).
- **Commit**: `5bf95d2`

---

### E-005: Noncontiguous Compaction Input Selection Violates Invariant `INV-ORDER-1`

- **ID**: `E-005`
- **Detected**: 2026-10-16, commit `6527d7f`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.3.0 (Reader Ownership & Ordering Audit)
- **Severity**: Architectural Invariant Hazard (P1)
- **Symptom**: If compaction selects noncontiguous input SSTables (e.g. $[R_0, R_2]$ skipping $R_1$), replacing them with a single merged table in the flat slice `m.ActiveSSreaders` violates chronological ordering `INV-ORDER-1`. As a result, older uncompacted versions in $R_1$ mask newer visible versions from $R_2$ in `Manager.Get`, returning stale data and violating snapshot isolation.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestCounterexample_NoncontiguousCompactionViolatesOrder
  ```
- **Expected / Actual**:
  - *Expected*: Point lookup returns latest visible version `"v2_new"` (seq 30).
  - *Actual*: Point lookup returns stale version `"v1_mid"` (seq 20) because $R_1$ sits after the merged table in the reverse search order.
- **Trace / Log**:
  ```
  === RUN   TestCounterexample_NoncontiguousCompactionViolatesOrder
      noncontiguous_compaction_counterexample_test.go:85: Result of Manager.Get after noncontiguous compaction: "v1_mid"
      noncontiguous_compaction_counterexample_test.go:88: COUNTEREXAMPLE PROVEN: Noncontiguous compaction resulted in stale read "v1_mid" (expected "v2_new"). r1 (seq 20) masked newer compacted version mCompact (seq 30) because mCompact was placed before r1 in the flat slice!
  --- FAIL: TestCounterexample_NoncontiguousCompactionViolatesOrder (0.02s)
  FAIL
  ```
- **Root Cause**: In a single flat slice architecture where `Manager.Get` iterates backwards, replacing noncontiguous inputs by inserting the merged table at the first input's position places the output before unselected intermediate tables, inverting relative version order.
- **Proposed Fix (Authorized D-008)**: Formally codify invariant `INV-COMPACT-1` (compaction inputs in a flat slice must be contiguous), and enforce in `replaceCompactedReaders` that `inputs` appear without duplicates, all are active, and form a strictly contiguous prefix starting at index 0 of `current`.
- **Status**: FIXED & VERIFIED.
- **Resolution**: Implemented defensive validation in `replaceCompactedReaders` (`lsmengine/compaction.go`). Codified `INV-COMPACT-1` in `research/design/invariants.md`. Added 4 passing regression tests covering noncontiguous rejection, non-prefix rejection, duplicate rejection, and valid prefix MVCC visibility across concurrent flushes (`lsmengine/noncontiguous_compaction_counterexample_test.go`). 24/24 tests PASS on Windows native (1.28s) and WSL2 with `-race` (2.39s) with 0 data races.
- **Evidence Path**: [`research/debug/E-005/repro.md`](../debug/E-005/repro.md), [`research/debug/E-005/failing_counterexample.log`](../debug/E-005/failing_counterexample.log), [`research/debug/E-005/after.log`](../debug/E-005/after.log), [`research/debug/E-005/diff.patch`](../debug/E-005/diff.patch), [`lsmengine/noncontiguous_compaction_counterexample_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/noncontiguous_compaction_counterexample_test.go).
- **Commit**: `3b2d0c1`

---

### E-006: Unsynchronized Compaction Lifetimes and Shutdown Reader Invalidation

- **ID**: `E-006`
- **Detected**: 2026-10-16, commit `2b434f7`, baseline Approach A, Windows 11 amd64
- **Sub-phase**: I1.3.2 (Deterministic Reader Lifetime Verification)
- **Severity**: Data Race / Lifecycle Hazard (P1)
- **Symptom**:
  1. Concurrent `CompactAll()` calls race on overlapping active reader sets outside `Manager.Mu`, causing one compaction to close and unlink SSTables that the other compaction is still reading, leading to file sharing violations on Windows and aborted/wasted compaction work.
  2. `Manager.Close()` closes reader file descriptors while an in-flight compaction is executing outside `Manager.Mu`. When compaction resumes, it publishes into a closed engine, double-closes readers, and leaves the new SSTable reader open and unclosed.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestReaderLifetime_CaseA
  go test -v ./lsmengine -run TestReaderLifetime_CaseB
  ```
- **Root Cause**: `Manager.Mu` protected only the initial reader snapshot and final slice swap in `CompactAll`, leaving the k-way merge and file retirement phases unprotected. `Manager.Close` held `m.Mu.Lock()` but lacked synchronization with in-flight background compactions.
- **Proposed Fix (Sub-phase I1.3.3)**: Introduce dedicated `m.compactionMu sync.Mutex` and `m.isClosed bool` in `Manager` following strict lock hierarchy `compactionMu -> Manager.Mu -> Wal.mu`. Serialize compaction lifetimes and coordinate with `Manager.Close`.
- **Status**: IDENTIFIED & REPRODUCED (I1.3.2) -> FIXED & VERIFIED (I1.3.3).
- **Evidence Path**: [`lsmengine/reader_lifetime_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/reader_lifetime_test.go).
- **Commit**: `f8eea7e`

---

### E-007: Background Flush Publication into Closed Manager & File Descriptor Leak

- **ID**: `E-007`
- **Detected**: 2026-10-16, baseline Approach A, Windows 11 amd64 & WSL2 Linux
- **Sub-phase**: I1.3.6b (Audit background flush versus Manager.Close)
- **Severity**: Lifecycle / Resource Leak / Shutdown Invariant (P1)
- **Symptom**: An in-flight background `flushToDisk` goroutine publishes a newly opened `SSTableReader` into `m.ActiveSSreaders` after `Manager.Close()` has completed. This leaves an unclosed file handle open on Windows NTFS (blocking cleanup) and corrupts the state of the closed manager. Committed data in `Frozenmem` can also be lost if shutdown terminates before background flush completes.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestFlushShutdown_InFlightFlushPublishesAfterClose
  ```
- **Expected / Actual**:
  - *Expected*: No readers published into a closed manager; all background flushes drained or safely cancelled before `Close()` returns; zero open file handles leaked.
  - *Actual*: Flush appends `reader` to `m.ActiveSSreaders` after `Close()` set `ActiveSSreaders = nil`; file handle remains open indefinitely.
- **Trace / Log**:
  ```
  === RUN   TestFlushShutdown_InFlightFlushPublishesAfterClose
      flush_shutdown_test.go:83: DEFECT E-007 REPRODUCED: In-flight flush published 1 reader(s) into closed Manager! Leaked reader: data_1792164148154987600.sst
  --- FAIL: TestFlushShutdown_InFlightFlushPublishesAfterClose (0.01s)
  ```
- **Root Cause**: `flushToDisk()` lacks an `isClosed` check under `m.Mu.Lock()`, and `Manager.Close()` lacks a drain/coordination mechanism (such as `sync.WaitGroup`) to ensure in-flight flushes complete before reader teardown.
- **Proposed Architectural Options**:
  - *Option A*: Defensive reader drop (`if m.isClosed { reader.Close(); os.Remove(...); return }`) in `flushToDisk`.
  - *Option B*: Coordinated flush drain via `m.flushWg sync.WaitGroup` in `Manager.Close()` ensuring zero data loss for committed frozen memtables.
- **Status**: FIXED & VERIFIED (Sub-phase I1.3.7b).
- **Resolution**: Implemented 4-phase deadlock-free drain protocol in `Manager.Close()` using `EngineState` (`EngineOpen`, `EngineClosing`, `EngineClosed`), `m.flushWg sync.WaitGroup`, and `m.flushErr error`. In-flight flushes are drained outside `m.Mu` without holding locks, published readers are safely closed on shutdown, and post-shutdown operations are rejected with `ErrClosed`. Verified by 6 deterministic tests (Cases A–F) in `lsmengine/flush_shutdown_test.go` on Windows native and WSL2 with `-race` (0 data races).
- **Evidence Path**: [`research/debug/E-007/repro.md`](../debug/E-007/repro.md), [`research/debug/E-007/failing_repro.log`](../debug/E-007/failing_repro.log), [`research/debug/E-007/wsl_failing_repro.log`](../debug/E-007/wsl_failing_repro.log), [`research/debug/E-007/after.log`](../debug/E-007/after.log), [`research/debug/E-007/wsl_after.log`](../debug/E-007/wsl_after.log).

---

### E-008: Concurrent Memtable Flushes Publish Out-of-Order, Corrupting Chronological Reader Order

- **ID**: `E-008`
- **Detected**: 2026-10-16, baseline Approach A, Windows 11 amd64 & WSL2 Linux
- **Sub-phase**: I1.3.6c (Verify flush publication ordering)
- **Severity**: Correctness / Ordering Invariant Violation / Stale Read (P0/P1)
- **Symptom**: When multiple Memtables flush concurrently, if an older flush ($F_1$) completes publication after a newer flush ($F_2$), $F_1$ is appended after $F_2$ in `m.ActiveSSreaders`. Because `Manager.Get()` iterates in reverse order assuming newer tables are at higher indices (`INV-ORDER-1`), it reads older data from $F_1$, masking newer committed data in $F_2$ and returning stale results.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestFlushOrdering_ConcurrentFlushesViolateOrder
  ```
- **Expected / Actual**:
  - *Expected*: `Manager.Get("key1", 100)` returns latest committed version `"v2_new"` (seq 20).
  - *Actual*: `Manager.Get` returns stale version `"v1_old"` (seq 10).
- **Trace / Log**:
  ```
  === RUN   TestFlushOrdering_ConcurrentFlushesViolateOrder
      flush_ordering_test.go:107: DEFECT E-008 REPRODUCED: Manager.Get returned stale value "v1_old" (expected "v2_new"). Out-of-order flushes corrupted reader chronological order!
  --- FAIL: TestFlushOrdering_ConcurrentFlushesViolateOrder (0.01s)
  ```
- **Root Cause**: Unsynchronized concurrent `flushToDisk` workers appending blindly to `m.ActiveSSreaders` without preserving creation sequence order.
- **Proposed Architectural Options**:
  - *Option 1*: Serialized flush worker or dedicated `flushMu sync.Mutex` enforcing FIFO flush completion.
  - *Option 2*: Multi-threaded flush builder with sequence-ordered publication queue.
  - *Option 3*: Dedicated managed FIFO flush worker with buffered channel (Selected).
- **Status**: FIXED & VERIFIED (Sub-phase I1.3.7c).
- **Resolution**: Implemented dedicated background FIFO flush worker with buffered `flushChan chan *Memtable` (D-014). Memtables are enqueued under `Manager.Mu` at the instant of freezing in exact chronological creation order. The single worker processes and publishes SSTables sequentially to `ActiveSSreaders` in strict FIFO order, guaranteeing `INV-ORDER-1`. Draining is seamlessly coordinated with `Manager.Close()` 4-phase drain protocol. Verified by 6 deterministic regression tests (reversed timing, 3+ queue drain, multi-version MVCC, tombstone precedence, interleaved compaction, concurrent point reads) in `lsmengine/flush_ordering_test.go` on Windows native (1.18s) and WSL2 with `-race` (2.50s, 0 data races).
- **Evidence Path**: [`research/debug/E-008/repro.md`](../debug/E-008/repro.md), [`research/debug/E-008/failing_repro.log`](../debug/E-008/failing_repro.log), [`research/debug/E-008/wsl_failing_repro.log`](../debug/E-008/wsl_failing_repro.log), [`research/debug/E-008/after.log`](../debug/E-008/after.log), [`research/debug/E-008/wsl_after.log`](../debug/E-008/wsl_after.log).

---

### E-009: Deadlock on Bounded Flush Queue Saturation via Circular Wait with Manager.Mu

- **ID**: `E-009`
- **Detected**: 2026-10-16, baseline Approach A, Windows 11 amd64 & WSL2 Linux
- **Sub-phase**: I1.3.8a (Flush Queue Deadlock and Lifecycle Hardening)
- **Severity**: Liveness / Deadlock / Engine Stall (P0 Blocker)
- **Symptom**: When write load triggers flushes faster than disk I/O can persist SSTables, 64 flush tasks saturate the bounded `flushChan` buffer (`make(chan *Memtable, 64)`). Any 65th flush task invokes `enqueueFlushLocked` while holding `m.Mu.Lock()`, which blocks on channel send (`m.flushChan <- mem`). The background flush worker completes its current SSTable ($F_0$) and attempts to acquire `m.Mu.Lock()` to publish the reader into `m.ActiveSSreaders`. This creates an inescapable circular wait: the producer holds `m.Mu` waiting for space in `flushChan`, while the worker holds no lock and waits for `m.Mu` to finish $F_0$ before dequeuing $F_1$. All subsequent engine operations hang indefinitely.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestFlushDeadlock_E009_BoundedQueueDeadlock
  ```
- **Expected / Actual**:
  - *Expected*: Flush admission never blocks while holding `Manager.Mu`, or applies backpressure outside lock boundaries; background worker publishes completed flushes without deadlock.
  - *Actual*: Producer blocks on `m.flushChan <- mem` inside `enqueueFlushLocked` holding `m.Mu.Lock()`; worker blocks on `m.Mu.Lock()` at `manager.go:396` in `flushToDisk`.
- **Trace / Log**:
  ```
  goroutine 9 [sync.Mutex.Lock]:
  sync.(*RWMutex).Lock(...)
  owndbms/lsmengine.(*Manager).flushToDisk(0x1052198b83c0, 0x10521984a490)
      manager.go:396 +0x3ef
  owndbms/lsmengine.(*Manager).flushLoop(0x1052198b83c0)
      manager.go:242 +0x75

  goroutine 18 [chan send]:
  owndbms/lsmengine.(*Manager).enqueueFlushLocked(...)
      manager.go:248
  owndbms/lsmengine.(*Manager).EnqueueFlush(0x1052198b83c0, 0x1052199204f0)
      manager.go:259 +0x97
  ```
- **Root Cause**: Holding exclusive mutex `m.Mu` while performing a blocking channel send to `m.flushChan`, whose consumer requires `m.Mu` to complete the task ahead of it.
- **Status**: FIXED & VERIFIED (Sub-phase I1.3.8b).
- **Resolution**: Replaced raw bounded channel send with unbounded internal FIFO queue slice guarded by dedicated `m.flushQueueMu sync.Mutex` and `m.flushQueueCond *sync.Cond` (D-015). `enqueueFlushLocked` appends to the queue slice under `m.flushQueueMu` instantaneously without blocking on queue capacity or worker progress, while holding `m.Mu.Lock()`. The single background worker dequeues tasks under `m.flushQueueMu`, builds SSTables without holding locks, and acquires `m.Mu.Lock()` only to publish into `m.ActiveSSreaders` and remove from `m.Frozenmem`. Strict lock hierarchy: `compactionMu -> Manager.Mu -> flushQueueMu -> Wal.mu`. Verified by 5 comprehensive tests (resolved 65-item deadlock, 128-item rapid saturation, concurrent publication with point reads, shutdown during saturated queue, and continuous key visibility across all lifecycle phases) in `lsmengine/flush_deadlock_repro_test.go` on Windows native (2.80s) and WSL2 with `-race` (12.79s, 0 data races).
- **Evidence Path**: [`research/debug/E-009/repro.md`](../debug/E-009/repro.md), [`research/debug/E-009/before.log`](../debug/E-009/before.log), [`research/debug/E-009/wsl_before.log`](../debug/E-009/wsl_before.log), [`research/debug/E-009/after.log`](../debug/E-009/after.log), [`research/debug/E-009/wsl_after.log`](../debug/E-009/wsl_after.log).
- **Regression Test**: `TestFlushDeadlock_E009_Resolved_NoDeadlockOnQueueSaturation`, `TestFlushAdmission_QueueSaturated128Items`, `TestFlushAdmission_WorkerPublicationDuringAdmission`, `TestFlushAdmission_ShutdownDuringSaturatedQueue`, `TestFlushAdmission_KeyVisibilityAcrossLifecycle` in `lsmengine/flush_deadlock_repro_test.go`.

---

### E-010: Flush Failure Precedence Inversion (RISK-004)

- **ID**: `E-010`
- **Detected**: 2026-10-17, baseline Approach A, Windows 11 amd64 & WSL2 Linux
- **Sub-phase**: I1.4.7b (Build a deterministic reproduction)
- **Severity**: Correctness / Data Integrity / Serializability (P0 Blocker)
- **Symptom**: When a background Memtable flush $F_0$ fails (e.g. disk full, I/O error), $F_0$ is retained in `m.Frozenmem`. The worker continues running and processes subsequent flushes $F_1, F_2$. If $F_1$ succeeds, it is published to `m.ActiveSSreaders` and removed from `m.Frozenmem`. Because `Manager.Get` and `Manager.latestValueLocked` inspect Tier 2 (`Frozenmem`) before Tier 3 (`ActiveSSreaders`), the older version in $F_0$ masks newer writes or tombstones in $F_1$, leading to stale reads, tombstone resurrection, and missed write-write conflicts.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestFailedFlush_E010
  ```
- **Expected / Actual**:
  - *Expected*: Reads return latest committed version across all tiers; write-write conflicts are detected against the latest committed version; failure halts out-of-order publication.
  - *Actual*: `Manager.Get` returns stale value `"old"` (expected `"new"`); returns resurrected value `"val_to_delete"` instead of `ErrKeyDeleted`; transaction commits write without detecting conflict against on-disk SSTable.
- **Trace / Log**:
  ```
  === RUN   TestFailedFlush_E010_StaleReadRepro
      failed_flush_precedence_test.go:121: DEFECT E-010 REPRODUCED: Manager.Get returned stale value "old" (expected "new"). Failed flush F0 in Frozenmem masked newer SSTable F1!
  --- FAIL: TestFailedFlush_E010_StaleReadRepro (0.02s)
  === RUN   TestFailedFlush_E010_TombstoneResurrectionRepro
      failed_flush_precedence_test.go:205: DEFECT E-010 REPRODUCED: Manager.Get returned resurrected value "val_to_delete" (error <nil>, expected ErrKeyDeleted key has been deleted). Older Put in failed flush F0 masked newer Tombstone in SSTable F1!
  --- FAIL: TestFailedFlush_E010_TombstoneResurrectionRepro (0.02s)
  === RUN   TestFailedFlush_E010_WriteConflictDetectionFailure
      failed_flush_precedence_test.go:574: DEFECT E-010 REPRODUCED: Transaction committed write on k8 without detecting write-write conflict against SSTable F1 (seq S2=2)! latestValueLocked returned seq S1=1 from failed flush F0 in Frozenmem.
  --- FAIL: TestFailedFlush_E010_WriteConflictDetectionFailure (0.03s)
  ```
- **Root Cause**:
  1. `flushLoop` does not stop upon an error in `flushToDisk`; it continues dequeuing subsequent flushes.
  2. Failed Memtables remain in `m.Frozenmem` indefinitely while successful newer Memtables are promoted to `m.ActiveSSreaders`.
  3. `Manager.Get` and `Manager.latestValueLocked` rely on a hardcoded tier hierarchy ($\text{Mem} \succ \text{Frozenmem} \succ \text{ActiveSSreaders}$) that breaks when generations are split across tiers out of chronological order.
- **Status**: FIXED & VERIFIED (Sub-phase I1.4.7c).
- **Resolution**: Implemented Option A (Fail-stop flush processing, D-017). When `flushToDisk` encounters any fatal error, it sets `m.flushErr` under `m.Mu.Lock()`. The single background worker `flushLoop` immediately observes `m.flushErr != nil` and halts further processing without dequeuing or attempting subsequent flushes. All unflushed Memtables remain safely in `m.Frozenmem` in strict chronological order ($[F_0, F_1, F_2, \dots]$), preventing out-of-order publication to `m.ActiveSSreaders`. Reverse iteration in `Manager.Get` and `Manager.latestValueLocked` guarantees newest versions are encountered first, eliminating stale reads, tombstone resurrection, and missed write-write conflicts. `TriggerFlush()`, `EnqueueFlush()`, and `checkAndTriggerFlush()` reject or suppress new flushes once `m.flushErr != nil`, and `FlushErr()` exposes the failure state directly. Verified by 14 deterministic tests in `lsmengine/failed_flush_precedence_test.go` on Windows native (1.33s) and WSL2 with `-race` (1.66s, 0 data races).
- **Evidence Path**: [`research/debug/E-010/repro.md`](../debug/E-010/repro.md), [`research/debug/E-010/before.log`](../debug/E-010/before.log), [`research/debug/E-010/wsl_before.log`](../debug/E-010/wsl_before.log), [`research/debug/E-010/after.log`](../debug/E-010/after.log), [`research/debug/E-010/wsl_after.log`](../debug/E-010/wsl_after.log), [`research/debug/E-010/diff.patch`](../debug/E-010/diff.patch).
- **Regression Tests**: `TestFailedFlush_E010_Resolved_StaleReadPrevented`, `TestFailedFlush_E010_Resolved_TombstoneResurrectionPrevented`, `TestFailedFlush_E010_Resolved_StaleDeletionMaskingPrevented`, `TestFailedFlush_E010_Resolved_ThreeGenerationsPreserved`, `TestFailedFlush_E010_Resolved_ActiveMemtablePrecedence`, `TestFailedFlush_E010_Resolved_NoPartialPublication`, `TestFailedFlush_E010_Resolved_WriteConflictDetection`, `TestFailedFlush_E010_Resolved_ErrorReportingOnClose`, `TestFailedFlush_Interaction_SubsequentQueuedFlushes`, `TestFailedFlush_Interaction_ConcurrentReads`, `TestFailedFlush_Interaction_ConcurrentCommits`, `TestFailedFlush_Interaction_Compaction`, `TestFailedFlush_Interaction_RepeatedShutdownCalls`, `TestFailedFlush_Interaction_MultipleFailedFlushes` in `lsmengine/failed_flush_precedence_test.go`.

---

### E-011: Silent Transaction Commit on Conflict-Validation Lookup I/O Failure

- **ID**: `E-011`
- **Detected**: 2026-10-17, baseline Approach A, Windows 11 amd64 & WSL2 Linux
- **Sub-phase**: CP1-V.3 (Audit conflict-lookup error propagation)
- **Severity**: Correctness / Data Integrity / Conflict Detection (P0 Blocker)
- **Symptom**: When `Transaction.Commit` validates write-write conflicts across keys in `t.WriteSet`, it invokes `t.Man.latestValueLocked([]byte(key))`. If `latestValueLocked` encounters an unexpected error (e.g. disk read error, corrupted SSTable block, closed file descriptor), it returns `(0, err)`. `Transaction.Commit` only checked `if err == nil && latestSeqNo > t.ReadTs`. When `err != nil`, this check evaluated to `false`, causing the loop to complete without aborting. The transaction proceeded to allocate a sequence number, write to WAL, insert into the active Memtable, and commit successfully, silently bypassing conflict validation when disk tiers could not be verified reliably.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError
  ```
- **Expected / Actual**:
  - *Expected*: Transaction aborts and propagates the unexpected I/O error (`conflict validation failed for key "audit_key": ...`); transaction does not commit when conflict validation cannot be completed reliably.
  - *Actual*: Transaction committed silently without error (`commitErr == nil`), bypassing conflict validation.
- **Trace / Log**:
  ```
  === RUN   TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError
      cp1_verification_test.go:133: Confirmed latestValueLocked returns unexpected error: read data_1792251113965457100_1.sst: file already closed
      cp1_verification_test.go:142: DEFECT CONFIRMED: Transaction.Commit succeeded silently despite unexpected I/O error during conflict validation!
  --- FAIL: TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError (0.03s)
  FAIL
  ```
- **Root Cause**: In `lsmengine/transaction.go:68`, `Transaction.Commit` did not check for unexpected non-nil errors from `latestValueLocked`. It only evaluated `if err == nil && latestSeqNo > t.ReadTs`. Any non-nil error from `latestValueLocked` (including I/O faults, file corruption, or unexpected read failures) bypassed the abort check and allowed the transaction to proceed to commit.
- **Status**: IDENTIFIED & REPRODUCED (Fix Pending in CP1-V.3).
- **Resolution**: Check `if err != nil && !errors.Is(err, ErrNotFound)` in `Transaction.Commit`. If true, release `t.Man.Mu.Unlock()`, call `t.Abort()`, and return formatted error `fmt.Errorf("conflict validation failed for key %q: %w", key, err)`. `ErrNotFound` continues to be treated as absence of previous versions.
- **Evidence Path**: [`research/debug/E-011/repro.md`](../debug/E-011/repro.md), [`research/debug/E-011/before.log`](../debug/E-011/before.log), [`research/debug/E-011/wsl_before.log`](../debug/E-011/wsl_before.log).
- **Regression Test**: `TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError` in `lsmengine/cp1_verification_test.go`.

---

### E-012: Swallowed SSTable Read Errors in getApproachB Masking Stale Older Versions

- **ID**: `E-012`
- **Detected**: 2026-10-18, commit `6dd8e3c`, Approach B mode
- **Sub-phase**: I3.7.1
- **Severity**: Correctness / Data Integrity / Error Masking (P1 Blocker)
- **Symptom**: In `getApproachB`, when iterating through constituent SSTable readers in reverse chronological order, any unexpected error (such as file read error, bad offset, or nil reader) was silently ignored because the loop continued searching older SSTables. If an older version of the key existed in an older SSTable, the engine returned that stale older version with `nil` error; if no older version existed, it returned `ErrNotFound`.
- **Minimal Reproduction**:
  ```powershell
  go test -v ./lsmengine -run TestApproachB_NilReaderInSnapshot_DoesNotFallBack
  ```
- **Expected / Actual**:
  - *Expected*: Engine halts search immediately and returns the unexpected physical read error or corrupted snapshot error; never returns stale older version or false `ErrNotFound`.
  - *Actual*: `getApproachB` skipped the corrupted/failing reader and returned the older version `"v1"` with `nil` error.
- **Trace / Log**:
  ```
  === RUN   TestApproachB_NilReaderInSnapshot_DoesNotFallBack
      sstable_read_error_test.go:215: DEFECT E-012 REPRODUCED: getApproachB swallowed nil reader in newer snapshot position and returned stale older value "v1"!
  --- FAIL: TestApproachB_NilReaderInSnapshot_DoesNotFallBack (0.04s)
  FAIL
  ```
- **Root Cause**: `getApproachB` in `lsmengine/snapshot.go` checked `sr == nil || sr.Reader == nil { continue }`, and in the read result check only checked `if err == nil` and `else if err == ErrKeyDeleted`, with all other outcomes falling through to `continue` to older SSTables.
- **Fix**: Implemented strict error classification under Decision D-031:
  - If `sr == nil || sr.Reader == nil`: immediately return `"", errors.New("corrupted snapshot: nil reader encountered")`.
  - If `err == nil`: return `string(val), nil`.
  - If `errors.Is(err, ErrKeyDeleted)`: return `" ", err`.
  - If `errors.Is(err, ErrNotFound)`: continue search to older SSTables.
  - All other errors: immediately propagate `"", err`.
- **Regression Test**: `TestApproachB_UnexpectedReadError_PropagatesError`, `TestApproachB_UnexpectedReadError_SingleSSTable`, and `TestApproachB_NilReaderInSnapshot_DoesNotFallBack` in `lsmengine/sstable_read_error_test.go`.
- **Validation**:
  - Windows Native: `go test -v ./lsmengine -run TestApproachB_UnexpectedReadError` (3/3 PASS, 1.92s).
  - WSL2 Linux: `/snap/bin/go test -race -v ./lsmengine -run TestApproachB_UnexpectedReadError` (3/3 PASS, 0 race warnings, 0.16s).
- **Status**: Fixed (Sub-phase I3.7.1, Decision D-031).
- **Evidence Path**: [`research/debug/E-012/before.log`](../debug/E-012/before.log), [`research/debug/E-012/after.log`](../debug/E-012/after.log), [`research/debug/E-012/diff.patch`](../debug/E-012/diff.patch), [`research/debug/E-012/repro.md`](../debug/E-012/repro.md).

---

### E-013: Asynchronous Cleanup WaitGroup Registration Ordering and ReleaseSnapshot Panic-Safety

- **ID**: `E-013`
- **Detected**: 2026-10-18, commit `494b586`, Approach B mode
- **Sub-phase**: CP3a-V.2
- **Severity**: Concurrency / Deadlock / Synchronization Risk (P2)
- **Symptom**:
  1. Under the standard Go `sync.WaitGroup` contract, positive `Add` calls when the counter is zero must strictly happen-before `Wait()`. Unconstrained asynchronous cleanup scheduling outside `m.snapshotMu` could theoretically interleave with `closeApproachB`'s final `cleanupWg.Wait()`.
  2. In `ReleaseSnapshot`, manual mutex unlock before `snap.ReleasePinLocked()` risked leaving `m.snapshotMu` permanently locked if `ReleasePinLocked()` panicked on pin underflow, causing subsequent `man.Close()` calls to deadlock.
- **Root Cause**:
  1. `scheduleAsyncCleanup` was previously invoked without checking if shutdown had already transitioned to `SnapshotPhaseClosed`.
  2. `ReleaseSnapshot` lacked a deferred mutex release to protect against panic recovery during pin underflow testing.
- **Fix**:
  1. Implemented `scheduleAsyncCleanupLocked()` strictly under `m.snapshotMu` with an explicit phase boundary guard: `if m.snapshotPhase == SnapshotPhaseClosed || m.state == EngineClosed { return }`.
  2. Ensured `m.cleanupWg.Add(1)` executes synchronously within the caller's `m.snapshotMu` critical section before spawning the cleanup goroutine, guaranteeing happens-before relationship with `closeApproachB`'s `cleanupWg.Wait()`.
  3. Restored panic-safe deferred mutex release in `ReleaseSnapshot`: `defer func() { m.snapshotMu.Unlock(); ... }()`.
- **Regression Test**:
  - `TestCleanup_InterleavedFinalPinReleaseAndConcurrentClose` in `lsmengine/cleanup_coordination_test.go` (PASS, 0.16s).
  - `TestSnapshotPinning_UnderflowPanic` in `lsmengine/snapshot_pinning_test.go` (PASS, 0.00s).
- **Validation**:
  - Windows Native: `go test -count=1 ./...` (169/169 unit tests PASS, 9.45s).
  - WSL2 Linux: `go test -race -count=1 ./...` (169/169 unit tests PASS, 0 races, 18.97s).
- **Status**: Fixed (Sub-phase CP3a-V.2, Decision D-034).

---

### E-014: Unsynchronized Read of Manager.state under snapshotMu in scheduleAsyncCleanupLocked

- **ID**: `E-014`
- **Detected**: 2026-10-18, commit `29c40c6`, Approach B mode
- **Sub-phase**: CP3a-V.5
- **Severity**: Data Race / Lock Hierarchy Contract Violation (P2)
- **Symptom**: In `scheduleAsyncCleanupLocked()`, the caller holds only `m.snapshotMu`. The function checked `if m.snapshotPhase == SnapshotPhaseClosed || m.state == EngineClosed`. Reading `m.state` while holding only `m.snapshotMu` violated the locking contract because `m.state` is guarded by `Manager.Mu`. When concurrent threads mutated `m.state` under `Manager.Mu.Lock()` (e.g., during `Close()` state transitions), ThreadSanitizer flagged a data race on `m.state`.
- **Minimal Reproduction**:
  ```powershell
  wsl bash -c "cd /mnt/c/Users/ramst/Documents/Projects/selfdbms && /snap/bin/go test -race -v -run TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup ./lsmengine"
  ```
- **Root Cause**: `m.state == EngineClosed` was an unnecessary cross-mutex inspection. `m.snapshotPhase` was established in Decision D-026 as the dedicated lifecycle enum guarded exclusively by `m.snapshotMu`.
- **Fix**: Remove `m.state == EngineClosed` from `scheduleAsyncCleanupLocked()`. `m.snapshotPhase == SnapshotPhaseClosed` is guarded exclusively by `m.snapshotMu` and serves as the sole, strictly synchronized authority for closing async cleanup admission (Decision D-035).
- **Regression Test**: `TestE014_UnsynchronizedStateRead_ScheduleAsyncCleanup` in `lsmengine/cleanup_coordination_test.go` (PASS, 0.03s under WSL2 `-race`).
- **Validation**:
  - Windows Native: `go test -count=1 ./...` (170/170 unit tests PASS, 10.10s).
  - WSL2 Linux: `go test -race -count=1 ./...` (170/170 unit tests PASS, 0 data races, 20.71s).
- **Status**: Fixed (Sub-phase CP3a-V.5, Decision D-035).
- **Evidence Path**: [`research/debug/E-014/wsl_before.log`](../debug/E-014/wsl_before.log), [`research/debug/E-014/wsl_after.log`](../debug/E-014/wsl_after.log), [`research/debug/E-014/diff.patch`](../debug/E-014/diff.patch), [`research/debug/E-014/repro.md`](../debug/E-014/repro.md).










