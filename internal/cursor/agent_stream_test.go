package cursor

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cursor-proxy/internal/proto"
)

func agentTestKVGetFrame(id int, blobID string) []byte {
	args := proto.NewWriter().Str(1, blobID)
	kv := proto.NewWriter().Int32(1, id).Bytes(2, args.Finish())
	return frame(0x00, proto.NewWriter().Bytes(4, kv.Finish()).Finish())
}

func readAgentTestFrame(body io.Reader) ([]byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(body, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if header[0] != 0 || length > 1024 {
		return nil, fmt.Errorf("unexpected frame flag %d or length %d", header[0], length)
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(body, payload)
	return payload, err
}

func readAgentTestKVReply(body io.Reader, id uint64, resultField int, want []byte) error {
	payload, err := readAgentTestFrame(body)
	if err != nil {
		return err
	}
	kv := proto.FirstBytes(proto.Decode(payload), 3)
	fields := proto.Decode(kv)
	var gotID uint64
	if ids := fields[1]; len(ids) > 0 && ids[0].WireType == 0 {
		gotID = ids[0].Varint
	}
	result := proto.FirstBytes(fields, resultField)
	if gotID != id || result == nil || !bytes.Equal(result, want) {
		return fmt.Errorf("KV reply %x: want ID %d, result field %d, payload %x", payload, id, resultField, want)
	}
	return nil
}

func TestOpenAgentStreamRepliesToKVBeforeTurnEnded(t *testing.T) {
	const blobID = "blob-id"
	const blobData = "blob data"
	initial := frame(0x00, proto.NewWriter().Bytes(1, nil).Finish())

	// 上游在原请求体收到三条 KV 回复后才发 TurnEnded。提前关闭请求体会让上游读到 EOF。
	upstreamResult := make(chan error, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamResult <- func() error {
			if r.ProtoMajor != 2 || r.Method != http.MethodPost {
				return fmt.Errorf("upstream got %s over %s", r.Method, r.Proto)
			}
			rc := http.NewResponseController(w)
			if err := rc.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			first, err := readAgentTestFrame(r.Body)
			if err != nil || !bytes.Equal(first, initial[5:]) {
				return fmt.Errorf("initial frame = %x, err = %v", first, err)
			}
			w.Header().Set("Content-Type", "application/connect+proto")
			for _, exchange := range []struct {
				request []byte
				id      uint64
				field   int
				result  []byte
			}{
				{kvSetFrame(blobID, blobData), 0, 3, nil},
				{agentTestKVGetFrame(1, blobID), 1, 2, proto.NewWriter().Str(1, blobData).Finish()},
				{agentTestKVGetFrame(2, "missing"), 2, 2, nil},
			} {
				if _, err := w.Write(exchange.request); err != nil {
					return err
				}
				if err := rc.Flush(); err != nil {
					return err
				}
				if err := readAgentTestKVReply(r.Body, exchange.id, exchange.field, exchange.result); err != nil {
					return fmt.Errorf("KV request %d: %w", exchange.id, err)
				}
			}
			response := append(contentFrame("complete"), turnEndedFrame()...)
			response = append(response, frame(0x02, []byte("{}"))...)
			if _, err := w.Write(response); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil {
				return err
			}
			select {
			case <-r.Context().Done():
				return nil
			case <-time.After(2 * time.Second):
				return fmt.Errorf("upstream connection remained open after TurnEnded")
			}
		}()
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()
	client := upstream.Client()
	client.Timeout = 5 * time.Second

	stream, err := openAgentStream(context.Background(), client, upstream.URL, http.Header{}, initial, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var got []StreamEvent
	for event := range stream.Events {
		got = append(got, event)
	}
	if err := <-upstreamResult; err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != EventDelta || got[0].Text != "complete" || got[1].Kind != EventEnd || got[1].Truncated {
		t.Fatalf("events = %+v, want complete text followed by normal end", got)
	}
}
