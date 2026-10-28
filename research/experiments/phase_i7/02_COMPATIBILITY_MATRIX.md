# 02 — Operation and Durability Compatibility Matrix

**Status:** DESIGN + **partial verification**. Pebble v1.1.5 was downloaded into an isolated probe module and executed on Windows (nothing vendored, root module untouched); see §8 and [pebble_probe/FINDINGS.md](pebble_probe/FINDINGS.md). Badger was not downloaded.
**Purpose:** establish (a) what A-min must preserve relative to A and B, and (b) exactly which comparisons to Pebble (and, if ever, Badger) are semantically meaningful.

**Evidence basis:** our arms from source at `f1a561a` (A-min now implemented and tested, [13](13_G1_REVIEW_EVIDENCE.md)); Pebble from a pinned-version Windows probe (v1.1.5) plus its published interface; Badger from its published interface only. Cells marked **⟨V⟩** still require a source audit of the pinned version before a *semantic* claim is made; the probe established build/lifecycle behaviour, not every internal detail.

**Platform note:** the matrix was written for the original Linux plan. §8 records what changes on Windows/NTFS, including the two durability facts that differ.

---

## 1. Legend

| Mark | Meaning |
| :--- | :--- |
| ● | Supported, and semantically comparable across engines |
| ◐ | Supported but with a semantic difference that must be documented; comparison restricted |
| ○ | Not supported / not exposed |
| — | Not applicable |
| ⟨V⟩ | Requires source audit of the pinned engine version before use |

---

## 2. Operation Compatibility

| # | Operation | A | A-min | B | Pebble | Badger |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| O-01 | Point read by user key | ● | ● | ● | ● `DB.Get` | ● `Txn.Get` |
| O-02 | Point read at an explicit MVCC read timestamp | ● `Get(key, readTs)` | ● identical | ● identical | ○ no per-call read timestamp; user snapshot only ⟨V⟩ | ◐ read-only `Txn` at `readTs` ⟨V⟩ |
| O-03 | Read-your-writes inside a transaction | ● `Transaction.Get` checks `WriteSet` | ● | ● | ◐ batch read-your-writes via `Batch.Get` ⟨V⟩ | ● |
| O-04 | Multi-key write transaction | ● `WriteSet` map | ● | ● | ● `Batch` ⟨V⟩ | ● `Txn` |
| O-05 | Per-key write-conflict detection at commit | ● `latestValueLocked > ReadTs ⇒ abort` | ● | ● | ○ no transactional conflict detection | ● `ErrConflict` ⟨V⟩ |
| O-06 | Commit fsyncs before returning | ● per **key** (`Wal.Append` → `File.Sync`) | ● | ● | ● `WriteOptions{Sync:true}` ⟨V⟩ | ● `SyncWrites:true` ⟨V⟩ |
| O-07 | Group commit | ○ | ○ | ○ | ◐ internal commit pipeline; not user-tunable ⟨V⟩ | ◐ ⟨V⟩ |
| O-08 | Delete / tombstone | ● | ● | ● | ● | ● |
| O-09 | Ordered range scan | ○ (internal iterator only, used by compaction) | ○ | ○ | ● `NewIter` | ● `NewIterator` |
| O-10 | User-visible snapshot across several reads | ○ | ○ | ○ | ● `DB.NewSnapshot` | ● read-only `Txn` |
| O-11 | Forced flush | ● `TriggerFlush` | ● | ● | ● `DB.Flush` ⟨V⟩ | ◐ memtable flush is internal; `Flatten` is different ⟨V⟩ |
| O-12 | Forced compaction | ● `CompactAll` (all tables) | ● | ● | ● `DB.Compact(start,end)` ⟨V⟩ | ◐ level-based control only ⟨V⟩ |
| O-13 | Flush/compaction lifecycle observation | ● `FlushObserver` | ● (same observer) | ● | ● `EventListener` ⟨V⟩ | ○ no equivalent event stream ⟨V⟩ |
| O-14 | Reads concurrent with flush publication | ◐ reads blocked (lock excludes publication) | ● reads not blocked | ● reads not blocked | ● readState pinned, publication not blocking ⟨V⟩ | ● ⟨V⟩ |
| O-15 | Reads concurrent with compaction publication | ◐ blocked | ● not blocked | ● not blocked | ● ⟨V⟩ | ● ⟨V⟩ |
| O-16 | Immutable reader-set versioning | ○ | ○ (leases only) | ● `ReaderSnapshot` + `SharedReader` refcounts | ● internal versions + `readState` ⟨V⟩ | ● internal versions ⟨V⟩ |
| O-17 | Descriptor reclamation | lock-excluded (close after `Lock`) | lease-drain + `freeOnce` | refcount + async cleanup worker | obsolete-file deletion ⟨V⟩ | obsolete-file deletion + vlog GC ⟨V⟩ |
| O-18 | Runtime switch between synchronization modes | ○ constructor only | ○ | ○ | — | — |
| O-19 | Engine starts from pre-existing SSTables | ○ no directory discovery at startup | ○ | ○ | ● MANIFEST | ● MANIFEST |
| O-20 | Instrumentable read-side block time | ○ | ◐ (build-tag counters) | ◐ (build-tag counters) | ○ only via external tracing ⟨V⟩ | ○ ⟨V⟩ |

