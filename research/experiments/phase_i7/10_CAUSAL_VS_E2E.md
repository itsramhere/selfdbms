# 10 — End-to-End Comparisons versus Causal Mechanism Tests

**Status:** DESIGN DOCUMENT. Defines what each block may be used to claim, before any data exist. **Windows-amended 2026-10-28: claim types unchanged; platform scope narrowed.**
**Why this document exists:** the frozen manuscript mixes mechanistic language ("starves", "is driven by", "due to the CPU costs of…") with observational comparisons. Reviewers object to exactly that. I7 separates the two, and this file fixes the vocabulary in advance so the manuscript cannot drift back.

> **Windows amendment.** The three claim types and every block assignment stand. What changes is the **scope of generalization**:
>
> - §6's critical-section ratio ρ is now measured on Windows/NTFS with a warm file cache. That makes ρ a property of *this engine's read path*, not of a storage device, which actually **strengthens** the mechanism argument: the effect cannot be attributed to disk behaviour because there is no disk behaviour in the measurement.
> - The causal contrast A → A-min is now backed by an *implemented and tested* change ([13](13_G1_REVIEW_EVIDENCE.md)), not a proposed one, so the single-variable requirement is verifiable against a real diff.
> - No claim may generalise to Linux, WSL2 or enterprise NVMe. The platform statement in [12](12_WINDOWS_EXECUTION_AMENDMENT.md) §1 is the boundary.

---

## 1. Three Kinds of Claim

| Type | Question it answers | What licenses it |
| :--- | :--- | :--- |
| **E2E comparison** | "Under workload W and platform P, does arm X complete more work / have lower latency than arm Y?" | A randomized, paired, adequately powered comparison. No mechanism. |
| **Single-variable causal contrast** | "Does changing *this one structural property* change the outcome?" | Two arms that differ by exactly one audited code change, with correctness equivalence on everything else. |
| **Mechanism test** | "Through which intermediate quantity does the change act, and is that pathway sufficient and quantitatively closed?" | An intervention on the intermediate + measurement of it + a closure check against the outcome + exclusion of rivals. |

The frozen matrix already has a claim-type column, and two claims were found unsupported precisely because a comparative claim was written as a causal one.

---

## 2. Assignment of I7 Blocks

| Block | Claim type | Why |
| :--- | :--- | :--- |
| **E1** (A vs A-min vs B, B1 shape) | **A → A-min: single-variable causal.** A-min → B: design bundle, comparative only. A → B: E2E. | A→A-min changes exactly one property: whether `m.Mu.RLock` is held across the scan (plus the lease that makes it safe). Audited by the 5-site diff and the T-14 static check. |
| **E2** (publication tail) | **A → A-min: single-variable causal.** A-min → B: comparative. | Same one-property change; the publication path differs in *where* the writer lock is taken. |
| **E3** (churn, free vs equal-work) | **Comparative with a work-matching intervention.** The free↔equal-work manipulation is a *design* intervention, not a code change. | It removes a confound; what remains is still a design comparison. |
| **E4** (open loop) | **Comparative at a controlled independent variable** (offered load). | Offered rate is manipulated by the experimenter, but the arms still differ by bundles. |
| **E5** (critical-section manipulation) | **Dose-response mechanism test** (manipulated work inside the read critical section), with microbenchmark support. | The amount scanned per read is manipulated independently of the arm by changing the hit ratio; the prediction is a monotone shape, not a single difference. |
| **E6/D1–D2** (profiles, counters, microbench) | **Mechanism evidence.** | Measures the intermediate quantities. |
| **Deterministic barrier tests** | **Intervention-based causal test.** | The publication hold is manipulated directly; reader arrival is aligned with it. |
| **E7** (Pebble) | **Descriptive external anchor.** | No shared manipulation, no matching of durability, no common arm structure. |

---

## 3. Why A → A-min Is Causal and A-min → B Is Not

A causal claim from a code contrast requires that **nothing else changed**. For A → A-min:

| Requirement | Evidence |
| :--- | :--- |
| One structural property differs | The 5-site diff ([01](01_A_MIN_DESIGN.md) §10); T-14 static check |
| Observational equivalence on everything else | T-13 differential oracle: 10⁶ `(key, readTs)` probes, identical values and error strings including tier-precedence and tombstone masking |
| Same workload, same seed, same key stream, same schedule | Triplet design + counter-based key stream (H-11) |
| Same write path, WAL, flush, compaction algorithms | Explicit no-change list ([08](08_HARNESS_CHANGES.md) §2) |
| Same binary, same build flags | One binary, one build, mode selected at construction |

