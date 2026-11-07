package openai

import (
	"context"
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
	"cursor-proxy/internal/store"
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

		// 工具调用与工具结果保持结构化，由 proto 层写进上游真正的
		// conversation_history。早期在这里渲染成 <tool_call> 标签和
		// 「[工具名] 结果」文本拼进正文，模型会识破那是伪造的对话记录并
		// 拒绝采信，转而重复调用同样的工具——表现为反复读同一批文件。
		switch {
		case len(m.ToolCalls) > 0:
			calls := make([]types.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, types.ToolCall{
					ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments,
				})
			}
			out = append(out, types.Message{Role: "assistant", Content: text, ToolCalls: calls})

		case m.Role == "tool":
			out = append(out, types.Message{
				Role: "tool", Content: text, Images: images,
				ToolCallID: m.ToolCallID, ToolName: m.Name,
			})

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
	ParallelTools *bool           `json:"parallel_tool_calls"`
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

func chatStreamError(message, typ, code string) string {
	var body ErrorBody
	body.Error.Message = message
	body.Error.Type = typ
	if code != "" {
		body.Error.Code = &code
	}
	payload, _ := json.Marshal(body)
	return "data: " + string(payload) + "\n\n"
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
	startedAt := time.Now()
	// 原始请求体留着：出错时记进日志，便于原样复现。
	// 只读一次，之后都用这份字节。
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		SendError(w, 400, "Failed to read request body.", "invalid_request_error", "")
		return
	}
	var body chatBody
	if err := json.Unmarshal(raw, &body); err != nil {
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

	tl := newTimeline("chat "+body.Model, startedAt)
	var timingDetail string
	defer func() { tl.done(timingDetail) }()
	tl.mark("读请求体和解 JSON")

	messages := parseMessages(body.Messages)
	// 声明了 tools 才注入工具提示词；普通对话完全不受影响。
	toolDefs := body.toolDefs()
	toolChoice := body.toolChoice()
	toolDefs = allowedToolDefinitions(toolDefs, toolChoice)
	if len(toolDefs) == 0 && requiresTool(toolChoice) {
		SendError(w, 400, "tool_choice requires a declared tool", "invalid_request_error", "invalid_tool_choice")
		return
	}
	fileTools, promptTools := prepareFileTools(toolDefs)
	messages = fileTools.remind(injectToolPrompt(messages, promptTools, toolChoice))
	tl.mark("解析消息")

	id := "chatcmpl-" + uuid.NewString()
	keyPrefix := keyPrefixFromAuth(r)
	promptTokens := tokenize.CountMessages(body.Model, messages)
	input := splitInputUsage(promptTokens, store.GetSimulatedCache())
	tl.mark("算 token")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	open := func() (*OpenedStream, *cursor.UpstreamError) {
		return OpenWithFailover(ctx, messages, body.Model, ModeFor(len(toolDefs)), len(toolDefs) > 0, body.Stream, fileTools)
	}
	var opened *OpenedStream
	var uerr *cursor.UpstreamError
	var wire *sseResponse
	var startStream func()
	if body.Stream {
		if _, ok := w.(http.Flusher); !ok {
			SendError(w, 500, "streaming unsupported", "server_error", "")
			return
		}
		wire = newSSEResponse(ctx, cancel, w)
		// 重新编码 SSE 的网关可能丢掉注释。空增量不计入正文或 token。
		wire.heartbeat = sseChunk(id, body.Model, map[string]any{}, nil)
		startStream = func() {
			if !wire.started {
				wire.write(sseChunk(id, body.Model, map[string]any{"role": "assistant"}, nil))
			}
		}
		opened, uerr = wire.awaitOpen(startedAt, startStream, open)
	} else {
		opened, uerr = open()
	}
	tl.markOpen(opened)
	if uerr != nil {
		status, message, typ, code := uerr.Status, FriendlyUpstream(uerr.Msg), "upstream_error", ""
		switch status {
		case 0, 503:
			status, message, typ, code = 503, "无可用 Cursor 账号: "+message, "server_error", "no_account_available"
		case 429:
			typ = "rate_limit_error"
		}
		if ctx.Err() == nil {
			if wire != nil && wire.started {
				wire.write(chatStreamError(message, typ, code))
			} else {
				SendError(w, status, message, typ, code)
			}
		}
		errMsg := uerr.Msg
		if ctx.Err() != nil {
			status, errMsg = clientClosedStatus, wire.disconnectMessage()
		}
		timingDetail = fmt.Sprintf(" :: 输入 %d tok / 状态 %d", promptTokens, status)
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Stream: body.Stream, KeyPrefix: keyPrefix,
			Status: "error", HTTPStatus: status, Ms: time.Since(startedAt).Milliseconds(), InputTokens: promptTokens,
			Error: trunc(errMsg, 200), Request: reqlog.SanitizeRequest(raw)})
		return
	}

	account := opened.Account
	defer opened.Stream.Close()
	events := Chain(ctx, opened.Buffered, opened.Stream)
	toolOutput := newToolOutput(toolDefs, body.ParallelTools != nil && !*body.ParallelTools, fileTools)

	if body.Stream {
		startStream()
		ticks := time.NewTicker(streamKeepalive)
		defer ticks.Stop()

		errored := false
		errMsg := ""
		terminalDelivered := false
		var content, reasoning strings.Builder
		emitText := func(text string) {
			if text == "" {
				return
			}
			if wire.write(sseChunk(id, body.Model, map[string]any{"content": text}, nil)) {
				wire.markOutput()
				content.WriteString(text)
			}
		}

		truncated := false
		gotFirst := false
		// 纯对话（客户端没声明工具）时，把写文件调用的内容边收边吐，
		// 否则长内容要等整个调用发完才一次性出现。
		live := tools.NewLiveWriter(len(toolDefs) == 0)

		for !errored {
			ev, ok := wire.next(events, ticks.C)
			if !ok {
				break
			}
			switch ev.Kind {
			case cursor.EventDelta:
				if !gotFirst {
					gotFirst = true
					tl.mark("等首个增量")
				}
				if ev.Thinking != "" && wire.write(sseChunk(id, body.Model, map[string]any{"reasoning_content": ev.Thinking}, nil)) {
					wire.markThinking()
					reasoning.WriteString(ev.Thinking)
				}
				if ev.Text != "" {
					emitText(live.Interrupt())
					emitText(toolOutput.text(ev.Text))
				}
			case cursor.EventToolInputDelta:
				emitText(live.Push(toNativePtr(ev.Tool), ev.Text))
			case cursor.EventToolCall:
				if s, handled := live.Finish(toNativePtr(ev.Tool)); handled {
					emitText(s)
					continue
				}
				emitText(toolOutput.native(ev.Tool))
			case cursor.EventEnd:
				truncated = ev.Truncated
			case cursor.EventError:
				errored = true
				errMsg = ev.Message
				terminalDelivered = wire.write(chatStreamError(FriendlyUpstream(ev.Message), "upstream_error", ""))
			}
		}

		if !errored && !wire.disconnected() {
			emitText(live.Interrupt())
		}
		tl.mark("流式输出")

		// 被时长上限掐断时报 length，别让半截回答看起来像正常收尾
		finish := "stop"
		if truncated {
			finish = "length"
		}
		if !errored && !wire.disconnected() {
			emitText(toolOutput.flush())
			if message, code := toolOutput.streamCompletionError(toolChoice, truncated, content.Len(), reasoning.Len()); message != "" {
				errored, errMsg = true, message
				terminalDelivered = wire.write(chatStreamError(errMsg, "upstream_error", code))
			}
		}
		calls := toolOutput.calls
		var deliveredCalls []tools.Call
		if len(calls) > 0 && !errored && !wire.disconnected() {
			for i, c := range calls {
				if !writeToolArgs(c.Arguments, func(first bool, part string) bool {
					if !wire.write(sseChunk(id, body.Model, map[string]any{
						"tool_calls": []map[string]any{toolCallDelta(i, c, first, part)},
					}, nil)) {
						return false
					}
					wire.markOutput()
					return true
				}) {
					break
				}
				deliveredCalls = append(deliveredCalls, c)
			}
			finish = "tool_calls"
		}
		if !errored {
			wire.write(sseChunk(id, body.Model, map[string]any{}, finish))
		}

		// 按 OpenAI 规范：仅在 stream_options.include_usage 为真时补一个
		// choices 为空、只带 usage 的收尾分片。
		thinkingTokens := tokenize.CountText(body.Model, reasoning.String())
		completionTokens := countOutputTokens(body.Model, content.String(), "", deliveredCalls) + thinkingTokens
		if !errored && body.StreamOptions != nil && body.StreamOptions.IncludeUsage && !wire.disconnected() {
			payload, _ := json.Marshal(map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": time.Now().Unix(),
				"model":   body.Model,
				"choices": []any{},
				"usage":   input.openAI(completionTokens),
			})
			wire.write("data: " + string(payload) + "\n\n")
		}
		if !errored {
			terminalDelivered = wire.write("data: [DONE]\n\n")
		}

		outcome := cursor.OutcomeSuccess
		if errored {
			outcome = cursor.OutcomeError
		}
		httpStatus := 0
		if !terminalDelivered && wire.disconnected() {
			outcome, errored, errMsg, httpStatus = cursor.OutcomeCanceled, true, wire.disconnectMessage(), clientClosedStatus
		}
		cursor.ReleaseAccount(account.ID, outcome, errMsg)
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: true, Status: statusOf(errored), Ms: time.Since(startedAt).Milliseconds(),
			HTTPStatus: httpStatus, InputTokens: promptTokens, FirstTokenMs: wire.firstTokenMs(startedAt),
			FirstThinkingMs: elapsedMs(startedAt, wire.thinkingAt), ThinkingTokens: thinkingTokens,
			Chars: utf8.RuneCountInString(content.String()), Tokens: completionTokens,
			Error: trunc(errMsg, 200), Request: requestOnError(errored, raw)})
		timingDetail = fmt.Sprintf(" :: 提示 %d tok / 输出 %d tok / %d 字",
			promptTokens, completionTokens, utf8.RuneCountInString(content.String()))
		return
	}

	content := ""
	reasoning := ""
	lastError := ""
	truncated := false
	for ev := range events {
		switch ev.Kind {
		case cursor.EventDelta:
			content += toolOutput.text(ev.Text)
			reasoning += ev.Thinking
		case cursor.EventToolCall:
			content += toolOutput.native(ev.Tool)
		case cursor.EventEnd:
			truncated = ev.Truncated
		case cursor.EventError:
			lastError = ev.Message
		}
	}
	if ctx.Err() != nil {
		cursor.ReleaseAccount(account.ID, cursor.OutcomeCanceled, "")
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
			Status: "error", HTTPStatus: clientClosedStatus, Ms: time.Since(startedAt).Milliseconds(),
			InputTokens: promptTokens, Error: clientDisconnected().Msg, Request: reqlog.SanitizeRequest(raw)})
		return
	}

	content += toolOutput.flush()
	toolCalls := toolOutput.calls
	if lastError == "" {
		lastError, _ = toolOutput.completionError(toolChoice, truncated)
	}
	if lastError != "" {
		cursor.ReleaseAccount(account.ID, cursor.OutcomeError, lastError)
		reqlog.Record(reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
			Stream: false, Status: "error", HTTPStatus: 502, Ms: time.Since(startedAt).Milliseconds(), InputTokens: promptTokens,
			Error: trunc(lastError, 200), Request: reqlog.SanitizeRequest(raw)})
		SendError(w, 502, FriendlyUpstream(lastError), "upstream_error", "")
		return
	}

	completionTokens := countOutputTokens(body.Model, content, reasoning, toolCalls)

	message := map[string]any{"role": "assistant", "content": content}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	finishReason := "stop"
	if truncated {
		finishReason = "length"
	}
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
	entry := reqlog.Entry{Kind: "chat", Model: body.Model, Account: account.Label, KeyPrefix: keyPrefix,
		Status: "ok", InputTokens: promptTokens, Chars: utf8.RuneCountInString(content), Tokens: completionTokens}
	response := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   body.Model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   input.openAI(completionTokens),
	}
	outcome := cursor.OutcomeSuccess
	if ctx.Err() != nil || WriteJSON(w, 200, response) != nil {
		outcome = cursor.OutcomeCanceled
		entry.Status, entry.HTTPStatus, entry.Error = "error", clientClosedStatus, clientDisconnected().Msg
		entry.Chars, entry.Tokens, entry.Request = 0, 0, reqlog.SanitizeRequest(raw)
	}
	entry.Ms = time.Since(startedAt).Milliseconds()
	cursor.ReleaseAccount(account.ID, outcome, "")
	reqlog.Record(entry)
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
//
// 两边的类型值是同一批字符串（都取自上游的规范名），所以直接转换即可。
// 早期这里是一个手写的 switch，每加一个工具就得记得同步改，漏改的表现是
// 工具被悄悄归成「读文件」——不如让编译器和命名约定来保证一致。
func toNative(c *cursor.NativeToolCall) tools.Native {
	if c == nil {
		return tools.Native{}
	}
	todos := make([]tools.TodoItem, 0, len(c.Todos))
	for _, t := range c.Todos {
		todos = append(todos, tools.TodoItem{ID: t.ID, Content: t.Content, Status: t.Status})
	}
	return tools.Native{
		ID: c.ID, Kind: tools.NativeKind(c.Kind), Path: c.Path, Command: c.Command,
		Pattern: c.Pattern, Content: c.Content, Prompt: c.Prompt,
		Glob: c.Glob, OutputMode: c.OutputMode, Offset: c.Offset, Limit: c.Limit,
		URL: c.URL, Description: c.Description, Field: c.Field,
		Name: c.Name, Todos: todos,
	}
}

