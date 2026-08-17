//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

func TestCLIFullFlowPart2(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found (install: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
	}
	dir := fixtureRepo(t)

	// 前置：init 构建索引（Part1 的段 1 重跑——Part2 独立可跑）
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d, want 0", code)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)
	llmID := "symbol:go:example.com/app/svc:newLLM"
	var code int
	var out string
	saveID := "symbol:go:example.com/app/svc:saveService"
	nameNode := fieldAccessID(t, repo, saveID, "s.Name", "read")
	if nameNode == "" {
		t.Fatalf("saveService s.Name read node missing")
	}

	// 8. value-trace 穿层：从 newLLM 的 m.cfg.APIKey 读节点出发，反向
	//    链应穿过嵌套字段层与函数边界：
	//    newLLM.m ← argument ← runNested.m ← returns ← NewManager
	llmReadID := fieldAccessID(t, repo, llmID, "m.cfg.APIKey", "read")
	if llmReadID == "" {
		t.Fatalf("newLLM m.cfg.APIKey read node missing")
	}
	code, out = runCLIOut(t, "query", "value-trace", llmReadID, "--repo", dir)
	if code != 0 {
		t.Errorf("query value-trace exit = %d", code)
	}
	for _, want := range []string{"argument", "returns", "runNested", "NewManager", "m.cfg.APIKey"} {
		if !strings.Contains(out, want) {
			t.Errorf("value-trace 输出缺 %q（追溯链应穿层跨函数），output=%q", want, out)
		}
	}

	// 9. 条件标注（Q92）：newLLM 的 m.cfg.APIKey 读在 if 分支内——
	//    trace 输出带 [条件: ...]
	llmCondID := fieldAccessID(t, repo, llmID, "m.cfg.APIKey", "read")
	if llmCondID == "" {
		t.Fatalf("newLLM m.cfg.APIKey read node missing")
	}
	code, out = runCLIOut(t, "query", "trace-backward", "example.com/app/svc.Config.APIKey",
		"--func", llmID, "--repo", dir)
	if code != 0 {
		t.Errorf("trace-backward 条件 exit = %d", code)
	}
	if !strings.Contains(out, "[条件:") {
		t.Errorf("trace 输出应含条件标注 [条件:...]，output=%q", out[:min(len(out), 200)])
	}

	// 10. 跨层摘要（Q100）：saveService 字段主链（nameNode 复用第 8 节）
	code, out = runCLIOut(t, "query", "summary", nameNode, "--repo", dir)
	if code != 0 {
		t.Errorf("query summary exit = %d", code)
	}
	for _, want := range []string{"[entry]", "s.Name"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary 输出缺 %q，output=%q", want, out[:min(len(out), 200)])
		}
	}

	// 11. 全局溯源（Q98）：DefaultService.Name 读 → 反向可达 var.DefaultService
	gfName := fieldAccessID(t, repo, "symbol:go:example.com/app/svc:defaultServiceName", "DefaultService.Name", "read")
	if gfName == "" {
		t.Fatalf("defaultServiceName read node missing")
	}
	code, out = runCLIOut(t, "query", "value-trace", gfName, "--repo", dir)
	if code != 0 {
		t.Errorf("value-trace 全局 exit = %d", code)
	}
	if !strings.Contains(out, "DefaultService") {
		t.Errorf("value-trace 应显示全局节点 DefaultService（溯源链），output=%q", out[:min(len(out), 200)])
	}

	// 12. lifecycle 导出（Q99）：export graph --type lifecycle
	code, out = runCLIOut(t, "export", "graph", "--type", "lifecycle", "--target", nameNode, "--repo", dir)
	if code != 0 {
		t.Errorf("export graph lifecycle exit = %d", code)
	}
	if !strings.Contains(out, "flowchart") {
		t.Errorf("lifecycle 应输出 flowchart，output=%q", out[:min(len(out), 200)])
	}

	// 13. symbol 接口候选展示（Q95）：svc.Handler 详情含候选实现
	code, out = runCLIOut(t, "query", "symbol", "symbol:go:example.com/app/svc:Handler", "--repo", dir)
	if code != 0 {
		t.Errorf("symbol Handler exit = %d", code)
	}
	if !strings.Contains(out, "候选实现") {
		t.Errorf("symbol Handler 应展示候选实现，output=%q", out[:min(len(out), 200)])
	}

	// 14. trace-forward 跨函数（问题①）：从 run 出发（run 内无 Cfg.Key
	//     直接访问）应经 argument 进入 callee fillParam 的实际写入
	code, out = runCLIOut(t, "query", "trace-forward", "example.com/app/svc.Cfg.Key",
		"--func", "symbol:go:example.com/app/svc:run", "--repo", dir)
	if code != 0 {
		t.Errorf("trace-forward 跨函数 exit = %d", code)
	}
	if !strings.Contains(out, "c.Key") {
		t.Errorf("trace-forward 应从 run 经 argument 进入 fillParam 的 c.Key 写入，output=%q", out[:min(len(out), 200)])
	}

	// 15. summary 写锚点下游（③）：从 s.Name 写节点出发应含使用链
	//     （同字段读节点 → 返回消费）
	handleWrite := fieldAccessID(t, repo, "symbol:go:example.com/app/svc:(Service).Handle", "s.Name", "write")
	if handleWrite == "" {
		t.Fatalf("Handle s.Name write node missing")
	}
	code, out = runCLIOut(t, "query", "summary", handleWrite, "--repo", dir)
	if code != 0 {
		t.Errorf("summary 写锚点 exit = %d", code)
	}
	if !strings.Contains(out, "consume") {
		t.Errorf("summary 写锚点应含下游使用链（consume 读节点），output=%q", out[:min(len(out), 400)])
	}

	// 16. clean 删除索引（Q177：默认保留 .codeintel/cache 包级分析缓存）
	if code := runCLI(t, "clean", "--repo", dir, "--force"); code != 0 {
		t.Fatalf("clean exit = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, ".codeintel", "codeintel.db")); !os.IsNotExist(err) {
		t.Error("codeintel.db should be removed after clean")
	}
	if _, err := os.Stat(filepath.Join(dir, ".codeintel", "cache")); err != nil {
		t.Error(".codeintel/cache 应保留（pkg hash 自校验）")
	}
}
