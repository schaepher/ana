package ssa

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
)

// applySummary 对带摘要的外部函数调用生成虚拟节点与边。
// 返回 false 表示无摘要（或无需处理）。
func (ext *fieldExtractor) applySummary(cc *ssa.CallCommon, callee *ssa.Function, callVal ssa.Value) (bool, error) {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applySummary")
	defer logger.Debug("exit (fieldExtractor).applySummary")
	if len(ext.specs) == 0 {
		return false, nil
	}
	key := summaryKey(callee)
	spec, ok := ext.specs[key]
	if !ok {
		return false, nil
	}

	calleeID, ok := ext.funcIDOfFn(callee)
	if !ok {
		return false, nil
	}
	if !ext.extSummaries[calleeID] {
		ext.extSummaries[calleeID] = true
		specJSON := fmt.Sprintf(`{"reads":%q,"writes":%q,"param_index":%d}`,
			strings.Join(spec.Reads, ","), strings.Join(spec.Writes, ","), spec.ParamIndex)
		if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
			ID:   calleeID,
			Kind: domain.KindExternalSummary,
			Name: callee.Name(),
			Properties: map[string]any{
				"summary_json": specJSON,
				"func_id":      string(ext.funcID),
			},
		}}); err != nil {
			return true, err
		}
	}

	if spec.SQLStmt {
		return true, ext.applySQLSummary(cc, calleeID, spec, callVal, 1)
	}

	if spec.TxBoundary != "" {
		return true, ext.applyTxBoundary(cc, calleeID, spec.TxBoundary)
	}

	if spec.ORMWrite {
		return true, ext.applyORMWrite(cc, calleeID)
	}

	if spec.ORMRead {
		return true, ext.applyORMRead(cc, calleeID)
	}

	if spec.ScanOut {
		if err := ext.applyScanOut(cc, calleeID); err != nil {
			return true, err
		}
	}

	start := spec.ParamIndex
	if spec.ParamIndex < 0 || spec.ParamIndex >= len(cc.Args) {
		return true, nil
	}
	for i := start; i < len(cc.Args); i++ {
		arg := cc.Args[i]
		if err := ext.applyArgSummary(cc, calleeID, spec, i, arg); err != nil {
			return true, err
		}
	}
	return true, nil
}

// applyArgSummary 对单个实参应用摘要（all 模式递归展开字段）。
func (ext *fieldExtractor) applyArgSummary(cc *ssa.CallCommon, calleeID domain.CanonicalID,
	spec summarySpec, argIdx int, arg ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyArgSummary")
	defer logger.Debug("exit (fieldExtractor).applyArgSummary")

	elems := variadicElems(arg)
	for _, el := range elems {
		if err := ext.applyArgSummaryOne(cc, calleeID, spec, el); err != nil {
			return err
		}
	}
	return nil
}

