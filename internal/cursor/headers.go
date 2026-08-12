package cursor

import (
	"encoding/base64"
	"net/http"
	"time"

	"github.com/google/uuid"

	"cursor-proxy/internal/store"
)

func obfuscateBytes(b []byte) []byte {
	t := byte(165)
	for i := range b {
		b[i] = byte((int(b[i]^t) + (i % 256)) & 255)
		t = b[i]
	}
	return b
}

// generateCursorChecksum 复刻 Cursor 的 x-cursor-checksum：时间戳混淆段 + machineId/macMachineId。
func generateCursorChecksum(id store.AccountIdentity) string {
	timestamp := time.Now().UnixMilli() / 1e6
	byteArray := []byte{
		byte((timestamp >> 40) & 255),
		byte((timestamp >> 32) & 255),
		byte((timestamp >> 24) & 255),
		byte((timestamp >> 16) & 255),
		byte((timestamp >> 8) & 255),
		byte(timestamp & 255),
	}
	encoded := base64.StdEncoding.EncodeToString(obfuscateBytes(byteArray))
	return encoded + id.MachineID + "/" + id.MacMachineID
}

func baseClientHeaders(ctx Context) http.Header {
	requestID := uuid.NewString()
	id := ctx.Identity
	h := http.Header{}
	h.Set("authorization", "Bearer "+ctx.Bearer)
	h.Set("connect-protocol-version", "1")
	h.Set("user-agent", "connect-es/1.6.1")
	h.Set("x-amzn-trace-id", "Root="+requestID)
	h.Set("x-client-key", ctx.ClientKey)
	h.Set("x-cursor-checksum", generateCursorChecksum(id))
	h.Set("x-cursor-client-version", id.ClientVersion)
	h.Set("x-cursor-client-type", "ide")
	h.Set("x-cursor-client-os", id.OS)
	h.Set("x-cursor-client-arch", id.Arch)
	h.Set("x-cursor-client-os-version", id.OSVersion)
	h.Set("x-cursor-client-device-type", "desktop")
	h.Set("x-cursor-config-version", id.ConfigVersion)
	h.Set("x-cursor-timezone", id.Timezone)
	h.Set("x-ghost-mode", "true")
	h.Set("x-new-onboarding-completed", "false")
	h.Set("x-request-id", requestID)
	h.Set("x-session-id", ctx.SessionID)
	h.Set("Host", "api2.cursor.sh")
	return h
}

// buildModelsHeaders AvailableModels（unary, application/proto）请求头。
func buildModelsHeaders(ctx Context) http.Header {
	h := baseClientHeaders(ctx)
	h.Set("accept-encoding", "gzip")
	h.Set("content-type", "application/proto")
	return h
}

// buildChatHeaders StreamUnifiedChatWithTools（server-streaming）请求头。
func buildChatHeaders(ctx Context) http.Header {
	h := baseClientHeaders(ctx)
	h.Set("connect-accept-encoding", "gzip")
	h.Set("connect-content-encoding", "gzip")
	h.Set("content-type", "application/connect+proto")
	return h
}
