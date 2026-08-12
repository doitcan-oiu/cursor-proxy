// Package proto 实现 Cursor 上游所需的最小 protobuf wire-format 读写。
//
// 只覆盖 varint(wire type 0) 与 length-delimited(wire type 2) 两种编码，
// 足以构造/解析 Cursor 的 Chat 与 Agent 报文。
package proto

import "encoding/binary"

// Writer 增量拼装 protobuf 报文。
type Writer struct {
	buf []byte
}

// NewWriter 创建一个空写入器。
func NewWriter() *Writer { return &Writer{} }

func (w *Writer) varint(v uint64) {
	w.buf = binary.AppendUvarint(w.buf, v)
}

// Int32 写入一个 varint 字段。
func (w *Writer) Int32(field int, value int) *Writer {
	w.varint(uint64(field)<<3 | 0)
	w.varint(uint64(value))
	return w
}

// Bytes 写入一个 length-delimited 字段。
func (w *Writer) Bytes(field int, value []byte) *Writer {
	w.varint(uint64(field)<<3 | 2)
	w.varint(uint64(len(value)))
	w.buf = append(w.buf, value...)
	return w
}

// Str 写入一个字符串字段。
func (w *Writer) Str(field int, value string) *Writer {
	return w.Bytes(field, []byte(value))
}

// Finish 返回已拼装的字节。
func (w *Writer) Finish() []byte { return w.buf }

// Field 是一次解码得到的字段值。
type Field struct {
	WireType int
	Varint   uint64
	Bytes    []byte
}

// Decode 把 protobuf 报文解析为 field -> 多个值。
func Decode(data []byte) map[int][]Field {
	fields := make(map[int][]Field)
	pos := 0
	readVarint := func() (uint64, bool) {
		v, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return 0, false
		}
		pos += n
		return v, true
	}
	for pos < len(data) {
		tag, ok := readVarint()
		if !ok {
			break
		}
		field := int(tag >> 3)
		wire := int(tag & 0x07)
		var f Field
		f.WireType = wire
		switch wire {
		case 0:
			v, ok := readVarint()
			if !ok {
				return fields
			}
			f.Varint = v
		case 2:
			l, ok := readVarint()
			if !ok || pos+int(l) > len(data) {
				return fields
			}
			f.Bytes = data[pos : pos+int(l)]
			pos += int(l)
		case 1:
			if pos+8 > len(data) {
				return fields
			}
			f.Bytes = data[pos : pos+8]
			pos += 8
		case 5:
			if pos+4 > len(data) {
				return fields
			}
			f.Bytes = data[pos : pos+4]
			pos += 4
		default:
			return fields
		}
		fields[field] = append(fields[field], f)
	}
	return fields
}

// FirstBytes 取某字段首个 length-delimited 值。
func FirstBytes(fields map[int][]Field, field int) []byte {
	for _, f := range fields[field] {
		if f.WireType == 2 {
			return f.Bytes
		}
	}
	return nil
}

// FirstString 取某字段首个字符串值。
func FirstString(fields map[int][]Field, field int) string {
	b := FirstBytes(fields, field)
	if b == nil {
		return ""
	}
	return string(b)
}