### 2.1 Consequences for the study design

- **O-02/O-05** mean Pebble cannot be a drop-in replacement for any arm: it has no read timestamp and no conflict detection. It can only be run on workloads that do not depend on either (Workloads A, B1-like read/write mix without conflicts, D churn).
- **O-13** means the publication-only driver (H-07) is implementable on our engine and *not* on Pebble; therefore E2 and E7 must not be compared event-for-event.
- **O-19** means the fixture must be built in-process for our engine on every trial (root cause of the 31–42 s fixture cost, AF-7). Pebble can be pre-populated and snapshotted, but then the *fixture* differs; we therefore specify that Pebble also be populated through the same in-process driver, writing the same keys through its API (E7 §4).
- **O-20** is why [09](09_WORKLOAD_D_PROFILING.md) uses a separate instrumented build rather than instrumentation in the performance binary.

---

## 3. Durability Compatibility

| # | Property | A | A-min | B | Pebble (`Sync:true`) | Badger (`SyncWrites:true`) |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| D-01 | Commit durability model | WAL record `fsync`ed before commit returns | identical | identical | WAL `fsync`ed before commit returns ⟨V⟩ | WAL + value-log `fsync` ⟨V⟩ |
| D-02 | fsync granularity | **per key**, not per transaction | identical | identical | per batch / group ⟨V⟩ | per transaction ⟨V⟩ |
| D-03 | WAL rotation | new `wal_<ns>.log` file created on every freeze | identical | identical | WAL recycling + MANIFEST ⟨V⟩ | WAL rotation + MANIFEST ⟨V⟩ |
| D-04 | Crash recovery replays all segments | **○** `RecoverFromWAL` takes one path (RISK-002) | ○ | ○ | ● ⟨V⟩ | ● ⟨V⟩ |
| D-05 | Recovery metadata | none (no manifest) | none | none | MANIFEST ⟨V⟩ | MANIFEST ⟨V⟩ |
| D-06 | SSTable durability on flush | ● `SSTableBuilder.Close` → `File.Sync` ([sstableBuilder.go:100](../../../lsmengine/sstableBuilder.go)) | ● | ● | ● ⟨V⟩ | ● ⟨V⟩ |
| D-07 | Directory fsync after file create/rename | **○** | ○ | ○ | ● ⟨V⟩ | ● ⟨V⟩ |
| D-08 | Torn/partial record detection | ● WAL CRC32 per record | ● | ● | ● checksums ⟨V⟩ | ● checksums ⟨V⟩ |
| D-09 | SSTable data checksums verified on read | **○** (magic number only) | ○ | ○ | ● block checksums ⟨V⟩ | ● ⟨V⟩ |
| D-10 | WAL retention after flush | segments retained, never deleted | identical | identical | recycled after sstable durability ⟨V⟩ | recycled ⟨V⟩ |
| D-11 | Crash-consistency test coverage in repo | scenarios A–E documented; no power-loss harness | — | — | — | — |
| D-12 | Power-loss claim we may make | "committed-and-fsynced writes are on durable storage; recovery across rotation is not implemented" | identical | identical | — | — |

### 3.1 The matched-durability contract for E7

The only durability statement that is simultaneously true of our engine and Pebble is:

> **A write that has returned success has had its WAL record `fsync`ed (or group-`fsync`ed) before the call returned. Neither engine guarantees directory-entry durability for newly created files in this configuration; neither engine was subjected to power-loss injection in this study.**

Therefore E7 matches:
- `Sync:true` on every Pebble write (no `NoSync`, no `DisableWAL`);
- the same key and value sizes, keyspace cardinality, hit ratio and read distribution;
- the same process-level concurrency and the same measurement window.

