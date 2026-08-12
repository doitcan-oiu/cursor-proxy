package openai

import (
	"encoding/json"
	"fmt"
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

// blocksToText 把内容块拍平成文本；tool_use / tool_result 会被还原成
// 与提示词协议一致的形式，让模型看得懂上一轮发生了什么。
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
		switch b.Type {
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(tools.RenderCall(tools.Call{ID: b.ID, Name: b.Name, Arguments: args}))
		case "tool_result":
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			// 结果用显式标签包起来，模型更容易把它和自己的调用对应上；
			// 失败结果单独标注，避免模型看不出「上一次已经失败了」而原样重试。
			status := "tool_result"
			if b.IsError {
				status = "tool_error"
			}
			sb.WriteString(fmt.Sprintf("<%s id=%q>\n%s\n</%s>", status, b.ToolUseID, blocksToText(b.Content), status))
		default:
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

func anthropicToInternal(body anthropicBody) []types.Message {
	var msgs []types.Message
	if sys := blocksToText(body.System); sys != "" {
		msgs = append(msgs, types.Message{Role: "system", Content: sys})
	}
	for _, m := range body.Messages {
		msgs = append(msgs, types.Message{Role: m.Role, Content: blocksToText(m.Content)})
	}
	return msgs
}

func sse(event string, data any) string {
	b, _ := json.Marshal(data)
	return "event: " + event + "\ndata: " + string(b) + "\n\n"
}

// Messages 处理 POST /v1/messages（Anthropic 兼容）。
func Messages(w http.ResponseWriter, r *http.Request) {
	var body anthropicBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteJSON(w, 400, map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": "Invalid JSON body."}})
		return
	}
	model := body.Model
	if model == "" {
		model = "auto"
	}
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
	inputTokens := tokenize.CountMessages(model, messages)

	opened, uerr := OpenWithFailover(messages, model)
	if uerr != nil {
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: model, KeyPrefix: keyPrefix, Stream: body.Stream,
			Status: "error", HTTPStatus: uerr.Status, Ms: time.Since(startedAt).Milliseconds(), Error: trunc(uerr.Msg, 200)})
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
		// 纯对话时把写文件的内容边收边吐，避免长内容静默几十秒再整段出现
		live := tools.NewLiveWriter(len(toolDefs) == 0)

		for ev := range events {
			switch ev.Kind {
			case cursor.EventDelta:
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
			case cursor.EventError:
				errored = true
				errMsg = ev.Message
			}
		}
		emitText(live.Interrupt())

		stopReason := "end_turn"
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

		outcome := cursor.OutcomeSuccess
		if errored {
			outcome = cursor.OutcomeError
		}
		cursor.ReleaseAccount(account.ID, outcome, "")
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: true, Status: statusOf(errored), Ms: time.Since(startedAt).Milliseconds(),
			Chars: utf8.RuneCountInString(content.String()), Tokens: outputTokens, Error: trunc(errMsg, 200)})
		return
	}

	content := ""
	lastError := ""
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
			Stream: false, Status: "error", Ms: time.Since(startedAt).Milliseconds(), Error: trunc(lastError, 200)})
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
