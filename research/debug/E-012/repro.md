# Reproduction and Repair Evidence: E-012

## 1. Summary
- **ID**: `E-012`
- **Sub-phase**: I3.7.1
- **Severity**: Correctness / Data Integrity / Error Propagation (P1)
- **Symptom**: In `getApproachB`, if an unexpected physical read error occurred on a newer SSTable, or if `sr.Reader` was nil/corrupted, the search loop silently skipped the reader and either returned a stale older version from an earlier SSTable or returned `ErrNotFound`. Additionally, direct pointer equality (`err == ErrKeyDeleted`) failed to recognize wrapped tombstone errors.
- **Root Cause**:
  1. `getApproachB` contained `if sr == nil || sr.Reader == nil { continue }`, which silently bypassed unreadable or corrupted tables rather than surfacing a corrupted snapshot error.
  2. Pointer equality was used instead of `errors.Is(err, ErrKeyDeleted)` and `errors.Is(err, ErrNotFound)`.
  3. In the review packet documentation, the excerpt omitted non-tombstone error propagation, creating documentary and operational risk.

## 2. Minimal Reproduction
```powershell
go test -v -run TestApproachB_NilReaderInSnapshot_DoesNotFallBack ./lsmengine
```
Output:
```text
=== RUN   TestApproachB_NilReaderInSnapshot_DoesNotFallBack
    sstable_read_error_test.go:244: DEFECT E-012 REPRODUCED: getApproachB silently skipped nil reader and returned stale older value "v1"!
--- FAIL: TestApproachB_NilReaderInSnapshot_DoesNotFallBack (0.03s)
FAIL
```

## 3. Resolution
1. Enforce strict error classification in `getApproachB`:
   - `sr == nil || sr.Reader == nil`: returns error `errors.New("corrupted snapshot: nil reader encountered")`.
   - `err == nil`: returns visible value (`string(val), nil`).
   - `errors.Is(err, ErrKeyDeleted)`: returns tombstone (`" ", err`).
   - `errors.Is(err, ErrNotFound)`: continues search to older SSTables (`continue`).
   - All other errors: propagates immediately (`"", err`).
2. Apply the identical error classification to memory tier lookups (`Mem.Get` and `Frozenmem.Get`).
3. Harmonize Approach A `Manager.Get` with identical error classification and nil reader checks.

## 4. Verification Evidence
- Windows 11 Native: `TestApproachB_UnexpectedReadError_PropagatesError`, `TestApproachB_UnexpectedReadError_SingleSSTable`, `TestApproachB_NilReaderInSnapshot_DoesNotFallBack` (PASS, 1.92s).
- WSL2 Ubuntu Linux: (PASS, 0.16s).
- Files: [`before.log`](before.log), [`wsl_before.log`](wsl_before.log), [`after.log`](after.log), [`wsl_after.log`](wsl_after.log), [`diff.patch`](diff.patch).
