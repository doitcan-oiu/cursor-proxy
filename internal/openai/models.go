package openai

import (
	"net/http"
	"time"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/cursor"
)

var fallbackModels = []string{"auto", "composer-2.5", "claude-4.5-sonnet", "gpt-5.1", "gemini-3.1-pro"}

func toModelList(names []string) map[string]any {
	created := time.Now().Unix()
	ids := names
	hasAuto := false
	for _, n := range names {
		if n == "auto" {
			hasAuto = true
			break
		}
	}
	if !hasAuto {
		ids = append([]string{"auto"}, names...)
	}
	seen := map[string]bool{}
	var data []map[string]any
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		data = append(data, map[string]any{"id": id, "object": "model", "created": created, "owned_by": "cursor"})
	}
	return map[string]any{"object": "list", "data": data}
}

// Models 处理 GET /v1/models。
func Models(w http.ResponseWriter, r *http.Request) {
	if !auth.HasCursorToken() {
		SendError(w, 503, "No Cursor token configured. Add one via POST /admin/cursor-tokens.", "server_error", "no_cursor_token")
		return
	}
	account := cursor.AcquireAccount(nil)
	if account == nil {
		WriteJSON(w, 200, toModelList(fallbackModels))
		return
	}
	models, err := cursor.GetUsableModels(account.Bearer, account.ProxyURL)
	if err != nil {
		status := 0
		if aerr, ok := err.(*cursor.AgentUpstreamError); ok {
			status = aerr.Status
		}
		cursor.ReleaseAccount(account.ID, cursor.ClassifyStatus(status).Outcome, err.Error())
		WriteJSON(w, 200, toModelList(fallbackModels))
		return
	}
	cursor.ReleaseAccount(account.ID, cursor.OutcomeSuccess, "")
	var names []string
	for _, m := range models {
		names = append(names, m.ID)
		names = append(names, m.Aliases...)
	}
	if len(names) == 0 {
		names = fallbackModels
	}
	WriteJSON(w, 200, toModelList(names))
}
