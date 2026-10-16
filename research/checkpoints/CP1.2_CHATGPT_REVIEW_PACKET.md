# CHECKPOINT CP1.2 — External Review Packet (ChatGPT)

**Project:** Reader–Compactor Synchronization in a Go LSM Storage Engine  
**Milestone:** Checkpoint CP1.2 — Protected Compaction Publication Complete (Sub-phases I1.2.0 through I1.2.5)  
**Target Git Branch:** `research/reader-compactor-sync`  
**Host Platform:** Windows 11 amd64 (AMD Ryzen 7 7445HS, NTFS NVMe SSD) / WSL2 Linux amd64 (Kernel 6.6, GCC 13.3)  
**Date:** 2026-10-16  

---

## 1. Executive Summary & Purpose

Following the provisional approval of Checkpoint CP0.75, this checkpoint reports the complete implementation, audit, and empirical verification of **Phase I1, Sub-phase I1.2 (Repair Protected Compaction Publication)**.

### Primary Achievements:
1. **Audit of CP0.75 Invariants & E-004 Resolution (Option A)**:
   - Audited sparse-index search, 56-bit sequence math, 3-block boundary traversal (11.5 KB payload), and I/O error handling.
   - Identified and deterministically reproduced **Issue E-004** (`latestValueLocked` returned on the first match and failed when SSTables were out of order).
   - Under authorized **Option A** ([`D-006`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/logs/DECISIONS.md)), repaired `latestValueLocked` to scan all `ActiveSSreaders` and track `maxSeq` while preserving RAM monotonic early-returns (`INV-MVCC-6`).
   - Audited `Manager.Get` and established that point reads strictly depend on `INV-ORDER-1` (ActiveSSreaders ordered chronologically from oldest to newest).
