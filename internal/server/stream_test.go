package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/reqlog"
)

type streamTestTransport func(*http.Request) (*http.Response, error)

func (f streamTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func relayTestDelta(field int, text string) []byte {
	delta := proto.NewWriter().Str(1, text).Finish()
	update := proto.NewWriter().Bytes(field, delta).Finish()
	message := proto.NewWriter().Bytes(1, update).Finish()
	return cursor.EncodeConnectEnvelope(message, false)
}

func relayTestEnd() []byte {
	end := cursor.EncodeConnectEnvelope([]byte("{}"), false)
	end[0] = 2
	return end
}

func relayTestReply() []byte {
	return append(relayTestDelta(1, "relay output"), relayTestEnd()...)
}

type failOutputFlush struct {
	*httptest.ResponseRecorder
	failed bool
}

func (w *failOutputFlush) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("relay output")) {
		w.failed = true
		return len(p), nil
	}
	return w.ResponseRecorder.Write(p)
}

func (w *failOutputFlush) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *failOutputFlush) FlushError() error {
	if w.failed {
		return io.ErrClosedPipe
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestRelayStartsBeforeOutputAndTracksDelivery(t *testing.T) {
	const proxyURL = "http://stream-relay-test.invalid:8080"
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_TOKEN", "test-admin")
	t.Setenv("CURSOR_HTTP_PROXY", proxyURL)
	t.Setenv("TOKENIZER", "estimate")
	t.Setenv("PROXY_MAX_ATTEMPTS", "2")
	config.Reset()
	t.Cleanup(config.Reset)
	auth.AddCursorToken("fake-cursor-token", "test-account", "")
	key := auth.CreateProxyKey("stream-test").RawKey
	upstreamClient := httpx.Client(proxyURL)
	oldTransport := upstreamClient.Transport
	t.Cleanup(func() { upstreamClient.Transport = oldTransport })

	type protocol struct {
		name, path, start, end, thinking, keepalive string
	}
	openAI := protocol{"OpenAI", "/v1/chat/completions", `"role":"assistant"`, "data: [DONE]", `"reasoning_content":"relay thinking"`, `"delta":{},"finish_reason":null`}
	anthropic := protocol{"Anthropic", "/v1/messages", `"type":"message_start"`, `"type":"message_stop"`, `"thinking":"relay thinking"`, `"type":"ping"`}
	for _, tc := range []struct {
		name          string
		protocol      protocol
		thinking      bool
		noText        bool
		disconnect    bool
		waitKeepalive bool
	}{
		{name: "slow OpenAI completion", protocol: openAI},
		{name: "slow Anthropic completion", protocol: anthropic},
		{name: "OpenAI relay disconnect after keepalive before output", protocol: openAI, disconnect: true, waitKeepalive: true},
		{name: "Anthropic relay disconnect before text", protocol: anthropic, disconnect: true},
		{name: "OpenAI streams thinking before text", protocol: openAI, thinking: true},
		{name: "Anthropic streams thinking before text", protocol: anthropic, thinking: true},
		{name: "OpenAI thinking without text is an error", protocol: openAI, thinking: true, noText: true},
		{name: "Anthropic thinking without text is an error", protocol: anthropic, thinking: true, noText: true},
		{name: "OpenAI relay disconnect after thinking", protocol: openAI, thinking: true, disconnect: true},
		{name: "Anthropic relay disconnect after thinking", protocol: anthropic, thinking: true, disconnect: true, waitKeepalive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reqlog.Clear()
			t.Cleanup(reqlog.Clear)
			releaseOutput := make(chan struct{})
			upstreamCanceled := make(chan struct{})
			upstreamClient.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
				reader, writer := io.Pipe()
				context.AfterFunc(r.Context(), func() {
					_ = r.Body.Close()
					_ = writer.Close()
					close(upstreamCanceled)
				})
				go func() {
					defer writer.Close()
					if tc.thinking {
						if _, err := writer.Write(relayTestDelta(4, "relay thinking")); err != nil {
							return
						}
					}
					select {
					case <-releaseOutput:
					case <-r.Context().Done():
						return
					}
					if tc.noText {
						_, _ = writer.Write(relayTestEnd())
					} else {
						_, _ = writer.Write(relayTestReply())
					}
				}()
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
			})

			finished := make(chan struct{})
			handler := New()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+tc.protocol.path,
				strings.NewReader(`{"model":"auto","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+key)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("HTTP status = %d", response.StatusCode)
			}
			reader := bufio.NewReader(response.Body)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatalf("reading start frame before model output: %v", err)
				}
				if strings.HasPrefix(line, "data: ") {
					if !strings.Contains(line, tc.protocol.start) {
						t.Fatalf("unexpected start frame %q", line)
					}
					break
				}
			}
			if tc.thinking {
				// Wait for reasoning while the upstream is still blocked from sending text or a terminator.
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatalf("reading thinking before model output: %v", err)
					}
					if strings.HasPrefix(line, "data: ") && strings.Contains(line, tc.protocol.thinking) {
						break
					}
				}
			}
			if tc.waitKeepalive {
				// Require a protocol data frame while upstream text is still withheld.
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatalf("reading %s keepalive: %v", tc.protocol.name, err)
					}
					if strings.HasPrefix(line, "data: ") && strings.Contains(line, tc.protocol.keepalive) {
						if !json.Valid([]byte(strings.TrimPrefix(line, "data: "))) {
							t.Fatalf("invalid %s keepalive frame: %q", tc.protocol.name, line)
						}
						break
					}
				}
			}
			if tc.disconnect {
				cancel()
				_ = response.Body.Close()
			} else {
				close(releaseOutput)
				remaining, err := io.ReadAll(reader)
				body := string(remaining)
				if tc.noText {
					if err != nil || !strings.Contains(body, `"error"`) || !strings.Contains(body, "上游仅返回思考内容") || strings.Contains(body, tc.protocol.end) {
						t.Fatalf("expected error after reasoning without text, got: %s (%v)", body, err)
					}
				} else if err != nil || !strings.Contains(body, "relay output") || !strings.Contains(body, tc.protocol.end) || strings.Contains(body, tc.protocol.start) {
					t.Fatalf("missing downstream output: %s (%v)", remaining, err)
				}
			}
			for _, signal := range []<-chan struct{}{finished, upstreamCanceled} {
				select {
				case <-signal:
				case <-time.After(2 * time.Second):
					t.Fatal("relay or upstream request remained open")
				}
			}
			entries := reqlog.List(0)
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1", len(entries))
			}
			entry := entries[0]
			if entry.InputTokens <= 0 || entry.Ms < 0 || (entry.Ms > 0 && entry.TPS == nil) {
				t.Fatalf("missing log metrics: %+v", entry)
			}
			if tc.thinking {
				if entry.FirstThinkingMs == nil || entry.ThinkingTokens <= 0 || entry.Tokens < entry.ThinkingTokens ||
					(entry.FirstTokenMs != nil && *entry.FirstThinkingMs > *entry.FirstTokenMs) {
					t.Fatalf("missing or out-of-order thinking metrics: %+v", entry)
				}
			} else if entry.FirstThinkingMs != nil || entry.ThinkingTokens != 0 {
				t.Fatalf("non-thinking frame counted as thinking: %+v", entry)
			}
			if tc.disconnect {
				stage := "已发起始帧，尚无输出"
				if tc.thinking {
					stage = "仅已写出思考"
				}
				if entry.Status == "ok" || entry.HTTPStatus != 499 || entry.Chars != 0 || entry.FirstTokenMs != nil ||
					(!tc.thinking && entry.Tokens != 0) || !strings.Contains(entry.Error, stage) {
					t.Fatalf("disconnect logged as visible output or success: %+v", entry)
				}
			} else if tc.noText {
				if entry.Status == "ok" || entry.HTTPStatus == 499 || entry.Chars != 0 || entry.FirstTokenMs != nil ||
					!strings.Contains(entry.Error, "上游仅返回思考内容") {
					t.Fatalf("incomplete thinking-only reply logged as visible output or success: %+v", entry)
				}
			} else if entry.Status != "ok" || entry.Tokens <= 0 || entry.FirstTokenMs == nil || *entry.FirstTokenMs > entry.Ms ||
				(entry.TPS != nil && *entry.TPS <= 0) {
				t.Fatalf("incomplete successful output metrics: %+v", entry)
			}
		})
	}

	t.Run("nonstream retries a thinking-only response", func(t *testing.T) {
		reqlog.Clear()
		t.Cleanup(reqlog.Clear)
		for _, p := range []protocol{openAI, anthropic} {
			t.Run(p.name, func(t *testing.T) {
				attempts := 0
				upstreamClient.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
					_ = r.Body.Close()
					attempts++
					reply := relayTestReply()
					if attempts == 1 {
						reply = append(relayTestDelta(4, "relay thinking"), relayTestEnd()...)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(reply))}, nil
				})
				request := httptest.NewRequest(http.MethodPost, p.path,
					strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}]}`))
				request.Header.Set("Authorization", "Bearer "+key)
				response := httptest.NewRecorder()
				New().ServeHTTP(response, request)
				body := response.Body.String()
				if attempts != 2 || response.Code != 200 || !strings.Contains(body, "relay output") || strings.Contains(body, "relay thinking") {
					t.Fatalf("expected second attempt without first attempt's thinking, got %d attempts, status %d, body %s", attempts, response.Code, body)
				}
			})
		}
	})

	t.Run("failed flush does not count output", func(t *testing.T) {
		reqlog.Clear()
		t.Cleanup(reqlog.Clear)
		upstreamClient.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
			_ = r.Body.Close()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(relayTestReply()))}, nil
		})
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Authorization", "Bearer "+key)
		response := &failOutputFlush{ResponseRecorder: httptest.NewRecorder()}
		New().ServeHTTP(response, request)
		entries := reqlog.List(0)
		if !response.failed || len(entries) != 1 || entries[0].HTTPStatus != 499 || entries[0].Tokens != 0 || entries[0].FirstTokenMs != nil ||
			!strings.Contains(entries[0].Error, "刷新") {
			t.Fatalf("failed flush recorded as delivered: %+v", entries)
		}
	})

	t.Run("fast failure keeps HTTP status", func(t *testing.T) {
		upstreamClient.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
			_ = r.Body.Close()
			return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("invalid model"))}, nil
		})
		for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
			request := httptest.NewRequest(http.MethodPost, path,
				strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Authorization", "Bearer "+key)
			response := httptest.NewRecorder()
			New().ServeHTTP(response, request)
			if response.Code != 400 || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("%s: got status %d, type %s", path, response.Code, response.Header().Get("Content-Type"))
			}
		}
	})

	t.Run("long write is delivered as one tool call with bounded deltas", func(t *testing.T) {
		t.Setenv("NATIVE_TOOL_BRIDGE", "")
		const path = "/tmp/tool-probe.txt"
		content := strings.Repeat("汉字🌊 quotes=\" slash=\\\n", 2000)
		args := proto.NewWriter().Str(1, path).Str(6, content).Finish()
		tool := proto.NewWriter().Bytes(12, proto.NewWriter().Bytes(1, args).Finish()).Finish()
		call := proto.NewWriter().Str(1, "toolu_probe").Bytes(2, tool).Finish()
		update := proto.NewWriter().Bytes(2, call).Finish()
		message := proto.NewWriter().Bytes(1, update).Finish()
		end := cursor.EncodeConnectEnvelope([]byte("{}"), false)
		end[0] = 2
		stream := append(cursor.EncodeConnectEnvelope(message, false), end...)
		upstreamClient.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
			_ = r.Body.Close()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(stream))}, nil
		})
		const schema = `{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}},"required":["file_path","content"]}`
		for _, tc := range []struct{ route, toolDef, finish string }{
			{"/v1/messages", `{"name":"Write","input_schema":` + schema + `}`, `"stop_reason":"tool_use"`},
			{"/v1/chat/completions", `{"type":"function","function":{"name":"Write","parameters":` + schema + `}}`, `"finish_reason":"tool_calls"`},
		} {
			request := httptest.NewRequest(http.MethodPost, tc.route,
				strings.NewReader(`{"model":"auto","stream":true,"messages":[{"role":"user","content":"write"}],"tools":[`+tc.toolDef+`]}`))
			request.Header.Set("Authorization", "Bearer "+key)
			response := httptest.NewRecorder()
			New().ServeHTTP(response, request)
			if response.Code != 200 || !strings.Contains(response.Body.String(), tc.finish) {
				t.Fatalf("%s: expected a tool stop, got HTTP %d", tc.route, response.Code)
			}
			var joined strings.Builder
			identities, fragments := 0, 0
			for _, line := range strings.Split(response.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				if len(line) >= 64<<10 {
					t.Fatalf("%s: SSE line has %d bytes", tc.route, len(line))
				}
				var event struct {
					ContentBlock struct{ ID, Name string } `json:"content_block"`
					Delta        struct {
						PartialJSON string `json:"partial_json"`
					}
					Choices []struct {
						Delta struct {
							ToolCalls []struct {
								ID       string
								Function struct{ Name, Arguments string }
							} `json:"tool_calls"`
						}
					}
				}
				if err := json.Unmarshal([]byte(line[len("data: "):]), &event); err != nil {
					t.Fatal(err)
				}
				if event.ContentBlock.ID != "" {
					identities++
				}
				part := event.Delta.PartialJSON
				for _, choice := range event.Choices {
					for _, tool := range choice.Delta.ToolCalls {
						if tool.ID != "" {
							identities++
						}
						part += tool.Function.Arguments
					}
				}
				if part != "" {
					fragments++
					joined.WriteString(part)
				}
			}
			var reconstructed struct {
				FilePath string `json:"file_path"`
				Content  string
			}
			err := json.Unmarshal([]byte(joined.String()), &reconstructed)
			if err != nil || identities != 1 || fragments < 2 || reconstructed.FilePath != path || reconstructed.Content != content {
				t.Fatalf("%s: tool identities=%d fragments=%d path=%q contentBytes=%d err=%v", tc.route, identities, fragments, reconstructed.FilePath, len(reconstructed.Content), err)
			}
		}
	})
}