// mapNativeCall 把上游内置调用映射成客户端声明的工具调用。
func mapNativeCall(c *cursor.NativeToolCall, defs []tools.Definition) (tools.Call, bool) {
	if c == nil || len(defs) == 0 || !tools.NativeBridgeEnabled() {
		return tools.Call{}, false
	}
	return tools.MapNative(toNative(c), defs)
}

func allowedToolDefinitions(defs []tools.Definition, choice tools.Choice) []tools.Definition {
	switch choice.Mode {
	case "none":
		return nil
	case "function":
		for _, def := range defs {
			if def.Name == choice.Name {
				return []tools.Definition{def}
			}
		}
		return nil
	default:
		return defs
	}
}

const missingToolCall = "上游未返回 tool_choice 要求的工具调用"

func requiresTool(choice tools.Choice) bool {
	return choice.Mode == "function" || choice.Mode == "required"
}

// toolOutput 按上游出现的顺序收集原生调用和文本调用。
type toolOutput struct {
	defs             []tools.Definition
	fileTools        fileToolBridge
	single           bool
	mixedFileTools   bool
	unsafeNativeEdit bool
	scanner          *tools.Scanner
	calls            []tools.Call
}

func newToolOutput(defs []tools.Definition, single bool, fileTools fileToolBridge) *toolOutput {
	out := &toolOutput{defs: defs, single: single, fileTools: fileTools, mixedFileTools: tools.FileToolsNeedDistinctCalls(defs)}
	if len(defs) > 0 {
		out.scanner = tools.NewScanner(func(call tools.Call) bool {
			for _, def := range defs {
				if def.Name == call.Name {
					out.add(call)
					return true
				}
			}
			return false
		})
	}
	return out
}