Therefore: "releasing the read lock before the scan (with leases) **causes** the observed change in writer progress" is a defensible causal statement, scoped to this engine and workload.

For A-min → B, at least three things change at once:

1. pinning is Θ(1) via a single immutable version instead of Θ(k) leases;
2. publication allocates and swaps a version instead of mutating a slice;
3. reclamation is refcounted and asynchronous instead of `freeOnce` on the last lease.

A difference between A-min and B is therefore attributable to *the bundle*, not to any member of it. The manuscript must say "the versioned design" or "the snapshot mechanism as a whole", never "reference counting causes…". If a member-level claim is desired, it requires another arm; I7 does not provide one, and the microbenchmarks (D2) supply only component costs, not component effects.

---

## 4. What a Mechanism Claim Requires (and How I7 Satisfies It)

A mechanism claim of the form "X acts through M to produce Y" needs all four:

| Requirement | I7 evidence |
| :--- | :--- |
| **(i) Intervention on X** | A→A-min code change; `k` manipulation in E5; publication-hold manipulation in the barrier test; offered-rate manipulation in E4 |
| **(ii) Measurement of M** | Lock-occupancy counters (D2), `records_scanned`, `k`, publication hold `hold_e`, queueing delay |
| **(iii) Closure between M and Y** | The decomposition model with a closure ratio in [0.5, 2.0] ([09](09_WORKLOAD_D_PROFILING.md) §6); the dose-response monotonicity in E5; `block_ratio ≈ 1` in the barrier test |
| **(iv) Exclusion of rival pathways** | E3 equal-work removes the work-composition rival; `records_scanned` removes the cardinality rival; `t_meta` measurement removes the descriptor-cost rival; `read_bytes`/fsync removes the storage rival |

If any of (i)–(iv) fails, the claim is demoted to "consistent with" — not deleted, but not asserted as mechanism.

### 4.1 The deterministic barrier test (the strongest available intervention)

The existing test (`TestT1_DeterministicPublicationBarrier`) aligns reader arrival with the publication lock acquisition. Extend it to three arms:

| Arm | Predicted reader arrival latency | Rationale |
| :--- | :--- | :--- |
| A | ≈ the publication hold | readers wait for `m.Mu.Lock()` to be released |
| A-min | ≈ 0 | readers only wait for the brief O(k) capture |
| B | ≈ 0 | readers pin a version and never wait |

This is a *direct* manipulation: the experimenter controls the hold duration and the arrival instant, and observes the delay. It is not a performance measurement and has no sample-size question. It is the cleanest causal evidence in the study and costs ~0 trials.

---

## 5. Threats to Each Claim Type

### 5.1 E2E comparisons

| Threat | Mitigation |
| :--- | :--- |
| Composition shift (arms do different work) | report total ops, writes, flushes, `k`; E3 work matching; never call a read-only ratio "throughput" |
| Coordinated omission | open-loop E4 + stall-event metrics ([03](03_PACING_AUDIT.md), [04](04_OFFERED_LOAD_METHODOLOGY.md)) |
| Instrumentation correlated with treatment | per-worker sharding (H-02/H-03) |
| Platform drift | D3 reference triplets, position covariate |
| Order effects | balanced within-triplet permutations, randomized runlist |
| Multiple comparisons | Holm across pre-registered families |

### 5.2 Single-variable causal contrast (A → A-min)

| Threat | Mitigation |
| :--- | :--- |
| Hidden second change | diff audit + T-14 |
| Behavioural divergence under rare interleavings | T-13 oracle at 10⁶ probes + `-race` ×20 |
| Lease overhead masquerading as the effect | the effect is measured *with* leases present; the lease cost is separately reported (E5, D2), so the claim is "the change, including its necessary bookkeeping" — not "unlocking is free" |
| The A arm is not the frozen A | byte-identical A code path, same binary, mode flag |

### 5.3 Mechanism tests

| Threat | Mitigation |
| :--- | :--- |
| Post-hoc story fitting | hypotheses fixed in [09](09_WORKLOAD_D_PROFILING.md) §2; closure threshold fixed at [0.5, 2.0] |
| Profile overhead distorting the mechanism | separate build, never used for performance numbers |
| A single intermediate explains only part | report the fraction per term; no single-cause language |
| Generalization beyond the engine | see §6 |

---

## 6. External Validity of a Mechanism: the Critical-Section Ratio

The mechanism "a long read critical section starves writers" generalizes through a **dimensionless ratio**, not through the observed factor:

