# 01 — A-min: Correctness-Preserving Minimal Synchronization Fix

**Status:** **IMPLEMENTED AND VALIDATED AT G1** (2026-10-28). The design below is the implemented design; the test plan in §9 has been executed with the results recorded in [13_G1_REVIEW_EVIDENCE.md](13_G1_REVIEW_EVIDENCE.md). §3 was corrected after measurement (see below).
**Scope:** a third synchronization arm for the LSM engine that keeps Approach A's single `sync.RWMutex` and removes only the property that causes reader–writer interference: the read lock being held across the SSTable scan.
**Non-goals:** new APIs, new dashboards, version chains, user-visible snapshot isolation, any change to the write path, WAL, flush, or compaction algorithm.

> **Platform note (Windows amendment).** A-min was validated on Windows 11 / NTFS, not on Linux. One claim in §3 of the original text was wrong and is corrected there: Go opens files with `FILE_SHARE_DELETE`, so unlinking an open file on NTFS may *succeed* rather than raising `ERROR_SHARING_VIOLATION`. The real hazard is silent descriptor reuse after `Close`. See [12](12_WINDOWS_EXECUTION_AMENDMENT.md) §4.1.

---

## 1. Minimality Contract

A-min is admissible for I7 only if it satisfies **all** of the following. These are review criteria, not aspirations.

| ID | Requirement | Verified by |
| :--- | :--- | :--- |
| M-1 | **Single structural change.** The only behavioural difference from A is *when* `m.Mu.RUnlock()` executes on the disk path, plus the lifetime bookkeeping that this makes necessary. | Diff audit (§10) |
| M-2 | **No new version object.** No immutable reader-set descriptor, no per-publication allocation, no retirement queue, no cleanup goroutine. | Code review |
| M-3 | **Same visibility semantics as A.** For every interleaving, A and A-min return identical `(value, error)` for the same `(key, readTs)` — including tombstones (`ErrKeyDeleted`), `" "` vs `"Key is Deleted"` return conventions, and MVCC masking. | Differential oracle (§9, T-13) |
| M-4 | **No use-after-close.** A reader that has captured a table pointer can never observe a closed or unlinked file descriptor. | Proof §6 |
| M-5 | **No leak.** Every lease is released on every return path; every retired table is closed and unlinked exactly once. | Tests T-08..T-11 |
| M-6 | **Shutdown-safe.** `Close()` cannot close a descriptor while a lease is outstanding. | Test T-12 |
| M-7 | **No steady-state allocation.** The lease scratch buffer is reused; A-min must not add per-read heap allocations. | Microbench §7, `-benchmem` |
| M-8 | **Zero change to arms A and B.** A-min is a third `SyncMode`; A and B code paths are byte-identical to the frozen revision. | Diff audit §10 |

---

## 2. What Approach A Does Today

Relevant frozen code (line numbers as of `f1a561a`):

```
lsmengine/manager.go:199  func (m *Manager) Get(key string, readTs uint64) (string, error)
  205   m.Mu.RLock()
  206   defer m.Mu.RUnlock()
  216   m.Mem.Get(...)                      // memory tier
  230   for ... m.Frozenmem[i].Get(...)     // frozen tier
  251   for i := len(m.ActiveSSreaders)-1; i >= 0; i-- {
  255       m.ActiveSSreaders[i].Get(...)   // <-- SSTable scan UNDER RLock
  269   }
```

The lock is held for the entire tiered lookup. The SSTable probe is not a block read: `SSTableReader.Get` ([sstableReader.go:83](../../../lsmengine/sstableReader.go)) binary-searches a sparse index and then **sequentially scans records from the located block to the end of the data region**, allocating two byte slices per record. Measured cost on the frozen platform: `p50 ≈ 10.8 ms` per point read at `r=1` in Workload A. The critical section therefore lasts **milliseconds**, and Go's writer-preferring `sync.RWMutex` turns that into writer starvation.

Retirement today is safe *because* of the lock: compaction takes `m.Mu.Lock()` ([compaction.go:216](../../../lsmengine/compaction.go)), which cannot be granted while any reader holds `RLock`; after the swap it closes and unlinks the old readers ([compaction.go:245–248](../../../lsmengine/compaction.go)). No reader can hold a pointer to a retired table at that instant.

