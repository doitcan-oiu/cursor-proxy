package tools

import "strings"

import "testing"

func drain(w *LiveWriter, n *Native, chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(w.Push(n, c))
	}
	end, _ := w.Finish(n)
	b.WriteString(end)
	return b.String()
}

func TestLiveWriterStreamsCodeInsideFence(t *testing.T) {
	w := NewLiveWriter(true)
	n := &Native{ID: "c1", Kind: KindWriteFile, Path: "/tmp/hello.py"}

	first := w.Push(n, "print(")
	if !strings.HasPrefix(first, "```python\n") {
		t.Fatalf("首片段应带上代码块开头，实际 %q", first)
	}
	if !strings.HasSuffix(first, "print(") {
		t.Fatalf("首片段应原样带上内容，实际 %q", first)
	}
	if mid := w.Push(n, "1)"); mid != "1)" {
		t.Fatalf("后续片段应原样透传，实际 %q", mid)
	}
	if end, _ := w.Finish(n); end != "\n```\n" {
		t.Fatalf("结束应闭合代码块，实际 %q", end)
	}
}

func TestLiveWriterProseIsNotFenced(t *testing.T) {
	w := NewLiveWriter(true)
	n := &Native{ID: "c1", Kind: KindWriteFile, Path: "/tmp/README.md"}
	got := drain(w, n, "# 标题\n", "正文\n")
	if strings.Contains(got, "```") {
		t.Fatalf("markdown 不应被套进代码块，实际 %q", got)
	}
	if got != "# 标题\n正文\n" {
		t.Fatalf("散文应原样输出，实际 %q", got)
	}
}

func TestLiveWriterDisabledEmitsNothing(t *testing.T) {
	w := NewLiveWriter(false)
	n := &Native{ID: "c1", Kind: KindWriteFile, Path: "/tmp/a.go"}
	if got := drain(w, n, "package main"); got != "" {
		t.Fatalf("客户端声明了工具时不应流式吐正文，实际 %q", got)
	}
}

func TestLiveWriterIgnoresNonWriteTools(t *testing.T) {
	w := NewLiveWriter(true)
	for _, k := range []NativeKind{KindReadFile, KindRunTerminal, KindSearchFiles, KindUnknown} {
		n := &Native{ID: "c1", Kind: k, Path: "/tmp/a.go", Command: "ls"}
		if got := drain(w, n, "ls -la"); got != "" {
			t.Fatalf("%s 的参数不是正文，不应吐出，实际 %q", k, got)
		}
	}
}

func TestLiveWriterClosesFenceWhenTextInterrupts(t *testing.T) {
	w := NewLiveWriter(true)
	n := &Native{ID: "c1", Kind: KindWriteFile, Path: "/tmp/a.svg"}
	w.Push(n, "<svg/>")
	if got := w.Interrupt(); got != "\n```\n" {
		t.Fatalf("正文插入前应先闭合代码块，实际 %q", got)
	}
	if got := w.Interrupt(); got != "" {
		t.Fatalf("重复收尾不应再输出，实际 %q", got)
	}
}

func TestLiveWriterSwitchingCallsClosesPrevious(t *testing.T) {
	w := NewLiveWriter(true)
	a := &Native{ID: "a", Kind: KindWriteFile, Path: "/tmp/a.go"}
	b := &Native{ID: "b", Kind: KindWriteFile, Path: "/tmp/b.css"}
	w.Push(a, "package main")
	got := w.Push(b, "body{}")
	if !strings.HasPrefix(got, "\n```\n```css\n") {
		t.Fatalf("换文件时应先闭合上一个代码块，实际 %q", got)
	}
}

func TestLiveWriterFinishOnlyMatchesActiveCall(t *testing.T) {
	w := NewLiveWriter(true)
	a := &Native{ID: "a", Kind: KindWriteFile, Path: "/tmp/a.go"}
	w.Push(a, "x")
	other := &Native{ID: "b", Kind: KindWriteFile, Path: "/tmp/b.go"}
	if got, handled := w.Finish(other); handled || got != "" {
		t.Fatalf("别的调用结束不应收尾当前块，实际 %q", got)
	}
}

