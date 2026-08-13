package cursor

import (
	"context"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"

	"cursor-proxy/internal/proto"
)

// frame 按 Connect 信封格式打包一帧：[flag:1][len:4 BE][payload]。
func frame(flag byte, payload []byte) []byte {
	head := make([]byte, 5)
	head[0] = flag
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload)))
	return append(head, payload...)
}

// contentFrame 构造一个带正文的 Agent 响应帧（结构：top{1: sm{1: wrap{1: text}}}）。
func contentFrame(text string) []byte {
	wrap := proto.NewWriter()
	wrap.Str(1, text)
	sm := proto.NewWriter()
	sm.Bytes(1, wrap.Finish())
	top := proto.NewWriter()
	top.Bytes(1, sm.Finish())
	return frame(0x00, top.Finish())
}

// heartbeatFrame 复刻上游每 10s 一次的心跳：top{1: sm{13: 空}}。
func heartbeatFrame() []byte {
	sm := proto.NewWriter()
	sm.Bytes(fieldHeartbeat, nil)
	top := proto.NewWriter()
	top.Bytes(fieldStreamMessage, sm.Finish())
	return frame(0x00, top.Finish())
}

// conversationFrame 复刻上游在一轮结束时回写的会话记录帧：top{4: ...}。
func conversationFrame(index int) []byte {
	rec := proto.NewWriter()
	rec.Int32(1, index)
	rec.Str(3, "record")
	top := proto.NewWriter()
	top.Bytes(fieldConversation, rec.Finish())
	return frame(0x00, top.Finish())
}

// toolCallFrame 复刻上游的「工具调用完成」帧：
// top{1: sm{2{1: 调用id, 2{<工具字段>{1{<参数>}}}}}}
func toolCallFrame(id string, toolField int, args *proto.Writer) []byte {
	inner := proto.NewWriter()
	inner.Bytes(1, args.Finish())

	wrapper := proto.NewWriter()
	wrapper.Bytes(toolField, inner.Finish())

	done := proto.NewWriter()
	done.Str(1, id)
	done.Bytes(2, wrapper.Finish())

	sm := proto.NewWriter()
	sm.Bytes(smToolCallDone, done.Finish())

	top := proto.NewWriter()
	top.Bytes(fieldStreamMessage, sm.Finish())
	return frame(0x00, top.Finish())
}

func collectTools(t *testing.T, data []byte) []NativeToolCall {
	t.Helper()
	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpAgentStream(ctx, body, events, "test-model")

	var out []NativeToolCall
	for ev := range events {
		if ev.Kind == EventToolCall && ev.Tool != nil {
			out = append(out, *ev.Tool)
		}
	}
	return out
}

// 写文件的参数（路径 + 内容）只出现在「调用完成」帧里；
// 顶层的客户端动作帧只带路径，据此还原会得到错误的调用。
func TestParseNativeWriteFile(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "/tmp/cube.py")
	args.Str(6, "print(1)\n")

	var data []byte
	data = append(data, toolCallFrame("toolu_w1", toolWriteFile, args)...)
	data = append(data, conversationFrame(4)...)

	calls := collectTools(t, data)
	if len(calls) != 1 {
		t.Fatalf("应解析出 1 个调用，得到 %d", len(calls))
	}
	c := calls[0]
	if c.Kind != ToolWriteFile || c.Path != "/tmp/cube.py" || c.Content != "print(1)\n" {
		t.Fatalf("写文件解析错误: %+v", c)
	}
	if c.ID != "toolu_w1" {
		t.Fatalf("调用 id = %q", c.ID)
	}
}

func TestParseNativeReadFile(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "/etc/hostname")

	var data []byte
	data = append(data, toolCallFrame("toolu_r1", toolReadFile, args)...)
	data = append(data, conversationFrame(4)...)

	calls := collectTools(t, data)
	if len(calls) != 1 || calls[0].Kind != ToolReadFile || calls[0].Path != "/etc/hostname" {
		t.Fatalf("读文件解析错误: %+v", calls)
	}
}

