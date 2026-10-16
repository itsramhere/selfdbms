# Decision Records (D-###)

This document tracks all significant architectural and technical design decisions chronologically, following the format:
`ID | date | sub-phase | problem | alternatives | chosen option | why | trade-off | evidence path | commit`

---

| ID | Date | Sub-phase | Problem | Alternatives | Chosen Option | Why | Trade-off | Evidence Path | Commit |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| D-001 | 2026-10-16 | I0.1 | Branching strategy for research execution | 1. Work directly on `main`<br>2. Create dedicated feature branch `research/reader-compactor-sync` | Create dedicated feature branch `research/reader-compactor-sync` | Protects baseline repository history and allows isolated tracking of research phases without rewriting git history | Branch management overhead | Git history | TBD (I0.1 commit) |
