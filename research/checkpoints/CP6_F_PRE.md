# Checkpoint CP6-F-PRE: Corrective Experimental Design, Provenance Repair, and Preflight

- **Author**: Antigravity Assistant & owndbms Core Research Team
- **Date**: 2026-10-27
- **Target Phase**: Phase I6.9 — Corrective Experimental Design, Provenance Repair, and Preflight
- **Status**: **PREFLIGHT COMPLETE — AWAITING EXTERNAL REVIEWER APPROVAL (CP6-F-PRE)**
- **Campaign ID**: `I6_CORRECTIVE_MECHANISMS_V1`
- **Protocol Version**: 1.1.0
- **Base Git Revision**: `4a062174bb617a649cc3a2b03a83ee0e1e38ee45`
- **Preflight Implementation Commit**: `6079633e58c8b9e11086c8e4ad58059af88ea196`
- **Evaluation Platform**: WSL2 Ubuntu 24.04.3 LTS on native Linux ext4 (`/dev/sdd`), Go 1.27.3 linux/amd64
- **Historical Evidence Status**: 960-trial baseline and 600-trial confirmatory datasets remain 100% frozen, unmodified, and preserved.

---

## 1. Executive Summary & Authorization Compliance

Pursuant to the **External Reviewer Directive — Phase I6.9**, the research team has completed the design, implementation, unit testing, and strictly bounded engineering preflight for the corrective benchmark campaign.

### Strict Scope Enforcement:
1. **Design, Implementation, Testing, and Preflight Only**: Fully implemented and validated.
2. **Full Corrective Campaign**: **NOT LAUNCHED** (standing by for authorization).
3. **Engine Algorithm Optimization**: **ZERO CHANGES** to core synchronization semantics, MVCC conflict detection, snapshot lifecycle, WAL durability, or recovery rules.
4. **Historical Campaign Preservation**: All raw trial records, CSVs, checksums, and profiling archives for both the 960-trial campaign and the 600-trial confirmatory campaign remain completely intact and unaltered. Historical executable hash `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f` is preserved at `/home/ramst/selfdbms/targeted_runner`.
5. **Bounded Preflight**: Strictly restricted to 24 diagnostic trial executions across 12 experimental conditions.

---

## 2. Key Corrections Implemented & Verified

### 2.1 Provenance Repair (I6.9.1)
- **Defect Identified**: Historical trial records recorded `git_revision: "unknown_rev"` due to native ext4 WSL2 directory lacking `.git`.
- **Correction**: Built a dedicated provenance package (`owndbms/research/experiments/provenance`).
  - Strict fail-closed validation: Trials abort immediately if Git commit SHA is empty, missing, or `"unknown_rev"`.
  - Captures: 40-character Git commit SHA, branch name, working tree clean status, deterministic source tree hash across all `.go` and `go.mod` files, binary SHA-256, compiler version (`go1.27.3`), platform (`linux/amd64`), and filesystem type (`ext4 (Native Linux)`).
  - Clean isolated clone established on native ext4 at `/home/ramst/selfdbms_clean` (`/dev/sdd`), ensuring full Git provenance.
  - Verified by automated test suite `provenance_test.go` (5/5 tests passing).

### 2.2 Effective Random Seed Handling (I6.9.2)
- **Defect Identified**: Runner computed `baseSeed := Seed + TrialIndex*10007`. Because matched A and B trials had different trial indices, they received different effective seeds.
- **Correction**: Redesigned worker seed derivation using deterministic cryptographic mixing:
  $$\text{WorkerSeed} = \text{DeriveWorkerSeed}(\text{PairSeed}, \text{Role}, \text{WorkerIndex}, \text{Purpose})$$
  Strictly independent of `TrialIndex` and `SyncMode`.
  - Both Approach A and Approach B workers receive bit-for-bit identical initial PRNG states.
  - KeySampler draws match identically across workers (tested and verified across 10,000 draws in `seed_parity_test.go`).
  - Operation counts explicitly separate `attempted`, `admitted`, and `completed` operations.
  - Every raw JSON now embeds an auditable `worker_seeds` map.

### 2.3 T1 Mechanism Experiment Design & Synchronization Test (I6.9.3)
- **Defect Identified**: Prior flush vs sham-flush DiD attributed all read tail differences to physical flush publication, without isolating reader/writer lock contention on `m.Mu` during synchronous WAL `fsync`.
- **Correction**: Developed a 4-condition decomposition:
  - **Condition A (Pure Readers)**: $w=0$, no flush, large memtable. Establishes uncontended disk lookup baseline.
  - **Condition B (WAL-Synchronized Writes, No Flush)**: $w=2$ paced writers, `wal_sync = true`, 0 flushes. Isolates reader/writer lock contention and WAL `fsync` convoying on `m.Mu`.
  - **Condition C (Active Flush & Publication)**: Writers with periodic physical flushes and SSTable publications.
  - **Condition D (Deterministic Barrier Test)**: `TestT1_DeterministicPublicationBarrier` aligns reader arrivals with publication lock acquisition.
    - Verified: Under Approach A, arriving reader latency directly reflects publication lock hold duration ($20.3\,\text{ms}$ observed vs $21.7\,\text{ms}$ lock hold). Under Approach B, arriving reader latency is $0.5\,\text{ms}$, completely unblocked by publication.

