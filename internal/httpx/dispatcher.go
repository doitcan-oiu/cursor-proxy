// Package httpx 统一管理出站 HTTP 客户端。
//
// Cursor 的 chat 接口要求 HTTP/2，且需要支持「每账号独立出口代理」。这里按代理 URL
// 缓存复用 *http.Client（含连接池），并维护一个内置 VPN 启用后的全局出口覆盖。
// 出口优先级：账号独立代理 > 内置 VPN 全局出口 > CURSOR_HTTP_PROXY > 直连。
package httpx

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"cursor-proxy/internal/config"
)

var (
	cacheMu sync.Mutex
	cache   = map[string]*http.Client{}

	overrideMu  sync.RWMutex
	globalProxy string
)

// SetGlobalProxyOverride 设置/清除内置 VPN 的全局出口代理。
func SetGlobalProxyOverride(u string) {
	overrideMu.Lock()
	globalProxy = u
	overrideMu.Unlock()
}

// GlobalProxyOverride 读取当前全局出口代理。
func GlobalProxyOverride() string {
	overrideMu.RLock()
	defer overrideMu.RUnlock()
	return globalProxy
}

// Client 按出口优先级返回一个可复用的 HTTP/2 客户端。proxyURL 为账号独立代理。
func Client(proxyURL string) *http.Client {
	uri := proxyURL
	if uri == "" {
		uri = GlobalProxyOverride()
	}
	if uri == "" {
		uri = config.Get().HTTPProxy
	}
	key := uri
	if key == "" {
		key = "__direct__"
	}

	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := cache[key]; ok {
		return c
	}
	c := buildClient(uri)
	cache[key] = c
	return c
}

func buildClient(proxyURI string) *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{},
	}
	if proxyURI != "" {
		if u, err := url.Parse(proxyURI); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	// 显式启用 h2，保证即使经代理隧道也走多路复用。
	_ = http2.ConfigureTransport(transport)

	return &http.Client{
		Transport: transport,
		// 流式对话时长可能很久，靠上层 context 控制，不设整体超时。
		Timeout: 0,
	}
}
