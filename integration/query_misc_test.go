//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestUserSummaryYAMLSelfContained：S2 集成固化——field-summary.yaml 相对
// 字段路径（"user.ID"）须补全为类型限定路径（pkg.T.ID），而非错误拼成
// pkg.T.user.ID。
func TestUserSummaryYAMLSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/um\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "external/external.go"), `package external

func Wrap(v any) {}
`)
	writeFile(t, filepath.Join(dir, "main.go"), `package um

import "example.com/um/external"

type T struct {
	ID   int
	Name string
}

func f(t *T) {
	external.Wrap(t)
}
`)
	writeFile(t, filepath.Join(dir, "field-summary.yaml"), `summaries:
  - func: "example.com/um/external.Wrap"
    reads: ["user.ID", "user.Name"]
    param_index: 0
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT DISTINCT json_extract(properties, '$.full_path') FROM nodes
		WHERE kind = 'field_access' AND json_extract(properties, '$.full_path') LIKE '%T.ID'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	foundID := false
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			t.Fatal(err)
		}
		if fp == "example.com/um.T.ID" {
			foundID = true
		}
		if fp == "example.com/um.T.user.ID" {
			t.Errorf("相对路径补全错误: %s", fp)
		}
	}
	if !foundID {
		t.Error("用户摘要相对路径未补全为 example.com/um.T.ID")
	}
}

// TestAnonymousStructFieldLineSelfContained：B3 集成固化——匿名 struct
// （range 元素）字段访问须带行号（fieldInfo 匿名分支曾提前 return，
// line_start=0 导致 CLI 无定位）。
func TestAnonymousStructFieldLineSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/anon\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package anon

type Conf struct {
	Items []struct {
		Key string
	}
}

func f(c Conf) {
	for _, s := range c.Items {
		_ = s.Key
	}
}

func main() {}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var line int
	if err := db.QueryRow(`SELECT line_start FROM nodes
		WHERE kind = 'field_access'
		  AND json_extract(properties, '$.func_id') LIKE '%:f'
		  AND json_extract(properties, '$.instance_path') = 's.Key'`).Scan(&line); err != nil {
		t.Fatalf("s.Key 节点不存在: %v", err)
	}
	if line <= 0 {
		t.Errorf("匿名 struct 字段访问 line_start = %d, want > 0", line)
	}
}

// TestRelationsAllSelfContained：Q160 集成固化——query relations --all
// 一次返回全库键关联（原生 SQL 键关联链：member.id 读出值 → account
// 按 member_id 过滤），无需逐表查询；export relations 同源输出。
func TestRelationsAllSelfContained(t *testing.T) {
	if !scipGoAvailable() {
		t.Skip("scip-go not found")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/rels\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main

import "database/sql"

type Member struct{ ID int }
type Account struct{ ID int }

// 同一会员 id 读出值分别过滤 member 与 account——键关联链：
// member.id（读源）→ account.member_id（WHERE 过滤，2 跳）
func Load(db *sql.DB, m *Member) error {
	db.QueryRow("SELECT id FROM member WHERE id = ?", m.ID)
	db.QueryRow("SELECT id FROM account WHERE member_id = ?", m.ID)
	return nil
}

func main() {
	Load(&sql.DB{}, &Member{ID: 1})
}
`)
	if code := runCLI(t, "init", "--repo", dir); code != 0 {
		t.Fatalf("init exit = %d", code)
	}

	code, out := runCLIOut(t, "query", "relations", "--all", "--repo", dir, "--json")
	if code != 0 {
		t.Fatalf("relations --all exit = %d", code)
	}
	var rels []struct {
		FromTable string `json:"from_table"`
		FromCol   string `json:"from_col"`
		ToTable   string `json:"to_table"`
		ToCol     string `json:"to_col"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal([]byte(out), &rels); err != nil {
		t.Fatalf("relations --all JSON: %v\n%s", err, out)
	}
	found := false
	for _, r := range rels {
		if r.Type == "query" && r.FromTable == "member" && r.FromCol == "id" &&
			r.ToTable == "account" && r.ToCol == "member_id" {
			found = true
		}
	}
	if !found {
		t.Errorf("--all 未包含 member.id → account.member_id query 键关联:\n%s", out)
	}

	outPath := filepath.Join(t.TempDir(), "rels.json")
	if code := runCLI(t, "export", "relations", "--repo", dir, "--out", outPath); code != 0 {
		t.Fatalf("export relations exit = %d", code)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var exp struct {
		Relations []struct {
			FromTable string `json:"from_table"`
			ToTable   string `json:"to_table"`
			Type      string `json:"type"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(data, &exp); err != nil {
		t.Fatalf("export relations JSON: %v", err)
	}
	if len(exp.Relations) == 0 {
		t.Error("export relations 空文件")
	}
}
