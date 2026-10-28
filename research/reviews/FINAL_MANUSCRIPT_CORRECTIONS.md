# Phase I6.10: Final Manuscript Corrections and Justification Report

- **Target Document**: `paper.tex`
- **Auditor**: External Reviewer Forensic Protocol (Phase I6.10)
- **Scope**: Minimal, focused corrections to align `paper.tex` strictly with the audited empirical evidence.
- **Date**: 2026-10-28

---

## 1. Summary of Changes

The audited corrections preserve all core technical contributions of the paper:
1. Formal synchronization models (coarse-grained `RWMutex` Approach A vs. immutable `ReaderSnapshot` Approach B).
2. Four lifecycle invariants (Pin Exclusivity, Atomic Tracking, Exactly-Once Reclamation, Shutdown Barrier).
3. Verified empirical benefits: elimination of writer starvation (3.3x to 18.5x write progress increase), doubling of read throughput (up to 2.07x in B1), and 73%–76% p95 tail latency suppression at $r=2, 4$ under flushes.
4. Full disclosure of negative tradeoffs: 26% to 37% throughput regression under rapid reader-set churn (Workload D at $r \le 4$).
5. Overlap instrumentation audit proving positive publication reads in Approach A reflect reader drain delays rather than concurrent execution during lock acquisition.

The corrections resolve 5 critical evidentiary and typographical defects:
1. **Correction of Latency Unit Error**: In Section 5.3, Workload B2 latency values (`42.7 µs vs. 175.4 µs`) were mislabeled with microseconds instead of milliseconds. Corrected to `42.7 ms vs. 175.4 ms (42,735 µs vs. 175,434 µs)`.
2. **Equivalence Overstatement Repair**: In the Abstract and Section 5.4, replaced claims of "exact empirical parity" and "confirming zero measurable regression" with precise descriptions that observed throughput medians are closely aligned within experimental resolution (0.98x–1.02x in E, 0.97x–1.02x in A) and bootstrap confidence intervals span parity.
3. **Causal Scope Qualification for Tail Latency Suppression**: In the Abstract and Section 5.3, clarified that 73%–76% tail latency suppression is observed specifically at low reader counts ($r=2, 4$), that whole-trial throughput differences remain statistically inconclusive across all $r$, and that tail latency suppression reflects relief from general exclusive lock convoying in Go's write-preferring `RWMutex`.
4. **Clarification of Churn Attribution**: In the Abstract and Section 5.5, qualified the attribution of the 26%–37% churn penalty to descriptor allocation and atomic refcounting as a mechanistic explanation supported by microbenchmarks, avoiding unsupported claims of isolated hardware CPU percentages.
5. **Reproducibility & Provenance Transparency**: Updated the Appendix to cite the completed 960-trial commit (`e156b92`), the completed 600-trial confirmatory commit (`4a06217`), the byte-for-byte verified reconstructed executable SHA-256 (`2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f`), and disclosed the `unknown_rev` JSON metadata artifact caused by unversioned WSL2 directory execution.

---

## 2. Line-by-Line Itemized Corrections

### Item 1: Abstract Qualification of Tail Suppression, Equivalence, and Churn
- **Original Lines**: 41
- **Original Text**:
  > *"...while suppressing 95th-percentile read tail latency by 73%--76% during active Memtable flushes. In uncontended disk reads and pure in-memory Memtable lookups, both synchronization strategies demonstrate similar observed throughput (0.97$\times$--1.03$\times$), confirming zero measurable memory-tier regression. Crucially, we identify a significant negative tradeoff: under rapid reader-set churn ($>4$ flushes/compactions per second), Approach B incurs a 26\% to 37\% throughput penalty at low thread counts ($r \le 4$, 0.63$\times$--0.74$\times$) due to the CPU costs of snapshot descriptor allocation, atomic reference counting, and deferred reader retirement."*
- **Revised Text**:
  > *"...while suppressing 95th-percentile read tail latency by 73%--76% at low reader counts ($r=2, 4$) during periodic Memtable flushes (though whole-trial throughput differences remain statistically inconclusive). In uncontended disk reads and pure in-memory Memtable lookups, both synchronization strategies exhibit closely aligned throughput medians (0.97$\times$--1.02$\times$), with bootstrap confidence intervals spanning parity across all tested concurrencies. Crucially, we identify a significant negative tradeoff: under rapid reader-set churn ($>4$ flushes/compactions per second), Approach B incurs a 26\% to 37\% throughput penalty at low thread counts ($r \le 4$, 0.63$\times$--0.74$\times$), consistent with the computational overhead of snapshot descriptor allocation, atomic reference counting, and deferred reader retirement."*
