# CHECKPOINT CP0 — Baseline Reconnaissance and Architecture Audit

**Commit:** `3eaae45`  
**Branch:** `research/reader-compactor-sync`  
**Date:** `2026-10-16`  

---

### Completed Sub-phases
- **I0.1 — Capture Environment**: Established host hardware and toolchain baseline (Windows 11, Ryzen 7 7445HS, Samsung NVMe SSD, NTFS, Go 1.26.3). Initialized research directory structure, activity logs, decision registers, and reproducibility guide ([`research/README.md`](../README.md)).
- **I0.2 — Trace Point-Read and Compaction Paths**: Traced exact call graphs, mutex acquisitions, goroutine interactions, publication steps, and file removal paths ([`research/design/architecture.md`](../design/architecture.md)).
- **I0.3 — Trace Version and Memtable Safety**: Verified internal key encoding math, inverted version sorting, WAL append/replay semantics, SkipList concurrency, and categorized all formal invariants into Verified, Violated, and Unproven ([`research/design/invariants.md`](../design/invariants.md)).
- **I0.4 — Run Existing Validation Unchanged**: Executed the unmodified test suite. Identified 1 passing test, 3 timing-out/deadlocked unit tests, unmasked the missing CGO compiler requirement for `-race`, established test inventory ([`research/debug/TEST_MATRIX.md`](../debug/TEST_MATRIX.md)), and archived deadlock trace ([`research/debug/E-001/before.log`](../debug/E-001/before.log)).

---

### Changed Files + Diffstat
```text
 .gitignore                      |   2 +
 research/README.md              | 115 ++++++++++++++++++++++++++++++++++++++
 research/debug/E-001/before.log |  42 ++++++++++++++
 research/debug/E-001/repro.md   |  27 +++++++++
 research/debug/TEST_MATRIX.md   |  32 +++++++++++
 research/design/architecture.md | 121 ++++++++++++++++++++++++++++++++++++++++
 research/design/invariants.md   |  83 +++++++++++++++++++++++++++
 research/logs/DECISIONS.md      |  10 ++++
 research/logs/ISSUES.md         |  62 ++++++++++++++++++++
 research/logs/WORKLOG.md        |  16 ++++++
 10 files changed, 510 insertions(+)
```

---

### Architecture & Behavior Changes
- **Zero code changes were made to the storage engine synchronization or logic** during Phase I0, preserving the repository baseline intact per project rules.
- Created dedicated git branch `research/reader-compactor-sync` starting from commit `08d3189069294e8168ebb5234a3bef1d37f1d649`.
- Documented baseline lock and ownership architecture in [`research/design/architecture.md`](../design/architecture.md).

---

### Decisions Made
- **D-001** (2026-10-16): Created isolated feature branch `research/reader-compactor-sync` rather than modifying `main` directly, to preserve git auditability and clean diff boundaries.

---

### Issues Encountered
- **E-001** (Severity: Correctness / Deadlock, Status: OPEN): Deterministic recursive self-deadlock in `Transaction.Commit`. `Transaction.Commit` holds exclusive write lock `t.Man.Mu.Lock()` and subsequently invokes `t.Man.LatestValue(key)` which attempts to acquire read lock `m.Mu.RLock()`. Go's `sync.RWMutex` is not re-entrant; any transaction commit blocks indefinitely. Minimal reproduction: `go test -v ./lsmengine -run TestBasicPutGet -timeout 5s`.
- **E-002** (Severity: Environment / Tooling, Status: OPEN): Running `go test -race` fails immediately on Windows with `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`. The host system lacks a C compiler (`gcc` / `clang`) in PATH.

---

### Exact Commands Executed + Pass/Fail Counts
1. `go build ./...` — **PASS** (exit code 0).
2. `go test -v ./lsmengine -run TestSkipListLevelDistribution` — **PASS** (1 test passed, 0 failed, duration 0.00s).
3. `go test -v ./lsmengine -run TestBasicPutGet -timeout 5s` — **FAIL / TIMEOUT** (0 passed, 1 timed out with panic trace in `sync.runtime_SemacquireRWMutexR`).
4. `go test -v ./lsmengine -run TestDeleteAndTombstone -timeout 5s` — **FAIL / TIMEOUT** (0 passed, 1 timed out on `txn1.Commit()`).
5. `go test -v ./lsmengine -run TestWALRecovery -timeout 5s` — **FAIL / TIMEOUT** (0 passed, 1 timed out on `txn.Commit()`).
6. `go test -race ./lsmengine` — **FAIL** (exit code 1: `-race requires cgo`).

**Summary:** 1 passed, 3 failed (deadlock), 0 skipped out of 4 existing unit tests.

---

### Evidence Paths
- System & Environment: [`research/README.md`](../README.md)
- Architectural & Call Hierarchy: [`research/design/architecture.md`](../design/architecture.md)
- Invariants & Correctness Rules: [`research/design/invariants.md`](../design/invariants.md)
- Issue Log: [`research/logs/ISSUES.md`](../logs/ISSUES.md)
- Decision Log: [`research/logs/DECISIONS.md`](../logs/DECISIONS.md)
- Chronological Work Log: [`research/logs/WORKLOG.md`](../logs/WORKLOG.md)
- Deadlock Repro Document: [`research/debug/E-001/repro.md`](../debug/E-001/repro.md)
- Raw Panic Stack Trace: [`research/debug/E-001/before.log`](../debug/E-001/before.log)
- Complete Test Inventory: [`research/debug/TEST_MATRIX.md`](../debug/TEST_MATRIX.md)

