// Package openai 实现 OpenAI / Anthropic 兼容的 HTTP 处理器与故障转移管线。
package openai

import (
	"encoding/json"
	"net/http"
)

// ErrorBody 以 OpenAI 风格返回错误。
type ErrorBody struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Code    *string `json:"code"`
		Param   *string `json:"param"`
	} `json:"error"`
}

// WriteJSON 写出 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// SendError 以 OpenAI 风格 JSON 返回错误。
func SendError(w http.ResponseWriter, status int, message, typ, code string) {
	var body ErrorBody
	body.Error.Message = message
	body.Error.Type = typ
	if code != "" {
		body.Error.Code = &code
	}
	WriteJSON(w, status, body)
}
