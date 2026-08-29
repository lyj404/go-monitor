package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lyj404/go-monitor/config"
	"github.com/lyj404/go-monitor/version"
)

func newVersionTestServer(t *testing.T, enabled bool) *Server {
	t.Helper()
	on := enabled
	s := &Server{cfg: config.FromSnapshot("", config.Snapshot{
		Update: config.UpdateCheckConfig{Enabled: &on},
	})}
	return s
}

func TestVersionHandlerReportsCachedState(t *testing.T) {
	t.Parallel()
	s := newVersionTestServer(t, true)
	s.verState.set("v2.0.0", "", time.Now())

	old := version.Current
	version.Current = "v1.0.0"
	defer func() { version.Current = old }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	s.versionHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["current"] != "v1.0.0" || resp["latest"] != "v2.0.0" {
		t.Fatalf("unexpected versions: %v", resp)
	}
	if resp["has_update"] != true {
		t.Fatal("has_update should be true")
	}
	if resp["enabled"] != true {
		t.Fatal("enabled should be true")
	}
	if _, ok := resp["checked_at"]; !ok {
		t.Fatal("checked_at should be present after a check")
	}
}

func TestVersionHandlerNeverCheckedHasNoUpdate(t *testing.T) {
	t.Parallel()
	s := newVersionTestServer(t, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	s.versionHandler(rec, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["has_update"] != false {
		t.Fatal("has_update must be false before any check")
	}
	if _, ok := resp["checked_at"]; ok {
		t.Fatal("checked_at must be absent before any check")
	}
}

func TestCheckVersionHandlerDisabled(t *testing.T) {
	t.Parallel()
	s := newVersionTestServer(t, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/version/check", nil)
	s.checkVersionHandler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestCheckVersionHandlerRejectsGet(t *testing.T) {
	t.Parallel()
	s := newVersionTestServer(t, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/version/check", nil)
	s.checkVersionHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestCheckVersionHandlerRecordsResult(t *testing.T) {
	// No t.Parallel: these tests swap the package-level
	// latestReleaseURL, so they must not run concurrently.

	var hit int
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"2.1.0"}`))
	}))
	defer stub.Close()

	oldURL := latestReleaseURL
	latestReleaseURL = stub.URL
	defer func() { latestReleaseURL = oldURL }()

	old := version.Current
	version.Current = "v2.0.0"
	defer func() { version.Current = old }()

	s := newVersionTestServer(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/version/check", nil)
	s.checkVersionHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if hit != 1 {
		t.Fatalf("stub hit = %d, want 1", hit)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// A tag without the "v" prefix is normalized before comparison.
	if resp["latest"] != "v2.1.0" {
		t.Fatalf("latest = %v, want v2.1.0", resp["latest"])
	}
	if resp["has_update"] != true {
		t.Fatal("has_update should be true")
	}

	s.verState.mu.RLock()
	defer s.verState.mu.RUnlock()
	if s.verState.checkedAt.IsZero() {
		t.Fatal("check must record its timestamp")
	}
}

func TestCheckVersionHandlerRecordsFailureState(t *testing.T) {
	// Shares latestReleaseURL with the other stub tests; keep sequential.

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer stub.Close()

	oldURL := latestReleaseURL
	latestReleaseURL = stub.URL
	defer func() { latestReleaseURL = oldURL }()

	s := newVersionTestServer(t, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/version/check", nil)
	s.checkVersionHandler(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "error" || resp["message"] == "" {
		t.Fatalf("failure response should carry status and message: %v", resp)
	}
	// The failure must also land in the cached state so the settings
	// page shows "检查失败" after a reload.
	s.verState.mu.RLock()
	defer s.verState.mu.RUnlock()
	if s.verState.errMsg == "" {
		t.Fatal("failed check must record the error message")
	}
}

func TestCheckVersionHandlerConcurrentRequests(t *testing.T) {
	t.Parallel()

	// Hold the check lock to simulate a check already in progress; a
	// second concurrent request must get a conflict, not a second fetch.
	s := newVersionTestServer(t, true)
	s.verCheckMu.Lock()
	defer s.verCheckMu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/version/check", nil)
	s.checkVersionHandler(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
}
