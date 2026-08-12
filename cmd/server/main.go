// Command server 启动 Cursor -> OpenAI 反向代理。
package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/server"
	"cursor-proxy/internal/vpn"
	"cursor-proxy/internal/webui"
)

func main() {
	cfg := config.Get()

	// 后台定时刷新临近过期的 token。
	cursor.StartRefreshSweeper(5 * time.Minute)
	// 恢复上次启用的内置 VPN（不阻塞启动）。
	go vpn.RestoreIfEnabled()

	handler := server.New()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))

	base := fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
	browseHost := cfg.Host
	if browseHost == "0.0.0.0" || browseHost == "::" {
		browseHost = "127.0.0.1"
	}
	line := strings.Repeat("=", 60)
	fmt.Println(line)
	fmt.Println("  Cursor -> OpenAI 反代已启动 (Go)")
	fmt.Printf("  Base URL   : %s/v1\n", base)
	if webui.Available() {
		fmt.Printf("  管理界面   : http://%s:%d  ← 浏览器打开，用下面的口令登录\n", browseHost, cfg.Port)
	} else {
		fmt.Println("  管理界面   : 未构建（在 webui/ 下执行 npm install && npm run build 后重新编译）")
	}
	fmt.Printf("  Admin token: %s\n", cfg.AdminToken)
	if cfg.AdminTokenGenerated {
		fmt.Println("  (以上 ADMIN_TOKEN 为自动生成，建议写入环境变量固定)")
	}
	fmt.Printf("  Cursor 凭证: %s\n", tokenState(auth.HasCursorToken()))
	fmt.Printf("  代理 Key   : 已签发 %d 把\n", len(auth.ListProxyKeys()))
	fmt.Println(line)

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func tokenState(has bool) string {
	if has {
		return "已配置"
	}
	return "未配置 (先调用 /admin/cursor-tokens)"
}
