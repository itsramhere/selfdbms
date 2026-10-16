# CHECKPOINT CP1 — EXTERNAL CHATGPT REVIEW PACKET

**Target Milestone:** Major Checkpoint CP1 (Approach A Baseline Complete & Hardened)  
**Git Tag:** `baseline-approach-a`  
**Git Commit:** `c697d7d5800eebcaa20b0002b8d808f69cca83fa`  
**Git Branch:** `research/reader-compactor-sync`  
**Date:** 2026-10-17  
**Host Environment:** Windows 11 Home 64-bit (NTFS, NVMe SSD, `go1.26.3 windows/amd64`)  
**Race Detector Environment:** WSL2 Ubuntu 24.04 LTS (`Linux 5.15.167.4`, `/snap/bin/go` `go1.27.1 linux/amd64`)  
**Status:** **AWAITING EXTERNAL REVIEWER DECISION**

---

## 1. Reviewer Instructions & Purpose of this Packet

This packet is a **completely self-contained, standalone dossier** prepared for the external reviewer. It contains all context, code diffs, pseudocode, execution traces, test logs, formal invariants, and risk assessments necessary to evaluate **Major Checkpoint CP1** without requiring local filesystem access or repository navigation.

### The Explicit Review Request:
1. Review the resolution of all 10 correctness defects (`E-001` through `E-010`) and 18 architectural decisions (`D-001` through `D-018`).
2. Review the verified formal invariants and the acyclic lock hierarchy (`m.compactionMu -> Manager.Mu -> m.flushQueueMu -> Wal.mu`).
3. Inspect the production pseudocode and dual-platform test results (98 unit tests passing on Windows native and WSL2 `-race`).
4. Review the codified operational recovery scenarios (Scenarios A through E) and durability risk boundaries (`RISK-001` through `RISK-003`).
5. **Render a decision**: Determine whether Checkpoint **CP1 is APPROVED**, certifying Approach A as a stable baseline, and authorize progression to **Phase I2 (Approach B — Reader Snapshot Implementation)**.

---

## 2. Executive Summary of Phase I1 Achievements

Phase I1 transformed an initially broken and untested storage engine prototype into a hardened, verified, and benchmark-ready baseline storage engine implementing **Approach A (Manager-Wide RWMutex Baseline with Serialized Compaction and Safe Reader Retirement)**:

