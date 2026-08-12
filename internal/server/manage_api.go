package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/manage"
	"cursor-proxy/internal/openai"
	"cursor-proxy/internal/toollog"
)

var bearerRe = regexp.MustCompile(`(?i)^Bearer\s+`)

func stripBearer(s string) string {
	return strings.TrimSpace(bearerRe.ReplaceAllString(s, ""))
}

// manageHandler 管理 REST API（替代 Electron IPC 通道），复用 manage 门面。
// 受 ADMIN_TOKEN 保护（Authorization: Bearer 或 ?token= 查询参数）。
func manageHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /manage/server/info", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.GetServerInfo())
	})

	mux.HandleFunc("GET /manage/accounts", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.ListAccounts())
	})
	mux.HandleFunc("POST /manage/accounts/import", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		openai.WriteJSON(w, 200, manage.ImportAccounts(body.Text))
	})
	mux.HandleFunc("POST /manage/accounts/add", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
			Label string `json:"label"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		res, err := manage.AddAccount(body.Token, body.Label)
		if err != nil {
			openai.WriteJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		openai.WriteJSON(w, 200, res)
	})
	mux.HandleFunc("DELETE /manage/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, map[string]any{"deleted": manage.RemoveAccount(r.PathValue("id"))})
	})
	mux.HandleFunc("GET /manage/accounts/{id}/check", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.CheckAccount(r.PathValue("id")))
	})
	mux.HandleFunc("GET /manage/accounts/check-all", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.CheckAllAccounts())
	})
	mux.HandleFunc("GET /manage/accounts/health", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.AccountsHealth())
	})
	mux.HandleFunc("POST /manage/accounts/{id}/proxy", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		openai.WriteJSON(w, 200, map[string]any{"ok": manage.SetProxy(r.PathValue("id"), body.URL)})
	})

	mux.HandleFunc("GET /manage/logs", func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		openai.WriteJSON(w, 200, manage.GetLogs(since))
	})
	mux.HandleFunc("POST /manage/logs/clear", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, map[string]any{"ok": manage.ClearLogs()})
	})

	mux.HandleFunc("GET /manage/vpn/status", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.VPNGetStatus())
	})
	mux.HandleFunc("POST /manage/vpn/sub", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		openai.WriteJSON(w, 200, map[string]any{"ok": manage.VPNSetSub(body.URL)})
	})
	mux.HandleFunc("POST /manage/vpn/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		openai.WriteJSON(w, 200, map[string]any{"ok": manage.VPNSetMode(body.Mode)})
	})
	mux.HandleFunc("POST /manage/vpn/enable", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL  string `json:"url"`
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := manage.VPNEnable(body.URL, body.Mode); err != nil {
			openai.WriteJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		openai.WriteJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /manage/vpn/disable", func(w http.ResponseWriter, r *http.Request) {
		_ = manage.VPNDisable()
		openai.WriteJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /manage/vpn/install", func(w http.ResponseWriter, r *http.Request) {
		ok, err := manage.VPNInstall()
		if err != nil {
			openai.WriteJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		openai.WriteJSON(w, 200, map[string]any{"installed": ok})
	})
	mux.HandleFunc("POST /manage/vpn/test", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.VPNTest())
	})
	mux.HandleFunc("POST /manage/vpn/switch", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := manage.VPNSwitch(body.Name); err != nil {
			openai.WriteJSON(w, 502, map[string]any{"error": err.Error()})
			return
		}
		openai.WriteJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /manage/keys", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.ListKeys())
	})
	mux.HandleFunc("POST /manage/keys", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		created := manage.CreateKey(body.Name)
		openai.WriteJSON(w, 200, map[string]any{
			"id": created.Entry.ID, "name": created.Entry.Name, "key": created.RawKey, "createdAt": created.Entry.CreatedAt,
		})
	})
	mux.HandleFunc("DELETE /manage/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, map[string]any{"revoked": manage.RevokeKey(r.PathValue("id"))})
	})

	mux.HandleFunc("GET /manage/models", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, manage.ListModels())
	})

	// 未识别的上游工具：供界面展示与导出
	mux.HandleFunc("GET /manage/unknown-tools", func(w http.ResponseWriter, r *http.Request) {
		openai.WriteJSON(w, 200, toollog.List())
	})
	mux.HandleFunc("POST /manage/unknown-tools/clear", func(w http.ResponseWriter, r *http.Request) {
		toollog.Clear()
		openai.WriteJSON(w, 200, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /manage/chat/test", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model     string `json:"model"`
			Prompt    string `json:"prompt"`
			AccountID string `json:"accountId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		openai.WriteJSON(w, 200, manage.TestChat(body.Model, body.Prompt, body.AccountID))
	})

	mux.HandleFunc("POST /manage/chat/test-stream", testChatStream)

	return manageAuth(mux)
}

// testChatStream 以 SSE 把测试对话的增量实时推给 WebUI。
func testChatStream(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model     string `json:"model"`
		Prompt    string `json:"prompt"`
		AccountID string `json:"accountId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	flusher, ok := w.(http.Flusher)
	if !ok {
		openai.WriteJSON(w, 500, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(v any) {
		raw, _ := json.Marshal(v)
		_, _ = w.Write([]byte("data: " + string(raw) + "\n\n"))
		flusher.Flush()
	}

	done := r.Context().Done()
	manage.TestChatStream(body.Model, body.Prompt, body.AccountID, func(d manage.TestDelta) {
		select {
		case <-done:
			// 客户端已断开，丢弃后续增量
		default:
			send(d)
		}
	})
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
}

func manageAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := stripBearer(r.Header.Get("authorization"))
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" || token != config.Get().AdminToken {
			openai.WriteJSON(w, 401, map[string]any{"error": "Unauthorized. Provide ADMIN_TOKEN via Authorization: Bearer or ?token="})
			return
		}
		next.ServeHTTP(w, r)
	})
}
