package cursor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/toollog"
	"cursor-proxy/internal/types"
)

const agentBase = "https://agentn.api5.cursor.sh"

func runURL() string         { return agentBase + "/agent.v1.AgentService/Run" }
func agentModelsURL() string { return agentBase + "/agent.v1.AgentService/GetUsableModels" }

// AgentUpstreamError agent 端点的上游错误。
type AgentUpstreamError struct {
	Status int
	Msg    string
}

func (e *AgentUpstreamError) Error() string { return e.Msg }

// NewAgentUpstreamError 构造 agent 上游错误。
func NewAgentUpstreamError(status int, msg string) *AgentUpstreamError {
	return &AgentUpstreamError{Status: status, Msg: msg}
}

func agentHeaders(bearer, contentType string) http.Header {
	requestID := uuidV5(bearer + time.Now().String())[:36]
	h := http.Header{}
	h.Set("authorization", "Bearer "+bearer)
	h.Set("connect-protocol-version", "1")
	h.Set("content-type", contentType)
	h.Set("x-cursor-client-type", "cli")
	h.Set("x-cursor-client-version", config.Get().AgentClientVersion)
	h.Set("x-ghost-mode", "true")
	h.Set("x-request-id", requestID)
	h.Set("x-amzn-trace-id", "Root="+requestID)
	return h
}

func connectFrame(payload []byte) []byte {
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	return append(header, payload...)
}

// AgentModel agent 端点返回的模型。
type AgentModel struct {
	ID          string   `json:"id"`
	Aliases     []string `json:"aliases"`
	DisplayName string   `json:"displayName,omitempty"`
}