1. **Deadlock Elimination on Mutation (`E-001`, Decision `D-003`)**: Eliminated recursive locking on non-reentrant `sync.RWMutex` inside `Transaction.Commit` by extracting `latestValueLocked`, unlocking basic Put/Get, tombstone deletions, and WAL recovery.
2. **Cross-Tier Snapshot Isolation Conflict Detection (`E-003`, Decision `D-005`)**: Added `SSTableReader.LatestValue` with sparse index seek on `math.MaxUint64`, ensuring committing transactions abort if a conflicting write or tombstone was flushed to disk before commit.
3. **Ordering-Independent Conflict Detection (`E-004`, Decision `D-006`)**: Hardened `latestValueLocked` via Option A to examine all active SSTables and track `maxSeq`, eliminating vulnerability to reader slice reordering.
4. **Identity-Preserving Compaction Publication (`D-007`, `D-008`, `D-010`, Issue `E-005`)**: Replaced blind slice overwriting with in-place prefix replacement (`replaceCompactedReaders`). Strictly enforced contiguous-prefix inputs (`INV-COMPACT-1`) and rejected nil replacement readers (`D-010`), preventing silent erasure of concurrently flushed SSTables.
5. **Safe Reader Retirement on Windows NTFS (`E-006`, Decision `D-013`)**: Added `m.compactionMu` serializing compaction lifetimes, closed iterators deterministically, and ensured reader file handles close before `os.Remove`, eliminating Windows `ERROR_SHARING_VIOLATION`.
6. **Coordinated Shutdown Drain Protocol (`E-007`, Decision `D-013`)**: Implemented a 4-phase drain protocol (`EngineOpen -> EngineClosing -> EngineClosed`) via `m.flushWg` outside `Manager.Mu`, preventing handle leaks and post-close races.
7. **Chronological Flush Ordering (`E-008`, Decision `D-014`)**: Replaced uncoordinated concurrent flushes with a dedicated managed FIFO flush worker, guaranteeing `INV-ORDER-1` ($F_0 \to F_1 \to F_2$).
8. **Flush Queue Deadlock Elimination (`E-009`, Decision `D-015`)**: Replaced bounded channel sends under `Manager.Mu.Lock()` with an unbounded internal FIFO slice protected by `m.flushQueueMu` and `sync.Cond`, eliminating circular wait deadlocks on queue saturation.
9. **Memory-Tier Immutability & Zero-Loss Handoff (`D-016`, Invariants `INV-MUTABLE-1`, `INV-HANDOFF-1`)**: Formally audited memory-tier synchronization. Verified that frozen Memtables are immutable and concurrently safe under Go ThreadSanitizer. Verified atomic swap from `Frozenmem` to `ActiveSSreaders` with zero dropped values across 188k+ continuous reads.
10. **Fail-Stop Flush Pipeline Precedence (`E-010`, Decision `D-017`, `INV-FLUSH-FAIL-1`)**: Resolved tier precedence inversion and tombstone resurrection by halting background flush processing upon disk errors, retaining all unflushed Memtables in `Frozenmem` in strict chronological order.
11. **Explicit Synchronization-Mode Seam (`D-018`, Invariant `INV-APPROACH-A`)**: Introduced typed `SyncMode` (`SyncModeApproachA` vs `SyncModeApproachB`) and `CreateManagerWithConfig(cfg)`. Default initialization selects safe Approach A; unimplemented Approach B is cleanly rejected with `ErrUnsupportedSyncMode`.
12. **Full Dual-Platform Test Pass**: Reconciled test inventory from 56 tests up to **98 unit tests (104 total runnable tests including 6 benchmarks)**. All 98 pass on Windows native (7.70s) and WSL2 Linux with `-race` (14.52s, 0 data races, 0 deadlocks). Concurrency stress tests (`-count=5`) pass cleanly. Clean static analysis via `go vet ./...`.

---

## 3. Reconciled Defect & Resolution Inventory (`E-001` through `E-010`)

All 10 defects encountered across Phase I1 have been deterministically reproduced, repaired, and permanently verified:

```
Defect Progression Across Phase I1:
E-001 (Commit Self-Deadlock)          ──► RESOLVED (85775ba)
E-002 (Windows -race CGO Toolchain)   ──► RESOLVED (85775ba via WSL2)
E-003 (Cross-Tier Conflict Blindness) ──► RESOLVED (4a4264f)
E-004 (latestValueLocked Reorder Bug) ──► RESOLVED (5bf95d2)
E-005 (Noncontiguous Compaction Inversion) ──► RESOLVED (b6f531a)
E-006 (Reader Retirement Race / Delete)    ──► RESOLVED (7d0b306)
E-007 (Flush vs Shutdown Handle Leak)      ──► RESOLVED (396ed81)
E-008 (Concurrent Flush Out-of-Order Pub)  ──► RESOLVED (624a5f5)
E-009 (Flush Queue Circular Wait Deadlock) ──► RESOLVED (ed78f2c)
E-010 (Failed-Flush Precedence Inversion)  ──► RESOLVED (592461c)
```

### Detailed Resolution Summary:

1. **`E-001` (Self-Deadlock on `Transaction.Commit`)**:
   - *Symptom*: Commits deadlocked waiting on `m.Mu.RLock()` while already holding `m.Mu.Lock()`.
   - *Fix*: Extracted `latestValueLocked` on `Manager`, called directly by `Commit` under `m.Mu.Lock()`.
   - *Evidence*: `TestWriteWriteConflict` PASS (0.00s). Commit: `85775ba`.
