package cli

// Q243 MCP：`codeintel mcp` stdio server（go-sdk）——tools/list +
// tools/call 暴露 query 能力（Agent 直接调用，输出复用 --json 契约
// docs/json-contract.md）。测试用内存 transport + SDK client 直连。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/schaepher/codeintel/internal/action"
	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// mcpDial 起 server（内存 transport）+ client 连接，返回 client session。
func mcpDial(t *testing.T, dir string) *mcp.ClientSession {
	t.Helper()
	db, err := sqlite.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	acts := action.New(sqlite.NewRepo(db))

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcpServer(acts)
	srvSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { srvSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cliSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cliSession.Close() })
	return cliSession
}

// mcpCallTool 调工具并取 text 内容（isError 断言由调用方做）。
func mcpCallTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return text, res.IsError
}

// TestMCPDial：连接握手（initialize）成功 + 服务端信息。
func TestMCPDial(t *testing.T) {
	cs := mcpDial(t, seedRepo(t))
	if cs == nil {
		t.Fatal("client session 为空")
	}
}

// TestMCPToolsList：tools/list 返回工具注册表（含 inputSchema）。
func TestMCPToolsList(t *testing.T) {
	cs := mcpDial(t, seedRepo(t))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		if tool.InputSchema == nil {
			t.Errorf("工具 %s 缺 inputSchema", tool.Name)
		}
	}
	for _, want := range []string{"symbol", "fields", "callers", "callees", "impact", "context", "table", "relations", "table_path", "value_trace"} {
		if !names[want] {
			t.Errorf("tools/list 缺 %s（现有: %v）", want, names)
		}
	}
}

// TestMCPToolSymbol：tools/call symbol——content text 是契约 JSON。
func TestMCPToolSymbol(t *testing.T) {
	cs := mcpDial(t, seedRepo(t))
	text, isErr := mcpCallTool(t, cs, "symbol", map[string]any{"id": "main"})
	if isErr {
		t.Fatalf("symbol 调用报错: %s", text)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("content 应为 JSON: %v\n%s", err, text)
	}
	if m["name"] != "main" || m["id"] != "symbol:go:example.com/m:main" {
		t.Errorf("symbol 结果 = %v", m)
	}
}

// TestMCPToolFields：fields 输出契约字段（function_id/access_kind）。
func TestMCPToolFields(t *testing.T) {
	cs := mcpDial(t, seedFieldTrace(t))
	text, isErr := mcpCallTool(t, cs, "fields", map[string]any{"func": "main"})
	if isErr {
		t.Fatalf("fields 调用报错: %s", text)
	}
	if !strings.Contains(text, `"access_kind"`) || !strings.Contains(text, `"function_id"`) {
		t.Errorf("fields 应输出契约字段（access_kind/function_id）:\n%s", text)
	}
}

// TestMCPUnknownTool：tools/call 未知工具 → SDK 报错。
func TestMCPUnknownTool(t *testing.T) {
	cs := mcpDial(t, seedRepo(t))
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "nope_tool"}); err == nil {
		t.Error("未知工具应报错")
	}
}

// TestMCPToolError：工具执行错误 → isError（如符号不存在）。
func TestMCPToolError(t *testing.T) {
	cs := mcpDial(t, seedRepo(t))
	text, isErr := mcpCallTool(t, cs, "symbol", map[string]any{"id": "nope_nope"})
	if !isErr {
		t.Errorf("符号不存在应 isError，text=%s", text)
	}
	if !strings.Contains(text, "不存在") {
		t.Errorf("错误信息应含原因: %s", text)
	}
}
