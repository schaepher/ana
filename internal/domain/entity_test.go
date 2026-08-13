package domain

import "testing"

func TestCodeEntityProperty(t *testing.T) {
	cases := []struct {
		name  string
		props map[string]any
		key   string
		want  string
	}{
		{"nil map", nil, "signature", ""},
		{"missing key", map[string]any{"a": "1"}, "signature", ""},
		{"non-string value", map[string]any{"signature": 42}, "signature", ""},
		{"string value", map[string]any{"signature": "func main()"}, "signature", "func main()"},
	}
	for _, c := range cases {
		e := &CodeEntity{Properties: c.props}
		if got := e.Property(c.key); got != c.want {
			t.Errorf("%s: Property(%q) = %q, want %q", c.name, c.key, got, c.want)
		}
	}
}

func TestSignatureAndDocComment(t *testing.T) {
	e := &CodeEntity{Properties: map[string]any{
		"signature":   "func (s *Service) Handle() error",
		"doc_comment": "Handle 处理请求",
	}}
	if e.Signature() != "func (s *Service) Handle() error" {
		t.Errorf("Signature = %q", e.Signature())
	}
	if e.DocComment() != "Handle 处理请求" {
		t.Errorf("DocComment = %q", e.DocComment())
	}
	// 无属性时均返回空串
	empty := &CodeEntity{}
	if empty.Signature() != "" || empty.DocComment() != "" {
		t.Errorf("empty entity should return empty signature/doc")
	}
}

func TestErrNotFound(t *testing.T) {
	if ErrNotFound == nil {
		t.Fatal("ErrNotFound must not be nil")
	}
}

// 常量 sanity：图/查询逻辑依赖的 FactKind 都必须定义（新增关系时漏
// 定义会导致查询静默为空）。
func TestFactKindsDefined(t *testing.T) {
	defined := map[FactKind]bool{
		FactCalls: false, FactImports: false, FactImplements: false,
		FactInitializes: false, FactUses: false, FactPassesTo: false,
		FactPassesResult: false, FactOfType: false, FactHasMethod: false,
		FactModifiedBy: false, FactDataFlowsTo: false,
	}
	for k := range defined {
		if k == "" {
			t.Errorf("FactKind %v must not be empty string", k)
		}
		defined[k] = true
	}
	for k, ok := range defined {
		if !ok {
			t.Errorf("FactKind %q is empty", k)
		}
	}
}
