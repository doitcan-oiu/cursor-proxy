// Package tokenize 负责 usage 字段与日志统计所需的 token 计数。
//
// 计数分两档：
//
//  1. 真实 BPE 分词（默认）。用 github.com/tiktoken-go/tokenizer 这个纯 Go 移植，
//     词表随二进制内嵌，运行时不联网。按 model 名选择编码，OpenAI 系模型的结果
//     与官方一致。
//  2. 启发式估算（回退）。分词器不可用、用 TOKENIZER=estimate 关闭、
//     或以 `-tags notokenizer` 编译时使用。
//
// 需要说明的是：Cursor 上游不返回 usage，且同一个反代会把请求转发给 GPT / Claude /
// Gemini 等不同模型。Claude 与 Gemini 没有公开的官方分词器，对这类模型用
// o200k_base 近似——它同样是现代多语言 BPE，比「按字符数换算」准得多，但仍是近似值。
package tokenize

import (
	"strings"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/types"
)

// charsPerToken 是启发式回退里非宽字符的平均每 token 字符数。
const charsPerToken = 4

// 聊天格式的固定开销：每条消息的角色与分隔符，以及回复引导。
const (
	perMessageOverhead   = 4
	replyPrimingOverhead = 3
)

func bpeDisabled() bool {
	return strings.EqualFold(config.Get().TokenizerMode, "estimate")
}

// CountText 计算一段文本的 token 数。优先真实分词，不可用时回退估算。
func CountText(model, text string) int {
	if text == "" {
		return 0
	}
	if !bpeDisabled() {
		if n, ok := bpeCount(model, text); ok {
			return n
		}
	}
	return Estimate(text)
}

// CountMessages 计算整轮对话作为输入的 token 数（含聊天格式固定开销）。
func CountMessages(model string, messages []types.Message) int {
	total := replyPrimingOverhead
	for _, m := range messages {
		total += perMessageOverhead
		total += CountText(model, m.Role)
		total += CountText(model, types.ContentToText(m.Content))
	}
	return total
}

// ---- 启发式回退 ----

// isWideScript 判断某个字符是否属于「一字约一 token」的表意/音节文字。
func isWideScript(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11FF, // 谚文字母
		r >= 0x2E80 && r <= 0x303F,   // CJK 部首与标点
		r >= 0x3040 && r <= 0x30FF,   // 平假名 / 片假名
		r >= 0x3130 && r <= 0x318F,   // 谚文兼容字母
		r >= 0x3400 && r <= 0x4DBF,   // CJK 扩展 A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK 基本区
		r >= 0xA000 && r <= 0xA4CF,   // 彝文
		r >= 0xAC00 && r <= 0xD7AF,   // 谚文音节
		r >= 0xF900 && r <= 0xFAFF,   // CJK 兼容表意
		r >= 0xFF00 && r <= 0xFFEF,   // 全角形式
		r >= 0x20000 && r <= 0x2FA1F: // CJK 扩展 B 及以上
		return true
	}
	return false
}

// Estimate 在没有分词器时估算 token 数：
// CJK 约 1 字 1 token，其余约 4 字符 1 token。
func Estimate(text string) int {
	if text == "" {
		return 0
	}
	var wide, narrow int
	for _, r := range text {
		if isWideScript(r) {
			wide++
			continue
		}
		narrow++
	}
	tokens := wide + (narrow+charsPerToken-1)/charsPerToken
	if tokens == 0 {
		tokens = 1
	}
	return tokens
}
