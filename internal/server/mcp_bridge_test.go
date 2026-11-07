package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/proto"
)

func TestCursorMCPToolsAcrossProtocols(t *testing.T) {
	const proxyURL = "http://cursor-mcp-bridge-test.invalid:8080"
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("ADMIN_TOKEN", "test-admin")
	t.Setenv("CURSOR_HTTP_PROXY", proxyURL)
	t.Setenv("TOKENIZER", "estimate")
	t.Setenv("NATIVE_TOOL_BRIDGE", "")
	config.Reset()
	t.Cleanup(config.Reset)
	auth.AddCursorToken("fake-cursor-token", "test-account", "")
	key := auth.CreateProxyKey("cursor-mcp-test").RawKey
	client := httpx.Client(proxyURL)
	oldTransport := client.Transport
	t.Cleanup(func() { client.Transport = oldTransport })

	const path = "/tmp/cursor-mcp/probe.txt"
	content := strings.Repeat("文件片段🌊 \"quote\" \\slash\n", 2000)
	tools := []struct {
		name    string
		args    map[string]any
		schema  string
		wrapped bool
	}{
		{"Write", map[string]any{"file_path": path, "content": content},
			`{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}},"required":["file_path","content"]}`, true},
		{"Edit", map[string]any{"file_path": path, "old_string": "before", "new_string": "after", "replace_all": true},
			`{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}},"required":["file_path","old_string","new_string"]}`, true},
		{"LS", map[string]any{"path": "/tmp/cursor-mcp"},
			`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`, true},
		{"Read", map[string]any{"file_path": path},
			`{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}`, false},
	}

	for _, protocol := range []struct{ name, route, stop, forced string }{
		{"anthropic", "/v1/messages", `"stop_reason":"tool_use"`, `{"type":"tool","name":"Edit"}`},
		{"openai", "/v1/chat/completions", `"finish_reason":"tool_calls"`, `{"type":"function","function":{"name":"Edit"}}`},
	} {
		for _, forceEdit := range []bool{false, true} {
			caseName := protocol.name + "/all"
			if forceEdit {
				caseName = protocol.name + "/force-edit"
			}
			t.Run(caseName, func(t *testing.T) {
				var downstreamTools []any
				var wantCalls []string
				wantSchemas := make(map[string]string)
				for _, tool := range tools {
					def := map[string]any{"name": tool.name, "description": "regression:" + tool.name}
					if protocol.name == "anthropic" {
						def["input_schema"] = json.RawMessage(tool.schema)
						downstreamTools = append(downstreamTools, def)
					} else {
						def["parameters"] = json.RawMessage(tool.schema)
						downstreamTools = append(downstreamTools, map[string]any{"type": "function", "function": def})
					}
					if forceEdit && tool.name != "Edit" {
						continue
					}
					wantCalls = append(wantCalls, tool.name)
					if tool.wrapped {
						wantSchemas[tool.name] = tool.schema
					}
				}
				client.Transport = streamTestTransport(func(r *http.Request) (*http.Response, error) {
					registrations, err := readMCPTestRegistrations(r.Body, wantSchemas)
					if err != nil {
						_ = r.Body.Close()
						return nil, err
					}
					frames := make([][]byte, 0, len(tools)+3)
					edit := registrations["Edit"]
					// Reject both an unregistered tool and a registered name from a different provider.
					frames = append(frames,
						mcpTestCall("toolu_unknown", mcpTestRegistration{name: "unregistered_probe", provider: edit.provider}, map[string]any{}),
						mcpTestCall("toolu_wrong_provider", mcpTestRegistration{name: edit.name, provider: "other_provider", toolName: edit.toolName}, map[string]any{}),
					)
					for _, tool := range tools {
						if tool.wrapped {
							if registration, ok := registrations[tool.name]; ok {
								frames = append(frames, mcpTestCall("toolu_"+tool.name, registration, tool.args))
							}
						} else {
							args := proto.NewWriter().Str(1, path).Finish()
							frames = append(frames, mcpTestStarted("toolu_"+tool.name, 8, args))
						}
					}
					end := cursor.EncodeConnectEnvelope([]byte("{}"), false)
					end[0] = 2
					frames = append(frames, end)
					reader, writer := io.Pipe()
					go func() {
						defer r.Body.Close()
						defer writer.Close()
						if err := exchangeMCPTestDiscovery(r.Body, writer, registrations, wantSchemas); err != nil {
							_ = writer.CloseWithError(err)
							return
						}
						_, _ = writer.Write(bytes.Join(frames, nil))
					}()
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: reader}, nil
				})
				body := map[string]any{
					"model": "auto", "stream": true, "tools": downstreamTools,
					"messages": []map[string]string{{"role": "user", "content": "test client tool bridge"}},
				}
				if forceEdit {
					body["tool_choice"] = json.RawMessage(protocol.forced)
				}
				payload, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				request := httptest.NewRequestWithContext(ctx, http.MethodPost, protocol.route, bytes.NewReader(payload))
				request.Header.Set("Authorization", "Bearer "+key)
				response := httptest.NewRecorder()
				New().ServeHTTP(response, request)
				if response.Code != 200 || !strings.Contains(response.Body.String(), protocol.stop) {
					t.Fatalf("HTTP %d, expected a tool-call stop: %.1000s", response.Code, response.Body.String())
				}
				if strings.Contains(response.Body.String(), "get_mcp_tools") || strings.Contains(response.Body.String(), "toolu_discovery") {
					t.Fatal("internal MCP discovery leaked into the API response")
				}
				calls := readMCPTestSSECalls(t, response.Body.String())
				if len(calls) != len(wantCalls) {
					t.Fatalf("received %d tool calls, want %v", len(calls), wantCalls)
				}
				for i, call := range calls {
					if call.name != wantCalls[i] || call.id != "toolu_"+call.name {
						t.Fatalf("call %d = %s/%s, want %s", i, call.name, call.id, wantCalls[i])
					}
					var args map[string]any
					if err := json.Unmarshal([]byte(call.parts.String()), &args); err != nil {
						t.Fatalf("%s: invalid reconstructed arguments: %v", call.name, err)
					}
					for _, tool := range tools {
						if tool.name == call.name && !reflect.DeepEqual(args, tool.args) {
							t.Fatalf("%s: arguments changed during relay (received %d JSON bytes)", call.name, call.parts.Len())
						}
					}
					if call.name == "Write" && call.fragments < 2 {
						t.Fatal("long write must use multiple input deltas")
					}
				}
			})
		}
	}
}