E7 explicitly **does not** match, and will not claim to match:
- fsync granularity (ours is per key, Pebble's is per batch) → **this is a large, uncontrolled difference in write-path cost**;
- group commit (Pebble has it, we do not);
- rotation/recovery protocol;
- reader-set versioning details.

**Consequence:** E7 write-side numbers are not comparable at all. Only read-side numbers from read-only shapes (Y1) and the *qualitative shape* of the latency distribution are reported. See §5.

---

## 4. What Each Comparison Can Support

| Comparison | Design | Supports | Does **not** support |
| :--- | :--- | :--- | :--- |
| A vs A-min | Paired triplet, same seed, balanced order | A **single-variable causal** claim: releasing the read lock before the scan (with leases) changes writer progress and tail exposure | Any claim about versioning |
| A-min vs B | Paired triplet | A **design-bundle** comparison: does Θ(k) per-read leasing vs Θ(1) versioned pinning matter at cardinality k | A causal claim about any single mechanism |
| A vs B | Paired triplet (as in history) | End-to-end effect of the full design difference | Mechanism |
| A/A-min/B vs Pebble | Unpaired, absolute, descriptive | External plausibility: is the observed behaviour plausibly a synchronization phenomenon rather than an engine bug; is the tail *shape* qualitatively similar | Any ratio-based performance claim; any causal claim |
| A/A-min/B vs Badger | Not run | — | — |

---

## 5. Pebble Integration Requirements (E7)

All items are prerequisites; any unmet item downgrades the block to "exploratory".

1. **Pinned version** recorded by module path, pseudo-version, and `go.sum` hash; source audit of: `DB.Get` read path (`readState` acquisition), `WriteOptions.Sync` handling, `Flush` semantics, `EventListener` flush/compaction callbacks, obsolete-file deletion.
2. **Single-threaded fixture population**, same 10,000 keys / 128-byte values / internal-key-equivalent ordering, via `Set` with `Sync:true`, then `Flush()` to reach 4 SSTables. Assert `Metrics().NumFiles`-equivalent table count = 4 ⟨V⟩ before warmup; fail closed otherwise.
3. **Read path**: `DB.Get` with the same key-sampling stream (counter-based, H-11), no `NewSnapshot` per read (that would add a version pin the arm does not have).
4. **Write path (only for the mixed shape)**: 2 writers, `Set` with `Sync:true`.
5. **Churn shape**: `MemTableSize = 16 KiB` ⟨V⟩, automatic compactions enabled, and *record the realized flush/compaction counts from `Metrics()`* so the work-composition check (AF-4) can be applied to Pebble too.
6. **Forced publication** is not matched; the churn shape relies on natural flushes in both engines.
7. **Metrics extraction**: reads completed, writes completed, latency samples (recorded by our harness around the call), flush/compaction counts, table count. Pebble's own internal histograms are recorded but not used for inference.
8. **Process isolation**: E7 runs in a separate binary (separate module) that shares only the workload generator and the latency recorder contract. No shared mutable state, no shared fixture directory.

### 5.1 Y-shapes

| Shape | Description | Purpose |
| :--- | :--- | :--- |
| **Y1** | Read-only, 4 tables, 10k keys, 95 % hit, `r = 8`, 10 s | Absolute read cost and tail shape vs a mature LSM |
| **Y2** | Mixed, 2 writers with sync, `r = 8`, 10 s | Qualitative write-progress behaviour |
| **Y3** | Churn, 16 KiB memtable, `r = 8`, 10 s | Realized work composition under churn |

8 trials per shape = 24.

---

## 6. Badger: Exclusion Rationale (OD-3)

Badger is a legitimate engine, but it is the wrong control for this paper:

1. **Durability semantics are structurally different.** Badger is a WiscKey-style design in which values live in an append-only value log and the LSM stores keys with pointers. Matching our durability (WAL `fsync` per key, SSTable `fsync` on flush, no directory `fsync`, single-segment replay) is impossible without either weakening Badger's guarantees or misrepresenting ours. Any durability comparison would be an *architectural* difference, not a controlled one.
2. **It cannot represent any arm.** Like Pebble, Badger has versioned reads and no manager-wide read lock, so it is neither A nor A-min nor B; it would only add a second external anchor that is *harder* to match than Pebble.
3. **Its additional architectural distance does not buy discrimination.** Reviewers' R1 asks whether the synchronization mechanism is what matters. That question is answered by A vs A-min vs B; adding a value-log engine cannot answer it better, and it introduces value-log GC as an uncontrolled third workload.
4. **Cost.** 10 trials plus an integration/audit effort comparable to Pebble's, for a descriptive anchor that duplicates Pebble's role.

**If a reviewer insists**, the minimal admissible form is: one shape (Y3 churn), 8 trials, reported descriptively, with an explicit statement that durability is unmatched and that value-log GC is an active, uncontrolled background process.

---

## 7. A-min Compatibility Contract

A-min must be **bit-for-bit observationally equivalent to A** for every operation in this matrix. The only permitted observable differences are:

| Permitted difference | Where it shows up |
| :--- | :--- |
| Read latency and throughput | E1–E5 |
| Writer progress | E1, E3 |
| Time a table remains open after supersession | instrumentation only |
| Per-read metadata cost Θ(k) | E5, D2 |

Not permitted, and gated by test T-13: returned values, error identities, error strings, tier precedence, tombstone masking, MVCC masking, `readTs` semantics, WAL bytes written, SSTable bytes written, and recovery behaviour.

**Status at G1:** the contract is met for the operations exercised by `TestAMin_ParityWithApproachA` (puts, deletes, overwrites, hits, misses, tombstones, absent keys) on Windows/NTFS. It is *not* yet met for the `⟨V⟩`-free rows of §2 that the parity test does not cover: range scan (O-09) is unsupported in both arms, and crash recovery (D-04) is unchanged and equally limited in all three arms.

---

## 8. Windows Amendment (2026-10-28)

### 8.1 What changed

| Item | Linux plan | Windows reality |
| :--- | :--- | :--- |
| Filesystem | ext4 / XFS on a block device | **NTFS** on an internal NVMe partition |
| Delete semantics | unlink-while-open is normal | Go requests `FILE_SHARE_DELETE`, so unlink may succeed while a descriptor is open (delete-pending); third-party handles can still block it |
| `fsync` primitive | `fdatasync`/`fsync` | `FlushFileBuffers` via `File.Sync`; device-side media commit depends on the drive's volatile cache |
| Directory `fsync` | not implemented | not implemented (same gap) |
| Cold-read control | `posix_fadvise(DONTNEED)` | **unavailable**; OD-6 withdrawn, all trials warm-cache |
| Timer for latency | `CLOCK_MONOTONIC` (~ns) | **QPC 100 ns** (the harness already used it); `time.Now` is ~0.4 ms granular and unusable |
| Affinity | `taskset` | `Process.ProcessorAffinity`, inherited by children; no admin |
| Race detector | available (`-race` + gcc) | **unavailable** (no C compiler) |

### 8.2 Pebble — verified on Windows

`github.com/cockroachdb/pebble v1.1.5`, `CGO_ENABLED=0`, Go 1.26.3 windows/amd64: builds and passes an isolated lifecycle test (open → 200 `Sync:true` writes → byte-exact reads → `Flush` → `Close` → reopen → re-read → `Close` → `RemoveAll` succeeded immediately). No leaked handles; no `runtime/cgo` in the dependency graph. Transcript: [pebble_probe/FINDINGS.md](pebble_probe/FINDINGS.md).

Therefore §5's requirements 1–7 remain the integration checklist, but the platform blocker is **removed**. Pebble stays descriptive (§4) and is still **not** part of any paired comparison.

### 8.3 Durability wording for the Windows campaign

The only defensible matched statement is:

> "A write that returned success has had its WAL record written and `FlushFileBuffers` issued before the call returned. Neither engine was subjected to power-loss injection; on a consumer NVMe the drive may acknowledge a flush before committing to media, so this is an ordering guarantee, not a media-durability guarantee."

Both our engine (`Wal.Append` → `File.Sync`) and Pebble (`WriteOptions{Sync:true}`) satisfy that sentence. Neither engine fsyncs the containing directory; our engine additionally cannot replay rotated WAL segments (D-04), which Pebble can. That asymmetry is documented in [12](12_WINDOWS_EXECUTION_AMENDMENT.md) §4.8 and must appear in Threats to Validity.

### 8.4 A-min on NTFS

The A-min contract is unchanged, but the *risk it defends against* changed shape: on NTFS the failure is silent descriptor reuse, not a loud sharing violation. The implemented design (lease before unlock, close-then-unlink, 3×10 ms unlink retry, shutdown drain) is validated on NTFS by `TestAMin_CompactionDuringScan` and `TestAMin_NoOrphanTablesUnderChurn` ([13](13_G1_REVIEW_EVIDENCE.md) §4).
