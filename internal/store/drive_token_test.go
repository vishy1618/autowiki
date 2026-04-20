package store_test

import (
	"testing"

	"github.com/suvish/autowiki/internal/store"
)

func TestMemStore_DriveToken_SetAndGet(t *testing.T) {
	// Arrange
	s := store.NewMemStore()

	// Act
	if err := s.SetDriveToken("refresh-tok-123"); err != nil {
		t.Fatalf("SetDriveToken: %v", err)
	}
	got, err := s.GetDriveToken()

	// Assert
	if err != nil {
		t.Fatalf("GetDriveToken: %v", err)
	}
	if got != "refresh-tok-123" {
		t.Errorf("GetDriveToken: want %q, got %q", "refresh-tok-123", got)
	}
}

func TestMemStore_DriveToken_GetWhenNotSet_ReturnsEmpty(t *testing.T) {
	// Arrange
	s := store.NewMemStore()

	// Act
	got, err := s.GetDriveToken()

	// Assert
	if err != nil {
		t.Fatalf("GetDriveToken: %v", err)
	}
	if got != "" {
		t.Errorf("GetDriveToken: want empty string, got %q", got)
	}
}
