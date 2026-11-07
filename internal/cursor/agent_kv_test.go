package cursor

import (
	"bytes"
	"encoding/binary"
	"testing"

	"cursor-proxy/internal/proto"
)

func kvSetFrame(key, data string) []byte {
	args := proto.NewWriter().Str(1, key).Str(2, data).Finish()
	request := proto.NewWriter().Bytes(3, args).Finish()
	return connectFrame(proto.NewWriter().Bytes(4, request).Finish())
}

func TestAgentKVReplies(t *testing.T) {
	var kv agentKV
	key := []byte{0, 1, 2, 0xff}
	data := []byte{3, 4, 5}
	set := proto.NewWriter().Bytes(1, key).Bytes(2, data).Finish()
	// Proto3 omits the request ID when it is zero.
	setRequest := proto.NewWriter().Bytes(3, set).Finish()
	setReply, err := kv.reply(setRequest)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 0, 0, 0, 4, 0x1a, 2, 0x1a, 0}; !bytes.Equal(setReply, want) {
		t.Fatalf("zero-ID SetBlobResult = %x, want %x", setReply, want)
	}
	clear(setRequest)

	const id = uint64(1<<32 - 1)
	get := proto.NewWriter().Bytes(1, key).Finish()
	getRequest := binary.AppendUvarint([]byte{0x08}, id)
	getRequest = append(getRequest, proto.NewWriter().Bytes(2, get).Finish()...)
	getReply, err := kv.reply(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	reply := decodeKVReply(t, getReply)
	if ids := reply[1]; len(ids) != 1 || ids[0].Varint != id {
		t.Fatalf("GetBlobResult ID = %v, want %d", ids, id)
	}
	if got := proto.FirstBytes(proto.Decode(proto.FirstBytes(reply, 2)), 1); !bytes.Equal(got, data) {
		t.Fatalf("GetBlobResult data = %x, want %x", got, data)
	}

	var otherRun agentKV
	missingReply, err := otherRun.reply(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	if result := proto.FirstBytes(decodeKVReply(t, missingReply), 2); result == nil || len(result) != 0 {
		t.Fatalf("GetBlobResult for a missing blob = %x, want empty result", result)
	}

	// SetBlobArgs.blob_data is empty when omitted by a proto3 sender.
	if _, err := kv.reply(proto.NewWriter().Bytes(3, get).Finish()); err != nil {
		t.Fatal(err)
	}
	emptyReply, err := kv.reply(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	if got := proto.FirstBytes(proto.Decode(proto.FirstBytes(decodeKVReply(t, emptyReply), 2)), 1); got == nil || len(got) != 0 {
		t.Fatalf("GetBlobResult for an empty stored blob = %x, want present empty bytes", got)
	}
	if kv.bytes != len(key) {
		t.Fatalf("KV size after replacement = %d, want %d", kv.bytes, len(key))
	}
}

func TestAgentKVRejectsMalformedOperations(t *testing.T) {
	var kv agentKV
	validKey := proto.NewWriter().Str(1, "key").Finish()
	for name, request := range map[string][]byte{
		"empty key":    proto.NewWriter().Bytes(2, nil).Finish(),
		"invalid args": proto.NewWriter().Int32(3, 1).Finish(),
		"invalid id":   proto.NewWriter().Str(1, "id").Bytes(2, validKey).Finish(),
		"two ops":      proto.NewWriter().Bytes(2, validKey).Bytes(3, validKey).Finish(),
		"invalid data": proto.NewWriter().Bytes(3, proto.NewWriter().Str(1, "key").Int32(2, 7).Finish()).Finish(),
	} {
		t.Run(name, func(t *testing.T) {
			if response, err := kv.reply(request); response != nil || err == nil {
				t.Fatalf("response = %x, error = %v, want only an error", response, err)
			}
		})
	}
}

func TestAgentKVEntryLimit(t *testing.T) {
	var kv agentKV
	for i := 0; i < maxAgentKVEntries; i++ {
		key := binary.AppendUvarint(nil, uint64(i))
		if err := kv.store(key, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := kv.store([]byte("overflow"), nil); err == nil {
		t.Fatal("new entry beyond the limit was accepted")
	}
	if err := kv.store([]byte{0}, []byte("updated")); err != nil {
		t.Fatalf("updating an existing blob at the limit: %v", err)
	}
}

func decodeKVReply(t *testing.T, frame []byte) map[int][]proto.Field {
	t.Helper()
	if len(frame) < 5 || frame[0] != 0 || int(binary.BigEndian.Uint32(frame[1:5])) != len(frame)-5 {
		t.Fatalf("invalid Connect response frame: %x", frame)
	}
	return proto.Decode(proto.FirstBytes(proto.Decode(frame[5:]), 3))
}
