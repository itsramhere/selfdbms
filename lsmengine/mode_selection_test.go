package lsmengine

import (
	"errors"
	"os"
	"testing"
)

func TestSyncMode_DefaultIsApproachA(t *testing.T) {
	walPath := "test_sync_mode_default.wal"
	defer os.Remove(walPath)

	m := CreateManager()
	defer m.Close()

	if m.SyncMode() != SyncModeApproachA {
		t.Fatalf("expected default SyncMode to be SyncModeApproachA, got %v", m.SyncMode())
	}
	if m.SyncMode().String() != "ApproachA_RWMutexBaseline" {
		t.Fatalf("expected string representation 'ApproachA_RWMutexBaseline', got %q", m.SyncMode().String())
	}
}

func TestSyncMode_ApproachBReturnsUnsupportedError(t *testing.T) {
	cfg := Config{
		Mode:    SyncModeApproachB,
		WalPath: "test_sync_mode_b.wal",
	}

	m, err := CreateManagerWithConfig(cfg)
	if err == nil {
		if m != nil {
			m.Close()
			os.Remove(cfg.WalPath)
		}
		t.Fatalf("expected error when requesting SyncModeApproachB, got nil error")
	}
	if !errors.Is(err, ErrUnsupportedSyncMode) {
		t.Fatalf("expected ErrUnsupportedSyncMode, got %v", err)
	}
	if m != nil {
		t.Fatalf("expected nil manager when error is returned, got non-nil: %v", m)
	}
}

func TestSyncMode_UnknownModeReturnsError(t *testing.T) {
	cfg := Config{
		Mode:    SyncMode(999),
		WalPath: "test_sync_mode_unknown.wal",
	}

	m, err := CreateManagerWithConfig(cfg)
	if err == nil {
		if m != nil {
			m.Close()
			os.Remove(cfg.WalPath)
		}
		t.Fatalf("expected error when requesting unknown SyncMode, got nil error")
	}
	if m != nil {
		t.Fatalf("expected nil manager when error is returned, got non-nil: %v", m)
	}
}

func TestSyncMode_ExplicitApproachASucceeds(t *testing.T) {
	walPath := "test_sync_mode_explicit_a.wal"
	defer os.Remove(walPath)

	cfg := Config{
		Mode:    SyncModeApproachA,
		WalPath: walPath,
	}

	m, err := CreateManagerWithConfig(cfg)
	if err != nil {
		t.Fatalf("expected success creating manager with explicit SyncModeApproachA, got: %v", err)
	}
	defer m.Close()

	if m.SyncMode() != SyncModeApproachA {
		t.Fatalf("expected SyncMode to be SyncModeApproachA, got %v", m.SyncMode())
	}
}

func TestSyncMode_StringRepresentation(t *testing.T) {
	tests := []struct {
		mode     SyncMode
		expected string
	}{
		{SyncModeApproachA, "ApproachA_RWMutexBaseline"},
		{SyncModeApproachB, "ApproachB_ReaderSnapshot"},
		{SyncMode(-1), "Unknown"},
		{SyncMode(100), "Unknown"},
	}

	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.expected {
			t.Errorf("SyncMode(%d).String() = %q, expected %q", int(tt.mode), got, tt.expected)
		}
	}
}
