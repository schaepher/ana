package ssa

import (
	"testing"
)

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
