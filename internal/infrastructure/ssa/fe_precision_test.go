package ssa

import (
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

// 以下 SelfContained 系列从 integration/ 迁移（2026-08-17）：fixture 自建、
// 断言全为 SSA/sqlite 产物——本质是单元测试，脱离 scip/CLI 管道用
// indexFixtureRepo 落库后直接 repo 断言，随 make test 覆盖。

// vtHas 检查 value-trace 行中是否存在 full_path 以 suffix 结尾且
// access 匹配的行（文本断言 ".ID [写]" 的 rows 等价）。
func vtHas(rows []*domain.TraceRow, suffix, access string) bool {
	for _, r := range rows {
		if r.Access == access && strings.HasSuffix(r.FullPath, suffix) {
			return true
		}
	}
	return false
}

// TestFieldPrecisionSelfContained：⑥ 字段精度——从 src.ID 读节点出发，
// 拷贝链应连到 dst.ID 写入；对象锚点显示值分叉读 + 消费写点。
func TestFieldPrecisionSelfContained(t *testing.T) {
	repo := indexFixtureRepo(t, map[string]string{
		"go.mod": "module example.com/mtest\n\ngo 1.21\n",
		"main.go": `package field

type Src struct {
	ID   string
	Name string
}

type Dst struct {
	ID   string
	Name string
}

func copyAndSave(src *Src) *Dst {
	d := &Dst{}
	d.ID = src.ID
	d.Name = src.Name
	return d
}

func main() {}
`,
	})
	funcID := "symbol:go:example.com/mtest:copyAndSave"

	srcID := fieldAccessID(t, repo, funcID, "src.ID", "read")
	if srcID == "" {
		t.Fatal("src.ID.read 节点缺失")
	}

	rows, err := repo.GetValueTrace(domain.CanonicalID(srcID), 8, 0, false)
	if err != nil {
		t.Fatalf("value-trace: %v", err)
	}
	if !vtHas(rows, ".Dst.ID", "write") {
		t.Errorf("拷贝链应连到 dst.ID 写入，rows=%+v", rows)
	}

	// src 参数 ssa_value 节点（对象锚点）
	var srcVal string
	r2, err := repo.Query(`SELECT id FROM nodes WHERE kind='ssa_value'
		AND json_extract(properties, '$.func_id') = ? AND name = 'src'`, funcID)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Next() {
		_ = r2.Scan(&srcVal)
	}
	r2.Close()
	if srcVal == "" {
		t.Fatal("src 参数 ssa_value 节点缺失")
	}

	rows, err = repo.GetValueTrace(domain.CanonicalID(srcVal), 8, 0, false)
	if err != nil {
		t.Fatalf("value-trace src: %v", err)
	}
	if !vtHas(rows, ".Src.ID", "read") || !vtHas(rows, ".Src.Name", "read") {
		t.Errorf("对象锚点应显示值分叉读，rows=%+v", rows)
	}
	if !vtHas(rows, ".Dst.ID", "write") || !vtHas(rows, ".Dst.Name", "write") {
		t.Errorf("对象锚点应显示值消费写点，rows=%+v", rows)
	}
}
