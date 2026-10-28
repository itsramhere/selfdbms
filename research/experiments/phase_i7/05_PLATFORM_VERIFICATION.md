# 05 — Hardware / Storage Verification Plan (No Partition Modification)

> ## SUPERSEDED — LINUX PLAN, ARCHIVED
> **This document is no longer the I7 platform plan.** The operator has no bare-metal Linux host; I7 runs natively on Windows 11 / NTFS. The governing plan is [12_WINDOWS_EXECUTION_AMENDMENT.md](12_WINDOWS_EXECUTION_AMENDMENT.md), and the executed checks are recorded in [13_G1_REVIEW_EVIDENCE.md](13_G1_REVIEW_EVIDENCE.md) §6.
>
> **Retained for:** the prohibition list (§1), the "no partition modification" rule, the reproducible-build requirements (§6), and the general principle that a platform is accepted only if every check passes.
>
> **Replaced by Windows-native equivalents:**
>
> | Here (Linux) | Windows implementation |
> | :--- | :--- |
> | `lsblk`, `findmnt`, `stat -f`, `df -hT` | `Get-Volume`, `Get-Partition`, `Get-Disk`, `Get-PhysicalDisk`, `[IO.DriveInfo]` |
> | `nvme list`, `nvme id-ctrl`, `nvme smart-log` | `Get-PhysicalDisk` (`BusType`, `MediaType`, `SerialNumber`, `FriendlyName`) |
> | `/sys/block/*/queue/*`, `/proc/interrupts` | not exposed to unprivileged processes; recorded as "not available" |
> | `fio` with `--direct=1` | bounded 32 MiB write + `FileStream.Flush(true)`, timed, deleted immediately |
> | `posix_fadvise(DONTNEED)` cold condition | **unavailable**; OD-6 withdrawn, all trials warm-cache |
> | `taskset` + `GOMAXPROCS` | `Process.ProcessorAffinity` (inherited by children) + `GOMAXPROCS = popcount(mask)` |
> | `uname -a`, `/etc/os-release` | `Get-CimInstance Win32_OperatingSystem`, `Win32_ComputerSystem`, `Win32_Processor` |
> | `git status --porcelain`, double build | unchanged; `run_i7.ps1 -Check` records both |
>
> The command list below is therefore **reference material for a future Linux replication**, not an I7 requirement.

**Status:** ARCHIVED (superseded). The Linux commands below were never executed for I7.
**Hard rule (still in force):** I7 **must not** create, delete, resize, format, mount, or remount any partition, filesystem, or block device. Every measurement is either read-only metadata or a bounded write **inside a directory that I7 itself creates** on an already-mounted filesystem.

---

## 1. Prohibited Actions (hard fail if any occurs)

| Prohibited | Why |
| :--- | :--- |
| `mkfs.*`, `mke2fs`, `wipefs` | destroys data |
| `parted`, `fdisk`, `gdisk`, `sgdisk`, `blkdiscard` | partition/device mutation |
| `mount`, `umount`, `mount -o remount` | changes system state; may affect other users |
| `fstrim`, `nvme format`, `nvme sanitize`, `nvme fw-download` | device-level mutation |
| `echo 3 > /proc/sys/vm/drop_caches` | global side effect; requires root; invalidates other users' caches |
| `sysctl -w …`, `/sys/...` writes (governor, THP, dirty ratios, scheduler) | system modification; record instead |
| `isolcpus`, `nohz_full`, `intel_idle.max_cstate` boot params | reboot-level change |
| Interrupt-affinity rewriting (`/proc/irq/*/smp_affinity`) | system modification |

Everything in §2–§7 is read-only. The only writes are in §3.2 (a 1 GiB test file in a directory created for the purpose, deleted afterwards) and the campaign's own data directory.

---

## 2. Read-Only Platform Discovery

Capture to `platform_manifest.json`; every command's raw output is stored alongside it.

### 2.1 OS and kernel

```bash
uname -a
cat /etc/os-release
getconf PAGESIZE
cat /sys/kernel/mm/transparent_hugepage/enabled
cat /sys/kernel/mm/transparent_hugepage/defrag
cat /proc/sys/vm/dirty_ratio /proc/sys/vm/dirty_background_ratio \
    /proc/sys/vm/dirty_expire_centisecs /proc/sys/vm/dirty_writeback_centisecs
cat /sys/devices/system/clocksource/clocksource0/current_clocksource
```

