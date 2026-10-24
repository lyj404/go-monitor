package server

import (
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/login", s.loginHandler)
	mux.HandleFunc("/api/login", s.loginHandler)
	mux.HandleFunc("/api/logout", s.logoutHandler)
	mux.HandleFunc("/health", s.healthHandler)

	protected := http.NewServeMux()
	protected.HandleFunc("/api/metrics", s.metricsHandler)
	protected.HandleFunc("/api/history/daily", s.historyDailyHandler)
	protected.HandleFunc("/api/history/monthly", s.historyMonthlyHandler)
	protected.HandleFunc("/api/history/alerts", s.alertHistoryHandler)
	protected.HandleFunc("/api/history/metrics", s.metricsHistoryHandler)
	protected.HandleFunc("/api/config", s.configAPIHandler)
	protected.HandleFunc("/api/version", s.versionHandler)
	protected.HandleFunc("/api/version/check", s.checkVersionHandler)
	protected.HandleFunc("/settings", s.configPageHandler)
	protected.HandleFunc("/", s.indexHandler)

	mux.Handle("/", s.authMiddleware(protected))

	return securityHeaders(sameOriginPolicy(mux))
}

func (s *Server) configAPIHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getConfigHandler(w, r)
	case http.MethodPut:
		s.updateConfigHandler(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self';")
		next.ServeHTTP(w, r)
	})
}

// sameOriginPolicy rejects cross-site state-changing requests. Session
// cookies are SameSite=Lax, which already blocks most CSRF, but browsers
// without the attribute support (and future relaxations) still benefit from
// an explicit check. GET/HEAD/OPTIONS are safe methods and pass through.
// An absent Origin header is allowed so curl, health checks and non-browser
// clients keep working; any browser that sends Origin gets it verified.
// The scheme is compared too, so an https page cannot POST to an http host.
func sameOriginPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || strings.ToLower(u.Scheme) != strings.ToLower(requestScheme(r)) ||
					strings.ToLower(u.Host) != strings.ToLower(r.Host) {
					http.Error(w, "cross-origin request rejected", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestScheme reports the scheme the request was received on. TLS can only
// be detected via r.TLS because Go's http server does not expose X-Forwarded-Proto.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
