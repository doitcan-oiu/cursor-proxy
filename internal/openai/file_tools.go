package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/tools"
)

const fileToolProvider = "cursor_proxy"

// fileToolBridge registers operations whose Cursor built-in form is ambiguous or unavailable.
// It records the original client names so only tools declared for this request can be returned.
type fileToolBridge struct {
	definitions []proto.MCPTool
	clientNames map[string]string
	prompt      string
}

func prepareFileTools(defs []tools.Definition) (fileToolBridge, []tools.Definition) {
	selected := tools.ClientFileToolDefinitions(defs)
	if len(selected) == 0 {
		return fileToolBridge{}, defs
	}
	bridge := fileToolBridge{clientNames: make(map[string]string, len(selected))}
	registered := make(map[string]bool, len(selected))
	var prompt strings.Builder
	prompt.WriteString("The registered cursor_proxy MCP tools operate on the downstream client's workspace. " +
		"Use the matching MCP tool for these operations. Cursor's local file tools address a different workspace.\n")
	for _, def := range selected {
		if registered[def.Name] {
			continue
		}
		registered[def.Name] = true
		base := "client_" + strings.ToLower(def.Name)
		alias := base
		for suffix := 2; bridge.clientNames[alias] != ""; suffix++ {
			alias = fmt.Sprintf("%s_%d", base, suffix)
		}
		modelName := "mcp__" + fileToolProvider + "__" + alias
		schema := def.Parameters
		if len(schema) == 0 || string(schema) == "null" {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		bridge.definitions = append(bridge.definitions, proto.MCPTool{
			Name: fileToolProvider + "-" + alias, ToolName: alias, ProviderIdentifier: fileToolProvider,
			Description: "Downstream client tool " + def.Name + ". " + def.Description, InputSchema: schema,
		})
		bridge.clientNames[alias] = def.Name
		fmt.Fprintf(&prompt, "- For client %q, call %s.\n", def.Name, modelName)
	}
	bridge.prompt = prompt.String()
	remaining := make([]tools.Definition, 0, len(defs)-len(registered))
	for _, def := range defs {
		if !registered[def.Name] {
			remaining = append(remaining, def)
		}
	}
	return bridge, remaining
}

func (b fileToolBridge) mapCall(call *cursor.NativeToolCall) (tools.Call, bool) {
	if call.MCPProvider != fileToolProvider || call.MCPName != fileToolProvider+"-"+call.Name {
		return tools.Call{}, false
	}
	name := b.clientNames[call.Name]
	if name == "" {
		return tools.Call{}, false
	}
	id := call.ID
	if id == "" {
		id = tools.NewCallID()
	}
	return tools.Call{ID: id, Name: name, Arguments: call.Arguments}, true
}
