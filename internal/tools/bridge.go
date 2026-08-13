package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
	// KindSearchFiles 按内容正则搜索（上游叫 grep）。
	KindSearchFiles NativeKind = "search_files"
	// KindGlob 按文件名模式查找文件。
	KindGlob NativeKind = "glob"
	// KindWebSearch 联网搜索。
	KindWebSearch NativeKind = "web_search"
	// KindAwait 等待后台任务。
	KindAwait NativeKind = "await"
	// KindAskQuestion 向用户提问。
	KindAskQuestion NativeKind = "ask_question"
	// KindWriteFile 写入文件。
	KindWriteFile NativeKind = "write_file"
	// KindTask 派发子 agent。
	KindTask NativeKind = "task"
	// KindDeleteFile 删除文件。
	KindDeleteFile NativeKind = "delete_file"
	// KindListFiles 列出目录内容（上游叫 ls）。
	KindListFiles NativeKind = "list_files"
	// KindFetchURL 抓取网页。
	KindFetchURL NativeKind = "fetch_url"
	// KindUpdateTodos 更新待办清单。
	KindUpdateTodos NativeKind = "update_todos"
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
	// Name 是上游给这个工具的规范名，未识别时用于提示。
	Name string
	// Todos 是待办清单条目。
	Todos []TodoItem
}

// TodoItem 是待办清单里的一条。
type TodoItem struct {
	ID      string
	Content string
	Status  string
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
		"grep", "search", "grep_search", "ripgrep", "search_files", "codebase_search",
	},
	KindGlob: {
		"glob", "file_search", "find_files", "find", "fuzzy_file_search",
	},
	KindWebSearch: {
		"web_search", "websearch", "search_web", "google", "bing", "brave_search",
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
		"ls", "list_dir", "list_files", "list_directory", "list", "dir",
	},
	KindFetchURL: {
		"webfetch", "web_fetch", "fetch", "read_url", "url_fetch", "browse", "http_get",
	},
	KindUpdateTodos: {
		"todowrite", "todo_write", "todos", "task_list", "update_plan", "update_todos",
	},
}

