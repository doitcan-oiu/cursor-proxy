package types

import (
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"
	"time"

	// 注册解码器，仅用于读取图片尺寸
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// MaxImageBytes 是单张图片的大小上限。
// 图片要整个塞进 protobuf 请求体，太大既拖慢上传也容易被上游拒绝。
const MaxImageBytes = 20 << 20

// fetchTimeout 是拉取远程图片的超时。客户端多数直接发 data: URL，
// 走网络只是兼容 OpenAI 那种传 https 链接的用法，不值得等太久。
const fetchTimeout = 20 * time.Second

// DecodeImageURL 把 OpenAI 的 image_url 还原成图片字节。
// 支持 data: 内联和 http(s) 链接两种形式。
func DecodeImageURL(url string) (Image, error) {
	if strings.HasPrefix(url, "data:") {
		return decodeDataURL(url)
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return fetchImage(url)
	}
	return Image{}, errors.New("不支持的图片地址（只支持 data: 与 http(s):）")
}

// DecodeImageBase64 把 Anthropic 的 base64 图片块还原成图片字节。
func DecodeImageBase64(mediaType, data string) (Image, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return Image{}, fmt.Errorf("图片 base64 解码失败: %w", err)
	}
	return newImage(raw, mediaType)
}

func decodeDataURL(url string) (Image, error) {
	comma := strings.IndexByte(url, ',')
	if comma < 0 {
		return Image{}, errors.New("data URL 缺少逗号分隔")
	}
	meta, payload := url[5:comma], url[comma+1:]
	if !strings.Contains(meta, "base64") {
		return Image{}, errors.New("data URL 只支持 base64 编码")
	}
	mime, _, _ := strings.Cut(meta, ";")

	// 部分客户端会发带换行的 base64，标准解码器不接受
	payload = strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(payload)
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return Image{}, fmt.Errorf("data URL 解码失败: %w", err)
	}
	return newImage(raw, mime)
}

func fetchImage(url string) (Image, error) {
	client := &http.Client{Timeout: fetchTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return Image{}, fmt.Errorf("拉取图片失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Image{}, fmt.Errorf("拉取图片失败: HTTP %d", resp.StatusCode)
	}

	// 多读 1 字节以便区分「刚好到上限」和「超了」
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxImageBytes+1))
	if err != nil {
		return Image{}, fmt.Errorf("读取图片失败: %w", err)
	}
	return newImage(raw, resp.Header.Get("Content-Type"))
}

func newImage(raw []byte, mime string) (Image, error) {
	if len(raw) == 0 {
		return Image{}, errors.New("图片内容为空")
	}
	if len(raw) > MaxImageBytes {
		return Image{}, fmt.Errorf("图片超过 %d MB 上限", MaxImageBytes>>20)
	}

	mime, _, _ = strings.Cut(mime, ";")
	mime = strings.TrimSpace(mime)

	img := Image{Data: raw, MimeType: mime}
	// 尺寸只用于告知上游，解不出来（如 webp）也照发
	if cfg, _, err := image.DecodeConfig(strings.NewReader(string(raw))); err == nil {
		img.Width, img.Height = cfg.Width, cfg.Height
	}
	if img.MimeType == "" {
		img.MimeType = sniffMime(raw)
	}
	return img, nil
}

// sniffMime 在客户端没给 MIME 时按魔数判断，实在认不出就交给上游去猜。
func sniffMime(raw []byte) string {
	switch {
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return "image/jpeg"
	case len(raw) >= 6 && string(raw[:6]) == "GIF89a", len(raw) >= 6 && string(raw[:6]) == "GIF87a":
		return "image/gif"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image/webp"
	}
	return "application/octet-stream"
}

// CollectImages 汇总整轮对话里的图片，按出现顺序排列。
func CollectImages(messages []Message) []Image {
	var out []Image
	for _, m := range messages {
		out = append(out, m.Images...)
	}
	return out
}
