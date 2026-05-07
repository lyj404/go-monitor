package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"go-monitor/collector"
	"go-monitor/config"
	"go-monitor/store"
)

type Server struct {
	cfg             *config.Config
	col             *collector.Collector
	db              *store.DB
	sessions        map[string]sessionInfo
	sessMu          sync.RWMutex
	loginLimits     map[string]*loginAttempt
	limitMu         sync.Mutex
	done            chan struct{}
	indexHTMLBytes  []byte
	loginHTMLBytes  []byte
	configHTMLBytes []byte
}

type sessionInfo struct {
	username string
	expires  time.Time
}

type loginAttempt struct {
	count       int
	lockedUntil time.Time
	lastSeen    time.Time
}

func NewServer(cfg *config.Config, col *collector.Collector, db *store.DB) *Server {
	s := &Server{
		cfg:             cfg,
		col:             col,
		db:              db,
		sessions:        make(map[string]sessionInfo),
		loginLimits:     make(map[string]*loginAttempt),
		done:            make(chan struct{}),
		indexHTMLBytes:  indexHTMLBytes,
		loginHTMLBytes:  loginHTMLBytes,
		configHTMLBytes: configHTMLBytes,
	}
	go s.cleanupStale()
	return s
}

func (s *Server) Close() {
	close(s.done)
}

func (s *Server) cleanupStale() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupStaleOnce(time.Now())

		case <-s.done:
			return
		}
	}
}

func (s *Server) cleanupStaleOnce(now time.Time) {
	s.sessMu.Lock()
	for token, info := range s.sessions {
		if now.After(info.expires) {
			delete(s.sessions, token)
		}
	}
	s.sessMu.Unlock()

	s.limitMu.Lock()
	for ip, attempt := range s.loginLimits {
		if !attempt.lockedUntil.IsZero() && now.After(attempt.lockedUntil) {
			attempt.lockedUntil = time.Time{}
		}
		if attempt.count == 0 && attempt.lockedUntil.IsZero() && now.Sub(attempt.lastSeen) > 10*time.Minute {
			delete(s.loginLimits, ip)
		}
	}
	s.limitMu.Unlock()
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func parsePositiveLimit(raw string, defaultLimit int) int {
	if raw == "" {
		return defaultLimit
	}

	limit := 0
	if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit <= 0 {
		return defaultLimit
	}

	return limit
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			if ip := strings.TrimSpace(strings.Split(forwarded, ",")[0]); ip != "" {
				return ip
			}
		}

		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}

	return r.RemoteAddr
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		token := cookie.Value
		s.sessMu.RLock()
		info, exists := s.sessions[token]
		s.sessMu.RUnlock()

		if !exists || time.Now().After(info.expires) {
			if exists {
				s.sessMu.Lock()
				delete(s.sessions, token)
				s.sessMu.Unlock()
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "private, max-age=300")
		w.Write(s.loginHTMLBytes)
		return
	}

	ip := clientIP(r, s.cfg.Snapshot().Server.TrustProxy)
	now := time.Now()

	s.limitMu.Lock()
	attempt, exists := s.loginLimits[ip]
	if !exists {
		attempt = &loginAttempt{}
		s.loginLimits[ip] = attempt
	}
	attempt.lastSeen = now
	if !attempt.lockedUntil.IsZero() && now.Before(attempt.lockedUntil) {
		s.limitMu.Unlock()
		http.Error(w, "Too many login attempts, try again later", http.StatusTooManyRequests)
		return
	}
	s.limitMu.Unlock()

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}

	cfg := s.cfg.Snapshot()

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Username == cfg.Auth.Username && req.Password == cfg.Auth.Password {
		s.limitMu.Lock()
		attempt.count = 0
		attempt.lockedUntil = time.Time{}
		attempt.lastSeen = time.Now()
		s.limitMu.Unlock()

		token, err := generateToken()
		if err != nil {
			log.Println("生成会话 token 失败:", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		duration := 24 * time.Hour
		if req.Remember {
			duration = 30 * 24 * time.Hour // 记住我则保持 30 天
		}
		expires := time.Now().Add(duration)

		s.sessMu.Lock()
		s.sessions[token] = sessionInfo{
			username: req.Username,
			expires:  expires,
		}
		s.sessMu.Unlock()

		http.SetCookie(w, &http.Cookie{
			Name:     "session",
			Value:    token,
			Path:     "/",
			Expires:  expires,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	s.limitMu.Lock()
	attempt.count++
	attempt.lastSeen = time.Now()
	if attempt.count >= 5 {
		attempt.lockedUntil = time.Now().Add(5 * time.Minute)
		attempt.count = 0
		log.Printf("登录锁定: IP %s 连续失败5次，锁定5分钟", ip)
	}
	s.limitMu.Unlock()

	http.Error(w, "Invalid credentials", http.StatusUnauthorized)
}

func (s *Server) logoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		s.sessMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessMu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/login", http.StatusFound)
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if data := s.col.GetMetricsJSON(); len(data) > 0 {
		w.Write(data)
		return
	}
	json.NewEncoder(w).Encode(s.col.GetMetrics())
}

func (s *Server) historyDailyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		json.NewEncoder(w).Encode([]store.DailyNetwork{})
		return
	}
	startDate := r.URL.Query().Get("start")
	endDate := r.URL.Query().Get("end")
	limit := parsePositiveLimit(r.URL.Query().Get("limit"), 30)
	if startDate == "" {
		startDate = "1970-01-01"
	}
	if endDate == "" {
		endDate = "2099-12-31"
	}
	dailies, err := s.db.GetDailyNetwork(startDate, endDate, limit)
	if err != nil {
		json.NewEncoder(w).Encode([]store.DailyNetwork{})
		return
	}
	json.NewEncoder(w).Encode(dailies)
}

func (s *Server) historyMonthlyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		json.NewEncoder(w).Encode([]store.MonthlyNetwork{})
		return
	}
	startMonth := r.URL.Query().Get("start")
	endMonth := r.URL.Query().Get("end")
	limit := parsePositiveLimit(r.URL.Query().Get("limit"), 12)
	if startMonth == "" {
		startMonth = "1970-01"
	}
	if endMonth == "" {
		endMonth = "2099-12"
	}
	monthlies, err := s.db.GetMonthlyNetwork(startMonth, endMonth, limit)
	if err != nil {
		json.NewEncoder(w).Encode([]store.MonthlyNetwork{})
		return
	}
	json.NewEncoder(w).Encode(monthlies)
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Write(s.indexHTMLBytes)
}

func (s *Server) configPageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Write(s.configHTMLBytes)
}

func (s *Server) getConfigHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.cfg.MaskSensitive())
}

func (s *Server) updateConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var updated map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	intervalChanged, err := s.cfg.Reload(updated)
	if err != nil {
		log.Println("配置更新失败:", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": "保存配置失败: " + err.Error()})
		return
	}

	s.col.UpdateSnapshot()

	if intervalChanged {
		s.col.NotifyIntervalChanged()
	}

	log.Println("配置已更新并生效")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
