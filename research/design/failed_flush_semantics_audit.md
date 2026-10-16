# Sub-phase I1.4.7a: Failed-Flush Precedence and Lifecycle Semantics Audit

**Date:** 2026-10-17  
**Sub-phase:** I1.4.7a  
**Governing Architecture:** Approach A (Manager-wide Mutex Baseline)  
**Investigation Target:** `RISK-004` — Flush Failure Precedence Inversion  

---

## 1. Executive Summary

This audit investigates the exact execution semantics of the background flush worker, memory-tier storage, on-disk SSTable readers, read paths (`Manager.Get`), and transaction conflict detection (`Manager.latestValueLocked`) when a background SSTable flush fails while subsequent flushes have been admitted.

### Core Finding
**`RISK-004` is a confirmed critical correctness defect.**
When an older flush $F_0$ fails (e.g., due to simulated I/O failure, disk full, or reader initialization error) and returns early from `flushToDisk`, the background worker `flushLoop` does not halt. It continues dequeuing and processing subsequent tasks ($F_1, F_2$). If $F_1$ succeeds, $F_1$'s reader is published to `m.ActiveSSreaders` and $F_1$ is removed from `m.Frozenmem`. Meanwhile, $F_0$ is never removed from `m.Frozenmem`.

Because `Manager.Get` and `Manager.latestValueLocked` examine Tier 2 (`m.Frozenmem`) **before** Tier 3 (`m.ActiveSSreaders`) under the architectural assumption that *all* frozen memtables are strictly newer than *all* SSTables, the older data in $F_0$ shadows the newer data in $F_1$. This causes:
1. **Stale Reads:** An older value in $F_0$ masks an updated value committed in $F_1$.
2. **Tombstone Resurrection:** An older Put in $F_0$ resurrects a key deleted by a tombstone in $F_1$.
3. **Stale Deletion Masking:** An older Delete in $F_0$ masks a newly recreated key committed in $F_1$.
4. **Transaction Conflict Detection Failure:** `latestValueLocked` returns the sequence number from $F_0$ rather than the higher sequence number in $F_1$, allowing uncommitted transactions to violate serializability.

---

## 2. Component-by-Component Trace

### 2.1 Background Worker Loop (`flushLoop`)
In `lsmengine/manager.go` (lines 256–277):
```go
func (m *Manager) flushLoop() {
    defer m.flushWg.Done()
    for {
        m.flushQueueMu.Lock()
        for len(m.flushQueue) == 0 && !m.flushQueueClosed {
            m.flushQueueCond.Wait()
        }
        if len(m.flushQueue) == 0 && m.flushQueueClosed {
            m.flushQueueMu.Unlock()
            return
        }
        mem := m.flushQueue[0]
        m.flushQueue[0] = nil // allow GC
        m.flushQueue = m.flushQueue[1:]
        if len(m.flushQueue) == 0 {
            m.flushQueue = nil
        }
        m.flushQueueMu.Unlock()

        m.flushToDisk(mem)
    }
}
```
**Observation:**
- `flushLoop` executes indefinitely until `m.flushQueueClosed` is signaled and the queue is empty.
- After calling `m.flushToDisk(mem)`, it loops immediately to dequeue the next item in `m.flushQueue`.
- It performs **no check** on whether `m.flushToDisk` returned an error or whether `m.flushErr != nil`.
- The worker does NOT halt or enter a failed state upon encountering an error; it blindly processes subsequent queued flushes.

---

