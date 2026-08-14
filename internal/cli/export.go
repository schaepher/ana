package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"go.uber.org/zap"
)

// cmdExport 实现 `codeintel export [--out analysis.json]`（S4，field_trace.md §2）：
// 从 function_field_summary 生成双层索引 JSON（字段 → 产生者/消费者）。
func cmdExport(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdExport")
	defer logger.Debug("exit cmdExport")
	outPath := ""
	repoPath := "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--out" && i+1 < len(args):
			outPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--out="):
			outPath = strings.TrimPrefix(a, "--out=")
		case a == "--repo" && i+1 < len(args):
			repoPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = strings.TrimPrefix(a, "--repo=")
		}
	}

	abs, _, err := resolveRepo(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	acts := action.New(sqlite.NewRepo(db))

	index, err := acts.ExportIndex()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	data := struct {
		Fields map[string]*action.ExportField `json:"fields"`
	}{Fields: index}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if outPath == "" {
		fmt.Println(string(out))
		return 0
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("已导出 %d 个字段的索引到 %s\n", len(index), outPath)
	return 0
}
