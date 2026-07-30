package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lyj404/go-monitor/collector"
	"github.com/lyj404/go-monitor/config"
	"github.com/lyj404/go-monitor/store"
)

const (
	maxConfigPayloadBytes = 1 << 20
	maxSessions           = 10000
	maxLoginLimitEntries  = 10000
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
	cleanupWg       sync.WaitGroup
	indexHTMLBytes  []byte
	loginHTMLBytes  []byte
	configHTMLBytes []byte

	configJSONMu    sync.RWMutex
	configJSONBytes []byte
	configJSONGen   uint64
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
	s.cleanupWg.Add(1)
	go s.cleanupStale()
	return s
}

func (s *Server) Close() {
	select {
	case <-s.done:
		return
	default:
		close(s.done)
		// Wait for cleanupStale to exit so its goroutine does not outlive
		// Close (e.g. touch sessMu/limitMu after the rest of the process
		// has torn down). It returns promptly because done is now closed.
		s.cleanupWg.Wait()
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "error",
		"message": message,
	})
}

func (s *Server) cleanupStale() {
	defer s.cleanupWg.Done()

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
		if attempt.lockedUntil.IsZero() && now.Sub(attempt.lastSeen) > 10*time.Minute {
			delete(s.loginLimits, ip)
		}
	}
	s.limitMu.Unlock()
}

// evictSessionsLocked is called before inserting a new session. If the map is
// at capacity, it first drops expired sessions; if still at capacity, it drops
// the session that expires soonest. Caller must hold s.sessMu.
func (s *Server) evictSessionsLocked(now time.Time) {
	if len(s.sessions) < maxSessions {
		return
	}
	for token, info := range s.sessions {
		if now.After(info.expires) {
			delete(s.sessions, token)
		}
	}
	if len(s.sessions) < maxSessions {
		return
	}
	var victim string
	var earliest time.Time
	for token, info := range s.sessions {
		if victim == "" || info.expires.Before(earliest) {
			victim = token
			earliest = info.expires
		}
	}
	if victim != "" {
		delete(s.sessions, victim)
	}
}

// evictOldestLoginLimitLocked drops the loginLimits entry with the oldest
// lastSeen so that a fresh IP can be tracked. Caller must hold s.limitMu.
func (s *Server) evictOldestLoginLimitLocked() {
	var victim string
	var oldest time.Time
	for ip, attempt := range s.loginLimits {
		if !attempt.lockedUntil.IsZero() {
			continue
		}
		if victim == "" || attempt.lastSeen.Before(oldest) {
			victim = ip
			oldest = attempt.lastSeen
		}
	}
	if victim != "" {
		delete(s.loginLimits, victim)
	}
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
		if len(s.loginLimits) >= maxLoginLimitEntries {
			s.evictOldestLoginLimitLocked()
		}
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

	// Constant-time comparison to avoid leaking username/password length or
	// prefix information via timing side-channels.
	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(cfg.Auth.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(req.Password), []byte(cfg.Auth.Password)) == 1
	if userOK && passOK {
		s.limitMu.Lock()
		if cur, ok := s.loginLimits[ip]; ok {
			cur.count = 0
			cur.lockedUntil = time.Time{}
			cur.lastSeen = time.Now()
		}
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
		s.evictSessionsLocked(now)
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
	cur, ok := s.loginLimits[ip]
	if !ok {
		if len(s.loginLimits) >= maxLoginLimitEntries {
			s.evictOldestLoginLimitLocked()
		}
		cur = &loginAttempt{}
		s.loginLimits[ip] = cur
	}
	cur.count++
	cur.lastSeen = time.Now()
	if cur.count >= 5 {
		cur.lockedUntil = time.Now().Add(5 * time.Minute)
		cur.count = 0
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
		writeJSONError(w, http.StatusServiceUnavailable, "database unavailable")
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
		log.Println("查询每日历史数据失败:", err)
		writeJSONError(w, http.StatusInternalServerError, "query daily history failed")
		return
	}
	json.NewEncoder(w).Encode(dailies)
}

func (s *Server) historyMonthlyHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "database unavailable")
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
		log.Println("查询月度历史数据失败:", err)
		writeJSONError(w, http.StatusInternalServerError, "query monthly history failed")
		return
	}
	json.NewEncoder(w).Encode(monthlies)
}

func (s *Server) alertHistoryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	limit := parsePositiveLimit(r.URL.Query().Get("limit"), 50)
	alerts, err := s.db.GetAlertHistory(limit)
	if err != nil {
		log.Println("查询告警历史失败:", err)
		writeJSONError(w, http.StatusInternalServerError, "query alert history failed")
		return
	}
	json.NewEncoder(w).Encode(alerts)
}

func (s *Server) metricsHistoryHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "database unavailable")
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
	metrics, err := s.db.GetDailyMetrics(startDate, endDate, limit)
	if err != nil {
		log.Println("查询指标历史失败:", err)
		writeJSONError(w, http.StatusInternalServerError, "query metrics history failed")
		return
	}
	json.NewEncoder(w).Encode(metrics)
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
	data, err := s.maskedConfigJSON()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "marshal config failed")
		return
	}
	w.Write(data)
}

// maskedConfigJSON returns a cached JSON encoding of the masked config. The
// cache is invalidated on every successful Reload. A generation counter prevents
// a slow marshal from writing a stale snapshot back after invalidation.
func (s *Server) maskedConfigJSON() ([]byte, error) {
	s.configJSONMu.RLock()
	cached := s.configJSONBytes
	gen := s.configJSONGen
	s.configJSONMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	data, err := json.Marshal(s.cfg.MaskSensitive())
	if err != nil {
		return nil, err
	}
	s.configJSONMu.Lock()
	if s.configJSONGen == gen && s.configJSONBytes == nil {
		s.configJSONBytes = data
	} else if s.configJSONBytes != nil {
		data = s.configJSONBytes
	}
	s.configJSONMu.Unlock()
	return data, nil
}

func (s *Server) invalidateConfigJSON() {
	s.configJSONMu.Lock()
	s.configJSONBytes = nil
	s.configJSONGen++
	s.configJSONMu.Unlock()
}

func (s *Server) updateConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var updated map[string]interface{}
	r.Body = http.MaxBytesReader(w, r.Body, maxConfigPayloadBytes)
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if len(updated) == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty config payload")
		return
	}

	intervalChanged, err := s.cfg.Reload(updated)
	if err != nil {
		log.Println("配置更新失败:", err)
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrPermission) {
			status = http.StatusForbidden
		}
		writeJSONError(w, status, "保存配置失败: "+err.Error())
		return
	}

	s.col.UpdateSnapshot()
	s.invalidateConfigJSON()
	s.applyLanWanSplit(s.cfg.Snapshot().Monitor.LanWanSplit)

	if intervalChanged {
		s.col.NotifyIntervalChanged()
	}

	log.Println("配置已更新并生效")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) applyLanWanSplit(enabled bool) {
	if enabled {
		if err := collector.EnableLanWanSplit(); err != nil {
			log.Println("LAN/WAN 流量分类启用失败:", err)
		}
		return
	}
	collector.DisableLanWanSplit()
}
