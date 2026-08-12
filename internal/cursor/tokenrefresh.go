package cursor

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/store"
)

const authClientID = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
const refreshSkewSeconds = 15 * 60

// needsRefresh 判断某条凭证是否临近过期且有 refresh token。
func needsRefresh(entry store.CursorTokenEntry) bool {
	if entry.RefreshToken == "" || entry.ExpiresAt == 0 {
		return false
	}
	return entry.ExpiresAt-time.Now().Unix() < refreshSkewSeconds
}

// refreshAccessToken 用 refresh token 换取新的 access token。
func refreshAccessToken(refreshToken string) string {
	body, _ := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     authClientID,
		"refresh_token": refreshToken,
	})
	req, _ := http.NewRequest(http.MethodPost, config.Get().CursorBackendURL+"/oauth/token", strings.NewReader(string(body)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := httpx.Client("").Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var data struct {
		AccessToken  string `json:"accessToken"`
		AccessToken2 string `json:"access_token"`
	}
	if json.NewDecoder(resp.Body).Decode(&data) != nil {
		return ""
	}
	if data.AccessToken != "" {
		return data.AccessToken
	}
	return data.AccessToken2
}

// EnsureFreshToken 确保凭证新鲜：临近过期则刷新并持久化，返回可用的 Bearer JWT。
func EnsureFreshToken(entry store.CursorTokenEntry) string {
	if !needsRefresh(entry) {
		return auth.ExtractBearer(entry.Token)
	}
	if next := refreshAccessToken(entry.RefreshToken); next != "" {
		auth.UpdateTokenValue(entry.ID, next)
		return auth.ExtractBearer(next)
	}
	return auth.ExtractBearer(entry.Token)
}

// StartRefreshSweeper 后台定时把临近过期的凭证批量刷新。
func StartRefreshSweeper(interval time.Duration) {
	run := func() {
		for _, entry := range store.Read().CursorTokens {
			if needsRefresh(entry) {
				if next := refreshAccessToken(entry.RefreshToken); next != "" && auth.TokenExpiry(next) != 0 {
					auth.UpdateTokenValue(entry.ID, next)
				}
			}
		}
	}
	go func() {
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()
}
