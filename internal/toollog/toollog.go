// Package toollog 记录上游下发的、本代理尚未识别的内置工具调用。
//
// 上游随时可能新增工具。未识别的工具会导致这一轮任务做不了，
// 把它们的字段号与参数结构留档，就能直接据此补上映射，
// 不必再让使用者去抓帧。
package toollog

import (
	"encoding/base64"
	"sync"
	"time"
)

// Entry 一条未识别工具的记录。
type Entry struct {
	ID   int64 `json:"id"`
	Time int64 `json:"time"`
	// Field 是参数容器里的字段号，补映射时按它区分工具。
	Field int `json:"field"`
	// Name 是上游给这个工具的规范名（取自 Cursor 客户端自带的描述）。
	// 光有字段号的话，补映射时还得再去查一次描述文件。
	Name string `json:"name,omitempty"`
	// Model 是触发这次调用的模型，不同模型可用的工具集可能不同。
	Model  string `json:"model"`
	CallID string `json:"callId,omitempty"`
	// Hint 是从参数里提取的第一个可读字符串，用于快速判断工具用途。
	Hint string `json:"hint,omitempty"`
	// Structure 是解码出的 protobuf 字段结构，补映射时照着写即可。
	Structure string `json:"structure,omitempty"`
	// RawBase64 是原始参数字节，供精确复核。
	RawBase64 string `json:"rawBase64,omitempty"`
}

const maxEntries = 200

var (
	mu     sync.Mutex
	buffer []Entry
	seq    int64 = 1
	// seen 按「字段号」去重计数：同一个未知工具可能被反复调用，
	// 没必要把缓冲塞满，但要能看出它出现了多少次。
	counts = map[int]int{}
)

// Record 记录一次未识别的工具调用。
func Record(field int, name, model, callID, hint, structure string, raw []byte) {
	mu.Lock()
	defer mu.Unlock()

	counts[field]++
	// 同一字段号只保留最近一条完整记录，避免刷屏
	for i := range buffer {
		if buffer[i].Field == field {
			buffer[i].Time = time.Now().UnixMilli()
			buffer[i].Model = model
			buffer[i].Name = name
			buffer[i].Hint = hint
			buffer[i].Structure = structure
			buffer[i].RawBase64 = base64.StdEncoding.EncodeToString(raw)
			return
		}
	}

	buffer = append(buffer, Entry{
		ID:        seq,
		Time:      time.Now().UnixMilli(),
		Field:     field,
		Model:     model,
		CallID:    callID,
		Name:      name,
		Hint:      hint,
		Structure: structure,
		RawBase64: base64.StdEncoding.EncodeToString(raw),
	})
	seq++
	if len(buffer) > maxEntries {
		buffer = buffer[len(buffer)-maxEntries:]
	}
}

// View 是带出现次数的对外视图。
type View struct {
	Entry
	Count int `json:"count"`
}

// List 返回全部记录，按最近出现时间倒序。
func List() []View {
	mu.Lock()
	defer mu.Unlock()
	out := make([]View, 0, len(buffer))
	for _, e := range buffer {
		out = append(out, View{Entry: e, Count: counts[e.Field]})
	}
	// 简单插入排序：条目很少，按时间倒序
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Time > out[j-1].Time; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Count 返回未识别工具的种类数，供界面显示角标。
func Count() int {
	mu.Lock()
	defer mu.Unlock()
	return len(buffer)
}

// Clear 清空记录。
func Clear() {
	mu.Lock()
	defer mu.Unlock()
	buffer = nil
	counts = map[int]int{}
}
