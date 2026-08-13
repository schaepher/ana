# codeintel 构建配置
# version 通过 -ldflags 注入编译时的 git commit hash。

BINARY     := codeintel
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION_PKG := github.com/schaepher/codeintel/internal/cli
LDFLAGS    := -X '$(VERSION_PKG).gitCommit=$(GIT_COMMIT)'

.PHONY: build install test it vet clean version

## build: 编译二进制（注入 commit hash）
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/codeintel

## install: 安装到 GOBIN（默认 GOPATH/bin），同样注入 commit hash
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/codeintel

## test: 运行全部测试（-race 竞态检测 + -count=1 禁用缓存 + 覆盖率汇总）
test:
	go test -race -count=1 -cover ./...

## it: 集成测试（真实仓库 → CLI init/query/clean + HTTP serve 全 API；
##     需要 scip-go 在 PATH 或 GOBIN/GOPATH/bin，缺失时自动跳过）
it:
	go test -count=1 -tags integration ./integration/

## vet: 静态检查
vet:
	go vet ./...

## version: 显示将注入的 commit hash
version:
	@echo $(GIT_COMMIT)

## clean: 删除编译产物
clean:
	rm -f $(BINARY)
