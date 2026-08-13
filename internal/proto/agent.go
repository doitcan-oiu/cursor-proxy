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

// EncodeAgentRequest 构造 agentn.api5 的 AgentClientMessage/AgentRunRequest（未加 Connect 信封）。
func EncodeAgentRequest(messages []types.Message, modelID string) []byte {
	userMsg := NewWriter()
	userMsg.Str(1, BuildPrompt(messages))
	userMsg.Str(2, uuid.NewString())
	// 字段 3 是 SelectedContext。没有图片时发空消息，行为与之前一致。
	userMsg.Bytes(3, encodeSelectedImages(types.CollectImages(messages)))

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