**Therefore any change that releases `RLock` before the scan must supply its own lifetime guarantee.** That is the whole substance of A-min.

---

## 3. Failure Mode of the "Obvious" Fix

Removing `defer m.Mu.RUnlock()` and unlocking before the scan is **incorrect**, and the failure is silent:

```
T0  Reader R: RLock; copy ActiveSSreaders = [S1..S4]; RUnlock
T1  Compactor C: Lock; ActiveSSreaders = [S5]; Unlock
T2  Compactor C: S1.Close(); os.Remove(S1.Path)        // fd 7 closed, inode unlinked
T3  Reader R: S1.Get(...) -> pread(fd 7)               // EBADF, or fd 7 reused by
                                                       // an unrelated file -> wrong data
```

On Linux, `unlink` alone would be harmless (the open descriptor keeps the inode alive). The bug is `Close()`: `os.File.Close` on descriptor 7 while another goroutine is inside `ReadAt` on the same `*os.File` is a data race on the descriptor, and the runtime may hand descriptor 7 to a subsequent `open`.

**Correction (measured, 2026-10-28).** The original text claimed that on Windows the failure is louder because unlink of an open file raises `ERROR_SHARING_VIOLATION`. That is **not true for Go's own handles**: `os.Open` requests `FILE_SHARE_DELETE`, so `os.Remove` can succeed while a descriptor is still open (the file enters delete-pending state and the name disappears while the data survives). The failure mode on NTFS is therefore **silent**, not loud: a later `Close` recycles a descriptor number that a concurrent reader may still be using. `ERROR_SHARING_VIOLATION` can still appear when a *third party* (Defender, Search indexer) holds a handle, which is why reclamation retries the unlink.

**A lease is mandatory on both platforms.**

---

## 4. Data Structures

Two fields added to `SSTableReader` ([sstableReader.go:12](../../../lsmengine/sstableReader.go)):

```go
type SSTableReader struct {
    File          *os.File
    Index         []IndexEntry
    DataEndOffset int64

    // A-min lifetime bookkeeping (unused by Approach B, which owns SharedReader instead).
    inflight atomic.Int32   // number of in-flight readers that may touch this table
    retired  atomic.Bool     // true iff the table has been superseded and removed from the set
    freeOnce sync.Once       // exactly-once close+unlink
    freeErr  error           // first physical error observed during reclamation
}
```

Three additions to `Manager`:

```go
leasePool        sync.Pool   // reuse []*SSTableReader scratch buffers (M-7)
cleanupErrMu     sync.Mutex
cleanupErrs      []error     // reclamation errors surfaced by Close()
```

No version object, no snapshot chain, no refcounted wrapper, no background worker (M-2).

**Why not a single global reader counter + epoch-based reclamation?** It is Θ(1) per read, but (a) the counter is one contended cache line for every reader, (b) reclamation must wait for *global* quiescence, so under sustained churn retired descriptors and their descriptors accumulate (fd growth), and (c) it needs a grace-period mechanism, which is more machinery than the leases it replaces. Per-table leases are strictly more local, and their cache lines are distinct.

---

## 5. Read Protocol

The invariant that makes A-min correct is: **the entire read set is captured and leased inside one `RLock` window; nothing is captured afterwards.**

