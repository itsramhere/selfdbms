# Compaction Ownership & Error Handling Audit (Sub-phase I1.2.4)

**Sub-phase:** I1.2.4 — Verify ownership and error handling  
**Date:** 2026-10-16  

---

## 1. Safety Audit Matrix

| Hazard | Code Inspection | Test Verification | Status / Mitigation |
| :--- | :--- | :--- | :--- |
| **Publishing partially constructed SSTables** | `builder.Close()` and `NewSSTableReader()` must succeed before acquiring `m.Mu.Lock()`. | Unit tests verify complete footer & index check before publication. | **SAFE**: Partially constructed tables are never published. |
| **Erasing concurrently flushed SSTables** | `replaceCompactedReaders` filters by input identity (`inputSet[r]`), preserving all other active readers. | `TestCompactionPublication_ConcurrentFlushOverlap` verifies reader C is preserved. | **SAFE**: Verified in I1.2.2 & I1.2.3. |
| **Inconsistent manager state on failure** | If merge or validation fails, `m.ActiveSSreaders` is untouched; partial output is deleted. | `TestCompactionPublication_OverlappingInputsValidation` asserts state untouched. | **SAFE**: Atomic state swap under `m.Mu.Lock()`. |
| **Deleting an active/reachable SSTable** | Only input readers removed from `m.ActiveSSreaders` are unlinked. | Slice identity assertion proves reachable tables are never deleted. | **SAFE**: Removed from slice before unlinking. |
| **Closing a reader mid-query** | In Approach A baseline, `Manager.Get` holds `m.Mu.RLock()`. Compaction publication acquires `m.Mu.Lock()`. | Approach A baseline locking invariants. | **SAFE in Approach A** (holding lock across read). Ref-counted pinning deferred to I1.3 / Approach B. |
| **Double publication** | Each compaction run generates a unique nanosecond-timestamped filename and publishes once. | Monotonic clock + process isolation. | **SAFE**. |

---

## 2. Filesystem Observations: Windows NTFS vs. WSL2 Linux

1. **Windows 11 NTFS (`os.Remove`)**:
   - On Windows, attempting to delete an open file via `os.Remove` fails with `ERROR_SHARING_VIOLATION` (or `Access is denied`) if any process or iterator holds an open handle without `FILE_SHARE_DELETE`.
   - In `CompactAll`: All `iterators` are closed (`it.Close()`) before `r.Close()` and `os.Remove(r.File.Name())`.
   - In our deterministic tests, all files were deleted cleanly on Windows without file locking panics.
2. **WSL2 Linux (`ext4` / `9p`)**:
   - Linux POSIX semantics permit unlinking open files (`unlink`); the inode persists until the last file descriptor closes.
   - WSL2 execution passes all tests identically.
