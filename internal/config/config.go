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
	MinIntervalMs  int
	MaxConcurrency int
	JitterMs       int
	// Cooldown429Ms 被限流后暂停使用该账号的时长；0 表示不冷却（默认）。
	Cooldown429Ms int
	// QuarantineMs 连续失败后隔离该账号的时长；0 表示不隔离（默认）。
	//
	// 默认关闭是因为「失败」这个信号并不可靠：上游抖动、区域限制、模型名无效
	// 都会被算成账号故障，攒够几次就把一个其实好用的账号下线半小时，
	// 池子小的时候直接变成「无可用账号」。现在改为只降权不下线——
	// 最近失败过的账号排到最后，但仍然可选，成功一次立刻恢复优先级。
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
	// AgentHardCapMs 是单轮对话的时长兜底，防的是「一直吐、永远不停」的失控流。
	// 卡住的流由 AgentIdleMs 负责，所以这里给得很宽：早期默认 180s，
	// 结果长文写到一半（约 1.5 万字）就被静默掐断，还报成正常结束。
	AgentHardCapMs    int
	AgentFirstTokenMs int

	LoginHeadless     bool
	LoginTimeoutMs    int
	MailCodeTimeoutMs int
	IMAPHost          string
	IMAPPort          int

	// AskMode 决定纯对话（客户端没声明工具）时是否让上游走 ASK 模式。
	//
	// 默认关闭。ASK 不是通用问答模式，而是 Cursor 里「就代码库提问」的模式：
	// 上游会告诉模型「你只能回答代码和代码库相关的问题」。对通用代理来说这是负担——
	// 实测模型会拿它当拒绝理由（「I'm in Ask mode, so I can't generate creative
	// writing, roleplay content…」），而且这句话一旦进了对话历史就会被反复回放，
	// 之后每轮都照着拒绝。
	//
	// 它唯一的好处是纯对话时不走「写文件再还原」那条路，但那条路现在已经能正常
	// 流式输出了，收益有限。确定只用来问代码的话可以设 ASK_MODE=on 打开。
	AskMode bool

	// TokenizerMode 决定 usage 的 token 如何计算：
	// "bpe"（默认）用内嵌词表精确分词；"estimate" 关闭分词器改用启发式，省约 7MB 内存。
	TokenizerMode string

	Antiban Antiban
}

var (
	// mu 只保护 once/cached 的替换，正常路径上 Get 仍走 sync.Once 无锁快路径。
	mu     sync.Mutex
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

// Reset 丢弃缓存，下次 Get 会重新读环境变量。仅供测试使用。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	once = sync.Once{}
	cached = nil
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
			AgentHardCapMs:      envInt("AGENT_HARD_CAP_MS", 1800000),
			AgentFirstTokenMs:   envInt("AGENT_FIRST_TOKEN_MS", 60000),
			LoginHeadless:       envStr("CURSOR_LOGIN_HEADLESS", "true") != "false",
			LoginTimeoutMs:      envInt("ACCOUNT_LOGIN_TIMEOUT_MS", 180000),
			MailCodeTimeoutMs:   envInt("MAIL_CODE_TIMEOUT_MS", 120000),
			IMAPHost:            envStr("IMAP_HOST", ""),
			IMAPPort:            envInt("IMAP_PORT", 993),
			AskMode:             envStr("ASK_MODE", "off") == "on",
			TokenizerMode:       envStr("TOKENIZER", "bpe"),
			Antiban: Antiban{
				MinIntervalMs:          envInt("ACCOUNT_MIN_INTERVAL_MS", 0),
				MaxConcurrency:         envInt("ACCOUNT_MAX_CONCURRENCY", 64),
				JitterMs:               envInt("ACCOUNT_JITTER_MS", 0),
				Cooldown429Ms:          envInt("ACCOUNT_COOLDOWN_429_MS", 0),
				QuarantineMs:           envInt("ACCOUNT_QUARANTINE_MS", 0),
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
