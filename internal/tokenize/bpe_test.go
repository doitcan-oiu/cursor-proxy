//go:build !notokenizer

package tokenize

import "testing"

// 断言具体数值可以在编码选错时立刻发现（如误把 o200k 当成 cl100k）。
func TestCountTextMatchesRealBPE(t *testing.T) {
	cases := []struct {
		name  string
		model string
		text  string
		want  int
	}{
		{"英文-cl100k", "gpt-4", "Hello, how are you doing today?", 8},
		{"英文-o200k", "gpt-4o", "Hello, how are you doing today?", 8},
		{"中文-cl100k", "gpt-4", "你好，请用一句话介绍一下你自己。", 18},
		{"中文-o200k", "gpt-4o", "你好，请用一句话介绍一下你自己。", 10},
		{"中英混排-cl100k", "gpt-4", "把 Cursor 订阅暴露成 OpenAI compatible API", 16},
		{"中英混排-o200k", "gpt-4o", "把 Cursor 订阅暴露成 OpenAI compatible API", 12},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CountText(c.model, c.text); got != c.want {
				t.Fatalf("CountText(%q) = %d，期望 %d", c.text, got, c.want)
			}
		})
	}
}

// gpt-4o / gpt-4.1 前缀不能被 gpt-4 规则抢先命中。
func TestEncodingSelection(t *testing.T) {
	cases := map[string]string{
		"gpt-4":             "cl100k_base",
		"gpt-3.5-turbo":     "cl100k_base",
		"gpt-4o":            "o200k_base",
		"gpt-4.1":           "o200k_base",
		"gpt-5.1":           "o200k_base",
		"claude-4.5-sonnet": "o200k_base",
		"auto":              "o200k_base",
		"":                  "o200k_base",
	}
	for model, want := range cases {
		if got := string(encodingForModel(model)); got != want {
			t.Errorf("model %q -> %s，期望 %s", model, got, want)
		}
	}
}

// 回退值应与真实分词处在同一量级（不超过 2 倍），否则启发式失去意义。
func TestEstimateStaysCloseToRealBPE(t *testing.T) {
	samples := []string{
		"Hello, how are you doing today?",
		"你好，请用一句话介绍一下你自己。",
		"把 Cursor 订阅暴露成 OpenAI compatible API",
	}
	for _, s := range samples {
		real := CountText("gpt-4o", s)
		est := Estimate(s)
		if est > real*2 || real > est*2 {
			t.Errorf("%q: 估算 %d 与真实 %d 相差超过 2 倍", s, est, real)
		}
	}
}
