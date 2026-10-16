# Checkpoint CP1.3a: Defensive Publication and Shutdown Verification

**Milestone:** Checkpoint CP1.3a  
**Sub-phase Executed:** I1.3.6 (I1.3.6a, I1.3.6b, I1.3.6c, I1.3.6d)  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Date:** 2026-10-16  
**Status:** BLOCKED — STOPPED FOR EXTERNAL REVIEWER (ChatGPT) DECISION  

---

## 1. Executive Summary

In Sub-phase I1.3.6, we executed the defensive publication and shutdown verification requested by the external reviewer.

### Key Outcomes:
1. **Compacted Output Validation (I1.3.6a — Verified)**:
   - Audited all production callers of `replaceCompactedReaders`.
   - Identified that the existing implementation conditionally appended `newReader` only if non-nil, which would silently delete all input SSTables from `m.ActiveSSreaders` if `newReader == nil` was passed.
   - Enforced strict rejection of `newReader == nil` in `replaceCompactedReaders` with error `"compaction aborted: nil replacement reader"`.
   - Verified that empty-output compaction (all tombstones) produces a valid 8-byte SSTable with magic number `0x4C534D31` and `len(Index) == 0`, returning `ErrNotFound` without crashing or producing a nil reader.
   - Added 4 deterministic regression tests in [`lsmengine/compaction_publication_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction_publication_test.go). All 8 publication tests PASS on Windows native and WSL2 with `-race`.
2. **Background Flush vs Shutdown Audit (I1.3.6b — Defect E-007 Discovered & Reproduced)**:
   - Traced `checkAndTriggerFlush`, `flushToDisk`, and `Manager.Close`.
   - Implemented deterministic test `TestFlushShutdown_InFlightFlushPublishesAfterClose` in [`lsmengine/flush_shutdown_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_shutdown_test.go).
   - Proved that an in-flight background flush publishes into `m.ActiveSSreaders` after `Manager.Close()` has set `ActiveSSreaders = nil` and `isClosed = true`.
   - On Windows NTFS, this leaks an open file handle, preventing file and directory deletion (`ERROR_SHARING_VIOLATION` / "Access is denied").
   - Recorded as **Issue E-007**. In accordance with reviewer instructions, execution was STOPPED before implementing an architectural correction.
