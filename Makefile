BINARY := cursor-proxy
WEBUI_DIR := webui
DIST := internal/webui/dist

.PHONY: all build ui ui-deps dev run clean fmt vet

## 构建完整产物（管理界面 + 单一二进制）
all: build

## 安装前端依赖（首次或依赖变更后执行）
ui-deps:
	cd $(WEBUI_DIR) && npm install

## 构建管理界面，产物直接落到 internal/webui/dist 供 go:embed 打包
ui:
	cd $(WEBUI_DIR) && npm run build

## 先构建界面再编译二进制（内嵌 BPE 词表，token 计数精确）
build: ui
	go build -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/server
	@echo "构建完成 -> ./$(BINARY)"

## 轻量构建：不内嵌分词器词表，token 改用启发式估算，二进制小约 11MB
build-lite: ui
	go build -tags notokenizer -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/server
	@echo "构建完成(轻量) -> ./$(BINARY)"

## 只编译后端（沿用上次的界面产物）
build-go:
	go build -o $(BINARY) ./cmd/server

## 本地开发：Go 服务 + Vite 热更新（另开终端跑 make dev-ui）
run:
	go run ./cmd/server

## 前端开发服务器，API 自动代理到 127.0.0.1:3100
dev-ui:
	cd $(WEBUI_DIR) && npm run dev

fmt:
	gofmt -w cmd internal

vet:
	go vet ./...

clean:
	rm -rf $(BINARY) $(DIST)
