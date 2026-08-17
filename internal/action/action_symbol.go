package action

import (
	"errors"
	"fmt"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
)

// ResolveSymbol 将用户输入解析为符号：canonical ID 直接命中，否则按名称查找；
// 多匹配时返回错误并列出候选 ID（原 CLI 语义，供符号类 action 复用）。
func (a *Actions) ResolveSymbol(input string) (*domain.CodeEntity, error) {
	if strings.HasPrefix(input, "symbol:") || strings.HasPrefix(input, "file:") || strings.HasPrefix(input, "commit:") {
		n, err := a.repo.GetSymbol(domain.CanonicalID(input))
		if err == nil {
			return n, nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	matches, err := a.repo.GetSymbolByName(input)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("符号 %q 不存在", input)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("符号 %q 有 %d 个匹配，请使用 canonical ID:\n  %s",
			input, len(matches), joinIDs(matches))
	}
	return matches[0], nil
}

// joinIDs 拼接候选 ID（多匹配错误提示用）。
func joinIDs(nodes []*domain.CodeEntity) string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, string(n.ID))
	}
	return strings.Join(ids, "\n  ")
}

// ResolveAnchor 解析摘要/生命周期锚点（③⑤）：canonical ID 直连、符号
// 名称解析，类型限定字段路径（example.com/m.T.A）回退到同字段读节点
// （FindFieldReads 首个）——此前字段路径被误报"不存在的符号"。
func (a *Actions) ResolveAnchor(input string) (domain.CanonicalID, error) {
	if strings.HasPrefix(input, "symbol:") || strings.HasPrefix(input, "file:") || strings.HasPrefix(input, "commit:") {
		if _, err := a.repo.GetSymbol(domain.CanonicalID(input)); err == nil {
			return domain.CanonicalID(input), nil
		}
	}
	if n, err := a.ResolveSymbol(input); err == nil {
		return n.ID, nil
	}
	if reads, err := a.repo.FindFieldReads(input); err == nil && len(reads) > 0 {
		return reads[0].ID, nil
	}
	return "", fmt.Errorf("符号或字段路径 %q 不存在", input)
}

// Symbol 按 canonical ID 查询符号（HTTP expand 的存在性检查用）。
func (a *Actions) Symbol(id domain.CanonicalID) (*domain.CodeEntity, error) {
	return a.repo.GetSymbol(id)
}

// SymbolDetail 符号详情：基本信息 + 调用者/被调用者摘要（query symbol）。
type SymbolDetail struct {
	Node    *domain.CodeEntity
	Callers []*domain.Fact
	Callees []*domain.Fact
}

// SymbolDetail 解析符号并返回其详情（调用者/被调用者深度 1）。
func (a *Actions) SymbolDetail(input string) (*SymbolDetail, error) {
	n, err := a.ResolveSymbol(input)
	if err != nil {
		return nil, err
	}
	callers, err := a.repo.GetCallers(n.ID, 1, MinConfidence)
	if err != nil {
		return nil, err
	}
	callees, err := a.repo.GetCallees(n.ID, 1, MinConfidence)
	if err != nil {
		return nil, err
	}
	return &SymbolDetail{Node: n, Callers: callers, Callees: callees}, nil
}