```go
func (m *Manager) getApproachAMin(key string, readTs uint64) (string, error) {
    byteKey := []byte(key)

    // ---- one RLock window: memory tiers + read-set capture + leases ----
    m.Mu.RLock()
    if m.state != EngineOpen {
        m.Mu.RUnlock()
        return "", ErrClosed
    }
    if m.duringMemtableReadHook != nil { m.duringMemtableReadHook() }

    // T1: active Memtable (identical to A)
    sol, err := m.Mem.Get(byteKey, readTs)
    if err == nil { m.memtablePointReads.Add(1); m.Mu.RUnlock(); return string(sol), nil }
    if errors.Is(err, ErrKeyDeleted) { m.memtablePointReads.Add(1); m.Mu.RUnlock(); return "Key is Deleted", err }
    if !errors.Is(err, ErrNotFound) { m.Mu.RUnlock(); return "", err }

    // T2: frozen Memtables, newest first (identical to A)
    for i := len(m.Frozenmem) - 1; i >= 0; i-- {
        val, err := m.Frozenmem[i].Get(byteKey, readTs)
        if err == nil { m.memtablePointReads.Add(1); m.Mu.RUnlock(); return string(val), nil }
        if errors.Is(err, ErrKeyDeleted) { m.memtablePointReads.Add(1); m.Mu.RUnlock(); return " ", err }
        if !errors.Is(err, ErrNotFound) { m.Mu.RUnlock(); return "", err }
    }

    // CAPTURE + LEASE (the only new work inside the critical section)
    n := len(m.ActiveSSreaders)
    if n == 0 { m.Mu.RUnlock(); return " ", ErrNotFound }
    buf := m.acquireScratch(n)              // []*SSTableReader from sync.Pool
    copy(buf, m.ActiveSSreaders)
    for i := 0; i < n; i++ { buf[i].inflight.Add(1) }   // BEFORE RUnlock (invariant I-2)
    m.Mu.RUnlock()

    // from here on: zero manager locks held; every table in buf is leased
    defer func() {
        for i := 0; i < n; i++ { buf[i].release(m) }     // may trigger reclamation
        m.releaseScratch(buf[:0])
    }()

    if m.duringPointReadHook != nil { m.duringPointReadHook() }

    // T3: SSTables, newest first (identical logic to A, no locks)
    for i := n - 1; i >= 0; i-- {
        val, err := buf[i].Get(byteKey, readTs)
        if err == nil { m.sstablePointReads.Add(1); return string(val), nil }
        if errors.Is(err, ErrKeyDeleted) { m.sstablePointReads.Add(1); return " ", err }
        if errors.Is(err, ErrNotFound) { continue }
        return "", err
    }
    return " ", ErrNotFound
}

func (sr *SSTableReader) release(m *Manager) {
    if sr.inflight.Add(-1) == 0 && sr.retired.Load() {
        sr.freeOnce.Do(func() { sr.reclaim(m) })   // close, then unlink, outside all locks
    }
}
```

Notes:

- The `ErrKeyDeleted` / `" "` return conventions are copied verbatim from A (`manager.go:255–269`) so M-3 holds.
- Memory-tier hits return **before** the capture, so A-min pays its Θ(k) lease cost only on disk-bound reads — exactly the reads A pays milliseconds for. This preserves the Workload E fast path by construction.
- `releaseScratch`/`acquireScratch` keep the buffer at `cap ≥ 32` (the largest observed reader set is `1 (compacted) + concurrent flushes`); growth is amortized.
- `duringPointReadHook` keeps its original *semantic* position (once, after memory tiers, before the disk scan) but now runs **without** `m.Mu` held. Existing tests that use the hook to pause while the lock is held (the publication-overlap audit, the deterministic barrier tests) must be parameterized on the arm; this is a test-harness change, not an engine change.

**Reclamation cost is on the reader path.** The unlucky releaser that drops `inflight` to zero pays the `close` plus, on Windows, up to 3 × 10 ms of unlink retries. This is O(1) per retired table (≈ 28 flushes + 15 compactions per 10 s in the churn regime), but a single reader can absorb a 30 ms tail event. It is accepted in the minimal design and measured as a tail term; if it appears in the E2/E3 tail, the pre-registered alternative is to hand reclamation to the existing bounded cleanup path used by `SharedReader` (`cleanupWg`), which is a *second* change and therefore a different arm, not a silent optimization.

---

## 6. Handle Lifetime and Reclamation

### 6.1 State machine (per `SSTableReader`)

```
                 published (in ActiveSSreaders, retired=false, inflight>=0)
                    │
      lease ────────┤◄──────────── lease (only while in ActiveSSreaders, under RLock)
                    │
                    │  compaction publishes replacement:
                    │    under m.Mu.Lock(): retired=true; removed from ActiveSSreaders
                    ▼
                 retired (retired=true, inflight may be >0)
                    │
       inflight==0 ├──────────────► reclaiming (freeOnce)
                    │                    │ close(fd)   [exactly once]
                    │                    │ unlink(path) [bounded retries; skipped on close failure]
                    ▼                    ▼
                 retired,0            reclaimed (terminal)
```

