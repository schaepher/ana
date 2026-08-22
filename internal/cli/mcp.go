package cli

// Q243 MCP：`codeintel mcp [--repo <path>]`——stdio MCP server
// （github.com/modelcontextprotocol/go-sdk，协议由 SDK 实现），把 query
// 能力暴露为 tools（Agent 直接调用；输出复用 --json 契约，
// docs/json-contract.md）。工具 = 参数结构体（json tag 即 inputSchema
// 字段名）+ handler（闭包捕获 Actions）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// 工具参数结构体（json tag = inputSchema 字段名，snake_case）。
type symbolParams struct {
	ID string `json:"id"` // 符号名或 canonical ID
}
type fieldsParams struct {
	Func string `json:"func"` // 函数名或 canonical ID
}
type graphParams struct {
	Symbol string `json:"symbol"` // 符号名或 canonical ID
	Depth  int    `json:"depth"`  // 深度（默认 1/3）
}
type traceParams struct {
	Field    string `json:"field"`     // 类型限定字段路径（如 example.com/m.T.A）
	Func     string `json:"func"`      // 函数名
	Dir      string `json:"dir"`       // backward（产生点）或 forward（使用点）
	MaxDepth int    `json:"max_depth"` // 深度（默认 8）
}
type valueTraceParams struct {
	Node     string  `json:"node"`      // 节点 ID（如 symbol:go:...:main#t.A.read@5）
	MaxDepth int     `json:"max_depth"` // 深度（默认 8）
	MinConf  float64 `json:"min_conf"`  // 候选边置信度剪枝（默认 1.0）
}
type contextParams struct {
	Node string `json:"node"` // 符号/字段路径
}
type tableParams struct {
	Name string `json:"name"` // 表名
}
type relationsParams struct {
	Table      string `json:"table"`       // 表名
	Type       string `json:"type"`        // 关联类型（逗号分隔，默认 query,write）
	MaxHops    int    `json:"max_hops"`    // 跳数上限
	MaxResults int    `json:"max_results"` // 条数上限
}
type tablePathParams struct {
	From    string `json:"from"`     // 起始表名
	To      string `json:"to"`       // 目标表名
	MaxHops int    `json:"max_hops"` // 跳数上限（默认 6）
}
type summaryParams struct {
	Node string `json:"node"` // 锚点（符号/字段路径）
}
type moduleCallsParams struct {
	Module string `json:"module"` // module 名过滤（可选）
}

