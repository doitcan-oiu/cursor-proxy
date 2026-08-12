package cursor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/httpx"
	"cursor-proxy/internal/proto"
)

// UpstreamError 传统 Chat/Models 端点的上游错误。
type UpstreamError struct {
	Status int
	Msg    string
}

func (e *UpstreamError) Error() string { return e.Msg }

// NewUpstreamError 构造上游错误。
func NewUpstreamError(status int, msg string) *UpstreamError {
	return &UpstreamError{Status: status, Msg: msg}
}

func modelsURL() string {
	return config.Get().CursorBackendURL + "/aiserver.v1.AiService/AvailableModels"
}

// TokenCheckResult 轻量验号结果。
type TokenCheckResult struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func doModelsRequest(ctx Context) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPost, modelsURL(), nil)
	req.Header = buildModelsHeaders(ctx)
	return httpx.Client(ctx.ProxyURL).Do(req)
}

// CheckToken 用 AvailableModels 探测凭证是否被 Cursor 接受。
func CheckToken(ctx Context) TokenCheckResult {
	resp, err := doModelsRequest(ctx)
	if err != nil {
		return TokenCheckResult{OK: false, Status: 0, Detail: err.Error()}
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		return TokenCheckResult{OK: true, Status: 200, Detail: fmt.Sprintf("%d models", len(proto.DecodeAvailableModels(buf)))}
	}
	detail := snippet(buf, 160)
	var parsed struct {
		Code    string `json:"code"`
		Details []struct {
			Debug struct {
				Error string `json:"error"`
			} `json:"debug"`
		} `json:"details"`
	}
	if json.Unmarshal(buf, &parsed) == nil {
		if len(parsed.Details) > 0 && parsed.Details[0].Debug.Error != "" {
			detail = parsed.Details[0].Debug.Error
		} else if parsed.Code != "" {
			detail = parsed.Code
		}
	}
	return TokenCheckResult{OK: false, Status: resp.StatusCode, Detail: detail}
}

// AvailableModels 拉取账号可用模型名列表（传统端点）。
func AvailableModels(ctx Context) ([]string, error) {
	resp, err := doModelsRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, NewUpstreamError(resp.StatusCode, snippet(buf, 300))
	}
	return proto.DecodeAvailableModels(buf), nil
}

var (
	warmedMu sync.Mutex
	warmed   = map[string]bool{}
)

// warmupOnce 每账号仅预热一次，模拟真实客户端启动拉模型。
func warmupOnce(ctx Context) {
	if ctx.ID == "" {
		return
	}
	warmedMu.Lock()
	if warmed[ctx.ID] {
		warmedMu.Unlock()
		return
	}
	warmed[ctx.ID] = true
	warmedMu.Unlock()
	go func() {
		if resp, err := doModelsRequest(ctx); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
}
