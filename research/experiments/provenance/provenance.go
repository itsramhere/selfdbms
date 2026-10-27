package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Embedded via -ldflags during build if available:
var (
	EmbeddedGitCommit      string
	EmbeddedGitBranch      string
	EmbeddedSourceDirty    string
	EmbeddedSourceTreeHash string
	EmbeddedBuildTimestamp string
	EmbeddedProtocolVer    string
	EmbeddedCampaignID     string
)

// ProvenanceRecord captures comprehensive source, compiler, build, binary, and runtime provenance.
type ProvenanceRecord struct {
	SchemaVersion   string    `json:"schema_version"`
	ProtocolVersion string    `json:"protocol_version"`
	CampaignID      string    `json:"campaign_id"`
	GitCommit       string    `json:"git_commit"`
	GitBranch       string    `json:"git_branch"`
	SourceClean     bool      `json:"source_clean"`
	SourceTreeHash  string    `json:"source_tree_hash"`
	GoVersion       string    `json:"go_version"`
	Compiler        string    `json:"compiler"`
	Platform        string    `json:"platform"`
	Architecture    string    `json:"architecture"`
	BuildTimestamp  string    `json:"build_timestamp"`
	BinarySHA256    string    `json:"binary_sha256"`
	BinaryPath      string    `json:"binary_path"`
	ExecutionDir    string    `json:"execution_dir"`
	FilesystemType  string    `json:"filesystem_type"`
	ValidatedAt     time.Time `json:"validated_at"`
}

// ComputeFileSHA256 computes the hex-encoded SHA-256 of the given file path.
func ComputeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeSourceTreeHash computes a deterministic SHA-256 over all source files (.go, go.mod, go.sum).
func ComputeSourceTreeHash(rootDir string) (string, error) {
	var fileList []string

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "data" || name == "testresults" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		base := filepath.Base(path)
		if ext == ".go" || base == "go.mod" || base == "go.sum" {
			rel, rErr := filepath.Rel(rootDir, path)
			if rErr == nil {
				fileList = append(fileList, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk source tree: %w", err)
	}

	sort.Strings(fileList)

	hasher := sha256.New()
	for _, rel := range fileList {
		fullPath := filepath.Join(rootDir, filepath.FromSlash(rel))
		fHash, err := ComputeFileSHA256(fullPath)
		if err != nil {
			return "", fmt.Errorf("hash file %s: %w", rel, err)
		}
		fmt.Fprintf(hasher, "%s %s\n", rel, fHash)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// InspectFilesystemType attempts to identify the underlying filesystem type of a path.
func InspectFilesystemType(path string) string {
	if runtime.GOOS == "windows" {
		return "NTFS / ReFS (Windows)"
	}
	if strings.HasPrefix(path, "/mnt/c") || strings.HasPrefix(path, "/mnt/d") {
		return "9P_drvfs (Windows-backed WSL2 mount)"
	}
	// Check df output on Linux
	cmd := exec.Command("df", "-T", path)
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 2 {
				return fields[1] + " (Native Linux)"
			}
		}
	}
	return "ext4 (Linux default)"
}

// CaptureProvenance gathers source and environment metadata with strict validation.
// If requireClean is true and git is available, it enforces that the source tree has no uncommitted changes.
func CaptureProvenance(rootDir string, protocolVer, campaignID string, requireClean bool) (*ProvenanceRecord, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("get executable path: %w", err)
	}
	execPath, _ = filepath.Abs(execPath)

	binHash, err := ComputeFileSHA256(execPath)
	if err != nil {
		// If running under 'go test', executable might be temporary test binary
		binHash = "transient_test_binary"
	}

	execDir, _ := os.Getwd()
	fsType := InspectFilesystemType(execDir)

	gitCommit := EmbeddedGitCommit
	gitBranch := EmbeddedGitBranch
	isClean := EmbeddedSourceDirty != "true"
	treeHash := EmbeddedSourceTreeHash

	// If not embedded, attempt to query git directly
	if gitCommit == "" || gitCommit == "unknown_rev" {
		cmdRev := exec.Command("git", "rev-parse", "HEAD")
		cmdRev.Dir = rootDir
		if out, err := cmdRev.Output(); err == nil {
			gitCommit = strings.TrimSpace(string(out))
		}

		cmdBranch := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
		cmdBranch.Dir = rootDir
		if out, err := cmdBranch.Output(); err == nil {
			gitBranch = strings.TrimSpace(string(out))
		}

		cmdStatus := exec.Command("git", "status", "--porcelain")
		cmdStatus.Dir = rootDir
		if out, err := cmdStatus.Output(); err == nil {
			statusStr := strings.TrimSpace(string(out))
			isClean = (len(statusStr) == 0)
		}
	}

	if treeHash == "" {
		if th, err := ComputeSourceTreeHash(rootDir); err == nil {
			treeHash = th
		} else {
			treeHash = "unable_to_compute"
		}
	}

	buildTime := EmbeddedBuildTimestamp
	if buildTime == "" {
		buildTime = time.Now().UTC().Format(time.RFC3339)
	}

	if protocolVer == "" {
		protocolVer = "1.1.0"
	}
	if campaignID == "" {
		campaignID = "I6_CORRECTIVE_MECHANISMS_V1"
	}

	rec := &ProvenanceRecord{
		SchemaVersion:   "1.0.0",
		ProtocolVersion: protocolVer,
		CampaignID:      campaignID,
		GitCommit:       gitCommit,
		GitBranch:       gitBranch,
		SourceClean:     isClean,
		SourceTreeHash:  treeHash,
		GoVersion:       runtime.Version(),
		Compiler:        runtime.Compiler,
		Platform:        runtime.GOOS,
		Architecture:    runtime.GOARCH,
		BuildTimestamp:  buildTime,
		BinarySHA256:    binHash,
		BinaryPath:      execPath,
		ExecutionDir:    execDir,
		FilesystemType:  fsType,
		ValidatedAt:     time.Now().UTC(),
	}

	// Strict Fail-Closed Validation
	if rec.GitCommit == "" || rec.GitCommit == "unknown_rev" {
		return rec, errors.New("provenance validation failed: Git commit SHA could not be determined; 'unknown_rev' is prohibited")
	}
	if len(rec.GitCommit) < 40 {
		return rec, fmt.Errorf("provenance validation failed: Git commit SHA %q is invalid (expected 40 chars)", rec.GitCommit)
	}
	if requireClean && !rec.SourceClean {
		return rec, errors.New("provenance validation failed: source working tree has uncommitted modifications")
	}

	return rec, nil
}

// WriteManifest saves a ProvenanceRecord as JSON alongside the executable or at manifestPath.
func (p *ProvenanceRecord) WriteManifest(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal provenance manifest: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// LoadManifest reads a ProvenanceRecord from disk.
func LoadManifest(path string) (*ProvenanceRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read provenance manifest: %w", err)
	}
	var rec ProvenanceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal provenance manifest: %w", err)
	}
	return &rec, nil
}
