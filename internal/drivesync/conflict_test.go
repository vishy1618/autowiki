package drivesync_test

import (
	"testing"
	"time"

	"github.com/suvish/autowiki/internal/config"
	"github.com/suvish/autowiki/internal/drivesync"
)

func TestResolve_LastWriteWins_LocalNewer(t *testing.T) {
	// Arrange
	drive := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	local := drive.Add(10 * time.Second)

	// Act
	d := drivesync.Resolve(local, drive, config.ConflictLastWriteWins)

	// Assert
	if d.Winner != "local" {
		t.Errorf("want winner %q, got %q", "local", d.Winner)
	}
	if d.ConflictPath != "" {
		t.Errorf("want empty ConflictPath, got %q", d.ConflictPath)
	}
}

func TestResolve_KeepBoth_LargeGap_PicksLaterSideNoConflictPath(t *testing.T) {
	// Arrange — gap is 10 s (> 5 s threshold); Drive is newer.
	local := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	drive := local.Add(10 * time.Second)

	// Act
	d := drivesync.Resolve(local, drive, config.ConflictKeepBoth)

	// Assert
	if d.Winner != "drive" {
		t.Errorf("want winner %q, got %q", "drive", d.Winner)
	}
	if d.ConflictPath != "" {
		t.Errorf("want empty ConflictPath for large gap, got %q", d.ConflictPath)
	}
}

func TestResolve_KeepBoth_SmallGap_WinnerIsDriveAndConflictPathSet(t *testing.T) {
	// Arrange — gap is 3 s (≤ 5 s); local is older so its name becomes the conflict path.
	local := time.Date(2024, 6, 15, 9, 30, 45, 0, time.UTC)
	drive := local.Add(3 * time.Second)

	// Act
	d := drivesync.Resolve(local, drive, config.ConflictKeepBoth)

	// Assert
	if d.Winner != "drive" {
		t.Errorf("want winner %q, got %q", "drive", d.Winner)
	}
	// ConflictPath encodes the local modtime: notes.conflict.20240615093045.md
	// Resolve only knows the timestamp; the caller prefixes the stem and extension.
	// We test the timestamp segment that Resolve must produce.
	want := "conflict.20240615093045"
	if d.ConflictPath != want {
		t.Errorf("want ConflictPath %q, got %q", want, d.ConflictPath)
	}
}

func TestResolve_LastWriteWins_DriveNewer(t *testing.T) {
	// Arrange
	local := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	drive := local.Add(10 * time.Second)

	// Act
	d := drivesync.Resolve(local, drive, config.ConflictLastWriteWins)

	// Assert
	if d.Winner != "drive" {
		t.Errorf("want winner %q, got %q", "drive", d.Winner)
	}
	if d.ConflictPath != "" {
		t.Errorf("want empty ConflictPath, got %q", d.ConflictPath)
	}
}
