package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func scanAll(chunks []string) (string, []Call) {
	var s Scanner
	var text strings.Builder
	for _, c := range chunks {
		text.WriteString(s.Push(c))
	}
	text.WriteString(s.Flush())
	return text.String(), s.Calls()
}

func TestScannerPlainTextPassesThrough(t *testing.T) {
	text, calls := scanAll([]string{"你好", "，世界"})
	if text != "你好，世界" {
		t.Fatalf("正文 = %q", text)
	}
	if len(calls) != 0 {
		t.Fatalf("不应有工具调用，得到 %d 个", len(calls))
	}
}

func TestScannerExtractsSingleCall(t *testing.T) {
	in := `好的。` + OpenTag + `{"name":"bash","arguments":{"command":"ls -l"}}` + CloseTag
	text, calls := scanAll([]string{in})
	if text != "好的。" {
		t.Fatalf("正文应剥离标签，得到 %q", text)
	}
	if len(calls) != 1 {
		t.Fatalf("应解析出 1 个调用，得到 %d", len(calls))
	}
	if calls[0].Name != "bash" {
		t.Fatalf("工具名 = %q", calls[0].Name)
	}
	if !strings.Contains(calls[0].Arguments, `"ls -l"`) {
		t.Fatalf("参数 = %q", calls[0].Arguments)
	}
	if calls[0].ID == "" {
		t.Fatal("应生成调用 id")
	}
}

// 标签被切碎到多个增量里是流式下的常态，必须不吐出半截标签。
func TestScannerHandlesTagSplitAcrossChunks(t *testing.T) {
	full := `执行：` + OpenTag + `{"name":"read","arguments":{"path":"a.txt"}}` + CloseTag + `完成`
	for _, size := range []int{1, 3, 7, 13} {
		var chunks []string
		for i := 0; i < len(full); i += size {
			end := i + size
			if end > len(full) {
				end = len(full)
			}
			chunks = append(chunks, full[i:end])
		}
		text, calls := scanAll(chunks)
		if strings.Contains(text, "<tool") || strings.Contains(text, "tool_call>") {
			t.Fatalf("分块 %d：正文泄漏了标签片段 %q", size, text)
		}
		if text != "执行：完成" {
			t.Fatalf("分块 %d：正文 = %q", size, text)
		}
		if len(calls) != 1 || calls[0].Name != "read" {
			t.Fatalf("分块 %d：调用解析失败 %+v", size, calls)
		}
	}
}

func TestScannerExtractsMultipleCalls(t *testing.T) {
	in := OpenTag + `{"name":"a","arguments":{}}` + CloseTag +
		"中间" + OpenTag + `{"name":"b","arguments":{"x":1}}` + CloseTag
	text, calls := scanAll([]string{in})
	if text != "中间" {
		t.Fatalf("正文 = %q", text)
	}
	if len(calls) != 2 || calls[0].Name != "a" || calls[1].Name != "b" {
		t.Fatalf("应解析出 a、b 两个调用，得到 %+v", calls)
	}
}

// 未闭合的标签宁可原样显示，也不能把内容吞掉。
func TestScannerUnclosedTagIsNotSwallowed(t *testing.T) {
	in := "前文" + OpenTag + `{"name":"x"`
	text, calls := scanAll([]string{in})
	if !strings.HasPrefix(text, "前文") {
		t.Fatalf("正文 = %q", text)
	}
	if !strings.Contains(text, `{"name":"x"`) {
		t.Fatalf("未闭合内容应原样返回，得到 %q", text)
	}
	if len(calls) != 0 {
		t.Fatalf("未闭合不应产出调用")
	}
}

