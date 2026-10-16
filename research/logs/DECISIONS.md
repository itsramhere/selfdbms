# Decision Records (D-###)

This document tracks all significant architectural and technical design decisions chronologically, following the format:
`ID | date | sub-phase | problem | alternatives | chosen option | why | trade-off | evidence path | commit`

---

| ID | Date | Sub-phase | Problem | Alternatives | Chosen Option | Why | Trade-off | Evidence Path | Commit |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| D-001 | 2026-10-16 | I0.1 | Branching strategy for research execution | 1. Work directly on `main`<br>2. Create dedicated feature branch `research/reader-compactor-sync` | Create dedicated feature branch `research/reader-compactor-sync` | Protects baseline repository history and allows isolated tracking of research phases without rewriting git history | Branch management overhead | Git history | `8732000` |
| D-002 | 2026-10-16 | I1.1 | Plan amendment for E-001 & Approach A invariant specification | 1. Implement ad-hoc fix during I1.2<br>2. Insert formal sub-phase I1.1a to restore commit correctness before compaction work | Insert formal sub-phase I1.1a; codify Approach A baseline invariants | Establishes a verifiable working baseline before touching compactor synchronization; preserves scientific rigor | Adds mini-checkpoint CP0.5 review step | `01_IMPLEMENTATION_PLAN.md`, `research/design/invariants.md` | `e46f6db` |
| D-003 | 2026-10-16 | I1.1a | Self-deadlock in `Transaction.Commit` (E-001) | 1. Duplicate traversal loop inside `Transaction.Commit`<br>2. Make `sync.RWMutex` recursive<br>3. Extract unexported `latestValueLocked` helper | Extract unexported `latestValueLocked` helper; `Commit` calls it directly while holding `t.Man.Mu.Lock()` | Eliminates self-deadlock with minimal diff without code duplication, preserves public `LatestValue` API and conflict detection | None; cleanly separates locking boundary | `research/debug/E-001/`, `lsmengine/manager.go`, `lsmengine/transaction.go` | Pending I1.1a commit |
| D-004 | 2026-10-16 | I1.1a | Race detector execution on Windows without native CGO | 1. Install MinGW-w64 on Windows<br>2. Use WSL2 Ubuntu with existing GCC and Go toolchain | Use WSL2 Ubuntu environment for `-race` testing | Avoids invasive host OS toolchain modifications, uses native Linux C toolchain approved by reviewer | Commands must be executed via `wsl` bridge | `research/debug/E-001/after.log` | Pending I1.1a commit |
