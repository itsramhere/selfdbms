# Reproduction and Counterexample: Issue E-005

**Title:** Noncontiguous Compaction Input Selection Violates Invariant `INV-ORDER-1` in Flat Reader Architecture  
**Sub-phase Discovered:** I1.3.0 (Reader Ownership & Ordering Audit)  
**Status:** FIXED & VERIFIED (Sub-phase I1.3.0a, commit pending)  
**Severity:** Architectural Invariant Hazard (P1)  

---

## 1. Problem Description & Root Cause

In `owndbms/lsmengine`, all on-disk SSTables reside in a single flat slice: `m.ActiveSSreaders []*SSTableReader`.

The point-read path ([`Manager.Get`](file:///c:/Users/ramst/Documents/Projects/selfdbms/lsmengine/manager.go#L87-L100)) relies strictly on invariant [`INV-ORDER-1`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/design/invariants.md#L83):
> *"Elements in `m.ActiveSSreaders` must be ordered chronologically by creation timestamp/generation, from oldest at index 0 to newest at index `len-1`."*

Because `Manager.Get` iterates in reverse order (`for i := len(m.ActiveSSreaders) - 1; i >= 0; i--`) and terminates on the first visible MVCC record with sequence number $\le ReadTs$, the correctness of all point reads depends on newer sequence numbers always appearing at higher slice indices than older sequence numbers.

### The Noncontiguous Compaction Counterexample:
Suppose three chronological SSTables exist:
- $R_0$: Created at $t_0$, sequence range $[1, 10]$, contains `key1 = "v0_old"` at $Seq = 10$.
- $R_1$: Created at $t_1$, sequence range $[11, 20]$, contains `key1 = "v1_mid"` at $Seq = 20$.
- $R_2$: Created at $t_2$, sequence range $[21, 30]$, contains `key1 = "v2_new"` at $Seq = 30$.

If compaction selects noncontiguous inputs $S_{\text{inputs}} = \{ R_0, R_2 \}$ (skipping $R_1$):
- The merged output $M$ contains the deduplicated newest version from $\{R_0, R_2\}$, which is `key1 = "v2_new"` at $Seq = 30$.
- When replacing $\{ R_0, R_2 \}$ in the slice `[R_0, R_1, R_2]`:
  - **Option 1 (In-place at $R_0$'s position)**: Resulting slice is `[M, R_1]`.
    - Reverse search in `Manager.Get` checks $R_1$ first!
    - $R_1$ has $Seq = 20 \le 100$.
    - `Manager.Get` returns `"v1_mid"`!
    - **FAILURE**: $R_1$ ($Seq = 20$) masks the newer committed version $M$ ($Seq = 30$).
  - **Option 2 (At $R_2$'s position)**: Resulting slice is `[R_1, M]`.
    - If another key `key2` existed in $R_0$ ($Seq = 5$) and was deleted or updated in $R_1$ ($Seq = 15$):
    - Reverse search in `Manager.Get` checks $M$ first.
    - $M$ contains $R_0$'s version ($Seq = 5$).
    - `Manager.Get` returns $Seq = 5$ from $M$, resurrecting or masking $R_1$'s newer update ($Seq = 15$)!

**Conclusion**: In a single flat slice architecture without multi-level tiering (L0, L1, etc.), **it is mathematically impossible to compact noncontiguous SSTables without violating `INV-ORDER-1`**.

---

## 2. Before-Fix Reproduction Evidence

Archived in [`research/debug/E-005/failing_counterexample.log`](failing_counterexample.log):

```text
=== RUN   TestCounterexample_NoncontiguousCompactionViolatesOrder
    noncontiguous_compaction_counterexample_test.go:85: Result of Manager.Get after noncontiguous compaction: "v1_mid"
    noncontiguous_compaction_counterexample_test.go:88: COUNTEREXAMPLE PROVEN: Noncontiguous compaction resulted in stale read "v1_mid" (expected "v2_new"). r1 (seq 20) masked newer compacted version mCompact (seq 30) because mCompact was placed before r1 in the flat slice!
--- FAIL: TestCounterexample_NoncontiguousCompactionViolatesOrder (0.02s)
FAIL
```

---

## 3. Resolution & Safeguards Implemented (Sub-phase I1.3.0a)

Under external reviewer authorization (D-008):

1. **Adopted Invariant `INV-COMPACT-1`**: Formally codified in [`research/design/invariants.md`](../../design/invariants.md). Compaction inputs must preserve chronological precedence of all surviving readers. In a flat slice, input selection is strictly restricted to a contiguous prefix `[0..k]`.
2. **Defensive Validation in `replaceCompactedReaders`**:
   - Validates that each input reader appears without duplicates.
   - Validates that all inputs are currently active in `current`.
   - Validates that inputs start at index 0 (`current[0] == inputs[0]`), preserving the full-prefix compaction policy.
   - Validates that inputs are strictly contiguous (`current[j] == inputs[j]` for all $j$).
   - Returns descriptive validation error on violation without mutating `current`.
   - Cleans up temporary output files and leaves active readers untouched.
3. **Comprehensive Regression Suite**:
   - `TestE005_NoncontiguousCompactionRejected`: Asserts rejection and zero mutation.
   - `TestE005_NonPrefixCompactionRejected`: Asserts rejection when inputs do not start at 0.
   - `TestE005_DuplicateInputsRejected`: Asserts rejection when duplicate input readers are passed.
   - `TestE005_ValidPrefixCompaction_PreservesOrderAndVisibility`: Tests valid prefix compaction with concurrently flushed tables, tombstone visibility, newer version visibility, and multiple timestamp evaluations.

Post-fix output preserved in [`research/debug/E-005/after.log`](after.log). All 24 tests pass on Windows native and WSL2 with `-race`.

