package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/schaepher/codeintel/internal/action"
)

// queryContext Q235-5：query context <节点>——一次调用拿全链上下文
// （symbol + callers/callees + fields + chain + traces + dispatch），
// 聚合查询编排（action.Context），默认 JSON 输出。MCP 地基：transport
// 解耦，未来 MCP 暴露直接复用 action.Context。
func queryContext(acts *action.Actions, input string, opts outputOpts) int {
	ctx, err := acts.Context(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Println(string(data))
	return 0
}