**Accept:** clocksource is `tsc` or `kvm-clock` with a stable rating; THP setting recorded (never changed); `dirty_ratio` recorded (`≤ 20` desirable — large dirty ratios can absorb `fsync` cost and make WAL durability measurements meaningless).

**Record-only, never change:** THP, dirty ratios, clocksource.

### 2.2 CPU topology and frequency

```bash
lscpu
lscpu -e=CPU,CORE,SOCKET,NODE,ONLINE,MAXMHZ,MINMHZ,MHZ
for c in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do cat "$c"; done | sort | uniq -c
cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_driver
cat /proc/cpuinfo | grep -m1 'model name'
```

**Accept:** ≥ 8 logical CPUs, ≥ 4 physical cores, homogeneous topology (one socket preferred), `scaling_driver` recorded. **SMT is recorded, not disabled** — the frozen campaign ran with SMT enabled and I7 keeps the comparison to itself.

**Governor:** recorded per CPU. If it is not `performance`, the study still runs; the governor is a recorded covariate and a Threats-to-Validity item. *Changing it requires explicit sysadmin action outside this protocol; I7 must not mutate `/sys`.*

### 2.3 Block device and filesystem identity

```bash
lsblk -o NAME,MAJ:MIN,SIZE,ROTA,TYPE,MODEL,SERIAL,MOUNTPOINTS,FSTYPE
findmnt -T /data -o SOURCE,TARGET,FSTYPE,OPTIONS
findmnt -no SOURCE,FSTYPE,OPTIONS /data
stat -f -c '%T %S %b %f' /data
df -hT /data
```

**Accept:**
- `findmnt -T /data` resolves to a **real block device** (`/dev/nvmeXnY`), not `overlay`, `fuse.*`, `9p`, `nfs`, `tmpfs`, `drvfs`;
- `FSTYPE` ∈ {`ext4`, `xfs`};
- the mount is not on the same device as `/` if a dedicated data volume exists (preferred; not mandatory);
- `ROTA=0` for the data device.

**Record:** mount options (`noatime`, `discard`, `data=`) — these are covariates; do **not** remount to change them.

### 2.4 NVMe identity and health

```bash
nvme list
nvme id-ctrl /dev/nvme0 -H
nvme id-ns /dev/nvme0n1
nvme smart-log /dev/nvme0
```

**Accept:**
- device model/serial/firmware recorded;
- `media_errors = 0`, `num_err_log_entries` recorded before and after the campaign;
- `percentage_used` recorded (wear);
- **no** `nvme format`/`sanitize` even if the device is "fresh".

**Record before/after:** SMART log, `/sys/block/nvme0n1/stat` (sectors read/written, I/Os), so total campaign I/O can be audited.

### 2.5 Storage queue configuration

```bash
for f in scheduler nr_requests rotational physical_block_size logical_block_size \
         max_sectors_kb read_ahead_kb nomerges; do
  printf '%s=' "$f"; cat "/sys/block/nvme0n1/queue/$f" 2>/dev/null || echo n/a
done
cat /proc/interrupts | grep -i nvme
ls /sys/block/nvme0n1/mq 2>/dev/null | head
```

**Record:** I/O scheduler (`none`/`mq-deadline`/`kyber`), `nr_requests`, `read_ahead_kb`, IRQ distribution, number of hardware queues.
**Never modify any of these.**

---

## 3. Storage Behaviour Verification (bounded, self-cleaning)

### 3.1 Target directory identity

```bash
mkdir -p /data/selfdbms_i7_verify        # the only directory I7 creates outside its results
dev_major_minor=$(stat -c '%d' /data/selfdbms_i7_verify)
# must equal the MAJ:MIN of the intended NVMe partition from lsblk
```

**Gate:** if the directory's device does not match the intended NVMe device, **stop** (the campaign would silently measure the wrong storage).

### 3.2 Throughput and fsync latency (fio, 1 GiB file, direct I/O)

```bash
cd /data/selfdbms_i7_verify
fio --name=w1 --filename=fio.dat --size=1G --bs=128k --rw=write --ioengine=libaio \
    --iodepth=8 --direct=1 --runtime=30 --time_based --output-format=json --output=w1.json
fio --name=r1 --filename=fio.dat --size=1G --bs=4k --rw=randread --ioengine=libaio \
    --iodepth=32 --direct=1 --runtime=30 --time_based --output-format=json --output=r1.json
fio --name=f1 --filename=fsync.dat --size=64M --bs=4k --rw=write --ioengine=libaio \
    --iodepth=1 --fsync=1 --runtime=20 --time_based --output-format=json --output=f1.json
rm -f fio.dat fsync.dat
rmdir /data/selfdbms_i7_verify
```