2. **Elimination of Compaction Data Loss Hazard**:
   - In baseline, `CompactAll` executed a blind slice assignment: `m.ActiveSSreaders = []*SSTableReader{newReader}`. Any SSTables concurrently published by `flushToDisk` during compaction were permanently destroyed and orphaned on disk.
   - Replaced this with an **identity-preserving publication protocol** ([`replaceCompactedReaders`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go#L14-L52)) under `m.Mu.Lock()`: validates input presence, replaces only selected inputs in-place, and preserves all concurrently flushed tables in strict chronological search order.
3. **Deterministic Concurrency Test Suite**:
   - Added channel-barrier hook `beforeCompactionPublishHook` to force concurrent flushes during the exact window between merge completion and publication.
   - Proved with zero time-based sleeps that:
     - Concurrently flushed SSTables are never lost.
     - Newer versions flushed during compaction remain visible and unmasked.
     - Flushed tombstones remain visible and unmasked.
     - Competing/overlapping compactions are safely aborted.
4. **Validation**:
   - `go vet ./...` clean.
   - 20/20 unit tests pass on native Windows 11 (`1.47s`).
   - 20/20 unit tests pass under WSL2 Linux amd64 ThreadSanitizer (`go test -race -v ./lsmengine`, `2.44s`) with **zero data races**.

---

## 2. Before / After Publication Diagrams & Sequence Diagram

### A. The Baseline Publication Race (Data Loss)

```
Compactor Goroutine                       Flush Worker Goroutine               Manager State (ActiveSSreaders)
───────────────────                       ──────────────────────               ──────────────────────────────
1. Captures inputs [A, B]                                                      [A, B]
   m.Mu.Unlock()
2. Performs merge outside lock                                                 [A, B]
                                          3. Memtable exceeds threshold
                                          4. Writes C.sst
                                          5. Acquires m.Mu.Lock()
                                          6. Appends C:
                                             ActiveSSreaders = [A, B, C]       [A, B, C]
                                          7. Releases m.Mu.Unlock()
8. Compactor completes M.sst
9. Acquires m.Mu.Lock()
10. Blind Assignment:
    ActiveSSreaders = [M]                 <── [CRITICAL DATA LOSS]:             [M]
11. Releases m.Mu.Unlock()                    SSTable C is permanently erased!
                                              Keys in C are lost from queries!
```

### B. Corrected Execution (Sub-phase I1.2 Identity-Preserving Publication)

```
Compactor Goroutine                       Flush Worker Goroutine               Manager State (ActiveSSreaders)
───────────────────                       ──────────────────────               ──────────────────────────────
1. Captures inputs [A, B]                                                      [A, B]
   m.Mu.Unlock()
2. Performs merge outside lock                                                 [A, B]
                                          3. Concurrent flush writes C.sst
                                          4. Acquires m.Mu.Lock()
                                          5. Appends C:
                                             ActiveSSreaders = [A, B, C]       [A, B, C]
                                          6. Releases m.Mu.Unlock()
7. Compactor completes M.sst
8. Acquires m.Mu.Lock()
9. replaceCompactedReaders:
   - Validates A and B still present
   - In-place replaces [A, B] with M
   - Preserves C at index 1:
     ActiveSSreaders = [M, C]             <── [CORRECT & SAFE]:                [M, C]
10. Releases m.Mu.Unlock()                    C is preserved;
11. Retires A and B safely                    Order [M, C] preserves INV-ORDER-1!
```

---

## 3. Exact Identity-Validation and Replacement Algorithm

In [`lsmengine/compaction.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction.go#L14-L52):

```go
func replaceCompactedReaders(current []*SSTableReader, inputs []*SSTableReader, newReader *SSTableReader) ([]*SSTableReader, error) {
	if len(inputs) == 0 {
		return current, nil
	}

	// 1. Build set of input identities
	inputSet := make(map[*SSTableReader]bool, len(inputs))
	for _, r := range inputs {
		inputSet[r] = true
	}

	// 2. Pre-publication Validation:
	// Verify that ALL selected inputs are still active members of current ActiveSSreaders.
	// If a concurrent compaction or administrative action removed any input, abort.
	currentSet := make(map[*SSTableReader]bool, len(current))
	for _, r := range current {
		currentSet[r] = true
	}
	for _, r := range inputs {
		if !currentSet[r] {
			return nil, errors.New("compaction aborted: input reader is no longer active")
		}
	}

	// 3. In-Place Identity Replacement:
	// Insert newReader at the slice position of the first replaced reader.
	// Any concurrently flushed SSTables remain after newReader, preserving chronological search order (INV-ORDER-1).
	updated := make([]*SSTableReader, 0, len(current)-len(inputs)+1)
	insertedNew := false

	for _, r := range current {
		if inputSet[r] {
			if !insertedNew {
				updated = append(updated, newReader)
				insertedNew = true
			}
			// Skip remaining compacted inputs (they are retired)
		} else {
			// Retain uncompacted and concurrently flushed readers
			updated = append(updated, r)
		}
	}

	return updated, nil
}
```

---

## 4. Deterministic Concurrency Reproduction & Evidence

All tests in [`lsmengine/compaction_concurrency_test.go`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/compaction_concurrency_test.go) use zero time-based sleeps. Interleaving is governed strictly by Go channels and the `beforeCompactionPublishHook`:

```go
	compactionPaused := make(chan struct{})
	resumeCompaction := make(chan struct{})

	man.beforeCompactionPublishHook = func() {
		close(compactionPaused)
		<-resumeCompaction
	}
```

### Verified Scenarios:
1. **`TestCompactionPublication_ConcurrentFlushOverlap`**:
   - Initial state: `[A, B]`.
   - Compaction merges A and B, pauses before publication.
   - Concurrent flush publishes C (`key_c` = `"val_c"`). Active readers become `[A, B, C]`.
   - Compaction resumes.
   - **Result**: Active readers become `[M, C]`. Both `key_a`, `key_b`, and `key_c` are verified accessible via point reads (`man.Get`).
2. **`TestCompactionPublication_ConcurrentFlushNewerVersion`**:
   - Key `"k"` initialized in A (`"v_initial"`).
   - Compaction merges A and B.
   - Concurrent flush writes newer version `"k"` = `"v_updated"` into C.
   - Compaction resumes and publishes M.
   - **Result**: `man.Get("k", 1000)` returns `"v_updated"` (proves C is not masked by M).
3. **`TestCompactionPublication_ConcurrentFlushTombstone`**:
   - Key `"del_key"` initialized in A.
   - Concurrent flush writes deletion tombstone for `"del_key"` into C.
   - Compaction resumes and publishes M.
   - **Result**: `man.Get("del_key", 1000)` returns `ErrKeyDeleted` (tombstone in C correctly masks older value in M).
4. **`TestCompactionPublication_OverlappingInputsValidation`**:
   - Compaction 1 selects `[A, B]`.
   - While paused, input A is removed.
   - Compaction 1 resumes: validation detects missing reader, aborts with `"compaction aborted: input reader is no longer active"`, deletes partial file, and leaves manager state uncorrupted.

---

## 5. CP0.75 Invariant Audit Results & Defect E-004

In Sub-phase I1.2.0, we conducted a rigorous 7-point audit of CP0.75's version lookup invariants:

1. **Sparse-Index Search**: Verified binary search logic on sparse index.
2. **SSTable Ordering Anomaly (E-004)**:
   - **Defect Identified**: `latestValueLocked` returned on the first SSTable match. When SSTables were inverted (`[sstA(seq=50), sstB(seq=10)]`), it returned 10 instead of 50.
   - **Reproduction**: Captured in [`research/debug/E-004/failing_test.log`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/debug/E-004/failing_test.log).
   - **Resolution (Option A - D-006)**: Modified `latestValueLocked` to scan all `ActiveSSreaders` and track `maxSeq`. Verified by `TestI1_2_0_SSTableOrderingAnomaly` and `TestE004_TxnConflict_NewestVersionNotInLastSSTable`.
3. **Multi-Version Resolution**: Verified across multiple SSTables (`TestI1_2_0_MultipleSSTablesDifferentVersions`).
4. **Tombstone Sequencing**: Verified tombstone followed by subsequent put (`TestI1_2_0_TombstoneAndSubsequentPut`).
5. **Multi-Block Boundary Traversal**: Generated 50 versions spanning 3 index blocks (11.5 KB payload). Confirmed `LatestValue` accurately found the highest sequence number (`TestI1_2_0_KeySpanningMultipleBlocks`).
6. **`math.MaxUint64` & Sequence Space**: Verified that `EncodeInternalKey` operates on a 56-bit sequence space (`seqNo << 8`). Inverted trailer `0x00000000000000FE` is mathematically $\le$ all valid key trailers (`TestI1_2_0_MaxUint64EncodingBehavior`).
7. **I/O Error Distinction**: Confirmed `ErrNotFound` is cleanly distinguished from physical OS I/O read errors (`TestI1_2_0_MissingKeyVsIOError`).
8. **`Manager.Get` Ordering Audit**: Demonstrated via `TestManagerGet_OrderingAudit` that `Manager.Get` relies strictly on `INV-ORDER-1` (ActiveSSreaders ordered oldest to newest), proving why in-place replacement in I1.2.2 was required.

---

## 6. Complete Verification Test Matrix (20 / 20 Tests Passing)

| Test Suite / Test Function | Windows 11 Native (NTFS) | WSL2 Linux amd64 (`-race`) | Purpose / Coverage |
| :--- | :--- | :--- | :--- |
| `TestCompactionPublication_ConcurrentFlushOverlap` | PASS (0.08s) | PASS (0.16s) | Concurrent flush preservation during compaction |
| `TestCompactionPublication_ConcurrentFlushNewerVersion` | PASS (0.05s) | PASS (0.10s) | Newer version search visibility after compaction |
| `TestCompactionPublication_ConcurrentFlushTombstone` | PASS (0.09s) | PASS (0.11s) | Tombstone visibility after compaction |
| `TestCompactionPublication_OverlappingInputsValidation` | PASS (0.03s) | PASS (0.05s) | Competing compaction safe validation abort |
| `TestCrossTierWriteConflict_FlushedSSTable` | PASS (0.01s) | PASS (0.05s) | Flushed SSTable write-write conflict detection |
| `TestCrossTierWriteConflict_FlushedTombstone` | PASS (0.02s) | PASS (0.05s) | Flushed tombstone conflict detection |
| `TestCrossTierWriteConflict_NonConflicting` | PASS (0.01s) | PASS (0.05s) | Non-conflicting concurrent transaction commit |
| `TestSkipListLevelDistribution` | PASS (0.00s) | PASS (0.00s) | SkipList level distribution math |
| `TestBasicPutGet` | PASS (0.01s) | PASS (0.06s) | Single-txn Put/Get |
| `TestDeleteAndTombstone` | PASS (0.01s) | PASS (0.04s) | Tombstone visibility |
| `TestWALRecovery` | PASS (0.09s) | PASS (0.33s) | WAL crash recovery & CRC32 verification |
| `TestWriteWriteConflict` | PASS (0.01s) | PASS (0.03s) | Memtable write-write conflict |
| `TestI1_2_0_MaxUint64EncodingBehavior` | PASS (0.00s) | PASS (0.00s) | Internal key 56-bit sequence math |
| `TestI1_2_0_MultipleSSTablesDifferentVersions` | PASS (0.01s) | PASS (0.06s) | Multi-SSTable version resolution |
| `TestI1_2_0_TombstoneAndSubsequentPut` | PASS (0.01s) | PASS (0.08s) | Tombstone + subsequent put sequence |
| `TestI1_2_0_KeySpanningMultipleBlocks` | PASS (0.02s) | PASS (0.06s) | 3-block sparse index boundary traversal |
| `TestI1_2_0_MissingKeyVsIOError` | PASS (0.02s) | PASS (0.04s) | Absent key vs. closed file descriptor I/O error |
| `TestI1_2_0_SSTableOrderingAnomaly` | PASS (0.01s) | PASS (0.06s) | E-004 fix verification (inverted SSTables) |
| `TestE004_TxnConflict_NewestVersionNotInLastSSTable` | PASS (0.02s) | PASS (0.07s) | Txn conflict abort against out-of-order SSTables |
| `TestManagerGet_OrderingAudit` | PASS (0.01s) | PASS (0.06s) | Point read ordering audit (INV-ORDER-1) |
| **Total Test Suite** | **PASS (1.47s)** | **PASS (2.44s)** | **Zero Data Races Across 20 Tests** |

---

## 7. Formally Codified Invariants Status

- **INV-SYNC-1 (SkipList Mutation Safety)**: **Verified**.
- **INV-SYNC-2 (Re-entrant Lock Freedom)**: **Verified**.
- **INV-SYNC-3 (Lock Hierarchy)**: `Manager.Mu -> Wal.mu` strictly preserved.
- **INV-LIFETIME-3 (Non-destructive Compaction Publication)**: **VERIFIED in I1.2** via `replaceCompactedReaders`.
- **INV-ORDER-1 (Chronological Ordering of ActiveSSreaders)**: **VERIFIED in I1.2** via in-place slice replacement.
- **INV-MVCC-5 (Cross-Tier Conflict Safety)**: **VERIFIED in I1.1b & I1.2.0 Option A** (immune to slice reordering).
- **INV-MVCC-6 (Monotonic Sequence Ordering of RAM vs Disk)**: **VERIFIED in I1.2.0**.
- **INV-LIFETIME-2 (Safe Reader Retirement & File Deletion)**: **Protected under Approach A RWMutex; Target of Sub-phase I1.3**.

---

## 8. Remaining Hazards & Technical Debt (Sub-phase I1.3 Scope)

1. **Reader File Descriptor Lifetime (Sub-phase I1.3)**:
   In Approach A, readers hold `m.Mu.RLock()` for the entire point-read duration, ensuring open files cannot be closed while in use. If readers outlive the lock or long scans occur, closing files during compaction can trigger races. Sub-phase I1.3 will implement safe reader retirement.
2. **Crash-Safe File Cleanup**:
   Old SSTable files are unlinked via `os.Remove`. On Windows NTFS, closing all iterators before `os.Remove` avoids `ERROR_SHARING_VIOLATION`. Full crash-consistent deletion tracking across system restarts remains future work.

---

## 9. Questions for External Reviewer

1. **Reader Retirement Strategy for I1.3**:
   For Sub-phase I1.3 (Approach A baseline), do you recommend reference-counting individual `SSTableReader` instances, or is ensuring readers complete under `m.Mu.RLock()` before compaction closes files sufficient for Approach A (leaving reference-counted reader-set snapshots exclusively for Approach B)?
2. **Compaction Input Selection Policy**:
   Currently `CompactAll` selects all active readers. In I1.3/I1.4, should we introduce a size-tiered or level-based input selection helper to compact only adjacent subsets, now that `replaceCompactedReaders` fully supports arbitrary subset replacement?

---

**STATUS: CHECKPOINT CP1.2 COMPLETE. AWAITING EXTERNAL REVIEW APPROVAL BEFORE STARTING SUB-PHASE I1.3.**
