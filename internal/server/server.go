// Package server 组装所有路由：对外 OpenAI/Anthropic 接口、/admin 管理接口、
// /manage 管理 REST API（替代原 Electron IPC）与静态管理界面。
package server

import (
	"net/http"

	"cursor-proxy/internal/admin"
	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/openai"
	"cursor-proxy/internal/webui"
)

// New 构造顶层 http.Handler。
func New() http.Handler {
	mux := http.NewServeMux()

	// 根路径交给内嵌的 Vue 管理界面（SPA，未知路径回落 index.html）；
	// 服务自身的 JSON 简介移到 /api。
	mux.Handle("/", webui.Handler())
	mux.HandleFunc("GET /api", apiInfo)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})

	// 对外接口：先过代理 Key 鉴权。
	mux.Handle("GET /v1/models", proxyKeyAuth(http.HandlerFunc(openai.Models)))
	mux.Handle("POST /v1/chat/completions", proxyKeyAuth(withBodyDebug(http.HandlerFunc(openai.ChatCompletions))))
	mux.Handle("POST /v1/messages", proxyKeyAuth(withBodyDebug(http.HandlerFunc(openai.Messages))))

	// 管理接口。
	mux.Handle("/admin/", admin.Handler())

	// 兼容社区习惯：GET /cursor/loginDeepControl。
	mux.HandleFunc("GET /cursor/loginDeepControl", loginDeepControl)

	// 管理 REST API（WebUI 与外部脚本共用）。
	mux.Handle("/manage/", manageHandler())

	return withCommonHeaders(mux)
}

func apiInfo(w http.ResponseWriter, r *http.Request) {
	openai.WriteJSON(w, 200, map[string]any{
		"name":   "cursor-openai-proxy",
		"status": "ok",
		"endpoints": []string{
			"/v1/models",
			"/v1/chat/completions (OpenAI)",
			"/v1/messages (Anthropic/Claude)",
			"/admin/*",
			"/manage/* (管理 API)",
		},
		"webui": webui.Available(),
	})
}

// proxyKeyAuth 校验自管代理 Key：OpenAI 用 Authorization: Bearer，Anthropic 用 x-api-key。
func proxyKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := stripBearer(r.Header.Get("authorization"))
		apiKey := r.Header.Get("x-api-key")
		if !auth.ValidateProxyKey(bearer) && !auth.ValidateProxyKey(apiKey) {
			openai.SendError(w, 401, "Invalid API key. Provide a proxy key issued by this server.",
				"authentication_error", "invalid_api_key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func loginDeepControl(w http.ResponseWriter, r *http.Request) {
	workos := stripBearer(r.Header.Get("authorization"))
	if workos == "" {
		openai.WriteJSON(w, 400, map[string]any{"error": "Provide Authorization: Bearer <WorkosCursorSessionToken>."})
		return
	}
	token, err := auth.ExchangeWorkosCookie(workos)
	if err != nil {
		openai.WriteJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	entry := auth.AddCursorToken(token, "loginDeepControl", "")
	openai.WriteJSON(w, 200, map[string]any{"accessToken": token, "id": entry.ID})
}

func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 允许管理界面从浏览器直接调用。
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "authorization, content-type, x-api-key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
