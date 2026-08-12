package proto

import (
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
func BuildPrompt(messages []types.Message) string {
	// 单条 user 消息原样送出，不加任何包装
	if len(messages) == 1 && messages[0].Role == "user" {
		return types.ContentToText(messages[0].Content)
	}

	var b strings.Builder
	for _, m := range messages {
		content := strings.TrimSpace(types.ContentToText(m.Content))
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		switch m.Role {
		case "system", "developer":
			// 系统提示直接作为正文开头，不加角色前缀
			b.WriteString(content)
		default:
			b.WriteString(m.Role)
			b.WriteString(": ")
			b.WriteString(content)
		}
	}
	return b.String()
}

// EncodeAgentRequest 构造 agentn.api5 的 AgentClientMessage/AgentRunRequest（未加 Connect 信封）。
func EncodeAgentRequest(messages []types.Message, modelID string) []byte {
	userMsg := NewWriter()
	userMsg.Str(1, BuildPrompt(messages))
	userMsg.Str(2, uuid.NewString())
	userMsg.Str(3, "")

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
