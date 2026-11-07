package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/proto"
)

func TestToolPolicyAcrossProtocols(t *testing.T) {
	const proxyURL = "http://tool-policy-test.invalid:8080"
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_TOKEN", "test-admin")
	t.Setenv("CURSOR_HTTP_PROXY", proxyURL)
	t.Setenv("TOKENIZER", "estimate")
	t.Setenv("NATIVE_TOOL_BRIDGE", "")
	config.Reset()
	t.Cleanup(config.Reset)
	auth.AddCursorToken("fake-cursor-token", "test-account", "")
	key := auth.CreateProxyKey("tool-policy-test").RawKey
	client := httpx.Client(proxyURL)
	oldTransport := client.Transport
	t.Cleanup(func() { client.Transport = oldTransport })

	const tagged = `<tool_call>{"name":"custom_first","arguments":{}}</tool_call><tool_call>{"name":"undeclared_probe","arguments":{}}</tool_call>`
	textFrame := func(s string) []byte {
		text := proto.NewWriter().Str(1, s).Finish()
		update := proto.NewWriter().Bytes(1, text).Finish()
		return cursor.EncodeConnectEnvelope(proto.NewWriter().Bytes(1, update).Finish(), false)
	}
	args := proto.NewWriter().Str(1, "/tmp/probe.txt").Str(6, tagged).Finish()
	tool := proto.NewWriter().Bytes(12, proto.NewWriter().Bytes(1, args).Finish()).Finish()
	call := proto.NewWriter().Str(1, "toolu_policy").Bytes(2, tool).Finish()
	nativeUpdate := proto.NewWriter().Bytes(2, call).Finish()
	nativeFrame := cursor.EncodeConnectEnvelope(proto.NewWriter().Bytes(1, nativeUpdate).Finish(), false)
	end := cursor.EncodeConnectEnvelope([]byte("{}"), false)
	end[0] = 2
	success := bytes.Join([][]byte{textFrame(tagged), nativeFrame, end}, nil)
	split := strings.Index(tagged, "custom_first") + len("custom")
	splitTag := bytes.Join([][]byte{textFrame(tagged[:split]), nativeFrame, textFrame(tagged[split:]), end}, nil)
	chunk := proto.NewWriter().Str(1, "first half of write").Finish()
	holder := proto.NewWriter().Bytes(3, chunk).Finish()
	inputDelta := proto.NewWriter().Str(1, "toolu_partial").Bytes(2, holder).Finish()
	inputUpdate := proto.NewWriter().Bytes(15, inputDelta).Finish()
	inputFrame := cursor.EncodeConnectEnvelope(proto.NewWriter().Bytes(1, inputUpdate).Finish(), false)
	failure := bytes.Join([][]byte{textFrame(tagged), inputFrame, end}, nil)
	textOnly := bytes.Join([][]byte{textFrame("I will use a tool."), end}, nil)

	for _, protocol := range []struct {
		name, path, tools, none, forced, forcedCustom, unknown, serial, required string
	}{
		{"openai", "/v1/chat/completions",
			`[{"type":"function","function":{"name":"custom_first"}},{"type":"function","function":{"name":"Write","parameters":{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}},"required":["file_path","content"]}}}]`,
			`,"tool_choice":"none"`, `,"tool_choice":{"type":"function","function":{"name":"Write"}}`,
			`,"tool_choice":{"type":"function","function":{"name":"custom_first"}}`,
			`,"tool_choice":{"type":"function","function":{"name":"undeclared_probe"}}`, `,"parallel_tool_calls":false`, `,"tool_choice":"required"`},
		{"anthropic", "/v1/messages",
			`[{"name":"custom_first","input_schema":{"type":"object"}},{"name":"Write","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}},"required":["file_path","content"]}}]`,
			`,"tool_choice":{"type":"none"}`, `,"tool_choice":{"type":"tool","name":"Write"}`,
			`,"tool_choice":{"type":"tool","name":"custom_first"}`,
			`,"tool_choice":{"type":"tool","name":"undeclared_probe"}`, `,"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, `,"tool_choice":{"type":"any"}`},
	} {
		for _, stream := range []bool{true, false} {
			mode := "json"
			if stream {
				mode = "sse"
			}
			for _, tc := range []struct {
				name, options, errorText string
				calls                    []string
				status                   int
				upstream                 []byte
			}{
				{"allow declared in order", "", "", []string{"custom_first", "Write"}, 200, success},
				{"split tag only becomes a call after closing", "", "", []string{"Write", "custom_first"}, 200, splitTag},
				{"none blocks native and tagged calls", protocol.none, "", nil, 200, success},
				{"forced only allows selected tool", protocol.forced, "", []string{"Write"}, 200, success},
				{"forced custom blocks native calls and preserves literal tool markup", protocol.forcedCustom, "", []string{"custom_first"}, 200, success},
				{"forced unknown rejected", protocol.unknown, "", nil, 400, success},
				{"forced tool absent", protocol.forced, "tool_choice 要求的工具调用", nil, 502, textOnly},
				{"required tool absent", protocol.required, "tool_choice 要求的工具调用", nil, 502, textOnly},
				{"serial returns first call", protocol.serial, "", []string{"custom_first"}, 200, success},
				{"serial split tag returns first complete call", protocol.serial, "", []string{"Write"}, 200, splitTag},
				{"upstream partial tool error", "", "工具参数未收齐", nil, 502, failure},
			} {
				t.Run(protocol.name+"/"+mode+"/"+tc.name, func(t *testing.T) {
					client.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
						_ = r.Body.Close()
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(tc.upstream))}, nil
					})
					streamJSON, _ := json.Marshal(stream)
					payload := `{"model":"auto","stream":` + string(streamJSON) + `,"tools":` + protocol.tools +
						`,"messages":[{"role":"user","content":"test"}]` + tc.options + `}`
					request := httptest.NewRequest(http.MethodPost, protocol.path, strings.NewReader(payload))
					request.Header.Set("Authorization", "Bearer "+key)
					response := httptest.NewRecorder()
					New().ServeHTTP(response, request)

					status := tc.status
					if stream && tc.status == 502 {
						status = 200
					}
					raw := response.Body.String()
					if response.Code != status {
						t.Fatalf("HTTP %d, want %d: %s", response.Code, status, raw)
					}
					if tc.errorText != "" && (!strings.Contains(raw, tc.errorText) || strings.Contains(raw, `"type":"message_stop"`) || strings.Contains(raw, "data: [DONE]")) {
						t.Fatalf("upstream failure treated as success: %s", raw)
					}
					var got []string
					for _, name := range []string{"custom_first", "Write", "undeclared_probe"} {
						for range strings.Count(raw, `"name":"`+name+`"`) {
							got = append(got, name)
						}
					}
					if len(got) != len(tc.calls) {
						t.Fatalf("calls = %v, want %v: %s", got, tc.calls, raw)
					}
					last := -1
					for _, name := range tc.calls {
						index := strings.Index(raw, `"name":"`+name+`"`)
						if index <= last {
							t.Fatalf("missing or reordered call %q: %s", name, raw)
						}
						last = index
					}
				})
			}
		}
	}
}
