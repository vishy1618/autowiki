package drivesync

import (
	"time"

	"github.com/suvish/autowiki/internal/config"
)

// ConflictDecision is the outcome of Resolve.
// Winner is "local" or "drive". ConflictPath is non-empty only when the caller
// must rename the local file before writing (keep_both within 5 s window).
type ConflictDecision struct {
	Winner       string
	ConflictPath string
}

const conflictWindow = 5 * time.Second

// Resolve decides which side wins when both local and Drive have a version of
// the same file. It is a pure function — no filesystem I/O.
//
// For keep_both within the conflict window, ConflictPath is the timestamp token
// "conflict.<YYYYMMDDHHMMSS>" that the caller inserts before the file extension
// when renaming the local file.
func Resolve(localModTime, driveModTime time.Time, strategy config.ConflictStrategy) ConflictDecision {
	gap := driveModTime.Sub(localModTime)
	if gap < 0 {
		gap = -gap
	}

	if strategy == config.ConflictKeepBoth && gap <= conflictWindow {
		older := localModTime
		if driveModTime.Before(localModTime) {
			older = driveModTime
		}
		token := "conflict." + older.UTC().Format("20060102150405")
		return ConflictDecision{Winner: "drive", ConflictPath: token}
	}

	if driveModTime.After(localModTime) {
		return ConflictDecision{Winner: "drive"}
	}
	return ConflictDecision{Winner: "local"}
}
