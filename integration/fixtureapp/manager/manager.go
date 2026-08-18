package manager

import "context"

// Manager 形态与 e2e/field-trace-e2e.mjs 断言对齐（自包含验证仓库，
// 不依赖外部仓库符号形态）：
//   - 方法 (Manager).Run：接收者 m（*Manager）
//   - 参数 ctx / sessionID / userMessage（定义顺序）
//   - 返回 reply / newSessionID / err（定义顺序，string/error 类型）
//   - m.cfg 字段数据流：写（m.cfg = cfg）← 局部变量 cfg（ssa_value）
//     → 读 cfg.APIKey（[读] 标记）——产生链/使用链闭环
//   - if 条件分支（[条件:] 标注）、跨函数调用（(Handler).PageChatSend
//     函数上下文分组）
type Config struct {
	APIKey string
}

type Manager struct {
	cfg Config
}

// Handler 跨函数形态：Run 调用 PageChatSend（if 条件分支内）——
// value-trace 按函数分组；h.prefix 字段读（[读] 标记，跨函数链上）。
type Handler struct {
	prefix string
}

func (h *Handler) PageChatSend(ctx context.Context, msg string) string {
	return h.prefix + msg
}

// Run 字段数据流形态：m.cfg 写（[写] 节点，if 外）→ m.cfg 读（[读]
// 节点，if 条件分支内——[条件:] 标注）→ reply → PageChatSend（跨函数
// 上下文分组）。value-trace 全链锚点 = 画布读节点。
func (m *Manager) Run(ctx context.Context, sessionID string, userMessage string) (reply string, newSessionID string, err error) {
	m.cfg = Config{APIKey: userMessage} // [写] m.cfg
	if userMessage != "" {             // [条件: userMessage != ""]
		reply = m.cfg.APIKey // 读 m.cfg.APIKey（if 内）
		h := &Handler{}
		reply = h.PageChatSend(ctx, reply) // 跨函数
	}
	newSessionID = sessionID
	return reply, newSessionID, nil
}