### 6.2 Ownership rules

| Rule | Statement |
| :--- | :--- |
| **O-1** | Only `m.ActiveSSreaders` grants new leases. A table not in that slice can never gain `inflight`. |
| **O-2** | A table is added to `m.ActiveSSreaders` only under `m.Mu.Lock()`, already open and with `retired=false`. |
| **O-3** | A table is removed from `m.ActiveSSreaders` and marked `retired=true` in the *same* `m.Mu.Lock()` critical section that installs its replacement. |
| **O-4** | Marking `retired` is the linearization point of supersession: after it, the table is reachable only through a pre-existing lease. |
| **O-5** | Reclamation is attempted at exactly two sites: the compactor immediately after publication, and the last `release()`. Both are guarded by `freeOnce`; neither holds `m.Mu`. |
| **O-6** | `reclaim` closes the descriptor before unlinking the path (Windows `ERROR_SHARING_VIOLATION`, same rule as `SharedReader`). If `Close` fails, unlinking is abandoned and the error is recorded. |

### 6.3 `reclaim`

```go
func (sr *SSTableReader) reclaim(m *Manager) {
    var err error
    if sr.File != nil { err = sr.File.Close() }
    if err != nil { sr.freeErr = err; m.recordCleanupErr(err); return }   // no unlink after failed close
    path := sr.File.Name()
    for attempt := 0; attempt < 3; attempt++ {
        if uerr := os.Remove(path); uerr == nil || errors.Is(uerr, os.ErrNotExist) { return }
        time.Sleep(10 * time.Millisecond)
    }
    sr.freeErr = /* last unlink error */; m.recordCleanupErr(sr.freeErr)
}
```

Retries are synchronous in the unlucky releaser's goroutine, bounded at 3 × 10 ms, and reuse the retry policy already established for `SharedReader` ([snapshot.go:600–617](../../../lsmengine/snapshot.go)). No unbounded goroutines.

### 6.4 Publication and retirement sites

| Site | Change |
| :--- | :--- |
| Flush publication ([manager.go:784–815](../../../lsmengine/manager.go)) | **No change.** `append` under `Lock`; a newly appended table is already `retired=false`. |
| Compaction publication ([compaction.go:216–248](../../../lsmengine/compaction.go)) | Mark `retired=true` for each input **inside** the existing `m.Mu.Lock()` block (immediately after the `ActiveSSreaders` swap), then attempt reclamation after `m.Mu.Unlock()`. Delete the unconditional `r.Close(); os.Remove(...)` loop. |
| `Close()` ([manager.go:275–325](../../../lsmengine/manager.go)) | Insert a lease drain between `flushWg.Wait()` and the final close of readers (§6.5). |
| Failed-flush / failed-compaction paths | Unchanged: those readers were never published, so they are closed directly (they have `inflight==0`, `retired==false`; a direct `Close` + `Remove` is correct because no lease can exist). |
| `LatestValue` ([manager.go:327](../../../lsmengine/manager.go)) | **No change.** It runs under `m.Mu.Lock()`, sees only non-retired tables, and no lease can be granted to a table it can see. |

### 6.5 Shutdown drain

```go
// inside Close(), after m.flushWg.Wait() and before the final close of readers:
if err := m.drainReaderLeases(5 * time.Second); err != nil { m.flushErr = err }
```

`drainReaderLeases` sums `inflight` across `ActiveSSreaders` (which may still hold un-retired tables at close time) and polls every 100 µs until zero or timeout. Polling is acceptable because `Close` runs once; it adds **no** steady-state cost and no hot shared counter. At this point `m.state != EngineOpen`, so no new lease can be acquired (every `getApproachAMin` call that could lease already passed the state check under `RLock` *before* the close transition took `Lock`), and the drain terminates.

