package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lyj404/go-monitor/config"
)

func TestParsePositiveLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		raw          string
		defaultLimit int
		want         int
	}{
		{name: "empty uses default", raw: "", defaultLimit: 30, want: 30},
		{name: "invalid uses default", raw: "abc", defaultLimit: 30, want: 30},
		{name: "zero uses default", raw: "0", defaultLimit: 30, want: 30},
		{name: "positive value used", raw: "7", defaultLimit: 30, want: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parsePositiveLimit(tt.raw, tt.defaultLimit); got != tt.want {
				t.Fatalf("parsePositiveLimit(%q, %d) = %d, want %d", tt.raw, tt.defaultLimit, got, tt.want)
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	if got := clientIP(req, false); got != "127.0.0.1" {
		t.Fatalf("unexpected remote IP: %q", got)
	}

	// Without trust_proxy, X-Forwarded-For must be ignored to prevent spoofing.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	if got := clientIP(req, false); got != "127.0.0.1" {
		t.Fatalf("XFF should be ignored when trust_proxy=false, got: %q", got)
	}

	// With trust_proxy, the first XFF entry wins.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	if got := clientIP(req, true); got != "10.0.0.1" {
		t.Fatalf("unexpected forwarded IP: %q", got)
	}
}

func TestHistoryHandlersReturnDBUnavailableWhenDBNil(t *testing.T) {
	t.Parallel()

	s := &Server{cfg: config.FromSnapshot("", config.Snapshot{})}

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "daily", handler: s.historyDailyHandler},
		{name: "monthly", handler: s.historyMonthlyHandler},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/?limit=5", nil)
			tc.handler(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("unexpected status: %d", rec.Code)
			}

			var payload map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}
			if payload["status"] != "error" {
				t.Fatalf("expected error payload, got %v", payload)
			}
		})
	}
}

func TestCleanupStaleRemovesExpiredLimiterEntries(t *testing.T) {
	t.Parallel()

	s := &Server{
		loginLimits: map[string]*loginAttempt{
			"1.1.1.1": {
				count:       0,
				lockedUntil: time.Time{},
				lastSeen:    time.Now().Add(-11 * time.Minute),
			},
			"2.2.2.2": {
				count:       1,
				lockedUntil: time.Time{},
				lastSeen:    time.Now().Add(-11 * time.Minute),
			},
		},
		sessions: make(map[string]sessionInfo),
		done:     make(chan struct{}),
	}

	s.cleanupStaleOnce(time.Now())

	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	if _, ok := s.loginLimits["1.1.1.1"]; ok {
		t.Fatal("expected idle limiter entry to be cleaned")
	}
	if _, ok := s.loginLimits["2.2.2.2"]; ok {
		t.Fatal("expected stale partial-attempt limiter entry to be cleaned")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	s := &Server{done: make(chan struct{})}
	s.Close()
	s.Close()
}

func TestSameOriginPolicy(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := sameOriginPolicy(ok)

	cases := []struct {
		name   string
		method string
		origin string
		host   string
		want   int
	}{
		// No Origin header: non-browser clients (curl, health checks).
		{name: "post without origin passes", method: http.MethodPost, origin: "", host: "m:9527", want: http.StatusOK},
		// Same-origin browser requests.
		{name: "post same origin passes", method: http.MethodPost, origin: "http://m:9527", host: "m:9527", want: http.StatusOK},
		// Cross-site POST must be rejected.
		{name: "post cross origin rejected", method: http.MethodPost, origin: "http://evil.example", host: "m:9527", want: http.StatusForbidden},
		// A lookalike host must not pass the string comparison.
		{name: "post lookalike origin rejected", method: http.MethodPost, origin: "http://m:9527.evil.example", host: "m:9527", want: http.StatusForbidden},
		// HTTPS page posting to HTTP host must be rejected (scheme differs).
		{name: "post https origin to http host rejected", method: http.MethodPost, origin: "https://m:9527", host: "m:9527", want: http.StatusForbidden},
		// Safe methods are not state-changing and always pass.
		{name: "get cross origin passes", method: http.MethodGet, origin: "http://evil.example", host: "m:9527", want: http.StatusOK},
		// PUT (config save) is covered by the same check.
		{name: "put cross origin rejected", method: http.MethodPut, origin: "http://evil.example", host: "m:9527", want: http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestEvictOldestLoginLimitAlwaysFreesSlot(t *testing.T) {
	t.Parallel()

	s := &Server{loginLimits: map[string]*loginAttempt{
		"1.1.1.1": {count: 5, lockedUntil: time.Now().Add(5 * time.Minute), lastSeen: time.Now().Add(-time.Minute)},
		"2.2.2.2": {count: 5, lockedUntil: time.Now().Add(5 * time.Minute), lastSeen: time.Now().Add(-2 * time.Minute)},
	}}

	s.limitMu.Lock()
	s.evictOldestLoginLimitLocked()
	s.limitMu.Unlock()

	if len(s.loginLimits) != 1 {
		t.Fatalf("expected a locked entry to be evicted when the map is full, len=%d", len(s.loginLimits))
	}
	if _, ok := s.loginLimits["2.2.2.2"]; ok {
		t.Fatal("expected the oldest lastSeen entry to be evicted")
	}
}