// 参数名候选：把上游的值填进客户端 schema 里对应的属性。
var candidateParams = map[NativeKind][]string{
	KindReadFile:    {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindRunTerminal: {"command", "cmd", "script", "shell_command"},
	KindSearchFiles: {"pattern", "query", "regex", "search_term", "q"},
	KindGlob:        {"pattern", "glob", "globPattern", "query", "path"},
	KindWebSearch:   {"query", "search_term", "q", "search", "keywords"},
	KindWriteFile:   {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindTask:        {"prompt", "task", "instructions", "input", "message"},
	KindDeleteFile:  {"filePath", "file_path", "path", "target_file", "filename", "file"},
	KindListFiles:   {"path", "directory", "dir", "target_directory", "relative_workspace_path"},
	KindFetchURL:    {"url", "uri", "link", "address"},
	KindUpdateTodos: {"todos", "items", "tasks", "todo_list", "plan"},
}

// todoCall 把待办清单合成成客户端声明的待办工具调用。
// 这类工具的参数是对象数组，走不了通用的「单值填一个属性」那套。
func todoCall(n Native, def Definition) (Call, bool) {
	items := make([]map[string]any, 0, len(n.Todos))
	for i, t := range n.Todos {
		id := t.ID
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		items = append(items, map[string]any{
			"id": id, "content": t.Content, "status": t.Status,
		})
	}

	key := "todos"
	if k, ok := matchParam(KindUpdateTodos, def); ok {
		key = k
	}
	raw, err := json.Marshal(map[string]any{key: items})
	if err != nil {
		return Call{}, false
	}
	id := n.ID
	if id == "" {
		id = NewCallID()
	}
	return Call{ID: id, Name: def.Name, Arguments: string(raw)}, true
}

// MapNative 把一次上游内置调用映射成客户端声明的工具调用。
// 找不到合适的客户端工具时返回 false，调用方应回退到文本描述。
func MapNative(n Native, defs []Definition) (Call, bool) {
	// 这几类没有稳妥的参数可合成（或本就是给用户看的），交给文本兜底
	if n.Kind == KindUnknown || n.Kind == KindAwait || n.Kind == KindAskQuestion {
		return Call{}, false
	}
	def, ok := matchTool(n.Kind, defs)
	if !ok {
		return Call{}, false
	}
	// 待办清单是数组参数，与其它工具的单值参数不同，单独合成
	if n.Kind == KindUpdateTodos {
		return todoCall(n, def)
	}

	var value string
	switch n.Kind {
	case KindRunTerminal:
		value = n.Command
	case KindSearchFiles, KindGlob, KindWebSearch:
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
		return "（上游请求按内容搜索：" + n.Pattern + "）"
	case KindGlob:
		return "（上游请求按文件名查找：" + n.Pattern + "）"
	case KindWebSearch:
		return "（上游请求联网搜索：" + n.Pattern + "）"
	case KindWriteFile:
		return "（上游请求写入文件：" + n.Path + "）"
	case KindTask:
		return "（上游请求派发子任务：" + n.Description + "）"
	case KindDeleteFile:
		return "（上游请求删除文件：" + n.Path + "）"
	case KindListFiles:
		return "（上游请求列出目录：" + n.Path + "）"
	case KindFetchURL:
		return "（上游请求抓取网页：" + n.URL + "）"
	case KindAskQuestion:
		return "（上游想向你提问：" + n.Description + "）"
	case KindAwait:
		return "（上游在等待后台任务完成）"
	case KindUpdateTodos:
		return renderTodos(n.Todos)
	case KindUnknown:
		if n.Name != "" {
			return fmt.Sprintf("（上游用了本代理尚未支持的内置工具 %s（#%d），已跳过）", n.Name, n.Field)
		}
		return fmt.Sprintf("（上游请求了本代理尚未支持的工具 #%d，已跳过。"+
			"如需支持请带上此编号反馈）", n.Field)
	}
	return ""
}

// todoLabel 把待办状态翻成显示用的中文。
var todoLabel = map[string]string{
	"pending": "待办", "in_progress": "进行中",
	"completed": "已完成", "cancelled": "已取消",
}

// renderTodos 把待办清单渲染成一段可读文本。
// 客户端多半没有对应的工具可执行，但把清单显示出来对用户是有用的进度信息。
func renderTodos(items []TodoItem) string {
	if len(items) == 0 {
		return "（上游更新了待办清单）"
	}
	var b strings.Builder
	b.WriteString("（上游更新了待办清单）\n")
	for _, it := range items {
		b.WriteString("- ")
		if label := todoLabel[it.Status]; label != "" {
			b.WriteString("[" + label + "] ")
		}
		b.WriteString(it.Content)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// NativeBridgeEnabled 控制是否把上游内置工具调用翻译给客户端。
//
// 默认开启。关掉后退回纯提示词模拟，用于上游行为异常时兜底。
func NativeBridgeEnabled() bool {
	return !strings.EqualFold(os.Getenv("NATIVE_TOOL_BRIDGE"), "off")
}

// WithoutNativeEquivalent 挑出上游没有内置对应物的工具。
//
// 读文件、跑命令、搜索这类工具上游本来就有，交给原生桥接即可；只有客户端自己定义的
// 业务工具（比如「录入发票」）才需要靠提示词协议模拟。区分开来能少注入一大段提示词，
// 也避免模型把它当成可疑的额外指令。
func WithoutNativeEquivalent(defs []Definition) []Definition {
	if !NativeBridgeEnabled() {
		return defs
	}
	out := make([]Definition, 0, len(defs))
	for _, d := range defs {
		if !hasNativeEquivalent(d.Name) {
			out = append(out, d)
		}
	}
	return out
}

func hasNativeEquivalent(name string) bool {
	for _, names := range candidateNames {
		for _, want := range names {
			if strings.EqualFold(name, want) {
				return true
			}
		}
	}
	return false
}
