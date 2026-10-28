# Phase I6.10: Rebuild Verification and Cryptographic Build Reproducibility Report

- **Date**: 2026-10-28
- **Auditor**: External Reviewer Forensic Protocol (Phase I6.10)
- **Target Executable**: `/home/ramst/selfdbms/targeted_runner`
- **Compiler**: Go 1.27.3 (`go version go1.27.3 linux/amd64`)
- **Platform**: WSL2 Ubuntu 24.04.3 LTS on native Linux ext4 block device (`/dev/sdd`)
- **Reconstruction Status**: **VERIFIED RECONSTRUCTED BINARY MATCH (BYTE-FOR-BYTE)**

---

## 1. Preserved Historical Binary Artifact

The preserved historical executable used during the Phase I6.8 600-trial targeted validation campaign was forensically fingerprinted:

```bash
$ sha256sum /home/ramst/selfdbms/targeted_runner
2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f  /home/ramst/selfdbms/targeted_runner

$ ls -l /home/ramst/selfdbms/targeted_runner
-rwxr-xr-x 1 ramst ramst 5453058 Oct 27 06:51 /home/ramst/selfdbms/targeted_runner

$ go tool buildid /home/ramst/selfdbms/targeted_runner
raCgnlKhcejFVErxy8Ge/pSHSnyg97yOboCajV8nD/xtHS_TCzXh93CP88li-A/ax9WzPJrX-5xwTVSUvpn

$ readelf -n /home/ramst/selfdbms/targeted_runner
Displaying notes found in: .note.gnu.build-id
  Owner                Data size 	Description
  GNU                  0x00000014	NT_GNU_BUILD_ID (unique build ID bitstring)
    Build ID: ea953cf6bc0025fd4e351027a7190a8ca9e48772
```

---

## 2. Rebuild Experiment 1: Native Tree Direct Reconstruction

### 2.1 Build Command & Environment
Executed within the native ext4 workspace `/home/ramst/selfdbms` targeting an isolated destination path:

```bash
cd /home/ramst/selfdbms
go build -o /tmp/targeted_runner_rebuilt ./research/experiments/targeted_validation/cmd
```

Environment settings active during build:
- `GOOS=linux`
- `GOARCH=amd64`
- `CGO_ENABLED=1` (standard Go default; no cgo files present)
- `GOAMD64=v1`
- `GOPATH=/home/ramst/go`
- `GOROOT=/usr/local/go` (Go 1.27.3)

### 2.2 Comparison Against Preserved Historical Binary

| Artifact Property | Historical Executable | Reconstructed Executable (`/tmp/targeted_runner_rebuilt`) | Exact Match? |
| :--- | :--- | :--- | :---: |
| **File Path** | `/home/ramst/selfdbms/targeted_runner` | `/tmp/targeted_runner_rebuilt` | N/A (separate path) |
| **SHA-256 Checksum** | `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f` | `2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f` | **YES (Exact)** |
| **File Size (Bytes)** | `5,453,058` | `5,453,058` | **YES (Exact)** |
| **Go Build ID** | `raCgnlKhcejFVErxy8Ge/pSHSnyg97yOboCajV8nD/xtHS_TCzXh93CP88li-A/ax9WzPJrX-5xwTVSUvpn` | `raCgnlKhcejFVErxy8Ge/pSHSnyg97yOboCajV8nD/xtHS_TCzXh93CP88li-A/ax9WzPJrX-5xwTVSUvpn` | **YES (Exact)** |
| **GNU Build ID** | `ea953cf6bc0025fd4e351027a7190a8ca9e48772` | `ea953cf6bc0025fd4e351027a7190a8ca9e48772` | **YES (Exact)** |
| **Entry Point** | `0x48ed60` | `0x48ed60` | **YES (Exact)** |
| **Section Count** | 26 | 26 | **YES (Exact)** |
| **Program Headers** | 6 | 6 | **YES (Exact)** |

`cmp /home/ramst/selfdbms/targeted_runner /tmp/targeted_runner_rebuilt` exits with code 0 (zero byte differences across all 5,453,058 bytes).

---

## 3. Rebuild Experiment 2: Sensitivity to Alternate Source Paths and Line Endings

To test reproducibility from clean Git checkouts and understand compiler sensitivities, two controlled experiments were performed.

### 3.1 Sensitivity to Clean Git Checkout Line Endings
When Git commit `370fd6c` was exported via `git archive` into `/home/ramst/checkouts/370fd6c`, Git's export wrote all files with LF (`\n`) line endings.
Compiling this raw tree produced:
- **Output SHA-256**: `f23409e0bcaff7323e806991d79f9c07e32ab5f5b44fb0c3a236f1b1579ac516`
- **Root Cause**: In `/home/ramst/selfdbms`, 11 core LSM files had CRLF line endings. The Go compiler hashes package source files at the byte level when computing action IDs. The newline difference changed the package action ID for `owndbms/lsmengine`, which cascaded into the final binary hash.

### 3.2 Sensitivity to Absolute Source Path
When the 11 files were given matching CRLF line endings and compiled from an alternate path `/home/ramst/reconstruction_test`:
- **Output SHA-256**: `a6e045bf3726ec200ba1983a9fb6e56fb1dc91976a72d1a55baae808fbfe926e`
- **Root Cause**: Go compiles with full source paths embedded in DWARF debug symbols and pclntab unless `-trimpath` is specified. Inspection of the historical binary via `strings` confirmed embedded path strings:
  ```
  /home/ramst/selfdbms/lsmengine/compaction.go
  /home/ramst/selfdbms/lsmengine/sstableReader.go
  /home/ramst/selfdbms/lsmengine/snapshot.go
  ```
  The presence of these strings demonstrates conclusively that:
  1. The historical binary was compiled without `-trimpath`.
  2. The historical binary was compiled specifically from the path `/home/ramst/selfdbms`.

---

## 4. External Reviewer Step-by-Step Verification Protocol

An external reviewer can independently verify the reconstructed binary match using the following commands inside WSL2:

```bash
# 1. Verify Go Compiler Version
go version
# Expected: go version go1.27.3 linux/amd64

# 2. Check Preserved Historical Executable Hash
sha256sum /home/ramst/selfdbms/targeted_runner
# Expected: 2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f

# 3. Check Preserved Go Build ID
go tool buildid /home/ramst/selfdbms/targeted_runner
# Expected: raCgnlKhcejFVErxy8Ge/pSHSnyg97yOboCajV8nD/xtHS_TCzXh93CP88li-A/ax9WzPJrX-5xwTVSUvpn

# 4. Perform Isolated Rebuild to /tmp
cd /home/ramst/selfdbms
go build -o /tmp/targeted_runner_verify ./research/experiments/targeted_validation/cmd

# 5. Compare Bit-for-Bit
cmp /home/ramst/selfdbms/targeted_runner /tmp/targeted_runner_verify
# Expected: silent exit (code 0)

# 6. Verify Checksum of Rebuild
sha256sum /tmp/targeted_runner_verify
# Expected: 2855b4318d5c574a2d0c3fbf5eb773f3ae7aebd1d0469961db3a9b8e6399832f
```

---

## 5. Summary Conclusion

The reconstruction is **successful, complete, and byte-for-byte exact**. The historical binary `/home/ramst/selfdbms/targeted_runner` is proven to have been compiled directly from the Go source files currently residing in `/home/ramst/selfdbms`, which are 100% content-identical to Git commit `370fd6c` (and subsequent documentation/data commits `81429fc` and `4a06217`).
