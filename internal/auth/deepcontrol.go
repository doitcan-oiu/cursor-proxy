package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"cursor-proxy/internal/httpx"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.6834.210 Safari/537.36"

// PkcePair PKCE 校验对。
type PkcePair struct {
	Verifier  string
	Challenge string
}

// GeneratePkcePair 生成 PKCE verifier/challenge。
func GeneratePkcePair() PkcePair {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	if len(verifier) > 43 {
		verifier = verifier[:43]
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return PkcePair{Verifier: verifier, Challenge: challenge}
}

// AuthPollResult auth/poll 返回体。
type AuthPollResult struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	AuthID       string `json:"authId"`
}

// BuildTokenFromPoll 用 authId 里的 userId 拼成 userId%3A%3AaccessToken 形式。
func BuildTokenFromPoll(data AuthPollResult) string {
	if data.AccessToken == "" {
		return ""
	}
	parts := strings.Split(data.AuthID, "|")
	if len(parts) > 1 {
		return parts[1] + "%3A%3A" + data.AccessToken
	}
	return data.AccessToken
}

// QueryAuthPoll 轮询一次 auth/poll。
func QueryAuthPoll(uuidStr, verifier string) (AuthPollResult, bool) {
	u := fmt.Sprintf("https://api2.cursor.sh/auth/poll?uuid=%s&verifier=%s",
		url.QueryEscape(uuidStr), url.QueryEscape(verifier))
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("user-agent", browserUA)
	req.Header.Set("accept", "*/*")
	req.Header.Set("cache-control", "no-cache")
	resp, err := httpx.Client("").Do(req)
	if err != nil {
		return AuthPollResult{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AuthPollResult{}, false
	}
	var out AuthPollResult
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return AuthPollResult{}, false
	}
	return out, true
}

func buildCookie(workosToken string) string {
	sep := "::"
	if strings.Contains(workosToken, "%3A%3A") {
		sep = "%3A%3A"
	}
	idx := strings.Index(workosToken, sep)
	if idx <= 0 {
		return "WorkosCursorSessionToken=" + workosToken
	}
	workosID := workosToken[:idx]
	jwt := workosToken[idx+len(sep):]
	return "WorkosCursorSessionToken=" + workosID + "%3A%3A" + url.QueryEscape(jwt)
}

// ExchangeResult deep-control 换取结果。
type ExchangeResult struct {
	Token        string
	RefreshToken string
}

// ExchangeWorkosCookieFull 用浏览器 web token 走 deep-control PKCE 换取可对话的 session token。
func ExchangeWorkosCookieFull(workosToken string) (ExchangeResult, error) {
	pair := GeneratePkcePair()
	uuidStr := uuid.NewString()

	body, _ := json.Marshal(map[string]string{"uuid": uuidStr, "challenge": pair.Challenge})
	req, _ := http.NewRequest(http.MethodPost, "https://cursor.com/api/auth/loginDeepCallbackControl", strings.NewReader(string(body)))
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("user-agent", browserUA)
	req.Header.Set("origin", "https://www.cursor.com")
	req.Header.Set("referer", fmt.Sprintf("https://www.cursor.com/cn/loginDeepControl?challenge=%s&uuid=%s&mode=login", pair.Challenge, uuidStr))
	req.Header.Set("cookie", buildCookie(workosToken))

	resp, err := httpx.Client("").Do(req)
	if err != nil {
		return ExchangeResult{}, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ExchangeResult{}, fmt.Errorf("loginDeepCallbackControl failed with HTTP %d. web 会话可能已在网页端登出/失效", resp.StatusCode)
	}

	for i := 0; i < 30; i++ {
		if data, ok := QueryAuthPoll(uuidStr, pair.Verifier); ok {
			if token := BuildTokenFromPoll(data); token != "" {
				return ExchangeResult{Token: token, RefreshToken: data.RefreshToken}, nil
			}
		}
		time.Sleep(time.Second)
	}
	return ExchangeResult{}, fmt.Errorf("timed out waiting for Cursor auth poll, please retry")
}

// ExchangeWorkosCookie 仅返回换取的 token 字符串。
func ExchangeWorkosCookie(workosToken string) (string, error) {
	r, err := ExchangeWorkosCookieFull(workosToken)
	return r.Token, err
}