The final `Close()` then closes and unlinks exactly what A closes today, but only after every reader that could still be scanning has finished.

---

## 7. Cost Model (a prediction, to be tested by E5/D2)

Let `k` = `len(ActiveSSreaders)` and `S(r)` = records scanned, `c_scan ≈ 2–4 µs/record` (measured: Workload A total ≈ 10.8 ms per hit).

| Arm | Metadata cost per disk read | Time inside the exclusive-critical-section window |
| :--- | :--- | :--- |
| A | Θ(1) | `Θ(S(r))` ≈ 2–30 ms |
| **A-min** | **Θ(k) atomics + 1 scratch slice from a pool** | **Θ(k)** atomics ≈ 0.05–0.5 µs |
| B | 2× `snapshotMu` lock/unlock + pin | Θ(1) |

Prediction: **A-min should behave like B in E1/E2/E3** (where `k = 4–8`) and should be *worse than B* in E5 (`k = 16`) and in high-churn regimes, because its metadata cost grows with `k` while B's does not. If that ordering holds, the paper has a clean, non-obvious tradeoff to report instead of a binary "B is better".

`sync.Pool` is a per-P cache; with `GOMAXPROCS = cores` and `r ≤ 32`, contention is negligible. On the very first read per P the pool allocates once (`GOMAXPROCS` allocations total), which is excluded by the warmup phase.

---

## 8. Invariants and Correctness Argument

| ID | Invariant |
| :--- | :--- |
| **I-1** | *Atomic read-set capture.* Memory-tier verdict, frozen set, and SSTable slice are all observed inside one `RLock` window; nothing is re-read after `RUnlock`. |
| **I-2** | *Lease before unlock.* Every table in the captured slice has `inflight ≥ 1` before `RUnlock`. |
| **I-3** | *Retirement exclusion.* `retired=true` and removal from `ActiveSSreaders` occur in one `m.Mu.Lock()` section; therefore no new lease can be granted to a retired table. |
| **I-4** | *No resurrection.* `inflight` never increases after `retired=true` becomes visible to a leaser (there is no leaser that does not go through `ActiveSSreaders` under `RLock`). |
| **I-5** | *Exactly-once reclamation.* `freeOnce` guards the only two reclamation sites. |
| **I-6** | *Balanced leases.* Every `inflight.Add(1)` is matched by exactly one `release()`; `release` is deferred after the last capture-time failure point. |
| **I-7** | *Shutdown drain.* No descriptor is closed while `inflight > 0`. |
| **I-8** | *No lock held during I/O or reclamation.* `release`, `reclaim`, and the scan run with zero manager locks. Reclamation never runs under `m.Mu`. |

### 8.1 Theorem (no use-after-close)

*Claim.* No `SSTableReader.Get` executes on a table whose descriptor has been closed.

*Proof.* Suppose reader `R` calls `buf[i].Get`. By I-2, `buf[i].inflight ≥ 1` at the moment `R` released `m.Mu`. The descriptor is closed only inside `freeOnce` (I-5), which is entered only from (a) `release()` when the decrement yields 0, or (b) the compactor after publication when it observes `inflight == 0`. In case (a) the decrement is caused by `R`'s own later `release`, which happens after `Get` returns. In case (b), the compactor's observation of `inflight == 0` is atomic and I-4 guarantees the counter cannot subsequently rise; since `inflight ≥ 1` from I-2, the observation cannot be 0 while `R` is inside the scan. Hence no close can occur while `R` is inside `Get`. ∎

### 8.2 Theorem (visibility equivalence with A)

*Claim.* For any history, `A-min.Get(k, ts)` returns the same `(value, error)` as `A.Get(k, ts)` evaluated at the read's linearization point.

*Proof.* A's result is determined entirely by the (Mem, Frozenmem, SSTable-set) state observed at some instant `t0` inside its `RLock` window, because all tiers are immutable once written (SSTables are never mutated; frozen memtables are immutable; the active memtable is only mutated under `Lock`). A-min observes exactly that same triple at its own `t0` inside its `RLock` window (I-1), after which no member of the triple can change: active/frozen memtables are no longer touched, and every SSTable in the captured set is immutable and kept readable by its lease (§8.1). Therefore A-min's result equals A's result for the state at `t0`. ∎

