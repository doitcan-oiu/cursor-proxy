package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/reqlog"
	"cursor-proxy/internal/tokenize"
	"cursor-proxy/internal/tools"
	"cursor-proxy/internal/types"
)

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	// image
	Source *anthropicSource `json:"source"`
}

// anthropicSource 是 image 块的图片来源：base64 内联或 url 链接。
type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
	URL       string `json:"url"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicBody struct {
	Model      string             `json:"model"`
	System     json.RawMessage    `json:"system"`
	Messages   []anthropicMessage `json:"messages"`
	Stream     bool               `json:"stream"`
	Tools      []anthropicTool    `json:"tools"`
	ToolChoice json.RawMessage    `json:"tool_choice"`
}

func (b anthropicBody) toolDefs() []tools.Definition {
	defs := make([]tools.Definition, 0, len(b.Tools))
	for _, t := range b.Tools {
		if t.Name == "" {
			continue
		}
		defs = append(defs, tools.Definition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return defs
}

// toolChoice 解析 Anthropic 的 tool_choice：{"type":"auto"|"any"|"none"|"tool","name":"x"}。
func (b anthropicBody) toolChoice() tools.Choice {
	if len(b.ToolChoice) == 0 {
		return tools.Choice{Mode: "auto"}
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(b.ToolChoice, &obj) != nil {
		return tools.Choice{Mode: "auto"}
	}
	switch obj.Type {
	case "none":
		return tools.Choice{Mode: "none"}
	case "any":
		return tools.Choice{Mode: "required"}
	case "tool":
		if obj.Name != "" {
			return tools.Choice{Mode: "function", Name: obj.Name}
		}
	}
	return tools.Choice{Mode: "auto"}
}

// blocksToText 把内容块里的纯文本拍平。
//
// tool_use / tool_result 不在这里渲染：它们要保持结构化，由 proto 层写进上游
// 真正的 conversation_history。早期把它们拼成 <tool_call> / <tool_result> 标签
// 混进正文，模型会识破那是伪造的对话记录并拒绝采信，转而重复调用同样的工具。
func blocksToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []anthropicBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// splitAnthropicMessage 把一条 Anthropic 消息按内容块拆成内部消息序列。
//
// Anthropic 把「助手发起的调用」和「工具返回的结果」放在同一条消息的不同块里，
// 而上游的 conversation_history 要求它们各占一个轮次，所以这里要拆开。
func splitAnthropicMessage(m anthropicMessage) []types.Message {
	var blocks []anthropicBlock
	if len(m.Content) > 0 && json.Unmarshal(m.Content, &blocks) != nil {
		// 纯字符串内容
		return []types.Message{{Role: m.Role, Content: blocksToText(m.Content)}}
	}

	var out []types.Message
	var calls []types.ToolCall
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		args := "{}"
		if len(b.Input) > 0 {
			args = string(b.Input)
		}
		calls = append(calls, types.ToolCall{ID: b.ID, Name: b.Name, Args: args})
	}

	text := blocksToText(m.Content)
	images := blocksToImages(m.Content)
	if text != "" || len(calls) > 0 || (len(images) > 0 && m.Role != "user") {
		msg := types.Message{Role: m.Role, Content: text, ToolCalls: calls}
		if m.Role == "user" {
			msg.Images = images
		}
		out = append(out, msg)
	} else if len(images) > 0 {
		out = append(out, types.Message{Role: m.Role, Content: text, Images: images})
	}

	// 工具结果各自成一轮
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		out = append(out, types.Message{
			Role: "tool", Content: blocksToText(b.Content),
			Images: blocksToImages(b.Content),
			// Anthropic 的结果块只带调用 id，工具名由上游按 id 对应
			ToolCallID: b.ToolUseID, IsError: b.IsError,
		})
	}
	return out
}

// blocksToImages 取出内容块里的 image。tool_result 里也可能嵌图片，一并递归取出。
// 单张图片解不开只跳过它，不让整轮对话失败。
func blocksToImages(raw json.RawMessage) []types.Image {
	if len(raw) == 0 {
		return nil
	}
	var blocks []anthropicBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []types.Image
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			out = append(out, blocksToImages(b.Content)...)
		case "image":
			if b.Source == nil {
				continue
			}
			var (
				img types.Image
				err error
			)
			switch b.Source.Type {
			case "url":
				img, err = types.DecodeImageURL(b.Source.URL)
			default:
				img, err = types.DecodeImageBase64(b.Source.MediaType, b.Source.Data)
			}
			if err != nil {
				log.Printf("[image] 忽略一张无法解析的图片: %s", err)
				continue
			}
			out = append(out, img)
		}
	}
	return out
}

func anthropicToInternal(body anthropicBody) []types.Message {
	var msgs []types.Message
	if sys := blocksToText(body.System); sys != "" {
		msgs = append(msgs, types.Message{Role: "system", Content: sys})
	}
	for _, m := range body.Messages {
		msgs = append(msgs, splitAnthropicMessage(m)...)
	}
	return msgs
}

func sse(event string, data any) string {
	b, _ := json.Marshal(data)
	return "event: " + event + "\ndata: " + string(b) + "\n\n"
}

// Messages 处理 POST /v1/messages（Anthropic 兼容）。
func Messages(w http.ResponseWriter, r *http.Request) {
	// 原始请求体留着：出错时记进日志，便于原样复现
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		WriteJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": "Failed to read request body."}})
		return
	}
	var body anthropicBody
	if err := json.Unmarshal(raw, &body); err != nil {
		WriteJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": "Invalid JSON body."}})
		return
	}
	model := body.Model
	if model == "" {
		model = "auto"
	}
	tl := newTimeline("messages " + model)
	tl.mark("读请求体")

	messages := anthropicToInternal(body)
	hasNonSystem := false
	for _, m := range messages {
		if m.Role != "system" {
			hasNonSystem = true
			break
		}
	}
	if !hasNonSystem {
		WriteJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": "messages required"}})
		return
	}

	toolDefs := body.toolDefs()
	messages = injectToolPrompt(messages, toolDefs, body.toolChoice())

	id := "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	startedAt := time.Now()
	keyPrefix := anthropicKeyPrefix(r)
	tl.mark("解析消息")
	inputTokens := tokenize.CountMessages(model, messages)
	tl.mark("算 token")

	opened, uerr := OpenWithFailover(messages, model, ModeFor(len(toolDefs)))
	tl.markOpen(opened)
	if uerr != nil {
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: model, KeyPrefix: keyPrefix, Stream: body.Stream,
			Status: "error", HTTPStatus: uerr.Status, Ms: time.Since(startedAt).Milliseconds(),
			Error: trunc(uerr.Msg, 200), Request: reqlog.SanitizeRequest(raw)})
		status := 502
		if uerr.Status >= 400 && uerr.Status < 600 {
			status = uerr.Status
		}
		WriteJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": FriendlyUpstream(uerr.Msg)}})
		return
	}

	account := opened.Account
	events := Chain(opened.Buffered, opened.Stream)

	if body.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			WriteJSON(w, 500, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": "streaming unsupported"}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		send := func(ev string, d any) {
			fmt.Fprint(w, sse(ev, d))
			flusher.Flush()
		}

		var content strings.Builder
		errored := false
		errMsg := ""

		// message_start 携带输入 token；输出 token 在 message_delta 里给出（Anthropic 规范）。
		send("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": id, "type": "message", "role": "assistant", "model": model,
				"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": 0},
			},
		})
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		send("ping", map[string]any{"type": "ping"})

		var scanner *tools.Scanner
		if len(toolDefs) > 0 {
			scanner = &tools.Scanner{}
		}
		emitText := func(text string) {
			if text == "" {
				return
			}
			content.WriteString(text)
			send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": text}})
		}

		var calls []tools.Call
		truncated := false
		gotFirst := false
		// 纯对话时把写文件的内容边收边吐，避免长内容静默几十秒再整段出现
		live := tools.NewLiveWriter(len(toolDefs) == 0)

		for ev := range events {
			switch ev.Kind {
			case cursor.EventDelta:
				if !gotFirst {
					gotFirst = true
					tl.mark("等首个增量")
				}
				if ev.Text != "" {
					emitText(live.Interrupt())
					if scanner != nil {
						emitText(scanner.Push(ev.Text))
					} else {
						emitText(ev.Text)
					}
				}
			case cursor.EventToolInputDelta:
				emitText(live.Push(toNativePtr(ev.Tool), ev.Text))
			case cursor.EventToolCall:
				if s, handled := live.Finish(toNativePtr(ev.Tool)); handled {
					emitText(s)
					continue
				}
				// 上游内置工具调用：翻译成客户端声明的工具
				if c, ok := mapNativeCall(ev.Tool, toolDefs); ok {
					calls = append(calls, c)
				} else {
					emitText(tools.NativeToText(toNative(ev.Tool)))
				}
			case cursor.EventEnd:
				truncated = ev.Truncated
			case cursor.EventError:
				errored = true
				errMsg = ev.Message
			}
		}
		emitText(live.Interrupt())

		// 被时长上限掐断时报 max_tokens——Anthropic 侧最接近「没说完」的语义
		stopReason := "end_turn"
		if truncated {
			stopReason = "max_tokens"
		}
		if scanner != nil {
			emitText(scanner.Flush())
			calls = append(calls, scanner.Calls()...)
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})

		// 工具调用各占一个 tool_use 内容块，紧跟在文本块之后。
		for i, c := range calls {
			idx := i + 1
			send("content_block_start", map[string]any{
				"type": "content_block_start", "index": idx,
				"content_block": map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": map[string]any{}},
			})
			send("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": c.Arguments},
			})
			send("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx})
			stopReason = "tool_use"
		}
		if errored {
			stopReason = "error"
		}
		outputTokens := tokenize.CountText(model, content.String())
		send("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
		})
		send("message_stop", map[string]any{"type": "message_stop"})
		tl.done(fmt.Sprintf(" :: 输入 %d tok / 输出 %d tok / %d 字",
			inputTokens, outputTokens, utf8.RuneCountInString(content.String())))

		outcome := cursor.OutcomeSuccess
		if errored {
			outcome = cursor.OutcomeError
		}
		cursor.ReleaseAccount(account.ID, outcome, "")
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: true, Status: statusOf(errored), Ms: time.Since(startedAt).Milliseconds(),
			Chars: utf8.RuneCountInString(content.String()), Tokens: outputTokens,
			Error: trunc(errMsg, 200), Request: requestOnError(errored, raw)})
		return
	}

	content := ""
	lastError := ""
	truncated := false
	var toolCalls []tools.Call
	for ev := range events {
		switch ev.Kind {
		case cursor.EventDelta:
			content += ev.Text
		case cursor.EventToolCall:
			if c, ok := mapNativeCall(ev.Tool, toolDefs); ok {
				toolCalls = append(toolCalls, c)
			} else {
				content += tools.NativeToText(toNative(ev.Tool))
			}
		case cursor.EventEnd:
			truncated = ev.Truncated
		case cursor.EventError:
			lastError = ev.Message
		}
	}

	if len(toolDefs) > 0 {
		var scanner tools.Scanner
		content = scanner.Push(content) + scanner.Flush()
		toolCalls = append(toolCalls, scanner.Calls()...)
	}

	if content == "" && len(toolCalls) == 0 && lastError != "" {
		cursor.ReleaseAccount(account.ID, cursor.OutcomeError, lastError)
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: false, Status: "error", Ms: time.Since(startedAt).Milliseconds(),
			Error: trunc(lastError, 200), Request: reqlog.SanitizeRequest(raw)})
		WriteJSON(w, 502, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": FriendlyUpstream(lastError)}})
		return
	}

	cursor.ReleaseAccount(account.ID, cursor.OutcomeSuccess, "")
	outputTokens := tokenize.CountText(model, content)
	reqlog.Record(reqlog.Entry{Kind: "chat", Model: model, Account: account.Label, KeyPrefix: keyPrefix,
		Stream: false, Status: "ok", Ms: time.Since(startedAt).Milliseconds(),
		Chars: utf8.RuneCountInString(content), Tokens: outputTokens})
	blocks := []map[string]any{}
	if content != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": content})
	}
	stopReason := "end_turn"
	if truncated {
		stopReason = "max_tokens"
	}
	for _, c := range toolCalls {
		var input any = map[string]any{}
		if json.Unmarshal([]byte(c.Arguments), &input) != nil {
			input = map[string]any{}
		}
		blocks = append(blocks, map[string]any{
			"type": "tool_use", "id": c.ID, "name": c.Name, "input": input,
		})
		stopReason = "tool_use"
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}

	WriteJSON(w, 200, map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content":       blocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
	})
}

func anthropicKeyPrefix(r *http.Request) string {
	k := r.Header.Get("x-api-key")
	if k == "" {
		k = stripBearer(r.Header.Get("authorization"))
	}
	if len(k) > 10 {
		return k[:10]
	}
	return k
}
