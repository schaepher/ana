package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
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
	repo := sqlite.NewRepo(db)

	// 双层索引：field_path → {producers, consumers}
	index := map[string]*exportField{}
	rows, err := repo.Query(`SELECT function_id, access_kind, field_path, instance_path, line_start, code_snippet
		FROM function_field_summary ORDER BY field_path, access_kind`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer rows.Close()
	for rows.Next() {
		var (
			fid, access, fieldPath, instance, snippet string
			line                                        int
		)
		if err := rows.Scan(&fid, &access, &fieldPath, &instance, &line, &snippet); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		ef := index[fieldPath]
		if ef == nil {
			ef = &exportField{Producers: []exportEntry{}, Consumers: []exportEntry{}}
			index[fieldPath] = ef
		}
		entry := exportEntry{
			Function: fid,
			Line:     line,
			Instance: instance,
			Code:     snippet,
		}
		switch access {
		case domain.SummaryDirectRead:
			ef.Consumers = append(ef.Consumers, entry)
		default: // direct_write / indirect_write 均为产生者
			entry.Access = access
			ef.Producers = append(ef.Producers, entry)
		}
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	data := exportJSON{Fields: index}
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

type exportJSON struct {
	Fields map[string]*exportField `json:"fields"`
}

type exportField struct {
	Producers []exportEntry `json:"producers"`
	Consumers []exportEntry `json:"consumers"`
}

type exportEntry struct {
	Function string `json:"function"`
	Access   string `json:"access,omitempty"` // producers 的写类型（direct/indirect）
	Line     int    `json:"line"`
	Instance string `json:"instance"`
	Code     string `json:"code"`
}
