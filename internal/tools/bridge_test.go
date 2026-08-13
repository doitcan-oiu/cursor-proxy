package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// OpenCode 实际声明的两个工具，用来验证映射结果能被它直接消费。
var openCodeTools = []Definition{
	{
		Name:        "read",
		Description: "Read a file",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"filePath":{"type":"string"},"offset":{"type":"number"}},"required":["filePath"]}`),
	},
	{
		Name:        "bash",
		Description: "Run a shell command",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"},"description":{"type":"string"}},"required":["command","description"]}`),
	},
}

func TestMapNativeReadFile(t *testing.T) {
	got, ok := MapNative(Native{ID: "toolu_1", Kind: KindReadFile, Path: "/tmp/a.txt"}, openCodeTools)
	if !ok {
		t.Fatal("应映射成功")
	}
	if got.Name != "read" {
		t.Fatalf("工具名 = %q，期望 read", got.Name)
	}
	if got.ID != "toolu_1" {
		t.Fatalf("应沿用上游调用 id，得到 %q", got.ID)
	}
	var args map[string]any
	if json.Unmarshal([]byte(got.Arguments), &args) != nil {
		t.Fatalf("参数不是合法 JSON: %s", got.Arguments)
	}
	if args["filePath"] != "/tmp/a.txt" {
		t.Fatalf("应填入 schema 里的 filePath，得到 %v", args)
	}
}

func TestMapNativeRunTerminal(t *testing.T) {
	got, ok := MapNative(Native{
		ID: "toolu_2", Kind: KindRunTerminal,
		Command: "ls -la", Description: "List files",
	}, openCodeTools)
	if !ok {
		t.Fatal("应映射成功")
	}
	if got.Name != "bash" {
		t.Fatalf("工具名 = %q，期望 bash", got.Name)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(got.Arguments), &args)
	if args["command"] != "ls -la" {
		t.Fatalf("命令未正确填入: %v", args)
	}
	// bash 的 schema 把 description 列为必填，必须补上
	if args["description"] != "List files" {
		t.Fatalf("必填的 description 未补全: %v", args)
	}
}

// 客户端可能用别的命名，映射要覆盖常见变体。
func TestMapNativeAlternativeNames(t *testing.T) {
	defs := []Definition{
		{Name: "view_file", Parameters: json.RawMessage(`{"type":"object","properties":{"target_file":{"type":"string"}}}`)},
		{Name: "run_terminal_cmd", Parameters: json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)},
	}

	r, ok := MapNative(Native{Kind: KindReadFile, Path: "/x"}, defs)
	if !ok || r.Name != "view_file" {
		t.Fatalf("读文件应映射到 view_file，得到 %+v ok=%v", r, ok)
	}
	var ra map[string]any
	_ = json.Unmarshal([]byte(r.Arguments), &ra)
	if ra["target_file"] != "/x" {
		t.Fatalf("应填入 target_file，得到 %v", ra)
	}

	c, ok := MapNative(Native{Kind: KindRunTerminal, Command: "pwd"}, defs)
	if !ok || c.Name != "run_terminal_cmd" {
		t.Fatalf("命令应映射到 run_terminal_cmd，得到 %+v ok=%v", c, ok)
	}
	var ca map[string]any
	_ = json.Unmarshal([]byte(c.Arguments), &ca)
	if ca["cmd"] != "pwd" {
		t.Fatalf("应填入 cmd，得到 %v", ca)
	}
}

func TestMapNativeTask(t *testing.T) {
	defs := []Definition{{
		Name: "task",
		Parameters: json.RawMessage(`{"type":"object","properties":{"description":{"type":"string"},` +
			`"prompt":{"type":"string"},"subagent_type":{"type":"string"}},` +
			`"required":["description","prompt","subagent_type"]}`),
	}}
	got, ok := MapNative(Native{
		ID: "toolu_t", Kind: KindTask,
		Description: "Analyze project", Prompt: "Analyze the structure",
	}, defs)
	if !ok || got.Name != "task" {
		t.Fatalf("应映射到 task，得到 %+v ok=%v", got, ok)
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(got.Arguments), &args)
	if args["prompt"] != "Analyze the structure" {
		t.Fatalf("prompt 未填入: %v", args)
	}
	if args["description"] != "Analyze project" {
		t.Fatalf("必填的 description 未补全: %v", args)
	}
	if args["subagent_type"] == nil {
		t.Fatalf("必填的 subagent_type 未补全: %v", args)
	}
}

func TestMapNativeNoMatchingTool(t *testing.T) {
	defs := []Definition{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}}
	if _, ok := MapNative(Native{Kind: KindRunTerminal, Command: "ls"}, defs); ok {
		t.Fatal("客户端没有对应工具时不应映射")
	}
}

func TestMapNativeGeneratesIDWhenMissing(t *testing.T) {
	got, ok := MapNative(Native{Kind: KindReadFile, Path: "/x"}, openCodeTools)
	if !ok || got.ID == "" {
		t.Fatalf("缺少上游 id 时应自行生成，得到 %+v", got)
	}
}

