// Package manage 是账号 / 密钥 / 模型 / 测试 / VPN 的统一业务门面。
//
// admin HTTP 路由与管理 REST API 都复用此层，避免逻辑散落到 handler 里。
package manage

import (
	"sync"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/reqlog"
	"cursor-proxy/internal/tools"
	"cursor-proxy/internal/types"
	"cursor-proxy/internal/vpn"
)

// ServerInfo 基本服务信息。
type ServerInfo struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	BaseURL    string `json:"baseUrl"`
	AdminToken string `json:"adminToken"`
}

// GetServerInfo 返回服务信息。
func GetServerInfo() ServerInfo {
	cfg := config.Get()
	return ServerInfo{
		Host:       cfg.Host,
		Port:       cfg.Port,
		BaseURL:    "http://127.0.0.1:" + itoa(cfg.Port) + "/v1",
		AdminToken: cfg.AdminToken,
	}
}

// ListAccounts 列出账号。
func ListAccounts() []auth.TokenView { return auth.ListCursorTokens() }

// ImportDetail 单条导入结果。
type ImportDetail struct {
	Label  string `json:"label"`
	Status string `json:"status"`
}

// ImportResult 批量导入结果。
type ImportResult struct {
	Added      int            `json:"added"`
	Duplicates int            `json:"duplicates"`
	Invalid    int            `json:"invalid"`
	Exchanged  int            `json:"exchanged"`
	Details    []ImportDetail `json:"details"`
}

// ImportAccounts 批量导入，web token 自动交换成 session token。
func ImportAccounts(text string) ImportResult {
	items := auth.ParseTokensText(text)
	res := ImportResult{Details: []ImportDetail{}}
	for _, item := range items {
		raw := item.Token
		if raw == "" {
			continue
		}
		token := raw
		refreshToken := ""
		if auth.IsWebToken(raw) {
			r, err := auth.ExchangeWorkosCookieFull(raw)
			if err != nil {
				res.Invalid++
				res.Details = append(res.Details, ImportDetail{Label: labelOr(item.Label, raw), Status: "web交换失败: " + err.Error()})
				continue
			}
			token = r.Token
			refreshToken = r.RefreshToken
			res.Exchanged++
		}
		out := auth.AddCursorTokensBatch([]auth.BatchImportItem{{Token: token, Label: item.Label, RefreshToken: refreshToken}})
		switch {
		case len(out.Added) > 0:
			res.Added++
			res.Details = append(res.Details, ImportDetail{Label: out.Added[0].Label, Status: "added"})
		case len(out.Duplicates) > 0:
			res.Duplicates++
			res.Details = append(res.Details, ImportDetail{Label: labelOr(item.Label, raw), Status: "duplicate"})
		default:
			res.Invalid++
			res.Details = append(res.Details, ImportDetail{Label: labelOr(item.Label, raw), Status: "invalid"})
		}
	}
	return res
}

// AddAccountResult 添加单账号结果。
type AddAccountResult struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Exchanged bool   `json:"exchanged"`
}

// AddAccount 添加单个账号。
func AddAccount(token, label string) (AddAccountResult, error) {
	t := token
	refreshToken := ""
	exchanged := false
	if auth.IsWebToken(t) {
		r, err := auth.ExchangeWorkosCookieFull(t)
		if err != nil {
			return AddAccountResult{}, err
		}
		t = r.Token
		refreshToken = r.RefreshToken
		exchanged = true
	}
	entry := auth.AddCursorToken(t, label, refreshToken)
	return AddAccountResult{ID: entry.ID, Label: entry.Label, Exchanged: exchanged}, nil
}

// RemoveAccount 删除账号。
func RemoveAccount(id string) bool { return auth.RemoveCursorToken(id) }

// SetProxy 设置账号独立出口代理。
func SetProxy(id, proxyURL string) bool { return auth.SetAccountProxy(id, proxyURL) }

// Logs 请求日志与统计。
type Logs struct {
	Entries []reqlog.Entry `json:"entries"`
	Stats   reqlog.Stats   `json:"stats"`
}

// GetLogs 请求/流量日志。
func GetLogs(sinceID int64) Logs {
	return Logs{Entries: reqlog.List(sinceID), Stats: reqlog.Snapshot()}
}

// ClearLogs 清空日志。
func ClearLogs() bool {
	reqlog.Clear()
	return true
}

// AccountCheck 连接测试结果。
type AccountCheck struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	OK          bool     `json:"ok"`
	Detail      string   `json:"detail"`
	Plan        string   `json:"plan,omitempty"`
	UsedPercent *float64 `json:"usedPercent,omitempty"`
	Exhausted   *bool    `json:"exhausted,omitempty"`
}