Two boundary cases:

- **Tombstone masking.** A returns `" "` for a tombstone found in an SSTable and `"Key is Deleted"` for one in memory. A-min reproduces both, in the same order, because the tier order and the per-tier return strings are copied verbatim.
- **Compaction during the scan.** The captured set is the pre-compaction version; the compactor's output contains every visible version of the input tables (k-way merge with dedup by newest sequence). A-min therefore reads the same versions A would have read at `t0`; it does not miss data. It may read a table that no longer appears in the current set — which is the entire purpose of the lease.

### 8.3 What A-min does *not* provide

- No consistent multi-key read across *separate* `Get` calls (neither does A).
- No snapshot isolation for transactions (`Transaction.Get` still reads at `t.ReadTs` through the same path).
- No protection against a future compaction implementation that *drops* visible versions instead of merging them; A-min's correctness argument depends on compaction preserving visibility, which is the same assumption A makes.

---

## 9. Test Plan

**Executed 2026-10-28 on Windows 11 / NTFS.** Seven tests were implemented and pass; the mapping to this plan, the exact assertions and the timings are in [13_G1_REVIEW_EVIDENCE.md](13_G1_REVIEW_EVIDENCE.md) §2. Coverage of this table:

| Executed | Covers |
| :--- | :--- |
| `TestAMin_ParityWithApproachA` | T-13 (203-op observational equivalence, reduced from 10⁶ probes for runtime) |
| `TestAMin_LeasesAreBalanced` | T-02, T-09 |
| `TestAMin_CompactionDuringScan` | T-03, T-04, T-07 |
| `TestAMin_ShutdownDrainsLeases` | T-11, T-12 |
| `TestAMin_NoOrphanTablesUnderChurn` | T-08 |
| `TestAMin_NoExtraAllocations` | T-10 (assertion restated as *no additional* allocations, since the scan itself allocates) |
| `TestAMin_ConcurrentCloseWithReaders` | T-11 under contention |
| **Not implemented** | T-01 (flush atomically after capture), T-05 (1,000-race reclaim counter), T-06 (injected close failure), T-14 (automated diff-shape check) |

T-01, T-05, T-06 and T-14 are open items, not silent omissions: T-05/T-06 need an injectable file-error seam that does not exist yet, and T-14 was performed manually (the diff inventory in 13 §1) rather than by script.

Deterministic interleaving tests reuse the existing engine hooks (`beforeCompactionPublishHook`, `duringCompactionMergeHook`, `afterReleaseSnapshotUnlockHook`, `flushBuildHook`).

| ID | Test | Assertion |
| :--- | :--- | :--- |
| T-01 | `TestAMin_ReadSetCapturedAtomically` | Hook inserted between capture and `RUnlock`; a concurrent flush either appears fully (as a new table in a later read) or not at all. |
| T-02 | `TestAMin_LeaseBeforeUnlock` | Instrumented counter proves `leaseCount ≥ 1` for every captured table at the instant of `RUnlock`. |
| T-03 | `TestAMin_CompactionDuringScan` | Merge hook pauses after capture; compaction publishes and retires; reader completes and returns the correct value; fd remains valid throughout. |
| T-04 | `TestAMin_NoNewLeaseOnRetired` | After retirement, attempts to reach the table via `ActiveSSreaders` fail; `leaseCount` is unchanged. |
| T-05 | `TestAMin_ReclaimExactlyOnce` | 1,000 retire/lease races; `Close` called exactly once per table; no double unlink; `freeErr` nil. |
| T-06 | `TestAMin_ReclaimAfterCloseFailure` | Injected close error: unlink **not** attempted; error surfaced by `Close()`. |
| T-07 | `TestAMin_RetireWithOutstandingLease` | Table retired while `leaseCount=3`; not freed until all three release; freed by the third. |
| T-08 | `TestAMin_LeakUnderChurn` | churn loop; `.sst` files on disk == published readers; data directory removable. |
| T-09 | `TestAMin_BalancedLeasesOnErrorPaths` | Injected `ErrNotFound`, `ErrKeyDeleted`, `io.ErrUnexpectedEOF`; `leaseCount` returns to 0 every time. |
| T-10 | `TestAMin_NoExtraAllocations` | `testing.AllocsPerRun` on the disk path must not exceed Approach A's. |
| T-11 | `TestAMin_ShutdownDrain` | `Close()` concurrent with in-flight readers: returns after all complete; no `EBADF`; no deadlock. |
| T-12 | `TestAMin_ShutdownBlocksOnLease` | Artificially held lease blocks `Close()`; released lease unblocks it within the poll interval. |
| T-13 | `TestAMin_DifferentialOracle` | A and A-min results identical, including all error strings, across puts/deletes/reads/misses/tombstones. |
| T-14 | `TestAMin_SingleVariableDiff` | Static check (script) that the diff touches only the 5 sites in §10. |

