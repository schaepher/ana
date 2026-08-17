package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/domain"
)

func TestAPIEndpoints(t *testing.T) {
	ts := newTestServer(t)

	resp, m := get(t, ts, "/api/roots")
	if resp.StatusCode != 200 {
		t.Fatalf("roots status = %d", resp.StatusCode)
	}
	nodes, _ := m["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("roots = %v", nodes)
	}
	first := nodes[0].(map[string]any)
	flags, _ := first["flags"].([]any)
	if len(flags) != 2 || flags[0] != "main" || flags[1] != "http" {
		t.Errorf("main flags = %v, want [main http]", flags)
	}

	if resp, _ := get(t, ts, "/api/search"); resp.StatusCode != 400 {
		t.Errorf("search without q status = %d, want 400", resp.StatusCode)
	}

	_, m = get(t, ts, "/api/search?q=Greet")
	if nodes, _ := m["nodes"].([]any); len(nodes) != 1 {
		t.Errorf("search Greet = %v", nodes)
	}

	if resp, _ := get(t, ts, "/api/expand"); resp.StatusCode != 400 {
		t.Errorf("expand without id status = %d", resp.StatusCode)
	}
	if resp, _ := get(t, ts, "/api/expand?id=nope"); resp.StatusCode != 404 {
		t.Errorf("expand missing symbol status = %d", resp.StatusCode)
	}

	resp, m = get(t, ts, "/api/expand?id=symbol:go:example.com/app:main")
	if resp.StatusCode != 200 {
		t.Fatalf("expand status = %d", resp.StatusCode)
	}
	edges, _ := m["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("expand edges = %v", edges)
	}
	e := edges[0].(map[string]any)
	if e["direction"] != "out" || e["kind"] != "calls" {
		t.Errorf("edge = %v", e)
	}
	if e["line"] != float64(2) {
		t.Errorf("edge line = %v, want 2", e["line"])
	}
	neighbors, _ := m["neighbors"].([]any)
	if len(neighbors) != 1 {
		t.Errorf("neighbors = %v", neighbors)
	}
}
func TestSourceEndpoint(t *testing.T) {
	ts := newTestServer(t)

	if resp, _ := get(t, ts, "/api/source"); resp.StatusCode != 400 {
		t.Errorf("source without id = %d", resp.StatusCode)
	}

	if resp, _ := get(t, ts, "/api/source?id=symbol:go:example.com/app:Svc"); resp.StatusCode != 400 {
		t.Errorf("source struct = %d, want 400", resp.StatusCode)
	}

	if resp, _ := get(t, ts, "/api/source?id=nope"); resp.StatusCode != 404 {
		t.Errorf("source missing = %d, want 404", resp.StatusCode)
	}

	resp, m := get(t, ts, "/api/source?id=symbol:go:example.com/app:Greet")
	if resp.StatusCode != 200 {
		t.Fatalf("source status = %d", resp.StatusCode)
	}
	if m["line"] != float64(4) {
		t.Errorf("source line = %v, want 4 (name-match fallback)", m["line"])
	}
	code, _ := m["code"].(string)
	if !strings.Contains(code, "func Greet") || !strings.Contains(code, "hi ") {
		t.Errorf("source code = %q", code)
	}
	if m["file"] != "internal/app/app.go" {
		t.Errorf("source file = %v", m["file"])
	}
}
func TestStaticFile(t *testing.T) {
	ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("static status = %d", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "codeintel") {
		t.Errorf("static content = %q", string(buf[:n]))
	}
}
func TestNodeToJSON(t *testing.T) {

	n := &domain.CodeEntity{
		ID:   "symbol:go:example.com/app:main",
		Kind: domain.KindFunction,
		Name: "main",
		Properties: map[string]any{
			"serves_http": "true",
			"framework":   "true",
		},
	}
	j := nodeToJSON(n)
	if len(j.Flags) != 3 || j.Flags[0] != "main" || j.Flags[1] != "framework" || j.Flags[2] != "http" {
		t.Errorf("flags = %v", j.Flags)
	}

	n2 := &domain.CodeEntity{ID: "x", Kind: domain.KindStruct, Name: "S", Properties: map[string]any{"framework": "true"}}
	if j2 := nodeToJSON(n2); len(j2.Flags) != 1 || j2.Flags[0] != "framework" {
		t.Errorf("framework flags = %v", j2.Flags)
	}

	n3 := &domain.CodeEntity{ID: "y", Kind: domain.KindStruct, Name: "S", Properties: map[string]any{
		"fields": []any{map[string]any{"name": "Port", "type": "int"}},
	}}
	if j3 := nodeToJSON(n3); len(j3.Fields) != 1 || j3.Fields[0].Name != "Port" || j3.Fields[0].Type != "int" {
		t.Errorf("fields = %+v", j3.Fields)
	}

	n4 := &domain.CodeEntity{ID: "z", Kind: domain.KindStruct, Name: "S", Properties: map[string]any{
		"fields": []any{"bad"},
	}}
	if j4 := nodeToJSON(n4); len(j4.Fields) != 0 {
		t.Errorf("bad fields = %+v", j4.Fields)
	}
}
