package canonicalizer

import (
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
)

func TestFromScipSymbol(t *testing.T) {
	cases := []struct {
		symbol   string
		wantPath string
		wantName string
		wantErr  bool
	}{
		{
			symbol:   "scip-go gomod example.com/mtest . `example.com/mtest`/NewService().",
			wantPath: "example.com/mtest",
			wantName: "NewService",
		},
		{
			// 子包：Package.Name 只有 module 名，完整路径来自 descriptor
			symbol:   "scip-go gomod codeintel . `codeintel/internal/orchestrator`/FullBuild().",
			wantPath: "codeintel/internal/orchestrator",
			wantName: "FullBuild",
		},
		{
			symbol:   "scip-go gomod example.com/mtest . `example.com/mtest`/global.",
			wantErr:  true, // 变量不建节点
		},
		{
			symbol:   "scip-go gomod example.com/mtest . `example.com/mtest`/Service#",
			wantPath: "example.com/mtest",
			wantName: "Service",
		},
		{
			// 方法：值/指针接收者统一为 (T).m
			symbol:   "scip-go gomod example.com/mtest . `example.com/mtest`/Service#CreatePayment().",
			wantPath: "example.com/mtest",
			wantName: "(Service).CreatePayment",
		},
		{
			// 接口方法
			symbol:   "scip-go gomod example.com/mtest . `example.com/mtest`/Payer#CreatePayment.",
			wantPath: "example.com/mtest",
			wantName: "(Payer).CreatePayment",
		},
		{
			symbol:   "scip-go gomod example.com/mtest . `example.com/mtest`/",
			wantPath: "example.com/mtest",
			wantName: "mtest",
		},
		{
			symbol:  "local 3",
			wantErr: true,
		},
	}
	for _, c := range cases {
		got, err := FromScipSymbol(c.symbol)
		if c.wantErr {
			if err == nil {
				t.Errorf("FromScipSymbol(%q) expected error, got %+v", c.symbol, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("FromScipSymbol(%q) unexpected error: %v", c.symbol, err)
			continue
		}
		if got.ImportPath != c.wantPath {
			t.Errorf("FromScipSymbol(%q) path = %q, want %q", c.symbol, got.ImportPath, c.wantPath)
		}
		if got.Name != c.wantName {
			t.Errorf("FromScipSymbol(%q) name = %q, want %q", c.symbol, got.Name, c.wantName)
		}
	}
}

func TestMethodName(t *testing.T) {
	if got := MethodName("*Service", "CreatePayment"); got != "(Service).CreatePayment" {
		t.Errorf("MethodName(*Service) = %q", got)
	}
	if got := MethodName("Service", "CreatePayment"); got != "(Service).CreatePayment" {
		t.Errorf("MethodName(Service) = %q", got)
	}
}

func TestScipKindToDomainKind(t *testing.T) {
	if k, ok := ScipKindToDomainKind(scip.SymbolInformation_Method); !ok || k != "method" {
		t.Errorf("Method -> %v %v", k, ok)
	}
	if k, ok := ScipKindToDomainKind(scip.SymbolInformation_MethodSpecification); !ok || k != "method" {
		t.Errorf("MethodSpecification -> %v %v", k, ok)
	}
	if _, ok := ScipKindToDomainKind(scip.SymbolInformation_Variable); ok {
		t.Error("Variable should not create node")
	}
}

func TestGoSymbolID(t *testing.T) {
	id := GoSymbolID("example.com/mtest", "main")
	if !strings.HasPrefix(string(id), "symbol:go:example.com/mtest:main") {
		t.Errorf("GoSymbolID = %q", id)
	}
}
