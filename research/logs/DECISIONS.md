# Decision Records (D-###)

This document tracks all significant architectural and technical design decisions chronologically, following the format:
`ID | date | sub-phase | problem | alternatives | chosen option | why | trade-off | evidence path | commit`

---

| ID | Date | Sub-phase | Problem | Alternatives | Chosen Option | Why | Trade-off | Evidence Path | Commit |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| D-001 | 2026-10-16 | I0.1 | Branching strategy for research execution | 1. Work directly on `main`<br>2. Create dedicated feature branch `research/reader-compactor-sync` | Create dedicated feature branch `research/reader-compactor-sync` | Protects baseline repository history and allows isolated tracking of research phases without rewriting git history | Branch management overhead | Git history | `8732000` |
| D-002 | 2026-10-16 | I1.1 | Plan amendment for E-001 & Approach A invariant specification | 1. Implement ad-hoc fix during I1.2<br>2. Insert formal sub-phase I1.1a to restore commit correctness before compaction work | Insert formal sub-phase I1.1a; codify Approach A baseline invariants | Establishes a verifiable working baseline before touching compactor synchronization; preserves scientific rigor | Adds mini-checkpoint CP0.5 review step | `01_IMPLEMENTATION_PLAN.md`, `research/design/invariants.md` | Pending I1.1 commit |