```
ρ = T_readCS / T_writeCS
```

where `T_readCS` is the time the reader holds the exclusive-compatible lock and `T_writeCS` is the writer's exclusive hold.

| Regime | `T_readCS` | `T_writeCS` | ρ | Expected starvation |
| :--- | ---: | ---: | ---: | :--- |
| This engine, Workload A/B (scan-dominated) | 2–31 ms | ≈ 0.2–3 ms | **1–10** | severe (observed) |
| This engine with H-09-style read optimisation (if ever) | ~10–100 µs | ≈ 0.2–3 ms | 0.003–0.05 | mild |
| Production LSM, page-cache hit | ~1–10 µs | ~1–10 µs (plus group commit) | 0.1–1 | workload-dependent |

Consequences for the manuscript:

1. The observed 3.3–18.5× write-progress gaps are an **upper bound** attainable when `ρ ≫ 1`; they are not a prediction for production engines.
2. The correct portable statement is: *"the magnitude of writer starvation scales with ρ; in our engine ρ ≈ 1–10 because point reads are linear scans, and the effect disappears when the read critical section is shortened"*. That statement is supported by E1 and E5 and is honest.
3. The paper should report the measured `T_readCS` and `T_writeCS` per cell (D2) so a reader can compute ρ for their own system.

This is the single most important reframing for external validity, and it is only available because I7 measures the intermediate quantities.

---

## 7. Allowed Wording per Claim Type

| Claim type | Allowed | Not allowed |
| :--- | :--- | :--- |
| E2E comparison | "under workload W, arm X completed 2.1× more writes than arm Y (95 % CI …)" | "X is faster", "X eliminates starvation" |
| Single-variable causal | "releasing the read lock before the scan (with lease-based lifetime management) **caused** writer progress to increase by …" | "locking causes…", "snapshots fix…" |
| Design-bundle comparison | "the versioned design differs from the lease design by …" | "reference counting causes…", "versioning is unnecessary" (unless P1b/P1c hold) |
| Mechanism | "the delay is accounted for by `m.Mu` writer occupancy: 78 % of the latency gap closes under the decomposition (closure 0.86)" | "the delay is due to descriptor overhead" (unless closure holds for that term) |
| Descriptive external anchor | "on the same workload, the reference engine showed … . Absolute ratios are not comparable because …" | any comparative performance claim |
| Negative mechanism | "descriptor cost accounts for < 10 % of the gap; the attribution is withdrawn" | "there is no overhead" |

---

## 8. Revised Claim–Evidence Map (to be filled by G6)

This table is the target structure for the manuscript revision. Each row must cite a block, a metric family, and a claim type.

| # | Candidate claim | Block | Metric | Type | Pre-registered decision |
| :--- | :--- | :--- | :--- | :--- | :--- |
| C1 | A long read critical section causes writer starvation | E1 (A vs A-min) | completed writes | single-variable causal | P1a |
| C2 | Lease-based unlocking captures most of the versioned design's writer-progress benefit | E1 | capture fraction, NI bound | single-variable causal + bound | P1b, P1c |
| C3 | Versioned pinning costs Θ(1) metadata per read vs Θ(k) for leases | D2 microbench | ns/read vs `k` | component cost (not an effect) | P5c |
| C4 | A publication blocks arriving readers for approximately the hold duration | E2 + barrier test | `block_ratio`, arrival latency | mechanism (intervention) | P2a, P2c |
| C5 | At matched offered load, shortening the read critical section reduces response p99 | E4 | `y_p99` | comparative at controlled load | P4a |
| C6 | The churn read penalty is not caused by snapshot descriptor cost | E3 + D1 | closure, equal-work ratio | negative mechanism | P3b, closure |
| C7 | The churn penalty is (partly) a lock-occupancy-sharing effect | D2 + E3 | writer lock occupancy, median latency | mechanism (closure) | closure + H2 |
| C8 | The magnitude of starvation scales with the critical-section ratio ρ | E5 + D2 | write progress vs `k`, `T_readCS`, `T_writeCS` | mechanism (dose-response) | P5a |
| C9 | Absolute throughput versus a mature Go LSM | E7 | ops/s, latency | descriptive | none |

Claims that cannot be supported by their designated block are removed from the manuscript, not downgraded to "observations" in the abstract.

---

## 9. One-Sentence Summary of the Separation

**E1/E2 tell us what changes when one property changes; E3/E4 tell us what happens under matched work and matched load; E5 and D1–D2 tell us through what quantity it happens and whether that account closes; E7 tells us nothing causal at all.**
