package tools

import (
	"encoding/json"
	"strings"
)

// 上游内置工具与客户端声明工具之间的桥接。
//
// Cursor 的 agent 不自己执行工具，而是把「读文件」「跑命令」这类内置调用下发给
// 客户端。这些调用带的是上游自己的形态（路径 / 命令行），而客户端（OpenCode 等）
// 声明的是自己那套具名工具与 JSON Schema。这里负责把前者映射成后者。
//
// 这条路径比提示词模拟可靠得多：它用的是模型原生的工具调用能力，
// 不依赖模型是否愿意遵守我们约定的文本格式。

// NativeKind 与 tools 包解耦，避免反向依赖 cursor 包。
type NativeKind string

const (
	// KindReadFile 读取文件。
	KindReadFile NativeKind = "read_file"
	// KindRunTerminal 执行终端命令。
	KindRunTerminal NativeKind = "run_terminal"
	// KindSearchFiles 按 glob 模式搜索文件。
	KindSearchFiles NativeKind = "search_files"
	// KindWriteFile 写入文件。
	KindWriteFile NativeKind = "write_file"
)

// Native 是归一化后的上游内置工具调用。
type Native struct {
	ID          string
	Kind        NativeKind
	Path        string
	Command     string
	Pattern     string
	Content     string
	Description string
}

// 各类内置工具在客户端侧的候选名称，按优先级排列。
// 覆盖 OpenCode、Claude Code、Cline 等常见 agent 的命名习惯。
var candidateNames = map[NativeKind][]string{
	KindReadFile: {
		"read", "read_file", "readfile", "view", "view_file",
		"cat", "open_file", "get_file", "str_replace_editor",
	},
	KindRunTerminal: {
		"bash", "shell", "run_terminal_cmd", "run_command", "execute_command",
		"terminal", "exec", "run", "command",
	},
	KindSearchFiles: {
		"glob", "file_search", "find_files", "search_files", "grep", "list", "ls",
	},
	KindWriteFile: {
		"write", "write_file", "create_file", "edit", "str_replace_editor", "apply_patch",
	},
}

// 参数名候选：把上游的值填进客户端 schema 里对应的属性。
var candidateParams = map[NativeKind][]string{
	KindReadFile:    {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindRunTerminal: {"command", "cmd", "script", "shell_command"},
	KindSearchFiles: {"pattern", "glob", "query", "globPattern", "path"},
	KindWriteFile:   {"filePath", "file_path", "path", "target_file", "filename", "file"},
}

// MapNative 把一次上游内置调用映射成客户端声明的工具调用。
// 找不到合适的客户端工具时返回 false，调用方应回退到文本描述。
func MapNative(n Native, defs []Definition) (Call, bool) {
	def, ok := matchTool(n.Kind, defs)
	if !ok {
		return Call{}, false
	}

	var value string
	switch n.Kind {
	case KindRunTerminal:
		value = n.Command
	case KindSearchFiles:
		value = n.Pattern
	default:
		value = n.Path
	}

	args := map[string]any{}
	if key, ok := matchParam(n.Kind, def); ok {
		args[key] = value
	} else {
		// schema 里找不到对应属性时给个通用兜底，总比不调用强
		args[fallbackParam(n.Kind)] = value
	}
	// 写文件还要带上内容
	if n.Kind == KindWriteFile {
		if key, ok := matchContentParam(def); ok {
			args[key] = n.Content
		} else {
			args["content"] = n.Content
		}
	}
	// 部分客户端（如 OpenCode 的 bash）要求必填 description
	if n.Kind == KindRunTerminal && schemaRequires(def, "description") {
		desc := n.Description
		if desc == "" {
			desc = "Run command"
		}
		args["description"] = desc
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return Call{}, false
	}

	id := n.ID
	if id == "" {
		id = NewCallID()
	}
	return Call{ID: id, Name: def.Name, Arguments: string(raw)}, true
}

func matchTool(kind NativeKind, defs []Definition) (Definition, bool) {
	for _, want := range candidateNames[kind] {
		for _, d := range defs {
			if strings.EqualFold(d.Name, want) {
				return d, true
			}
		}
	}
	// 退一步做包含匹配，兼容带前缀的工具名（如 mcp_xxx_bash）
	for _, want := range candidateNames[kind] {
		for _, d := range defs {
			if strings.Contains(strings.ToLower(d.Name), want) {
				return d, true
			}
		}
	}
	return Definition{}, false
}

func matchParam(kind NativeKind, def Definition) (string, bool) {
	props := schemaProperties(def)
	for _, want := range candidateParams[kind] {
		for name := range props {
			if strings.EqualFold(name, want) {
				return name, true
			}
		}
	}
	return "", false
}

// matchContentParam 找出写文件工具里承载文件内容的属性。
func matchContentParam(def Definition) (string, bool) {
	props := schemaProperties(def)
	for _, want := range []string{"content", "contents", "text", "new_string", "code_edit", "body"} {
		for name := range props {
			if strings.EqualFold(name, want) {
				return name, true
			}
		}
	}
	return "", false
}

func fallbackParam(kind NativeKind) string {
	switch kind {
	case KindRunTerminal:
		return "command"
	case KindSearchFiles:
		return "pattern"
	default:
		return "path"
	}
}

func schemaProperties(def Definition) map[string]any {
	if len(def.Parameters) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(def.Parameters, &schema) != nil {
		return nil
	}
	return schema.Properties
}

func schemaRequires(def Definition, field string) bool {
	if len(def.Parameters) == 0 {
		return false
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(def.Parameters, &schema) != nil {
		return false
	}
	for _, r := range schema.Required {
		if strings.EqualFold(r, field) {
			return true
		}
	}
	return false
}

// DescribeNative 在找不到可映射的客户端工具时，用一句人话描述这次调用，
// 让客户端至少能看到模型想做什么，而不是收到一个空回复。
func DescribeNative(n Native) string {
	switch n.Kind {
	case KindReadFile:
		return "（上游请求读取文件：" + n.Path + "）"
	case KindRunTerminal:
		return "（上游请求执行命令：" + n.Command + "）"
	case KindSearchFiles:
		return "（上游请求搜索文件：" + n.Pattern + "）"
	case KindWriteFile:
		return "（上游请求写入文件：" + n.Path + "）"
	}
	return ""
}
