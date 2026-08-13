package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"

	"cursor-proxy/internal/tools"
	"cursor-proxy/internal/types"
)

func testPNG(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestParseMessagesExtractsImages(t *testing.T) {
	content, _ := json.Marshal([]any{
		map[string]any{"type": "text", "text": "这是什么"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": testPNG(t, 30, 20)}},
	})
	msgs := parseMessages([]rawMessage{{Role: "user", Content: content}})

	if len(msgs) != 1 {
		t.Fatalf("应有 1 条消息，实际 %d", len(msgs))
	}
	if len(msgs[0].Images) != 1 {
		t.Fatalf("应取出 1 张图，实际 %d", len(msgs[0].Images))
	}
	if msgs[0].Images[0].Width != 30 || msgs[0].Images[0].Height != 20 {
		t.Fatalf("尺寸应为 30x20，实际 %dx%d", msgs[0].Images[0].Width, msgs[0].Images[0].Height)
	}
	if got := types.ContentToText(msgs[0].Content); got != "这是什么" {
		t.Fatalf("文字部分应保留，实际 %q", got)
	}
}

// 坏图片只跳过，不能让整轮对话失败——客户端常常一次带好几张。
func TestParseMessagesSkipsBadImages(t *testing.T) {
	content, _ := json.Marshal([]any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,!!!"}},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": testPNG(t, 5, 5)}},
	})
	msgs := parseMessages([]rawMessage{{Role: "user", Content: content}})
	if len(msgs[0].Images) != 1 {
		t.Fatalf("坏图应跳过、好图应保留，实际 %d 张", len(msgs[0].Images))
	}
}

// 回归：注入工具提示词时若整个重建消息结构体，会把 Images 字段丢掉，
// 表现为「带图 + 声明工具」的请求里模型看不见图，只回一句没有这个工具。
func TestInjectToolPromptKeepsImages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "读这张发票", Images: []types.Image{{Data: []byte{1}, MimeType: "image/png"}}},
	}
	defs := []tools.Definition{{Name: "record", Description: "记录", Parameters: json.RawMessage(`{"type":"object"}`)}}

	out := injectToolPrompt(msgs, defs, tools.Choice{Mode: "auto"})

	var total int
	for _, m := range out {
		total += len(m.Images)
	}
	if total != 1 {
		t.Fatalf("注入提示词后图片不能丢失，实际剩 %d 张", total)
	}
	if len(types.CollectImages(out)) != 1 {
		t.Fatal("汇总时也应能拿到这张图")
	}
}

// 没有 system 消息时会在最前面插一条，同样不能影响后面消息上的图片。
func TestInjectToolPromptKeepsImagesWithoutSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "看图", Images: []types.Image{{Data: []byte{1}}, {Data: []byte{2}}}},
	}
	defs := []tools.Definition{{Name: "x", Parameters: json.RawMessage(`{"type":"object"}`)}}
	out := injectToolPrompt(msgs, defs, tools.Choice{Mode: "auto"})
	if len(types.CollectImages(out)) != 2 {
		t.Fatalf("两张图都应保留，实际 %d", len(types.CollectImages(out)))
	}
}

func TestAnthropicBlocksToImages(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","text":"看这个"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64PNG(t) + `"}}
	]`)
	imgs := blocksToImages(raw)
	if len(imgs) != 1 {
		t.Fatalf("应取出 1 张图，实际 %d", len(imgs))
	}
	if imgs[0].MimeType != "image/png" {
		t.Fatalf("MIME 应为 image/png，实际 %q", imgs[0].MimeType)
	}
	if got := blocksToText(raw); got != "看这个" {
		t.Fatalf("文字部分应保留，实际 %q", got)
	}
}

// 工具结果里也可能夹带图片（比如截图类工具的回传），要能递归取出。
func TestAnthropicBlocksToImagesRecursesToolResult(t *testing.T) {
	raw := json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":[
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64PNG(t) + `"}}
	]}]`)
	if len(blocksToImages(raw)) != 1 {
		t.Fatal("tool_result 里的图片应被取出")
	}
}

func base64PNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}
