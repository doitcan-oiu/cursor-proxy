package tools

import (
	"encoding/json"
	"fmt"
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
	// KindTask 派发子 agent。
	KindTask NativeKind = "task"
	// KindDeleteFile 删除文件。
	KindDeleteFile NativeKind = "delete_file"
	// KindListFiles 按 glob 列出文件。
	KindListFiles NativeKind = "list_files"
	// KindFetchURL 抓取网页。
	KindFetchURL NativeKind = "fetch_url"
	// KindTodoWrite 记录待办清单。
	KindTodoWrite NativeKind = "todo_write"
	// KindUnknown 尚未识别的上游工具。
	KindUnknown NativeKind = "unknown"
)

// Native 是归一化后的上游内置工具调用。
type Native struct {
	ID          string
	Kind        NativeKind
	Path        string
	Command     string
	Pattern     string
	Content     string
	Prompt      string
	URL         string
	Description string
	Field       int
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
	KindTask: {
		"task", "agent", "subagent", "dispatch_agent", "delegate",
	},
	KindDeleteFile: {
		"delete", "delete_file", "remove", "rm", "removefile",
	},
	KindListFiles: {
		"glob", "list", "ls", "list_dir", "list_files", "find_files", "file_search",
	},
	KindFetchURL: {
		"webfetch", "web_fetch", "fetch", "read_url", "url_fetch", "browse", "http_get",
	},
	KindTodoWrite: {
		"todowrite", "todo_write", "todos", "task_list", "update_plan",
	},
}

// 参数名候选：把上游的值填进客户端 schema 里对应的属性。
var candidateParams = map[NativeKind][]string{
	KindReadFile:    {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindRunTerminal: {"command", "cmd", "script", "shell_command"},
	KindSearchFiles: {"pattern", "glob", "query", "globPattern", "path"},
	KindWriteFile:   {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindTask:        {"prompt", "task", "instructions", "input", "message"},
	KindDeleteFile:  {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindListFiles:   {"pattern", "glob", "globPattern", "path", "directory", "dir"},
	KindFetchURL:    {"url", "uri", "link", "address"},
}

// MapNative 把一次上游内置调用映射成客户端声明的工具调用。
// 找不到合适的客户端工具时返回 false，调用方应回退到文本描述。
func MapNative(n Native, defs []Definition) (Call, bool) {
	// 待办与未知工具没有稳妥的参数可合成，交给文本兜底
	if n.Kind == KindUnknown || n.Kind == KindTodoWrite {
		return Call{}, false
	}
	def, ok := matchTool(n.Kind, defs)
	if !ok {
		return Call{}, false
	}

	var value string
	switch n.Kind {
	case KindRunTerminal:
		value = n.Command
	case KindSearchFiles, KindListFiles:
		value = n.Pattern
	case KindTask:
		value = n.Prompt
	case KindFetchURL:
		value = n.URL
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
	// 部分客户端（如 OpenCode 的 bash / task）要求必填 description
	if schemaRequires(def, "description") {
		desc := n.Description
		if desc == "" {
			desc = defaultDescription(n.Kind)
		}
		args["description"] = desc
	}
	// task 工具通常还要指定子 agent 类型
	if n.Kind == KindTask {
		if key, ok := matchNamedParam(def, "subagent_type", "subagentType", "agent", "agent_type"); ok {
			args[key] = "general"
		}
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

func defaultDescription(kind NativeKind) string {
	switch kind {
	case KindRunTerminal:
		return "Run command"
	case KindTask:
		return "Subtask"
	}
	return "Tool call"
}

// matchNamedParam 在 schema 里找出候选名之一对应的属性。
func matchNamedParam(def Definition, wants ...string) (string, bool) {
	props := schemaProperties(def)
	for _, want := range wants {
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

// 常见扩展名到代码块语言标记的映射。
// NativeToText 把内置工具调用还原成给纯对话客户端看的正文。
// 写文件的内容就是答案本身，其余工具只能给一句描述。
func NativeToText(n Native) string {
	if n.Kind == KindWriteFile && n.Content != "" {
		return RenderContent(n.Path, n.Content)
	}
	return DescribeNative(n)
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
	case KindTask:
		return "（上游请求派发子任务：" + n.Description + "）"
	case KindDeleteFile:
		return "（上游请求删除文件：" + n.Path + "）"
	case KindListFiles:
		return "（上游请求列出文件：" + n.Pattern + "）"
	case KindFetchURL:
		return "（上游请求抓取网页：" + n.URL + "）"
	case KindTodoWrite:
		return "（上游更新了待办清单：" + n.Description + "）"
	case KindUnknown:
		return fmt.Sprintf("（上游请求了本代理尚未支持的工具 #%d，已跳过。"+
			"如需支持请带上此编号反馈）", n.Field)
	}
	return ""
}
