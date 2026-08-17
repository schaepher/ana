# codeintel 构建配置
# version 通过 -ldflags 注入编译时的 git commit hash。

BINARY     := codeintel
E2E_REPO   ?= .
GIT_COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION_PKG := github.com/schaepher/codeintel/internal/cli
LDFLAGS    := -X '$(VERSION_PKG).gitCommit=$(GIT_COMMIT)'

.PHONY: build install test it e2e serve vet clean version

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

## bench: 性能基准（构建时间/内存/DB 大小；默认当前目录，-bench-repo 指定仓库）
bench:
	go test -count=1 -tags benchmark ./benchmarks/ -bench-repo "$(BENCH_REPO)" $(BENCH_FLAGS)

## serve: 启动图探索 Web 服务（E2E_REPO 指定仓库，默认当前目录（须已
##        构建索引；前台运行，Ctrl+C 退出；--addr 默认 :8096）
serve:
	go build -o /tmp/codeintel-e2e ./cmd/codeintel
	@/tmp/codeintel-e2e serve --repo $(E2E_REPO) --addr :8096

## e2e: 前端回归（playwright）。serve 指定仓库（E2E_REPO，默认当前目录，
##      须已构建索引）后运行 e2e/field-trace-e2e.mjs 全量断言。
e2e:
	go build -o /tmp/codeintel-e2e ./cmd/codeintel
	@/tmp/codeintel-e2e serve --repo $(E2E_REPO) --addr :8096 >/dev/null 2>&1 & \
	  echo $$! > /tmp/codeintel-e2e.pid
	@sleep 2
	@cd e2e && node field-trace-e2e.mjs; status=$$?; \
	  kill $$(cat /tmp/codeintel-e2e.pid) 2>/dev/null; \
	  rm -f /tmp/codeintel-e2e /tmp/codeintel-e2e.pid; exit $$status

## vet: 静态检查
vet:
	go vet ./...

## version: 显示将注入的 commit hash
version:
	@echo $(GIT_COMMIT)

## clean: 删除编译产物
clean:
	rm -f $(BINARY)
