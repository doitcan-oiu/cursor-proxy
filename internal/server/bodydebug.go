package server

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"
)

// PROXY_DEBUG_BODY=1 时把 /v1 请求体与响应体打到日志。
// 对接第三方客户端（OpenCode、Cherry Studio 等）出问题时，
// 这是最直接的排查手段——能看清对方到底发了什么、我们回了什么。
var bodyDebug = os.Getenv("PROXY_DEBUG_BODY") != ""

type capturingWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *capturingWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *capturingWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func withBodyDebug(next http.Handler) http.Handler {
	if !bodyDebug {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqBody, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(reqBody))
		log.Printf("[req] %s %s\n%s", r.Method, r.URL.Path, string(reqBody))

		cw := &capturingWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(cw, r)

		log.Printf("[resp %d] %s\n%s", cw.status, r.URL.Path, cw.body.String())
	})
}