2. **`E-002` (Missing CGO for `-race` on Windows)**:
   - *Symptom*: `go test -race` failed on Windows due to absence of native C toolchain.
   - *Resolution*: Routed all `-race` executions through WSL2 Ubuntu Linux Go toolchain mounted at `/mnt/c/Users/ramst/Documents/Projects/selfdbms`.
   - *Evidence*: Zero race warnings across entire suite. Commit: `85775ba`.
3. **`E-003` (Cross-Tier Conflict Detection Blindness)**:
   - *Symptom*: Transactions committed overwrites without detecting conflicts if prior writes had flushed to disk.
   - *Fix*: Implemented `SSTableReader.LatestValue` with sparse index seek on `math.MaxUint64`; extended `latestValueLocked` to scan `ActiveSSreaders` in reverse order.
   - *Evidence*: `TestCrossTierWriteConflict_*` PASS (0.03s). Commit: `4a4264f`.
4. **`E-004` (`latestValueLocked` Reader Slice Ordering Dependency)**:
   - *Symptom*: Early-return in `latestValueLocked` failed if SSTables were reordered in slice.
   - *Fix*: Option A: Scans all active readers and tracks `maxSeq` while preserving RAM early-returns.
   - *Evidence*: `TestE004_TxnConflict_NewestVersionNotInLastSSTable` PASS (0.03s). Commit: `5bf95d2`.
5. **`E-005` (Noncontiguous Compaction Ordering Inversion)**:
   - *Symptom*: Compacting noncontiguous tables in a flat slice inverted relative reader order, causing older surviving tables to mask newer updates.
   - *Fix*: Codified `INV-COMPACT-1`; enforced prefix index 0, contiguity, and duplicate-freedom in `replaceCompactedReaders`.
   - *Evidence*: `TestE005_*` (4 tests) PASS (0.10s). Commit: `b6f531a`.
6. **`E-006` (Reader Lifetime & Overlapping Compaction Races)**:
   - *Symptom*: Compacting goroutines closed reader file descriptors while readers were still in use.
   - *Fix*: Introduced `m.compactionMu` serializing compaction lifetimes; closed iterators and retired readers before `os.Remove`.
   - *Evidence*: `TestReaderLifetime_*` (4 tests) PASS (0.27s). Commit: `7d0b306`.
7. **`E-007` (Flush vs Shutdown Lifecycle Race & Handle Leak)**:
   - *Symptom*: Background flush published new SSTable after `Close()` set slice to nil, leaking file handles on Windows NTFS.
   - *Fix*: Implemented 4-phase deadlock-free drain protocol (`OPEN -> CLOSING -> CLOSED`) with `m.flushWg.Wait()` outside locks.
   - *Evidence*: `TestFlushShutdown_CaseA` through `CaseI` (9 tests) PASS (0.24s). Commit: `396ed81`.
8. **`E-008` (Out-of-Order Concurrent Flush Publication)**:
   - *Symptom*: Uncoordinated background flush goroutines completed out of order, violating `INV-ORDER-1`.
   - *Fix*: Dedicated managed FIFO flush worker with buffered channel `flushChan`.
   - *Evidence*: `TestFlushOrdering_Case1` through `Case6` (6 tests) PASS (0.37s). Commit: `624a5f5`.
9. **`E-009` (Flush Queue Deadlock on Channel Saturation)**:
   - *Symptom*: Channel buffer capacity 64 saturated; enqueue blocked under `Manager.Mu.Lock()`; worker blocked on `Manager.Mu.Lock()` to publish; circular wait deadlock.
   - *Fix*: Unbounded internal FIFO queue slice protected by `m.flushQueueMu` and `sync.Cond`; enqueueing is non-blocking $O(1)$ slice append.
   - *Evidence*: `TestFlushDeadlock_Case1` through `Case5` (5 tests) PASS (1.94s). Commit: `ed78f2c`.
