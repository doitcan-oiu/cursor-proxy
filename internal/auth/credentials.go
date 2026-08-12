package auth

import "strings"

// Credential 一行账号凭证。
type Credential struct {
	Email          string
	EmailPassword  string
	CursorPassword string
	Raw            string
}

// ParseCredentialLine 解析单行凭证：邮箱----邮箱密码----Cursor密码，兼容 | 与空白分隔。
func ParseCredentialLine(line string) (Credential, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return Credential{}, false
	}
	var parts []string
	switch {
	case strings.Contains(trimmed, "----"):
		parts = strings.Split(trimmed, "----")
	case strings.Contains(trimmed, "|"):
		parts = strings.Split(trimmed, "|")
	default:
		parts = strings.Fields(trimmed)
	}
	cleaned := parts[:0]
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 || !strings.Contains(cleaned[0], "@") {
		return Credential{}, false
	}
	c := Credential{Email: cleaned[0], Raw: trimmed}
	if len(cleaned) > 1 {
		c.EmailPassword = cleaned[1]
	}
	if len(cleaned) > 2 {
		c.CursorPassword = cleaned[2]
	}
	return c, true
}

// ParseCredentialsText 解析多行凭证文本。
func ParseCredentialsText(text string) []Credential {
	var out []Credential
	for _, line := range strings.Split(text, "\n") {
		if c, ok := ParseCredentialLine(line); ok {
			out = append(out, c)
		}
	}
	return out
}
