// Package vpn 管理内置 Mihomo(Clash.Meta) 内核：下载、启动、切换节点、测速。
//
// 启用后把 httpx 的全局出口指向本地 mixed-port，让所有上游请求走机场节点。
package vpn

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"cursor-proxy/internal/config"
	"cursor-proxy/internal/httpx"
)

const (
	mixedPort = 7899
	ctlPort   = 9099
)

// Mode Clash 分组策略。
type Mode string

const (
	// ModeURLTest 自动测速选最优。
	ModeURLTest Mode = "url-test"
	// ModeFallback 故障转移。
	ModeFallback Mode = "fallback"
	// ModeLoadBalance 负载均衡轮询。
	ModeLoadBalance Mode = "load-balance"
)

type settings struct {
	SubURL  string `json:"subUrl"`
	Enabled bool   `json:"enabled"`
	Mode    Mode   `json:"mode,omitempty"`
}

var (
	mu     sync.Mutex
	cmd    *exec.Cmd
	secret = randomHex(8)
)

func exeName() string {
	if runtime.GOOS == "windows" {
		return "mihomo.exe"
	}
	return "mihomo"
}

func vpnDir() string       { return filepath.Join(config.Get().DataDir, "mihomo") }
func binPath() string      { return filepath.Join(vpnDir(), exeName()) }
func configPath() string   { return filepath.Join(vpnDir(), "config.yaml") }
func settingsPath() string { return filepath.Join(vpnDir(), "vpn.json") }
func ctlBase() string      { return fmt.Sprintf("http://127.0.0.1:%d", ctlPort) }
func proxyURL() string     { return fmt.Sprintf("http://127.0.0.1:%d", mixedPort) }

func ensureDir() error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	return os.MkdirAll(vpnDir(), 0o755)
}

