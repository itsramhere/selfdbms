# Research Artifacts & Reproducibility Guide

## Project Title
**Reader–Compactor Synchronization in a Go LSM Storage Engine**

This directory contains research records, architectural documentation, formal invariants, debug archives, benchmarks, and experimental data investigating synchronization between point readers and SSTable compaction in a custom Go LSM-tree storage engine.

---

## 1. Environment & Platform Specification

Captured during **Sub-phase I0.1** (Baseline reconnaissance):

| Parameter | Value / Specification |
| :--- | :--- |
| **Base Git Commit** | `08d3189069294e8168ebb5234a3bef1d37f1d649` |
| **Research Branch** | `research/reader-compactor-sync` |
| **Windows Go Version** | `go version go1.26.3 windows/amd64` |
| **WSL2 Go Version** | `go version go1.27.1 linux/amd64` (`/snap/bin/go`) |
| **Host Operating System** | Microsoft Windows 11 Home Single Language (Version 10.0.26200, Build 26200, 64-bit) |
| **Linux Subsystem** | WSL2 Ubuntu 24.04 LTS (Linux 5.15.167.4-microsoft-standard-WSL2 x86_64) |
| **CPU Model** | AMD Ryzen 7 7445HS w/ Radeon 740M Graphics (6 physical cores, 12 logical processors) |
| **Primary Storage Device** | SAMSUNG MZVL8512HFLU-00BH1 (NVMe / PCIe, 512 GB) |
| **Filesystem Type** | Windows NTFS (native) & Plan 9 / 9P (WSL2 `/mnt/c` mount) |
| **Go Module Name** | `owndbms` (defined in `go.mod`) |
| **Direct / Indirect Deps** | `go.etcd.io/bbolt v1.5.0`, `golang.org/x/sys v0.45.0` |


---

## 2. How to Build and Test

### Building the Project
From the repository root:
```powershell
go build ./...
```
Or to build the package specifically:
```powershell
go build ./lsmengine
```

### Running Tests
Standard test execution:
```powershell
go test -v ./lsmengine
```

Race detection execution:
```powershell
go test -v -race ./lsmengine
```

Targeting specific unit tests:
```powershell
go test -v ./lsmengine -run TestBasicPutGet
go test -v ./lsmengine -run TestDeleteAndTombstone
go test -v ./lsmengine -run TestWALRecovery
go test -v ./lsmengine -run TestSkipListLevelDistribution
```

Running benchmarks:
```powershell
go test -v -bench=. -run=^# ./lsmengine
```

*Note on Windows file locking*: The engine creates local directories (`test_data_*`) during test runs. Due to Windows NTFS file locking semantics, ensure any open file descriptors are properly closed prior to directory removal or cleanup.

---

## 3. Repository Tree & Directory Index

```
.
├── 01_IMPLEMENTATION_PLAN.md          # Specification: Implementation Plan (Phases I0-I6)
├── 02_DEBUGGING_VALIDATION_PLAN.md    # Specification: Debugging & Validation Plan (Phases D0-D7)
├── go.mod                             # Go module declaration (owndbms)
├── go.sum                             # Checksums for external dependencies
├── paper.tex                          # Manuscript draft on LSM engine MVCC/I/O trade-offs
├── Results.md                         # Historical raw performance notes
├── lsmengine/                         # Storage engine source package
│   ├── compaction.go                  # Merging and compaction logic
│   ├── compactionHeap.go              # Min-heap priority queue for multi-way merge
│   ├── comparison_bench_test.go       # BoltDB vs LSMEngine comparison benchmark
│   ├── engine_test.go                 # Core unit tests (SkipList, Put/Get, Delete, WAL)
│   ├── keys.go                        # Internal key formatting, timestamping, tombstones
│   ├── manager.go                     # Storage engine manager and lifecycle coordinator
│   ├── manager_bench_test.go          # Microbenchmarks for engine operations
│   ├── memtable.go                    # Memtable implementation using SkipList
│   ├── sstableBuilder.go              # SSTable serialization and block formatting
│   ├── sstableIterator.go             # SSTable reader iterator
│   ├── sstableReader.go               # SSTable block-indexed reader
│   ├── transaction.go                 # Transaction abstraction (Put, Get, Delete, Commit)
│   ├── wal.go                         # Write-Ahead Log sequencing and replay
│   └── testresults/                   # Historical benchmark output JSON files
└── research/                          # Research coordination and evidence workspace
    ├── README.md                      # This file (environment, build, test, directory index)
    ├── checkpoints/                   # Review packets (CP0.md - CP6.md)
    ├── debug/                         # Issue repro scripts, logs, and test matrix
    ├── design/                        # Architecture, invariants, alternative designs
    ├── experiments/                   # Workload specifications, run results, raw data
    ├── figures/                       # Plotting scripts and generated figures
    ├── logs/                          # DECISIONS.md, ISSUES.md, WORKLOG.md
    └── paper/                         # Claim-to-evidence mappings and paper draft assets
```

---

## 4. Research Logs & Navigation

- **Decision Records**: [`research/logs/DECISIONS.md`](logs/DECISIONS.md) (`D-###`)
- **Issues & Anomalies**: [`research/logs/ISSUES.md`](logs/ISSUES.md) (`E-###`)
- **Activity Log**: [`research/logs/WORKLOG.md`](logs/WORKLOG.md)
- **Checkpoints**: [`research/checkpoints/`](checkpoints/)
- **Architecture & Lock Hierarchy**: [`research/design/architecture.md`](design/architecture.md)
- **Invariants**: [`research/design/invariants.md`](design/invariants.md)
- **Claim-Evidence Map**: [`research/paper/CLAIM_EVIDENCE_MAP.md`](paper/CLAIM_EVIDENCE_MAP.md)
