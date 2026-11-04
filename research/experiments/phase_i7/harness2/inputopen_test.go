package harness2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInputopenSpec(t *testing.T) {
	for _, block := range []string{"X1", "X2", "X3", "X4", "E1", "SMOKE"} {
		s := Spec{Block: block, CompactionOpenOutsideLock: true, WalSync: true,
			PublicationMode: "none", WriterConcurrency: 1, PacedWriteRatePerSec: 100}
		if _, err := s.ResolveMode(); (err == nil) != s.IsX() {
			t.Fatalf("%s: %v", block, err)
		}
	}
	for _, enabled := range []bool{false, true} {
		b, err := json.Marshal(Spec{CompactionOpenOutsideLock: enabled})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "compaction_open_outside_lock") != enabled {
			t.Fatal(string(b))
		}
	}
}
