// Package auth 负责对外代理 Key、Cursor 凭证与登录换取相关逻辑。
package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"cursor-proxy/internal/store"
)

// ExtractBearer 从浏览器 Cookie 形态 `user_xxx%3A%3AeyJ...`（即 userId::JWT）里取出真正用于
// 上游 Authorization 的 JWT。纯 JWT 原样返回。
func ExtractBearer(raw string) string {
	token := strings.TrimSpace(raw)
	if strings.Contains(token, "%3A%3A") {
		parts := strings.SplitN(token, "%3A%3A", 2)
		if len(parts) == 2 {
			token = parts[1]
		}
	} else if strings.Contains(token, "::") {
		parts := strings.SplitN(token, "::", 2)
		if len(parts) == 2 {
			token = parts[1]
		}
	}
	return strings.TrimSpace(token)
}

func jwtPayload(raw string) map[string]any {
	parts := strings.Split(ExtractBearer(raw), ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// 兼容带 padding 的情况
		if data, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return nil
		}
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

// AccountIDFromToken 从 JWT 的 sub（形如 auth0|user_xxx）解出账号标识。
func AccountIDFromToken(raw string) string {
	m := jwtPayload(raw)
	if m == nil {
		return ""
	}
	sub, _ := m["sub"].(string)
	if sub == "" {
		return ""
	}
	if i := strings.LastIndex(sub, "|"); i >= 0 {
		return sub[i+1:]
	}
	return sub
}

// TokenExpiry 读取 JWT 的 exp（unix 秒），无则返回 0。
func TokenExpiry(raw string) int64 {
	m := jwtPayload(raw)
	if m == nil {
		return 0
	}
	if exp, ok := m["exp"].(float64); ok {
		return int64(exp)
	}
	return 0
}

// TokenKind 返回 JWT 的 type：web（浏览器会话）或 session（可对话）。
func TokenKind(raw string) string {
	m := jwtPayload(raw)
	if m == nil {
		return ""
	}
	t, _ := m["type"].(string)
	return t
}

// IsWebToken 判断是否为不能直接对话的 web token。
func IsWebToken(raw string) bool { return TokenKind(raw) == "web" }

func machineCodeFor(rawToken string) string {
	sum := sha256.Sum256([]byte(ExtractBearer(rawToken) + "machineId"))
	return hex.EncodeToString(sum[:])[:16]
}

// AddCursorToken 保存一条 Cursor 凭证并返回条目。
func AddCursorToken(token, label, refreshToken string) store.CursorTokenEntry {
	trimmed := strings.TrimSpace(token)
	if label == "" {
		label = AccountIDFromToken(trimmed)
	}
	entry := store.CursorTokenEntry{
		ID:           uuid.NewString(),
		Label:        label,
		Token:        trimmed,
		CreatedAt:    time.Now().UnixMilli(),
		RefreshToken: strings.TrimSpace(refreshToken),
		ExpiresAt:    TokenExpiry(trimmed),
	}
	if entry.Label == "" {
		entry.Label = "token-" + entry.ID[:8]
	}
	store.Mutate(func(s *store.Shape) any {
		s.CursorTokens = append(s.CursorTokens, entry)
		return nil
	})
	return entry
}

// UpdateTokenValue 用刷新得到的新 access token 更新库内条目。
func UpdateTokenValue(id, newToken string) {
	store.Mutate(func(s *store.Shape) any {
		for i := range s.CursorTokens {
			if s.CursorTokens[i].ID == id {
				s.CursorTokens[i].Token = strings.TrimSpace(newToken)
				s.CursorTokens[i].ExpiresAt = TokenExpiry(newToken)
				s.CursorTokens[i].LastRefreshedAt = time.Now().UnixMilli()
			}
		}
		return nil
	})
}

// BatchImportItem 批量导入的单条输入。
type BatchImportItem struct {
	Token        string
	Label        string
	RefreshToken string
}

// BatchImportResult 批量导入结果。
type BatchImportResult struct {
	Added      []AddedRef `json:"added"`
	Duplicates []string   `json:"duplicates"`
	Invalid    []string   `json:"invalid"`
}

// AddedRef 新增账号的引用。
type AddedRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func previewOf(token string) string {
	if len(token) <= 18 {
		return token
	}
	return token[:12] + "..." + token[len(token)-6:]
}

// AddCursorTokensBatch 批量导入凭证，按 JWT 去重（跨库内与本批次）。
func AddCursorTokensBatch(items []BatchImportItem) BatchImportResult {
	return store.Mutate(func(s *store.Shape) BatchImportResult {
		result := BatchImportResult{Added: []AddedRef{}, Duplicates: []string{}, Invalid: []string{}}
		seen := map[string]bool{}
		for _, t := range s.CursorTokens {
			seen[ExtractBearer(t.Token)] = true
		}
		for _, item := range items {
			token := strings.TrimSpace(item.Token)
			if token == "" {
				continue
			}
			bearer := ExtractBearer(token)
			if len(strings.Split(bearer, ".")) < 3 {
				result.Invalid = append(result.Invalid, previewOf(token))
				continue
			}
			if seen[bearer] {
				label := item.Label
				if label == "" {
					label = AccountIDFromToken(token)
				}
				if label == "" {
					label = previewOf(token)
				}
				result.Duplicates = append(result.Duplicates, label)
				continue
			}
			seen[bearer] = true
			id := uuid.NewString()
			label := strings.TrimSpace(item.Label)
			if label == "" {
				label = AccountIDFromToken(token)
			}
			if label == "" {
				label = "token-" + id[:8]
			}
			entry := store.CursorTokenEntry{
				ID:           id,
				Label:        label,
				Token:        token,
				CreatedAt:    time.Now().UnixMilli(),
				RefreshToken: strings.TrimSpace(item.RefreshToken),
				ExpiresAt:    TokenExpiry(token),
			}
			s.CursorTokens = append(s.CursorTokens, entry)
			result.Added = append(result.Added, AddedRef{ID: id, Label: label})
		}
		return result
	})
}

// ParseTokensText 把自由文本拆成 token 列表（支持换行、`token,label`、`token label`，# 注释）。
func ParseTokensText(text string) []BatchImportItem {
	var items []BatchImportItem
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, ","); idx > 0 && strings.Contains(line[:idx], ".") {
			items = append(items, BatchImportItem{Token: strings.TrimSpace(line[:idx]), Label: strings.TrimSpace(line[idx+1:])})
			continue
		}
		if i := strings.IndexAny(line, " \t"); i > 0 {
			items = append(items, BatchImportItem{Token: strings.TrimSpace(line[:i]), Label: strings.TrimSpace(line[i+1:])})
			continue
		}
		items = append(items, BatchImportItem{Token: line})
	}
	return items
}

