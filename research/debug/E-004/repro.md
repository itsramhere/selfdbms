# Reproduction and Investigation: Issue E-004

**Title:** `latestValueLocked` Fails to Identify Greatest Sequence Number when SSTable Ordering Changes  
**Sub-phase Discovered:** I1.2.0 (Invariant Verification)  
**Status:** REPRODUCED / AWAITING AUTHORIZATION FOR REPAIR  
**Impact:** If `ActiveSSreaders` contains SSTables whose order deviates from strict chronological sequence (for example, if compaction publishes a merged SSTable at the end of the slice after concurrent flushes, or during multi-level SSTable reorganization), `latestValueLocked` returns the sequence number from the SSTable with the highest slice index rather than the greatest sequence number across all SSTables. A transaction committing concurrently with $ReadTs$ between the stale sequence number and the true committed sequence number will fail to detect the write-write conflict and silently overwrite committed data.

---

## 1. Problem Description & Root Cause

In `Manager.latestValueLocked` ([`lsmengine/manager.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L142-L151)):
```go
	for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
		seq, err := m.ActiveSSreaders[i].LatestValue(userKey)
		if err == nil {
			return seq, nil
		} else if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return 0, err
		}
	}
```

The loop iterates in reverse order and immediately returns `seq, nil` upon the first SSTable that contains `userKey`.

### The Defect Scenario:
Suppose two SSTables exist:
- `sstA`: contains `k` with sequence number 50.
- `sstB`: contains `k` with sequence number 10.

If `m.ActiveSSreaders` is `[sstA, sstB]` (e.g. `sstB` is at index 1):
1. `latestValueLocked` inspects `m.ActiveSSreaders[1]` (`sstB`).
2. `sstB.LatestValue("k")` returns `seq = 10, err = nil`.
3. `latestValueLocked` returns `seq = 10, nil` immediately without checking `m.ActiveSSreaders[0]` (`sstA`).
4. A transaction $T$ with $ReadTs = 30$ evaluates:
   `latestSeqNo (10) > t.ReadTs (30)` $\implies$ `false`.
5. $T$ commits without detecting the write-write conflict, silently overwriting `seq = 50`.

---

## 2. Failing Test Output

From [`lsmengine/i1_2_0_invariants_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/i1_2_0_invariants_test.go#L318-L343) (`go test -v ./lsmengine -run TestI1_2_0_SSTableOrderingAnomaly`):
```text
=== RUN   TestI1_2_0_SSTableOrderingAnomaly
    i1_2_0_invariants_test.go:341: FAIL: conflict detector returned seq 10 instead of greatest committed sequence 50 when SSTable ordering changed
--- FAIL: TestI1_2_0_SSTableOrderingAnomaly (0.01s)
FAIL
FAIL	owndbms/lsmengine	0.929s
FAIL
```

---

## 3. Minimal Proposed Fix

Extend `latestValueLocked` in `lsmengine/manager.go` to scan all readers in `ActiveSSreaders` and track `maxSeq`:

```go
	var maxSeq uint64
	var found bool

	for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
		seq, err := m.ActiveSSreaders[i].LatestValue(userKey)
		if err == nil {
			if !found || seq > maxSeq {
				maxSeq = seq
				found = true
			}
		} else if errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return 0, err
		}
	}

	if found {
		return maxSeq, nil
	}
```

### Architectural Implications:
- **Resilience to Publication Reordering**: If compaction publishes an output table, conflict detection is guaranteed to find the highest sequence number regardless of where the table is placed in the slice.
- **Cost**: Only iterates in-memory slice `ActiveSSreaders`. For each table containing the key, does a sparse-index binary search (nanoseconds) and at most 1 block read. If tables do not contain the key, binary search returns `ErrNotFound` without block I/O.
- **Zero Lock Changes**: Executes under the already-held `Manager.Mu` lock.
