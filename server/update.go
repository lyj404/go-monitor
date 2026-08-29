package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lyj404/go-monitor/version"
)

const (
	releasesPageURL   = "https://github.com/lyj404/go-monitor/releases/latest"
	versionCheckLimit = 10 * time.Second
)

// latestReleaseURL is a var so tests can point it at a local stub server.
var latestReleaseURL = "https://api.github.com/repos/lyj404/go-monitor/releases/latest"

var versionHTTPClient = &http.Client{Timeout: versionCheckLimit}

// versionState caches the result of the most recent update check so the
// dashboard badge can read it without triggering a network request.
type versionState struct {
	mu        sync.RWMutex
	latest    string
	checkedAt time.Time
	errMsg    string
}

func (v *versionState) set(latest, errMsg string, now time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.latest = latest
	v.checkedAt = now
	v.errMsg = errMsg
}

// versionResponse builds the JSON payload shared by the status and check
// endpoints. has_update is only meaningful when both versions parse as
// semver; version.HasUpdate returns false otherwise (e.g. "dev" builds).
func (s *Server) versionResponse(enabled bool) map[string]interface{} {
	s.verState.mu.RLock()
	latest, errMsg := s.verState.latest, s.verState.errMsg
	checkedAt := s.verState.checkedAt
	s.verState.mu.RUnlock()

	resp := map[string]interface{}{
		"current":      version.Current,
		"known":        version.IsKnown(),
		"enabled":      enabled,
		"latest":       latest,
		"has_update":   version.HasUpdate(latest),
		"error":        errMsg,
		"releases_url": releasesPageURL,
	}
	if !checkedAt.IsZero() {
		resp["checked_at"] = checkedAt.Format(time.RFC3339)
	}
	return resp
}

func (s *Server) versionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.versionResponse(s.cfg.Snapshot().Update.CheckEnabled()))
}

func (s *Server) checkVersionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.Snapshot().Update.CheckEnabled() {
		writeJSONError(w, http.StatusForbidden, "版本检查已在设置中禁用")
		return
	}

	// TryLock keeps concurrent clicks (or multiple tabs) from piling up
	// identical GitHub requests; a check is at most a few seconds long.
	if !s.verCheckMu.TryLock() {
		writeJSONError(w, http.StatusConflict, "已有一次检查正在进行，请稍候")
		return
	}
	defer s.verCheckMu.Unlock()

	latest, err := fetchLatestRelease(r.Context())
	if err != nil {
		s.verState.set("", err.Error(), time.Now())
		// Return the full version state alongside the error so the
		// settings page can render "检查失败：<原因>" from one response.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		resp := s.versionResponse(true)
		resp["status"] = "error"
		resp["message"] = err.Error()
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	s.verState.set(latest, "", time.Now())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.versionResponse(true))
}

// fetchLatestRelease queries the GitHub Releases API and returns the
// latest tag normalized to semver's required "v" prefix.
func fetchLatestRelease(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionCheckLimit)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := versionHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("无法连接 GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub 返回异常状态码 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("响应中没有版本号（仓库可能尚未发布任何 Release）")
	}
	tag := payload.TagName
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return tag, nil
}