// TokenView 是对外展示的账号视图（不含完整 token）。
type TokenView struct {
	ID              string `json:"id"`
	Label           string `json:"label"`
	CreatedAt       int64  `json:"createdAt"`
	Preview         string `json:"preview"`
	ExpiresAt       int64  `json:"expiresAt,omitempty"`
	HasRefresh      bool   `json:"hasRefresh"`
	LastRefreshedAt int64  `json:"lastRefreshedAt,omitempty"`
	MachineCode     string `json:"machineCode"`
	ProxyURL        string `json:"proxyUrl,omitempty"`
}

// ListCursorTokens 列出全部账号视图。
func ListCursorTokens() []TokenView {
	tokens := store.Read().CursorTokens
	out := make([]TokenView, 0, len(tokens))
	for _, t := range tokens {
		mc := machineCodeFor(t.Token)
		if t.Identity != nil && len(t.Identity.MachineID) >= 16 {
			mc = t.Identity.MachineID[:16]
		}
		out = append(out, TokenView{
			ID:              t.ID,
			Label:           t.Label,
			CreatedAt:       t.CreatedAt,
			Preview:         previewOf(t.Token),
			ExpiresAt:       t.ExpiresAt,
			HasRefresh:      t.RefreshToken != "",
			LastRefreshedAt: t.LastRefreshedAt,
			MachineCode:     mc,
			ProxyURL:        t.ProxyURL,
		})
	}
	return out
}

// SetAccountProxy 设置/清除某账号的独立出口代理。
func SetAccountProxy(id, proxyURL string) bool {
	return store.Mutate(func(s *store.Shape) bool {
		for i := range s.CursorTokens {
			if s.CursorTokens[i].ID == id {
				s.CursorTokens[i].ProxyURL = strings.TrimSpace(proxyURL)
				return true
			}
		}
		return false
	})
}

// GetCursorTokenByID 按 id 取一条凭证。
func GetCursorTokenByID(id string) (store.CursorTokenEntry, bool) {
	for _, t := range store.Read().CursorTokens {
		if t.ID == id {
			return t, true
		}
	}
	return store.CursorTokenEntry{}, false
}

// RemoveCursorToken 删除一条凭证。
func RemoveCursorToken(id string) bool {
	return store.Mutate(func(s *store.Shape) bool {
		before := len(s.CursorTokens)
		filtered := s.CursorTokens[:0]
		for _, t := range s.CursorTokens {
			if t.ID != id {
				filtered = append(filtered, t)
			}
		}
		s.CursorTokens = filtered
		return len(s.CursorTokens) < before
	})
}

// HasCursorToken 是否已配置至少一条凭证。
func HasCursorToken() bool {
	return len(store.Read().CursorTokens) > 0
}
