package ssa

import (
	"testing"
)

// TestBuiltinSummaryMetrics：prometheus 观测函数内置摘要（Q99 观测指标识别）。

// TestBuiltinSummaryGORM：GORM 写操作内置摘要（②：ORM 更新映射字段→列）。

// TestSnakeCase：表名/列名转换——与 GORM 默认命名完全一致（缩写表
// Title 化 + 大小写扫描：SessionID → session_id、SourceURL → source_url、
// SQLiteKnowledgeGraph → sq_lite_knowledge_graph）。
func TestSnakeCase(t *testing.T) {
	cases := map[string]string{
		"UserProfile":            "user_profile",
		"APIKey":                 "api_key",
		"Name":                   "name",
		"HTTPServer":             "http_server",
		"ID":                     "id",
		"SQLiteKnowledgeGraph":   "sq_lite_knowledge_graph",
		"SQLiteKnowledgeGraphID": "sq_lite_knowledge_graph_id",
		"ChatMessage":            "chat_message",
		"SessionID":              "session_id",
		"SourceURL":              "source_url",
		"SessionIDAndURL":        "session_id_and_url",
	}
	for in, want := range cases {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseSQLStmt：⑬ 猎 bug——SQL 语句启发式解析形态矩阵
// （parseSQLStmt 此前无直接单测；曾有切片 panic 历史）。

// TestUserSummaryRelativeFieldPath：S2 回归——field-summary.yaml 相对字段
// 路径（"user.ID" 带实例前缀）须补全为类型限定路径（pkg.T.ID），
// 而非错误拼成 pkg.T.user.ID（此前补全条件含点相对路径全拼）。

// TestWhereColDollar：Q158——$N 占位符（PostgreSQL 风格）的 WHERE 过滤
// 列提取（go2o memberRepo 用 gof Connector 的 "level= $1" 形态）。

// TestInterfaceSQLSummary：Q158——接口摘要 SQL 形态（gof Connector 接口：
// SQL 字符串在 Args[0]，无接收者）。ExecScalar=读（SELECT 列 read + filter）、
// ExecNonQuery=写（SET 列 write + filter）。

// TestXORMSummarySelfContained：Q175——XORM 链式形态（Table().Where()
// .Find()）的表名/字段/查询条件提取。

// TestPkgCacheHit：Q176——同一 repo 两次 Index：第一次写包级缓存，
// 第二次命中缓存，产物一致（nodes/facts 数相同）。

// TestPkgCacheInvalidation：Q176——包源码变更后缓存失效重新分析。