### 2.2 SSTable Build Failure Handling (`flushToDisk`)
In `lsmengine/manager.go` (lines 409–488):
```go
func (m *Manager) flushToDisk(mem *Memtable) {
    if m.flushBuildHook != nil {
        if err := m.flushBuildHook(); err != nil {
            m.Mu.Lock()
            if m.flushErr == nil {
                m.flushErr = err
            }
            m.Mu.Unlock()
            return
        }
    }

    filename := fmt.Sprintf("data_%d_%d.sst", time.Now().UnixNano(), sstableFileSeq.Add(1))
    file, err := os.Create(filename)
    if err != nil {
        m.Mu.Lock()
        if m.flushErr == nil {
            m.flushErr = err
        }
        m.Mu.Unlock()
        return
    }

    builder := &SSTableBuilder{
        File:        file,
        BlockBuffer: new(bytes.Buffer),
    }

    curr := mem.SK.headNode.Forward[0]
    for curr != nil {
        builder.Add(curr.Key, curr.Value)
        curr = curr.Forward[0]
    }

    if err := builder.Close(); err != nil {
        os.Remove(filename)
        m.Mu.Lock()
        if m.flushErr == nil {
            m.flushErr = err
        }
        m.Mu.Unlock()
        return
    }

    reader, err := NewSSTableReader(filename)
    if err != nil {
        os.Remove(filename)
        m.Mu.Lock()
        if m.flushErr == nil {
            m.flushErr = err
        }
        m.Mu.Unlock()
        return
    }

    if m.beforeFlushPublishHook != nil {
        m.beforeFlushPublishHook()
    }

    m.Mu.Lock()
    defer m.Mu.Unlock()

    if m.state == EngineClosed {
        reader.Close()
        os.Remove(filename)
        return
    }

    m.ActiveSSreaders = append(m.ActiveSSreaders, reader)

    for i, f := range m.Frozenmem {
        if f == mem {
            m.Frozenmem = append(m.Frozenmem[:i], m.Frozenmem[i+1:]...)
            break
        }
    }
}
```
**Observation:**
- When an error occurs during SSTable creation, build, or reader initialization:
  1. The incomplete file is cleaned up (`os.Remove(filename)`).
  2. `m.flushErr` is updated under `m.Mu.Lock()`.
  3. The function returns immediately.
- Critically, **`mem` is retained in `m.Frozenmem`** because the removal loop (lines 482–487) is bypassed.
- If a later flush $F_1$ succeeds, its reader is published to `m.ActiveSSreaders`, and $F_1$ is removed from `m.Frozenmem`.
- **Resulting State**:
  - `m.Frozenmem` contains $[F_0]$.
  - `m.ActiveSSreaders` contains $[R_1]$ (representing $F_1$).
  - Chronologically: $F_0 < F_1$.
  - Tier location: $F_0 \in \text{Tier 2 (Frozenmem)}$, $R_1 \in \text{Tier 3 (ActiveSSreaders)}$.

---

### 2.3 Read Path Semantics (`Manager.Get`)
In `lsmengine/manager.go` (lines 84–145):
```go
func (m *Manager) Get(key string, readTs uint64) (string, error) {
    ...
    // Tier 1: Check active Memtable
    sol, err := m.Mem.Get(byteKey, readTs)
    if err == nil {
        return string(sol), nil
    } ...

    // Tier 2: Check frozen Memtables (newest to oldest)
    for i := len(m.Frozenmem) - 1; i >= 0; i-- {
        val, err := m.Frozenmem[i].Get(byteKey, readTs)
        if err != nil {
            if err == ErrKeyDeleted {
                return " ", err
            } else if err == ErrNotFound {
                continue
            } else {
                return "", err
            }
        } else {
            return string(val), nil
        }
    }

    // Tier 3: Check ActiveSSreaders (newest to oldest)
    for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
        val, err := m.ActiveSSreaders[i].Get(byteKey, readTs)
        ...
    }
    return " ", ErrNotFound
}
```
**Observation:**
- `Manager.Get` implements strict tier-by-tier precedence:
  $$\text{Tier 1 (Mem)} \succ \text{Tier 2 (Frozenmem)} \succ \text{Tier 3 (ActiveSSreaders)}$$
- If $F_0$ (older) is in Tier 2 and $R_1$ (newer) is in Tier 3:
  - If `key` is present in $F_0$ with sequence number $S_0 \le \text{readTs}$, `m.Frozenmem[0].Get(...)` succeeds.
  - `Manager.Get` returns the value or error from $F_0$ **immediately**.
  - Tier 3 (`m.ActiveSSreaders`) is **never inspected**.
- Even though $R_1$ contains a more recent committed write ($S_1 > S_0$), $R_1$ is completely masked by $F_0$.

---

### 2.4 Write-Write Conflict Detection (`Manager.latestValueLocked`)
In `lsmengine/manager.go` (lines 201–246):
```go
    // Tier 1: Check active Memtable.
    seq, err := m.Mem.LatestValue(userKey)
    if err == nil {
        return seq, nil
    } ...

    // Tier 2: Check frozen Memtables (newest to oldest).
    for i := len(m.Frozenmem) - 1; i >= 0; i-- {
        seq, err := m.Frozenmem[i].LatestValue(userKey)
        if err == nil {
            return seq, nil
        } ...
    }

    // Tier 3: Check all on-disk SSTables
    for _, reader := range m.ActiveSSreaders {
        ...
    }
```
**Observation:**
- `latestValueLocked` returns immediately as soon as a key is found in Tier 2 (`m.Frozenmem`).
- If $F_0$ contains sequence $S_0$, it returns $S_0$.
- It never inspects Tier 3 (`m.ActiveSSreaders`), missing the higher sequence $S_1$ in $R_1$.
- Any transaction attempting to commit with read timestamp $T_{\text{read}} \in [S_0, S_1)$ will observe $\text{latestSeq} = S_0 \le T_{\text{read}}$ and commit successfully, failing to detect a write-write conflict with $S_1$.

