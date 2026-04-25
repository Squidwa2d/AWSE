# Makefile — aswe CLI 构建 & 安装
#
# 常用:
#   make build        产出 ./bin/aswe (本地用)
#   make install      安装到 $GOPATH/bin, 之后终端可直接敲 `aswe ...`
#   make uninstall    从 $GOPATH/bin 卸载
#   make test         跑全部单元测试
#   make fmt / vet    代码检查
#   make completion SHELL_NAME=zsh   打印 zsh 补全脚本 (> ~/.zsh/_aswe)
#   make clean        清理 ./bin
#
# 版本号通过 ldflags 注入到 cmd/aswe.version:
#   make build VERSION=0.2.0
VERSION   ?= dev
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILDDATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
# ldflags 针对 package main, 直接用 main.<var> 的写法
LDFLAGS    = -s -w \
             -X main.version=$(VERSION) \
             -X main.commit=$(COMMIT) \
             -X main.buildDate=$(BUILDDATE)

BIN_DIR = bin
BIN     = $(BIN_DIR)/aswe

# go install 默认装到 $(go env GOBIN) 或 $(go env GOPATH)/bin
INSTALL_DIR := $(shell go env GOBIN)
ifeq ($(INSTALL_DIR),)
INSTALL_DIR := $(shell go env GOPATH)/bin
endif

.PHONY: all build install uninstall test fmt vet clean completion help run

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/aswe
	@echo "✅ built $(BIN) ($(VERSION) $(COMMIT))"

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/aswe
	@echo "✅ installed to $(INSTALL_DIR)/aswe"
	@echo "   请确保 $(INSTALL_DIR) 在你的 PATH 中"

uninstall:
	@rm -f $(INSTALL_DIR)/aswe && echo "✅ removed $(INSTALL_DIR)/aswe" || true

test:
	go test ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)

# make completion SHELL_NAME=bash|zsh|fish|powershell
SHELL_NAME ?= zsh
completion: build
	@$(BIN) completion $(SHELL_NAME)

run: build
	@$(BIN) $(ARGS)

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}' || true
	@echo "常用目标: build install uninstall test fmt vet clean completion"
