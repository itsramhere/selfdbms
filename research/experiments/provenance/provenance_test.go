package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvenanceValidation_RejectsUnknownRev(t *testing.T) {
	// Temporarily override embedded variable
	origCommit := EmbeddedGitCommit
	defer func() { EmbeddedGitCommit = origCommit }()

	EmbeddedGitCommit = "unknown_rev"
	tmpDir := t.TempDir()

	_, err := CaptureProvenance(tmpDir, "1.1.0", "TEST_CAMPAIGN", false)
	if err == nil {
		t.Fatalf("expected error for 'unknown_rev', got nil")
	}
	if !strings.Contains(err.Error(), "unknown_rev") {
		t.Errorf("expected error mentioning 'unknown_rev', got: %v", err)
	}
}

func TestProvenanceValidation_RejectsEmptyCommit(t *testing.T) {
	origCommit := EmbeddedGitCommit
	defer func() { EmbeddedGitCommit = origCommit }()

	EmbeddedGitCommit = ""
	tmpDir := t.TempDir()

	_, err := CaptureProvenance(tmpDir, "1.1.0", "TEST_CAMPAIGN", false)
	if err == nil {
		t.Fatalf("expected error for empty commit, got nil")
	}
}

func TestProvenanceValidation_AcceptsValidCommit(t *testing.T) {
	origCommit := EmbeddedGitCommit
	defer func() { EmbeddedGitCommit = origCommit }()

	validCommit := "4a062174bb617a649cc3a2b03a83ee0e1e38ee45"
	EmbeddedGitCommit = validCommit
	tmpDir := t.TempDir()

	rec, err := CaptureProvenance(tmpDir, "1.1.0", "TEST_CAMPAIGN", false)
	if err != nil {
		t.Fatalf("expected success with valid 40-char commit, got: %v", err)
	}
	if rec.GitCommit != validCommit {
		t.Errorf("expected commit %s, got %s", validCommit, rec.GitCommit)
	}
	if rec.ProtocolVersion != "1.1.0" {
		t.Errorf("expected protocol version 1.1.0, got %s", rec.ProtocolVersion)
	}
	if rec.CampaignID != "TEST_CAMPAIGN" {
		t.Errorf("expected campaign ID TEST_CAMPAIGN, got %s", rec.CampaignID)
	}
	if rec.GoVersion == "" {
		t.Errorf("expected non-empty GoVersion")
	}
}

func TestDeterministicSourceTreeHash(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.go")
	f2 := filepath.Join(dir, "b.go")
	f3 := filepath.Join(dir, "sub", "c.go")
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0755)

	_ = os.WriteFile(f1, []byte("package test\n"), 0644)
	_ = os.WriteFile(f2, []byte("package test\nfunc Foo() {}\n"), 0644)
	_ = os.WriteFile(f3, []byte("package sub\n"), 0644)

	h1, err := ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}

	h2, err := ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("hash failed 2nd time: %v", err)
	}

	if h1 != h2 {
		t.Fatalf("expected identical hash, got %s vs %s", h1, h2)
	}

	// Mutate one file and verify hash changes
	_ = os.WriteFile(f1, []byte("package test\n// modification\n"), 0644)
	h3, _ := ComputeSourceTreeHash(dir)
	if h1 == h3 {
		t.Fatalf("expected hash to change after file mutation")
	}
}

func TestManifestRoundtrip(t *testing.T) {
	origCommit := EmbeddedGitCommit
	defer func() { EmbeddedGitCommit = origCommit }()
	EmbeddedGitCommit = "4a062174bb617a649cc3a2b03a83ee0e1e38ee45"

	tmpDir := t.TempDir()
	rec, err := CaptureProvenance(tmpDir, "1.1.0", "TEST_CAMPAIGN", false)
	if err != nil {
		t.Fatalf("failed capture: %v", err)
	}

	manifestPath := filepath.Join(tmpDir, "manifest.json")
	if err := rec.WriteManifest(manifestPath); err != nil {
		t.Fatalf("write manifest failed: %v", err)
	}

	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("load manifest failed: %v", err)
	}

	if loaded.GitCommit != rec.GitCommit {
		t.Errorf("commit mismatch: %s vs %s", loaded.GitCommit, rec.GitCommit)
	}
	if loaded.BinarySHA256 != rec.BinarySHA256 {
		t.Errorf("binary hash mismatch: %s vs %s", loaded.BinarySHA256, rec.BinarySHA256)
	}
}