type mcpTestRegistration struct {
	name, provider, toolName string
}

func readMCPTestRegistrations(r io.Reader, wantSchemas map[string]string) (map[string]mcpTestRegistration, error) {
	payload, err := readMCPTestPayload(r)
	if err != nil {
		return nil, err
	}
	run := proto.Decode(proto.FirstBytes(proto.Decode(payload), 1))
	registry := proto.Decode(proto.FirstBytes(run, 4))
	return decodeMCPTestDefinitions(registry[1], wantSchemas)
}

func readMCPTestPayload(r io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[1:])
	if header[0] != 0 || size > 1<<20 {
		return nil, fmt.Errorf("unexpected Cursor request frame: %v", header)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func decodeMCPTestDefinitions(definitions []proto.Field, wantSchemas map[string]string) (map[string]mcpTestRegistration, error) {
	if len(definitions) != len(wantSchemas) {
		return nil, fmt.Errorf("registered %d client tools, want %d", len(definitions), len(wantSchemas))
	}
	out := make(map[string]mcpTestRegistration, len(definitions))
	wireNames := make(map[string]bool, len(definitions))
	for _, def := range definitions {
		fields := proto.Decode(def.Bytes)
		_, name, _ := strings.Cut(proto.FirstString(fields, 2), "regression:")
		want, ok := wantSchemas[name]
		if !ok || out[name].name != "" {
			return nil, fmt.Errorf("unexpected or duplicate tool description %q", proto.FirstString(fields, 2))
		}
		wireName := proto.FirstString(fields, 1)
		if wireName == "" || wireNames[wireName] {
			return nil, fmt.Errorf("invalid or duplicate wire name for %s: %q", name, wireName)
		}
		if !mcpTestSchemaMatches(fields, want) {
			return nil, fmt.Errorf("schema for %s was not registered intact", name)
		}
		wireNames[wireName] = true
		out[name] = mcpTestRegistration{wireName, proto.FirstString(fields, 4), proto.FirstString(fields, 5)}
	}
	return out, nil
}

func exchangeMCPTestDiscovery(r io.Reader, w io.Writer, initial map[string]mcpTestRegistration, wantSchemas map[string]string) error {
	const execID = "test-discover"
	const execNumber = 41
	if _, err := w.Write(mcpTestStarted("toolu_discovery", 44, nil)); err != nil {
		return err
	}
	query := proto.NewWriter()
	for _, registration := range initial {
		query.Str(1, registration.provider)
	}
	exec := proto.NewWriter().Int32(1, execNumber).Str(15, execID).Bytes(36, query.Finish())
	if _, err := w.Write(cursor.EncodeConnectEnvelope(proto.NewWriter().Bytes(2, exec.Finish()).Finish(), false)); err != nil {
		return err
	}
	payload, err := readMCPTestPayload(r)
	if err != nil {
		return err
	}
	reply := proto.Decode(proto.FirstBytes(proto.Decode(payload), 2))
	if ids := reply[1]; len(ids) != 1 || ids[0].Varint != execNumber || proto.FirstString(reply, 15) != execID {
		return fmt.Errorf("unexpected MCP discovery reply identity")
	}
	result := proto.Decode(proto.FirstBytes(reply, 36))
	success := proto.Decode(proto.FirstBytes(result, 1))
	var definitions []proto.Field
	for _, field := range success[1] {
		server := proto.Decode(field.Bytes)
		if proto.FirstString(server, 7) != "connected" {
			return fmt.Errorf("MCP provider discovery did not report connected")
		}
		definitions = append(definitions, server[5]...)
	}
	discovered, err := decodeMCPTestDefinitions(definitions, wantSchemas)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(discovered, initial) {
		return fmt.Errorf("MCP discovery did not advertise the initial tool names")
	}
	return nil
}

func mcpTestSchemaMatches(fields map[int][]proto.Field, raw string) bool {
	var want any
	if json.Unmarshal([]byte(raw), &want) != nil {
		return false
	}
	if schema := proto.FirstBytes(fields, 6); len(schema) > 0 {
		var got any
		return json.Unmarshal(schema, &got) == nil && reflect.DeepEqual(got, want)
	}
	return reflect.DeepEqual(mcpTestValue(proto.FirstBytes(fields, 3)), want)
}

// These schemas use only strings, objects and arrays from google.protobuf.Value.
func mcpTestValue(raw []byte) any {
	value := proto.Decode(raw)
	if text := proto.FirstBytes(value, 3); text != nil {
		return string(text)
	}
	if object := proto.FirstBytes(value, 5); object != nil {
		out := map[string]any{}
		for _, field := range proto.Decode(object)[1] {
			entry := proto.Decode(field.Bytes)
			out[proto.FirstString(entry, 1)] = mcpTestValue(proto.FirstBytes(entry, 2))
		}
		return out
	}
	if array := proto.FirstBytes(value, 6); array != nil {
		out := []any{}
		for _, field := range proto.Decode(array)[1] {
			out = append(out, mcpTestValue(field.Bytes))
		}
		return out
	}
	return nil
}

func mcpTestStarted(id string, field int, args []byte) []byte {
	tool := proto.NewWriter().Bytes(field, proto.NewWriter().Bytes(1, args).Finish()).Finish()
	call := proto.NewWriter().Str(1, id).Bytes(2, tool).Finish()
	update := proto.NewWriter().Bytes(2, call).Finish()
	return cursor.EncodeConnectEnvelope(proto.NewWriter().Bytes(1, update).Finish(), false)
}

func mcpTestCall(id string, registration mcpTestRegistration, args map[string]any) []byte {
	mcp := proto.NewWriter().Str(1, registration.name).Str(3, id).
		Str(4, registration.provider).Str(5, registration.toolName)
	for name, arg := range args {
		value := proto.NewWriter()
		switch v := arg.(type) {
		case string:
			value.Str(3, v)
		case bool:
			flag := 0
			if v {
				flag = 1
			}
			value.Int32(4, flag)
		default:
			panic(fmt.Sprintf("unsupported MCP test argument %T", arg))
		}
		entry := proto.NewWriter().Str(1, name).Bytes(2, value.Finish()).Finish()
		mcp.Bytes(2, entry)
	}
	return mcpTestStarted(id, 15, mcp.Finish())
}

type mcpTestSSECall struct {
	id, name  string
	parts     strings.Builder
	fragments int
}

func readMCPTestSSECalls(t *testing.T, body string) []*mcpTestSSECall {
	t.Helper()
	var ordered []*mcpTestSSECall
	byIndex := make(map[int]*mcpTestSSECall)
	add := func(index int, id, name, part string) {
		if id != "" {
			if byIndex[index] != nil {
				t.Fatalf("duplicate tool identity at SSE index %d", index)
			}
			call := &mcpTestSSECall{id: id, name: name}
			byIndex[index] = call
			ordered = append(ordered, call)
		}
		if part != "" {
			if byIndex[index] == nil {
				t.Fatalf("arguments before tool identity at SSE index %d", index)
			}
			byIndex[index].parts.WriteString(part)
			byIndex[index].fragments++
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		if len(line) >= 64<<10 {
			t.Fatalf("SSE line has %d bytes", len(line))
		}
		var event struct {
			Index        int
			ContentBlock struct{ ID, Name string } `json:"content_block"`
			Delta        struct {
				PartialJSON string `json:"partial_json"`
			}
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    int
						ID       string
						Function struct{ Name, Arguments string }
					} `json:"tool_calls"`
				}
			}
		}
		if err := json.Unmarshal([]byte(line[len("data: "):]), &event); err != nil {
			t.Fatal(err)
		}
		add(event.Index, event.ContentBlock.ID, event.ContentBlock.Name, event.Delta.PartialJSON)
		for _, choice := range event.Choices {
			for _, call := range choice.Delta.ToolCalls {
				add(call.Index, call.ID, call.Function.Name, call.Function.Arguments)
			}
		}
	}
	return ordered
}
