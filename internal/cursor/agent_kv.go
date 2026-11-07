package cursor

import (
	"bytes"
	"encoding/binary"
	"errors"

	"cursor-proxy/internal/proto"
)

const (
	maxAgentKVBytes   = 64 << 20
	maxAgentKVEntries = 1024
)

// agentKV 只在当前 Run 内保留上游写入的 blob，供同一轮后续读取。
type agentKV struct {
	blobs map[string][]byte
	bytes int
}

// reply 消费 KvServerMessage，生成一帧 AgentClientMessage.kv_client_message。
func (kv *agentKV) reply(payload []byte) ([]byte, error) {
	request := proto.Decode(payload)
	get, set := request[2], request[3]
	if len(get)+len(set) == 0 {
		return nil, nil
	}
	if len(get)+len(set) != 1 {
		return nil, errors.New("Cursor KV 请求包含多个读写操作")
	}

	var id uint64
	if fields := request[1]; len(fields) > 0 {
		if len(fields) != 1 || fields[0].WireType != 0 || fields[0].Varint > 1<<32-1 {
			return nil, errors.New("Cursor KV 请求的 ID 无效")
		}
		id = fields[0].Varint
	}

	operation := get
	resultField := 2 // GetBlobResult
	if len(set) == 1 {
		operation = set
		resultField = 3 // SetBlobResult
	}
	if operation[0].WireType != 2 {
		return nil, errors.New("Cursor KV 请求的参数无效")
	}
	args := proto.Decode(operation[0].Bytes)
	keys := args[1]
	if len(keys) != 1 || keys[0].WireType != 2 || len(keys[0].Bytes) == 0 {
		return nil, errors.New("Cursor KV 请求的 blob ID 无效")
	}
	key := keys[0].Bytes

	var result []byte
	if resultField == 2 {
		if value, ok := kv.blobs[string(key)]; ok {
			result = proto.NewWriter().Bytes(1, value).Finish()
		}
	} else {
		values := args[2]
		if len(values) > 1 || (len(values) == 1 && values[0].WireType != 2) {
			return nil, errors.New("Cursor KV 写入的数据无效")
		}
		if err := kv.store(key, proto.FirstBytes(args, 2)); err != nil {
			return nil, err
		}
	}

	reply := proto.NewWriter().Bytes(resultField, result).Finish()
	if id != 0 {
		// 保持 uint32 ID 的线格式，不经由 int 转换。
		reply = append(binary.AppendUvarint([]byte{0x08}, id), reply...)
	}
	return connectFrame(proto.NewWriter().Bytes(3, reply).Finish()), nil
}

func (kv *agentKV) store(blobID, data []byte) error {
	if len(blobID) > maxAgentKVBytes || len(data) > maxAgentKVBytes {
		return errors.New("Cursor KV 数据超过单轮容量限制")
	}
	key := string(blobID)
	old, exists := kv.blobs[key]
	size := kv.bytes - len(old) + len(data)
	if !exists {
		size += len(key)
		if len(kv.blobs) >= maxAgentKVEntries {
			return errors.New("Cursor KV 条目超过单轮数量限制")
		}
	}
	if size > maxAgentKVBytes {
		return errors.New("Cursor KV 数据超过单轮容量限制")
	}
	if kv.blobs == nil {
		kv.blobs = make(map[string][]byte)
	}
	kv.blobs[key] = bytes.Clone(data)
	kv.bytes = size
	return nil
}
