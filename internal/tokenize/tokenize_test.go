package tokenize

import (
	"testing"

	"cursor-proxy/internal/types"
)

// 以下用例在两种构建模式（含/不含分词器）下都应通过。

func TestCountTextEmpty(t *testing.T) {
	if got := CountText("gpt-4o", ""); got != 0 {
		t.Fatalf("空串应为 0，得到 %d", got)
	}
}

func TestCountTextAlwaysPositiveForNonEmpty(t *testing.T) {
	for _, s := range []string{"a", "你", "hello world"} {
		if got := CountText("gpt-4o", s); got <= 0 {
			t.Errorf("CountText(%q) = %d，应为正数", s, got)
		}
	}
}

func TestCountMessagesIncludesOverheadAndGrows(t *testing.T) {
	short := CountMessages("gpt-4o", []types.Message{{Role: "user", Content: "hi"}})
	long := CountMessages("gpt-4o", []types.Message{{Role: "user", Content: "hi there, this is a much longer prompt"}})
	if short <= 0 {
		t.Fatalf("消息计数应为正，得到 %d", short)
	}
	if long <= short {
		t.Fatalf("更长的输入应得到更大的计数：short=%d long=%d", short, long)
	}
}

// 分块内容（OpenAI 的 content 数组形态）要与等价纯文本一致。
func TestCountMessagesHandlesBlockContent(t *testing.T) {
	block := CountMessages("gpt-4o", []types.Message{{
		Role:    "user",
		Content: []any{map[string]any{"type": "text", "text": "你好世界"}},
	}})
	plain := CountMessages("gpt-4o", []types.Message{{Role: "user", Content: "你好世界"}})
	if block != plain {
		t.Fatalf("分块内容 = %d，应与纯文本一致 %d", block, plain)
	}
}

// ---- 启发式回退 ----

func TestEstimateEmpty(t *testing.T) {
	if got := Estimate(""); got != 0 {
		t.Fatalf("空串应为 0，得到 %d", got)
	}
}

func TestEstimateEnglishAboutFourCharsPerToken(t *testing.T) {
	if got, want := Estimate("the quick brown fox jumps over t"), 8; got != want {
		t.Fatalf("英文估算 = %d，期望 %d", got, want)
	}
}

func TestEstimateCJKOneTokenPerChar(t *testing.T) {
	if got, want := Estimate("你好世界测试"), 6; got != want {
		t.Fatalf("中文估算 = %d，期望 %d", got, want)
	}
}

// 中文不能按字节算：UTF-8 下每字 3 字节，按字节会虚高约 3 倍。
func TestEstimateCJKIsNotByteLength(t *testing.T) {
	text := "你好世界测试"
	if got := Estimate(text); got >= len(text) {
		t.Fatalf("估算 %d 不应达到字节长度 %d", got, len(text))
	}
}

func TestEstimateMixed(t *testing.T) {
	if got, want := Estimate("你好世界abcdefgh"), 6; got != want {
		t.Fatalf("中英混排估算 = %d，期望 %d", got, want)
	}
}
