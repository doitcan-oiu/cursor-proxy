package server

import (
	"bufio"
	"bytes"
	"context"
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

func relayTestReply() []byte {
	text := proto.NewWriter().Str(1, "relay output").Finish()
	update := proto.NewWriter().Bytes(1, text).Finish()
	message := proto.NewWriter().Bytes(1, update).Finish()
	end := cursor.EncodeConnectEnvelope([]byte("{}"), false)
	end[0] = 2
	return append(cursor.EncodeConnectEnvelope(message, false), end...)
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
	config.Reset()
	t.Cleanup(config.Reset)
	auth.AddCursorToken("fake-cursor-token", "test-account", "")
	key := auth.CreateProxyKey("stream-test").RawKey
	upstreamClient := httpx.Client(proxyURL)
	oldTransport := upstreamClient.Transport
	t.Cleanup(func() { upstreamClient.Transport = oldTransport })

	for _, tc := range []struct {
		name, path, start, end string
		disconnect             bool
	}{
		{"slow OpenAI completion", "/v1/chat/completions", `"role":"assistant"`, "data: [DONE]", false},
		{"slow Anthropic completion", "/v1/messages", `"type":"message_start"`, `"type":"message_stop"`, false},
		{"Anthropic relay disconnect before text", "/v1/messages", `"type":"message_start"`, "", true},
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
					select {
					case <-releaseOutput:
					case <-r.Context().Done():
						return
					}
					defer writer.Close()
					_, _ = writer.Write(relayTestReply())
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
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+tc.path,
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
					if !strings.Contains(line, tc.start) {
						t.Fatalf("unexpected start frame %q", line)
					}
					break
				}
			}
			if tc.disconnect {
				cancel()
				_ = response.Body.Close()
			} else {
				close(releaseOutput)
				remaining, err := io.ReadAll(reader)
				if err != nil || !strings.Contains(string(remaining), "relay output") || !strings.Contains(string(remaining), tc.end) || strings.Contains(string(remaining), tc.start) {
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
			if entry.InputTokens <= 0 || entry.Ms <= 0 || entry.TPS == nil {
				t.Fatalf("missing log metrics: %+v", entry)
			}
			if tc.disconnect {
				if entry.Status == "ok" || entry.HTTPStatus != 499 || entry.Tokens != 0 || entry.FirstTokenMs != nil {
					t.Fatalf("disconnect logged as output or success: %+v", entry)
				}
			} else if entry.Status != "ok" || entry.Tokens <= 0 || entry.FirstTokenMs == nil || *entry.FirstTokenMs > entry.Ms || *entry.TPS <= 0 {
				t.Fatalf("incomplete successful output metrics: %+v", entry)
			}
		})
	}

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
		if !response.failed || len(entries) != 1 || entries[0].HTTPStatus != 499 || entries[0].Tokens != 0 || entries[0].FirstTokenMs != nil {
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
}
