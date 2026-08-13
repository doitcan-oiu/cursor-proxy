package openai

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

type rawToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type rawMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []rawToolCall   `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
}

// parseImageBlocks 从 OpenAI 的分块内容里取出 image_url。
//
// 解不开的图片不会让整个请求失败——多数客户端会一次带好几张，
// 因为其中一张坏掉就拒绝整轮对话不划算。返回的错误只用于提示。
func parseImageBlocks(content any) ([]types.Image, []string) {
	blocks, ok := content.([]any)
	if !ok {
		return nil, nil
	}
	var images []types.Image
	var errs []string
	for _, part := range blocks {
		p, ok := part.(map[string]any)
		if !ok || p["type"] != "image_url" {
			continue
		}
		holder, ok := p["image_url"].(map[string]any)
		if !ok {
			continue
		}
		url, _ := holder["url"].(string)
		if url == "" {
			continue
		}
		img, err := types.DecodeImageURL(url)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		images = append(images, img)
	}
	return images, errs
}

// parseMessages 把请求里的消息归一化成内部形态。
// assistant 的 tool_calls 与 tool 角色的结果都会被还原成文本回放给模型，
// 否则模型看不到自己上一轮调用了什么、拿到了什么结果。
func parseMessages(raw []rawMessage) []types.Message {
	out := make([]types.Message, 0, len(raw))
	for _, m := range raw {
		var content any
		if len(m.Content) > 0 {
			_ = json.Unmarshal(m.Content, &content)
		}
		text := types.ContentToText(content)
		images, imgErrs := parseImageBlocks(content)
		for _, e := range imgErrs {
			log.Printf("[image] 忽略一张无法解析的图片: %s", e)
		}

		switch {
		case len(m.ToolCalls) > 0:
			var b strings.Builder
			b.WriteString(text)
			for _, tc := range m.ToolCalls {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(tools.RenderCall(tools.Call{
					ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				}))
			}
			out = append(out, types.Message{Role: "assistant", Content: b.String()})

		case m.Role == "tool":
			label := m.Name
			if label == "" {
				label = m.ToolCallID
			}
			body := text
			if label != "" {
				body = fmt.Sprintf("[%s] %s", label, text)
			}
			out = append(out, types.Message{Role: "tool", Content: body})

		default:
			out = append(out, types.Message{Role: m.Role, Content: content, Images: images})
		}
	}
	return out
}

type rawTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type chatBody struct {
	Model         string          `json:"model"`
	Messages      []rawMessage    `json:"messages"`
	Stream        bool            `json:"stream"`
	Tools         []rawTool       `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// toolDefs 把 OpenAI 的 tools 字段转成内部定义。