func (o *toolOutput) add(call tools.Call) {
	if !o.single || len(o.calls) == 0 {
		o.calls = append(o.calls, call)
	}
}

func (o *toolOutput) text(s string) string {
	if o.scanner == nil {
		return s
	}
	return o.scanner.Push(s)
}

func (o *toolOutput) native(call *cursor.NativeToolCall) string {
	if call != nil && call.Kind == cursor.ToolMCP {
		if mapped, ok := o.fileTools.mapCall(call); ok {
			o.add(mapped)
		}
		return ""
	}
	if call != nil && call.Kind == cursor.ToolWriteFile && o.mixedFileTools {
		o.unsafeNativeEdit = true
		return ""
	}
	if mapped, ok := mapNativeCall(call, o.defs); ok {
		o.add(mapped)
		return ""
	}
	return tools.NativeToText(toNative(call))
}

func (o *toolOutput) completionError(choice tools.Choice, truncated bool) (string, string) {
	if truncated || len(o.calls) > 0 {
		return "", ""
	}
	if o.unsafeNativeEdit {
		return "上游文件操作缺少整文件写入或局部替换的区分，未执行", "ambiguous_file_operation"
	}
	if requiresTool(choice) {
		return missingToolCall, "tool_choice_not_satisfied"
	}
	return "", ""
}

