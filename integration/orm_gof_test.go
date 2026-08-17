//go:build integration

package integration

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestGofRepositoryInterfaceSelfContained：Q156 集成固化——gof 框架
// fw.Repository[M] 接口摘要（真实外部依赖）：init 前置 go mod tidy
// （模块缓存有 gof，离线可解析），断言表.列虚拟节点生成（表名取实体
// TableName）+ where filter 列。
func TestGofRepositoryInterfaceSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/gofiface\n\ngo 1.21\n\nrequire github.com/ixre/gof v1.17.15\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import (
	"github.com/ixre/gof/ext/fw"
)

type Order struct {
	Id       int
	FinalFee float64
}

func (o *Order) TableName() string { return "mm_order" }

// fw.Repository 接口调用（候选实现 BaseRepository[M] 在外部模块——
// 触发接口摘要）
func saveBill(repo fw.Repository[Order], o *Order) {
	repo.Save(o)
	x := repo.FindBy("id = ? AND final_fee > ?", o.Id, 100)
	_ = x.FinalFee
}

func main() {}
`)
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy 失败（无 gof 缓存）: %v %s", err, string(out))
	}
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Save：对象写节点（表名 TableName → mm_order）
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM nodes
		WHERE kind = 'field_access' AND name = 'mm_order.final_fee'
		  AND json_extract(properties, '$.access_kind') = 'write'
		  AND json_extract(properties, '$.is_external') = 'true'`).Scan(&n); err != nil {
		t.Fatalf("write 节点查询: %v", err)
	}
	if n == 0 {
		t.Error("gof Save 未生成 mm_order.final_fee write 节点（接口摘要未生效）")
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM nodes
		WHERE kind = 'field_access' AND name = 'mm_order.id'
		  AND json_extract(properties, '$.access_kind') = 'filter'
		  AND json_extract(properties, '$.is_external') = 'true'`).Scan(&n); err != nil {
		t.Fatalf("filter 节点查询: %v", err)
	}
	if n == 0 {
		t.Error("gof FindBy 未生成 mm_order.id filter 节点")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM nodes
		WHERE kind = 'field_access' AND name = 'mm_order.final_fee'
		  AND json_extract(properties, '$.access_kind') = 'filter'
		  AND json_extract(properties, '$.is_external') = 'true'`).Scan(&n); err != nil {
		t.Fatalf("filter 节点查询: %v", err)
	}
	if n == 0 {
		t.Error("gof FindBy 未生成 mm_order.final_fee filter 节点（AND 拆分）")
	}
}
