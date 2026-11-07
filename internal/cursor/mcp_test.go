package cursor

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/proto"
)

func TestPendingMCPToolSurvivesIdleAfterAnotherCall(t *testing.T) {
	t.Setenv("AGENT_IDLE_MS", "40")
	t.Setenv("AGENT_THINKING_MS", "1500")
	config.Reset()
	defer restoreEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	events := make(chan StreamEvent, 8)
	registered := proto.MCPTool{Name: "proxy-client-Write", ProviderIdentifier: "proxy-client", ToolName: "Write"}
	go pumpAgentStream(ctx, reader, events, "test-model", nil, true, registered)

	args := proto.NewWriter().Str(1, registered.Name).Str(4, registered.ProviderIdentifier).Str(5, registered.ToolName)
	frames := append(toolCallFrame("first", toolMCP, args), progressFrame("second", toolMCP, registered.Name)...)
	if _, err := writer.Write(frames); err != nil {
		t.Fatal(err)
	}
	if event := <-events; event.Kind != EventToolCall || event.Tool == nil || event.Tool.ID != "first" {
		t.Fatalf("first tool call not received: %+v", event)
	}
	select {
	case event := <-events:
		t.Fatalf("stream ended before second MCP call arguments: %+v", event)
	case <-time.After(120 * time.Millisecond):
	}

	tail := append(toolCallFrame("second", toolMCP, args), frame(0x02, []byte("{}"))...)
	if _, err := writer.Write(tail); err != nil {
		t.Fatal(err)
	}
	if event := <-events; event.Kind != EventToolCall || event.Tool == nil || event.Tool.ID != "second" {
		t.Fatalf("second tool call not received: %+v", event)
	}
	if event := <-events; event.Kind != EventEnd {
		t.Fatalf("stream did not finish: %+v", event)
	}
}

func TestMCPDiscoveryKeepsStreamOpenUntilActualCall(t *testing.T) {
	t.Setenv("AGENT_IDLE_MS", "40")
	t.Setenv("AGENT_THINKING_MS", "1500")
	config.Reset()
	defer restoreEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	events := make(chan StreamEvent, 8)
	registered := proto.MCPTool{Name: "proxy-client-Write", ProviderIdentifier: "proxy-client", ToolName: "Write"}
	// Disable the generic post-preamble grace to isolate the discovery window.
	go pumpAgentStream(ctx, reader, events, "test-model", nil, false, registered)

	const callID = "write"
	discovery := toolCallFrame(callID, toolGetMCPTools, proto.NewWriter().Str(1, registered.ProviderIdentifier))
	if _, err := writer.Write(append(contentFrame("准备写文件"), discovery...)); err != nil {
		t.Fatal(err)
	}
	if event := <-events; event.Kind != EventDelta {
		t.Fatalf("expected preamble, got %+v", event)
	}
	select {
	case event := <-events:
		t.Fatalf("discovery must stay internal and preserve the stream: %+v", event)
	case <-time.After(120 * time.Millisecond):
	}

	args := proto.NewWriter().Str(1, registered.Name).Str(4, registered.ProviderIdentifier).Str(5, registered.ToolName)
	tail := append(toolCallFrame(callID, toolMCP, args), frame(0x02, []byte("{}"))...)
	if _, err := writer.Write(tail); err != nil {
		t.Fatal(err)
	}
	if event := <-events; event.Kind != EventToolCall || event.Tool == nil || event.Tool.ID != callID {
		t.Fatalf("expected completed MCP tool, got %+v", event)
	}
	if event := <-events; event.Kind != EventEnd {
		t.Fatalf("stream did not finish: %+v", event)
	}
}

func TestMCPStartedCallPreservesWholeFileArguments(t *testing.T) {
	content := strings.Repeat("line with Unicode 🙂\n", 1024)
	args := proto.NewWriter().Str(1, "proxy-client-Write").Str(4, "proxy-client").Str(5, "Write")
	for key, value := range map[string]string{"file_path": "C:\\probe.txt", "content": content} {
		item := proto.NewWriter().Str(1, key).Bytes(2, proto.NewWriter().Str(3, value).Finish())
		args.Bytes(2, item.Finish())
	}
	wrapper := proto.NewWriter().Bytes(toolMCP, proto.NewWriter().Bytes(1, args.Finish()).Finish())
	started := proto.NewWriter().Str(1, "test-write").Bytes(2, wrapper.Finish())
	update := proto.NewWriter().Bytes(smToolCallStarted, started.Finish())

	state := agentStreamState{mcpTools: []proto.MCPTool{
		{Name: "proxy-client-Write", ProviderIdentifier: "proxy-client", ToolName: "Write"},
	}}
	call := state.parseNativeToolCall(proto.Decode(update.Finish()))
	if call == nil || call.Kind != ToolMCP || call.ID != "test-write" ||
		call.Name != "Write" || call.MCPName != "proxy-client-Write" || call.MCPProvider != "proxy-client" {
		t.Fatalf("unexpected call identity: %+v", call)
	}
	expected := `{"content":"` + strings.ReplaceAll(content, "\n", `\n`) + `","file_path":"C:\\probe.txt"}`
	if call.Arguments != expected {
		t.Fatalf("write arguments were changed: got %d bytes, want %d", len(call.Arguments), len(expected))
	}
}

func TestMCPDiscoveryOnlyReturnsRequestedRegisteredProvider(t *testing.T) {
	state := proto.NewWriter().Str(1, "proxy-client")
	exec := proto.NewWriter().Int32(1, 17).Str(15, "exec-1").Bytes(execMCPState, state.Finish())
	reply := mcpDiscoveryReply(proto.Decode(exec.Finish()), []proto.MCPTool{
		{Name: "proxy-client-LS", ProviderIdentifier: "proxy-client", ToolName: "LS"},
		{Name: "other-Read", ProviderIdentifier: "other", ToolName: "Read"},
	})
	if len(reply) <= 5 {
		t.Fatal("missing discovery reply")
	}
	clientExec := proto.Decode(proto.FirstBytes(proto.Decode(reply[5:]), 2))
	if firstToolVarint(clientExec, 1) != 17 || proto.FirstString(clientExec, 15) != "exec-1" {
		t.Fatal("discovery reply lost execution identity")
	}
	result := proto.Decode(proto.FirstBytes(clientExec, execMCPState))
	success := proto.Decode(proto.FirstBytes(result, 1))
	if len(success[1]) != 1 {
		t.Fatalf("unexpected number of providers: %d", len(success[1]))
	}
	server := proto.Decode(proto.FirstBytes(success, 1))
	if proto.FirstString(server, 2) != "proxy-client" || proto.FirstString(server, 7) != "connected" || len(server[5]) != 1 {
		t.Fatal("discovery reply did not include exactly the registered provider and tool")
	}
	def := proto.Decode(proto.FirstBytes(server, 5))
	if proto.FirstString(def, 5) != "LS" {
		t.Fatal("incorrect discovered tool")
	}
}
