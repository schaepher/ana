package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/schaepher/codeintel/internal/action"
)

// queryRelations 实现 `codeintel query relations <表名>`：表间关联分析——
// 本表列的值沿数据流链流入其他表列（A.x 读出 → B.y 过滤/写入，
// 代码层推断，无外键依赖）。
func queryRelations(acts *action.Actions, table string, opts outputOpts) int {
	rels, err := acts.Relations(table)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		data, err := json.MarshalIndent(rels, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	if len(rels) == 0 {
		fmt.Printf("表 %s：无关联表（数据流链上未命中其他表的列）\n", table)
		return 0
	}
	fmt.Printf("表 %s 关联（数据流链推断，%d 条）:\n", table, len(rels))
	for _, r := range rels {
		fmt.Printf("  %s.%s → %s.%s  [%d 跳]\n", r.FromTable, r.FromCol, r.ToTable, r.ToCol, r.Hops)
	}
	return 0
}