- **Justification**: Eliminates unjustified claim of "confirming zero regression", explicitly bounds the 73%–76% tail latency suppression to $r=2, 4$, notes whole-trial throughput inconclusiveness in B2, and qualifies the churn mechanism without asserting unverified exact hardware percentages.

---

### Item 2: Section 5.3 Workload B2 Latency Magnitude Unit Correction
- **Original Lines**: 234
- **Original Text**:
  > *"At $r=2$ and $r=4$, 95th-percentile read latency drops by **73\% to 76\%** (42.7 µs vs. 175.4 µs at $r=2$; 48.9 µs vs. 181.0 µs at $r=4$)."*
- **Revised Text**:
  > *"At $r=2$ and $r=4$, 95th-percentile read latency drops by **73\% to 76\%** (42.7 ms vs. 175.4 ms at $r=2$; 48.9 ms vs. 181.0 ms at $r=4$, corresponding to 42,735 µs vs. 175,434 µs and 48,916 µs vs. 180,966 µs)."*
- **Justification**: Fixes critical unit symbol error where milliseconds were printed with the microsecond symbol "µs".

---

### Item 3: Section 5.3 Causal Lock Contention Clarification
- **Original Lines**: 234–235
- **Original Text**:
  > *"In Approach A, readers arriving during publication stall behind the exclusive lock, inflating latency to hundreds of milliseconds. In Approach B, readers continue querying their pinned snapshot uninterrupted while the flusher swaps pointers in microseconds."*
- **Revised Text**:
  > *"In Approach A, readers arriving while an exclusive publication lock is queued or held stall behind the writer lock in Go's write-preferring RWMutex, inflating latency to hundreds of milliseconds. In Approach B, readers continue querying their pinned snapshot uninterrupted while metadata pointer swaps execute without blocking readers."*
- **Justification**: Incorporates findings from the sham-flush causal decomposition (Experiment T1), recognizing that writer-preferring RWMutex queue convoying is the primary driver of the latency spike.

---

### Item 4: Section 5.4 Equivalence Boundary Repair (Workloads A and E)
- **Original Lines**: 236–237
- **Original Text**:
  > *"In Workload A (uncontended disk reads) and Workload E (in-memory Memtable hits with 2 writers), Approach B demonstrates exact empirical parity with Approach A (0.97$\times$--1.03$\times$). Bootstrap confidence intervals cross 1.0 across all 8 concurrency levels. In Workload E, throughput scales linearly from 656 ops/s to 20,601 ops/s with sub-2µs median latencies ($p50 \approx 1.5\text{--}1.6$ µs). This confirms that when snapshot pinning is bypassed or writers do not contest disk reader sets, Approach B introduces zero measurable cache or synchronization overhead."*
- **Revised Text**:
  > *"In Workload A (uncontended disk reads) and Workload E (in-memory Memtable hits with 2 writers), observed throughput medians between Approach A and Approach B remain closely aligned (0.97$\times$--1.02$\times$). Bootstrap confidence intervals cross 1.0 across all 8 concurrency levels. In Workload E, throughput scales linearly from 656 ops/s to 20,601 ops/s with sub-2µs median latencies ($p50 \approx 1.5\text{--}1.6$ µs). These observations indicate that when snapshot pinning is bypassed or writers do not contest disk reader sets, Approach B introduces no detectable synchronization penalty within our measurement resolution, though wide confidence intervals in Workload A preclude formal claims of mathematical equivalence across all cells."*
- **Justification**: Removes unsupported assertion of "exact empirical parity" and "zero measurable overhead", acknowledging confidence interval widths and TOST limitations.

---

### Item 5: Section 5.5 Churn Attribution Qualification
- **Original Lines**: 241–242
- **Original Text**:
  > *"Under rapid churn, the CPU cost of allocating new \texttt{ReaderSnapshot} descriptors, executing atomic CAS reference count updates, and tracking asynchronous reader retirement exceeds the concurrency benefit when reader threads are few. Only when concurrency reaches $r \ge 16$ do the parallel query gains amortize descriptor lifecycle costs."*
- **Revised Text**:
  > *"Under rapid churn, the computational overhead of allocating new \texttt{ReaderSnapshot} descriptors, executing atomic reference count updates, and managing asynchronous reader retirement outweighs the concurrency advantage when reader threads are few. Only when concurrency reaches $r \ge 16$ do parallel query gains amortize descriptor lifecycle management overheads."*