func TestParseCallTolerance(t *testing.T) {
	cases := []struct {
		name string
		body string
		tool string
		args string
	}{
		{"标准", `{"name":"a","arguments":{"k":1}}`, "a", `{"k":1}`},
		{"代码围栏", "```json\n{\"name\":\"a\",\"arguments\":{\"k\":1}}\n```", "a", `{"k":1}`},
		{"参数写成字符串", `{"name":"a","arguments":"{\"k\":1}"}`, "a", `{"k":1}`},
		{"用 input 代替 arguments", `{"name":"a","input":{"k":1}}`, "a", `{"k":1}`},
		{"无参数", `{"name":"a"}`, "a", `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseCall(c.body)
			if !ok {
				t.Fatalf("应解析成功")
			}
			if got.Name != c.tool || got.Arguments != c.args {
				t.Fatalf("得到 name=%q args=%q，期望 name=%q args=%q", got.Name, got.Arguments, c.tool, c.args)
			}
		})
	}
}

func TestParseCallRejectsGarbage(t *testing.T) {
	for _, body := range []string{"", "not json", `{"arguments":{}}`} {
		if _, ok := ParseCall(body); ok {
			t.Errorf("不应解析成功: %q", body)
		}
	}
}

// 模型偶尔会在 JSON 前后多写几句话，应能容忍。
func TestParseCallToleratesSurroundingProse(t *testing.T) {
	got, ok := ParseCall("好的，我来调用：\n{\"name\":\"a\",\"arguments\":{\"k\":1}}\n以上。")
	if !ok || got.Name != "a" || got.Arguments != `{"k":1}` {
		t.Fatalf("应剥离说明文字后解析成功，得到 %+v ok=%v", got, ok)
	}
}

// 解析失败绝不能静默吞内容——否则会变成一次彻底的空响应。
func TestScannerMalformedBlockIsReturnedAsText(t *testing.T) {
	in := "前文" + OpenTag + `{"name": broken json}` + CloseTag + "后文"
	text, calls := scanAll([]string{in})
	if len(calls) != 0 {
		t.Fatalf("非法块不应产出调用")
	}
	if !strings.Contains(text, "前文") || !strings.Contains(text, "后文") {
		t.Fatalf("正文应保留，得到 %q", text)
	}
	if !strings.Contains(text, "broken json") {
		t.Fatalf("非法块应原样归还，得到 %q", text)
	}
}

func TestScannerCountsFailedBlocks(t *testing.T) {
	var s Scanner
	s.Push(OpenTag + `{"bad"` + CloseTag)
	s.Flush()
	if s.Failed() != 1 {
		t.Fatalf("应统计到 1 个解析失败块，得到 %d", s.Failed())
	}
}

func TestBuildSystemPromptEmptyWithoutTools(t *testing.T) {
	if got := BuildSystemPrompt(nil, Choice{Mode: "auto"}); got != "" {
		t.Fatalf("无工具时不应注入提示词，得到 %q", got)
	}
}

func TestBuildSystemPromptNoneMode(t *testing.T) {
	defs := []Definition{{Name: "bash"}}
	if got := BuildSystemPrompt(defs, Choice{Mode: "none"}); got != "" {
		t.Fatalf("tool_choice=none 时不应注入，得到 %q", got)
	}
}

func TestBuildSystemPromptIncludesToolAndSchema(t *testing.T) {
	defs := []Definition{{
		Name:        "bash",
		Description: "Run a shell command",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
	}}
	got := BuildSystemPrompt(defs, Choice{Mode: "auto"})
	for _, want := range []string{"bash", "Run a shell command", `"command"`, OpenTag, CloseTag} {
		if !strings.Contains(got, want) {
			t.Errorf("提示词应包含 %q", want)
		}
	}
}

func TestBuildSystemPromptForcedFunction(t *testing.T) {
	defs := []Definition{{Name: "bash"}, {Name: "read"}}
	got := BuildSystemPrompt(defs, Choice{Mode: "function", Name: "read"})
	if !strings.Contains(got, `MUST call the tool "read"`) {
		t.Fatalf("应强制指定工具，得到 %q", got)
	}
}

// 回放历史里的调用后，再解析回来应得到同样的名称与参数。
func TestRenderCallRoundTrips(t *testing.T) {
	orig := Call{Name: "bash", Arguments: `{"command":"ls"}`}
	rendered := RenderCall(orig)
	text, calls := scanAll([]string{rendered})
	if strings.TrimSpace(text) != "" {
		t.Fatalf("回放内容不应留下正文，得到 %q", text)
	}
	if len(calls) != 1 || calls[0].Name != orig.Name || calls[0].Arguments != orig.Arguments {
		t.Fatalf("回放解析不一致: %+v", calls)
	}
}
