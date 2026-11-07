package openai

import (
	"encoding/json"
	"testing"

	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/tools"
)

func TestMixedFileToolsRejectAmbiguousNativeCall(t *testing.T) {
	defs := []tools.Definition{
		{Name: "Write", Parameters: json.RawMessage(`{"type":"object","properties":{"file_path":{"type":"string"},"content":{"type":"string"}}}`)},
		{Name: "Edit", Parameters: json.RawMessage(`{"type":"object","properties":{"old_string":{"type":"string"},"new_string":{"type":"string"}}}`)},
	}
	out := newToolOutput(defs, false, fileToolBridge{})
	if text := out.native(&cursor.NativeToolCall{Kind: cursor.ToolWriteFile, Path: "/tmp/sample", Content: "ambiguous"}); text != "" || len(out.calls) != 0 {
		t.Fatal("an ambiguous native write must not become text or an executable file operation")
	}
	choice := tools.Choice{Mode: "auto"}
	if msg, code := out.completionError(choice, false); msg == "" || code != "ambiguous_file_operation" {
		t.Fatalf("ambiguous edit should end with an upstream error, got %q %q", msg, code)
	}
	markup := `<tool_call>{"name":"Edit","arguments":{"old_string":"a","new_string":"b"}}</tool_call>`
	if text := out.text(markup); text != "" || len(out.calls) != 1 || out.calls[0].Name != "Edit" {
		t.Fatalf("text call did not map to the declared tool: text=%q calls=%+v", text, out.calls)
	}
	if msg, code := out.completionError(choice, false); msg != "" || code != "" {
		t.Fatalf("successful text retry should clear the error, got %q %q", msg, code)
	}
}