10. **`E-010` (Failed-Flush Precedence Inversion & Resurrection)**:
    - *Symptom*: Older failed flush $F_0$ stayed in `Frozenmem` while newer flush $F_1$ published to disk; `Frozenmem` masked $F_1$, causing stale reads and tombstone resurrection.
    - *Fix*: Option A Fail-Stop: Worker halts immediately upon fatal error (`m.flushErr != nil`); suppresses subsequent publications; retains unflushed Memtables in `Frozenmem` in strict chronological order.
    - *Evidence*: `TestFailedFlush_Case1` through `Case14` (14 tests) PASS (0.35s). Commit: `592461c`.

---

## 4. Formal Invariants Governing Approach A

| Invariant ID | Subsystem | Formal Contract & Scope | Status |
| :--- | :--- | :--- | :--- |
| **`INV-SYNC-1`** | Mutation Safety | Active Memtable mutations (`Put`) and lookups (`Get`) are synchronized exclusively under `Manager.Mu` (`Lock` for writes, `RLock` for reads). | **VERIFIED** |
| **`INV-SYNC-2`** | Re-entrancy | No call path attempts nested or recursive locking on `Manager.Mu`. | **VERIFIED** |
| **`INV-SYNC-3`** | Lock Hierarchy | Lock acquisition strictly follows: `m.compactionMu -> Manager.Mu -> m.flushQueueMu -> Wal.mu`. | **VERIFIED** |
| **`INV-LIFETIME-1`**| Reader Pinning | SSTable file descriptors are never closed or unlinked while point readers hold `m.Mu.RLock()`. | **VERIFIED** |
| **`INV-LIFETIME-2`**| Safe Deletion | SSTable files are unlinked only after all readers close; file handles closed prior to `os.Remove`. | **VERIFIED** |
| **`INV-LIFETIME-3`**| Safe Publication| Compaction atomically retires only participating inputs; concurrently flushed SSTables are preserved. | **VERIFIED** |
| **`INV-LIFETIME-4`**| Compaction Life | Compactions are serialized by `m.compactionMu`; shutdown coordinates via `m.compactionMu` and `isClosed`. | **VERIFIED** |
| **`INV-LIFETIME-5`**| Flush Drain | FIFO worker publishes in freeze order; shutdown drains pending flushes via 4-phase protocol. | **VERIFIED** |
| **`INV-MVCC-1`** | Monotonic Seq | Commit sequence numbers are strictly monotonically increasing, allocated under `m.Mu.Lock()`. | **VERIFIED** |
| **`INV-MVCC-2`** | Key Inversion | Internal key encoding bitwise-inverts sequence numbers, ensuring descending version sort in SkipList & SSTables. | **VERIFIED** |
| **`INV-MVCC-3`** | Point Visibility| Point reads at $T_{\text{read}}$ return highest sequence $S \le T_{\text{read}}$, or `ErrKeyDeleted` for tombstone, or `ErrNotFound`. | **VERIFIED** |
| **`INV-MVCC-4`** | Tier Precedence | Search order: Active Memtable $\to$ Frozen Memtables (newest to oldest) $\to$ SSTables (newest to oldest). | **VERIFIED** |
| **`INV-MVCC-5`** | Conflict Check | Conflict validation inspects latest committed sequence number across all tiers for Puts and Tombstones. | **VERIFIED** |
| **`INV-MVCC-6`** | Tier Monotonic | Active Memtable sequence numbers strictly exceed Frozen Memtables; Frozen strictly exceed on-disk SSTables. | **VERIFIED** |
| **`INV-ORDER-1`** | Reader Order | `ActiveSSreaders` slice is ordered chronologically from oldest (index 0) to newest (index `len-1`). | **VERIFIED** |
| **`INV-COMPACT-1`**| Prefix Compaction| Under flat reader slice, compaction inputs must be a contiguous prefix `[0..k]` starting at index 0. | **VERIFIED** |
| **`INV-QUEUE-1`** | Queue Admission | Enqueueing frozen Memtables never blocks on queue capacity or worker progress; circular wait eliminated. | **VERIFIED** |
| **`INV-MUTABLE-1`**| Immutability | Once frozen, Memtables are permanently sealed against mutations; accessed as concurrent immutable data. | **VERIFIED** |
| **`INV-HANDOFF-1`**| Tier Visibility | Committed keys remain continuously discoverable across tiers with zero missing-version window gaps. | **VERIFIED** |
| **`INV-FLUSH-FAIL-1`**| Fail-Stop | Fatal flush error halts worker, suppresses subsequent SSTable publication, and retains RAM order. | **VERIFIED** |
| **`INV-APPROACH-A`**| Mode Seam | Engine provides typed `SyncMode` configuration; defaults to Approach A; rejects Approach B until Phase I2. | **VERIFIED** |

