package proto

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"cursor-proxy/internal/types"
)

// BuildPrompt 把整轮对话拍平成一段提示词。
//
// 这里刻意不使用 ExplicitContext 的「文件上下文」：早期实现把历史塞进一个假的
// /context.txt，结果 Cursor 的 agent 会认为自己身处代码工作区，先回一句
// 「先读取工作区规则…」然后发起工具调用就结束本轮——对纯对话代理来说这等于
// 每次都被截断。把对话直接写进消息正文后，模型会直接作答。
//
// 上游协议里其实有结构化的 UserMessageAction.conversation_history 字段
// （含 tool_call / tool_result 子消息），但实测该端点完全忽略它：只发结构化历史时
// 模型连上一轮说过的名字都不记得。所以历史只能继续拍平成文本。
//
// 拍平时刻意用中性叙述，不套 <tool_call> 之类的伪协议标签：模型会把那种写法
// 连同注入的提示词一起判定为「伪造的代理对话记录」，明确拒绝采信，
// 转而反复重试同一个工具。
func BuildPrompt(messages []types.Message) string {
	// 单条 user 消息原样送出，不加任何包装
	if len(messages) == 1 && messages[0].Role == "user" && len(messages[0].ToolCalls) == 0 {
		return types.ContentToText(messages[0].Content)
	}

	var b strings.Builder
	write := func(s string) {
		if s == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(s)
	}

	endsWithResult := false
	for _, m := range messages {
		content := strings.TrimSpace(types.ContentToText(m.Content))
		endsWithResult = m.Role == "tool"
		switch m.Role {
		case "system", "developer":
			// 系统提示直接作为正文开头，不加角色前缀
			write(content)
		case "assistant":
			write(joinNonEmpty(content, describeCalls(m.ToolCalls)))
		case "tool":
			write(describeResult(m, content))
		default:
			if content != "" {
				write(m.Role + ": " + content)
			}
		}
	}

	// 对话以工具结果结尾时，末尾没有任何指令，模型会倾向于「重新开始」
	// 而不是「接着往下做」——表现为把刚执行过的工具再调一遍。
	if endsWithResult {
		write("The tool results above are already available to you. " +
			"Continue the task using them; do not call the same tools again.")
	}
	return b.String()
}

// describeCalls 交代助手上一轮调用了什么工具。
//
// 刻意用平铺叙述而不是 <tool_call> 之类的伪协议标签：那种写法会被模型判定为
// 「伪造的代理对话记录」，明确拒绝采信后反复重试同一个工具。
// 也刻意用英文，避免给回答的语言带偏。
func describeCalls(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range calls {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("assistant: called tool ")
		b.WriteString(c.Name)
		if args := strings.TrimSpace(c.Args); args != "" && args != "{}" {
			b.WriteString(" with ")
			b.WriteString(args)
		}
	}
	return b.String()
}

// describeResult 交代工具返回了什么。失败要单独标注，
// 否则模型看不出上一次已经失败了，会原样再试一遍。
func describeResult(m types.Message, content string) string {
	name := m.ToolName
	if name == "" {
		name = "tool"
	}
	verb := "returned"
	if m.IsError {
		verb = "failed"
	}
	if content == "" {
		return name + " " + verb + " (no output)"
	}
	return name + " " + verb + ":\n" + content
}

