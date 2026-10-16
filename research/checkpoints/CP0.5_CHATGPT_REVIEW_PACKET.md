# CHECKPOINT CP0.5 — External Review Packet

**Project:** Reader–Compactor Synchronization in a Go LSM Storage Engine  
**Milestone:** Mini-Checkpoint CP0.5 (Sub-phases I1.1 & I1.1a Complete)  
**Target Git Branch:** `research/reader-compactor-sync`  
**Git Commit:** `85775ba`  
**Host Platform:** Windows 11 amd64 (AMD Ryzen 7 7445HS, NTFS NVMe SSD) / WSL2 Linux amd64 (Kernel 6.6, GCC 13.3)  
**Date:** 2026-10-16  

---

## 1. Executive Summary & Purpose

Following the conditional approval of Checkpoint CP0, this mini-checkpoint reports the resolution of **Issue E-001** (deterministic recursive self-deadlock in `Transaction.Commit`) and the specification of the **Approach A Baseline RWMutex Protocol** (Sub-phase I1.1).

Prior to this fix, 3 out of 4 unit tests in the repository timed out due to self-deadlock on `sync.RWMutex`, blocking all write operations and benchmarks. With the completion of Sub-phase I1.1a:
- The self-deadlock has been completely eliminated via an unexported helper [`latestValueLocked`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L125).
- All 4 original unit tests plus 1 new conflict detection regression test pass cleanly on native Windows (`0.97s`).
- The entire package passes Go's thread sanitizer (`go test -race -v ./lsmengine`) in WSL2 with **zero data races**.
- The compaction publication hazard (overwriting flushed tables) is documented and formally deferred to Sub-phase I1.2.

---

## 2. Lock Acquisition Graph Before and After

```
================================================================================
BEFORE FIX (Deadlock State - E-001)
================================================================================
Caller: Transaction.Commit()
  │
  ├── 1. Acquires Exclusive Lock: t.Man.Mu.Lock()
  │
  ├── 2. Write Conflict Check Loop (for key in WriteSet):
  │        Calls: t.Man.LatestValue(key)
  │                 │
  │                 └── Attempts to acquire: m.Mu.RLock()
  │                       │
  │                       └── [DEADLOCK]: Go's sync.RWMutex is non-reentrant.
  │                           The active writer goroutine suspends itself on
  │                           runtime_SemacquireRWMutexR waiting for writer to unlock.
  │                           Process hangs until test alarm triggers timeout panic.
  │
  └── (Never Reached): WAL Append, Memtable Put, Mutex Unlock


================================================================================
AFTER FIX (Correct Baseline Lock Flow - I1.1a)
================================================================================
Caller: Transaction.Commit()
  │
  ├── 1. Acquires Exclusive Lock: t.Man.Mu.Lock()
  │
  ├── 2. Write Conflict Check Loop (for key in WriteSet):
  │        Calls: t.Man.latestValueLocked(key)
  │                 │
  │                 └── Directly reads m.Mem and m.Frozenmem [NO MUTEX CALL]
  │                       - If latestSeqNo > t.ReadTs:
  │                           Unlocks t.Man.Mu.Unlock(), aborts txn, returns error.
  │
  ├── 3. Sequence Number Increment: t.Man.CurrentSeqNo.Add(1)
  │
  ├── 4. Write-Ahead Log Append: t.Man.Log.Append(encodedKey, val)
  │        └── Acquires w.mu.Lock() -> Writes -> Syncs -> w.mu.Unlock()
  │            (Strict lock hierarchy: Manager.Mu -> Wal.mu)
  │
  ├── 5. Memtable In-Memory Put: t.Man.Mem.Put(encodedKey, val)
  │
  ├── 6. Releases Exclusive Lock: t.Man.Mu.Unlock()
  │
  └── 7. Flush Evaluation: t.Man.checkAndTriggerFlush()
           └── Acquires m.Mu.Lock() briefly -> checks 64MB -> spawns background flusher

Public Read Path (Concurrent Point Queries):
Caller: Manager.Get(key, readTs)
  └── Acquires Shared Lock: m.Mu.RLock()
        ├── Scans m.Mem
        ├── Scans m.Frozenmem
        ├── Scans m.ActiveSSreaders[i].Get() [Direct File I/O]
        └── Releases Shared Lock: m.Mu.RUnlock()
```

---

## 3. Exact Code Modifications (Diffstat: 3 files changed, 48 insertions, 4 deletions)

### A. [`lsmengine/manager.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L119-L139)
```diff
 func (m *Manager) LatestValue(userKey []byte) (uint64, error) {
 	m.Mu.RLock()
 	defer m.Mu.RUnlock()
+	return m.latestValueLocked(userKey)
+}
+
+func (m *Manager) latestValueLocked(userKey []byte) (uint64, error) {
 	seq, err := m.Mem.LatestValue(userKey)
 	if err == nil {
 		return seq, nil
 	} else if errors.Is(err, ErrKeyDeleted) {
 		return 0, err
 	}
 
 	for i := len(m.Frozenmem) - 1; i >= 0; i-- {
 		seq, err := m.Frozenmem[i].LatestValue(userKey)
 		if err == nil {
 			return seq, nil
 		} else if errors.Is(err, ErrKeyDeleted) {
 			return 0, err
 		}
 	}
 
 	return 0, ErrNotFound
 }
```

