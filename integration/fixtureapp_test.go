//go:build integration

package integration

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// copyDir 递归复制目录（fixtureapp 含嵌套 go.mod 独立模块）。
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, e := range entries {
		s, d := filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())
		if e.IsDir() {
			copyDir(t, s, d)
			continue
		}
		in, err := os.Open(s)
		if err != nil {
			t.Fatalf("open %s: %v", s, err)
		}
		out, err := os.Create(d)
		if err != nil {
			in.Close()
			t.Fatalf("create %s: %v", d, err)
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			t.Fatalf("copy %s: %v", s, err)
		}
		in.Close()
		out.Close()
	}
}

// TestFixtureAppRealForms：固定真实形态代码库（integration/fixtureapp，
// 模拟真实仓库）——CLI 全管道验证三种真实调用形态：
//  1. XORM 具体类型 *xorm.Session 链式（Table→Where/And/In/Or→
//     Find/Get/Update/Insert/Delete）：filter/read/write 节点 + xorm 类型
//  2. GORM 具体类型 *gorm.DB 链式（Table→Where→Find/Create/Updates）
//  3. 接口动态派发（AccountRepo 接口 + 实现 + MakeInterface 注册）：
//     argument 候选边
//
// 适配器改动后跑本测试防形态回归（AGENTS.md 验证形态矩阵）。
func TestFixtureAppRealForms(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	copyDir(t, "fixtureapp", filepath.Join(dir, "fixtureapp"))
	repoDir := filepath.Join(dir, "fixtureapp")
	if code := runCLI(t, "init", "--repo", repoDir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	db, err := sqlite.Open(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := sqlite.NewRepo(db)

	// 1. XORM 链式节点（settlement 表）：filter/read/write + type_string=xorm
	rows, err := repo.Query(`SELECT name, json_extract(properties, '$.type_string'),
		json_extract(properties, '$.access_kind') FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'
		AND name LIKE 'settlement.%'`)
	if err != nil {
		t.Fatal(err)
	}
	type nodeT struct{ name, ts, access string }
	var xormNodes []nodeT
	for rows.Next() {
		var n nodeT
		if err := rows.Scan(&n.name, &n.ts, &n.access); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		xormNodes = append(xormNodes, n)
	}
	rows.Close()
	// 同名多节点（同列 filter+read 并存）——按 access 分组统计存在性
	byAccess := map[string]map[string]bool{} // access → name 集合
	for _, n := range xormNodes {
		if n.ts != "xorm" {
			t.Errorf("settlement 节点 type_string = %q, want xorm（%s）", n.ts, n.name)
		}
		if byAccess[n.access] == nil {
			byAccess[n.access] = map[string]bool{}
		}
		byAccess[n.access][n.name] = true
	}
	for _, col := range []string{"settlement.order_id", "settlement.amount", "settlement.status"} {
		if !byAccess["filter"][col] {
			t.Errorf("链式条件应产 filter %s（Where/And/In/Or），现有 filter 节点: %v", col, byAccess["filter"])
		}
	}
	if len(byAccess["read"]) == 0 {
		t.Error("Find/Get 应产 read 节点（settlement 字段展开）")
	}
	if len(byAccess["write"]) == 0 {
		t.Error("Update/Insert/Delete 应产 write 节点")
	}

	// 2. GORM 链式节点（goods 表）：type_string=gorm
	var goodsNode bool
	grows, err := repo.Query(`SELECT COUNT(*) FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.is_external') = 'true'
		AND name LIKE 'goods.%' AND json_extract(properties, '$.type_string') = 'gorm'`)
	if err != nil {
		t.Fatal(err)
	}
	if grows.Next() {
		var cnt int
		if err := grows.Scan(&cnt); err != nil {
			grows.Close()
			t.Fatal(err)
		}
		goodsNode = cnt > 0
	}
	grows.Close()
	if !goodsNode {
		t.Error("GORM 链式（Table→Where→Find/Create）应产 goods 表节点")
	}

	// 3. 接口动态派发：argument 候选边（AccountRepo 候选实现 accountRepoImpl）
	arows, err := repo.Query(`SELECT metadata FROM edges
		WHERE kind = 'argument' AND json_extract(metadata, '$.candidate_origin') = 'register'
		LIMIT 1`)
	if err != nil {
		t.Fatal(err)
	}
	dispatchOK := false
	if arows.Next() {
		var meta string
		if err := arows.Scan(&meta); err == nil && meta != "" {
			dispatchOK = true
		}
	}
	arows.Close()
	if !dispatchOK {
		t.Error("接口动态派发应产带候选元数据的 argument 边")
	}
}
