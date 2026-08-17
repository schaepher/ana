package cli

import (
	"fmt"
	"os"

	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// queryGraph 输出 callers/callees/impact 查询结果。
func queryGraph(acts *action.Actions, sub, input string, depth int, opts outputOpts, since *domain.SinceInfo) int {
	logger := zap.L()
	logger.Debug("enter queryGraph")
	defer logger.Debug("exit queryGraph")
	n, err := acts.ResolveSymbol(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if opts.json {
		switch sub {
		case "callers":
			facts, err := acts.Callers(n.ID, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			encodeJSON(map[string]any{"target": input, "rows": factIDs(facts, "source")})
		case "callees":
			facts, err := acts.Callees(n.ID, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			encodeJSON(map[string]any{"target": input, "rows": factIDs(facts, "target")})
		case "impact":
			nodes, err := acts.Impact(n.ID, depth)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			encodeJSON(map[string]any{"target": input, "nodes": nodeBriefs(nodes)})
		}
		return 0
	}

	switch sub {
	case "callers":
		facts, err := acts.Callers(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		marks := sinceMarks(acts, factEndpointIDs(facts, "source"), since)
		fmt.Printf("调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, action.MinConfidence, len(facts))
		printFacts(facts, "source", 100, marks)
	case "callees":
		facts, err := acts.Callees(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		marks := sinceMarks(acts, factEndpointIDs(facts, "target"), since)
		fmt.Printf("被调用者（深度 %d，置信度 ≥%.2f）: %d 个\n", depth, action.MinConfidence, len(facts))
		printFacts(facts, "target", 100, marks)
	case "impact":
		nodes, err := acts.Impact(n.ID, depth)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("影响范围（深度 %d）: %d 个节点\n", depth, len(nodes))
		printNodes(nodes)
	}
	return 0
}