func TestParseNativeRunTerminal(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "ls -la")
	args.Str(15, "List files")

	var data []byte
	data = append(data, toolCallFrame("toolu_t1", toolRunTerminal, args)...)
	data = append(data, conversationFrame(4)...)

	calls := collectTools(t, data)
	if len(calls) != 1 {
		t.Fatalf("应解析出 1 个调用，得到 %+v", calls)
	}
	if calls[0].Kind != ToolRunTerminal || calls[0].Command != "ls -la" || calls[0].Description != "List files" {
		t.Fatalf("终端命令解析错误: %+v", calls[0])
	}
}

func TestParseNativeSearchFiles(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "foobar")

	var data []byte
	data = append(data, toolCallFrame("toolu_s1", toolSearchFiles, args)...)
	data = append(data, conversationFrame(4)...)

	calls := collectTools(t, data)
	if len(calls) != 1 || calls[0].Kind != ToolSearchFiles || calls[0].Pattern != "foobar" {
		t.Fatalf("搜索解析错误: %+v", calls)
	}
}

// 「分析整个项目」这类任务上游会派发子 agent，漏掉它同样会让对话只回一句就断。
func TestParseNativeTask(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "Analyze project structure")
	args.Str(2, "Analyze the structure of the current project")
	args.Str(4, "claude-4.6-opus-max")

	var data []byte
	data = append(data, toolCallFrame("toolu_task1", toolTask, args)...)
	data = append(data, conversationFrame(4)...)

	calls := collectTools(t, data)
	if len(calls) != 1 {
		t.Fatalf("应解析出 1 个调用，得到 %+v", calls)
	}
	c := calls[0]
	if c.Kind != ToolTask {
		t.Fatalf("类型 = %v，期望 %v", c.Kind, ToolTask)
	}
	if c.Description != "Analyze project structure" {
		t.Fatalf("任务描述 = %q", c.Description)
	}
	if c.Prompt != "Analyze the structure of the current project" {
		t.Fatalf("任务提示词 = %q", c.Prompt)
	}
}

func TestParseNativeOtherTools(t *testing.T) {
	cases := []struct {
		name      string
		toolField int
		build     func(*proto.Writer)
		check     func(*testing.T, NativeToolCall)
	}{
		{
			"删除文件", toolDeleteFile,
			func(w *proto.Writer) { w.Str(1, "/tmp/x.txt") },
			func(t *testing.T, c NativeToolCall) {
				if c.Kind != ToolDeleteFile || c.Path != "/tmp/x.txt" {
					t.Fatalf("%+v", c)
				}
			},
		},
		{
			// 这个工具的模式在子字段 2，与搜索不同，最容易写错
			"列出文件", toolListFiles,
			func(w *proto.Writer) { w.Str(2, "internal/**/*.go") },
			func(t *testing.T, c NativeToolCall) {
				if c.Kind != ToolListFiles || c.Pattern != "internal/**/*.go" {
					t.Fatalf("%+v", c)
				}
			},
		},
		{
			"抓取网页", toolFetchURL,
			func(w *proto.Writer) { w.Str(1, "https://example.com") },
			func(t *testing.T, c NativeToolCall) {
				if c.Kind != ToolFetchURL || c.URL != "https://example.com" {
					t.Fatalf("%+v", c)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := proto.NewWriter()
			tc.build(args)
			var data []byte
			data = append(data, toolCallFrame("toolu_x", tc.toolField, args)...)
			data = append(data, conversationFrame(4)...)

			calls := collectTools(t, data)
			if len(calls) != 1 {
				t.Fatalf("应解析出 1 个调用，得到 %+v", calls)
			}
			tc.check(t, calls[0])
		})
	}
}

// 未识别的工具必须报出来而不是丢弃，否则对话会静默中断且无从排查。
func TestParseNativeUnknownToolIsReported(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "some payload")

	var data []byte
	data = append(data, toolCallFrame("toolu_u", 99, args)...)
	data = append(data, conversationFrame(4)...)

	calls := collectTools(t, data)
	if len(calls) != 1 {
		t.Fatalf("未知工具也应产出事件，得到 %+v", calls)
	}
	if calls[0].Kind != ToolUnknown {
		t.Fatalf("类型 = %v，期望 %v", calls[0].Kind, ToolUnknown)
	}
	if calls[0].Field != 99 {
		t.Fatalf("应带上字段号便于补齐，得到 %d", calls[0].Field)
	}
}