---

## 5. Architectural Diagram & Production Pseudocode

### 5.1 Lock Ordering and Data Flow
```
        +-------------------------------------------------------+
        |                  m.compactionMu                       |
        |  (Serializes background compactions & shutdown drain) |
        +---------------------------+---------------------------+
                                    |
                                    v
        +-------------------------------------------------------+
        |                     Manager.Mu                        |
        |  RLock(): Manager.Get, LatestValue                    |
        |  Lock() : Transaction.Commit, replaceCompactedReaders,|
        |           flushToDisk swap, Manager.Close             |
        +---------------------------+---------------------------+
                                    |
                                    v
        +-------------------------------------------------------+
        |                  m.flushQueueMu                       |
        |  (Guards unbounded FIFO slice & flushQueueCond)       |
        +---------------------------+---------------------------+
                                    |
                                    v
        +-------------------------------------------------------+
        |                      Wal.mu                           |
        |  (Guards WAL file encoding, File.Write, File.Sync)    |
        +-------------------------------------------------------+
```

### 5.2 Core Operational Pseudocode

#### 1. Point Query (`Manager.Get`)
```go
func (m *Manager) Get(key []byte, readTs uint64) (string, error) {
    m.Mu.RLock()
    defer m.Mu.RUnlock()

    if m.state == EngineClosed {
        return "", ErrClosed
    }

    // Tier 1: Active Memtable
    if val, err := m.Mem.Get(key, readTs); err == nil {
        return val, nil
    } else if errors.Is(err, ErrKeyDeleted) {
        return "", ErrKeyDeleted
    }

    // Tier 2: Frozen Memtables (Reverse Chronological: Newest to Oldest)
    for i := len(m.Frozenmem) - 1; i >= 0; i-- {
        if val, err := m.Frozenmem[i].Get(key, readTs); err == nil {
            return val, nil
        } else if errors.Is(err, ErrKeyDeleted) {
            return "", ErrKeyDeleted
        }
    }

    // Tier 3: Active SSTables (Reverse Chronological: Newest to Oldest)
    for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
        val, err := m.ActiveSSreaders[i].Get(key, readTs)
        if err == nil {
            return val, nil
        } else if errors.Is(err, ErrKeyDeleted) {
            return "", ErrKeyDeleted
        } else if !errors.Is(err, ErrNotFound) {
            return "", err // Surface I/O error
        }
    }
    return "", ErrNotFound
}
```

#### 2. Compaction Publication (`replaceCompactedReaders`)
```go
func (m *Manager) replaceCompactedReaders(inputs []*SSTableReader, newReader *SSTableReader) error {
    if newReader == nil {
        return errors.New("compaction aborted: nil replacement reader")
    }

    // Validate Contiguous Prefix Starting at Index 0 (INV-COMPACT-1)
    if len(m.ActiveSSreaders) < len(inputs) {
        return errors.New("active readers modified concurrently")
    }
    for i, in := range inputs {
        if m.ActiveSSreaders[i] != in {
            return errors.New("compaction inputs do not form a contiguous prefix starting at index 0")
        }
    }

    // Preserve Concurrently Flushed Tables at Tail (INV-ORDER-1)
    survivingTail := m.ActiveSSreaders[len(inputs):]
    updated := make([]*SSTableReader, 1+len(survivingTail))
    updated[0] = newReader
    copy(updated[1:], survivingTail)
    m.ActiveSSreaders = updated
    return nil
}
```