---

### 2.5 Lifecycle and Error Propagation (`m.flushErr`, Admission, Shutdown)
1. **Flush Admission:**
   `EnqueueFlush`, `TriggerFlush`, and `checkAndTriggerFlush` check only `m.state == EngineOpen`. They do not inspect `m.flushErr`. Flush tasks continue to be admitted even after a fatal disk error.
2. **Read / Write APIs:**
   `Manager.Get`, `Manager.Begin`, and `Transaction.Commit` do not check `m.flushErr`. The engine operates silently in a degraded/corrupted state.
3. **Shutdown (`Manager.Close`):**
   `Manager.Close()` waits for `m.flushWg.Wait()`. Because the background worker continues processing, all remaining tasks in `m.flushQueue` are attempted. When the worker finishes, `m.ActiveSSreaders` contains readers from successful flushes, while `m.Frozenmem` retains failed Memtables. `Close()` closes `m.ActiveSSreaders` and `m.Log.File`, and returns `m.flushErr`. The retained Memtables in `m.Frozenmem` are dropped in memory.

---

## 3. Detailed Failure Scenario Walkthrough

Consider three sequential flushes $F_0, F_1, F_2$:

| Generation | Memtable | Operation on Key `K` | Sequence | Destination Tier if Succeeded |
| :--- | :--- | :--- | :--- | :--- |
| **Gen 0** | $F_0$ | `Put(K, "v0")` | Seq 10 | Tier 3 (SSTable $R_0$) |
| **Gen 1** | $F_1$ | `Put(K, "v1_updated")` | Seq 20 | Tier 3 (SSTable $R_1$) |
| **Gen 2** | $F_2$ | `Delete(K)` | Seq 30 | Tier 3 (SSTable $R_2$) |

Suppose $F_0$ encounters a disk write error during SSTable creation.
- $F_0$ fails and remains in `m.Frozenmem`.
- $F_1$ succeeds and is published to `m.ActiveSSreaders` as $R_1$; removed from `m.Frozenmem`.
- $F_2$ succeeds and is published to `m.ActiveSSreaders` as $R_2$; removed from `m.Frozenmem`.

Now consider point reads for key `K`:
- Read at `readTs = 25`:
  - Expected: `"v1_updated"` (from $F_1$, Seq 20).
  - Actual: `Manager.Get` checks Tier 2 (`m.Frozenmem`), finds $F_0$ (Seq 10 $\le 25$), and returns `"v0"`. **STALE READ.**
- Read at `readTs = 35`:
  - Expected: `ErrKeyDeleted` (from $F_2$, Seq 30).
  - Actual: `Manager.Get` checks Tier 2 (`m.Frozenmem`), finds $F_0$ (Seq 10 $\le 35$), and returns `"v0"`. **TOMBSTONE RESURRECTION.**

---

## 4. Architectural Invariant Violations

1. **`INV-ORDER-1` (Chronological Reader Order):**
   Assumed that all elements in `m.Frozenmem` are strictly younger than all elements in `m.ActiveSSreaders`. A failed flush followed by a successful flush violates this invariant by straddling older data in `Frozenmem` above newer data in `ActiveSSreaders`.
2. **`INV-MVCC-6` (Monotonic Version Resolution):**
   Assumed that searching Tier 1 $\to$ Tier 2 $\to$ Tier 3 monotonically searches from newest to oldest version. This invariant fails when an older generation remains in Tier 2 while newer generations migrate to Tier 3.

---

## 5. Audit Conclusions & Plan for Sub-phase I1.4.7b

1. **Defect Confirmed**: The failure semantics allow subsequent flushes to publish out of precedence when an earlier flush fails.
2. **Issue Identifier**: This defect will be formally cataloged as **`E-010` (Flush Failure Precedence Inversion)** in `research/logs/ISSUES.md`.
3. **Reproduction Strategy (I1.4.7b)**:
   - Construct a targeted test suite in `lsmengine/failed_flush_precedence_test.go` utilizing `flushBuildHook` to deterministically fail $F_0$ while allowing $F_1$ and $F_2$ to proceed.
   - Assert expected behavior using an independent reference oracle.
   - Capture and preserve failing baseline logs on Windows and WSL2.
