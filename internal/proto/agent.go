package proto

import (
	"strings"

	"github.com/google/uuid"

	"cursor-proxy/internal/types"
)

// EncodeAgentRequest 构造 agentn.api5 的 AgentClientMessage/AgentRunRequest（未加 Connect 信封）。
// 取最后一条 user 消息作为本轮输入，其余（含 system）作为上下文文件塞进 ExplicitContext。
func EncodeAgentRequest(messages []types.Message, modelID string) []byte {
	var lastUserIdx = -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	var text string
	if lastUserIdx >= 0 {
		text = types.ContentToText(messages[lastUserIdx].Content)
	} else if len(messages) > 0 {
		text = types.ContentToText(messages[len(messages)-1].Content)
	}

	var historyParts []string
	for i, m := range messages {
		if i == lastUserIdx {
			continue
		}
		historyParts = append(historyParts, m.Role+": "+types.ContentToText(m.Content))
	}
	history := strings.Join(historyParts, "\n")
	if history == "" {
		history = "chat"
	}

	userMsg := NewWriter()
	userMsg.Str(1, text)
	userMsg.Str(2, uuid.NewString())
	userMsg.Str(3, "")

	fileCtx := NewWriter()
	fileCtx.Str(1, "/context.txt")
	fileCtx.Str(2, history)

	explicitCtx := NewWriter()
	explicitCtx.Bytes(2, fileCtx.Finish())

	userMsgAction := NewWriter()
	userMsgAction.Bytes(1, userMsg.Finish())
	userMsgAction.Bytes(2, explicitCtx.Finish())

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
