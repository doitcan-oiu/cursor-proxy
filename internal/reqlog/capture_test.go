package reqlog

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// 一张图就能有几百 KB，原样留档会把内存吃掉，对排查也没帮助。
func TestSanitizeStripsImagePayload(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 4096)))
	raw := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":[`+
		`{"type":"text","text":"看图"},`+
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, payload)

	got := SanitizeRequest([]byte(raw))

	if strings.Contains(got, payload[:64]) {
		t.Fatalf("base64 载荷应被去掉：%s", got)
	}
	if !strings.Contains(got, "image/png base64 已省略") {
		t.Fatalf("应留下占位说明：%s", got)
	}
	// 结构信息必须保住，否则留档就没意义了
	for _, want := range []string{`"model":"m"`, `"看图"`, `"type":"image_url"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("应保留 %s：%s", want, got)
		}
	}
	if len(got) > 600 {
		t.Fatalf("去掉载荷后应大幅缩短，实际 %d 字节", len(got))
	}
}

// 没有图片的普通请求原样保留，并且仍是合法 JSON，方便直接拿去复现。
func TestSanitizeKeepsPlainRequestParsable(t *testing.T) {
	raw := `{"model":"claude-4.6-opus-max","stream":true,"messages":[{"role":"user","content":"你好"}]}`
	got := SanitizeRequest([]byte(raw))
	if got != raw {
		t.Fatalf("普通请求应原样保留：%s", got)
	}
	var v any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("应仍是合法 JSON：%v", err)
	}
}

func TestSanitizeTruncatesHugeRequest(t *testing.T) {
	raw := `{"text":"` + strings.Repeat("a", MaxRequestBytes*2) + `"}`
	got := SanitizeRequest([]byte(raw))
	if len(got) > MaxRequestBytes+140 {
		t.Fatalf("应截断到上限附近，实际 %d 字节", len(got))
	}
	if !strings.Contains(got, "已截断") {
		t.Fatalf("截断了就要说明：%s", got[len(got)-80:])
	}
}

// 上游整体不可用时错误会成片出现，请求体全留会把内存吃掉，只保最近若干条。
func TestOnlyRecentErrorsKeepRequestBody(t *testing.T) {
	Clear()
	for i := 0; i < maxBodies+10; i++ {
		Record(Entry{Kind: "chat", Status: "error", Request: fmt.Sprintf(`{"n":%d}`, i)})
	}

	withBody := 0
	for _, e := range List(0) {
		if e.Request != "" {
			withBody++
		}
	}
	if withBody != maxBodies {
		t.Fatalf("应只为最近 %d 条保留请求体，实际 %d 条", maxBodies, withBody)
	}

	// 保住的必须是最近的那些
	all := List(0)
	if last := all[len(all)-1]; last.Request == "" {
		t.Fatal("最新一条必须保留请求体")
	}
	Clear()
}

// 成功的请求不留请求体，也不该挤掉失败请求已留存的那些。
func TestSuccessEntriesDoNotEvictBodies(t *testing.T) {
	Clear()
	Record(Entry{Kind: "chat", Status: "error", Request: `{"keep":true}`})
	for i := 0; i < 50; i++ {
		Record(Entry{Kind: "chat", Status: "ok"})
	}

	found := false
	for _, e := range List(0) {
		if strings.Contains(e.Request, "keep") {
			found = true
		}
	}
	if !found {
		t.Fatal("成功请求不应挤掉已留存的失败请求体")
	}
	Clear()
}
