#!/usr/bin/env bash
# 构建管理界面 + 单一二进制。等价于 `make build`，给没装 make 的环境用。
#
#   ./build.sh          默认构建，内嵌 BPE 词表，token 计数精确（约 19MB）
#   ./build.sh --lite   不含分词器（-tags notokenizer），token 改用启发式估算（约 8MB）
set -euo pipefail

cd "$(dirname "$0")"

BINARY="${BINARY:-cursor-proxy}"
TAGS=""

if [ "${1:-}" = "--lite" ]; then
  TAGS="notokenizer"
  echo "==> 轻量模式：不内嵌分词器词表"
fi

if [ ! -d webui/node_modules ]; then
  echo "==> 安装前端依赖"
  (cd webui && npm install)
fi

echo "==> 构建管理界面"
(cd webui && npm run build)

echo "==> 编译二进制"
go build -tags "$TAGS" -trimpath -ldflags "-s -w" -o "$BINARY" ./cmd/server

echo "==> 完成: ./$BINARY"