func (o *toolOutput) streamCompletionError(choice tools.Choice, truncated bool, textBytes, thinkingBytes int) (string, string) {
	if message, code := o.completionError(choice, truncated); message != "" {
		return message, code
	}
	if !truncated && len(o.calls) == 0 && textBytes == 0 && thinkingBytes > 0 {
		return "上游仅返回思考内容，未返回正文或工具调用", "thinking_only_response"
	}
	return "", ""
}

func (o *toolOutput) flush() string {
	if o.scanner == nil {
		return ""
	}
	return o.scanner.Flush()
}

// toolCallDelta 在首个片段发送工具身份，后续片段仅补充参数。
func toolCallDelta(index int, c tools.Call, first bool, part string) map[string]any {
	function := map[string]any{"arguments": part}
	delta := map[string]any{"index": index, "function": function}
	if first {
		delta["id"] = c.ID
		delta["type"] = "function"
		function["name"] = c.Name
	}
	return delta
}

// injectToolPrompt 把工具说明并入 system 提示，并在最后一条用户消息末尾附一句提醒。
// 没有工具时原样返回，保证普通对话的行为完全不变。
//
// 只给自定义工具注入 <tool_call> 协议；原生工具走桥接，只有显式要求调用工具时才补充提示。
func injectToolPrompt(messages []types.Message, defs []tools.Definition, choice tools.Choice) []types.Message {
	prompt := tools.BuildSystemPrompt(tools.WithoutNativeEquivalent(defs), choice)
	reminder := ""
	if prompt != "" {
		reminder = tools.Reminder()
	} else if len(defs) > 0 && choice.Mode == "function" {
		prompt = fmt.Sprintf("For the current turn, use the built-in tool corresponding to the client's %q tool. Provide the tool call, not just a description of the action.", choice.Name)
	} else if len(defs) > 0 && choice.Mode == "required" {
		names := make([]string, 0, len(defs))
		for _, def := range defs {
			names = append(names, def.Name)
		}
		prompt = "For the current turn, make at least one built-in tool call corresponding to a tool available to the client: " + strings.Join(names, ", ") + "."
	}
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
			out[i].Content = types.ContentToText(out[i].Content) + reminder
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

// maxRequestBody 是允许读入的请求体上限。带图请求可能很大，但也不能无上限。
const maxRequestBody = 64 << 20

// requestOnError 只在出错时返回留档用的请求体。
// 成功的请求不留，避免把内存耗在正常流量上。
func requestOnError(errored bool, raw []byte) string {
	if !errored {
		return ""
	}
	return reqlog.SanitizeRequest(raw)
}
