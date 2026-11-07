package cursor

import "cursor-proxy/internal/proto"

const execMCPState = 36

func mcpDiscoveryReply(exec map[int][]proto.Field, tools []proto.MCPTool) []byte {
	request := proto.FirstBytes(exec, execMCPState)
	if request == nil {
		return nil
	}
	requested := make(map[string]bool)
	for _, identifier := range proto.Decode(request)[1] {
		if identifier.WireType == 2 {
			requested[string(identifier.Bytes)] = true
		}
	}

	servers := make(map[string]*proto.Writer)
	var providers []string
	for _, tool := range tools {
		provider := tool.ProviderIdentifier
		if provider == "" || (len(requested) > 0 && !requested[provider]) {
			continue
		}
		def := proto.EncodeMCPTool(tool)
		if def == nil {
			continue
		}
		server := servers[provider]
		if server == nil {
			server = proto.NewWriter().Str(1, provider).Str(2, provider).Str(7, "connected")
			servers[provider] = server
			providers = append(providers, provider)
		}
		server.Bytes(5, def)
	}

	success := proto.NewWriter()
	for _, provider := range providers {
		success.Bytes(1, servers[provider].Finish())
	}
	result := proto.NewWriter().Bytes(1, success.Finish())
	reply := proto.NewWriter()
	for _, id := range exec[1] {
		if id.WireType == 0 {
			reply.Int32(1, int(id.Varint))
			break
		}
	}
	if id := proto.FirstString(exec, 15); id != "" {
		reply.Str(15, id)
	}
	reply.Bytes(execMCPState, result.Finish())
	return connectFrame(proto.NewWriter().Bytes(2, reply.Finish()).Finish())
}
