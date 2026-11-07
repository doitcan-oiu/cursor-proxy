package proto

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
)

// MCPTool describes a client tool registered for the Cursor run.
type MCPTool struct {
	Name               string
	Description        string
	ProviderIdentifier string
	ToolName           string
	InputSchema        json.RawMessage
}

func encodeMCPTools(tools []MCPTool) []byte {
	list := NewWriter()
	for _, tool := range tools {
		if def := EncodeMCPTool(tool); def != nil {
			list.Bytes(1, def)
		}
	}
	return list.Finish()
}

// EncodeMCPTool writes the definition shared by registration and discovery.
func EncodeMCPTool(tool MCPTool) []byte {
	schema := map[string]any{"type": "object"}
	if len(tool.InputSchema) > 0 && json.Unmarshal(tool.InputSchema, &schema) != nil {
		return nil
	}
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return NewWriter().Str(1, tool.Name).Str(2, tool.Description).
		Bytes(3, encodeValue(schema)).Str(4, tool.ProviderIdentifier).Str(5, tool.ToolName).Finish()
}

// encodeValue writes a google.protobuf.Value oneof from decoded JSON.
func encodeValue(v any) []byte {
	w := NewWriter()
	switch x := v.(type) {
	case float64:
		w.varint(2<<3 | 1)
		w.buf = binary.LittleEndian.AppendUint64(w.buf, math.Float64bits(x))
	case string:
		w.Str(3, x)
	case bool:
		var b int
		if x {
			b = 1
		}
		w.Int32(4, b)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		object := NewWriter()
		for _, k := range keys {
			entry := NewWriter().Str(1, k).Bytes(2, encodeValue(x[k])).Finish()
			object.Bytes(1, entry)
		}
		w.Bytes(5, object.Finish())
	case []any:
		list := NewWriter()
		for _, item := range x {
			list.Bytes(1, encodeValue(item))
		}
		w.Bytes(6, list.Finish())
	default:
		w.Int32(1, 0)
	}
	return w.Finish()
}

// DecodeMCPArguments reads McpArgs.args, a map<string, google.protobuf.Value>.
func DecodeMCPArguments(fields []Field) (string, bool) {
	args, ok := decodeValueMap(fields, 0)
	if !ok {
		return "", false
	}
	data, err := json.Marshal(args)
	return string(data), err == nil
}

func decodeValueMap(fields []Field, depth int) (map[string]any, bool) {
	object := make(map[string]any, len(fields))
	for _, field := range fields {
		if field.WireType != 2 {
			return nil, false
		}
		entry := Decode(field.Bytes)
		value, ok := decodeValue(FirstBytes(entry, 2), depth+1)
		if !ok {
			return nil, false
		}
		object[FirstString(entry, 1)] = value
	}
	return object, true
}

func decodeValue(data []byte, depth int) (any, bool) {
	if depth > 100 {
		return nil, false
	}
	fields := Decode(data)
	if len(fields) != 1 {
		return nil, false
	}
	for key, entries := range fields {
		if len(entries) != 1 {
			return nil, false
		}
		f := entries[0]
		switch key {
		case 1:
			return nil, f.WireType == 0 && f.Varint == 0
		case 2:
			if f.WireType == 1 {
				return math.Float64frombits(binary.LittleEndian.Uint64(f.Bytes)), true
			}
		case 3:
			return string(f.Bytes), f.WireType == 2
		case 4:
			return f.Varint != 0, f.WireType == 0
		case 5:
			if f.WireType == 2 {
				return decodeValueMap(Decode(f.Bytes)[1], depth)
			}
		case 6:
			if f.WireType == 2 {
				entries := Decode(f.Bytes)[1]
				items := make([]any, 0, len(entries))
				for _, item := range entries {
					value, ok := decodeValue(item.Bytes, depth+1)
					if item.WireType != 2 || !ok {
						return nil, false
					}
					items = append(items, value)
				}
				return items, true
			}
		}
	}
	return nil, false
}
