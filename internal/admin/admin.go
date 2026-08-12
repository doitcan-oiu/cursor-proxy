// Package admin 实现受 ADMIN_TOKEN 保护的 /admin/* 管理接口。
package admin

import (
	"encoding/json"
	"net/http"
	"sync"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/cursor"
	"cursor-proxy/internal/openai"
	"cursor-proxy/internal/store"
)

// Handler 返回挂载在 /admin/ 下的处理器（已含鉴权中间件）。
func Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /admin/keys", createKey)
	mux.HandleFunc("GET /admin/keys", listKeys)
	mux.HandleFunc("DELETE /admin/keys/{id}", deleteKey)

	mux.HandleFunc("GET /admin/cursor-tokens", listTokens)
	mux.HandleFunc("POST /admin/cursor-tokens", addToken)
	mux.HandleFunc("POST /admin/cursor-tokens/batch", batchTokens)
	mux.HandleFunc("GET /admin/cursor-tokens/check", checkTokens)
	mux.HandleFunc("POST /admin/cursor-tokens/exchange", exchangeToken)
	mux.HandleFunc("DELETE /admin/cursor-tokens/{id}", deleteToken)

	mux.HandleFunc("GET /admin/accounts/health", accountsHealth)

	return authMiddleware(mux)
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := stripBearer(r.Header.Get("authorization"))
		if token == "" || token != config.Get().AdminToken {
			openai.WriteJSON(w, 401, map[string]any{"error": "Unauthorized. Provide Authorization: Bearer <ADMIN_TOKEN>."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func createKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	name := body.Name
	if name == "" {
		name = "unnamed"
	}
	created := auth.CreateProxyKey(name)
	openai.WriteJSON(w, 200, map[string]any{
		"id":        created.Entry.ID,
		"name":      created.Entry.Name,
		"key":       created.RawKey,
		"createdAt": created.Entry.CreatedAt,
		"note":      "Save this key now, it will not be shown again.",
	})
}

func listKeys(w http.ResponseWriter, r *http.Request) {
	openai.WriteJSON(w, 200, map[string]any{"object": "list", "data": auth.ListProxyKeys()})
}

func deleteKey(w http.ResponseWriter, r *http.Request) {
	ok := auth.RevokeProxyKey(r.PathValue("id"))
	openai.WriteJSON(w, statusCode(ok), map[string]any{"deleted": ok})
}

func listTokens(w http.ResponseWriter, r *http.Request) {
	openai.WriteJSON(w, 200, map[string]any{"object": "list", "data": auth.ListCursorTokens()})
}

func addToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token        string `json:"token"`
		Label        string `json:"label"`
		RefreshToken string `json:"refreshToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Token == "" {
		openai.WriteJSON(w, 400, map[string]any{"error": "Missing 'token' in body."})
		return
	}
	token := body.Token
	refreshToken := body.RefreshToken
	exchanged := false
	if auth.IsWebToken(token) {
		r2, err := auth.ExchangeWorkosCookieFull(token)
		if err != nil {
			openai.WriteJSON(w, 502, map[string]any{"error": "web token 交换失败: " + err.Error(), "hint": "该 web 会话可能已在网页端登出。"})
			return
		}
		token = r2.Token
		if refreshToken == "" {
			refreshToken = r2.RefreshToken
		}
		exchanged = true
	}
	entry := auth.AddCursorToken(token, body.Label, refreshToken)
	openai.WriteJSON(w, 200, map[string]any{
		"id": entry.ID, "label": entry.Label, "createdAt": entry.CreatedAt,
		"expiresAt": entry.ExpiresAt, "hasRefresh": entry.RefreshToken != "", "exchangedFromWeb": exchanged,
	})
}

func batchTokens(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tokens   []json.RawMessage `json:"tokens"`
		Text     string            `json:"text"`
		Validate bool              `json:"validate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		openai.WriteJSON(w, 400, map[string]any{"error": "Invalid JSON body."})
		return
	}
	var items []auth.BatchImportItem
	if body.Text != "" {
		items = append(items, auth.ParseTokensText(body.Text)...)
	}
	for _, raw := range body.Tokens {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			items = append(items, auth.BatchImportItem{Token: s})
			continue
		}
		var obj struct {
			Token        string `json:"token"`
			Label        string `json:"label"`
			RefreshToken string `json:"refreshToken"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Token != "" {
			items = append(items, auth.BatchImportItem{Token: obj.Token, Label: obj.Label, RefreshToken: obj.RefreshToken})
		}
	}
	if len(items) == 0 {
		openai.WriteJSON(w, 400, map[string]any{"error": "Provide 'tokens' (array) and/or 'text' (string)."})
		return
	}
	result := auth.AddCursorTokensBatch(items)

	resp := map[string]any{
		"imported":       len(result.Added),
		"duplicateCount": len(result.Duplicates),
		"invalidCount":   len(result.Invalid),
		"added":          result.Added,
		"duplicates":     result.Duplicates,
		"invalid":        result.Invalid,
	}
	if body.Validate {
		type validated struct {
			ID     string `json:"id"`
			Label  string `json:"label"`
			OK     bool   `json:"ok"`
			Status int    `json:"status"`
			Detail string `json:"detail"`
		}
		out := make([]validated, len(result.Added))
		mapLimit(len(result.Added), 5, func(i int) {
			a := result.Added[i]
			entry, ok := auth.GetCursorTokenByID(a.ID)
			if !ok {
				out[i] = validated{ID: a.ID, Label: a.Label, Detail: "not found"}
				return
			}
			c := cursor.CheckToken(cursor.BuildContext(entry.ID, entry.Token, true))
			out[i] = validated{ID: a.ID, Label: a.Label, OK: c.OK, Status: c.Status, Detail: c.Detail}
		})
		validCount := 0
		for _, v := range out {
			if v.OK {
				validCount++
			}
		}
		resp["validated"] = out
		resp["validCount"] = validCount
	}
	openai.WriteJSON(w, 200, resp)
}

func checkTokens(w http.ResponseWriter, r *http.Request) {
	tokens := store.Read().CursorTokens
	type result struct {
		ID          string   `json:"id"`
		Label       string   `json:"label"`
		Plan        string   `json:"plan,omitempty"`
		UsedPercent *float64 `json:"usedPercent,omitempty"`
		Exhausted   *bool    `json:"exhausted,omitempty"`
		OK          bool     `json:"ok"`
		Status      int      `json:"status"`
		Detail      string   `json:"detail"`
	}
	out := make([]result, len(tokens))
	mapLimit(len(tokens), 5, func(i int) {
		t := tokens[i]
		c := cursor.CheckToken(cursor.BuildContext(t.ID, t.Token, true))
		res := result{ID: t.ID, Label: t.Label, OK: c.OK, Status: c.Status, Detail: c.Detail}
		if c.OK {
			if plan, err := cursor.FetchAccountPlan(t.Token); err == nil {
				res.Plan = plan
			}
			if u, err := cursor.FetchAccountUsage(t.Token); err == nil {
				up := u.TotalPercentUsed
				ex := u.TotalPercentUsed >= 100
				res.UsedPercent = &up
				res.Exhausted = &ex
			}
		}
		out[i] = res
	})
	valid, usable := 0, 0
	for _, r := range out {
		if r.OK {
			valid++
			if r.Exhausted == nil || !*r.Exhausted {
				usable++
			}
		}
	}
	openai.WriteJSON(w, 200, map[string]any{
		"total": len(out), "valid": valid, "invalid": len(out) - valid, "usable": usable, "results": out,
	})
}

func exchangeToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkosToken string `json:"workosToken"`
		Label       string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.WorkosToken == "" {
		openai.WriteJSON(w, 400, map[string]any{"error": "Missing 'workosToken' in body."})
		return
	}
	exchanged, err := auth.ExchangeWorkosCookie(body.WorkosToken)
	if err != nil {
		openai.WriteJSON(w, 502, map[string]any{"error": err.Error()})
		return
	}
	label := body.Label
	if label == "" {
		label = "deep-control"
	}
	entry := auth.AddCursorToken(exchanged, label, "")
	openai.WriteJSON(w, 200, map[string]any{"id": entry.ID, "label": entry.Label, "createdAt": entry.CreatedAt})
}

func deleteToken(w http.ResponseWriter, r *http.Request) {
	ok := auth.RemoveCursorToken(r.PathValue("id"))
	openai.WriteJSON(w, statusCode(ok), map[string]any{"deleted": ok})
}

func accountsHealth(w http.ResponseWriter, r *http.Request) {
	snap := cursor.HealthSnapshot()
	available := 0
	for _, s := range snap {
		if s.Available {
			available++
		}
	}
	openai.WriteJSON(w, 200, map[string]any{"total": len(snap), "available": available, "accounts": snap})
}

func statusCode(ok bool) int {
	if ok {
		return 200
	}
	return 404
}

// mapLimit 限并发执行 0..n 的任务。
func mapLimit(n, limit int, fn func(i int)) {
	if n == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, limit)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}