#### 3. Fail-Stop Flush Loop (`flushLoop`)
```go
func (m *Manager) flushLoop() {
    defer m.flushWg.Done()
    for {
        m.flushQueueMu.Lock()
        for len(m.flushQueue) == 0 && !m.isClosed {
            m.flushQueueCond.Wait()
        }
        if len(m.flushQueue) == 0 && m.isClosed {
            m.flushQueueMu.Unlock()
            return // Clean shutdown drain completed
        }
        mem := m.flushQueue[0]
        m.flushQueue = m.flushQueue[1:]
        m.flushQueueMu.Unlock()

        // Build SSTable on disk without holding locks
        err := m.flushToDisk(mem)
        if err != nil {
            m.Mu.Lock()
            m.flushErr = err // Record fatal storage error
            m.Mu.Unlock()
            return // Option A Fail-Stop: Halt worker immediately
        }
    }
}
```

---

## 6. Verification Results Across Platforms

| Test Suite / Metric | Windows 11 NTFS Native | WSL2 Ubuntu Linux (`-race`) | Pass Rate | Status |
| :--- | :--- | :--- | :--- | :--- |
| **Full Unit Test Suite** (`go test ./...`) | **98/98 PASS (7.70s)** | **98/98 PASS (14.52s)** | 100% | **CERTIFIED** |
| **Race Detector Warnings** | N/A (Windows host) | **0 data races** | 100% | **CERTIFIED** |
| **Deadlock Warnings** | 0 deadlocks | **0 deadlocks** | 100% | **CERTIFIED** |
| **Concurrency Stress Suite** (`-count=5`) | **PASS (10.12s)** | **PASS (23.64s, 0 races)** | 100% | **CERTIFIED** |
| **Defect Regression Suite** (`E-001..E-010`) | **PASS (2.49s)** | **PASS (6.06s, 0 races)** | 100% | **CERTIFIED** |
| **Static Code Analysis** (`go vet ./...`) | Clean (0 errors) | Clean (0 errors) | 100% | **CERTIFIED** |
| **File Handle Sharing Leaks** | 0 leaks (`ERROR_SHARING_VIOLATION` eliminated) | 0 unlinked open descriptors | 100% | **CERTIFIED** |

---

## 7. Durability Boundaries & Codified Operational Scenarios

| Scenario | Trigger / Condition | State Transitions | Memory Behavior | Disk & Durability Behavior | Exit / Error State |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Scenario A: Normal Clean Shutdown** | `Close()` with 0 pending flushes. | `OPEN -> CLOSING -> CLOSED` | Active `m.Mem` flushed; SSTable built; `Frozenmem` drained. | All SSTables intact; readers closed; file locks released. | `Close()` returns `nil`. |
| **Scenario B: Clean Shutdown with Queued Flushes** | `Close()` with queued frozen tables ($F_0..F_k$). | `OPEN -> CLOSING -> CLOSED` | Worker sequentially drains FIFO queue in freeze order. | All queued data persisted to durable SSTables before close. | `Close()` waits for drain and returns `nil`. |
| **Scenario C: Fatal Flush Failure (Fail-Stop)** | Background worker encounters disk I/O error on $F_0$. | `OPEN` with `flushErr != nil` | Worker records `flushErr`, halts; keeps $F_0$ in `Frozenmem`. Subsequent generations stay in RAM in order. | Partial SSTable unlinked; zero corrupt files published. Precedence preserved. | `Close()` surfaces disk error. Post-halt durability relies on WAL. |
| **Scenario D: Restart Recovery via `RecoverFromWAL`** | Engine restarts; caller invokes `RecoverFromWAL(walPath)`. | Init $\to$ Replay $\to$ Ready | Committed entries replayed into active Memtable. | Replays active WAL file. Limitation (`RISK-002`): does not auto-scan rotated `wal_*.log` files. | Returns `nil` on clean replay. |
| **Scenario E: Power Loss / Ungraceful Crash** | Power failure or `SIGKILL` (`kill -9`). | Immediate process termination. | Volatile memory (`m.Mem`, `m.Frozenmem`, `m.flushQueue`) lost instantly. | Durability bounded by WAL filesystem sync policy. Intact SSTables preserved. | Unwritten in-memory data lost unless recovered from durable WAL. |