func (b chatBody) toolDefs() []tools.Definition {
	defs := make([]tools.Definition, 0, len(b.Tools))
	for _, t := range b.Tools {
		if t.Function.Name == "" {
			continue
		}
		defs = append(defs, tools.Definition{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	return defs
}

// toolChoice 解析 tool_choice：可能是 "auto"/"none"/"required"，
// 也可能是 {"type":"function","function":{"name":"x"}}。
func (b chatBody) toolChoice() tools.Choice {
	if len(b.ToolChoice) == 0 {
		return tools.Choice{Mode: "auto"}
	}
	var s string
	if json.Unmarshal(b.ToolChoice, &s) == nil {
		if s == "" {
			s = "auto"
		}
		return tools.Choice{Mode: s}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(b.ToolChoice, &obj) == nil && obj.Function.Name != "" {
		return tools.Choice{Mode: "function", Name: obj.Function.Name}
	}
	return tools.Choice{Mode: "auto"}
}

// usagePayload 组装 OpenAI 风格的 usage。token 数为估算值，见 internal/tokenize。
func usagePayload(promptTokens, completionTokens int) map[string]any {
	return map[string]any{
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      promptTokens + completionTokens,
	}
}

func sseChunk(id, model string, delta map[string]any, finish any) string {
	payload := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	b, _ := json.Marshal(payload)
	return "data: " + string(b) + "\n\n"
}

func keyPrefixFromAuth(r *http.Request) string {
	bearer := stripBearer(r.Header.Get("authorization"))
	if len(bearer) > 10 {
		return bearer[:10]
	}
	return bearer
}

// ChatCompletions 处理 POST /v1/chat/completions。
func ChatCompletions(w http.ResponseWriter, r *http.Request) {
	var body chatBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		SendError(w, 400, "Invalid JSON body.", "invalid_request_error", "")
		return
	}
	if body.Model == "" {
		SendError(w, 400, "Missing 'model'.", "invalid_request_error", "missing_model")
		return
	}
	if len(body.Messages) == 0 {
		SendError(w, 400, "'messages' must be a non-empty array.", "invalid_request_error", "missing_messages")
		return
	}

	messages := parseMessages(body.Messages)
	// 声明了 tools 才注入工具提示词；普通对话完全不受影响。
	toolDefs := body.toolDefs()
	messages = injectToolPrompt(messages, toolDefs, body.toolChoice())

	id := "chatcmpl-" + uuid.NewString()
	startedAt := time.Now()
	keyPrefix := keyPrefixFromAuth(r)
	promptTokens := tokenize.CountMessages(body.Model, messages)

	opened, uerr := OpenWithFailover(messages, body.Model)
	if uerr != nil {
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Stream: body.Stream, KeyPrefix: keyPrefix,
			Status: "error", HTTPStatus: uerr.Status, Ms: time.Since(startedAt).Milliseconds(), Error: trunc(uerr.Msg, 200)})
		if uerr.Status == 0 || uerr.Status == 503 {
			SendError(w, 503, "无可用 Cursor 账号: "+FriendlyUpstream(uerr.Msg), "server_error", "no_account_available")
			return
		}
		typ := "upstream_error"
		if uerr.Status == 429 {
			typ = "rate_limit_error"
		}
		status := uerr.Status
		if status == 0 {
			status = 502
		}
		SendError(w, status, FriendlyUpstream(uerr.Msg), typ, "")
		return
	}

	account := opened.Account
	events := Chain(opened.Buffered, opened.Stream)

	if body.Stream {
		flusher, ok := w.(http.Flusher)
		if !ok {
			SendError(w, 500, "streaming unsupported", "server_error", "")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		writeSSE := func(s string) {
			fmt.Fprint(w, s)
			flusher.Flush()
		}
		writeSSE(sseChunk(id, body.Model, map[string]any{"role": "assistant"}, nil))

		errored := false
		errMsg := ""
		var content, reasoning strings.Builder
		// 有工具时用 Scanner 边流边剥离调用标签，避免半截标签漏给客户端。
		var scanner *tools.Scanner
		if len(toolDefs) > 0 {
			scanner = &tools.Scanner{}
		}

		emitText := func(text string) {
			if text == "" {
				return
			}
			content.WriteString(text)
			writeSSE(sseChunk(id, body.Model, map[string]any{"content": text}, nil))
		}

		var nativeCalls []tools.Call
		// 纯对话（客户端没声明工具）时，把写文件调用的内容边收边吐，
		// 否则长内容要等整个调用发完才一次性出现。
		live := tools.NewLiveWriter(len(toolDefs) == 0)

		for ev := range events {
			switch ev.Kind {
			case cursor.EventDelta:
				if ev.Thinking != "" {
					reasoning.WriteString(ev.Thinking)
					writeSSE(sseChunk(id, body.Model, map[string]any{"reasoning_content": ev.Thinking}, nil))
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
				if c, ok := mapNativeCall(ev.Tool, toolDefs); ok {
					nativeCalls = append(nativeCalls, c)
				} else {
					emitText(tools.NativeToText(toNative(ev.Tool)))
				}
			case cursor.EventError:
				errored = true
				errMsg = ev.Message
				b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": ev.Message}})
				writeSSE("data: " + string(b) + "\n\n")
			}
		}

		emitText(live.Interrupt())

		finish := "stop"
		calls := nativeCalls
		if scanner != nil {
			emitText(scanner.Flush())
			// 上游原生调用优先；模型自己按文本协议写的作为补充
			calls = append(calls, scanner.Calls()...)
		}
		if len(calls) > 0 {
			for i, c := range calls {
				writeSSE(sseChunk(id, body.Model, map[string]any{
					"tool_calls": []map[string]any{toolCallDelta(i, c)},
				}, nil))
			}
			finish = "tool_calls"
		}
		writeSSE(sseChunk(id, body.Model, map[string]any{}, finish))

		// 按 OpenAI 规范：仅在 stream_options.include_usage 为真时补一个
		// choices 为空、只带 usage 的收尾分片。
		completionTokens := tokenize.CountText(body.Model, content.String()) +
			tokenize.CountText(body.Model, reasoning.String())
		if body.StreamOptions != nil && body.StreamOptions.IncludeUsage {
			payload, _ := json.Marshal(map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   body.Model,
				"choices": []any{},
				"usage":   usagePayload(promptTokens, completionTokens),
			})
			writeSSE("data: " + string(payload) + "\n\n")
		}

		outcome := cursor.OutcomeSuccess
		if errored {
			outcome = cursor.OutcomeError
		}
		cursor.ReleaseAccount(account.ID, outcome, "")
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: true, Status: statusOf(errored), Ms: time.Since(startedAt).Milliseconds(),
			Chars: utf8.RuneCountInString(content.String()), Tokens: completionTokens, Error: trunc(errMsg, 200)})
		writeSSE("data: [DONE]\n\n")
		return
	}

	content := ""
	reasoning := ""
	lastError := ""
	var toolCalls []tools.Call
	for ev := range events {
		switch ev.Kind {
		case cursor.EventDelta:
			content += ev.Text
			reasoning += ev.Thinking
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
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: false, Status: "error", Ms: time.Since(startedAt).Milliseconds(), Error: trunc(lastError, 200)})
		SendError(w, 502, FriendlyUpstream(lastError), "upstream_error", "")
		return
	}

	cursor.ReleaseAccount(account.ID, cursor.OutcomeSuccess, "")
	completionTokens := tokenize.CountText(body.Model, content) + tokenize.CountText(body.Model, reasoning)
	reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
		Stream: false, Status: "ok", Ms: time.Since(startedAt).Milliseconds(),
		Chars: utf8.RuneCountInString(content), Tokens: completionTokens})

	message := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		list := make([]map[string]any, 0, len(toolCalls))
		for _, c := range toolCalls {
			list = append(list, map[string]any{
				"id":       c.ID,
				"type":     "function",
				"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
			})
		}
		message["tool_calls"] = list
		finishReason = "tool_calls"
		// 只有工具调用没有正文时，按规范 content 应为 null
		if content == "" {
			message["content"] = nil
		}
	}
	WriteJSON(w, 200, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   body.Model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   usagePayload(promptTokens, completionTokens),
	})
}