func loadSettings() settings {
	var s settings
	if raw, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func saveSettings(s settings) {
	_ = ensureDir()
	raw, _ := json.MarshalIndent(s, "", "  ")
	_ = os.WriteFile(settingsPath(), raw, 0o644)
}

// ResolveBinary 解析内核路径：打包内置(resources) > 运行时下载副本。
func ResolveBinary() string {
	candidates := []string{
		filepath.Join(config.Get().ProjectRoot, "resources", exeName()),
		binPath(),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// IsBinaryInstalled 内核是否可用。
func IsBinaryInstalled() bool { return ResolveBinary() != "" }

// DownloadBinary 下载 Mihomo 内核（自动检测平台，国内自动尝试镜像）。
func DownloadBinary(log func(string)) error {
	logf := func(m string) {
		if log != nil {
			log(m)
		}
	}
	if err := ensureDir(); err != nil {
		return err
	}
	isWin := runtime.GOOS == "windows"

	assetURL := os.Getenv("MIHOMO_URL")
	if assetURL == "" {
		logf("查询 Mihomo 最新版本…")
		assets := fetchLatestAssets()
		var pat *regexp.Regexp
		if isWin {
			pat = regexp.MustCompile(`windows-amd64-compatible-v[\d.]+\.zip$`)
		} else {
			pat = regexp.MustCompile(`linux-amd64-compatible-v[\d.]+\.gz$`)
		}
		for _, a := range assets {
			if pat.MatchString(a.Name) {
				assetURL = a.URL
				break
			}
		}
		if assetURL == "" {
			return errors.New("未找到对应平台的 Mihomo 资源，请设置 MIHOMO_URL 手动指定")
		}
	}

	mirrors := []string{assetURL, "https://ghproxy.net/" + assetURL, "https://mirror.ghproxy.com/" + assetURL}
	archiveName := "mihomo.gz"
	if isWin {
		archiveName = "mihomo.zip"
	}
	archivePath := filepath.Join(vpnDir(), archiveName)
	ok := false
	for _, u := range mirrors {
		logf("下载内核: " + trunc(u, 60))
		if downloadFile(u, archivePath) == nil {
			ok = true
			break
		}
	}
	if !ok {
		return errors.New("Mihomo 内核下载失败（网络问题），可设 MIHOMO_URL 指定直链或手动放置 " + exeName())
	}

	logf("解压内核…")
	if isWin {
		if err := run("tar", "-xf", archivePath, "-C", vpnDir()); err != nil {
			return err
		}
	} else {
		if err := gunzipToFile(archivePath, binPath()); err != nil {
			return err
		}
		_ = os.Chmod(binPath(), 0o755)
	}
	if _, err := os.Stat(binPath()); err != nil {
		return errors.New("解压后未找到 " + exeName())
	}
	logf("内核就绪")
	return nil
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func fetchLatestAssets() []ghAsset {
	apis := []string{
		"https://api.github.com/repos/MetaCubeX/mihomo/releases/latest",
		"https://gh.api.99988866.xyz/https://api.github.com/repos/MetaCubeX/mihomo/releases/latest",
	}
	for _, api := range apis {
		req, _ := http.NewRequest(http.MethodGet, api, nil)
		req.Header.Set("user-agent", "cursor-proxy-studio")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode == 200 {
			var data struct {
				Assets []ghAsset `json:"assets"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&data)
			resp.Body.Close()
			return data.Assets
		}
		resp.Body.Close()
	}
	return nil
}

func buildConfig(subURL string, mode Mode) string {
	groupExtra := ""
	if mode == ModeLoadBalance {
		groupExtra = "    strategy: round-robin\n"
	}
	sub := strings.ReplaceAll(subURL, `"`, "")
	return fmt.Sprintf(`mixed-port: %d
allow-lan: false
mode: rule
log-level: warning
external-controller: 127.0.0.1:%d
secret: "%s"
unified-delay: true
proxy-providers:
  airport:
    type: http
    url: "%s"
    interval: 3600
    path: ./providers/airport.yaml
    health-check:
      enable: true
      url: https://www.gstatic.com/generate_204
      interval: 120
proxy-groups:
  - name: PROXY
    type: %s
    use: [airport]
    url: https://www.gstatic.com/generate_204
    interval: 120
    tolerance: 80
%srules:
  - MATCH,PROXY
`, mixedPort, ctlPort, secret, sub, mode, groupExtra)
}

func ctl(path, method string, body any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, ctlBase()+path, reader)
	req.Header.Set("authorization", "Bearer "+secret)
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ctl %s HTTP %d", path, resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}

func waitReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := ctl("/version", http.MethodGet, nil); err == nil {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

// IsRunning 内核是否在运行。
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return cmd != nil && cmd.Process != nil
}

// Start 启动 VPN：写配置、拉起 mihomo、把全局出口指向本地端口。
func Start(subURL string, mode Mode, log func(string)) error {
	if subURL == "" {
		return errors.New("请先填写机场订阅地址")
	}
	if !IsBinaryInstalled() {
		if err := DownloadBinary(log); err != nil {
			return err
		}
	}
	exe := ResolveBinary()
	if exe == "" {
		return errors.New("未找到 Mihomo 内核")
	}
	_ = Stop()
	if err := ensureDir(); err != nil {
		return err
	}
	useMode := mode
	if useMode == "" {
		if m := loadSettings().Mode; m != "" {
			useMode = m
		} else {
			useMode = ModeURLTest
		}
	}
	if err := os.WriteFile(configPath(), []byte(buildConfig(subURL, useMode)), 0o644); err != nil {
		return err
	}
	if log != nil {
		log("启动内核…")
	}
	c := exec.Command(exe, "-d", vpnDir(), "-f", configPath())
	if err := c.Start(); err != nil {
		return err
	}
	mu.Lock()
	cmd = c
	mu.Unlock()
	go func() {
		_ = c.Wait()
		mu.Lock()
		cmd = nil
		mu.Unlock()
		httpx.SetGlobalProxyOverride("")
	}()

	if !waitReady(8 * time.Second) {
		_ = Stop()
		return errors.New("Mihomo 启动失败（配置或订阅无效）")
	}
	httpx.SetGlobalProxyOverride(proxyURL())
	saveSettings(settings{SubURL: subURL, Enabled: true, Mode: useMode})
	if log != nil {
		log("VPN 已启用，上游流量走机场节点")
	}
	return nil
}

// Stop 停止 VPN。
func Stop() error {
	httpx.SetGlobalProxyOverride("")
	mu.Lock()
	c := cmd
	cmd = nil
	mu.Unlock()
	if c != nil && c.Process != nil {
		_ = c.Process.Kill()
	}
	s := loadSettings()
	if s.Enabled {
		s.Enabled = false
		saveSettings(s)
	}
	return nil
}

// Node 一个节点。
type Node struct {
	Name  string `json:"name"`
	Delay int    `json:"delay"`
}

// Status VPN 状态。
type Status struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	SubURL    string `json:"subUrl"`
	ProxyURL  string `json:"proxyUrl"`
	Mode      Mode   `json:"mode"`
	Current   string `json:"current,omitempty"`
	Nodes     []Node `json:"nodes"`
}

var excludedNodes = map[string]bool{"DIRECT": true, "REJECT": true, "PROXY": true, "PASS": true, "COMPATIBLE": true}

// GetStatus 返回 VPN 状态与节点延迟。
func GetStatus() Status {
	s := loadSettings()
	mode := s.Mode
	if mode == "" {
		mode = ModeURLTest
	}
	base := Status{
		Installed: IsBinaryInstalled(),
		Running:   IsRunning(),
		SubURL:    s.SubURL,
		ProxyURL:  proxyURL(),
		Mode:      mode,
		Nodes:     []Node{},
	}
	if !base.Running {
		return base
	}
	data, err := ctl("/proxies", http.MethodGet, nil)
	if err != nil {
		return base
	}
	proxies, _ := data["proxies"].(map[string]any)
	group, _ := proxies["PROXY"].(map[string]any)
	if group == nil {
		return base
	}
	if now, ok := group["now"].(string); ok {
		base.Current = now
	}
	all, _ := group["all"].([]any)
	for _, n := range all {
		name, _ := n.(string)
		if name == "" || excludedNodes[name] {
			continue
		}
		delay := 0
		if p, ok := proxies[name].(map[string]any); ok {
			if hist, ok := p["history"].([]any); ok && len(hist) > 0 {
				if last, ok := hist[len(hist)-1].(map[string]any); ok {
					if d, ok := last["delay"].(float64); ok {
						delay = int(d)
					}
				}
			}
		}
		base.Nodes = append(base.Nodes, Node{Name: name, Delay: delay})
	}
	return base
}

// TestDelays 触发一次全组测速。
func TestDelays() {
	if !IsRunning() {
		return
	}
	_, _ = ctl("/group/PROXY/delay?url="+urlEncode("https://www.gstatic.com/generate_204")+"&timeout=3000", http.MethodGet, nil)
}

// SwitchNode 手动切换到指定节点。
func SwitchNode(name string) error {
	if !IsRunning() {
		return errors.New("VPN 未运行")
	}
	_, err := ctl("/proxies/PROXY", http.MethodPut, map[string]string{"name": name})
	return err
}

// SetSubURL 设置订阅地址。
func SetSubURL(u string) {
	s := loadSettings()
	s.SubURL = strings.TrimSpace(u)
	saveSettings(s)
}

// SetMode 设置分组策略。
func SetMode(m Mode) {
	s := loadSettings()
	s.Mode = m
	saveSettings(s)
}

// RestoreIfEnabled 进程启动时若上次启用过则自动恢复。
func RestoreIfEnabled() {
	s := loadSettings()
	if s.Enabled && s.SubURL != "" {
		_ = Start(s.SubURL, s.Mode, nil)
	}
}

// ---- 小工具 ----

func randomHex(n int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, n*2)
	now := time.Now().UnixNano()
	for i := range b {
		b[i] = hexChars[(now>>(uint(i)%16))&0xf]
	}
	return string(b)
}

func downloadFile(u, dest string) error {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("user-agent", "cursor-proxy-studio")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func gunzipToFile(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, gr)
	return err
}

func run(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func urlEncode(s string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F", "?", "%3F", "&", "%26", "=", "%3D").Replace(s)
}
