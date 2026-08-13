package openai

import "testing"

// recorder 收集 blockWriter 发出的事件，便于断言块顺序与下标。
type recorder struct {
	order []string
	idx   []int
	think string
	text  string
}

func (r *recorder) send(ev string, d any) {
	m := d.(map[string]any)
	switch ev {
	case "content_block_start":
		cb := m["content_block"].(map[string]any)
		r.order = append(r.order, cb["type"].(string))
		r.idx = append(r.idx, m["index"].(int))
	case "content_block_delta":
		dl := m["delta"].(map[string]any)
		switch dl["type"] {
		case "thinking_delta":
			r.think += dl["thinking"].(string)
		case "text_delta":
			r.text += dl["text"].(string)
		}
	}
}

// 推理内容早期在 Anthropic 端点被整个丢弃：实测一次问答上游给了 3000 多字推理，
// 客户端一个字都收不到，等于白付了这部分 token。
func TestBlockWriterPutsThinkingBeforeText(t *testing.T) {
	r := &recorder{}
	b := &blockWriter{send: r.send}
	b.thinking("先算体积。")
	b.thinking("再算剩余。")
	b.text("倒掉了一半。")
	textIdx := b.closeAll()

	if len(r.order) != 2 || r.order[0] != "thinking" || r.order[1] != "text" {
		t.Fatalf("应先 thinking 再 text，实际 %v", r.order)
	}
	if r.idx[0] != 0 || r.idx[1] != 1 {
		t.Fatalf("块下标应连续从 0 开始，实际 %v", r.idx)
	}
	if textIdx != 1 {
		t.Fatalf("正文块下标应为 1，实际 %d", textIdx)
	}
	if r.think != "先算体积。再算剩余。" || r.text != "倒掉了一半。" {
		t.Fatalf("内容不对：推理 %q 正文 %q", r.think, r.text)
	}
}

// 没有推理时正文必须是 0 号，不能空出下标让客户端解析错位。
func TestBlockWriterWithoutThinking(t *testing.T) {
	r := &recorder{}
	b := &blockWriter{send: r.send}
	b.text("你好")
	if idx := b.closeAll(); idx != 0 {
		t.Fatalf("正文块应是 0 号，实际 %d", idx)
	}
	if len(r.order) != 1 || r.order[0] != "text" {
		t.Fatalf("只该有一个 text 块，实际 %v", r.order)
	}
}

// 一个字都没产出时也要开一个空文本块——规范要求至少有一个内容块。
func TestBlockWriterAlwaysOpensOne(t *testing.T) {
	r := &recorder{}
	b := &blockWriter{send: r.send}
	if idx := b.closeAll(); idx != 0 {
		t.Fatalf("空回复的正文块也应是 0 号，实际 %d", idx)
	}
	if len(r.order) != 1 || r.order[0] != "text" {
		t.Fatalf("应开出一个空 text 块，实际 %v", r.order)
	}
}

// 正文开始之后再来的推理要丢掉：块已经收尾了，补进去会让顺序错乱。
func TestBlockWriterIgnoresLateThinking(t *testing.T) {
	r := &recorder{}
	b := &blockWriter{send: r.send}
	b.text("答案是")
	b.thinking("这段来晚了")
	b.text("一半")
	b.closeAll()

	if len(r.order) != 1 || r.order[0] != "text" {
		t.Fatalf("不该在正文之后再开推理块，实际 %v", r.order)
	}
	if r.think != "" {
		t.Fatalf("迟到的推理应被丢弃，实际 %q", r.think)
	}
	if r.text != "答案是一半" {
		t.Fatalf("正文应连续，实际 %q", r.text)
	}
}

// 多段推理只开一个块，不能每段都开一个。
func TestBlockWriterOpensThinkingOnce(t *testing.T) {
	r := &recorder{}
	b := &blockWriter{send: r.send}
	for i := 0; i < 5; i++ {
		b.thinking("片段")
	}
	b.closeAll()
	if len(r.order) != 2 || r.order[0] != "thinking" {
		t.Fatalf("多段推理应共用一个块，实际 %v", r.order)
	}
}
