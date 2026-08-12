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
	"cursor-proxy/internal/types"
)

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func parseMessages(raw []rawMessage) []types.Message {
	out := make([]types.Message, 0, len(raw))
	for _, m := range raw {
		var content any
		_ = json.Unmarshal(m.Content, &content)
		out = append(out, types.Message{Role: m.Role, Content: content})
	}
	return out
}

type chatBody struct {
	Model         string       `json:"model"`
	Messages      []rawMessage `json:"messages"`
	Stream        bool         `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
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
		for ev := range events {
			switch ev.Kind {
			case cursor.EventDelta:
				delta := map[string]any{}
				if ev.Text != "" {
					delta["content"] = ev.Text
				}
				if ev.Thinking != "" {
					delta["reasoning_content"] = ev.Thinking
				}
				if ev.Text != "" || ev.Thinking != "" {
					content.WriteString(ev.Text)
					reasoning.WriteString(ev.Thinking)
					writeSSE(sseChunk(id, body.Model, delta, nil))
				}
			case cursor.EventError:
				errored = true
				errMsg = ev.Message
				b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": ev.Message}})
				writeSSE("data: " + string(b) + "\n\n")
			}
		}
		writeSSE(sseChunk(id, body.Model, map[string]any{}, "stop"))

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
	for ev := range events {
		if ev.Kind == cursor.EventDelta {
			content += ev.Text
			reasoning += ev.Thinking
		} else if ev.Kind == cursor.EventError {
			lastError = ev.Message
		}
	}

	if content == "" && lastError != "" {
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
	WriteJSON(w, 200, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   body.Model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": "stop"}},
		"usage":   usagePayload(promptTokens, completionTokens),
	})
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