// toolErr 错误结果（isError=true，文本为错误信息）。
func toolErr(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

// toolJSON 成功结果：契约 JSON 文本（docs/json-contract.md）。
func toolJSON(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolErr("marshal result: " + err.Error())
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}

// registerMCPTools 注册全部工具（handler 闭包捕获 acts）。
func registerMCPTools(server *mcp.Server, acts *action.Actions) {
	mcp.AddTool(server, &mcp.Tool{Name: "symbol", Description: "符号详情（调用者/被调用者/动态派发候选）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args symbolParams) (*mcp.CallToolResult, any, error) {
			d, err := acts.SymbolDetail(args.ID)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			out := map[string]any{
				"id":        string(d.Node.ID),
				"name":      d.Node.Name,
				"kind":      string(d.Node.Kind),
				"file":      d.Node.FilePath,
				"line":      d.Node.LineStart,
				"signature": d.Node.Signature(),
				"doc":       d.Node.DocComment(),
				"callers":   factIDs(d.Callers, "source"),
				"callees":   factIDs(d.Callees, "target"),
			}
			return toolJSON(out), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "fields", Description: "函数字段读写摘要（direct_read/write + indirect_write）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args fieldsParams) (*mcp.CallToolResult, any, error) {
			n, rows, err := acts.FunctionFields(args.Func)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(map[string]any{"func": n.Name, "rows": rows}), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "callers", Description: "调用者（depth 默认 1）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args graphParams) (*mcp.CallToolResult, any, error) {
			return graphTool(acts, args, "callers"), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "callees", Description: "被调用者（depth 默认 1）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args graphParams) (*mcp.CallToolResult, any, error) {
			return graphTool(acts, args, "callees"), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "impact", Description: "影响分析（depth 默认 3）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args graphParams) (*mcp.CallToolResult, any, error) {
			n, err := acts.ResolveSymbol(args.Symbol)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			depth := args.Depth
			if depth <= 0 {
				depth = 3
			}
			nodes, err := acts.Impact(n.ID, depth)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(map[string]any{"target": string(n.ID), "nodes": nodeBriefs(nodes)}), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "trace", Description: "字段追溯（dir=backward/forward，max_depth 默认 8）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args traceParams) (*mcp.CallToolResult, any, error) {
			depth := args.MaxDepth
			if depth <= 0 {
				depth = 8
			}
			_, rows, err := acts.Trace(action.TraceParams{
				Field: args.Field, Func: args.Func, Forward: args.Dir == "forward", MaxDepth: depth,
			})
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(map[string]any{"steps": rows}), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "value_trace", Description: "数据值全链（跨函数；node 为节点 ID）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args valueTraceParams) (*mcp.CallToolResult, any, error) {
			id, err := acts.ResolveAnchor(args.Node)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			depth := args.MaxDepth
			if depth <= 0 {
				depth = 8
			}
			minConf := args.MinConf
			if minConf <= 0 {
				minConf = 1.0
			}
			rows, err := acts.ValueTrace(id, depth, minConf, false)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(map[string]any{"flows": rows}), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "context", Description: "跨层聚合上下文（symbol+callers/callees+fields+chain+traces）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args contextParams) (*mcp.CallToolResult, any, error) {
			c, err := acts.Context(args.Node)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(c), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "table", Description: "表级数据流聚合（列 + 写入方/读取方）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args tableParams) (*mcp.CallToolResult, any, error) {
			cols, err := acts.Table(args.Name)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(cols), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "relations", Description: "表间关联（type 过滤 query/write/read/fk）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args relationsParams) (*mcp.CallToolResult, any, error) {
			rels, err := acts.Relations(args.Table, "")
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			if args.Type != "" {
				rels = filterRelTypes(rels, args.Type)
			}
			if args.MaxHops > 0 {
				rels = filterRels(rels, func(r *domain.TableRelation) bool { return r.Hops <= args.MaxHops })
			}
			if args.MaxResults > 0 && len(rels) > args.MaxResults {
				rels = rels[:args.MaxResults]
			}
			return toolJSON(rels), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "table_path", Description: "表 A → 表 B 数据通路（跨 mapping 表）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args tablePathParams) (*mcp.CallToolResult, any, error) {
			from, _, err := acts.ResolveTableName(args.From)
			if err != nil {
				return toolErr("起始表: " + err.Error()), nil, nil
			}
			to, _, err := acts.ResolveTableName(args.To)
			if err != nil {
				return toolErr("目标表: " + err.Error()), nil, nil
			}
			maxHops := args.MaxHops
			if maxHops <= 0 {
				maxHops = 6
			}
			res, err := acts.TablePath(from, to, maxHops)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(res), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "summary", Description: "跨层生命周期摘要（entry/compute/write/consume 主链）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args summaryParams) (*mcp.CallToolResult, any, error) {
			id, err := acts.ResolveAnchor(args.Node)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			steps, err := acts.SummaryChain(id)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(steps), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "module_calls", Description: "模块间调用（gRPC/HTTP）"},
		func(ctx context.Context, req *mcp.CallToolRequest, args moduleCallsParams) (*mcp.CallToolResult, any, error) {
			calls, err := acts.ModuleCalls(args.Module)
			if err != nil {
				return toolErr(err.Error()), nil, nil
			}
			return toolJSON(map[string]any{"calls": calls}), nil, nil
		})
}

// graphTool callers/callees 共用 handler。
func graphTool(acts *action.Actions, args graphParams, which string) *mcp.CallToolResult {
	n, err := acts.ResolveSymbol(args.Symbol)
	if err != nil {
		return toolErr(err.Error())
	}
	depth := args.Depth
	if depth <= 0 {
		depth = 1
	}
	var facts []*domain.Fact
	if which == "callers" {
		facts, err = acts.Callers(n.ID, depth)
	} else {
		facts, err = acts.Callees(n.ID, depth)
	}
	if err != nil {
		return toolErr(err.Error())
	}
	return toolJSON(map[string]any{"target": string(n.ID), "rows": facts})
}

// filterRelTypes 按类型过滤（空=不过滤；逗号分隔）。
func filterRelTypes(rels []*domain.TableRelation, types string) []*domain.TableRelation {
	want := map[string]bool{}
	for _, t := range strings.Split(types, ",") {
		want[strings.TrimSpace(t)] = true
	}
	return filterRels(rels, func(r *domain.TableRelation) bool { return want[r.Type] })
}

// filterRels 谓词过滤（保留满足的）。
func filterRels(rels []*domain.TableRelation, keep func(*domain.TableRelation) bool) []*domain.TableRelation {
	out := make([]*domain.TableRelation, 0, len(rels))
	for _, r := range rels {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

// mcpServer 组装 MCP server（工具注册）。
func mcpServer(acts *action.Actions) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "codeintel", Version: "0.1.0"}, nil)
	registerMCPTools(server, acts)
	return server
}

// cmdMCP 实现 `codeintel mcp`（stdio MCP server）。
func cmdMCP(args []string) int {
	logger := zap.L()
	logger.Debug("enter cmdMCP")
	defer logger.Debug("exit cmdMCP")
	repoPath := "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--repo" && i+1 < len(args):
			repoPath = ResolveRepoRef(args[i+1])
			i++
		case strings.HasPrefix(a, "--repo="):
			repoPath = ResolveRepoRef(strings.TrimPrefix(a, "--repo="))
		case a == "--help" || a == "-h":
			fmt.Println("用法: codeintel mcp [--repo <path>]\n  stdio MCP server（tools/list + tools/call 暴露 query 能力）")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "error: 未知参数 %q\n", a)
			return 2
		}
	}
	abs, _, err := resolveRepo(repoPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		if repoPath == "." {
			printRepoHint()
		}
		return 1
	}
	if err := logging.ToFile(abs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: 日志切换失败: %v\n", err)
	}
	db, err := sqlite.Open(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()
	acts := action.New(sqlite.NewRepo(db))
	session, err := mcpServer(acts).Connect(context.Background(), &mcp.StdioTransport{}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		return 1
	}
	session.Wait()
	return 0
}
