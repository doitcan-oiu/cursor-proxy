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
	go pumpAgentStream(ctx, body, events)

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
	go pumpAgentStream(ctx, body, events)

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
	go pumpAgentStream(ctx, body, events)

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
	go pumpAgentStream(ctx, body, events)

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
	go pumpAgentStream(ctx, body, events)

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
	go pumpAgentStream(ctx, body, events)

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
