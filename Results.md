# Comprehensive Performance & Empirical Benchmark Analysis: Custom LSM-Tree Storage Engine

**Engine Core:** Go 1.26.3 (`owndbms/lsmengine`)  
**Hardware Platform:** AMD Ryzen 7 7445HS (12 Logical Cores, 4.0GHz Base)  
**Operating Environment:** WSL2 Linux Kernel 5.15 on x86_64 Architecture  

---

## Executive Summary

This report documents the empirical evaluation of a custom zero-dependency Log-Structured Merge (LSM) tree storage engine. The benchmarks quantify the mechanical tradeoffs between volatile memory traversals, synchronous Write-Ahead Logging (WAL), sparse index resolution across physical SSTable tiers, and virtualized cross-OS filesystem translation bridges. Additionally, baseline performance metrics were established against `bbolt` (etcd's B+Tree implementation) to evaluate structural access patterns under identical workloads.

---

## I. Comprehensive Benchmark Results Matrix

| Benchmark Scenario | Engine & Model | Target Storage Medium | Concurrency Model | Iterations ($b.N$) | Latency / Op | Throughput / Efficiency | Allocation Size | Heap Allocs |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| **Sequential Writes** | Custom LSM Engine | WAL + RAM (`wal.log`) | Single-Threaded | 438 | **2,674,267 ns** (~2.67 ms) | ~373 ops/sec | 1,030 B/op | 12 allocs/op |
| **Parallel Writes** | Custom LSM Engine | WAL Mutex Contention | 12 Goroutines | 440 | **2,690,794 ns** (~2.69 ms) | ~371 ops/sec | 1,086 B/op | 12 allocs/op |
| **Parallel Reads (Hot)** | Custom LSM Engine | Memtable (SkipList) | 12 Goroutines | 5,677,010 | **194.9 ns** | ~5,130,000 ops/sec | 94 B/op | 3 allocs/op |
| **SSTable Reads (Ext4)** | Custom LSM Engine | Native POSIX `pread` | 12 Goroutines | 379,525 | **27,953 ns** (~0.027 ms) | ~35,770 ops/sec | 2,583 B/op | 179 allocs/op |
| **SSTable Reads (9P/NTFS)** | Custom LSM Engine | Mount Bridge (`/mnt/c/`) | 12 Goroutines | 135 | **8,332,979 ns** (~8.33 ms) | ~120 ops/sec | 2,071 B/op | 139 allocs/op |
| **BoltDB Writes** | `bbolt` (B+Tree) | In-Place Pages (`bolt.db`)| Single-Threaded | 2,346 | **5,654,512 ns** (~5.65 ms) | ~176 ops/sec | 20,797 B/op | 57 allocs/op |
| **BoltDB Reads** | `bbolt` (B+Tree) | Memory-Mapped (`mmap`) | 12 Goroutines | 10,143,279 | **1,083 ns** | ~923,300 ops/sec | 494 B/op | 9 allocs/op |

---

## II. In-Depth Operational Breakdown

### 1. Write Path Dynamics: LSM Engine vs. B+Tree
* **WAL Mutex Serialization:** In the custom LSM engine, parallel write transactions flatlined at **2.69 ms/op**, exhibiting virtually identical performance to single-threaded sequential writes (**2.67 ms/op**). This proves that under synchronous file persistence (`os.File.Sync()`), write operations do not scale with CPU core counts; they strictly serialize at the WAL Mutex tollbooth awaiting physical disk flush.
* **LSM vs. B+Tree In-Place Updating:** The custom LSM write path (**2.67 ms/op**) outperformed BoltDB's B+Tree write path (**5.65 ms/op**) by **~2.12x**. BoltDB incurs higher latency and severe allocation overhead (**20,797 B/op** vs **1,030 B/op**) due to dirty page copying, structural node balancing (page splits), and internal B+Tree page serialization during transaction commits.

### 2. Read Path Dynamics: Memory Speed vs. Storage Misses
* **Lock-Free Memtable Scalability:** Point queries hitting the active SkipList Memtable executed in **194.9 ns/op** with an extremely low allocation footprint (**94 B/op**). By utilizing atomic sequence readers and avoiding coarse-grained locks on node traversals, the engine achieved a **~13,800x speedup** over its disk write path.
* **Custom Engine vs. BoltDB `mmap` Reads:** The custom engine's in-memory SkipList (**194.9 ns**) proved **~5.5x faster** than BoltDB's `mmap` point-lookups (**1,083 ns**). While `mmap` avoids explicit system calls, BoltDB must still navigate page headers and key offsets within memory-mapped byte slices, whereas the custom SkipList directly routes pointers in RAM.

### 3. Storage Layer & Isolation of the WSL2 Virtualization Tax
* **The 9P/drvfs Translation Overhead:** Running physical disk reads on a Windows drive mount (`/mnt/c/`) yielded a disastrous **8,332,979 ns/op** (~8.33 ms). Moving the binary and storage volume to the native Linux filesystem root (`~/` on `ext4`) dropped the latency down to **27,953 ns/op** (~0.027 ms).
* **Quantifying the Virtualization Tax Factor:**
  $$\text{WSL Interop Tax Factor} = \frac{8,332,979\text{ ns}}{27,953\text{ ns}} \approx \mathbf{298.1\times}$$
  Cross-OS filesystem translation via the 9P protocol bridge injected a **~298x latency penalty**, proving that microsecond-level positional reads (`pread`) must strictly run on native kernel block devices.
* **True Cache Miss Penalty:** On native `ext4`, the physical cache miss penalty (transitioning from a RAM Memtable query to a disk index binary search + positional block read) is accurately measured as:
  $$\text{Cache Miss Penalty} = \frac{27,953\text{ ns}}{194.9\text{ ns}} \approx \mathbf{143.4\times}$$
  Reading a 4KB block from physical storage introduces a true **~143x latency degradation** compared to volatile memory traversal.

---

## III. Architectural Vulnerability & Concurrency Design Fix

### The Unlocked Slice Mutation Data Race
During stress testing, a critical race condition was identified between `Manager.Get()` and `Manager.CompactAll()`:

```go
// --- READ PATH: manager.go ---
func (m *Manager) Get(key string, readTs uint64) (string, error) {
    // UNPROTECTED READ: Iterating over m.ActiveSSreaders without acquiring m.Mu lock!
    for i := len(m.ActiveSSreaders) - 1; i >= 0; i-- {
        val, err := m.ActiveSSreaders[i].Get(byteKey, readTs)
        if err == nil { return string(val), nil }
    }
    return "", ErrNotFound
}

Go
// --- COMPACTION PATH: compaction.go ---
func (m *Manager) CompactAll() error {
    // MUTATION: Reallocating m.ActiveSSreaders under m.Mu lock
    m.Mu.Lock()
    m.ActiveSSreaders = []*SSTableReader{newReader}
    m.Mu.Unlock()

    // DESTRUCTION: Immediately closing old physical readers
    for _, r := range readersToCompact {
        r.Close()
        os.Remove(r.File.Name())
    }
    return nil
}
Risk Analysis
Slice Reallocation Race: If CompactAll() overwrites m.ActiveSSreaders while Get() is iterating mid-loop, Get() will evaluate an out-of-bounds slice offset or dereference a nil pointer, causing a process panic.

Use-After-Close I/O Error: If Get() obtains a pointer to an SSTableReader right before CompactAll() calls r.Close(), Get() will execute an io.SectionReader operation on a closed file descriptor, throwing a read failure.

Modern Resolution: Upgrading to sync.RWMutex
To allow concurrent lock-free reads while strictly serializing background compactions, Manager.Mu must be upgraded from sync.Mutex to sync.RWMutex:

Go
// 1. Acquire Read-Lock during point queries (Non-blocking across concurrent readers)
func (m *Manager) Get(key string, readTs uint64) (string, error) {
    m.Mu.RLock()
    defer m.Mu.RUnlock()
    
    // Safely traverse Memtable, Frozenmem, and ActiveSSreaders...
}

// 2. Acquire Write-Lock during compaction swaps (Blocks until active readers finish)
m.Mu.Lock()
oldReaders := m.ActiveSSreaders
m.ActiveSSreaders = []*SSTableReader{newReader}
m.Mu.Unlock()

// 3. Safely close physical descriptors after write-lock release
for _, r := range oldReaders {
    r.Close()
    os.Remove(r.File.Name())
}
IV. Hardware & Environmental Metadata
Processor: AMD Ryzen 7 7445HS w/ Radeon 740M Graphics (12 Threads)

Architecture: amd64 / x86_64

Go Compiler Version: go1.26.3 linux/amd64

Linux Kernel Version: 5.15.153.1-microsoft-standard-WSL2

Filesystem Types: ext4 (Root Virtual Disk) vs. drvfs/9p (/mnt/c/ Host Mount)