**Race detector:** not available on the Windows host (`-race` requires cgo and a C compiler, neither present). `go test -race -count=20` was **not** run and is not claimed. Substitute evidence: `-count=5` repetition of the concurrency tests plus the invariant checks above ([12](12_WINDOWS_EXECUTION_AMENDMENT.md) §4.3).

---

## 10. Single-Variable Diff Audit (M-1/M-8)

Expected change set — nothing else:

| # | File | Change |
| :--- | :--- | :--- |
| 1 | `lsmengine/sstableReader.go` | Add `inflight`, `retired`, `freeOnce`, `freeErr` fields; add `release`/`reclaim` methods. No change to `Get`. |
| 2 | `lsmengine/manager.go` | Add `SyncModeApproachAMin`, `leasePool`, `cleanupErrs`; add `getApproachAMin`; dispatch in `Get`; add `drainReaderLeases` call in `Close`. |
| 3 | `lsmengine/compaction.go` | In the A branch: mark inputs retired inside the publication lock; replace the unconditional close/unlink loop with a post-unlock reclamation attempt. |
| 4 | `lsmengine/manager.go` | `CreateManagerWithConfig` accepts the third mode; `closeApproachB` untouched. |
| 5 | `research/experiments/harness/runner.go` | Accept `"ApproachAMin_Lease"` as a sync mode string. |

Explicitly **not** changed: `Transaction.Commit`, `LatestValue`, `latestValueLocked`, `flushToDisk`, `publishFlushLockedApproachB`, `snapshot.go`, `wal.go`, `sstableBuilder.go`, `memtable.go`, `keys.go`, the WAL format, the SSTable format, and every Approach A/B code path.

A CI check will assert that `git diff --name-only` is a subset of the five files above and that the A/B functions are textually unchanged (hash of the function bodies).

---

## 11. Rejected Alternatives (and why)

| Alternative | Verdict | Reason |
| :--- | :--- | :--- |
| Unlock before the scan without a lease | **Incorrect** | Use-after-close / fd reuse (§3). |
| Reopen the file per probe (`os.Open` inside the scan) | **Correct but not minimal** | Adds an `open`/`close` syscall pair per table probe to the read path; changes the cost profile being measured. |
| POSIX unlink semantics only (never `Close()` until shutdown) | **Not safe** | `Close()` is what races with `ReadAt`; also unbounded fd growth. |
| Per-reader lazy `tryLease` with Dekker-style check | **Incorrect** | A table retired *after* capture must still be readable by the capturing reader; a failed lazy lease cannot distinguish "retired before capture" (impossible) from "retired after capture" (must read). Would require a restart loop and would still need up-front capture. |
| Global reader counter + epoch reclamation (RCU-like) | **Rejected on cost/leak grounds** | One contended cache line per read; reclamation waits for global quiescence, so descriptors accumulate under sustained churn. |
| `RWMutex` → `atomic.Pointer[[]*SSTableReader]` swap (a "mini-B") | **Rejected** | That *is* snapshot versioning; it would destroy the single-variable property of the A → A-min contrast. |
| Fix the read path (block-level decode, no per-record alloc) first | **Out of scope for I7** | Would change the frozen baseline's cost model and invalidate comparability; recorded as a future work item. |
