package drivesync

import (
	"encoding/json"
	"net/http"
)

// NewStatusHandler returns an http.Handler for GET /api/drive/status.
// Pass nil for p when Drive sync is disabled.
func NewStatusHandler(p StatusProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		type response struct {
			Enabled          bool    `json:"enabled"`
			Connected        bool    `json:"connected"`
			LastVaultSync    *string `json:"last_vault_sync"`
			LastPebbleBackup *string `json:"last_pebble_backup"`
			LastError        *string `json:"last_error"`
		}

		if p == nil {
			json.NewEncoder(w).Encode(response{})
			return
		}

		s := p.Status()
		json.NewEncoder(w).Encode(response{
			Enabled:          s.Enabled,
			Connected:        s.Connected,
			LastVaultSync:    s.LastVaultSync,
			LastPebbleBackup: s.LastPebbleBackup,
			LastError:        s.LastError,
		})
	})
}
