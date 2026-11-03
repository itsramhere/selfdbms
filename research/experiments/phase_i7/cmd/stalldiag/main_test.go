package main

import (
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefuseCampaignOutput(t *testing.T) {
	for _, path := range []string{
		"X_READCOST/paced/trials/diag.json", "X_READCOST/paced/trials/nested/diag.json",
		"x_readcost/paced/TRIALS/diag.json", "X_READCOST/paced/../paced/trials/diag.json",
	} {
		out := filepath.Join(t.TempDir(), filepath.FromSlash(path))
		if err := run([]string{"-spec", "missing.json", "-out", out}, io.Discard); err == nil || !strings.Contains(err.Error(), "X_READCOST/*/trials") {
			t.Fatalf("did not refuse output %s first: %v", path, err)
		}
	}
	if err := checkOutput(filepath.Join(t.TempDir(), "diagnostics", "diag.json")); err != nil {
		t.Fatal(err)
	}
}
