package types

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pngBytes 造一张指定尺寸的 PNG，用于验证尺寸解析。
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dataURL(mime string, raw []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw)
}

func TestDecodeImageURLReadsDataURL(t *testing.T) {
	raw := pngBytes(t, 40, 25)
	img, err := DecodeImageURL(dataURL("image/png", raw))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img.Data, raw) {
		t.Fatal("图片字节应原样保留")
	}
	if img.MimeType != "image/png" {
		t.Fatalf("MIME 应为 image/png，实际 %q", img.MimeType)
	}
	if img.Width != 40 || img.Height != 25 {
		t.Fatalf("尺寸应解析为 40x25，实际 %dx%d", img.Width, img.Height)
	}
}

// 有些客户端会把 base64 折行，标准解码器不接受，必须先清掉空白。
func TestDecodeImageURLToleratesWrappedBase64(t *testing.T) {
	raw := pngBytes(t, 8, 8)
	enc := base64.StdEncoding.EncodeToString(raw)
	var wrapped strings.Builder
	for i := 0; i < len(enc); i += 40 {
		end := min(i+40, len(enc))
		wrapped.WriteString(enc[i:end] + "\n")
	}
	img, err := DecodeImageURL("data:image/png;base64," + wrapped.String())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img.Data, raw) {
		t.Fatal("折行的 base64 应能正常解码")
	}
}

// 客户端不给 MIME 时按魔数认，别把 image/png 发成空串。
func TestDecodeImageURLSniffsMissingMime(t *testing.T) {
	raw := pngBytes(t, 4, 4)
	img, err := DecodeImageURL("data:;base64," + base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if img.MimeType != "image/png" {
		t.Fatalf("应按魔数认出 image/png，实际 %q", img.MimeType)
	}
}

func TestDecodeImageURLRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"没有逗号":      "data:image/png;base64",
		"不是 base64": "data:image/png,hello",
		"内容为空":      "data:image/png;base64,",
		"不支持的协议":    "ftp://example.com/a.png",
		"空串":        "",
	}
	for name, url := range cases {
		if _, err := DecodeImageURL(url); err == nil {
			t.Fatalf("%s 应报错", name)
		}
	}
}

func TestDecodeImageBase64(t *testing.T) {
	raw := pngBytes(t, 12, 6)
	img, err := DecodeImageBase64("image/png", base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 12 || img.Height != 6 {
		t.Fatalf("尺寸应为 12x6，实际 %dx%d", img.Width, img.Height)
	}
	if _, err := DecodeImageBase64("image/png", "!!!not base64!!!"); err == nil {
		t.Fatal("坏的 base64 应报错")
	}
}

// 认不出尺寸的格式（如 webp）也要照发，不能因为解不出宽高就丢掉整张图。
func TestUnknownFormatStillCarriesData(t *testing.T) {
	raw := append([]byte("RIFF\x00\x00\x00\x00WEBP"), bytes.Repeat([]byte{1}, 32)...)
	img, err := DecodeImageBase64("", base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if img.MimeType != "image/webp" {
		t.Fatalf("应认出 webp，实际 %q", img.MimeType)
	}
	if img.Width != 0 || img.Height != 0 {
		t.Fatalf("解不出尺寸时应留 0，实际 %dx%d", img.Width, img.Height)
	}
	if len(img.Data) != len(raw) {
		t.Fatal("解不出尺寸也要保留原始字节")
	}
}

func TestFetchImageOverHTTP(t *testing.T) {
	raw := pngBytes(t, 20, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	img, err := DecodeImageURL(srv.URL + "/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if img.Width != 20 || img.Height != 10 {
		t.Fatalf("远程图片尺寸应为 20x10，实际 %dx%d", img.Width, img.Height)
	}
}

func TestFetchImageReportsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, err := DecodeImageURL(srv.URL + "/missing.png"); err == nil {
		t.Fatal("404 应报错")
	}
}

// 超大图片要挡在本地，别等上游拒绝。
func TestOversizedImageIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0}, MaxImageBytes+64))
	}))
	defer srv.Close()
	if _, err := DecodeImageURL(srv.URL + "/big.png"); err == nil {
		t.Fatal("超过上限的图片应报错")
	}
}

func TestCollectImagesKeepsOrder(t *testing.T) {
	msgs := []Message{
		{Role: "user", Images: []Image{{MimeType: "a"}, {MimeType: "b"}}},
		{Role: "assistant"},
		{Role: "user", Images: []Image{{MimeType: "c"}}},
	}
	got := CollectImages(msgs)
	if len(got) != 3 || got[0].MimeType != "a" || got[2].MimeType != "c" {
		t.Fatalf("应按出现顺序汇总三张图，实际 %+v", got)
	}
	if CollectImages([]Message{{Role: "user"}}) != nil {
		t.Fatal("没有图片时应返回 nil")
	}
}