// applyArgSummaryOne 对单个实参值应用摘要。
func (ext *fieldExtractor) applyArgSummaryOne(cc *ssa.CallCommon, calleeID domain.CanonicalID,
	spec summarySpec, arg ssa.Value) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyArgSummaryOne")
	defer logger.Debug("exit (fieldExtractor).applyArgSummaryOne")
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line

	realArg := arg
	if mi, ok := arg.(*ssa.MakeInterface); ok {
		realArg = mi.X
	}
	fields := spec.Reads
	if spec.WritesAll {
		fields = expandAllFields(realArg.Type(), 0)
	}
	if spec.ReadArgsAll {
		fields = expandAllFields(realArg.Type(), 0)
	}
	argID, err := ext.emitValue(realArg)
	if err != nil {
		return err
	}
	base := ext.instancePath(realArg)
	for _, f := range fields {
		fullPath := f

		if !strings.Contains(f, "/") {
			if named := namedStructOf(realArg.Type()); named != nil {
				seg := f
				if i := strings.LastIndex(f, "."); i >= 0 {
					seg = f[i+1:]
				}
				fullPath = named.Obj().Pkg().Path() + "." + named.Obj().Name() + "." + seg
			}
		}
		access := "read"
		if spec.WritesAll || contains(spec.Writes, f) {
			access = "write"
		}
		instance := base + "." + lastPathSeg(f)
		id := domain.CanonicalID(string(ext.funcID) + "#ext." + fullPath + "." + access + "@" + fmt.Sprintf("%d", line))
		node := &domain.CodeEntity{
			ID:        id,
			Kind:      domain.KindFieldAccess,
			Name:      instance,
			FilePath:  ext.currentFile,
			LineStart: line,
			LineEnd:   line,
			Properties: map[string]any{
				"full_path":     fullPath,
				"instance_path": instance,
				"access_kind":   access,
				"type_string":   realArg.Type().String(),
				"func_id":       string(ext.funcID),
				"is_external":   "true",
				"code_snippet":  ext.sourceLine(ext.currentFile, line),
			},
		}
		if err := ext.emit(domain.Item{Node: node}); err != nil {
			return err
		}

		if err := ext.emitEdgeKind(calleeID, id, domain.FactSummaryIO); err != nil {
			return err
		}
		if access == "write" {

			if err := ext.emitEdgeKind(ext.funcID, id, domain.FactIndirectWrite); err != nil {
				return err
			}
			if argID != "" {
				if err := ext.emitEdge(argID, id); err != nil {
					return err
				}
			}

			if ext.funcData != nil {
				ext.funcData.indirectWrites = append(ext.funcData.indirectWrites, fieldEntry{
					fieldPath:    fullPath,
					instancePath: instance,
					line:         line,
					snippet:      ext.sourceLine(ext.currentFile, line),
				})
			}
		} else if argID != "" {

			if err := ext.emitEdge(id, argID); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyScanOut 处理 Scan 写 out 实参（表关联链贯通）：
// row.Scan(&x) —— 接收者（row 值）→ 实参指向的局部变量节点
// （变量名 ID find#x，与 Load 归一节点一致）。数据流链：
// table_a.x.read → row → x → table_b.y.filter。
func (ext *fieldExtractor) applyScanOut(cc *ssa.CallCommon, calleeID domain.CanonicalID) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyScanOut")
	defer logger.Debug("exit (fieldExtractor).applyScanOut")
	if len(cc.Args) < 1 {
		return nil
	}
	recvID, err := ext.emitValue(cc.Args[0])
	if err != nil || recvID == "" {
		return nil
	}
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line

	for i := 1; i < len(cc.Args); i++ {
		for _, arg := range variadicElems(cc.Args[i]) {

			real := arg
			if mi, ok := arg.(*ssa.MakeInterface); ok {
				real = mi.X
			}
			name := ext.instancePath(real)
			if isSSAName(name) {
				continue
			}
			fid, ok := ext.funcIDOf(real)
			if !ok {
				continue
			}
			id := domain.CanonicalID(string(fid) + "#" + name)
			if err := ext.emit(domain.Item{Node: &domain.CodeEntity{
				ID:        id,
				Kind:      domain.KindSSAValue,
				Name:      name,
				FilePath:  ext.currentFile,
				LineStart: line,
				Properties: map[string]any{
					"origin_kind": "local",
					"ssa_op":      "scan_out",
					"type_string": arg.Type().String(),
					"func_id":     string(fid),
				},
			}}); err != nil {
				return err
			}
			if err := ext.emitEdgeKindLine(recvID, id, domain.FactDataFlowsTo, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyTxBoundary 事务边界（Q97）：Begin/Commit/Rollback → 事务虚拟节点
// （Name=sql.tx.<boundary>），标注事务边界位置。
func (ext *fieldExtractor) applyTxBoundary(cc *ssa.CallCommon, calleeID domain.CanonicalID, boundary string) error {
	logger := zap.L()
	logger.Debug("enter (fieldExtractor).applyTxBoundary")
	defer logger.Debug("exit (fieldExtractor).applyTxBoundary")
	line := ext.prog.Fset.PositionFor(cc.Pos(), false).Line
	name := "sql.tx." + boundary
	id := domain.CanonicalID(string(ext.funcID) + "#ext." + name + "@" + fmt.Sprintf("%d", line))
	return ext.emit(domain.Item{Node: &domain.CodeEntity{
		ID:        id,
		Kind:      domain.KindFieldAccess,
		Name:      name,
		FilePath:  ext.currentFile,
		LineStart: line,
		Properties: map[string]any{
			"full_path":     name,
			"instance_path": name,
			"access_kind":   "write",
			"type_string":   "tx",
			"is_external":   "true",
			"func_id":       string(ext.funcID),
		},
	}})
}

func gormColumnOf(tag reflect.StructTag, fieldName string) string {
	g := tag.Get("gorm")
	for _, part := range strings.Split(g, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "column:") {
			if col := strings.TrimSpace(strings.TrimPrefix(part, "column:")); col != "" {
				return col
			}
		}
	}
	return snakeCase(fieldName)
}
