// Package canonicalizer 实现实体解析与冲突裁决（TD.md 5.3）：
// Canonical ID 生成（symbol:go:<import_path>:<name>）与 SCIP 符号解析。
package canonicalizer

import (
	"fmt"
	pathpkg "path"

	"github.com/scip-code/scip/bindings/go/scip"

	"codeintel/internal/domain"
)

// GoSymbol 解析后的 Go 符号身份。
type GoSymbol struct {
	ImportPath string
	Name       string // canonical 名：函数为 funcName，方法为 (T).methodName
	IsMethod   bool
}

// GoSymbolID 生成 canonical ID：symbol:go:<import_path>:<name>。
func GoSymbolID(importPath, name string) domain.CanonicalID {
	return domain.CanonicalID("symbol:go:" + importPath + ":" + name)
}

// MethodName 将接收者类型名与方法名规范化为 canonical 方法名。
// 与 scip-go 一致：值/指针接收者统一为 (T).method 形式。
func MethodName(recvTypeName, method string) string {
	recv := recvTypeName
	if len(recv) > 0 && recv[0] == '*' {
		recv = recv[1:]
	}
	return "(" + recv + ")." + method
}

// FromScipSymbol 解析 SCIP symbol 字符串，得到 Go 符号身份。
//
// scip-go 生成的 symbol 形如 `scip-go gomod <pkg> . <descriptors>`，
// ParseSymbol 后 descriptors 结构：
//
//	desc[0] Namespace（包路径）
//	desc[1] Method    → 函数（NewService().）
//	desc[1] Term      → 变量/常量（global.）
//	desc[1] Type      → 类型（Service#）
//	desc[1] Type + desc[2] Method → 方法（Service#CreatePayment().）
//	desc[1] Type + desc[2] Term   → 接口方法（Payer#CreatePayment.）
//	"local N"                      → 局部符号（跳过）
func FromScipSymbol(sym string) (GoSymbol, error) {
	if len(sym) >= 6 && sym[:6] == "local " {
		return GoSymbol{}, fmt.Errorf("local symbol: %s", sym)
	}
	parsed, err := scip.ParseSymbol(sym)
	if err != nil {
		return GoSymbol{}, fmt.Errorf("parse scip symbol %q: %w", sym, err)
	}
	descs := parsed.Descriptors
	if len(descs) == 0 {
		return GoSymbol{}, fmt.Errorf("symbol %q has no descriptors", sym)
	}
	gs := GoSymbol{ImportPath: parsed.Package.GetName()}
	// 完整包路径在 Namespace descriptor 中（反引号内容）；scip-go 的
	// Package.Name 只含 module 名（如 "codeintel"），子包必须用 descriptor。
	if descs[0].Suffix == scip.Descriptor_Namespace && descs[0].Name != "" {
		gs.ImportPath = descs[0].Name
	}

	switch {
	case len(descs) == 1 && descs[0].Suffix == scip.Descriptor_Namespace:
		// 包（`` `example.com/mtest`/ ``）：name 取路径最后一段
		gs.Name = pathpkg.Base(descs[0].Name)
	case len(descs) == 2 && descs[1].Suffix == scip.Descriptor_Method:
		// 函数
		gs.Name = descs[1].Name
	case len(descs) == 2 && descs[1].Suffix == scip.Descriptor_Type:
		// 类型（struct/interface）
		gs.Name = descs[1].Name
	case len(descs) >= 3 && descs[1].Suffix == scip.Descriptor_Type:
		// 方法 / 接口方法：接收者为 desc[1]，名为最后一个 descriptor
		last := descs[len(descs)-1]
		gs.Name = MethodName(descs[1].Name, last.Name)
		gs.IsMethod = true
	default:
		// Term（变量/常量/字段）及其他：不在图中建节点
		return GoSymbol{}, fmt.Errorf("unsupported symbol: %s", sym)
	}
	return gs, nil
}

// ScipKindToDomainKind 将 SCIP 符号种类映射为领域种类。
// Variable/Constant/Field/Local/TypeParameter 等不在图中建节点。
func ScipKindToDomainKind(k scip.SymbolInformation_Kind) (domain.EntityKind, bool) {
	switch k {
	case scip.SymbolInformation_Package:
		return domain.KindPackage, true
	case scip.SymbolInformation_Function:
		return domain.KindFunction, true
	case scip.SymbolInformation_Method, scip.SymbolInformation_MethodSpecification:
		return domain.KindMethod, true
	case scip.SymbolInformation_Struct:
		return domain.KindStruct, true
	case scip.SymbolInformation_Interface:
		return domain.KindInterface, true
	default:
		return "", false
	}
}
