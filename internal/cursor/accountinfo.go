package cursor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"cursor-proxy/internal/auth"
	"cursor-proxy/internal/config"
	"cursor-proxy/internal/httpx"
)

const usageURL = "https://cursor.com/api/dashboard/get-current-period-usage"
const dashboardUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.6834.210 Safari/537.36"

// AccountUsage 账号本计费周期的用量。
type AccountUsage struct {
	PlanType         string  `json:"planType,omitempty"`
	APIPercentUsed   float64 `json:"apiPercentUsed"`
	AutoPercentUsed  float64 `json:"autoPercentUsed"`
	TotalPercentUsed float64 `json:"totalPercentUsed"`
	BillingCycleEnd  string  `json:"billingCycleEnd,omitempty"`
}

// FetchAccountPlan 用 access token 查账号套餐档位。
func FetchAccountPlan(token string) (string, error) {
	bearer := auth.ExtractBearer(token)
	req, _ := http.NewRequest(http.MethodGet, config.Get().CursorBackendURL+"/auth/full_stripe_profile", nil)
	req.Header.Set("accept", "application/json")
	req.Header.Set("authorization", "Bearer "+bearer)
	req.Header.Set("user-agent", "connect-es/1.6.1")
	resp, err := httpx.Client("").Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("full_stripe_profile HTTP %d: %s", resp.StatusCode, snippet(raw, 160))
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return "", fmt.Errorf("bad json")
	}
	for _, k := range []string{"membershipType", "membership_type", "planType", "plan_type"} {
		if s, ok := data[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s), nil
		}
	}
	return "", nil
}

func workosIDFromJwt(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) < 2 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var m struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(data, &m) != nil || m.Sub == "" {
		return ""
	}
	if i := strings.LastIndex(m.Sub, "|"); i >= 0 {
		return m.Sub[i+1:]
	}
	return m.Sub
}

func normalizeSessionCookie(raw string) string {
	value := strings.TrimSpace(raw)
	if strings.Contains(value, "%3A%3A") {
		return value
	}
	if idx := strings.Index(value, "::"); idx > 0 {
		return value[:idx] + "%3A%3A" + urlEncode(value[idx+2:])
	}
	if workosID := workosIDFromJwt(value); workosID != "" {
		return workosID + "%3A%3A" + urlEncode(value)
	}
	return value
}

func normalizePercent(v any) float64 {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return math.Round(f*100) / 100
}

// FetchAccountUsage 查询账号本计费周期用量（需 web 形式 session token）。
func FetchAccountUsage(sessionToken string) (AccountUsage, error) {
	cookie := normalizeSessionCookie(sessionToken)
	req, _ := http.NewRequest(http.MethodPost, usageURL, strings.NewReader("{}"))
	req.Header.Set("accept", "application/json")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("cookie", "WorkosCursorSessionToken="+cookie)
	req.Header.Set("origin", "https://cursor.com")
	req.Header.Set("referer", "https://cursor.com/dashboard?tab=usage")
	req.Header.Set("user-agent", dashboardUA)
	resp, err := httpx.Client("").Do(req)
	if err != nil {
		return AccountUsage{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return AccountUsage{}, fmt.Errorf("get-current-period-usage HTTP %d: %s", resp.StatusCode, snippet(raw, 160))
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		return AccountUsage{}, fmt.Errorf("bad json")
	}
	out := AccountUsage{}
	for _, k := range []string{"planType", "plan_type", "membershipType", "membership_type"} {
		if s, ok := data[k].(string); ok && strings.TrimSpace(s) != "" {
			out.PlanType = strings.TrimSpace(s)
			break
		}
	}
	if pu, ok := data["planUsage"].(map[string]any); ok {
		out.APIPercentUsed = normalizePercent(pu["apiPercentUsed"])
		out.AutoPercentUsed = normalizePercent(pu["autoPercentUsed"])
		out.TotalPercentUsed = normalizePercent(pu["totalPercentUsed"])
	}
	if bce, ok := data["billingCycleEnd"]; ok && bce != nil {
		out.BillingCycleEnd = fmt.Sprint(bce)
	}
	return out, nil
}

func snippet(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}
