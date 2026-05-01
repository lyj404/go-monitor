package server

import (
	"net/http"
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
	protected.HandleFunc("/api/config", s.configAPIHandler)
	protected.HandleFunc("/settings", s.configPageHandler)
	protected.HandleFunc("/", s.indexHandler)

	mux.Handle("/", s.authMiddleware(protected))

	return securityHeaders(mux)
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
