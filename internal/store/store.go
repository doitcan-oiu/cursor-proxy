// Package store 负责账号凭证与代理 Key 的持久化。
//
// 相比原 Node 版「每次调用都读磁盘」的做法，这里在内存中缓存整份数据，
// 用读写锁保护并发访问，仅在发生写操作时原子落盘（写临时文件再 rename）。
// 这样读路径（高频，如鉴权、选号）零磁盘 IO，写路径仍然崩溃安全。
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"cursor-proxy/internal/config"
)

// AccountIdentity 每账号持久化的设备指纹，让同一账号在所有请求里表现为一台稳定机器。
type AccountIdentity struct {
	MachineID     string `json:"machineId"`
	MacMachineID  string `json:"macMachineId"`
	DeviceID      string `json:"deviceId"`
	ConfigVersion string `json:"configVersion"`
	ClientVersion string `json:"clientVersion"`
	Timezone      string `json:"timezone"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	OSVersion     string `json:"osVersion"`
}

// CursorTokenEntry 一条 Cursor 账号凭证。
type CursorTokenEntry struct {
	ID              string           `json:"id"`
	Label           string           `json:"label"`
	Token           string           `json:"token"`
	CreatedAt       int64            `json:"createdAt"`
	Identity        *AccountIdentity `json:"identity,omitempty"`
	ProxyURL        string           `json:"proxyUrl,omitempty"`
	RefreshToken    string           `json:"refreshToken,omitempty"`
	ExpiresAt       int64            `json:"expiresAt,omitempty"`
	LastRefreshedAt int64            `json:"lastRefreshedAt,omitempty"`
}

// ProxyKeyEntry 一把对外代理 Key，只存哈希。
type ProxyKeyEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Hash       string `json:"hash"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"createdAt"`
	Disabled   bool   `json:"disabled"`
	LastUsedAt int64  `json:"lastUsedAt"`
}

// Shape 是落盘 JSON 的顶层结构。
type Shape struct {
	CursorTokens []CursorTokenEntry `json:"cursorTokens"`
	ProxyKeys    []ProxyKeyEntry    `json:"proxyKeys"`
}

var (
	mu     sync.RWMutex
	data   *Shape
	loaded bool
)

func filePath() string {
	return filepath.Join(config.Get().DataDir, "store.json")
}

func loadLocked() {
	if loaded {
		return
	}
	data = &Shape{}
	if raw, err := os.ReadFile(filePath()); err == nil {
		_ = json.Unmarshal(raw, data)
	}
	loaded = true
}

func persistLocked() error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := filePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filePath())
}

// Read 以只读方式访问 store 的深拷贝快照，避免调用方持有内部切片。
func Read() Shape {
	mu.RLock()
	defer mu.RUnlock()
	loadLocked()
	out := Shape{
		CursorTokens: make([]CursorTokenEntry, len(data.CursorTokens)),
		ProxyKeys:    make([]ProxyKeyEntry, len(data.ProxyKeys)),
	}
	copy(out.CursorTokens, data.CursorTokens)
	copy(out.ProxyKeys, data.ProxyKeys)
	return out
}

// Mutate 在写锁下对 store 做修改，返回值由回调决定，修改后立即落盘。
func Mutate[T any](fn func(s *Shape) T) T {
	mu.Lock()
	defer mu.Unlock()
	loadLocked()
	result := fn(data)
	_ = persistLocked()
	return result
}