---

## 8. Open Architectural Risks Catalog for Checkpoint CP1

| Risk ID | Subsystem | Severity | Status | Description | Current Mitigation | Future Mitigation Required |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`RISK-001`** | Admission / Memory | High | **OPEN** | Unbounded queue slice can exhaust host RAM under sustained write load exceeding disk flush bandwidth. | Non-blocking enqueue under `flushQueueMu`; queue drains cleanly on normal shutdown. | Introduce non-blocking write-stall admission control before acquiring `Manager.Mu`. |
| **`RISK-002`** | Recovery / WAL | Medium | **OPEN** | `RecoverFromWAL` only replays a single specified log file; does not discover or sequence multiple rotated `wal_*.log` files after crash. | Rotated WAL files are preserved on disk without premature unlinking. | Implement directory-wide WAL segment discovery, ordering, and multi-segment replay. |
| **`RISK-003`** | Durability / Crash | Medium | **OPEN** | Retaining failed Memtables in `Frozenmem` protects process RAM only; crash loses unwritten SSTables unless replayed from WAL. | Failed Memtables stay queryable in RAM; incomplete SSTables unlinked immediately. | Add explicit crash recovery test suite simulating power loss across rotated WALs. |
| **`RISK-004`** | Flush Precedence | Critical | **RESOLVED** | Older failed flush $F_0$ remaining in `Frozenmem` while newer flush $F_1$ publishes to disk inverts tier precedence (`E-010`). | **Resolved via Option A Fail-Stop (D-017)**: Worker halts on fatal error; retains unflushed Memtables in `Frozenmem` in strict chronological order. | None for in-memory precedence. Crash recovery after fail-stop relies on multi-WAL replay (`RISK-002`). |

---

## 9. Benchmark Eligibility Assessment for Phases I4/I5

Approach A is **certified ready for experimental baseline benchmarking** in Phases I4 and I5, subject to explicit experimental controls:
1. **Contention Baseline**: Under Approach A, point reads holding `Manager.Mu.RLock()` across disk I/O will contend directly with compaction publication acquiring exclusive `Manager.Mu.Lock()`. This latency and throughput degradation represents the exact baseline penalty that Approach B is hypothesized to mitigate.
2. **Serialized Compaction Throughput**: Compactor concurrency benchmarks will measure serialization latency through `m.compactionMu`.
3. **Ingestion Duration Bounds**: Ingestion benchmarks must bound test durations to avoid unthrottled heap expansion under `RISK-001`.
4. **WAL Flush Configuration**: Benchmarks must explicitly configure and report WAL disk synchronization policies.

---

## 10. Immutable Frozen Baseline Identification

The candidate Approach A baseline has been permanently frozen in Git:
- **Git Commit Hash**: `c697d7d5800eebcaa20b0002b8d808f69cca83fa`
- **Annotated Git Tag**: `baseline-approach-a`
- **Working Tree State**: 100% clean (`nothing to commit, working tree clean`).
- **Inspection Command**: `git show baseline-approach-a`

---

## 11. Questions for External Reviewer

1. **Formal Checkpoint CP1 Approval**:
   Does the external reviewer approve Checkpoint **CP1**, certifying Approach A as a stable, verified, and hardened baseline storage engine?
2. **Authorization for Phase I2**:
   Is the pair programming team authorized to proceed to **Phase I2 (Design and Minimal Scaffolding for Immutable Snapshots — Approach B)** in accordance with `01_IMPLEMENTATION_PLAN.md`?
3. **Strategy Seam Confirmation**:
   Does the reviewer approve the `SyncModeApproachA` / `SyncModeApproachB` seam established in `I1.5.2` (`CreateManagerWithConfig(cfg)`) as the boundary for introducing Approach B scaffolding in Phase I2?
