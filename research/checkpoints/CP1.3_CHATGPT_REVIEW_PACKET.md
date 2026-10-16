# CP1.3 Review Packet for External Reviewer (ChatGPT)

**Phase:** Phase I1 (Approach A Baseline — Reader-Compactor Synchronization)  
**Milestone:** Checkpoint CP1.3 (Reader Lifetime, Concurrency Hazards & Safe Retirement)  
**Date:** 2026-10-16  
**Git Branch:** `research/reader-compactor-sync`  
**Latest Commit:** Pending commit (`I1.3.5 / CP1.3`)  
**Operating System Tested:** Windows 11 amd64 (NTFS) & WSL2 Ubuntu (Linux 5.15 amd64)  
**Go Version:** `go1.27.1`  

---

## 1. Issue E-005: Before & After Reproduction

### Problem Statement & Root Cause
In `owndbms/lsmengine`, all on-disk SSTables reside in a single flat slice: `m.ActiveSSreaders []*SSTableReader`. Point lookup ([`Manager.Get`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L87)) iterates backwards from `len-1` down to 0, relying on [`INV-ORDER-1`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/invariants.md#L81) (oldest to newest).

If compaction selects noncontiguous inputs $S_{\text{inputs}} = \{ R_0, R_2 \}$ (skipping $R_1$):
- Output $M$ merges $\{R_0, R_2\}$, keeping the newest update at $Seq = 30$.
- Replacing $\{R_0, R_2\}$ in `[R0, R1, R2]` yields `[M, R1]`.
- Reverse search in `Manager.Get` visits $R_1$ ($Seq = 20$) **before** $M$ ($Seq = 30$), returning stale data `"v1_mid"`.

### Before-Fix Reproduction Log
Archived in [`research/debug/E-005/failing_counterexample.log`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/debug/E-005/failing_counterexample.log):
```text
=== RUN   TestCounterexample_NoncontiguousCompactionViolatesOrder
    noncontiguous_compaction_counterexample_test.go:85: Result of Manager.Get after noncontiguous compaction: "v1_mid"
    noncontiguous_compaction_counterexample_test.go:88: COUNTEREXAMPLE PROVEN: Noncontiguous compaction resulted in stale read "v1_mid" (expected "v2_new"). r1 (seq 20) masked newer compacted version mCompact (seq 30) because mCompact was placed before r1 in the flat slice!
--- FAIL: TestCounterexample_NoncontiguousCompactionViolatesOrder (0.02s)
FAIL
```

### After-Fix Reproduction Log
Archived in [`research/debug/E-005/after.log`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/debug/E-005/after.log):
```text
=== RUN   TestE005_NoncontiguousCompactionRejected
--- PASS: TestE005_NoncontiguousCompactionRejected (0.02s)
=== RUN   TestE005_NonPrefixCompactionRejected
--- PASS: TestE005_NonPrefixCompactionRejected (0.03s)
=== RUN   TestE005_DuplicateInputsRejected
--- PASS: TestE005_DuplicateInputsRejected (0.01s)
=== RUN   TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility
--- PASS: TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility (0.02s)
PASS
ok  	owndbms/lsmengine	1.045s
```

---

## 2. Exact Contiguous-Input Validation Code & Tests

### Implementation in `lsmengine/compaction.go`:
```go
func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader, newReader *SSTableReader) ([]*SSTableReader, error) {
	if len(inputs) == 0 {
		return current, nil
	}

	// 1. Ensure each input reader appears exactly once (no duplicates) and is non-nil
	inputSet := make(map[*SSTableReader]bool, len(inputs))
	for _, r := range inputs {
		if r == nil {
			return nil, errors.New("compaction aborted: nil input reader")
		}
		if inputSet[r] {
			return nil, errors.New("compaction aborted: duplicate input reader in inputs")
		}
		inputSet[r] = true
	}

	// 2. Verify all inputs are present in current
	currentSet := make(map[*SSTableReader]bool, len(current))
	for _, r := range current {
		currentSet[r] = true
	}
	for _, r := range inputs {
		if !currentSet[r] {
			return nil, errors.New("compaction aborted: input reader is no longer active")
		}
	}

	// 3. Verify inputs preserve the full-prefix policy (must start at index 0)
	if current[0] != inputs[0] {
		return nil, errors.New("compaction aborted: inputs must start at index 0 to preserve full-prefix policy")
	}

	// 4. Verify inputs form a strictly contiguous range in current
	for j := 0; j < len(inputs); j++ {
		if current[j] != inputs[j] {
			return nil, errors.New("compaction aborted: inputs are not contiguous in current active readers")
		}
	}

	// 5. Construct updated reader set:
	// New reader is published at index 0, followed by all concurrently flushed readers.
	updated := make([]*SSTableReader, 0, len(current)-len(inputs)+1)
	if newReader != nil {
		updated = append(updated, newReader)
	}
	updated = append(updated, current[len(inputs):]...)

	return updated, nil
}
```

### Passing Regression Tests (`lsmengine/noncontiguous_compaction_counterexample_test.go`):
- `TestE005_NoncontiguousCompactionRejected`: Proves $[R_0, R_2]$ is rejected with `"inputs are not contiguous in current active readers"`, leaving `current` and `Manager.Get` uncorrupted.
- `TestE005_NonPrefixCompactionRejected`: Proves contiguous range $[R_1, R_2]$ (not starting at index 0) is rejected under full-prefix policy.
- `TestE005_DuplicateInputsRejected`: Proves duplicate inputs $[R_0, R_0]$ are rejected.
- `TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility`: Proves valid prefix $[R_0, R_1]$ replacement with concurrent flush $R_{\text{flush}}$ preserves chronological order, correctly hides tombstones, and honors MVCC `ReadTs`.

---

## 3. Proof & Limitations of Chronological Reader Ordering

### Theoretical Proof for Flat-Slice Architecture:
In a flat reader slice $A = [R_0, R_1, \dots, R_{N-1}]$, point read `Get` executes reverse linear scan:
$$\text{VisKey}(k, T_{\text{read}}) = \text{first visible record in } R_i \text{ for } i = N-1 \text{ down to } 0$$
Let generation timestamp $g(R_i) < g(R_j)$ for $i < j$.
1. If compaction selects a contiguous prefix $S = [R_0, \dots, R_{k-1}]$, the merged output $M$ has generation $g(M)$ spanning the range $[0, k-1]$.
2. Placing $M$ at index 0 produces $[M, R_k, \dots, R_{N-1}]$.
3. For any $j \ge k$, $g(R_j) > g(M)$, so reverse scan evaluates $R_j$ before $M$. Chronological order is strictly preserved.
4. Conversely, if $S$ is noncontiguous (e.g. $\{R_0, R_2\}$ skipping $R_1$), then $g(R_0) < g(R_1) < g(R_2)$. Output $M$ merges versions from $R_2$ ($g(R_2) > g(R_1)$). If $M$ is placed at index 0 (`[M, R1]`), reverse scan checks $R_1$ first, so $R_1$ masks $R_2$'s updates. If $M$ is placed at index 1 (`[R1, M]`), reverse scan checks $M$ first, so $R_0$'s old versions in $M$ mask $R_1$'s newer updates. $\blacksquare$

### Documented Limitations:
- Selective middle-range compaction (e.g. compacting only intermediate tables) is **mathematically impossible** in a single flat reader slice without migrating to a leveled/tiered LSM layout (L0, L1, etc.).
- Compaction in `owndbms/lsmengine` is strictly constrained to **contiguous prefixes** (`INV-COMPACT-1`).

---

## 4. Reader Ownership & Lifetime State Diagram

```mermaid
stateDiagram-v2
    [*] --> Constructing: Background Flush or Compaction
    Constructing --> PrivateReady: NewSSTableReader() succeeds
    Constructing --> AbortedCleaned: Error -> os.Remove(tempFile)
    AbortedCleaned --> [*]

    PrivateReady --> Active: Published in m.ActiveSSreaders under m.Mu.Lock()
    
    state Active {
        [*] --> ServingReads: Point Read Borrow (m.Mu.RLock)
        ServingReads --> ServingReads: latestValueLocked
    }

    Active --> CompactingLease: CompactAll captures contiguous prefix under compactionMu & m.Mu.Lock()
    
    state CompactingLease {
        [*] --> MergingOutsideLock: K-Way Merge & Builder Add
        MergingOutsideLock --> IterationError: I/O Error -> Close iterators & abort
        MergingOutsideLock --> PublicationReady: Temp SSTable built & verified
    }

    IterationError --> Active: Readers remain active; temp files deleted
    PublicationReady --> Replaced: In-place prefix swap under m.Mu.Lock()
    Replaced --> Drained: Active RLock holders complete lookup
    Drained --> Closed: r.Close() (all handles closed)
    Closed --> Unlinked: os.Remove(r.File.Name())
    Unlinked --> [*]
```

### Ownership Transition Matrix Across All Paths
| Component | Creates Reader? | Owns Reader? | Locks Protecting Access | Can Reference Escape? | Can Reader be Closed While Reachable? | Leak Risk on Error? |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **`flushToDisk`** | Yes | Private $\to$ Published to `Manager` | `Manager.Mu.Lock()` on swap | No (pointer appended to slice) | No (appended to active slice) | No: builder error unlinks temp file |
| **`CompactAll`** | Yes | Private $\to$ Published to `Manager`; owns retired slice during cleanup | `m.compactionMu.Lock()` across lifecycle; `m.Mu.Lock()` on swap | **Yes (retained during merge outside `m.Mu`)** | No: protected by `compactionMu` and `replaceCompactedReaders` | No: all error paths close iterators and unlink temp files |
| **`Manager.Get`** | No | Borrowed from `m.ActiveSSreaders` | `m.Mu.RLock()` held across entire query | No: returns copied `string(val)` | No: holds `RLock()`, blocking publication `Lock()` | N/A (read-only) |
| **`latestValueLocked`**| No | Borrowed from `m.ActiveSSreaders` | Caller holds `m.Mu.Lock()` or `m.Mu.RLock()` | No: returns primitive `uint64` | No: caller holds lock | N/A (read-only) |
| **`Transaction.Commit`**| No | Indirect via `latestValueLocked` | `t.Man.Mu.Lock()` held across commit | No: operates strictly under lock | No | N/A |
| **`Manager.Close`** | No | `Manager` | `m.compactionMu.Lock()` $\to$ `m.Mu.Lock()` | No | No: waits for in-flight compactions; sets `isClosed = true` | No |

---

## 5. Read-Versus-Compaction Overlap Test (Case C)

In [`lsmengine/reader_lifetime_test.go:TestReaderLifetime_CaseC_ReadDuringRetirement`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/reader_lifetime_test.go#L12):
1. `Manager.Get` begins and pauses inside `duringPointReadHook` while holding `m.Mu.RLock()`.
2. `CompactAll` runs in a separate goroutine and attempts to acquire `m.Mu.Lock()` for publication.
3. Verification confirms compaction publication **blocks** and cannot close or unlink $R_0, R_1$ while `Get` holds `RLock()`.
4. Release `Get`: read completes cleanly, returning `"val1"`.
5. Compaction acquires `m.Mu.Lock()`, completes publication, and unlinks retired files.
- **Result**: `PASS (0.08s)`.

---

## 6. Concurrent Compaction Lifetime Test (Case A) & Defect E-006

In [`lsmengine/reader_lifetime_test.go:TestReaderLifetime_CaseA_OverlappingCompactions`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/reader_lifetime_test.go#L157):
1. Compaction A starts, captures $[R_0, R_1]$, and pauses during merge outside `m.Mu`.
2. Compaction B is started concurrently in another goroutine.
3. **Without `compactionMu` (Reproduction)**: Compaction B entered `CompactAll`, captured the same readers, raced on disk files, and caused aborted compactions or Windows sharing violations.
4. **With `compactionMu` (Fix)**: Compaction B blocks on `m.compactionMu.Lock()`. When Compaction A finishes and publishes $M_A$, Compaction B enters, observes `len(m.ActiveSSreaders) == 1`, and returns `nil` cleanly without conflict or data loss.
- **Result**: `PASS (0.08s)`.

---

## 7. Shutdown-Versus-Compaction Test (Case B) & Defect E-006

In [`lsmengine/reader_lifetime_test.go:TestReaderLifetime_CaseB_ShutdownDuringCompaction`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/reader_lifetime_test.go#L248):
1. Compaction starts, captures $[R_0, R_1]$, and pauses during merge.
2. `Manager.Close()` is called in the background.
3. **Without coordination (Reproduction)**: `Close()` closed $R_0, R_1$ immediately. When compaction resumed, it published into a closed engine, double-closed readers, and leaked $M$.
4. **With coordination (Fix)**: `Close()` acquires `m.compactionMu.Lock()` and waits for in-flight compaction to finish. Compaction publishes $M$, and `Close()` then closes $M$, sets `isClosed = true`, and sets `m.ActiveSSreaders = nil`.
- **Result**: `PASS (0.09s)`.

---

## 8. Windows Close/Unlink & Filesystem Cleanup Validation (I1.3.4)

Verified in [`lsmengine/filesystem_cleanup_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/filesystem_cleanup_test.go):
1. **`TestFilesystem_CloseBeforeDelete`**: Strictly closes all open iterators and file handles before `os.Remove`. Verified zero `ERROR_SHARING_VIOLATION` errors on Windows NTFS.
2. **`TestFilesystem_CleanupAfterAbortedCompaction`**: Forced validation abort during publication; verified that temporary `data_compacted_*.sst` files are immediately deleted, while all active SSTables remain intact on disk.
3. **`TestFilesystem_NoAccidentalActiveFileDeletion`**: Verified that concurrently flushed tables appended during compaction are preserved on disk and in the active slice.
4. **`TestFilesystem_ShutdownWithInflightOperations`**: Closed manager and verified entire database directory can be removed with `os.RemoveAll`, confirming zero leaked file handles on Windows.
- **Result**: `PASS (0.05s)`.

---

## 9. WSL2 Linux Race Detector Results (32 / 32 Tests Passing)

Executed via WSL2 Ubuntu 22.04 (`go1.27.1 linux/amd64`):
```bash
go test -v -race ./lsmengine
```

**Output**:
```text
=== RUN   TestCompactionPublication_ConcurrentFlushOverlap
--- PASS: TestCompactionPublication_ConcurrentFlushOverlap (0.09s)
=== RUN   TestCompactionPublication_ConcurrentFlushNewerVersion
--- PASS: TestCompactionPublication_ConcurrentFlushNewerVersion (0.11s)
=== RUN   TestCompactionPublication_ConcurrentFlushTombstone
--- PASS: TestCompactionPublication_ConcurrentFlushTombstone (0.08s)
=== RUN   TestCompactionPublication_OverlappingInputsValidation
--- PASS: TestCompactionPublication_OverlappingInputsValidation (0.08s)
=== RUN   TestCrossTierWriteConflict_FlushedSSTable
--- PASS: TestCrossTierWriteConflict_FlushedSSTable (0.04s)
=== RUN   TestCrossTierWriteConflict_FlushedTombstone
--- PASS: TestCrossTierWriteConflict_FlushedTombstone (0.05s)
=== RUN   TestCrossTierWriteConflict_NonConflicting
--- PASS: TestCrossTierWriteConflict_NonConflicting (0.04s)
=== RUN   TestSkipListLevelDistribution
--- PASS: TestSkipListLevelDistribution (0.00s)
=== RUN   TestBasicPutGet
--- PASS: TestBasicPutGet (0.03s)
=== RUN   TestDeleteAndTombstone
--- PASS: TestDeleteAndTombstone (0.02s)
=== RUN   TestWALRecovery
--- PASS: TestWALRecovery (0.21s)
=== RUN   TestWriteWriteConflict
--- PASS: TestWriteWriteConflict (0.03s)
=== RUN   TestFilesystem_CloseBeforeDelete
--- PASS: TestFilesystem_CloseBeforeDelete (0.05s)
=== RUN   TestFilesystem_CleanupAfterAbortedCompaction
--- PASS: TestFilesystem_CleanupAfterAbortedCompaction (0.06s)
=== RUN   TestFilesystem_NoAccidentalActiveFileDeletion
--- PASS: TestFilesystem_NoAccidentalActiveFileDeletion (0.08s)
=== RUN   TestFilesystem_ShutdownWithInflightOperations
--- PASS: TestFilesystem_ShutdownWithInflightOperations (0.04s)
=== RUN   TestI1_2_0_MaxUint64EncodingBehavior
--- PASS: TestI1_2_0_MaxUint64EncodingBehavior (0.00s)
=== RUN   TestI1_2_0_MultipleSSTablesDifferentVersions
--- PASS: TestI1_2_0_MultipleSSTablesDifferentVersions (0.04s)
=== RUN   TestI1_2_0_TombstoneAndSubsequentPut
--- PASS: TestI1_2_0_TombstoneAndSubsequentPut (0.05s)
=== RUN   TestI1_2_0_KeySpanningMultipleBlocks
--- PASS: TestI1_2_0_KeySpanningMultipleBlocks (0.04s)
=== RUN   TestI1_2_0_MissingKeyVsIOError
--- PASS: TestI1_2_0_MissingKeyVsIOError (0.03s)
=== RUN   TestI1_2_0_SSTableOrderingAnomaly
--- PASS: TestI1_2_0_SSTableOrderingAnomaly (0.04s)
=== RUN   TestE004_TxnConflict_NewestVersionNotInLastSSTable
--- PASS: TestE004_TxnConflict_NewestVersionNotInLastSSTable (0.05s)
=== RUN   TestManagerGet_OrderingAudit
--- PASS: TestManagerGet_OrderingAudit (0.04s)
=== RUN   TestE005_NoncontiguousCompactionRejected
--- PASS: TestE005_NoncontiguousCompactionRejected (0.06s)
=== RUN   TestE005_NonPrefixCompactionRejected
--- PASS: TestE005_NonPrefixCompactionRejected (0.06s)
=== RUN   TestE005_DuplicateInputsRejected
--- PASS: TestE005_DuplicateInputsRejected (0.03s)
=== RUN   TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility
--- PASS: TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility (0.06s)
=== RUN   TestReaderLifetime_CaseC_ReadDuringRetirement
--- PASS: TestReaderLifetime_CaseC_ReadDuringRetirement (0.14s)
=== RUN   TestReaderLifetime_CaseD_FailurePaths
--- PASS: TestReaderLifetime_CaseD_FailurePaths (0.04s)
=== RUN   TestReaderLifetime_CaseA_OverlappingCompactions
--- PASS: TestReaderLifetime_CaseA_OverlappingCompactions (0.11s)
=== RUN   TestReaderLifetime_CaseB_ShutdownDuringCompaction
--- PASS: TestReaderLifetime_CaseB_ShutdownDuringCompaction (0.11s)
PASS
ok  	owndbms/lsmengine	2.951s
```
**Static Analysis (`go vet ./...`)**: 0 errors, clean exit.

---

## 10. Performance & Synchronization Trade-offs (Approach A)

1. **Lock Hierarchy**:
   `m.compactionMu` (Mutex) $\to$ `Manager.Mu` (Exclusive / Shared) $\to$ `Wal.mu` (Mutex).
   - Strict hierarchical order eliminates all risk of deadlock or lock inversion.
2. **Read & Write Independence**:
   - `Manager.Get` acquires only `m.Mu.RLock()`.
   - `Transaction.Commit` acquires only `m.Mu.Lock()` and `Wal.mu`.
   - `flushToDisk` acquires only `m.Mu.Lock()`.
   - Neither reads, writes, nor flushes ever acquire `m.compactionMu`.
3. **Approach A Bottleneck Confirmed**:
   - Because `Manager.Get` must hold `m.Mu.RLock()` during disk I/O to prevent the reader from being closed underneath it, concurrent reads stall compaction publication.
   - This empirical finding directly confirms the thesis of the research paper: **Approach A suffers reader-compactor head-of-line blocking**, motivating Approach B's lock-free immutable snapshots.

---

## 11. Remaining Correctness and MVCC Risks

1. **Single Flat Slice Constraint**: Compaction cannot select non-prefix sub-slices without violating reader ordering (`INV-COMPACT-1`). Multi-tier compaction requires migrating to LevelDB/RocksDB-style leveled architecture.
2. **WAL Catalog across Rotations**: WAL replay is verified for single logs, but recovering across rotated logs (`wal_<timestamp>.log`) requires a manifest.
3. **Approach A Long Query Contention**: Long range scans holding `RLock` will block compaction swaps and transaction commits.

---

## 12. Review Questions for External Reviewer

1. Does the dedicated compaction mutex (`compactionMu`) and shutdown coordination satisfy the requirements for safe reader retirement in Approach A?
2. Are you satisfied with the proof and documentation regarding the flat-slice restriction (`INV-COMPACT-1`)?
3. With 32/32 tests passing on Windows and WSL2 with zero data races, do you authorize proceeding to **I1.4 (Audit and Benchmark Approach A Baseline)** or **I1.5 (Approach A Checkpoint)**?

---

**EXTERNAL REVIEW REQUIRED — CP1.3 AWAITING APPROVAL.**
