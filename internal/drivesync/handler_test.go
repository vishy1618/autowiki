package drivesync_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suvish/autowiki/internal/drivesync"
)

func strPtr(s string) *string { return &s }

func TestStatusHandler_EncodesPopulatedStatus(t *testing.T) {
	// Arrange — fake provider returns a fully populated status.
	ts := "2026-04-22T10:00:00Z"
	errMsg := "upload failed"
	provider := &fakeStatusProvider{status: drivesync.DriveStatus{
		Enabled:       true,
		Connected:     true,
		LastVaultSync: strPtr(ts),
		LastError:     strPtr(errMsg),
	}}
	handler := drivesync.NewStatusHandler(provider)
	req := httptest.NewRequest(http.MethodGet, "/api/drive/status", nil)
	w := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(w, req)

	// Assert
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if enabled, _ := body["enabled"].(bool); !enabled {
		t.Error("want enabled=true")
	}
	if connected, _ := body["connected"].(bool); !connected {
		t.Error("want connected=true")
	}
	if got, _ := body["last_vault_sync"].(string); got != ts {
		t.Errorf("last_vault_sync: want %q, got %q", ts, got)
	}
	if got, _ := body["last_error"].(string); got != errMsg {
		t.Errorf("last_error: want %q, got %q", errMsg, got)
	}
	if v, ok := body["last_pebble_backup"]; !ok || v != nil {
		t.Errorf("want last_pebble_backup=null, got %v", v)
	}
}

type fakeStatusProvider struct{ status drivesync.DriveStatus }

func (f *fakeStatusProvider) Status() drivesync.DriveStatus { return f.status }

func TestStatusHandler_WhenProviderNil_ReturnsEnabledFalseWithNullFields(t *testing.T) {
	// Arrange
	handler := drivesync.NewStatusHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/drive/status", nil)
	w := httptest.NewRecorder()

	// Act
	handler.ServeHTTP(w, req)

	// Assert
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("parsing response: %v", err)
	}
	if enabled, _ := body["enabled"].(bool); enabled {
		t.Error("want enabled=false")
	}
	for _, field := range []string{"last_vault_sync", "last_pebble_backup", "last_error"} {
		if v, ok := body[field]; !ok || v != nil {
			t.Errorf("want %s=null, got %v", field, v)
		}
	}
}
