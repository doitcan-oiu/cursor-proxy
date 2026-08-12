package cursor

import (
	"testing"

	"cursor-proxy/internal/proto"
	"cursor-proxy/internal/toollog"
)

func TestUnknownToolIsRecordedToLog(t *testing.T) {
	toollog.Clear()
	args := proto.NewWriter()
	args.Str(1, "some-new-tool-payload")

	var data []byte
	data = append(data, toolCallFrame("toolu_new", 77, args)...)
	data = append(data, conversationFrame(4)...)
	collectTools(t, data)

	list := toollog.List()
	if len(list) != 1 {
		t.Fatalf("应记录 1 条，得到 %d", len(list))
	}
	e := list[0]
	if e.Field != 77 {
		t.Fatalf("字段号 = %d", e.Field)
	}
	if e.Model != "test-model" {
		t.Fatalf("模型 = %q", e.Model)
	}
	if e.Hint != "some-new-tool-payload" {
		t.Fatalf("线索 = %q", e.Hint)
	}
	if e.Structure == "" || e.RawBase64 == "" {
		t.Fatalf("结构与原始字节都应留档: %+v", e)
	}
	toollog.Clear()
}
