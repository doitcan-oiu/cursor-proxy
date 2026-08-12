package webui

import "time"

// zeroTime 让 ServeContent 跳过 Last-Modified 协商，缓存策略由 Cache-Control 决定。
var zeroTime time.Time