// 同一次调用会先后出现多个帧，必须按调用 id 去重，否则客户端会重复执行。
func TestNativeToolCallDeduplicatedByID(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "/tmp/a.txt")

	var data []byte
	data = append(data, toolCallFrame("toolu_dup", toolReadFile, args)...)
	data = append(data, toolCallFrame("toolu_dup", toolReadFile, args)...)
	data = append(data, conversationFrame(4)...)

	if calls := collectTools(t, data); len(calls) != 1 {
		t.Fatalf("同一调用 id 应只产出一次，得到 %d", len(calls))
	}
}

// heldOpenBody 先吐出预置数据，然后一直阻塞不返回 EOF，
// 模拟上游发完 end-of-stream 帧后仍不关闭连接的真实行为。
type heldOpenBody struct {
	data     []byte
	pos      int
	release  chan struct{}
	closeOne sync.Once
}

func (b *heldOpenBody) Read(p []byte) (int, error) {
	if b.pos < len(b.data) {
		n := copy(p, b.data[b.pos:])
		b.pos += n
		return n, nil
	}
	<-b.release // 连接挂着不动
	return 0, io.EOF
}

func (b *heldOpenBody) Close() error {
	b.closeOne.Do(func() { close(b.release) })
	return nil
}

// 复刻 agentn.api5 的真实行为：开头回写会话上下文 → 流式正文 → 结尾回写会话记录，
// 之后连接不关，只有 10s 一次的心跳。必须在记录回写后很快收尾，而不是空等 6s idle。
func TestStreamEndsAfterConversationRecordWriteback(t *testing.T) {
	var data []byte
	data = append(data, heartbeatFrame()...)
	// 开头的会话上下文回写：此时生成尚未开始，不能被误判为结束
	data = append(data, conversationFrame(1)...)
	data = append(data, conversationFrame(2)...)
	// 生成
	data = append(data, contentFrame("答案是")...)
	data = append(data, contentFrame("2。")...)
	// 结尾的会话记录回写
	data = append(data, conversationFrame(4)...)
	data = append(data, conversationFrame(5)...)

	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go pumpAgentStream(ctx, body, events, "test-model")

	var text string
	var sawEnd bool
	for ev := range events {
		switch ev.Kind {
		case EventDelta:
			text += ev.Text
		case EventEnd:
			sawEnd = true
		case EventError:
			t.Fatalf("不应产生错误事件: %s", ev.Message)
		}
	}
	elapsed := time.Since(start)

	if text != "答案是2。" {
		t.Fatalf("正文 = %q，期望 %q", text, "答案是2。")
	}
	if !sawEnd {
		t.Fatal("应产出 EventEnd")
	}
	// 修复前要等满 6s idle；现在只需 AGENT_FINISH_IDLE_MS（默认 400ms）。
	if elapsed > 2*time.Second {
		t.Fatalf("会话记录回写后耗时 %v 才收尾", elapsed)
	}
}

// 只有思考内容、没有正文的一轮（例如模型直接发起工具调用）也必须正常收尾，
// 不能因为 gotContent 为假而一路等到首字超时（默认 60s）。
func TestStreamEndsWhenOnlyThinkingProduced(t *testing.T) {
	thinking := func(text string) []byte {
		wrap := proto.NewWriter()
		wrap.Str(1, text)
		sm := proto.NewWriter()
		sm.Bytes(4, wrap.Finish()) // sm 字段 4 = thinking
		top := proto.NewWriter()
		top.Bytes(fieldStreamMessage, sm.Finish())
		return frame(0x00, top.Finish())
	}

	var data []byte
	data = append(data, thinking("正在检查工作区规则")...)
	data = append(data, conversationFrame(4)...)

	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go pumpAgentStream(ctx, body, events, "test-model")

	var sawEnd bool
	for ev := range events {
		if ev.Kind == EventEnd {
			sawEnd = true
		}
		if ev.Kind == EventError {
			t.Fatalf("不应产生错误事件: %s", ev.Message)
		}
	}
	if !sawEnd {
		t.Fatal("应产出 EventEnd")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("只有思考内容时耗时 %v 才收尾", elapsed)
	}
}

