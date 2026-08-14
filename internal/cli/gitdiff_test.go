package cli

import (
	"reflect"
	"testing"
)

// TestParseGitDiff：--since 的 diff 解析——新增文件/修改文件/新增行号集合/
// rename 按修改处理（field_trace.md §16.5）。
func TestParseGitDiff(t *testing.T) {
	out := `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..e69de29
--- /dev/null
+++ b/new.go
@@ -0,0 +1,3 @@
+package m
+
+func NewThing() {}
diff --git a/mod.go b/mod.go
index 1111111..2222222 100644
--- a/mod.go
+++ b/mod.go
@@ -10,4 +10,5 @@ func Old() {
 	_ = 1
+	_ = 2
+	_ = 3
 }
diff --git a/ren.go b/ren2.go
similarity index 90%
rename from ren.go
rename to ren2.go
index 3333333..4444444 100644
--- a/ren.go
+++ b/ren2.go
@@ -1,1 +1,2 @@
 func R() {}
+func R2() {}
diff --git a/del.go b/del.go
deleted file mode 100644
index 5555555..0000000
--- a/del.go
+++ /dev/null
@@ -1,2 +0,0 @@
-func Gone() {}
-func Gone2() {}
`
	info := parseGitDiff(out)
	// 新增文件：new.go（文件内全部函数 [new]）
	if !info.NewFiles["new.go"] {
		t.Errorf("new.go 应标记为新增文件: %v", info.NewFiles)
	}
	// 修改文件：mod.go 新增行 12,13（@@ -10,4 +10,5 @@：+ 侧 10..14 五行
	// = @@ 内联首行上下文 10 + 上下文 11 + 新增 12,13 + 上下文 14）
	wantMod := map[int]bool{12: true, 13: true}
	if !reflect.DeepEqual(info.AddedLines["mod.go"], wantMod) {
		t.Errorf("mod.go added lines = %v, want %v", info.AddedLines["mod.go"], wantMod)
	}
	// rename 按修改处理：ren2.go 新增行 2（@@ -1,1 +1,2 @@：行1 上下文 + 行2 新增）
	if info.NewFiles["ren2.go"] {
		t.Errorf("rename 文件不应标为新增: %v", info.NewFiles)
	}
	if !info.AddedLines["ren2.go"][2] {
		t.Errorf("ren2.go added line 2 缺失: %v", info.AddedLines["ren2.go"])
	}
	// 删除文件跳过
	if _, ok := info.AddedLines["del.go"]; ok {
		t.Errorf("删除文件不应出现在结果: %v", info.AddedLines["del.go"])
	}
}

// TestParseGitDiffMultiHunk：多 hunk 累加。
func TestParseGitDiffMultiHunk(t *testing.T) {
	out := `diff --git a/m.go b/m.go
--- a/m.go
+++ b/m.go
@@ -1,2 +1,2 @@
 func A() {}
+_ = 1
@@ -30,3 +31,4 @@
 	_ = 2
+	_ = 3
+	_ = 4
 `
	info := parseGitDiff(out)
	// hunk1 +1,2：行1 上下文 + 行2 新增；hunk2 +31,4：31,32 上下文 + 32?,33 新增
	// 实际：hunk2 内 行31(_ = 2 上下文) 32(+_ = 3) 33(+_ = 4) → 新增 32,33
	want := map[int]bool{2: true, 32: true, 33: true}
	if !reflect.DeepEqual(info.AddedLines["m.go"], want) {
		t.Errorf("multi-hunk added lines = %v, want %v", info.AddedLines["m.go"], want)
	}
}

// TestParseGitDiffEmpty：无 diff 输出。
func TestParseGitDiffEmpty(t *testing.T) {
	info := parseGitDiff("")
	if len(info.NewFiles) != 0 || len(info.AddedLines) != 0 {
		t.Errorf("空 diff = %+v", info)
	}
}