// 上游的「进行中」帧有时先到、路径却还是空的。此时先攒一小段内容再猜类型，
// 猜不出也要套上代码块——XML、JSON 这类内容不套会被客户端当标签吞掉。
func TestLiveWriterSniffsWhenPathUnknown(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{"<svg viewBox=\"0 0 10 10\"><rect/></svg>", "```xml\n"},
		{"<!DOCTYPE html><html><body>hello world</body></html>", "```html\n"},
		{"{\"name\": \"demo\", \"version\": \"1.0.0\", \"private\": true}", "```json\n"},
		{"#!/bin/bash\nset -euo pipefail\necho hello world from here", "```bash\n"},
		{"这是一段没有明显特征的内容，猜不出类型时退化成无语言代码块。", "```\n"},
	}
	for _, c := range cases {
		w := NewLiveWriter(true)
		n := &Native{ID: "c1", Kind: KindWriteFile}
		got := drain(w, n, c.body)
		if !strings.HasPrefix(got, c.want) {
			t.Fatalf("%q 应以 %q 开头，实际 %q", c.body[:12], c.want, got[:16])
		}
		if !strings.Contains(got, c.body) {
			t.Fatalf("内容应原样保留，实际 %q", got)
		}
	}
}

// 路径未知时前几十字节要先攒着，攒够了才一次吐出，不能边攒边漏。
func TestLiveWriterHoldsUntilItCanDecide(t *testing.T) {
	w := NewLiveWriter(true)
	n := &Native{ID: "c1", Kind: KindWriteFile}
	if got := w.Push(n, "<svg"); got != "" {
		t.Fatalf("还没攒够就不该输出，实际 %q", got)
	}
	out := w.Push(n, " viewBox=\"0 0 1 1\">")
	if !strings.HasPrefix(out, "```xml\n<svg") {
		t.Fatalf("攒够后应连同暂存内容一起吐出，实际 %q", out)
	}
}

// 路径帧紧跟在第一个内容片段之后到达，多等一片就能拿到准确的语言标记。
func TestLiveWriterPrefersLatePath(t *testing.T) {
	w := NewLiveWriter(true)
	unknown := &Native{ID: "c1", Kind: KindWriteFile}
	if got := w.Push(unknown, "print("); got != "" {
		t.Fatalf("路径还没到时应先攒着，实际 %q", got)
	}
	known := &Native{ID: "c1", Kind: KindWriteFile, Path: "/hello.py"}
	out := w.Push(known, "1)")
	if !strings.HasPrefix(out, "```python\n") {
		t.Fatalf("应采用后到的路径判断语言，实际 %q", out)
	}
	if !strings.Contains(out, "print(1)") {
		t.Fatalf("暂存内容应与新片段一起吐出，实际 %q", out)
	}
}

// 内容还没攒够上游就发完了，收尾时也必须把暂存内容吐出来，不能吞掉。
func TestLiveWriterFlushesShortContentOnFinish(t *testing.T) {
	w := NewLiveWriter(true)
	n := &Native{ID: "c1", Kind: KindWriteFile}
	w.Push(n, "hi")
	got, handled := w.Finish(n)
	if !handled {
		t.Fatal("这次调用已被流式接管，应报告 handled")
	}
	if !strings.Contains(got, "hi") {
		t.Fatalf("暂存内容不能被吞掉，实际 %q", got)
	}
}

// handled 必须与「有没有输出」分开：内容已流式给过、收尾恰好没有余文时，
// 若只看返回值是否为空，调用方会再走一遍还原逻辑，同一份内容出现两遍。
func TestLiveWriterReportsHandledEvenWithEmptyTail(t *testing.T) {
	w := NewLiveWriter(true)
	n := &Native{ID: "c1", Kind: KindWriteFile, Path: "/tmp/a.md"}
	w.Push(n, "正文结尾带换行\n")
	got, handled := w.Finish(n)
	if got != "" {
		t.Fatalf("散文且已换行时无需补任何东西，实际 %q", got)
	}
	if !handled {
		t.Fatal("内容已流式给过，必须报告 handled，否则会被重复输出")
	}
}

func TestRenderContentEscapesInnerFence(t *testing.T) {
	content := "示例：\n```\ncode\n```\n"
	got := RenderContent("/tmp/a.txt", content)
	if got != content {
		t.Fatalf("txt 属散文应原样返回，实际 %q", got)
	}

	got = RenderContent("/tmp/a.html", content)
	if !strings.HasPrefix(got, "````html\n") || !strings.HasSuffix(got, "````") {
		t.Fatalf("内容自带 ``` 时围栏应加长，实际 %q", got)
	}
}

func TestRenderContentKnownAndUnknownExtensions(t *testing.T) {
	if got := RenderContent("/tmp/a.svg", "<svg/>"); got != "```xml\n<svg/>\n```" {
		t.Fatalf("svg 应标为 xml，实际 %q", got)
	}
	if got := RenderContent("/tmp/a.zzz", "x"); got != "```\nx\n```" {
		t.Fatalf("未知扩展名应退化成无语言代码块，实际 %q", got)
	}
	if got := RenderContent("/tmp/a.go", ""); got != "" {
		t.Fatalf("空内容不应产生空代码块，实际 %q", got)
	}
}
