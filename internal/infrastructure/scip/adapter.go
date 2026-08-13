// Package scip 实现 SCIP 适配器（TD.md 5.1：符号权威，置信度 1.0）。
// 调用 scip-go 生成 SCIP 索引（protobuf），解析后产出：
//   - 符号定义节点（function/method/struct/interface/package/file）
//   - IMPLEMENTS 边（来自 SymbolInformation.Relationships 的 is_implementation）
//
// 已知限制：scip-go 的定义 occurrence 只覆盖符号名本身（不含函数体），
// 引用 occurrence 无法归属到引用者符号，故 REFERENCES 边由 AST 分析补齐。
package scip

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"

	"github.com/schaepher/codeintel/internal/canonicalizer"
	"github.com/schaepher/codeintel/internal/domain"
	"github.com/schaepher/codeintel/internal/logging"
	"go.uber.org/zap"
)

// Adapter 是 SCIP 索引适配器。
type Adapter struct {
	BinPath string // scip-go 二进制路径，空则自动查找
}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	logger := zap.L()
	logger.Debug("enter (Adapter).Name")
	defer logger.Debug("exit (Adapter).Name")
	return "scip"
}

// Index 在仓库上执行全量 SCIP 索引并流式产出节点与边。
func (a *Adapter) Index(ctx context.Context, repo *domain.Repository, emit domain.EmitFunc) error {
	logger := logging.FromContext(ctx)
	logger.Debug("enter (Adapter).Index")
	defer logger.Debug("exit (Adapter).Index")
	bin, err := a.resolveBin()
	if err != nil {
		return err
	}
	indexPath := filepath.Join(repo.Path, ".codeintel", "index.scip")
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return fmt.Errorf("create .codeintel: %w", err)
	}

	// --skip-tests：不索引 _test.go 测试文件（测试符号不入图）
	cmd := exec.CommandContext(ctx, bin, "index", "-o", indexPath, "-q", "--skip-tests")
	cmd.Dir = repo.Path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scip-go index failed: %v: %s", err, string(out))
	}
	defer os.Remove(indexPath)

	data, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("read scip index: %w", err)
	}
	idx := &scip.Index{}
	if err := proto.Unmarshal(data, idx); err != nil {
		return fmt.Errorf("parse scip index: %w", err)
	}

	for _, doc := range idx.Documents {
		if err := a.processDocument(repo, doc, emit); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) resolveBin() (string, error) {
	logger := zap.L()
	logger.Debug("enter (Adapter).resolveBin")
	defer logger.Debug("exit (Adapter).resolveBin")
	if a.BinPath != "" {
		return a.BinPath, nil
	}
	if p, err := exec.LookPath("scip-go"); err == nil {
		return p, nil
	}
	// 兜底：go env GOBIN / GOPATH/bin（go install 的默认位置）
	for _, envVar := range []string{"GOBIN", "GOPATH"} {
		out, err := exec.Command("go", "env", envVar).Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			continue
		}
		p := filepath.Join(trimmed, "bin", "scip-go")
		if envVar == "GOBIN" {
			p = filepath.Join(trimmed, "scip-go")
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("scip-go not found (install with: go install github.com/scip-code/scip-go/cmd/scip-go@latest)")
}

// processDocument 处理单个 SCIP document：符号节点（含定义行范围）+ IMPLEMENTS 边。
func (a *Adapter) processDocument(repo *domain.Repository, doc *scip.Document, emit domain.EmitFunc) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processDocument")
	defer logger.Debug("exit (Adapter).processDocument")
	filePath := doc.RelativePath

	// FILE 节点：ID 为 file:<relpath>
	if err := emit(domain.Item{Node: &domain.CodeEntity{
		ID:       domain.CanonicalID("file:" + filePath),
		Kind:     domain.KindFile,
		Name:     filepath.Base(filePath),
		FilePath: filePath,
	}}); err != nil {
		return err
	}

	// pass 1: 符号 → 节点信息
	type symInfo struct {
		node *domain.CodeEntity
		occ  *scip.Occurrence
	}
	defs := map[string]*symInfo{} // SCIP symbol 字符串 → 节点信息

	for _, sym := range doc.Symbols {
		gs, err := canonicalizer.FromScipSymbol(sym.Symbol)
		if err != nil {
			continue // local / unsupported 符号跳过
		}
		if gs.IsInterfaceMethod {
			continue // 接口方法不作为独立节点（implements 只连接口类型→实现者）
		}
		kind, ok := canonicalizer.ScipKindToDomainKind(sym.Kind)
		if !ok {
			continue // 变量/常量/字段等不建节点
		}
		props := map[string]any{}
		// 注：scip-go (scip v0.7.1) 不输出 signature 字段，签名由 AST 适配器补充
		if len(sym.Documentation) > 0 {
			props["doc_comment"] = strings.Join(sym.Documentation, "\n")
		}
		defs[sym.Symbol] = &symInfo{node: &domain.CodeEntity{
			ID:         canonicalizer.GoSymbolID(gs.ImportPath, gs.Name),
			Kind:       kind,
			Name:       gs.Name,
			FilePath:   filePath,
			Properties: props,
		}}
	}

	// pass 2: 定义 occurrence → 行范围
	for _, occ := range doc.Occurrences {
		if occ.SymbolRoles&int32(scip.SymbolRole_Definition) == 0 {
			continue
		}
		if si, ok := defs[occ.Symbol]; ok {
			si.occ = occ
		}
	}

	// 发出节点（带行范围；scip-go 的 range 为单行 [start_line, start_char, end_char]）
	for _, si := range defs {
		if si.occ != nil && len(si.occ.Range) > 0 {
			si.node.LineStart = int(si.occ.Range[0]) + 1
			si.node.LineEnd = si.node.LineStart
		}
		if err := emit(domain.Item{Node: si.node}); err != nil {
			return err
		}
	}

	// pass 3: IMPLEMENTS 边（is_implementation relationship）——方向：
	// 接口 → 实现者（用户确认：接口要指向实现，而非实现指向接口）
	for _, sym := range doc.Symbols {
		if len(sym.Relationships) == 0 {
			continue
		}
		src, err := canonicalizer.FromScipSymbol(sym.Symbol)
		if err != nil {
			continue
		}
		if src.IsInterfaceMethod {
			continue // 接口方法不建 implements 边（只连接口类型→实现者）
		}
		implID := canonicalizer.GoSymbolID(src.ImportPath, src.Name) // 实现者
		for _, rel := range sym.Relationships {
			if !rel.IsImplementation {
				continue
			}
			iface, err := canonicalizer.FromScipSymbol(rel.Symbol)
			if err != nil {
				continue
			}
			if iface.IsInterfaceMethod {
				continue // 方法级 implements（接口方法→实现方法）不建
			}
			if !isInModule(iface.ImportPath, repo.Module) {
				continue // 外部接口无节点（外键约束）
			}
			if err := emit(domain.Item{Fact: &domain.Fact{
				SourceID:   canonicalizer.GoSymbolID(iface.ImportPath, iface.Name), // 接口
				TargetID:   implID,                                                 // 实现者
				Kind:       domain.FactImplements,
				ToolSource: domain.ToolSCIP,
				Confidence: 1.0,
			}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// isInModule 判断 importPath 是否属于被索引 module（自身或子包）。
func isInModule(importPath, module string) bool {
	logger := zap.L()
	logger.Debug("enter isInModule")
	defer logger.Debug("exit isInModule")
	return importPath == module || strings.HasPrefix(importPath, module+"/")
}
