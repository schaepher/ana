# codeintel 构建配置
# version 通过 -ldflags 注入编译时的 git commit hash。

BINARY     := codeintel
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION_PKG := github.com/schaepher/codeintel/internal/cli
LDFLAGS    := -X '$(VERSION_PKG).gitCommit=$(GIT_COMMIT)'

.PHONY: build install test vet clean version

## build: 编译二进制（注入 commit hash）
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/codeintel

## install: 安装到 GOBIN（默认 GOPATH/bin），同样注入 commit hash
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/codeintel

## test: 运行全部测试
test:
	go test ./...

## vet: 静态检查
vet:
	go vet ./...

## version: 显示将注入的 commit hash
version:
	@echo $(GIT_COMMIT)

## clean: 删除编译产物
clean:
	rm -f $(BINARY)