**Interpretation thresholds (informative, not pass/fail):**

| Metric | Expected on a modern NVMe | Action if not met |
| :--- | :--- | :--- |
| 4 KiB random read, QD32 | ≥ 100 k IOPS | record; if < 30 k, flag the device as unsuitable and report |
| 128 KiB sequential write | ≥ 500 MB/s | record |
| `fsync` p99 (single outstanding, 4 KiB) | ≤ 1 ms | **critical**: if p99 > 5 ms, the WAL path will dominate every trial; flag prominently |
| write latency p99 | ≤ 2 ms | record |

The measured fsync p99 predicts the fixture cost ([06](06_TRIAL_INVENTORY.md) §6) and the WAL convoy magnitude; it is a required input to the cost gate, not a mere formality.

### 3.3 Direct-vs-buffered sanity check

`fio --direct=1` above proves O_DIRECT works. The campaign itself does **not** use O_DIRECT (the engine uses buffered `pread`/`write`), so record the buffered path too:

```bash
dd if=/data/selfdbms_i7_verify/dd.dat of=/dev/null bs=1M count=512 iflag=direct status=none  # after creating it
```

### 3.4 SMART and I/O counters after verification

Re-run §2.4 and record deltas. Any `media_errors` increase aborts the campaign.

---

## 4. CPU Affinity and Scheduling Control

I7 does **not** re-partition cores or change boot parameters. It controls affinity only for its own processes.

| Control | Implementation | Verification |
| :--- | :--- | :--- |
| Process affinity | `taskset -c <mask> ./i7_runner` (mask = all physical cores, SMT siblings optional) | `sched_getaffinity` recorded in the manifest; `taskset -pc $$` inside the runner |
| `GOMAXPROCS` | set explicitly to the physical-core count (or to `len(mask)` if SMT is included) | `runtime.GOMAXPROCS(0)` recorded |
| Runner thread count | 1 process; no per-worker pinning (`runtime.LockOSThread` avoided — it would change scheduling vs the frozen campaign) | code audit |
| Background load | record `uptime`, `top -bn1` (top 10 by CPU), and number of logged-in sessions before the campaign; require idle | manifest |
| Frequency drift | record `MHZ` per CPU at start/end of each block; drift > 15 % on the reference triplet triggers the stopping rule ([07](07_ANALYSIS_PLAN.md) §8) | drift triplets D3 |
| IRQ affinity | **recorded only** | `/proc/interrupts` snapshot |

**Do not** use `isolcpus`, `nohz_full`, `cset`, cgroups v2 `cpuset`, or `chrt` — each is either a system modification or changes scheduling relative to the archived campaign. They are recorded as "not used" so a reviewer cannot mistake the environment for an isolated-core setup.

---

## 5. Page-Cache Condition (making "disk" true without touching the global cache)

The frozen workloads touch ≈ 1.5–6 MB, which is resident in page cache after any warmup. Under those conditions the "disk tier" is memory (see AF-1 in [00](00_README.md), [03](03_PACING_AUDIT.md) §3). I7 therefore measures **two cache conditions** and reports them separately:

| Condition | Mechanism | Cost |
| :--- | :--- | :--- |
| **Warm** (default, comparable to frozen campaign) | fixture built in-process, then 2 s warmup | none |
| **Cold-per-trial** (diagnostic) | after fixture build, call `unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)` on every SSTable, then run | one syscall per table |

`POSIX_FADV_DONTNEED` evicts clean pages **for that file only**, requires no privilege, and does not disturb other processes. It is a per-file hint, not a global cache drop.

**Verification that eviction actually happened** (needed, because `DONTNEED` on a mapped/busy page can be a no-op):

1. read `/proc/self/io` before and after the window; `read_bytes` (bytes actually fetched from storage) should increase by ≈ the SSTable size when cold, and by ≈ 0 when warm;
2. compare median read latency cold vs warm for the same cell; a cold run dominated by NVMe latency should be visibly different if the data are large enough.

**Scope decision (OD-6):** the cold condition is a **diagnostic on E2 only** — it is not a separate block and adds no trials. If `read_bytes` does not increase, the cold condition is declared ineffective and dropped, with the reason recorded.

**Why not `O_DIRECT`:** the engine uses buffered `io.SectionReader` over `*os.File` with unaligned lengths; O_DIRECT would require an engine change and would alter the cost model. Not in scope.

---

