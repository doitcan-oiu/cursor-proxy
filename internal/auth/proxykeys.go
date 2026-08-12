package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"cursor-proxy/internal/store"
)

func hashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// ProxyKeyView 对外展示的代理 Key（不含 hash）。
type ProxyKeyView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"createdAt"`
	Disabled   bool   `json:"disabled"`
	LastUsedAt int64  `json:"lastUsedAt"`
}

// CreatedProxyKey 新建 Key 的返回（含仅此一次的明文）。
type CreatedProxyKey struct {
	Entry  ProxyKeyView
	RawKey string
}

// CreateProxyKey 生成一把 sk- 开头的对外代理 Key。
func CreateProxyKey(name string) CreatedProxyKey {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	rawKey := "sk-" + hex.EncodeToString(buf)
	if name == "" {
		name = "unnamed"
	}
	entry := store.ProxyKeyEntry{
		ID:        uuid.NewString(),
		Name:      name,
		Hash:      hashKey(rawKey),
		Prefix:    rawKey[:10],
		CreatedAt: time.Now().UnixMilli(),
	}
	store.Mutate(func(s *store.Shape) any {
		s.ProxyKeys = append(s.ProxyKeys, entry)
		return nil
	})
	return CreatedProxyKey{Entry: toKeyView(entry), RawKey: rawKey}
}

func toKeyView(e store.ProxyKeyEntry) ProxyKeyView {
	return ProxyKeyView{
		ID: e.ID, Name: e.Name, Prefix: e.Prefix, CreatedAt: e.CreatedAt,
		Disabled: e.Disabled, LastUsedAt: e.LastUsedAt,
	}
}

// ListProxyKeys 列出全部 Key 视图。
func ListProxyKeys() []ProxyKeyView {
	keys := store.Read().ProxyKeys
	out := make([]ProxyKeyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyView(k))
	}
	return out
}

// RevokeProxyKey 删除一把 Key。
func RevokeProxyKey(id string) bool {
	return store.Mutate(func(s *store.Shape) bool {
		before := len(s.ProxyKeys)
		filtered := s.ProxyKeys[:0]
		for _, k := range s.ProxyKeys {
			if k.ID != id {
				filtered = append(filtered, k)
			}
		}
		s.ProxyKeys = filtered
		return len(s.ProxyKeys) < before
	})
}

// ValidateProxyKey 校验一把 Key 是否有效；命中则更新 lastUsedAt。
func ValidateProxyKey(rawKey string) bool {
	if rawKey == "" {
		return false
	}
	hash := hashKey(rawKey)
	var matchID string
	for _, k := range store.Read().ProxyKeys {
		if !k.Disabled && subtle.ConstantTimeCompare([]byte(k.Hash), []byte(hash)) == 1 {
			matchID = k.ID
			break
		}
	}
	if matchID == "" {
		return false
	}
	store.Mutate(func(s *store.Shape) any {
		for i := range s.ProxyKeys {
			if s.ProxyKeys[i].ID == matchID {
				s.ProxyKeys[i].LastUsedAt = time.Now().UnixMilli()
			}
		}
		return nil
	})
	return true
}