func joinNonEmpty(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

// encodeSelectedImages 构造 UserMessage.selected_context，把图片挂进去。
//
// 字段号取自 Cursor 客户端自带的 agent.v1 描述：
//
//	SelectedContext { 1: repeated SelectedImage selected_images }
//	SelectedImage   { 8: bytes data, 7: string mime_type,
//	                  2: string uuid, 3: string path,
//	                  4: Dimension { 1: int32 width, 2: int32 height } }
//
// data 与 blob_id 是同一个 oneof，这里走内联 data，不用先上传拿 blob。
func encodeSelectedImages(images []types.Image) []byte {
	ctx := NewWriter()
	for i, img := range images {
		if len(img.Data) == 0 {
			continue
		}
		sel := NewWriter()
		sel.Bytes(8, img.Data)
		sel.Str(2, uuid.NewString())
		// path 只是给模型看的标识，客户端发的图没有真实路径
		sel.Str(3, fmt.Sprintf("image-%d%s", i+1, extForMime(img.MimeType)))
		if img.Width > 0 && img.Height > 0 {
			dim := NewWriter()
			dim.Int32(1, img.Width)
			dim.Int32(2, img.Height)
			sel.Bytes(4, dim.Finish())
		}
		if img.MimeType != "" {
			sel.Str(7, img.MimeType)
		}
		ctx.Bytes(1, sel.Finish())
	}
	return ctx.Finish()
}

func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

// Mode 对应上游的 agent.v1.AgentMode 枚举。
type Mode int

const (
	// ModeUnspecified 不指定，由上游决定（历史行为）。
	ModeUnspecified Mode = 0
	// ModeAgent 让上游按 agent 行事：可以调用内置工具。
	ModeAgent Mode = 1
	// ModeAsk 纯问答：模型直接作答，不会把内容塞进「写文件」调用。
	ModeAsk Mode = 2
)

// EncodeAgentRequest 构造 agentn.api5 的 AgentClientMessage/AgentRunRequest（未加 Connect 信封）。
func EncodeAgentRequest(messages []types.Message, modelID string, mode Mode) []byte {
	userMsg := NewWriter()
	userMsg.Str(1, BuildPrompt(messages))
	userMsg.Str(2, uuid.NewString())
	// 字段 3 是 SelectedContext。没有图片时发空消息，行为与之前一致。
	userMsg.Bytes(3, encodeSelectedImages(types.CollectImages(messages)))
	if mode != ModeUnspecified {
		userMsg.Int32(4, int(mode))
	}

	// ExplicitContext 是必填的：完全省略这个字段时上游会接受请求但一个字都不生成。
	// 这里给一个空上下文，避免伪造文件让 agent 以为身处代码工作区。
	userMsgAction := NewWriter()
	userMsgAction.Bytes(1, userMsg.Finish())
	userMsgAction.Bytes(2, []byte{})

	convAction := NewWriter()
	convAction.Bytes(1, userMsgAction.Finish())

	displayName := modelID
	if len(modelID) > 0 {
		displayName = strings.ToUpper(modelID[:1]) + strings.ReplaceAll(modelID[1:], "-", " ")
	}
	model := NewWriter()
	model.Str(1, modelID)
	model.Str(3, modelID)
	model.Str(4, displayName)
	model.Str(5, displayName)
	model.Int32(7, 0)

	runReq := NewWriter()
	runReq.Str(1, "")
	runReq.Bytes(2, convAction.Finish())
	runReq.Bytes(3, model.Finish())
	runReq.Str(4, "")
	runReq.Str(5, uuid.NewString())

	clientMsg := NewWriter()
	clientMsg.Bytes(1, runReq.Finish())
	return clientMsg.Finish()
}

// AgentDelta 单个 Agent 帧解析出的增量。
type AgentDelta struct {
	Content  string
	Thinking string
}

// DecodeAgentFrame 解析 Run 响应里单个 protobuf 帧：
// content = payload.1.1.1，thinking = payload.1.4.1，过滤含 role 的回显帧。
func DecodeAgentFrame(payload []byte) AgentDelta {
	var out AgentDelta
	top := Decode(payload)
	sm := FirstBytes(top, 1)
	if sm == nil {
		return out
	}
	smFields := Decode(sm)

	if wrap := FirstBytes(smFields, 1); wrap != nil {
		if inner := FirstBytes(Decode(wrap), 1); inner != nil {
			t := string(inner)
			if t != "" && !strings.Contains(t, `"role"`) {
				out.Content = t
			}
		}
	}
	if wrap := FirstBytes(smFields, 4); wrap != nil {
		if inner := FirstBytes(Decode(wrap), 1); inner != nil {
			t := string(inner)
			if t != "" && !strings.Contains(t, `"role"`) {
				out.Thinking = t
			}
		}
	}
	return out
}
