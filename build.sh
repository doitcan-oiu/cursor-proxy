#!/usr/bin/env bash
# 构建管理界面 + 单一二进制。等价于 `make build`，给没装 make 的环境用。
set -euo pipefail

cd "$(dirname "$0")"

BINARY="${BINARY:-cursor-proxy}"

if [ ! -d webui/node_modules ]; then
  echo "==> 安装前端依赖"
  (cd webui && npm install)
fi

echo "==> 构建管理界面"
(cd webui && npm run build)

echo "==> 编译二进制"
go build -trimpath -ldflags "-s -w" -o "$BINARY" ./cmd/server

echo "==> 完成: ./$BINARY"