---

### Known Limits, Questionable Claims, and Unverified Assumptions

A rigorous line-by-line audit comparing the manuscript (`paper.tex` and `Results.md`) against the actual code revealed several major discrepancies:

| Manuscript Claim / Assumption | Code Reality | Risk / Assessment |
| :--- | :--- | :--- |
| **1. Write Path Scalability & Durability Benchmarks** (`paper.tex` Table I, §IV-A; `Results.md` §I): Reports sequential writes at ~2.67 ms/op and parallel writes at ~0.95–2.69 ms/op. | **BROKEN BY DEADLOCK**: `Transaction.Commit` deadlocks itself on line 60 ([`lsmengine/transaction.go:60`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L60)) when calling `LatestValue` under `m.Mu.Lock()`. None of the write benchmarks can execute in the present repository. | Critical regression introduced when `LatestValue` was modified to use manager-tiered lookup without considering lock re-entrancy. Must be fixed in Phase I1. |
| **2. "Lock-Free Memtable Scalability"** (`paper.tex` §II-A, §IV-B; `Results.md` §II-2): Claims point queries achieve lock-free Memtable reads without coarse-grained locks. | **FALSE**: `Manager.Get` explicitly acquires `m.Mu.RLock()` ([`lsmengine/manager.go:58`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L58)) and defers `RUnlock()` until all tiers (Memtable, Frozen, and physical SSTables) are read. Reads are NOT lock-free. | The paper's claim conflates read-write locking (`RLock`) with lock-free algorithms. Paper terminology must be corrected. |
| **3. Compaction Race Condition Resolution** (`paper.tex` §III-A, Listings 1–2; `Results.md` §III): Claims upgrading `m.Mu` to `sync.RWMutex` safely coordinates `Manager.Get` and `Manager.CompactAll`. | **FLAWED & INCOMPLETE**: <br>(a) `CompactAll` snapshots `m.ActiveSSreaders`, unlocks `m.Mu`, performs merge, re-locks `m.Mu`, and executes `m.ActiveSSreaders = []*SSTableReader{newReader}` ([`lsmengine/compaction.go:106`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go#L106)). If `flushToDisk` appends a new SSTable during the merge, compaction overwrites and permanently destroys the newly flushed SSTable.<br>(b) Closing readers immediately after releasing `m.Mu` lock is unsafe if any reader reference outlives the lock. On Windows NTFS, calling `os.Remove` while iterators hold open handles causes sharing violations. | High data-loss risk and unsafe retirement. Approach A (Phase I1) and Approach B (Phase I2-I3) must implement genuinely safe publication and retirement. |
| **4. Validated Compaction Concurrency** (`paper.tex` §III-A): Implies synchronization between `Get` and `CompactAll` was verified under concurrency. | **UNTESTED**: `CompactAll` is never called anywhere in `engine_test.go`, `manager_bench_test.go`, or `comparison_bench_test.go`. There are zero existing tests for compaction or reader-compactor concurrency. | Zero empirical evidence exists for reader-compactor concurrency in the baseline. New deterministic tests required. |
| **5. Cross-OS Environment Confound** (`paper.tex` §IV-C): Compares WSL2 9P mount vs Linux ext4 vs native Windows. | **HOST RESTRICTION**: Current environment is native Windows 11 on NTFS. Linux ext4 measurements cannot be directly replicated without WSL2/Linux. | Benchmarks on this machine will reflect native Windows NTFS. |
| **6. ThreadSanitizer Verification** (`paper.tex` §III-A): Claims data race was verified with Go's `-race` detector. | **UNAVAILABLE LOCALLY**: Running `-race` requires CGO, but no C toolchain (`gcc`/`clang`) is installed in the Windows host PATH (`E-002`). | Need human/reviewer guidance on whether to install MinGW-w64 via `winget` or test `-race` in an isolated Linux container/WSL. |

---

### Questions for ChatGPT Reviewer
1. **Self-Deadlock Resolution Strategy**: For Sub-phase I1.1 / I1.2, should we resolve `E-001` in `Transaction.Commit` by extracting an internal unexported `latestValueLocked(userKey []byte)` helper that assumes `m.Mu.Lock()` is already held, or should `Transaction.Commit` directly query the active `m.Mem` and `m.Frozenmem` slices?
2. **Compaction Publication Concurrency**: In `CompactAll`, when publishing the newly compacted SSTable, should the publication logic atomically compute the set difference (`(current_readers - compacted_readers) + new_reader`) under `m.Mu.Lock()` to prevent erasing SSTables concurrently created by `flushToDisk`?
3. **CGO / Race Detector Setup on Windows**: Since `go test -race` requires a C compiler on Windows, do you authorize installing MinGW-w64 (via `winget install MSYS2.MSYS2` or `winget install LLVM.LLVM`), or should race detector validation be conducted inside WSL2?

---

### Next Planned Sub-phase (DO NOT START UNTIL APPROVED)
**Phase I1 — Build a genuinely safe RWMutex baseline (Approach A)**:
- **Sub-phase I1.1**: Specify baseline invariants in `design/invariants.md`, covering reader lock scope over `ActiveSSreaders`, frozen memtables, active memtable, and file descriptor lifetimes.
- Fix `E-001` self-deadlock so that basic unit tests (`TestBasicPutGet`, `TestDeleteAndTombstone`, `TestWALRecovery`) pass.

---
**STATUS: STOPPED AT CHECKPOINT CP0 AWAITING HUMAN AND CHATGPT REVIEW.**