// 心跳帧不应被当成正文，也不该让流误以为还有数据。
func TestHeartbeatFramesProduceNoEvents(t *testing.T) {
	var data []byte
	data = append(data, contentFrame("hi")...)
	data = append(data, heartbeatFrame()...)
	data = append(data, conversationFrame(4)...)

	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpAgentStream(ctx, body, events, "test-model")

	var text string
	deltas := 0
	for ev := range events {
		if ev.Kind == EventDelta {
			deltas++
			text += ev.Text
		}
	}
	if deltas != 1 || text != "hi" {
		t.Fatalf("应只有 1 个正文分片 \"hi\"，实得 %d 个 %q", deltas, text)
	}
}

// 上游发出 end-of-stream 帧后必须立刻收尾，不能空等 idle 超时（默认 6s）。
func TestStreamEndsImmediatelyOnEndOfStreamFrame(t *testing.T) {
	var data []byte
	data = append(data, contentFrame("你好")...)
	data = append(data, contentFrame(" world")...)
	data = append(data, frame(0x02, []byte("{}"))...) // end-of-stream trailer

	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go pumpAgentStream(ctx, body, events, "test-model")

	var text string
	var sawEnd bool
	for ev := range events {
		switch ev.Kind {
		case EventDelta:
			text += ev.Text
		case EventEnd:
			sawEnd = true
		case EventError:
			t.Fatalf("不应产生错误事件: %s", ev.Message)
		}
	}
	elapsed := time.Since(start)

	if text != "你好 world" {
		t.Fatalf("正文 = %q，期望 %q", text, "你好 world")
	}
	if !sawEnd {
		t.Fatal("应产出 EventEnd")
	}
	// 修复前这里要等满 6s 的 idle 超时才会结束。
	if elapsed > time.Second {
		t.Fatalf("收到 end-of-stream 后耗时 %v 才收尾，说明没有即时结束", elapsed)
	}
}

// 上游直接关闭连接（不发 trailer）时也应立刻收尾。
func TestStreamEndsOnUpstreamClose(t *testing.T) {
	body := io.NopCloser(readerOf(contentFrame("hi")))
	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go pumpAgentStream(ctx, body, events, "test-model")

	var text string
	for ev := range events {
		if ev.Kind == EventDelta {
			text += ev.Text
		}
	}
	if text != "hi" {
		t.Fatalf("正文 = %q，期望 %q", text, "hi")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("上游关闭后耗时 %v 才收尾", elapsed)
	}
}

// 取消 context 应立即终止，不泄漏 goroutine。
func TestStreamStopsOnContextCancel(t *testing.T) {
	body := &heldOpenBody{data: contentFrame("hi"), release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 16)
	ctx, cancel := context.WithCancel(context.Background())
	go pumpAgentStream(ctx, body, events, "test-model")

	// 读到正文后取消
	<-events
	cancel()

	select {
	case _, ok := <-events:
		_ = ok // 通道关闭或残留事件都可以，重点是没有卡住
	case <-time.After(2 * time.Second):
		t.Fatal("取消后未能及时结束")
	}
}

type sliceReader struct {
	data []byte
	pos  int
}

func readerOf(b []byte) io.Reader { return &sliceReader{data: b} }

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// progressFrame 复刻「调用进行中」帧：工具类型与路径先到，参数还没发完。
func progressFrame(id string, toolField int, path string) []byte {
	args := proto.NewWriter()
	args.Str(1, path)
	inner := proto.NewWriter()
	inner.Bytes(1, args.Finish())
	wrapper := proto.NewWriter()
	wrapper.Bytes(toolField, inner.Finish())

	body := proto.NewWriter()
	body.Str(1, id)
	body.Bytes(2, wrapper.Finish())

	sm := proto.NewWriter()
	sm.Bytes(smToolCallProgress, body.Finish())
	top := proto.NewWriter()
	top.Bytes(fieldStreamMessage, sm.Finish())
	return frame(0x00, top.Finish())
}

// inputDeltaFrame 复刻参数的流式片段帧：sm{15{1: 调用id, 2{3{1: 文本}}}}。
func inputDeltaFrame(id, text string) []byte {
	chunk := proto.NewWriter()
	chunk.Str(1, text)
	holder := proto.NewWriter()
	holder.Bytes(3, chunk.Finish())

	body := proto.NewWriter()
	body.Str(1, id)
	body.Bytes(2, holder.Finish())

	sm := proto.NewWriter()
	sm.Bytes(smToolInputDelta, body.Finish())
	top := proto.NewWriter()
	top.Bytes(fieldStreamMessage, sm.Finish())
	return frame(0x00, top.Finish())
}

