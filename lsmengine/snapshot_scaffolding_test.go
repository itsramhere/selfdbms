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

func TestSnapshotShutdownPhase_String(t *testing.T) {
	tests := []struct {
		phase    SnapshotShutdownPhase
		expected string
	}{
		{SnapshotPhaseOpen, "Open"},
		{SnapshotPhaseDraining, "Draining"},
		{SnapshotPhaseClosed, "Closed"},
		{SnapshotShutdownPhase(99), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.phase.String(); got != tt.expected {
			t.Errorf("SnapshotShutdownPhase(%d).String() = %q, expected %q", int(tt.phase), got, tt.expected)
		}
	}
}

func TestSharedReaderState_String(t *testing.T) {
	tests := []struct {
		state    SharedReaderState
		expected string
	}{
		{SharedReaderUnpublished, "Unpublished"},
		{SharedReaderActive, "Active"},
		{SharedReaderObsolete, "Obsolete"},
		{SharedReaderClosing, "Closing"},
		{SharedReaderClosed, "Closed"},
		{SharedReaderUnlinking, "Unlinking"},
		{SharedReaderReclaimed, "Reclaimed"},
		{SharedReaderClosedValid, "ClosedValid"},
		{SharedReaderFailed, "Failed"},
		{SharedReaderState(99), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("SharedReaderState(%d).String() = %q, expected %q", int(tt.state), got, tt.expected)
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
	if sr.State() != SharedReaderUnpublished {
		t.Fatalf("expected initial state SharedReaderUnpublished, got %v", sr.State())
	}

	sr.Ref()
	sr.Ref()
	if count := sr.RefCount(); count != 2 {
		t.Fatalf("expected RefCount to be 2, got %d", count)
	}

	sr.SetState(SharedReaderActive)
	if sr.State() != SharedReaderActive {
		t.Fatalf("expected state SharedReaderActive, got %v", sr.State())
	}
}

func TestReaderSnapshot_AtomicAccessors(t *testing.T) {
	snap := &ReaderSnapshot{}
	if snap.State() != SnapshotConstructing {
		t.Fatalf("expected initial state SnapshotConstructing, got %v", snap.State())
	}
	if snap.Pins() != 0 {
		t.Fatalf("expected initial pins 0, got %d", snap.Pins())
	}

	snap.SetState(SnapshotPublished)
	if snap.State() != SnapshotPublished {
		t.Fatalf("expected state SnapshotPublished, got %v", snap.State())
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
