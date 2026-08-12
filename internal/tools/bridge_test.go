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