- **Justification**: Replaces absolute CPU cost assertions with precise computational overhead descriptions supported by microbenchmarks.

---

### Item 6: Section 7 Threats to Validity Expansion
- **Original Lines**: 257–261
- **Original Text**:
  > *"\item \textbf{Internal Validity}: The PRNG seed formula contains a mode index $mIdx$, ensuring that Approach A and B execute statistically equivalent Zipfian distributions rather than identical lockstep operation traces.
  \item \textbf{External Validity}: Benchmarks were executed within a virtualized WSL2 ext4 environment. While POSIX \texttt{pread} semantics on ext4 eliminate Windows 9P translation overheads, absolute numbers may differ on bare-metal NVMe storage.
  \item \textbf{Storage Engine Limitations}: The experimental engine does not implement asynchronous group commits, uncompacted tombstone garbage collection, or bounded flush queue backpressure. Findings characterize synchronization behavior, not production storage durability."*
- **Revised Text**:
  > *"\item \textbf{Internal Validity}: The experimental harness PRNG seed formula incorporated a mode index offset, ensuring that Approach A and Approach B evaluated statistically equivalent distributions rather than identical lockstep operation traces. Furthermore, while Difference-in-Differences analysis isolates exclusive lock contention, host thread scheduling under WSL2 hypervisor virtualization contributes background latency noise.
  \item \textbf{External Validity}: Benchmarks were executed within a virtualized WSL2 ext4 environment on an AMD Ryzen 7 7445HS laptop processor. While native ext4 block access eliminates Windows 9P translation overheads, absolute latencies and queue scaling behavior may differ on bare-metal Linux servers with enterprise NVMe storage.
  \item \textbf{Storage Engine Limitations}: The experimental engine isolates tiered synchronization primitives and does not implement asynchronous group commits, kernel-bypass asynchronous I/O (e.g., io\_uring), uncompacted tombstone garbage collection, or bounded flush queue backpressure. Findings characterize in-process Go synchronization behavior, not complete production DBMS durability."*
- **Justification**: Strengthens scientific humility regarding hardware scope, hypervisor scheduling artifacts, and runtime environments.

---

### Item 7: Section 8 Artifacts and Reproducibility Complete Provenance Disclosure
- **Original Lines**: 276–285
- **Original Text**:
  > *"\item \textbf{Frozen Git Commit}: \texttt{a5c0fb97deb8a67705a80e76b49e25f31d692492}
  \item \textbf{Raw Dataset (960 Trials)}: \texttt{research/experiments/data/campaign\_wsl2\_ext4\_restart\_v1/wsl2\_ext4/}
  \item \textbf{Checksum Manifest}: \texttt{.../manifest\_checksums.json} (SHA-256 for all 960 raw JSON files)
  \item \textbf{Comparative Statistics Script}: \texttt{research/experiments/analysis/compute\_comparative\_stats.py}
  \item \textbf{Figure Generator}: \texttt{research/experiments/analysis/generate\_figures.py}"*
- **Revised Text**:
  > *"\item \textbf{Frozen Campaign Commits}: Protocol freeze at \texttt{a5c0fb97deb8a67705a80e76b49e25f31d692492}; 960-trial campaign completion at \texttt{e156b92}; 600-trial confirmatory campaign completion at \texttt{4a06217} (source code identical to \texttt{370fd6c5a138321ee3741bb7774e950a30d23587}).
  \item \textbf{Targeted Validation Executable Provenance}: The 600-trial confirmatory executable (\texttt{targeted\_runner}, SHA-256: \texttt{2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f}) was independently audited in Phase I6.10. Rebuilding from the native source tree on Go 1.27.3 yields a byte-for-byte exact SHA-256 and Go Build ID match. Because the execution directory lacked Git version metadata, raw trial records record \texttt{git\_revision = \"unknown\_rev\"}.
  \item \textbf{Raw Datasets}: 960-trial exploratory campaign at \texttt{research/experiments/data/campaign\_wsl2\_ext4\_restart\_v1/}; 600-trial confirmatory campaign at \texttt{research/experiments/data/targeted\_validation\_v1/}.
  \item \textbf{Checksum Manifests}: SHA-256 manifests provided for both datasets (\texttt{manifest\_checksums.json}).
  \item \textbf{Analysis Scripts}: Full reproducibility package at \texttt{research/experiments/analysis/} and \texttt{research/reviews/}."*
- **Justification**: Completely and transparently discloses Git commit hierarchy, executable verification hashes, and the `unknown_rev` metadata artifact.