## 6. Reproducible Builds

```bash
# 1. clean tree
git status --porcelain            # must be empty
git rev-parse HEAD                # recorded

# 2. build twice, from two clean clones, compare
CGO_ENABLED=0 GOFLAGS=-mod=readonly \
  go build -trimpath -buildvcs=true -o /tmp/i7_runner_a ./research/experiments/phase_i7/cmd/runner
CGO_ENABLED=0 GOFLAGS=-mod=readonly \
  go build -trimpath -buildvcs=true -o /tmp/i7_runner_b ./research/experiments/phase_i7/cmd/runner
sha256sum /tmp/i7_runner_a /tmp/i7_runner_b     # must be equal
file /tmp/i7_runner_a                            # statically linked, x86-64
go version
go env GOAMD64 GOTOOLCHAIN GOVERSION
```

**Recorded with every trial:** git commit, branch, clean/dirty, source-tree hash over all `.go`+`go.mod`, binary SHA-256, Go version, `GOAMD64`, `CGO_ENABLED`, build flags, platform, filesystem type (the existing `provenance` package already captures most of this and rejects `unknown_rev`).

**Additional gate:** the runner refuses to start unless (a) `git status --porcelain` is empty, (b) the source-tree hash matches the one committed in the campaign manifest, and (c) the binary SHA-256 matches the recorded value.

---

## 7. Environment Manifest (replacing hardcoded values)

The corrective runner currently hardcodes platform facts:

```go
// research/experiments/corrective_validation/cmd/main.go:17-31
CPUModel: "AMD Ryzen 7 7445HS w/ Radeon 740M Graphics",
PhysicalCores: 6,
TotalRAMBytes: 16 * 1024 * 1024 * 1024,
```

I7 requires these to be **parsed and verified**, not asserted (H-13). Minimum fields:

| Field | Source |
| :--- | :--- |
| `os_release`, `kernel`, `arch` | `/etc/os-release`, `uname -r`, `runtime.GOARCH` |
| `cpu_model`, `physical_cores`, `logical_cores`, `smt`, `numa_nodes` | `/proc/cpuinfo`, `/sys/devices/system/cpu/*/topology/*` |
| `cpufreq_governor`, `cpufreq_driver`, `mhz_at_start`, `mhz_at_end` | `/sys/devices/system/cpu/cpu*/cpufreq/*` |
| `mem_total`, `mem_available`, `swap_total` | `/proc/meminfo` |
| `block_device`, `device_model`, `device_serial`, `device_firmware` | `lsblk -J`, `nvme id-ctrl -o json` |
| `fs_type`, `mount_options`, `fs_block_size` | `findmnt -J`, `statfs` |
| `io_scheduler`, `nr_requests`, `read_ahead_kb` | `/sys/block/*/queue/*` |
| `clocksource`, `thp_enabled`, `dirty_ratio` | `/sys`, `/proc/sys/vm` |
| `affinity_mask`, `gomaxprocs`, `go_version`, `goamd64` | runtime + `sched_getaffinity` |
| `fio_fsync_p99_us`, `fio_randread_iops`, `fio_seqwrite_mbps` | §3.2 |

Any missing field is a hard failure (`provenance: incomplete platform manifest`), matching the existing fail-closed provenance policy.

---

## 8. Platform Acceptance Gate (G2)

The campaign may start only if **all** hold:

| # | Gate |
| :--- | :--- |
| P-1 | Data directory resolves to the intended NVMe block device; filesystem ∈ {ext4, xfs}; not a network/FUSE/overlay/9p mount |
| P-2 | `media_errors` did not increase during verification; SMART log recorded |
| P-3 | `fio` fsync p99 ≤ 5 ms (else flag; if > 20 ms, treat as unsuitable and stop) |
| P-4 | 4 KiB random read ≥ 30 k IOPS |
| P-5 | clocksource stable; `dirty_ratio` recorded |
| P-6 | CPU topology recorded; affinity mask and `GOMAXPROCS` recorded and explicitly set |
| P-7 | Host idle; background CPU < 5 % of one core over a 60 s window (recorded) |
| P-8 | Double-build SHA-256 equality; clean git tree; provenance passes |
| P-9 | Page-cache condition verified (`read_bytes` delta) for whichever condition is chosen |
| P-10 | `platform_manifest.json` complete; no `null` required fields |

**On any failure:** stop before trial 1, publish the platform report, and do not silently downgrade. A downgraded platform (e.g. VM instead of bare metal) is a *different* study and requires a new authorization.
