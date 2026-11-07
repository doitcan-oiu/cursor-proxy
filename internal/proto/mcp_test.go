package proto

import (
	"encoding/json"
	"reflect"
	"testing"

	"cursor-proxy/internal/types"
)

func TestAgentRequestRegistersMCPDefinitionAndAppendsPrompt(t *testing.T) {
	input := json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"},"limit":{"minimum":1.5},"dry_run":{"default":false}},"required":["file_path"],"additionalProperties":false}`)
	tool := MCPTool{
		Name: "proxy-client-Write", Description: "Write a whole file.",
		ProviderIdentifier: "proxy-client", ToolName: "Write", InputSchema: input,
	}
	request := Decode(FirstBytes(Decode(EncodeAgentRequestWithMCP(
		[]types.Message{{Role: "user", Content: "Write a file."}}, "auto", ModeAgent,
		[]MCPTool{tool}, "Use the registered tool.")), 1))
	definitions := Decode(FirstBytes(request, 4))
	def := Decode(FirstBytes(definitions, 1))
	for field, want := range map[int]string{1: tool.Name, 2: tool.Description, 4: tool.ProviderIdentifier, 5: tool.ToolName} {
		if got := FirstString(def, field); got != want {
			t.Errorf("definition field %d = %q, want %q", field, got, want)
		}
	}
	got, ok := decodeValue(FirstBytes(def, 3), 0)
	var want any
	if err := json.Unmarshal(input, &want); err != nil {
		t.Fatal(err)
	}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("schema = %#v, want %#v", got, want)
	}
	if got := FirstString(Decode(FirstBytes(request, 29)), 2); got != "Use the registered tool." {
		t.Fatalf("appended prompt = %q", got)
	}
}

func TestMCPArgumentsPreserveNestedJSONValues(t *testing.T) {
	args := map[string]any{"enabled": false, "offset": -2.5, "content": "🙂\n", "filters": []any{nil, map[string]any{"prefix": ""}}}
	value := Decode(encodeValue(args))
	got, ok := DecodeMCPArguments(Decode(FirstBytes(value, 5))[1])
	want, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != string(want) {
		t.Fatalf("arguments = %q, want %q", got, want)
	}

	bad := NewWriter().Str(1, "content").Bytes(2, NewWriter().Int32(3, 0).Finish()).Finish()
	if got, ok := DecodeMCPArguments([]Field{{WireType: 2, Bytes: bad}}); ok {
		t.Fatalf("invalid Value wire type produced arguments %q", got)
	}
}
