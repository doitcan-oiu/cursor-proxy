package proto

import (
	"time"

	"github.com/google/uuid"

	"cursor-proxy/internal/types"
)

// EncodeChatRequest 构造 StreamUnifiedChatWithToolsRequest 的 protobuf 主体
// （未加 Connect 信封）。字段号严格对应 message.proto，system 消息合并进 instruction。
func EncodeChatRequest(messages []types.Message, modelName string) []byte {
	instruction := ""
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			if instruction != "" {
				instruction += "\n"
			}
			instruction += types.ContentToText(m.Content)
		}
	}

	type idRole struct {
		id   string
		role int
	}
	var messageIDs []idRole

	req := NewWriter()
	for _, m := range messages {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		role := 2
		if m.Role == "user" {
			role = 1
		}
		messageID := uuid.NewString()
		messageIDs = append(messageIDs, idRole{messageID, role})

		msg := NewWriter()
		msg.Str(1, types.ContentToText(m.Content))
		msg.Int32(2, role)
		msg.Str(13, messageID)
		if role == 1 {
			msg.Int32(47, 1)
		}
		req.Bytes(1, msg.Finish())
	}

	req.Int32(2, 1)

	instr := NewWriter()
	if instruction != "" {
		instr.Str(1, instruction)
	}
	req.Bytes(3, instr.Finish())

	req.Int32(4, 1)

	model := NewWriter()
	model.Str(1, modelName)
	model.Bytes(4, []byte{})
	req.Bytes(5, model.Finish())

	req.Str(8, "")
	req.Int32(13, 1)

	setting := NewWriter()
	setting.Str(1, `cursor\aisettings`)
	setting.Bytes(3, []byte{})
	unknown6 := NewWriter()
	unknown6.Bytes(1, []byte{})
	unknown6.Bytes(2, []byte{})
	setting.Bytes(6, unknown6.Finish())
	setting.Int32(8, 1)
	setting.Int32(9, 1)
	req.Bytes(15, setting.Finish())

	req.Int32(19, 1)
	req.Str(23, uuid.NewString())

	metadata := NewWriter()
	metadata.Str(1, "win32")
	metadata.Str(2, "x64")
	metadata.Str(3, "10.0.22631")
	metadata.Str(4, `C:\Program Files\PowerShell\7\pwsh.exe`)
	metadata.Str(5, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	req.Bytes(26, metadata.Finish())

	req.Int32(27, 0)

	for _, mid := range messageIDs {
		idMsg := NewWriter()
		idMsg.Str(1, mid.id)
		idMsg.Int32(3, mid.role)
		req.Bytes(30, idMsg.Finish())
	}

	req.Int32(35, 0)
	req.Int32(38, 0)
	req.Int32(46, 1)
	req.Str(47, "")
	req.Int32(48, 0)
	req.Int32(49, 0)
	req.Int32(51, 0)
	req.Int32(53, 1)
	req.Str(54, "Ask")

	top := NewWriter()
	top.Bytes(1, req.Finish())
	return top.Finish()
}

// DecodedChat 单帧解析出的增量。
type DecodedChat struct {
	Text     string
	Thinking string
}

// DecodeChatResponse 解析 StreamUnifiedChatWithToolsResponse：
// message = 2 -> { content = 1, thinking(25) -> content = 1 }。
func DecodeChatResponse(data []byte) DecodedChat {
	top := Decode(data)
	messageBuf := FirstBytes(top, 2)
	if messageBuf == nil {
		return DecodedChat{}
	}
	message := Decode(messageBuf)
	out := DecodedChat{Text: FirstString(message, 1)}
	if thinkingBuf := FirstBytes(message, 25); thinkingBuf != nil {
		out.Thinking = FirstString(Decode(thinkingBuf), 1)
	}
	return out
}

// DecodeAvailableModels 解析 AvailableModelsResponse：
// repeated AvailableModel models = 2 (name=1)；兼容 repeated string modelNames = 1。
func DecodeAvailableModels(data []byte) []string {
	top := Decode(data)
	seen := map[string]bool{}
	var names []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			names = append(names, s)
		}
	}
	for _, entry := range top[2] {
		if entry.WireType == 2 {
			add(FirstString(Decode(entry.Bytes), 1))
		}
	}
	for _, entry := range top[1] {
		if entry.WireType == 2 {
			s := string(entry.Bytes)
			if isModelName(s) {
				add(s)
			}
		}
	}
	return names
}

func isModelName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == ':' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}
