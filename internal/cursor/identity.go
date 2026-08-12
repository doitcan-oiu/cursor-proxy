// Package cursor 封装与 Cursor 上游交互的全部逻辑：设备指纹、请求头、
// 传统 Chat 端点、现代 Agent 端点、账号池调度、令牌刷新与账号信息查询。
package cursor

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/store"
)

// Context 是一次上游请求所需的账号上下文（含指纹）。
type Context struct {
	ID        string
	Bearer    string
	Identity  store.AccountIdentity
	ClientKey string
	SessionID string
	ProxyURL  string
}

func sha256Hex(input, salt string) string {
	sum := sha256.Sum256([]byte(input + salt))
	return hex.EncodeToString(sum[:])
}

func uuidV5(name string) string {
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(name)).String()
}

// deriveIdentity 从 token 确定性派生设备指纹，保证同一账号恒定同一台「机器」。
func deriveIdentity(bearer string) store.AccountIdentity {
	cfg := config.Get()
	return store.AccountIdentity{
		MachineID:     sha256Hex(bearer, "machineId"),
		MacMachineID:  sha256Hex(bearer, "macMachineId"),
		DeviceID:      uuidV5(bearer + "::device"),
		ConfigVersion: uuidV5(bearer + "::config"),
		ClientVersion: cfg.CursorClientVersion,
		Timezone:      cfg.CursorTimezone,
		OS:            "win32",
		Arch:          "x64",
		OSVersion:     "10.0.22631",
	}
}

// BuildContext 构造账号上下文，指纹由 token 确定性派生；stored 时把指纹写回并读取独立代理。
func BuildContext(id, token string, stored bool) Context {
	bearer := auth.ExtractBearer(token)
	identity := deriveIdentity(bearer)
	proxyURL := ""
	if stored && id != "" {
		proxyURL = store.Mutate(func(s *store.Shape) string {
			for i := range s.CursorTokens {
				if s.CursorTokens[i].ID == id {
					s.CursorTokens[i].Identity = &identity
					return s.CursorTokens[i].ProxyURL
				}
			}
			return ""
		})
	}
	return Context{
		ID:        id,
		Bearer:    bearer,
		Identity:  identity,
		ClientKey: sha256Hex(bearer, ""),
		SessionID: uuidV5(bearer),
		ProxyURL:  proxyURL,
	}
}
