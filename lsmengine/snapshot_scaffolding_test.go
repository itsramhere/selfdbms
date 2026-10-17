package lsmengine

import (
	"errors"
	"testing"
)

func TestSnapshotState_String(t *testing.T) {
	tests := []struct {
		state    SnapshotState
		expected string
	}{
		{SnapshotConstructing, "Constructing"},
		{SnapshotPublished, "Published"},
		{SnapshotPinned, "Pinned"},
		{SnapshotRetired, "Retired"},
		{SnapshotDrained, "Drained"},
		{SnapshotClosed, "Closed"},
		{SnapshotReclaimed, "Reclaimed"},
		{SnapshotState(99), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("SnapshotState(%d).String() = %q, expected %q", int(tt.state), got, tt.expected)
		}
	}
}

func TestSharedReader_RefAndCount(t *testing.T) {
	sr := &SharedReader{
		Path: "test_sstable.sst",
	}
	if count := sr.RefCount(); count != 0 {
		t.Fatalf("expected initial RefCount to be 0, got %d", count)
	}

	sr.Ref()
	sr.Ref()
	if count := sr.RefCount(); count != 2 {
		t.Fatalf("expected RefCount to be 2, got %d", count)
	}
}

func TestApproachB_ScaffoldingEnforcesUnsupportedMode(t *testing.T) {
	cfg := Config{
		Mode: SyncModeApproachB,
	}
	man, err := CreateManagerWithConfig(cfg)
	if !errors.Is(err, ErrUnsupportedSyncMode) {
		t.Fatalf("expected ErrUnsupportedSyncMode, got %v", err)
	}
	if man != nil {
		t.Fatalf("expected nil manager on unsupported mode, got %v", man)
	}
}
