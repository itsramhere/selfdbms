# Defect Reproduction: Issue E-011 (Silent Commit on Conflict-Validation I/O Failure)

**Discovered During:** CP1 Verification Addendum (Sub-phase CP1-V.3)  
**Date:** 2026-10-17  
**Platform:** Windows 11 NTFS native & WSL2 Ubuntu Linux (`-race`)  
**Commit:** `d6909a7` (`baseline-approach-a`)  
**Severity:** Correctness / Data Integrity / Conflict Detection (P0 Blocker)

---

## 1. Symptom

In `Transaction.Commit()`, the write-write conflict detection loop executes:
```go
for key := range t.WriteSet {
    latestSeqNo, err := t.Man.latestValueLocked([]byte(key))

    if err == nil && latestSeqNo > t.ReadTs {
        t.Man.Mu.Unlock()
        t.Abort()
        return errors.New("Write-write conflict detected. Transaction aborted.")
    }
}
```
If `latestValueLocked` encounters an unexpected I/O error (such as an unreadable SSTable block, disk read failure, or closed file descriptor):
1. `err` is non-nil and not `ErrNotFound`.
2. The condition `err == nil && latestSeqNo > t.ReadTs` evaluates to `false`.
3. The error is completely ignored; the loop terminates without aborting.
4. The transaction proceeds to allocate a commit sequence number, appends to the WAL, applies writes to the active Memtable, and returns `nil` (successful commit).
5. Corrupt or unreadable SSTables allow conflicting transactions to silently bypass Snapshot Isolation conflict validation and overwrite data.

---

## 2. Minimal Deterministic Reproduction

The test `TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError` in `lsmengine/cp1_verification_test.go`:
1. Commits key `audit_key` and flushes it to disk as an active SSTable.
2. Closes the SSTable file descriptor (`targetReader.File.Close()`), forcing subsequent block reads to return an I/O error (`os.ErrClosed`).
3. Confirms that `latestValueLocked("audit_key")` returns an unexpected I/O error (`read data_...sst: file already closed`).
4. Begins a new transaction `txn1` touching `audit_key` and calls `txn1.Commit()`.
5. In the unpatched code, `txn1.Commit()` returns `nil`, committing the write despite the unverified conflict state.

---

## 3. Reproduction Logs

### Windows 11 NTFS Native
```
=== RUN   TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError
    cp1_verification_test.go:133: Confirmed latestValueLocked returns unexpected error: read data_1792251113965457100_1.sst: file already closed
    cp1_verification_test.go:142: DEFECT CONFIRMED: Transaction.Commit succeeded silently despite unexpected I/O error during conflict validation!
--- FAIL: TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError (0.03s)
FAIL
```

### WSL2 Linux with `-race`
```
=== RUN   TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError
    cp1_verification_test.go:133: Confirmed latestValueLocked returns unexpected error: read data_1792251135190318049_1.sst: file already closed
    cp1_verification_test.go:142: DEFECT CONFIRMED: Transaction.Commit succeeded silently despite unexpected I/O error during conflict validation!
--- FAIL: TestCP1V3_TransactionCommit_ErrorPropagation_SSTableIOError (0.05s)
FAIL
```

---

## 4. Proposed Minimal Correction

In `lsmengine/transaction.go`:
```go
	for key := range t.WriteSet {
		// Use internal unexported latestValueLocked since t.Man.Mu is already locked
		latestSeqNo, err := t.Man.latestValueLocked([]byte(key))
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Man.Mu.Unlock()
			t.Abort()
			return fmt.Errorf("conflict validation failed for key %q: %w", key, err)
		}

		if err == nil && latestSeqNo > t.ReadTs {
			t.Man.Mu.Unlock()
			t.Abort()
			return errors.New("Write-write conflict detected. Transaction aborted.")
		}
	}
```
This guarantees:
1. `ErrNotFound` is treated as absence of a previous version.
2. Unexpected I/O or corruption errors abort the transaction immediately and propagate.
3. No transaction can commit if conflict validation cannot be completed reliably.