// toNativePtr 与 toNative 相同，但保留 nil 语义，供流式写出器判断。
func toNativePtr(c *cursor.NativeToolCall) *tools.Native {
	if c == nil {
		return nil
	}
	n := toNative(c)
	return &n
}

// toNative 把 cursor 层的内置调用转成 tools 层的形态（两层不互相依赖）。
func toNative(c *cursor.NativeToolCall) tools.Native {
	if c == nil {
		return tools.Native{}
	}
	kind := tools.KindReadFile
	switch c.Kind {
	case cursor.ToolRunTerminal:
		kind = tools.KindRunTerminal
	case cursor.ToolSearchFiles:
		kind = tools.KindSearchFiles
	case cursor.ToolWriteFile:
		kind = tools.KindWriteFile
	case cursor.ToolTask:
		kind = tools.KindTask
	case cursor.ToolDeleteFile:
		kind = tools.KindDeleteFile
	case cursor.ToolListFiles:
		kind = tools.KindListFiles
	case cursor.ToolFetchURL:
		kind = tools.KindFetchURL
	case cursor.ToolTodoWrite:
		kind = tools.KindTodoWrite
	case cursor.ToolUnknown:
		kind = tools.KindUnknown
	}
	return tools.Native{
		ID: c.ID, Kind: kind, Path: c.Path, Command: c.Command,
		Pattern: c.Pattern, Content: c.Content, Prompt: c.Prompt,
		URL: c.URL, Description: c.Description, Field: c.Field,
	}
}

// nativeBridgeEnabled 控制是否把上游内置工具调用翻译给客户端。
//
// 默认开启。部分模型（实测 claude-4.6-opus-max）在需要写文件时，上游只下发
// 「读文件」而从不下发写入，会导致客户端反复读同一个不存在的文件。
// 这种情况可以用 NATIVE_TOOL_BRIDGE=off 关掉，退回纯提示词模拟。
var nativeBridgeEnabled = !strings.EqualFold(os.Getenv("NATIVE_TOOL_BRIDGE"), "off")

// mapNativeCall 把上游内置调用映射成客户端声明的工具调用。
func mapNativeCall(c *cursor.NativeToolCall, defs []tools.Definition) (tools.Call, bool) {
	if c == nil || len(defs) == 0 || !nativeBridgeEnabled {
		return tools.Call{}, false
	}
	return tools.MapNative(toNative(c), defs)
}

// toolCallDelta 组装流式响应里的单个 tool_call 增量。
// 我们只在标签闭合后才识别出完整调用，所以一次性给全 id/name/arguments。
func toolCallDelta(index int, c tools.Call) map[string]any {
	return map[string]any{
		"index":    index,
		"id":       c.ID,
		"type":     "function",
		"function": map[string]any{"name": c.Name, "arguments": c.Arguments},
	}
}

// injectToolPrompt 把工具说明并入 system 提示，并在最后一条用户消息末尾附一句提醒。
// 没有工具时原样返回，保证普通对话的行为完全不变。
func injectToolPrompt(messages []types.Message, defs []tools.Definition, choice tools.Choice) []types.Message {
	prompt := tools.BuildSystemPrompt(defs, choice)
	if prompt == "" {
		return messages
	}

	out := make([]types.Message, len(messages))
	copy(out, messages)

	// 工具说明追加到已有 system 之后，避免打乱客户端自己的系统提示。
	// 注意只改写 Content，Images 必须原样保留——早期版本在这里整个重建了
	// 结构体，结果「带图 + 声明工具」的请求会把图片丢掉，模型只看到文字。
	injected := false
	for i, m := range out {
		if m.Role == "system" || m.Role == "developer" {
			out[i].Content = strings.TrimSpace(types.ContentToText(m.Content)) + "\n\n" + prompt
			injected = true
			break
		}
	}
	if !injected {
		out = append([]types.Message{{Role: "system", Content: prompt}}, out...)
	}

	// 末尾提醒：靠近生成点，压过上游自身 agent 提示词的影响
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == "user" {
			out[i].Content = types.ContentToText(out[i].Content) + tools.Reminder()
			break
		}
	}
	return out
}

func statusOf(errored bool) string {
	if errored {
		return "error"
	}
	return "ok"
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