### 2.4 T3 Churn Anomaly Resolution (I6.9.4)
- **Anomaly Investigated**: In the 600-trial campaign, "no-churn" control caused an unexpected throughput collapse and a 17x write disparity (73 writes in A vs 1,230 writes in B).
- **Audit Findings**:
  1. In `controlled_churn`, background compactions ran 7-8 times, merging 4 SSTables into 1-2 SSTables, which drastically accelerated disk lookups.
  2. In `no_churn_control`, compactions were disabled (0 compactions, 4 uncompacted SSTables), and 3 unpaced writers continuously ran with `wal_sync = true`. In Approach A, readers held `m.Mu.RLock()` across all 4 SSTable disk reads (~35 ms/read), blocking writers waiting for `m.Mu.Lock()`. Writers held `m.Mu.Lock()` during synchronous WAL `fsync`, blocking all new readers. This produced a massive lock convoy. In Approach B, readers released `m.Mu.RLock()` before disk I/O, allowing writers to commit 1,230 writes without convoying.
  3. The comparison conflated compaction topology changes and unpaced write lock convoying with snapshot overhead.
- **Corrective Design**:
  - Hold SSTable topology strictly constant across all conditions ($c=0$, initial 4 SSTables frozen).
  - Sub-condition 1: Writer-free churn ($w=0$) across $r \in \{1, 4, 16\}$ to isolate pure snapshot management overhead.
  - Sub-condition 2: Equal-work paced write churn ($w=1$ paced, 25 writes/s) to hold background write work identical.
  - Sub-condition 3: Direct synchronization microbenchmark (`BenchmarkReaderSync_ApproachA_vs_ApproachB`) isolating exact CPU cost: $37.78\,\text{ns/op}$ for Approach B snapshot vs $6.87\,\text{ns/op}$ for Approach A RLock ($\Delta = 30.91\,\text{ns/op}$, exactly 0 heap allocations).

### 2.5 Profiling Repair (I6.9.5)
- **Defects Identified**: Profiling began before `RunTrial`, capturing fixture creation, initial flushes, warmup, and teardown. Mutex profiles accumulated across A/B in the same process. Heap profiles forced GC, capturing only retained memory.
- **Correction**:
  - Implemented measurement-window lifecycle hooks (`OnMeasurementStart` and `OnMeasurementEnd`).
  - CPU, mutex, block, and allocation profiling activate strictly at window start and terminate strictly at window close.
  - Fixture creation and warmup are 100% excluded (verified by `profiler_test.go` and `go tool pprof -top`).
  - Approach A and Approach B profiling runs execute in separate, isolated OS processes.
  - Differential `runtime.ReadMemStats` captures total allocated bytes, malloc count, free count, and GC cycles strictly within the measurement window.

---

## 3. Preregistered Campaign Protocol (`I6_CORRECTIVE_MECHANISMS_V1`)

- **Total Planned Trials**: 280 trials (140 balanced pairs).
- **Sample Size**: 10 balanced pairs per cell ($N=20$ trials per condition).
- **Statistical Power**: $> 0.999$ for primary endpoints at $\alpha = 0.01$ (derived from historical variance).
- **Estimated Execution Cost**: ~29 minutes on native WSL2 ext4.
- **Primary Estimators**: Paired median differences, ratios of medians, BCa bootstrap 95% CIs (10,000 resamples), Wilcoxon signed-rank test, paired sign test, Holm-Bonferroni correction, and TOST practical equivalence ($\pm 5\%$ margin).

---

## 4. Bounded Engineering Preflight Results

The harness executed exactly 24 diagnostic trials across all 12 experimental conditions in native WSL2 ext4:
- **Total Executed**: 24 trials (12 pairs).
- **Trial Status**: 24 / 24 returned `SUCCESS` (100%).
- **Validation Readback**: `validation_readback_status == "PASSED"` in 24 / 24 trials.
- **Validation Errors**: Exactly 0 key-value errors.
- **Worker Seed Parity**: Identical worker seeds verified across all 12 matched pairs.
- **Git Revision**: Verified `6079633e58c8b9e11086c8e4ad58059af88ea196` in all 24 records.
- **Preflight Rule Compliance**: Preflight trial observations are held as engineering validation only and are not reported as experimental findings.

---

## 5. Full Regression & Scope Verification

- `go vet ./...`: Passed with 0 warnings.
- `go test -count=1 ./...`: Passed across all 10 packages.
- `go test -race -count=1 ./...`: Passed with zero data race warnings across all core engine and experiment packages.
- **Core Engine Algorithm Changes**: Exactly 0 changes to Approach A synchronization, Approach B snapshot mechanics, WAL logging, or compaction algorithms.

---

## 6. Stop Condition & Review Request

Execution has completed all authorized preflight objectives and has stopped.

**STOPPED AT CHECKPOINT CP6-F-PRE.**

**The research team requests External Reviewer approval of Checkpoint CP6-F-PRE and formal authorization to execute the preregistered 280-trial confirmatory campaign `I6_CORRECTIVE_MECHANISMS_V1`.**
