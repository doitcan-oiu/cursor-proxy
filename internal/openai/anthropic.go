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

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicBody struct {
	Model    string             `json:"model"`
	System   json.RawMessage    `json:"system"`
	Messages []anthropicMessage `json:"messages"`
	Stream   bool               `json:"stream"`
}

func blocksToText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []anthropicBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" || b.Text != "" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
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

		for ev := range events {
			if ev.Kind == cursor.EventDelta && ev.Text != "" {
				content.WriteString(ev.Text)
				send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": ev.Text}})
			} else if ev.Kind == cursor.EventError {
				errored = true
				errMsg = ev.Message
			}
		}
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		stopReason := "end_turn"
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
	for ev := range events {
		if ev.Kind == cursor.EventDelta {
			content += ev.Text
		} else if ev.Kind == cursor.EventError {
			lastError = ev.Message
		}
	}
	if content == "" && lastError != "" {
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
	WriteJSON(w, 200, map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content":       []map[string]any{{"type": "text", "text": content}},
		"stop_reason":   "end_turn",
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