// 纯对话场景：上游会把「写一段 SVG」变成一次写文件调用，
// 那份内容就是答案，必须还原成正文而不是丢掉。
func TestNativeToTextRendersWriteContentAsCodeBlock(t *testing.T) {
	got := NativeToText(Native{
		Kind: KindWriteFile, Path: "/tmp/pelican.svg", Content: "<svg></svg>",
	})
	if !strings.HasPrefix(got, "```xml\n") {
		t.Fatalf("应按扩展名标注语言，得到 %q", got)
	}
	if !strings.Contains(got, "<svg></svg>") {
		t.Fatalf("应包含文件内容，得到 %q", got)
	}
	if !strings.HasSuffix(got, "```") {
		t.Fatalf("代码块应闭合，得到 %q", got)
	}
}

func TestNativeToTextLanguageByExtension(t *testing.T) {
	cases := map[string]string{
		"/a/b.py": "```python", "/a/b.go": "```go",
		"/a/b.unknownext": "```", "/a/b.json": "```json",
	}
	for path, want := range cases {
		got := NativeToText(Native{Kind: KindWriteFile, Path: path, Content: "x"})
		if !strings.HasPrefix(got, want+"\n") {
			t.Errorf("%s -> %q，期望以 %q 开头", path, got[:12], want)
		}
	}
}

// 非写文件的工具没有可还原的内容，退回文字说明。
func TestNativeToTextFallsBackToDescription(t *testing.T) {
	got := NativeToText(Native{Kind: KindRunTerminal, Command: "ls"})
	if strings.HasPrefix(got, "```") {
		t.Fatalf("不应渲染成代码块，得到 %q", got)
	}
	if got == "" {
		t.Fatal("应给出说明文字")
	}
}

func TestDescribeNativeFallback(t *testing.T) {
	if s := DescribeNative(Native{Kind: KindRunTerminal, Command: "ls"}); s == "" {
		t.Fatal("兜底描述不应为空")
	}
	if s := DescribeNative(Native{Kind: KindReadFile, Path: "/x"}); s == "" {
		t.Fatal("兜底描述不应为空")
	}
}

// 待办清单是数组参数，走不了通用的「单值填一个属性」那套。
// 客户端声明了待办工具时应真正映射过去，而不是只渲染成文本。
func TestMapNativeUpdateTodos(t *testing.T) {
	defs := []Definition{{
		Name:       "TodoWrite",
		Parameters: json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array"}},"required":["todos"]}`),
	}}
	n := Native{ID: "c1", Kind: KindUpdateTodos, Todos: []TodoItem{
		{ID: "1", Content: "调研", Status: "in_progress"},
		{Content: "起草", Status: "pending"},
	}}

	call, ok := MapNative(n, defs)
	if !ok {
		t.Fatal("客户端声明了待办工具，应该能映射")
	}
	if call.Name != "TodoWrite" {
		t.Fatalf("工具名不对：%q", call.Name)
	}

	var got struct {
		Todos []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &got); err != nil {
		t.Fatalf("参数应是合法 JSON：%v (%s)", err, call.Arguments)
	}
	if len(got.Todos) != 2 {
		t.Fatalf("应有 2 条待办，实际 %d", len(got.Todos))
	}
	if got.Todos[0].Content != "调研" || got.Todos[0].Status != "in_progress" {
		t.Fatalf("第一条不对：%+v", got.Todos[0])
	}
	// 上游没给 id 时要补一个，客户端多半要求必填
	if got.Todos[1].ID == "" {
		t.Fatal("缺失的 id 应自动补上")
	}
}

// 没声明待办工具的客户端仍然回退到文本，并且要带上状态标注。
func TestDescribeTodosFallsBackToText(t *testing.T) {
	n := Native{Kind: KindUpdateTodos, Todos: []TodoItem{
		{Content: "调研", Status: "in_progress"},
		{Content: "校对", Status: "pending"},
	}}
	if _, ok := MapNative(n, nil); ok {
		t.Fatal("没有可映射的客户端工具时应返回 false")
	}
	text := NativeToText(n)
	for _, want := range []string{"调研", "校对", "进行中", "待办"} {
		if !strings.Contains(text, want) {
			t.Fatalf("文本里应包含 %q，实际 %q", want, text)
		}
	}
}

// glob 与 grep 是两个不同的工具，早期把它们混在一起，
// 结果客户端声明的 grep 会被 glob 调用抢走。
func TestGlobAndGrepMapToDifferentTools(t *testing.T) {
	defs := []Definition{
		{Name: "glob", Parameters: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}}}`)},
		{Name: "grep", Parameters: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}}}`)},
	}
	globCall, ok := MapNative(Native{Kind: KindGlob, Pattern: "**/*.go"}, defs)
	if !ok || globCall.Name != "glob" {
		t.Fatalf("glob 应映射到 glob，实际 %+v", globCall)
	}
	grepCall, ok := MapNative(Native{Kind: KindSearchFiles, Pattern: "func main"}, defs)
	if !ok || grepCall.Name != "grep" {
		t.Fatalf("grep 应映射到 grep，实际 %+v", grepCall)
	}
}

// 联网搜索是上游确实存在的内置工具（ToolCall 字段 18）。
func TestMapNativeWebSearch(t *testing.T) {
	defs := []Definition{{
		Name:       "web_search",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}}
	call, ok := MapNative(Native{Kind: KindWebSearch, Pattern: "golang 手写 protobuf"}, defs)
	if !ok {
		t.Fatal("应能映射到客户端的 web_search")
	}
	if !strings.Contains(call.Arguments, "golang 手写 protobuf") {
		t.Fatalf("搜索词应带进参数：%s", call.Arguments)
	}
}