// GetUsableModels 拉取当前账号可用模型（agent 端点）。
func GetUsableModels(token, proxyURL string) ([]AgentModel, error) {
	req, _ := http.NewRequest(http.MethodPost, agentModelsURL(), strings.NewReader("{}"))
	req.Header = agentHeaders(auth.ExtractBearer(token), "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := httpx.Client(proxyURL).Do(req)
	if err != nil {
		return nil, NewAgentUpstreamError(0, err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, NewAgentUpstreamError(resp.StatusCode, snippet(raw, 300))
	}
	var data struct {
		Models []struct {
			ModelID     string   `json:"modelId"`
			Aliases     []string `json:"aliases"`
			DisplayName string   `json:"displayName"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil, NewAgentUpstreamError(resp.StatusCode, "bad json")
	}
	out := make([]AgentModel, 0, len(data.Models))
	for _, m := range data.Models {
		if m.ModelID == "" {
			continue
		}
		// aliases 为空时也要序列化成 []，否则前端拿到 null 会在 .includes 上炸
		aliases := m.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		out = append(out, AgentModel{ID: m.ModelID, Aliases: aliases, DisplayName: m.DisplayName})
	}
	return out, nil
}

func parseErrorFrame(utf string) string {
	var j struct {
		Error *struct {
			Code    string `json:"code"`
			Details []struct {
				Debug struct {
					Error   string `json:"error"`
					Details struct {
						Title  string `json:"title"`
						Detail string `json:"detail"`
					} `json:"details"`
				} `json:"debug"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(utf), &j) != nil || j.Error == nil {
		return ""
	}
	if len(j.Error.Details) > 0 {
		d := j.Error.Details[0].Debug
		switch {
		case d.Details.Detail != "":
			return d.Details.Detail
		case d.Details.Title != "":
			return d.Details.Title
		case d.Error != "":
			return d.Error
		}
	}
	if j.Error.Code != "" {
		return j.Error.Code
	}
	return snippet([]byte(utf), 200)
}

// AgentStream 是一个可取消的对话流。
type AgentStream struct {
	Events <-chan StreamEvent
	cancel context.CancelFunc
}

// Close 取消流并释放底层连接。
func (s *AgentStream) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// hardCapFor 给出单轮对话的时长兜底，测试里会替换成很短的值来验证截断路径。
var hardCapFor = func() time.Duration {
	return time.Duration(config.Get().AgentHardCapMs) * time.Millisecond
}

// StreamAgent 向 agentn.api5 发起 Run 流式对话，返回事件流。
// 初始请求失败返回 error；流内错误以 EventError 事件下发。
//
// 收尾优先看 Connect 的 end-of-stream 帧，拿到即立刻结束；
// 空闲超时只作为上游不发该帧时的兜底。
//
// mode 决定上游按 agent 还是纯问答行事，见 AskMode / AgentMode。
func StreamAgent(messages []types.Message, modelID, token, proxyURL string, mode proto.Mode) (*AgentStream, error) {
	bearer := auth.ExtractBearer(token)
	body := connectFrame(proto.EncodeAgentRequest(messages, modelID, mode))

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, runURL(), strings.NewReader(string(body)))
	req.Header = agentHeaders(bearer, "application/connect+proto")

	resp, err := httpx.Client(proxyURL).Do(req)
	if err != nil {
		cancel()
		return nil, NewAgentUpstreamError(0, err.Error())
	}
	if resp.StatusCode != http.StatusOK || resp.Body == nil {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		msg := snippet(raw, 300)
		if msg == "" {
			msg = resp.Status
		}
		return nil, NewAgentUpstreamError(resp.StatusCode, msg)
	}

	events := make(chan StreamEvent, 32)
	go pumpAgentStream(ctx, resp.Body, events, modelID)
	return &AgentStream{Events: events, cancel: cancel}, nil
}

func pumpAgentStream(ctx context.Context, body io.ReadCloser, events chan<- StreamEvent, model string) {
	defer close(events)
	defer body.Close()

	cfg := config.Get()
	idle := time.Duration(cfg.AgentIdleMs) * time.Millisecond
	finishIdle := time.Duration(cfg.AgentFinishIdleMs) * time.Millisecond
	hardCap := hardCapFor()
	firstToken := time.Duration(cfg.AgentFirstTokenMs) * time.Millisecond
	start := time.Now()

	// 独立 goroutine 读取网络字节，主循环 select 实现空闲/超时判定。
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 16*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				select {
				case chunks <- chunk{data: cp}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case chunks <- chunk{err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	var buffer []byte
	state := agentStreamState{model: model}

	send := func(ev StreamEvent) bool {
		select {
		case events <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	timer := time.NewTimer(idle)
	defer timer.Stop()

	for {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		// 一旦上游回写了会话记录（本轮结束），只再留一个很短的收尾窗口收残余帧，
		// 而不是干等整个 idle。上游永不关连接、只发心跳，这是避免尾部空等的关键。
		wait := idle
		if state.turnComplete {
			wait = finishIdle
		}
		timer.Reset(wait)

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if state.turnComplete || state.gotContent || state.sawToolCall {
				state.finish(send, false)
				return
			}
			if time.Since(start) > firstToken {
				state.errored = true
				send(StreamEvent{Kind: EventError, Message: fmt.Sprintf(
					"上游 %ds 未返回任何内容：出口节点可能不通或该模型在当前出口区域不可用。请在界面切换 VPN 节点后重试，或改用 auto 等可用模型。",
					cfg.AgentFirstTokenMs/1000)})
				return
			}
			continue
		case ch := <-chunks:
			if ch.err != nil {
				if !state.errored && ch.err != io.EOF && !state.gotContent && !state.turnComplete {
					send(StreamEvent{Kind: EventError, Message: ch.err.Error()})
					state.errored = true
				} else {
					state.finish(send, false)
				}
				return
			}
			// 时长上限只是兜底，防的是「一直吐、永远不停」的失控流。
			// 卡住的流由 idle 超时（默认 6s）负责，不归这里管——所以这个值可以给得很宽，
			// 掐断正在正常输出的长回答比让它多跑几分钟糟糕得多。
			if time.Since(start) > hardCap {
				log.Printf("[agent] 达到 %s 时长上限，本轮被截断（已输出 %d 字节）",
					hardCap, state.textBytes)
				state.finish(send, true)
				return
			}
			buffer = append(buffer, ch.data...)
			if endOfStream := state.process(&buffer, send); endOfStream {
				state.finish(send, false)
				return
			}
		}
	}
}

// 上游帧的顶层字段号（逆向自 agentn.api5 的实际报文）。
const (
	fieldStreamMessage = 1  // 流式增量
	fieldClientAction  = 2  // 要求客户端执行的动作（内置工具调用）
	fieldConversation  = 4  // 会话记录回写
	fieldHeartbeat     = 13 // StreamMessage 内的心跳（约 10s 一次）
)

// StreamMessage 里与工具调用相关的字段。
const (
	smToolCallDone     = 2  // 调用完成：{ 1: 调用 id, 2: 参数容器, 3: 消息 id }
	smToolCallProgress = 7  // 调用进行中：结构同上，参数尚不完整
	smToolInputDelta   = 15 // 参数流式片段：{ 1: 调用 id, 2{3{1: 文本片段}} }
)

// finish 统一收尾：先把「宣告了却没完成」的工具调用交代清楚，再结束本轮。
//
// 上游偶尔会发一个「进行中」帧宣告要用某工具，然后既不发参数也不发完成帧
// （实测 web_search 就是这样，参数长度 0，两次都没有下文）。此时客户端只会
// 收到模型那句「我这就去搜索…」，然后流就正常结束了——看起来像模型自己
// 说完了，其实是这一轮没做完。宁可多一句说明，也不要静默。
func (s *agentStreamState) finish(send func(StreamEvent) bool, truncated bool) {
	if s.errored {
		return
	}
	for id, kind := range s.announced {
		if s.emitted[id] {
			continue
		}
		name := string(kind)
		if name == "" {
			name = "未知"
		}
		log.Printf("[cursor] 上游宣告了工具 %s 却没有下发参数，本轮可能未完成", name)
		if !send(StreamEvent{Kind: EventDelta, Text: fmt.Sprintf(
			"\n\n（上游想调用内置工具 %s，但没有给出参数，本轮就此结束。"+
				"该工具可能在当前账号或模型下不可用。）", name)}) {
			return
		}
	}
	send(StreamEvent{Kind: EventEnd, Truncated: truncated})
}

// parseToolProgress 从「进行中」帧里取出调用 id、工具类型与已知路径。
// 参数还没发完，但类型和路径先到，足以判断后续片段要不要流式输出。
func parseToolProgress(sm map[int][]proto.Field) (id string, kind NativeToolKind, path string, ok bool) {
	body := proto.FirstBytes(sm, smToolCallProgress)
	if body == nil {
		return "", "", "", false
	}
	f := proto.Decode(body)
	id = proto.FirstString(f, 1)
	wrapper := proto.FirstBytes(f, 2)
	if id == "" || wrapper == nil {
		return "", "", "", false
	}
	w := proto.Decode(wrapper)
	for field, kindOf := range toolKinds {
		if body := proto.FirstBytes(w, field); body != nil {
			if inner := proto.FirstBytes(proto.Decode(body), 1); inner != nil {
				path = proto.FirstString(proto.Decode(inner), 1)
			}
			return id, kindOf, path, true
		}
	}
	return id, "", "", true
}

// parseToolInputDelta 取出参数的流式文本片段。
func parseToolInputDelta(sm map[int][]proto.Field) (id, text string, ok bool) {
	body := proto.FirstBytes(sm, smToolInputDelta)
	if body == nil {
		return "", "", false
	}
	f := proto.Decode(body)
	id = proto.FirstString(f, 1)
	wrapper := proto.FirstBytes(f, 2)
	if id == "" || wrapper == nil {
		return "", "", false
	}
	chunk := proto.FirstBytes(proto.Decode(wrapper), 3)
	if chunk == nil {
		return "", "", false
	}
	return id, proto.FirstString(proto.Decode(chunk), 1), true
}

// 参数容器内按「哪个字段被设置」区分工具类型，其值再套一层字段 1 才是真正的参数。
// 例：2{12{1{1:"/a.py" 6:"内容"}}} 表示写文件。
const (
	toolRunTerminal  = 1  // ShellArgs        { 1: 命令, 2: 工作目录, 15: 说明 }
	toolDeleteFile   = 3  // DeleteArgs       { 1: 路径 }
	toolGlob         = 4  // GlobToolArgs     { 1: 起始目录, 2: 文件名模式 }
	toolSearchFiles  = 5  // GrepArgs         { 1: 正则, 2: 路径, 3: glob, 4: 输出模式 }
	toolReadFile     = 8  // ReadArgs         { 1: 路径, 4: 起始行, 5: 行数 }
	toolUpdateTodos  = 9  // UpdateTodosArgs  { 1: repeated TodoItem, 2: 是否合并 }
	toolReadTodos    = 10 // ReadTodosArgs
	toolWriteFile    = 12 // EditArgs         { 1: 路径, 6: 流式内容 }
	toolListFiles    = 13 // LsArgs           { 1: 路径, 2: 忽略模式 }
	toolReadLints    = 14 // ReadLintsArgs
	toolSemSearch    = 16 // SemSearchArgs
	toolWebSearch    = 18 // WebSearchArgs    { 1: 搜索词 }
	toolTask         = 19 // TaskArgs         { 1: 描述, 2: 提示词, 4: 模型 }
	toolAskQuestion  = 23 // AskQuestionArgs  { 1: 标题, 2: repeated 问题 }
	toolFetch        = 24 // FetchArgs        { 1: URL }
	toolGenerateImg  = 28 // GenerateImageArgs{ 1: 描述, 2: 文件路径 }
	toolFetchURL     = 37 // WebFetchArgs     { 1: URL }
	toolAwait        = 42 // AwaitArgs        { 1: 任务 id, 2: 等待毫秒, 3: 正则 }
	toolSwitchMode   = 25 // SwitchModeArgs
	toolCreatePlan   = 17 // CreatePlanArgs
	toolMCP          = 15 // McpArgs
	toolReportBug    = 45 // ReportBugArgs
	toolSearchConvos = 69 // SearchConversationsArgs
)

// toolNames 是参数容器 agent.v1.ToolCall 的字段表，抄自 Cursor 客户端自带的
// protobuf 描述（cursor-local-agent-runtime/dist/main.js），不是靠抓帧猜的。
//
// 覆盖全部字段而不只是我们会解析参数的那些：认不出参数没关系，
// 但至少要能在日志与「未识别工具」页面里报出这个工具叫什么，
// 而不是只给一个光秃秃的字段号。
var toolNames = map[int]string{
	1: "shell", 3: "delete", 4: "glob", 5: "grep", 8: "read",
	9: "update_todos", 10: "read_todos", 12: "edit", 13: "ls",
	14: "read_lints", 15: "mcp", 16: "sem_search", 17: "create_plan",
	18: "web_search", 19: "task", 20: "list_mcp_resources",
	21: "read_mcp_resource", 22: "apply_agent_diff", 23: "ask_question",
	24: "fetch", 25: "switch_mode", 28: "generate_image", 29: "record_screen",
	30: "computer_use", 31: "write_shell_stdin", 32: "reflect",
	33: "setup_vm_environment", 34: "truncated", 35: "start_grind_execution",
	36: "start_grind_planning", 37: "web_fetch", 38: "report_bugfix_results",
	39: "ai_attribution", 40: "pr_management", 41: "mcp_auth", 42: "await",
	43: "blame_by_file_path", 44: "get_mcp_tools", 45: "report_bug",
	46: "set_active_branch", 48: "communicate_update", 49: "send_final_summary",
	50: "update_pr_code_tour", 51: "replace_env", 52: "edit_pr_labels",
	53: "record_ci_investigation_findings", 55: "send_message",
	56: "fetch_cloud_agent_data", 58: "send_to_user", 61: "pi_read",
	62: "pi_bash", 63: "pi_edit", 64: "pi_write", 65: "pi_grep",
	66: "pi_find", 67: "pi_ls", 68: "connect_scm", 69: "search_conversations",
	70: "create_goal", 71: "update_goal",
}

// toolKinds 把字段号映射到内部类型。「进行中」帧的参数还不完整，
// 解析器那套「字段为空就返回 nil」的判定用不上，所以单列一张表。
var toolKinds = map[int]NativeToolKind{
	toolRunTerminal: ToolRunTerminal, toolDeleteFile: ToolDeleteFile,
	toolGlob: ToolGlob, toolSearchFiles: ToolSearchFiles,
	toolReadFile: ToolReadFile, toolUpdateTodos: ToolUpdateTodos,
	toolWriteFile: ToolWriteFile, toolListFiles: ToolListFiles,
	toolWebSearch: ToolWebSearch, toolTask: ToolTask,
	toolAskQuestion: ToolAskQuestion, toolFetch: ToolFetchURL,
	toolFetchURL: ToolFetchURL, toolAwait: ToolAwait,
}

// toolMetaFields 是参数容器里与具体工具无关的附带字段，
// 扫描未识别工具时必须跳过，否则会把它们误报成工具调用。
var toolMetaFields = map[int]bool{
	54: true, // hook_additional_contexts
	57: true, // tool_call_id
	59: true, // started_at_ms
	60: true, // completed_at_ms
}

// parseNativeToolCall 从「工具调用完成」帧里解出内置工具调用。
//
// 必须从这里取而不是顶层的「客户端动作」帧：动作帧只带路径这类摘要信息
// （例如写文件的动作帧只有路径没有内容），据此还原会得到错误的调用。
func parseNativeToolCall(sm map[int][]proto.Field) *NativeToolCall {
	done := proto.FirstBytes(sm, smToolCallDone)
	if done == nil {
		return nil
	}
	f := proto.Decode(done)
	id := proto.FirstString(f, 1)
	wrapper := proto.FirstBytes(f, 2)
	if wrapper == nil {
		return nil
	}
	w := proto.Decode(wrapper)

	// args 取出某个工具字段下再套一层字段 1 的参数对象
	args := func(toolField int) map[int][]proto.Field {
		body := proto.FirstBytes(w, toolField)
		if body == nil {
			return nil
		}
		inner := proto.FirstBytes(proto.Decode(body), 1)
		if inner == nil {
			return nil
		}
		return proto.Decode(inner)
	}

	// 按字段号查表解析。早期是一串 if，字段号靠抓帧猜，出过两次错
	// （4 当成 ls 其实是 glob、23 当成 todo 其实是 ask_question），
	// 改成查表后加工具只需加一行，也不会再受判断顺序影响。
	for field, parse := range toolParsers {
		a := args(field)
		if a == nil {
			continue
		}
		call := parse(a)
		if call == nil {
			continue
		}
		call.ID = id
		call.Field = field
		return call
	}

	// 走到这里说明上游用了我们还不解析参数的工具。必须报出来——直接丢弃会让
	// 客户端收不到任何调用，对话就那样断掉，且无从排查。
	for field := range w {
		if toolMetaFields[field] {
			continue
		}
		if _, parsed := toolParsers[field]; parsed {
			continue
		}
		if body := proto.FirstBytes(w, field); body != nil {
			return &NativeToolCall{
				Kind: ToolUnknown, ID: id, Field: field,
				Name:        toolNames[field],
				Description: firstStringDeep(body, 3),
				Raw:         body,
			}
		}
	}
	return nil
}

// toolParsers 按字段号解析各工具的参数。
// 只收录「知道参数长什么样、且还原出来对客户端有用」的工具；
// 其余的走未识别分支，靠 toolNames 报出名字即可。
var toolParsers = map[int]func(a map[int][]proto.Field) *NativeToolCall{
	toolWriteFile: func(a map[int][]proto.Field) *NativeToolCall {
		path := proto.FirstString(a, 1)
		if path == "" {
			return nil
		}
		return &NativeToolCall{Kind: ToolWriteFile, Path: path, Content: proto.FirstString(a, 6)}
	},
	toolReadFile: func(a map[int][]proto.Field) *NativeToolCall {
		if path := proto.FirstString(a, 1); path != "" {
			return &NativeToolCall{Kind: ToolReadFile, Path: path}
		}
		return nil
	},
	toolRunTerminal: func(a map[int][]proto.Field) *NativeToolCall {
		cmd := proto.FirstString(a, 1)
		if cmd == "" {
			return nil
		}
		return &NativeToolCall{
			Kind: ToolRunTerminal, Command: cmd,
			Path: proto.FirstString(a, 2), Description: proto.FirstString(a, 15),
		}
	},
	toolSearchFiles: func(a map[int][]proto.Field) *NativeToolCall {
		if q := proto.FirstString(a, 1); q != "" {
			return &NativeToolCall{Kind: ToolSearchFiles, Pattern: q, Path: proto.FirstString(a, 2)}
		}
		return nil
	},
	toolGlob: func(a map[int][]proto.Field) *NativeToolCall {
		// GlobToolArgs 的模式在字段 2，字段 1 是可选的起始目录
		if p := proto.FirstString(a, 2); p != "" {
			return &NativeToolCall{Kind: ToolGlob, Pattern: p, Path: proto.FirstString(a, 1)}
		}
		return nil
	},
	toolListFiles: func(a map[int][]proto.Field) *NativeToolCall {
		if path := proto.FirstString(a, 1); path != "" {
			return &NativeToolCall{Kind: ToolListFiles, Path: path}
		}
		return nil
	},
	toolDeleteFile: func(a map[int][]proto.Field) *NativeToolCall {
		if path := proto.FirstString(a, 1); path != "" {
			return &NativeToolCall{Kind: ToolDeleteFile, Path: path}
		}
		return nil
	},
	toolTask: func(a map[int][]proto.Field) *NativeToolCall {
		prompt := proto.FirstString(a, 2)
		if prompt == "" {
			return nil
		}
		return &NativeToolCall{Kind: ToolTask, Description: proto.FirstString(a, 1), Prompt: prompt}
	},
	toolWebSearch: func(a map[int][]proto.Field) *NativeToolCall {
		if q := proto.FirstString(a, 1); q != "" {
			return &NativeToolCall{Kind: ToolWebSearch, Pattern: q}
		}
		return nil
	},
	toolFetchURL: fetchParser,
	toolFetch:    fetchParser,
	toolUpdateTodos: func(a map[int][]proto.Field) *NativeToolCall {
		items := parseTodoItems(a)
		if len(items) == 0 {
			return nil
		}
		return &NativeToolCall{Kind: ToolUpdateTodos, Todos: items, Description: items[0].Content}
	},
	toolAwait: func(a map[int][]proto.Field) *NativeToolCall {
		// task_id 可以为空（等待任意任务），所以这个工具不靠字段非空来识别
		return &NativeToolCall{Kind: ToolAwait, Description: proto.FirstString(a, 1)}
	},
	toolAskQuestion: func(a map[int][]proto.Field) *NativeToolCall {
		return &NativeToolCall{Kind: ToolAskQuestion, Description: proto.FirstString(a, 1)}
	},
}

func fetchParser(a map[int][]proto.Field) *NativeToolCall {
	if url := proto.FirstString(a, 1); url != "" {
		return &NativeToolCall{Kind: ToolFetchURL, URL: url}
	}
	return nil
}

// todoStatus 对应 agent.v1.TodoStatus 枚举。
// 用规范英文名而不是中文：这个值可能要填进客户端声明的待办工具参数里，
// 显示用的中文在渲染那一层再翻。
var todoStatus = map[uint64]string{
	1: "pending", 2: "in_progress", 3: "completed", 4: "cancelled",
}

// parseTodoItems 解析 UpdateTodosArgs.todos（repeated TodoItem）。
// TodoItem { 1: id, 2: 内容, 3: 状态, 4: 创建时间, 5: 更新时间 }
func parseTodoItems(a map[int][]proto.Field) []TodoItem {
	var out []TodoItem
	for _, f := range a[1] {
		if f.WireType != 2 {
			continue
		}
		item := proto.Decode(f.Bytes)
		content := proto.FirstString(item, 2)
		if content == "" {
			continue
		}
		status := ""
		if v, ok := item[3]; ok && len(v) > 0 {
			status = todoStatus[v[0].Varint]
		}
		out = append(out, TodoItem{
			ID: proto.FirstString(item, 1), Content: content, Status: status,
		})
	}
	return out
}

// firstNonEmpty 依次尝试若干字段，返回第一个非空字符串。
func firstNonEmpty(f map[int][]proto.Field, fields ...int) string {
	for _, n := range fields {
		if s := proto.FirstString(f, n); s != "" {
			return s
		}
	}
	return ""
}

// firstStringDeep 在嵌套报文里找出第一个可打印字符串，用于给未知工具一个可读线索。
func firstStringDeep(data []byte, depth int) string {
	if depth < 0 {
		return ""
	}
	for _, entries := range proto.Decode(data) {
		for _, f := range entries {
			if f.WireType != 2 || len(f.Bytes) == 0 {
				continue
			}
			if s := string(f.Bytes); utf8.ValidString(s) && isPrintableText(s) {
				return s
			}
			if s := firstStringDeep(f.Bytes, depth-1); s != "" {
				return s
			}
		}
	}
	return ""
}

func isPrintableText(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' {
			return false
		}
	}
	return len(s) > 0
}

// agentStreamState 跟踪一次流的解析状态。
type agentStreamState struct {
	gotContent bool // 是否已产出过正文
	// textBytes 累计产出的正文字节数，仅用于截断时的日志。
	textBytes int
	errored   bool // 是否已下发过错误
	// sawGeneration 表示生成阶段已开始。用于区分开头回写的会话上下文
	// 与结尾回写的会话记录——两者都是 fieldConversation 帧。
	sawGeneration bool
	// turnComplete 表示上游已把整轮对话持久化，即本轮生成结束。
	turnComplete bool
	// sawToolCall 表示本轮下发过内置工具调用。此时即便没有正文也是正常结果，
	// 不应被上层当成空响应而重试。
	sawToolCall bool
	// emitted 按调用 id 去重：同一次调用会先后出现「进行中」与「完成」多个帧。
	emitted map[string]bool
	// model 仅用于给未识别工具的记录标注来源模型。
	model string
	// announced 记录「进行中」帧宣告过的调用；收到完成帧后由 emitted 抵消。
	// 收尾时还剩下的，就是上游宣告了却没做完的工具。
	announced map[string]NativeToolKind
	// toolKind / toolPath 记录每次调用的工具类型与路径。
	// 「进行中」帧先于参数片段到达，据此判断片段要不要转发。
	toolKind map[string]NativeToolKind
	toolPath map[string]string
}

// process 消费缓冲里完整的帧，返回是否遇到协议级结束帧。
func (s *agentStreamState) process(buffer *[]byte, send func(StreamEvent) bool) bool {
	b := *buffer
	pos := 0
	for pos+5 <= len(b) {
		flag := b[pos]
		length := binary.BigEndian.Uint32(b[pos+1 : pos+5])
		if pos+5+int(length) > len(b) {
			break
		}
		payload := b[pos+5 : pos+5+int(length)]
		pos += 5 + int(length)
		if frameDebug {
			debugFrame(flag, length, payload)
		}
		if length == 0 {
			continue
		}

		if flag&0x01 != 0 {
			raw, err := gunzipBytes(payload)
			if err != nil {
				continue
			}
			payload = raw
		}
		if flag&0x02 != 0 {
			// Connect 协议的 end-of-stream 帧。agentn.api5 目前不发这个，
			// 但按协议处理，换端点或上游改行为时都能立刻收尾。
			utf := strings.TrimSpace(string(payload))
			if utf != "" {
				if e := parseErrorFrame(utf); e != "" {
					s.errored = true
					send(StreamEvent{Kind: EventError, Message: e})
				}
			}
			*buffer = b[pos:]
			return true
		}

		top := proto.Decode(payload)

		// 心跳帧：上游每 10s 一个，不代表有新数据。
		if sm := proto.FirstBytes(top, fieldStreamMessage); sm != nil {
			smFields := proto.Decode(sm)
			if _, isHeartbeat := smFields[fieldHeartbeat]; isHeartbeat && len(smFields) == 1 {
				continue
			}
			s.sawGeneration = true

			// 「进行中」帧先到：记下这次调用的工具类型与路径，
			// 后续的参数片段才知道该不该转发。
			if id, kind, path, ok := parseToolProgress(smFields); ok {
				if s.toolKind == nil {
					s.toolKind = map[string]NativeToolKind{}
					s.toolPath = map[string]string{}
					s.announced = map[string]NativeToolKind{}
				}
				s.announced[id] = kind
				if kind != "" {
					s.toolKind[id] = kind
				}
				if path != "" {
					s.toolPath[id] = path
				}
				continue
			}

			// 参数流式片段
			if id, text, ok := parseToolInputDelta(smFields); ok {
				if text != "" {
					send(StreamEvent{
						Kind: EventToolInputDelta,
						Text: text,
						Tool: &NativeToolCall{ID: id, Kind: s.toolKind[id], Path: s.toolPath[id]},
					})
				}
				continue
			}

			// 内置工具调用：参数完整的「调用完成」帧
			if call := parseNativeToolCall(smFields); call != nil && !s.emitted[call.ID] {
				if s.emitted == nil {
					s.emitted = map[string]bool{}
				}
				s.emitted[call.ID] = true
				s.sawToolCall = true
				if call.Kind == ToolUnknown {
					name := call.Name
					if name == "" {
						name = "未知"
					}
					log.Printf("[cursor] 上游用了尚未支持的内置工具 %s（字段 %d，线索: %q）。"+
						"详情已记录到管理界面「未识别工具」页。", name, call.Field, trunc(call.Description, 80))
					toollog.Record(call.Field, call.Name, s.model, call.ID,
						call.Description, describeDeep(call.Raw, 5), call.Raw)
				}
				send(StreamEvent{Kind: EventToolCall, Tool: call})
				continue
			}
		}

		// 客户端动作帧只是同一次调用的摘要通知，参数不全，忽略即可。
		if _, ok := top[fieldClientAction]; ok {
			continue
		}

		// 会话记录回写。开头也会回写一次上下文，所以只有在生成已经开始之后
		// 出现，才说明这一轮真的结束了。
		if _, ok := top[fieldConversation]; ok {
			if s.sawGeneration {
				s.turnComplete = true
			}
			continue
		}

		utf := string(payload)
		if strings.Contains(utf, `"error"`) {
			if e := parseErrorFrame(utf); e != "" {
				s.errored = true
				send(StreamEvent{Kind: EventError, Message: e})
			}
			continue
		}

		delta := proto.DecodeAgentFrame(payload)
		if delta.Content != "" || delta.Thinking != "" {
			// 记录回写之后又来内容，说明这一轮还没完，撤回结束判定。
			s.turnComplete = false
			if delta.Content != "" {
				s.gotContent = true
				s.textBytes += len(delta.Content)
			}
			send(StreamEvent{Kind: EventDelta, Text: delta.Content, Thinking: delta.Thinking})
		}
	}
	*buffer = b[pos:]
	return false
}

// trunc 截断日志里的长字符串。
func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
