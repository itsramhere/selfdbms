# Investigation: Cross-Tier Transaction Conflict Detection (I1.1b.1)

**Phase:** I1.1b.1 — Conflict Detection Semantics Investigation  
**Commit:** `4ca8787`  
**Date:** 2026-10-16  

---

## 1. Investigation Scope & Questions

This audit investigates the end-to-end lifecycle of transaction commits, write-write conflict detection, timestamp assignment, tombstone handling, and tier transitions in `owndbms/lsmengine`.

---

## 2. Answers to Core Semantic Questions

### Q1: How are transaction snapshot timestamps assigned?
- In `Manager.Begin()` ([`lsmengine/manager.go:48-54`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L48-L54)):
  ```go
  func (m *Manager) Begin() *Transaction {
      t := &Transaction{}
      t.Man = m
      t.ReadTs = t.Man.CurrentSeqNo.Load()
      t.WriteSet = make(map[string]PendingWrite)
      return t
  }
  ```
- `ReadTs` is assigned from the global atomic counter `m.CurrentSeqNo` at the time `Begin()` is called.
- Any transaction committed *after* `t.Begin()` receives an `AllocatedSeqNo = t.Man.CurrentSeqNo.Add(1)` which is strictly greater than `t.ReadTs`.

### Q2: How are write-write conflicts detected in current code?
- In `Transaction.Commit()` ([`lsmengine/transaction.go:56-67`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/transaction.go#L56-L67)):
  ```go
  t.Man.Mu.Lock()
  for key := range t.WriteSet {
      latestSeqNo, err := t.Man.latestValueLocked([]byte(key))
      if err == nil && latestSeqNo > t.ReadTs {
          t.Man.Mu.Unlock()
          t.Abort()
          return errors.New("Write-write conflict detected. Transaction aborted.")
      }
  }
  ```
- A conflict is flagged if:
  1. `err == nil`, **and**
  2. `latestSeqNo > t.ReadTs`.
- If `latestValueLocked` returns `ErrNotFound` or any non-nil error, the condition `err == nil` evaluates to `false`, and the conflict check is bypassed.

### Q3: Whether the latest committed sequence can be retrieved from an SSTable?
- **Current State in `SSTableReader`**:
  - `SSTableReader` currently only implements `Get(userKey []byte, readTs uint64) ([]byte, error)`.
  - It does **not** expose a `LatestValue` or `LatestSequenceNo` method.
- **Feasibility with Current On-Disk Format**:
  - **YES**. SSTable data blocks are written in sorted internal key order (`CompareInternalKeys`).
  - For a given `userKey`, internal keys are packed as `(seqNo << 8) | isDelete` and bitwise inverted. Consequently, higher sequence numbers sort *first*.
  - By constructing a seek key `EncodeInternalKey(userKey, math.MaxUint64, true)`, the index binary search locates the block where the newest version of `userKey` must reside.
  - Scanning from that block offset, the *first* record matching `userKey` is mathematically guaranteed to be the latest committed version of that key in the SSTable.
  - Decoding that record via `DecodeInternalKey(diskKey)` directly yields `diskSeqNo` and `isDelete`.

### Q4: Whether tombstone records retain the necessary version metadata?
- **YES**.
  - Tombstones are encoded via `EncodeInternalKey(userKey, seqNo, true)`.
  - The sequence number `seqNo` is preserved identically to put operations.
  - In `Memtable.LatestValue` ([`lsmengine/memtable.go:113-116`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/memtable.go#L113-L116)):
    ```go
    candidateUserKey, candidateSeqNo, _ := DecodeInternalKey(candidate.Key)
    if bytes.Equal(userKey, candidateUserKey) {
        return candidateSeqNo, nil
    }
    ```
    It returns `candidateSeqNo` regardless of whether `isDelete` is true or false.
  - **Subtle Bug in `Manager.LatestValue` / `latestValueLocked`**:
    `manager.go` contains:
    ```go
    else if errors.Is(err, ErrKeyDeleted) {
        return 0, err
    }
    ```
    If any tier returned `ErrKeyDeleted`, `latestValueLocked` would return `(0, ErrKeyDeleted)`, causing `Transaction.Commit` (`if err == nil ...`) to treat it as "no conflict"!
    Tombstones are mutations and must be treated as writes for conflict detection. The sequence number of a tombstone must be returned with `nil` error so `latestSeqNo > t.ReadTs` triggers abort.

### Q5: Whether conflict-detection logic is correct after Memtable flush and compaction?
- **CRITICAL DEFECT**:
  - `latestValueLocked` currently checks **only** `m.Mem` and `m.Frozenmem`.
  - If a transaction $T_2$ commits an update to key $K$ at $Seq = 15$ ($> T_1.ReadTs = 10$), and $T_2$'s version is flushed to disk via `flushToDisk`, $K$ is removed from `m.Mem` and `m.Frozenmem` and resides solely in `m.ActiveSSreaders`.
  - When $T_1$ subsequently attempts to commit, `latestValueLocked(K)` returns `(0, ErrNotFound)`.
  - $T_1$ fails to detect that $K$ was updated and committed after $T_1$'s snapshot.
  - $T_1$ commits and silently overwrites $T_2$'s change, violating Snapshot Isolation / write-write conflict safety.

### Q6: Whether the current read path can be safely reused without recursively acquiring the manager lock?
- `Manager.Get` acquires `m.Mu.RLock()`.
- `Transaction.Commit` already holds `m.Mu.Lock()`.
- Directly calling `Manager.Get` from `Transaction.Commit` would re-introduce the recursive self-deadlock (`E-001`).
- Therefore, cross-tier lookup within `Transaction.Commit` must either:
  1. Call tier-specific helpers that assume `m.Mu` is already held (e.g. `m.ActiveSSreaders[i].LatestValue(userKey)`), which perform file I/O statelessly via `io.NewSectionReader` without touching `Manager.Mu`.
  2. Never invoke `Manager.Get` directly from `Commit`.

---

## 3. Exact Call Graph for Conflict Detection

```
Transaction.Commit() [holds t.Man.Mu.Lock()]
  │
  ├── For each key in t.WriteSet:
  │     │
  │     └── Call: t.Man.latestValueLocked(userKey)
  │           │
  │           ├── Tier 1: m.Mem.LatestValue(userKey)
  │           │     └── Scans SkipList -> returns (seq, nil) if found
  │           │
  │           ├── Tier 2: m.Frozenmem[i].LatestValue(userKey) (newest to oldest)
  │           │     └── Scans Frozen SkipLists -> returns (seq, nil) if found
  │           │
  │           ├── Tier 3: [CURRENTLY MISSING!] m.ActiveSSreaders[i].LatestValue(userKey)
  │           │     └── Must scan SSTables (newest to oldest) -> returns (seq, nil) if found
  │           │
  │           └── Fallthrough: return (0, ErrNotFound)
  │
  └── Evaluation:
        if err == nil && latestSeqNo > t.ReadTs:
            ABORT (Write-Write Conflict)
```

---

## 4. Potential Failure Scenarios

1. **Flush Before Commit (Lost Update)**:
   - $T_1.Begin()$ at $Seq=5$.
   - $T_2.Begin()$, writes $K$, commits at $Seq=6$.
   - Flush runs: $K$ moves to `ActiveSSreaders[0]`.
   - $T_1$ writes $K$, calls $Commit()$.
   - `latestValueLocked` returns `ErrNotFound`.
   - $T_1$ commits at $Seq=7$, overwriting $T_2$.
2. **Tombstone Flush Before Commit (Resurrection / Lost Delete)**:
   - $T_1.Begin()$ at $Seq=5$.
   - $T_2$ deletes $K$, commits at $Seq=6$.
   - Flush runs: tombstone moves to `ActiveSSreaders[0]`.
   - $T_1$ writes $K$, calls $Commit()$.
   - `latestValueLocked` returns `ErrNotFound`.
   - $T_1$ commits at $Seq=7$, silently ignoring that $K$ was concurrently deleted.
3. **Compacted SSTable Conflict**:
   - Same as above, but multiple SSTables are merged into a compacted table before $T_1$ commits. $T_1$ must still detect the conflict from the compacted SSTable.
