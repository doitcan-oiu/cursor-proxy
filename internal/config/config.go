// Package config 集中管理运行期配置。所有可调项都来自环境变量，缺省值对齐原 Node 版本。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Antiban 汇总账号调度 / 防封相关旋钮。
type Antiban struct {
	MinIntervalMs          int
	MaxConcurrency         int
	JitterMs               int
	Cooldown429Ms          int
	QuarantineMs           int
	MaxConsecutiveFailures int
	MaxAttempts            int
	AcquireTimeoutMs       int
}

// Config 是进程级不可变配置快照。
type Config struct {
	ProjectRoot string
	DataDir     string
	Host        string
	Port        int

	AdminToken          string
	AdminTokenGenerated bool

	CursorClientVersion string
	CursorTimezone      string
	HTTPProxy           string
	CursorBackendURL    string

	AgentClientVersion string
	AgentIdleMs        int
	// AgentFinishIdleMs 是上游回写会话记录（本轮生成结束）之后的收尾等待。
	// 上游永不关闭连接、只每 10s 发心跳，靠这个短窗口收残余帧后立刻结束。
	AgentFinishIdleMs int
	AgentHardCapMs    int
	AgentFirstTokenMs int

	LoginHeadless     bool
	LoginTimeoutMs    int
	MailCodeTimeoutMs int
	IMAPHost          string
	IMAPPort          int

	// TokenizerMode 决定 usage 的 token 如何计算：
	// "bpe"（默认）用内嵌词表精确分词；"estimate" 关闭分词器改用启发式，省约 7MB 内存。
	TokenizerMode string

	Antiban Antiban
}

var (
	once   sync.Once
	cached *Config
)

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func projectRoot() string {
	// 可执行文件同级目录优先；否则回退到工作目录。
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

// Get 惰性构造并缓存全局配置。
func Get() *Config {
	once.Do(func() {
		root := projectRoot()
		dataDir := envStr("DATA_DIR", ".data")
		if !filepath.IsAbs(dataDir) {
			dataDir = filepath.Join(root, dataDir)
		}

		token, generated := resolveAdminToken(dataDir)

		cached = &Config{
			ProjectRoot:         root,
			DataDir:             dataDir,
			Host:                envStr("HOST", "0.0.0.0"),
			Port:                envInt("PORT", 3100),
			AdminToken:          token,
			AdminTokenGenerated: generated,
			CursorClientVersion: envStr("CURSOR_CLIENT_VERSION", "3.12.10"),
			CursorTimezone:      envStr("CURSOR_TIMEZONE", "Asia/Shanghai"),
			HTTPProxy:           envStr("CURSOR_HTTP_PROXY", ""),
			CursorBackendURL:    "https://api2.cursor.sh",
			AgentClientVersion:  envStr("CURSOR_AGENT_CLIENT_VERSION", "cli-2026.01.28-fd13201"),
			AgentIdleMs:         envInt("AGENT_IDLE_MS", 6000),
			AgentFinishIdleMs:   envInt("AGENT_FINISH_IDLE_MS", 400),
			AgentHardCapMs:      envInt("AGENT_HARD_CAP_MS", 180000),
			AgentFirstTokenMs:   envInt("AGENT_FIRST_TOKEN_MS", 60000),
			LoginHeadless:       envStr("CURSOR_LOGIN_HEADLESS", "true") != "false",
			LoginTimeoutMs:      envInt("ACCOUNT_LOGIN_TIMEOUT_MS", 180000),
			MailCodeTimeoutMs:   envInt("MAIL_CODE_TIMEOUT_MS", 120000),
			IMAPHost:            envStr("IMAP_HOST", ""),
			IMAPPort:            envInt("IMAP_PORT", 993),
			TokenizerMode:       envStr("TOKENIZER", "bpe"),
			Antiban: Antiban{
				MinIntervalMs:          envInt("ACCOUNT_MIN_INTERVAL_MS", 0),
				MaxConcurrency:         envInt("ACCOUNT_MAX_CONCURRENCY", 64),
				JitterMs:               envInt("ACCOUNT_JITTER_MS", 0),
				Cooldown429Ms:          envInt("ACCOUNT_COOLDOWN_429_MS", 60000),
				QuarantineMs:           envInt("ACCOUNT_QUARANTINE_MS", 1800000),
				MaxConsecutiveFailures: envInt("ACCOUNT_MAX_FAILURES", 3),
				MaxAttempts:            envInt("PROXY_MAX_ATTEMPTS", 3),
				AcquireTimeoutMs:       envInt("ACCOUNT_ACQUIRE_TIMEOUT_MS", 30000),
			},
		}
	})
	return cached
}

// EnsureDataDir 保证数据目录存在。
func EnsureDataDir() error {
	return os.MkdirAll(Get().DataDir, 0o755)
}

// ADMIN_TOKEN 保护 /admin 与 /cursor 登录接口；未显式配置则生成并落盘。
func resolveAdminToken(dataDir string) (string, bool) {
	if fromEnv := os.Getenv("ADMIN_TOKEN"); fromEnv != "" {
		return fromEnv, false
	}
	_ = os.MkdirAll(dataDir, 0o755)
	tokenFile := filepath.Join(dataDir, "admin-token")
	if data, err := os.ReadFile(tokenFile); err == nil {
		if s := string(data); s != "" {
			return s, false
		}
	}
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token := "admin_" + hex.EncodeToString(buf)
	_ = os.WriteFile(tokenFile, []byte(token), 0o600)
	return token, true
}