// 上游是分片下发写文件内容的：先 sm.7 告知工具与路径，再若干 sm.15 片段。
// 解析对了纯对话才能边收边吐，而不是等整段发完静默几十秒。
func TestToolInputDeltasAreStreamed(t *testing.T) {
	args := proto.NewWriter()
	args.Str(1, "/tmp/hello.py")
	args.Str(6, "print(\"hi\")\n")

	var data []byte
	data = append(data, progressFrame("call-1", toolWriteFile, "/tmp/hello.py")...)
	data = append(data, inputDeltaFrame("call-1", "print(")...)
	data = append(data, inputDeltaFrame("call-1", "\"hi\")\n")...)
	data = append(data, toolCallFrame("call-1", toolWriteFile, args)...)
	data = append(data, conversationFrame(4)...)

	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()
	events := make(chan StreamEvent, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpAgentStream(ctx, body, events, "test-model")

	var streamed string
	var done int
	for ev := range events {
		switch ev.Kind {
		case EventToolInputDelta:
			if ev.Tool == nil || ev.Tool.ID != "call-1" {
				t.Fatalf("片段应带上调用标识，得到 %+v", ev.Tool)
			}
			if ev.Tool.Kind != ToolWriteFile || ev.Tool.Path != "/tmp/hello.py" {
				t.Fatalf("片段应继承进行中帧的工具类型与路径，得到 %+v", ev.Tool)
			}
			streamed += ev.Text
		case EventToolCall:
			done++
		}
	}

	if streamed != "print(\"hi\")\n" {
		t.Fatalf("片段应按序拼回完整内容，得到 %q", streamed)
	}
	if done != 1 {
		t.Fatalf("完成帧仍应下发一次，得到 %d", done)
	}
}

// dripBody 每隔一小段时间吐一帧，模拟「一直在正常输出」的长回答。
type dripBody struct {
	frame    []byte
	every    time.Duration
	done     chan struct{}
	closeOne sync.Once
}

func (b *dripBody) Read(p []byte) (int, error) {
	select {
	case <-b.done:
		return 0, io.EOF
	case <-time.After(b.every):
		return copy(p, b.frame), nil
	}
}

func (b *dripBody) Close() error {
	b.closeOne.Do(func() { close(b.done) })
	return nil
}

// 时长上限触发时必须标记 Truncated：早期版本直接发一个普通 EventEnd，
// 客户端收到 finish_reason=stop，半截回答看起来像正常说完，最难排查。
func TestHardCapMarksTruncated(t *testing.T) {
	restore := hardCapFor
	hardCapFor = func() time.Duration { return 120 * time.Millisecond }
	defer func() { hardCapFor = restore }()

	body := &dripBody{frame: contentFrame("续写中…"), every: 20 * time.Millisecond, done: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpAgentStream(ctx, body, events, "test-model")

	var gotText, sawEnd, truncated bool
	for ev := range events {
		switch ev.Kind {
		case EventDelta:
			gotText = true
		case EventEnd:
			sawEnd = true
			truncated = ev.Truncated
		}
	}

	if !gotText {
		t.Fatal("截断前应该已经产出过正文")
	}
	if !sawEnd {
		t.Fatal("应该收到结束事件")
	}
	if !truncated {
		t.Fatal("被时长上限掐断时必须标记 Truncated，否则客户端会以为是正常收尾")
	}
}

// 正常收尾（上游回写会话记录）不能被误标成截断。
func TestNormalEndIsNotTruncated(t *testing.T) {
	var data []byte
	data = append(data, contentFrame("好的")...)
	data = append(data, conversationFrame(4)...)

	body := &heldOpenBody{data: data, release: make(chan struct{})}
	defer body.Close()

	events := make(chan StreamEvent, 32)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pumpAgentStream(ctx, body, events, "test-model")

	for ev := range events {
		if ev.Kind == EventEnd && ev.Truncated {
			t.Fatal("正常收尾不应标记为截断")
		}
	}
}