// CheckAccount 用 agent 端点拉模型 + 查套餐/用量。
func CheckAccount(id string) AccountCheck {
	entry, ok := auth.GetCursorTokenByID(id)
	if !ok {
		return AccountCheck{ID: id, Label: "?", OK: false, Detail: "not found"}
	}
	models, err := cursor.GetUsableModels(entry.Token, entry.ProxyURL)
	if err != nil {
		status := 0
		if aerr, ok := err.(*cursor.AgentUpstreamError); ok {
			status = aerr.Status
		}
		detail := err.Error()
		if status != 0 {
			detail = itoa(status) + " " + detail
		}
		return AccountCheck{ID: id, Label: entry.Label, OK: false, Detail: detail}
	}
	res := AccountCheck{ID: id, Label: entry.Label, OK: true, Detail: itoa(len(models)) + " models"}
	if plan, err := cursor.FetchAccountPlan(entry.Token); err == nil {
		res.Plan = plan
	}
	if u, err := cursor.FetchAccountUsage(entry.Token); err == nil {
		up := u.TotalPercentUsed
		ex := u.TotalPercentUsed >= 100
		res.UsedPercent = &up
		res.Exhausted = &ex
	}
	return res
}

// CheckAllAccounts 限并发对全部账号验号。
func CheckAllAccounts() []AccountCheck {
	views := auth.ListCursorTokens()
	out := make([]AccountCheck, len(views))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, v := range views {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = CheckAccount(id)
		}(i, v.ID)
	}
	wg.Wait()
	return out
}

// ListKeys 列出代理 Key。
func ListKeys() []auth.ProxyKeyView { return auth.ListProxyKeys() }

// CreateKey 新建代理 Key。
func CreateKey(name string) auth.CreatedProxyKey { return auth.CreateProxyKey(name) }

// RevokeKey 删除代理 Key。
func RevokeKey(id string) bool { return auth.RevokeProxyKey(id) }

// ListModels 用任一有效账号列出可用模型。
func ListModels() []cursor.AgentModel {
	for _, v := range auth.ListCursorTokens() {
		entry, ok := auth.GetCursorTokenByID(v.ID)
		if !ok {
			continue
		}
		if models, err := cursor.GetUsableModels(entry.Token, entry.ProxyURL); err == nil {
			return models
		}
	}
	return []cursor.AgentModel{}
}

// askMode 调试台永远是纯对话，除非用户显式关掉 ASK 模式。
func askMode() proto.Mode {
	if config.Get().AskMode {
		return proto.ModeAsk
	}
	return proto.ModeAgent
}

// TestChatResult 测试对话结果。
type TestChatResult struct {
	OK        bool   `json:"ok"`
	Text      string `json:"text"`
	Reasoning string `json:"reasoning"`
	Error     string `json:"error,omitempty"`
}

// TestChatRequest 是调试台发起一次测试对话的入参。
type TestChatRequest struct {
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	AccountID string `json:"accountId"`
	// Images 是 data: URL 形式的图片，用于测试多模态。
	Images []string `json:"images,omitempty"`
}

// messages 把入参组装成一条用户消息，解不开的图片直接跳过。
func (r TestChatRequest) messages() ([]types.Message, string) {
	msg := types.Message{Role: "user", Content: r.Prompt}
	var skipped string
	for _, u := range r.Images {
		img, err := types.DecodeImageURL(u)
		if err != nil {
			skipped = err.Error()
			continue
		}
		msg.Images = append(msg.Images, img)
	}
	return []types.Message{msg}, skipped
}

// TestChat 用指定账号（或自动选一个）跑一句。
func TestChat(req TestChatRequest) TestChatResult {
	entry, ok := pickAccount(req.AccountID)
	if !ok {
		return TestChatResult{Error: "no account"}
	}
	msgs, skipped := req.messages()
	if skipped != "" {
		return TestChatResult{Error: "图片无法解析: " + skipped}
	}
	ctx := cursor.BuildContext(entry.ID, entry.Token, true)
	stream, err := cursor.StreamAgent(msgs, cursor.ResolveUpstreamModel(req.Model), ctx.Bearer, ctx.ProxyURL, askMode())
	if err != nil {
		return TestChatResult{Error: err.Error()}
	}
	defer stream.Close()
	var text, reasoning, errMsg string
	for ev := range stream.Events {
		switch ev.Kind {
		case cursor.EventDelta:
			text += ev.Text
			reasoning += ev.Thinking
		case cursor.EventToolCall:
			// 调试台没有工具可执行，把调用内容还原成正文，
			// 否则「写一段 SVG」这类请求会得到空回复。
			text += toolText(ev.Tool)
		case cursor.EventError:
			errMsg = ev.Message
		}
	}
	return TestChatResult{OK: text != "", Text: text, Reasoning: reasoning, Error: errMsg}
}

// TestDelta 流式测试对话的单个增量。
type TestDelta struct {
	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	Error     string `json:"error,omitempty"`
}

