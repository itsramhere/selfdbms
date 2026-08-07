# SelfDBMS (`lsmengine`)

A persistent, high-performance Key-Value storage engine built in Go based on Log-Structured Merge-tree (LSM-tree) architecture. Features a Write-Ahead Log (WAL) for durability, active in-memory tables, SSTable compaction, transactional capabilities, and comprehensive benchmark reporting.

---

## Key Features

* **Log-Structured Merge-Tree Architecture:** Optimized for high write throughput using in-memory tables and persistent immutable SSTables.
* **Durability (WAL):** Write-Ahead Logging ensures zero data loss across unexpected process crashes or system restarts.
* **Leveled/Size-Tiered Compaction:** Efficient background compaction process using a min-heap iterator to merge SSTable segments and reclaim space from obsolete or deleted keys.
* **SSTable Builders & Readers:** Binary-encoded immutable block files supporting point queries and range iterators.
* **ACID Transactions:** Built-in isolation and transaction management layer (`transaction.go`).
* **Benchmarking & Validation:** Native benchmarking suites comparing engine performance against B+Tree storage layers like BoltDB across Ext4/native filesystems.

---

## Directory Structure

```text
.
├── Results.md                      # Detailed benchmark and performance analysis
├── paper.tex                       # Design/research paper LaTeX source
├── go.mod                          # Go module definition
├── go.sum                          # Module checksums
└── lsmengine/
    ├── wal.go                      # Write-Ahead Log writer and recovery
    ├── memtable.go                 # In-memory skip-list/tree storage
    ├── manager.go                  # Core LSM lifecycle and coordinator
    ├── transaction.go              # Transaction state and concurrency handling
    ├── keys.go                     # Key comparator and serialization
    ├── sstableBuilder.go           # Immutable SSTable file generator
    ├── sstableReader.go            # Block reader and binary index parsing
    ├── sstableIterator.go          # Sequential range iterator across SSTables
    ├── compaction.go               # Compaction logic and merging policies
    ├── compactionHeap.go           # Min-heap data structure for N-way merge
    ├── engine_test.go              # Unit and integration tests
    ├── manager_bench_test.go       # Engine write/read performance benchmarks
    ├── comparison_bench_test.go    # Comparative benchmarks (e.g., vs. BoltDB)
    └── testresults/                # JSON benchmark results and metrics
```

## Getting Started
### Prerequisites
Go: Version 1.20 or higher recommended.

### Installation
Clone the repository and fetch dependencies:
```
git clone <repository-url>
cd selfdbms
go mod download
```

## Usage Example
```
package main

import (
	"fmt"
	"log"

	"selfdbms/lsmengine"
)

func main() {
	// Initialize the LSM Engine Manager
	opts := lsmengine.DefaultOptions("./db_data")
	mgr, err := lsmengine.Open(opts)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer mgr.Close()

	// Write Key-Value pair
	err = mgr.Put([]byte("user:101"), []byte("Alice"))
	if err != nil {
		log.Fatalf("Failed to put key: %v", err)
	}

	// Read Key
	val, err := mgr.Get([]byte("user:101"))
	if err != nil {
		log.Fatalf("Failed to get key: %v", err)
	}
	fmt.Printf("Retrieved Value: %s\n", string(val))

	// Delete Key
	err = mgr.Delete([]byte("user:101"))
	if err != nil {
		log.Fatalf("Failed to delete key: %v", err)
	}
}
```

## Running Tests & Benchmarks
Run standard unit tests:

```
go test -v ./lsmengine/...
```

Run benchmarks for parallel reads/writes and compare against external implementations:

```
go test -bench=. -benchmem ./lsmengine/
```
Results are captured and structured inside the lsmengine/testresults/ directory for historical comparison.

## Research & Documentation
Detailed analytical paper and storage layer comparisons are included in the repository:

paper.tex: Comprehensive LaTeX document detailing architecture design decisions, disk layout, and compaction algorithms.

Results.md: Execution logs, hardware specifications, and benchmark interpretations comparing write amplification, read latency, and throughput under heavy concurrent workloads.
