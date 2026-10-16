# Issue E-003: Cross-Tier Write Conflict Detection Failure After Memtable Flush

## Environment
- OS: Windows 11 Home Single Language (Build 26200, 64-bit)
- Go: go1.26.3 windows/amd64
- Base Commit: `4ca8787`
- Branch: `research/reader-compactor-sync`

## Reproduction Command
```powershell
go test -v ./lsmengine -run TestCrossTierWriteConflict
```

## Symptom
1. `TestCrossTierWriteConflict_FlushedSSTable` fails: Transaction T1 begins before T2, T2 writes to the same key and commits, T2's version is flushed to disk in an SSTable, but T1's commit succeeds instead of aborting.
2. `TestCrossTierWriteConflict_FlushedTombstone` fails: Transaction T1 begins before T2, T2 deletes the key and commits, T2's tombstone is flushed to disk in an SSTable, but T1's commit succeeds instead of aborting.

## Root Cause
In `lsmengine/manager.go`, `latestValueLocked(userKey)` only queries `m.Mem` and `m.Frozenmem`. It does not inspect `m.ActiveSSreaders`.
When a committed transaction's mutation or tombstone is flushed to disk via `flushToDisk`, the key is removed from memory tiers and resides exclusively in physical SSTables. When a concurrent transaction subsequently commits, `latestValueLocked` returns `ErrNotFound`, allowing the transaction to commit and silently overwrite the newer version on disk.

## Resolution Implemented (Sub-phase I1.1b.3)
1. Implemented `LatestValue(userKey []byte) (uint64, error)` on `SSTableReader` in `lsmengine/sstableReader.go`. Locates the latest version of `userKey` by seeking `EncodeInternalKey(userKey, math.MaxUint64, true)` via binary search on `Index` and reading the block via `io.NewSectionReader`. Returns `(diskSeqNo, nil)` for both Puts and Deletes, or `(0, ErrNotFound)` if absent.
2. Updated `Manager.latestValueLocked` in `lsmengine/manager.go` to scan `m.ActiveSSreaders` in reverse order (`len(m.ActiveSSreaders)-1` down to 0) if the key is not in `m.Mem` or `m.Frozenmem`.
3. Created deterministic regression suite in `lsmengine/cross_tier_conflict_test.go` covering SSTable put conflicts, SSTable tombstone conflicts, and independent non-conflicting commits.

## Validation & Evidence
1. **Windows Native (`go test -v ./lsmengine`)**:
   - `TestCrossTierWriteConflict_FlushedSSTable`: PASS (0.02s)
   - `TestCrossTierWriteConflict_FlushedTombstone`: PASS (0.01s)
   - `TestCrossTierWriteConflict_NonConflicting`: PASS (0.02s)
   - Full suite (8/8 tests): PASS (1.27s)
2. **WSL2 Linux amd64 (`go test -race -v ./lsmengine`)**:
   - Full suite (8/8 tests): PASS in 1.71s with 0 race warnings.
   - Repeated 3x (`-count=3`): PASS with 0 race warnings.
3. **Evidence Artifacts**:
   - Failing log before fix: [`before.log`](before.log)
   - Passing log after fix: [`after.log`](after.log)
   - Code patch: [`diff.patch`](diff.patch)