### B. [`lsmengine/transaction.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L56-L65)
```diff
 func (t *Transaction) Commit() error {
 	t.Man.Mu.Lock()
 
 	for key := range t.WriteSet {
-		// Changed to use the tiered Manager LatestValue
-		latestSeqNo, err := t.Man.LatestValue([]byte(key))
+		// Use internal unexported latestValueLocked since t.Man.Mu is already locked
+		latestSeqNo, err := t.Man.latestValueLocked([]byte(key))
 
 		if err == nil && latestSeqNo > t.ReadTs {
 			t.Man.Mu.Unlock()
 			t.Abort()
 			return errors.New("Write-write conflict detected. Transaction aborted.")
 		}
 	}
```

### C. [`lsmengine/engine_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/engine_test.go#L131-L168)
Added `TestWriteWriteConflict` to assert that:
1. Two concurrent transactions modifying the same key are serialized by commit order.
2. The transaction with a stale `ReadTs` aborts with `"Write-write conflict detected. Transaction aborted."`.
3. The committed value from the successful transaction remains visible.

---

## 4. Verification Evidence & Test Matrix

| Test Name | Pre-Fix Status | Post-Fix Status (Windows) | Post-Fix Status (WSL2 `-race`) | Coverage Category |
| :--- | :--- | :--- | :--- | :--- |
| `TestSkipListLevelDistribution` | PASS (0.00s) | PASS (0.00s) | PASS (0.00s) | SkipList Level Math |
| `TestBasicPutGet` | **FAIL (Deadlock 5s)** | **PASS (0.05s)** | **PASS (0.08s)** | Single-Txn Put/Get |
| `TestDeleteAndTombstone` | **FAIL (Deadlock 5s)** | **PASS (0.01s)** | **PASS (0.04s)** | Delete & Tombstones |
| `TestWALRecovery` | **FAIL (Deadlock 5s)** | **PASS (0.09s)** | **PASS (0.28s)** | WAL CRC32 & Replay |
| `TestWriteWriteConflict` | *New test* | **PASS (0.00s)** | **PASS (0.02s)** | MVCC Write Conflict |

### Raw WSL2 Race Detector Output (`/snap/bin/go test -race -v ./lsmengine`):
```text
=== RUN   TestSkipListLevelDistribution
--- PASS: TestSkipListLevelDistribution (0.00s)
=== RUN   TestBasicPutGet
--- PASS: TestBasicPutGet (0.08s)
=== RUN   TestDeleteAndTombstone
--- PASS: TestDeleteAndTombstone (0.04s)
=== RUN   TestWALRecovery
--- PASS: TestWALRecovery (0.28s)
=== RUN   TestWriteWriteConflict
--- PASS: TestWriteWriteConflict (0.02s)
PASS
ok  	owndbms/lsmengine	1.459s
```

---

## 5. Formal Invariants Status Summary

- **INV-SYNC-1 (SkipList Mutation Safety)**: **Verified**. SkipList is accessed under `m.Mu.Lock()` during write/commit and `m.Mu.RLock()` during reads.
- **INV-SYNC-2 (Re-entrant Lock Freedom)**: **Verified**. Recursive mutex acquisition in `Transaction.Commit` is completely eliminated.
- **INV-SYNC-3 (Lock Hierarchy)**: **Verified**. Strict order `Manager.Mu -> Wal.mu` is preserved.
- **INV-LIFETIME-3 (Non-destructive Compaction Publication)**: **Deferred to I1.2**. Currently `CompactAll` overwrites `ActiveSSreaders`.
- **INV-LIFETIME-2 (Safe Reader Retirement & File Deletion)**: **Deferred to I1.2/I1.3**. Closing/unlinking files outside mutex protection on Windows.

---

## 6. Questions for External Reviewer

1. **Conflict Detection Scope Across Tiers**: `latestValueLocked` currently checks `m.Mem` and `m.Frozenmem`. In high-throughput writes where a conflicting transaction commits and flushes to an SSTable before the concurrent transaction commits, should `latestValueLocked` eventually inspect `ActiveSSreaders`, or is searching memory tiers acceptable given LSM flush latencies?
2. **Compaction Publication Concurrency (Sub-phase I1.2)**: For Sub-phase I1.2, do you approve an identity-based slice filter (`newReaders = append(filterOut(currentReaders, compactedInputSet), newCompactedReader)`) performed under `m.Mu.Lock()` to prevent erasing concurrently flushed SSTables?
3. **WSL2 Race Validation**: Does the execution of `go test -race` inside WSL2 provide sufficient verification for the threading model, while reserving performance benchmarking exclusively for native Windows NTFS?

---
**STATUS: MINI-CHECKPOINT CP0.5 COMPLETE. AWAITING EXTERNAL REVIEW APPROVAL BEFORE STARTING SUB-PHASE I1.2.**
