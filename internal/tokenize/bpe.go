//go:build !notokenizer

package tokenize

import (
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

// encodingForModel 按对外 model 名选择 BPE 编码。
// 注意分支顺序：gpt-4o / gpt-4.1 必须先于 gpt-4 匹配，否则会被前缀抢先命中。
// 非 OpenAI 系（claude / gemini / composer / auto 等）统一用 o200k_base 近似。
func encodingForModel(model string) tokenizer.Encoding {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(m, "gpt-4o"),
		strings.HasPrefix(m, "gpt-4.1"),
		strings.HasPrefix(m, "gpt-4.5"),
		strings.HasPrefix(m, "gpt-5"):
		return tokenizer.O200kBase
	case strings.HasPrefix(m, "gpt-4"),
		strings.HasPrefix(m, "gpt-3.5"),
		strings.HasPrefix(m, "text-embedding"):
		return tokenizer.Cl100kBase
	default:
		return tokenizer.O200kBase
	}
}

var (
	codecMu     sync.Mutex
	codecCache  = map[tokenizer.Encoding]tokenizer.Codec{}
	codecFailed = map[tokenizer.Encoding]bool{}
)

// codecFor 惰性加载并缓存编码器。词表常驻内存不小（cl100k 约 3.4MB、o200k 约 6.7MB），
// 因此只在真正用到某个编码时才加载，用不到的永远不占内存。
func codecFor(model string) tokenizer.Codec {
	name := encodingForModel(model)

	codecMu.Lock()
	defer codecMu.Unlock()
	if c, ok := codecCache[name]; ok {
		return c
	}
	if codecFailed[name] {
		return nil
	}
	c, err := tokenizer.Get(name)
	if err != nil {
		codecFailed[name] = true
		return nil
	}
	codecCache[name] = c
	return c
}

// bpeCount 用真实分词器计数，第二个返回值表示是否可用。
func bpeCount(model, text string) (int, bool) {
	c := codecFor(model)
	if c == nil {
		return 0, false
	}
	ids, _, err := c.Encode(text)
	if err != nil {
		return 0, false
	}
	return len(ids), true
}
