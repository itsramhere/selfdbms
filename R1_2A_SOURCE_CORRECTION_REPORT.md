# Phase I7 — Checkpoint R1.2a: Source and Protocol Defect Correction Report

**DATE:** 2026-10-29  
**SCOPE:** SOURCE CODE, HARNESS CONFIGURATION, AND PROTOCOL DEFECT AUDIT  
**EVALUATED GIT REVISION:** `f4cce4b5faa80924c7ef945139ef1a65ccb2b32a`  
**STATUS:** READ-ONLY SOURCE AUDIT COMPLETE. NO SOURCE FILES MODIFIED.

---

## 1. Executive Summary

This report documents the four critical harness, runner, and workload configuration defects identified during the R1.2a audit that compromised Phase II G4 execution. In strict compliance with Option A and Checkpoint R1.2a directives, **zero experimental source files were modified**. All defects are documented here for external review and future protocol corrections.

---

## 2. Harness Defect 1: Runner Omission of Block "D3"

### File Location
`research/experiments/phase_i7/cmd/i7trial/main.go`

### Root Cause
In `cmd/i7trial/main.go`, the dispatch table or switch statement checking `spec.Block` did not include `"D3"` in its supported block list:
```go
switch spec.Block {
case "E1", "E2", "E3", "E4", "E5", "E7":
    // dispatch workload
default:
    fmt.Fprintf(os.Stderr, "unsupported block: %s\n", spec.Block)
    os.Exit(3)
}
```

### Impact on Campaign
1. Trials 1, 2, and 3 (`i7_D3_start_t01_A`, `AMIN`, `B`) crashed immediately upon launch (59–673 ms execution time) with exit code 3 (`unsupported block: D3`).
2. These 3 mechanical failures accounted for 30% of the 10 non-successes that triggered stopping rule S-1, prematurely halting the campaign at trial 60.
3. The omission prevented measurement of the start drift anchor ($C_{\text{start}}$), leaving environmental and thermal drift during the 26.4-minute campaign unbounded by the primary preregistered instrument.

---

## 3. Harness Defect 2: Pebble Runner Schema Omission and Buffer Overflow

### File Location
`research/experiments/phase_i7/pebble_runner/main.go`

### Root Cause
Two distinct defects invalidated Block E7:
1. **Schema Divergence:** `pebble_runner.exe` emitted top-level timing fields (`result.measurement_window_ms`) but omitted the required nested `result.timestamps` object (`measured_window_ns`, `window_start_ns`, `window_end_ns`). The orchestrator flagged all Pebble runs as `INVALID`.
2. **Buffer Exhaustion:** Fixed 1,000,000-sample latency buffers per reader filled in approximately 5.3 seconds under 1.88M ops/s unthrottled throughput. Over 10.45 million latency samples were dropped (>50% of completed reads), corrupting latency percentiles.

### Impact on Campaign
1. Trials 16, 50, and 58 were flagged as `INVALID`, contributing 3 non-successes to the stopping rule counter.
2. Pebble data were completely invalidated and excluded from primary statistical comparisons.

---

## 4. Workload Defect 3: Keystream Keyspace Mismatch in Block E5

### File Location
`research/experiments/phase_i7/harness2/keystream.go` and `engine/sstableReader.go`

### Root Cause
Block E5 was designed to manipulate read critical-section scan length by generating absent keys (`TargetReadHitRatio = 0`), forcing readers to scan all SSTable blocks (~10,000 keys) rather than exiting early on hit (~3,600 keys).

In `keystream.go`, all-miss keys were generated with sequential indices starting at 10,001:
```go
// keystream.go for hit0 workload
keyIndex := int64(10001) + reqIndex
```
However, the initial database fixture populated keys strictly in the range $0\dots 9,999$.

In `sstableReader.go`, binary search over the block index checks key bounds:
```go
// sstableReader.go
idx := sort.Search(len(r.index), func(i int) bool {
    return bytes.Compare(r.index[i].MaxKey, targetKey) >= 0
})
```
Because `targetKey` ($> 10,000$) was strictly greater than the `MaxKey` of every block in the SSTable, `sort.Search` returned `len(r.index)` (or the final block index). Readers loaded only the final 4KB data block, encountered EOF, and returned `ErrNotFound`.

### Impact on Campaign
1. Every reader goroutine repeatedly queried the exact same final 4KB block.
2. The operating system page cache pinned that single 4KB block in RAM.
3. Read throughput was an artificially high $\sim 3,000\text{ ops/s}$ with sub-2.5 ms latencies, resolving from in-memory EOF checks rather than disk SSTable block decoding.
4. Preregistered manipulation check MC-5 ($\text{records\_scanned} \ge 2.0\times$) failed, officially voiding Block E5.

---

## 5. Script Portability and Fail-Closed Defect

### File Location
`research/experiments/analysis/reproduce_all_metrics.py` (Historical)

### Root Cause
Earlier scripts used hardcoded absolute paths (`C:\Users\ramst\...`), lacked CLI flags, and did not fail closed when encountering corrupted or missing records.

### Resolution in Checkpoint R1.2a
A new portable, fail-closed script [`research/experiments/analysis/reconcile_r1_2a.py`](file:///c:/Users/ramst/Documents/Projects/selfdbms/research/experiments/analysis/reconcile_r1_2a.py) was implemented:
- Accepts `--data-dir` and `--output-dir` arguments with automatic repo-relative discovery.
- Validates the presence of exactly 60 trial records, required schema fields, and complete triplet structures.
- Fails closed immediately (exit code 1) with informative error messages upon any anomaly.

---

## 6. Summary of Action Items for Future Execution

If a future checkpoint authorizes a controlled confirmatory campaign:
1. **Patch `cmd/i7trial/main.go`:** Add `"D3"` to supported block dispatch table.
2. **Patch `pebble_runner/main.go`:** Emit standard `result.timestamps` schema and implement dynamic reservoir sampling to eliminate buffer drops.
3. **Patch `keystream.go`:** Generate absent keys interleaved within the existing $0\dots 9,999$ keyspace (e.g. non-existent fractional or even/odd keys) to force readers to traverse intermediate SSTable blocks.
4. **Toolchain Setup:** Configure MinGW/LLVM cgo environment to enable native Windows `-race` execution.