3. **Flush Publication Ordering Audit (I1.3.6c — Defect E-008 Discovered & Reproduced)**:
   - Audited concurrent Memtable flushes under write load.
   - Implemented deterministic test `TestFlushOrdering_ConcurrentFlushesViolateOrder` in [`lsmengine/flush_ordering_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/flush_ordering_test.go).
   - Proved that if an older flush ($F_1$, seq 10) completes publication after a newer flush ($F_2$, seq 20), $F_1$ is appended after $F_2$ in `m.ActiveSSreaders`.
   - Because `Manager.Get` iterates backwards, it visits $F_1$ first and returns stale data `"v1_old"`, completely masking newer committed version `"v2_new"`.
   - This directly violates fundamental invariant `INV-ORDER-1`.
   - Recorded as **Issue E-008**. In accordance with reviewer instructions, execution was STOPPED before implementing an architectural correction.
4. **Correction of Research Documentation Claims (I1.3.6d — Completed)**:
   - Formally qualified the flat-slice compaction limitation (`INV-COMPACT-1`) to apply specifically to the chronological first-matching-reader lookup in flat slices, avoiding universal impossibility claims.
   - Qualified the lock hierarchy (`compactionMu -> Manager.Mu -> Wal.mu`) as reducing lock inversion risk across operational call graphs without claiming mathematical deadlock elimination for arbitrary asynchronous routines.
   - Documented that Approach A throughput and latency degradation are architectural expectations pending empirical benchmark quantification in I1.4.
   - Clarified the reader lifecycle diagram: point reads holding `m.Mu.RLock()` drain before the exclusive publication lock (`m.Mu.Lock()`) is granted.
   - Updated CP1.3 review packet commit hash to `40c9a3d` and confirmed clean working tree.
   - Documented the dual-toolchain environment: Go 1.26.3 on Windows native vs Go 1.27.1 in WSL2 Ubuntu snap (Decision D-011).

---

## 2. Invariant & Issue Matrix

### Invariant Status
| Invariant ID | Name | Core Mandate | Status |
| :--- | :--- | :--- | :--- |
| **`INV-ORDER-1`** | Chronological Reader Ordering | `ActiveSSreaders` must be chronologically ordered oldest to newest. | **VIOLATED by E-008 (Concurrent Flushes)** |
| **`INV-COMPACT-1`** | Compaction Input Contiguity | Compaction inputs must form a contiguous prefix starting at index 0. | **Verified (I1.3.0a & I1.3.6a)** |
| **`INV-LIFETIME-2`** | Safe Reader Retirement | Retired SSTables not closed/unlinked while referenced by reads. | **Verified (I1.3.2)** |
| **`INV-LIFETIME-4`** | Compaction Serialization & Shutdown | Compactions serialized via `compactionMu`; `Close()` coordinates. | **Verified (I1.3.3)** |
| **`INV-SYNC-3`** | Hierarchical Lock Order | Strict acquisition: `m.compactionMu` $\to$ `Manager.Mu` $\to$ `Wal.mu`. | **Verified** |
| **`INV-SHUTDOWN-1`** | Post-Shutdown Inactivity | No readers published into a closed manager; zero file handles leaked. | **VIOLATED by E-007 (Flush vs Close)** |

### Active Issues
| Issue ID | Severity | Problem Description | Current Status |
| :--- | :--- | :--- | :--- |
| **E-005** | P1 (Ordering) | Noncontiguous compaction input selection violates reader order. | **FIXED & VERIFIED** (Sub-phase I1.3.0a) |
| **E-006** | P1 (Lifecycle) | Concurrent compactions race; shutdown invalidates in-use readers. | **FIXED & VERIFIED** (Sub-phase I1.3.3) |
| **E-007** | P1 (Lifecycle) | In-flight background flush publishes reader after `Manager.Close()`. | **REPRODUCED & BLOCKED** (Awaiting Decision) |
| **E-008** | P0/P1 (Correctness) | Out-of-order concurrent flushes corrupt `INV-ORDER-1` reader ordering. | **REPRODUCED & BLOCKED** (Awaiting Decision) |

---

## 3. Detailed Audit Findings

### 3.1 I1.3.6a: Compacted Output Validation
- **Inspection of Callers**: `replaceCompactedReaders` is invoked exclusively by `CompactAll()` under `m.Mu.Lock()`.
- **Nil Replacement Risk**: Previously, `if newReader != nil { updated = append(updated, newReader) }` allowed a nil replacement to succeed, which stripped all inputs from `current` without adding any replacement reader. Subsequent cleanup closed and deleted the input SSTables from disk, permanently destroying committed data.
- **Empty Output Analysis**: In this LSM engine, `CompactAll()` always instantiates an `SSTableBuilder`. If all input records are tombstones, the loop skips `builder.Add()`, but `builder.Close()` writes an 8-byte file with `indexStartOffset = 0` and magic number `0x4C534D31`. `NewSSTableReader` parses this file, initializes an `*SSTableReader` with `Index: nil`, and point reads/conflict lookups immediately return `ErrNotFound`. Therefore, normal compaction never generates a nil reader.
- **Defensive Fix**: In `replaceCompactedReaders`:
  ```go
  if newReader == nil {
      return nil, errors.New("compaction aborted: nil replacement reader")
  }
  ```
- **Tests Added**:
  1. `TestCompactionPublication_NilReplacementRejected`: PASS
  2. `TestCompactionPublication_ValidReplacementSucceeds`: PASS
  3. `TestCompactionPublication_AbortedPublicationPreservesActiveReaders`: PASS
  4. `TestCompactionPublication_EmptyCompactionProducesValidReader`: PASS

### 3.2 I1.3.6b: Flush vs Manager.Close Audit (Defect E-007)
- **Call Paths**:
  ```
  checkAndTriggerFlush() -> go m.flushToDisk(frozen)
  Manager.Close() -> m.compactionMu.Lock() -> m.Mu.Lock() -> isClosed = true -> readers.Close() -> ActiveSSreaders = nil -> Unlock()
  flushToDisk() -> finishes SSTable -> m.Mu.Lock() -> appends reader to ActiveSSreaders -> Unlock()
  ```
- **Defect Symptoms**:
  1. Open reader published into closed Manager (`isClosed == true`, but `len(ActiveSSreaders) == 1`).
  2. Open OS file handle leaked on Windows NTFS, causing subsequent directory removal to fail.
  3. Potential data loss: `Manager.Close()` closes the WAL and returns without waiting for in-flight flushes; if the host process terminates, data in `Frozenmem` is lost.
- **Reproduction**: [`research/debug/E-007/repro.md`](../debug/E-007/repro.md), [`research/debug/E-007/failing_repro.log`](../debug/E-007/failing_repro.log).

### 3.3 I1.3.6c: Flush Publication Ordering Audit (Defect E-008)
- **Call Paths**:
  ```
  checkAndTriggerFlush() -> spawns goroutine 1 for F1 (seq 10)
  checkAndTriggerFlush() -> spawns goroutine 2 for F2 (seq 20)
  Goroutine 2 finishes first -> m.ActiveSSreaders = append(..., R2)
  Goroutine 1 finishes second -> m.ActiveSSreaders = append(..., R1)
  ActiveSSreaders becomes [R2, R1]
  Manager.Get("key1") searches from index len-1 down to 0:
    -> finds key1 in R1 (seq 10) and returns "v1_old"!
    -> key1 in R2 (seq 20) is masked!
  ```
- **Defect Symptoms**: Silent stale reads in `Manager.Get`, violating snapshot isolation and chronological ordering `INV-ORDER-1`.
- **Reproduction**: [`research/debug/E-008/repro.md`](../debug/E-008/repro.md), [`research/debug/E-008/failing_repro.log`](../debug/E-008/failing_repro.log).

### 3.4 I1.3.6d: Research Claims and Invariant Corrections
- **Flat-slice Compaction Scope**: In `research/design/invariants.md`, clarified that `INV-COMPACT-1` is an artifact of the single flat-slice reverse-search lookup design, not a general impossibility theorem for all LSM storage engines.
- **Lock Hierarchy Scope**: Clarified that while the hierarchy `compactionMu -> Manager.Mu -> Wal.mu` prevents lock inversion across audited engine methods, it does not mathematically eliminate all deadlocks in arbitrary concurrency models.
- **Approach A Latency Expectation**: Clarified in `research/design/architecture.md` that while Case C and D tests prove the blocking mechanism under `m.Mu.Lock()`, throughput and tail-latency percentiles remain to be quantified in I1.4.
- **Lifecycle Diagram Clarification**: Updated `research/design/reader_lifetime_protocol.md` to show that in-flight `RLock()` point reads drain *before* the exclusive `m.Mu.Lock()` publication lock is granted.
- **Toolchain Dual-Environment**: Documented that Windows native uses `go1.26.3 windows/amd64` while WSL2 Ubuntu uses Snap package `go1.27.1 linux/amd64`. Both comply with the Go 1 specification; race detector and test semantics are identical.

---

## 4. Test Evidence Summary

### Passing Test Suite (Windows Native & WSL2 Linux)
- **Windows 11 Native (`go1.26.3 windows/amd64`)**:
  - `go test -v -skip "TestFlushShutdown_|TestFlushOrdering_" ./lsmengine`
  - Result: **36 / 36 PASS** in 2.01s.
- **WSL2 Linux (`/snap/bin/go test -v -race`)**:
  - `wsl -e bash -l -c "... go test -v -race -skip 'TestFlushShutdown_|TestFlushOrdering_' ./lsmengine"`
  - Result: **36 / 36 PASS**, **0 data races** in 3.18s.
- **Go Vet**:
  - `go vet ./...`: Clean (exit code 0).

### Preserved Failing Counterexample / Reproduction Tests
1. **E-007**: `TestFlushShutdown_InFlightFlushPublishesAfterClose`
   - Windows Native: `FAIL` (exit code 1) — [`research/debug/E-007/failing_repro.log`](../debug/E-007/failing_repro.log)
   - WSL2 `-race`: `FAIL` (exit code 1) — [`research/debug/E-007/wsl_failing_repro.log`](../debug/E-007/wsl_failing_repro.log)
2. **E-008**: `TestFlushOrdering_ConcurrentFlushesViolateOrder`
   - Windows Native: `FAIL` (exit code 1) — [`research/debug/E-008/failing_repro.log`](../debug/E-008/failing_repro.log)
   - WSL2 `-race`: `FAIL` (exit code 1) — [`research/debug/E-008/wsl_failing_repro.log`](../debug/E-008/wsl_failing_repro.log)

---

## 5. Architectural Options for E-007 and E-008

Before proceeding to I1.4, the external reviewer's decision is required on how to resolve E-007 and E-008:

### Options for E-007 (Flush vs Shutdown):
- **Option 7A (Defensive Reader Drop)**:
  Under `m.Mu.Lock()`, check `if m.isClosed { reader.Close(); os.Remove(filename); return }`.
  *Trade-off*: Prevents handle leaks, but committed data in `Frozenmem` is discarded without reaching disk.
- **Option 7B (Coordinated Flush Drain via `sync.WaitGroup`)**:
  Add `m.flushWg sync.WaitGroup`. `checkAndTriggerFlush` adds to the wait group; `Manager.Close()` waits for all in-flight flushes to complete before closing readers and WAL.
  *Advantage*: Guarantees zero data loss for committed frozen Memtables and clean teardown without leaks.

### Options for E-008 (Concurrent Flush Ordering):
- **Option 8A (Serialized Flush Worker / Dedicated Mutex)**:
  Introduce `m.flushMu sync.Mutex` or a single background flush worker channel. Flushes are executed strictly in FIFO sequence.
  *Advantage*: Guarantees `INV-ORDER-1` trivially, bounds disk write concurrency, simple and robust.
- **Option 8B (Parallel Build with Sequence-Ordered Publication)**:
  Allow concurrent background SSTable construction, but buffer and insert completed readers into `m.ActiveSSreaders` sorted by Memtable creation sequence.
  *Complexity*: Requires buffering and sequence tracking logic.
- **Option 8C (Approach B Preview — Explicit Generation/Level Ordering)**:
  Defer multi-flush ordering to Approach B where tables carry explicit sequence metadata rather than relying on slice index.

---

## 6. Recommendation Regarding Readiness for I1.4

**RECOMMENDATION: BLOCKED.**

Proceeding to I1.4 (Empirical Benchmarking of Approach A) while background flushes can leak open file handles on shutdown (E-007) and concurrent flushes can corrupt reader search order (E-008) would invalidate multi-threaded benchmark results and cause test teardown failures on Windows NTFS.

**STOPPED at Checkpoint CP1.3a.** Awaiting external reviewer authorization and architectural decision on E-007 and E-008.
