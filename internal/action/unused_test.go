package action

import (
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestActionsUnused：两档报告 + --since 标注（field_trace.md §16.2/16.4）。
func TestActionsUnused(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	f := func(id, name string, line int) *domain.CodeEntity {
		return &domain.CodeEntity{
			ID: domain.CanonicalID(id), Kind: domain.KindFunction, Name: name,
			FilePath: "main.go", LineStart: line, LineEnd: line + 2,
		}
	}
	nodes := []*domain.CodeEntity{
		f("symbol:go:example.com/m:dead", "dead", 5),
		f("symbol:go:example.com/m:hook", "hook", 10),
		f("symbol:go:example.com/m:run2", "run2", 15),
		f("symbol:go:example.com/m:solo", "solo", 20),
		f("symbol:go:example.com/m:main", "main", 25),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m:run2",
			Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:main", TargetID: "symbol:go:example.com/m:hook",
			Kind: domain.FactPassesTo, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatal(err)
	}
	a := New(r)

	// 1. 无 --since：全量报告
	rep, err := a.Unused(nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*domain.UnusedFunc{}
	for _, u := range rep.Unused {
		byName[u.Name] = u
	}
	// dead：无调用（无引用）→ 两档都命中
	if u := byName["dead"]; u == nil || u.Called || u.Referenced {
		t.Errorf("dead = %+v, want called=false referenced=false", u)
	}
	// hook：无调用但有引用
	if u := byName["hook"]; u == nil || u.Called || !u.Referenced {
		t.Errorf("hook = %+v, want called=false referenced=true", u)
	}
	// run2 被调用 → 不在未调用报告
	if _, ok := byName["run2"]; ok {
		t.Errorf("run2 不应在未调用报告中: %+v", byName["run2"])
	}
	// main 永不报告
	if _, ok := byName["main"]; ok {
		t.Error("main 不应报告")
	}
	// 孤立链：dead 与 solo 各成单节点链（hook 有引用不算）
	got := map[string]bool{}
	for _, ch := range rep.Chains {
		s := ""
		for _, u := range ch {
			s += u.Name + ","
		}
		got[s] = true
	}
	if !got["dead,"] || !got["solo,"] {
		t.Errorf("孤立链 = %v, want dead 与 solo 单节点链", got)
	}
	if got["hook,"] {
		t.Error("hook 有引用不应为孤立链")
	}

	// 2. --since：dead 声明行 5 ∈ 新增行 → [new]；solo 声明行 20 不在新增行
	//    但行号区间 20..22 ∩ {21} → [mod]
	since := &domain.SinceInfo{
		Ref: "HEAD",
		AddedLines: map[string]map[int]bool{
			"main.go": {5: true, 21: true},
		},
	}
	rep2, err := a.Unused(since)
	if err != nil {
		t.Fatal(err)
	}
	byName2 := map[string]*domain.UnusedFunc{}
	for _, u := range rep2.Unused {
		byName2[u.Name] = u
	}
	if u := byName2["dead"]; u == nil || u.SinceMark != "new" {
		t.Errorf("dead since = %+v, want new", u)
	}
	if u := byName2["solo"]; u == nil || u.SinceMark != "mod" {
		t.Errorf("solo since = %+v, want mod", u)
	}
	if u := byName2["hook"]; u != nil {
		t.Errorf("hook 未改动不应出现在 --since 报告: %+v", u)
	}
}

// TestActionsPath：query path 锚点解析与路径（field_trace.md §17.3）。
func TestActionsPath(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r := sqlite.NewRepo(db)

	f := func(id, name string, line int) *domain.CodeEntity {
		return &domain.CodeEntity{
			ID: domain.CanonicalID(id), Kind: domain.KindFunction, Name: name,
			FilePath: "main.go", LineStart: line,
		}
	}
	nodes := []*domain.CodeEntity{
		f("symbol:go:example.com/m:a", "a", 5),
		f("symbol:go:example.com/m:b", "b", 10),
		f("symbol:go:example.com/m:c", "c", 15),
	}
	edges := []*domain.Fact{
		{SourceID: "symbol:go:example.com/m:a", TargetID: "symbol:go:example.com/m:b",
			Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
		{SourceID: "symbol:go:example.com/m:b", TargetID: "symbol:go:example.com/m:c",
			Kind: domain.FactCalls, ToolSource: domain.ToolCodeGraph, Confidence: 0.8},
	}
	if _, err := r.SaveBatchStats(nodes, edges, nil); err != nil {
		t.Fatal(err)
	}
	a := New(r)

	// 名称解析 + calls 边集
	path, err := a.Path("a", "c", 50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 3 || string(path[0].ID) != "symbol:go:example.com/m:a" ||
		string(path[2].ID) != "symbol:go:example.com/m:c" {
		t.Errorf("path = %+v", path)
	}
	// 不可达（数据流边集下无路径）
	path2, err := a.Path("a", "c", 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(path2) != 0 {
		t.Errorf("数据流边集下不可达: %+v", path2)
	}
	// 不存在符号 → 错误
	if _, err := a.Path("nope", "c", 50, true); err == nil {
		t.Error("不存在符号应报错")
	}
}

// TestMarkSince：--since 标注纯函数（new/mod/空 + 新增文件）。
func TestMarkSince(t *testing.T) {
	since := &domain.SinceInfo{
		Ref:      "HEAD",
		NewFiles: map[string]bool{"new.go": true},
		AddedLines: map[string]map[int]bool{
			"main.go": {5: true, 11: true},
		},
	}
	cases := []struct {
		file  string
		start int
		end   int
		want  string
	}{
		{"main.go", 5, 7, "new"},   // 声明行命中新增行
		{"main.go", 10, 12, "mod"}, // 区间命中 11
		{"main.go", 20, 22, ""},    // 未改动
		{"new.go", 1, 1, "new"},    // 新增文件
		{"other.go", 1, 1, ""},     // 未变更文件
	}
	for _, c := range cases {
		if got := MarkSince(c.file, c.start, c.end, since); got != c.want {
			t.Errorf("MarkSince(%s,%d,%d) = %q, want %q", c.file, c.start, c.end, got, c.want)
		}
	}
}
