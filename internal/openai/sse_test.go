package openai

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"cursor-proxy/internal/cursor"
)

func TestSSEGraceIncludesRequestPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	wire := newSSEResponse(ctx, cancel, w)
	startSent := make(chan struct{})
	_, err := wire.awaitOpen(time.Now().Add(-streamStartGrace), func() {
		wire.write("data: start\n\n")
		close(startSent)
	}, func() (*OpenedStream, *cursor.UpstreamError) {
		select {
		case <-startSent:
			return nil, cursor.NewUpstreamError(503, "test upstream failure")
		case <-ctx.Done():
			return nil, clientDisconnected()
		}
	})
	if err == nil || err.Status != 503 || w.Body.String() != "data: start\n\n" {
		t.Fatalf("start should be sent without a second grace period, got body %q and error %v", w.Body.String(), err)
	}
}
