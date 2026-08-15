package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
)

// queryTable 实现 `codeintel query table <表名>`（表级数据流聚合）：
// 列出表的所有列虚拟节点（Q97 持久化映射），每列标注写入方函数与行号。
func queryTable(acts *action.Actions, table string, opts outputOpts) int {
	cols, err := acts.Table(table)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		return printTableJSON(cols)
	}
	if len(cols) == 0 {
		fmt.Printf("表 %s：无列虚拟节点（Q97 持久化映射仅覆盖字符串 SQL 写路径）\n", table)
		return 0
	}
	fmt.Printf("表 %s（%d 列，Q97 持久化映射）:\n", table, len(cols))
	for _, c := range cols {
		fmt.Printf("  %s [%s] :%d\n", c.Name, c.Access, c.LineStart)
		if len(c.Writers) == 0 {
			fmt.Println("    写入: （无 summary_io 边）")
			continue
		}
		for _, w := range c.Writers {
			fmt.Printf("    写入: %s %s :%d\n", w.FuncName, w.FuncID, w.Line)
		}
	}
	return 0
}

func printTableJSON(cols []*domain.TableColumn) int {
	data, err := json.MarshalIndent(cols, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