// TestChatStream 流式测试对话：每个增量通过 onDelta 实时回调（供 WebUI SSE 推送）。
// 返回是否产出过正文。
func TestChatStream(req TestChatRequest, onDelta func(TestDelta)) bool {
	entry, ok := pickAccount(req.AccountID)
	if !ok {
		onDelta(TestDelta{Error: "no account"})
		return false
	}
	msgs, skipped := req.messages()
	if skipped != "" {
		onDelta(TestDelta{Error: "图片无法解析: " + skipped})
		return false
	}
	ctx := cursor.BuildContext(entry.ID, entry.Token, true)
	stream, err := cursor.StreamAgent(msgs,
		cursor.ResolveUpstreamModel(req.Model), ctx.Bearer, ctx.ProxyURL, askMode())
	if err != nil {
		onDelta(TestDelta{Error: err.Error()})
		return false
	}
	defer stream.Close()

	gotText := false
	emit := func(s string) {
		if s == "" {
			return
		}
		gotText = true
		onDelta(TestDelta{Content: s})
	}
	// 调试台永远是纯对话，写文件的内容直接当正文流式吐出
	live := tools.NewLiveWriter(true)

	for ev := range stream.Events {
		switch ev.Kind {
		case cursor.EventDelta:
			if ev.Text != "" {
				gotText = true
				emit(live.Interrupt())
			}
			onDelta(TestDelta{Content: ev.Text, Reasoning: ev.Thinking})
		case cursor.EventToolInputDelta:
			n := nativeOf(ev.Tool)
			emit(live.Push(&n, ev.Text))
		case cursor.EventToolCall:
			n2 := nativeOf(ev.Tool)
			if s, handled := live.Finish(&n2); handled {
				emit(s)
				continue
			}
			emit(toolText(ev.Tool))
		case cursor.EventError:
			onDelta(TestDelta{Error: ev.Message})
		}
	}
	emit(live.Interrupt())
	return gotText
}

// toolText 把上游的内置工具调用还原成可读正文（调试台没有工具可执行）。
func toolText(c *cursor.NativeToolCall) string {
	if c == nil {
		return ""
	}
	return tools.NativeToText(nativeOf(c))
}

// nativeOf 把 cursor 层的内置调用转成 tools 层的形态。
func nativeOf(c *cursor.NativeToolCall) tools.Native {
	if c == nil {
		return tools.Native{}
	}
	kind := tools.KindUnknown
	switch c.Kind {
	case cursor.ToolWriteFile:
		kind = tools.KindWriteFile
	case cursor.ToolReadFile:
		kind = tools.KindReadFile
	case cursor.ToolRunTerminal:
		kind = tools.KindRunTerminal
	case cursor.ToolSearchFiles:
		kind = tools.KindSearchFiles
	case cursor.ToolListFiles:
		kind = tools.KindListFiles
	case cursor.ToolDeleteFile:
		kind = tools.KindDeleteFile
	case cursor.ToolFetchURL:
		kind = tools.KindFetchURL
	case cursor.ToolTask:
		kind = tools.KindTask
	case cursor.ToolTodoWrite:
		kind = tools.KindTodoWrite
	}
	return tools.Native{
		ID: c.ID, Kind: kind, Path: c.Path, Command: c.Command, Pattern: c.Pattern,
		Content: c.Content, Prompt: c.Prompt, URL: c.URL,
		Description: c.Description, Field: c.Field,
	}
}

// AccountsHealth 返回账号健康快照。
func AccountsHealth() []cursor.AccountHealthView { return cursor.HealthSnapshot() }

// ---- VPN ----

// VPNGetStatus 返回 VPN 状态。
func VPNGetStatus() vpn.Status { return vpn.GetStatus() }

// VPNSetSub 设置订阅地址。
func VPNSetSub(url string) bool { vpn.SetSubURL(url); return true }

// VPNSetMode 设置分组策略。
func VPNSetMode(mode string) bool { vpn.SetMode(vpn.Mode(mode)); return true }

// VPNEnable 启用 VPN。
func VPNEnable(subURL, mode string) error { return vpn.Start(subURL, vpn.Mode(mode), nil) }

// VPNDisable 停用 VPN。
func VPNDisable() error { return vpn.Stop() }

// VPNInstall 安装内核。
func VPNInstall() (bool, error) {
	if !vpn.IsBinaryInstalled() {
		if err := vpn.DownloadBinary(nil); err != nil {
			return false, err
		}
	}
	return vpn.IsBinaryInstalled(), nil
}

// VPNTest 测速并返回状态。
func VPNTest() vpn.Status {
	vpn.TestDelays()
	return vpn.GetStatus()
}

// VPNSwitch 切换节点。
func VPNSwitch(name string) error { return vpn.SwitchNode(name) }

// ---- helpers ----

func pickAccount(accountID string) (entry accountEntry, ok bool) {
	if accountID != "" {
		if e, found := auth.GetCursorTokenByID(accountID); found {
			return accountEntry{ID: e.ID, Token: e.Token, ProxyURL: e.ProxyURL}, true
		}
		return accountEntry{}, false
	}
	views := auth.ListCursorTokens()
	if len(views) == 0 {
		return accountEntry{}, false
	}
	if e, found := auth.GetCursorTokenByID(views[0].ID); found {
		return accountEntry{ID: e.ID, Token: e.Token, ProxyURL: e.ProxyURL}, true
	}
	return accountEntry{}, false
}

type accountEntry struct {
	ID       string
	Token    string
	ProxyURL string
}

func labelOr(label, raw string) string {
	if label != "" {
		return label
	}
	if len(raw) > 16 {
		return raw[:16]
	}
	return raw
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
