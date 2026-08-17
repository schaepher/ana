package sqlite

import (
	"database/sql"
	"strings"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// GetPath 节点间最短路径（field_trace.md §17.3）：
// BFS（有向 from→to，visited 防环），返回路径节点序列（TraceRow，
// EdgeKinds = 进入该节点的边类型）。viaCalls=true 用函数调用边集
// （calls/passes_to/passes_result），否则数据流边集（data_flows_to/
// argument/returns/phi_operand/summary_io）。不可达返回空切片。
func (r *Repo) GetPath(from, to domain.CanonicalID, maxDepth int, viaCalls bool) ([]*domain.TraceRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetPath")
	defer logger.Debug("exit (Repo).GetPath")
	if from == to {

		n, err := r.GetSymbol(from)
		if err != nil {
			return nil, err
		}
		return []*domain.TraceRow{{ID: from, Name: n.Name, Kind: n.Kind, Line: n.LineStart}}, nil
	}
	kinds := `'data_flows_to','argument','returns','phi_operand','summary_io'`
	if viaCalls {
		kinds = `'calls','passes_to','passes_result'`
	}
	rows, err := r.Query(`SELECT source_id, target_id, kind FROM edges WHERE kind IN (` + kinds + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 邻接表 + 边类型
	type edge struct {
		to   domain.CanonicalID
		kind string
	}
	adj := map[domain.CanonicalID][]edge{}
	for rows.Next() {
		var s, t, k string
		if err := rows.Scan(&s, &t, &k); err != nil {
			return nil, err
		}
		adj[domain.CanonicalID(s)] = append(adj[domain.CanonicalID(s)], edge{domain.CanonicalID(t), k})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	parent := map[domain.CanonicalID]struct {
		prev domain.CanonicalID
		kind string
	}{}
	queue := []domain.CanonicalID{from}
	visited := map[domain.CanonicalID]bool{from: true}
	found := false
	for len(queue) > 0 && !found {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range adj[cur] {
			if visited[e.to] {
				continue
			}
			visited[e.to] = true
			parent[e.to] = struct {
				prev domain.CanonicalID
				kind string
			}{cur, e.kind}
			if e.to == to {
				found = true
				break
			}
			if len(parent) <= maxDepth {
				queue = append(queue, e.to)
			}
		}
	}
	if !found {
		return nil, nil
	}
	// 回溯路径
	var ids []domain.CanonicalID
	var kindsPath []string
	for cur := to; cur != from; {
		ids = append(ids, cur)
		p := parent[cur]
		kindsPath = append(kindsPath, p.kind)
		cur = p.prev
	}
	ids = append(ids, from)

	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	for i, j := 0, len(kindsPath)-1; i < j; i, j = i+1, j-1 {
		kindsPath[i], kindsPath[j] = kindsPath[j], kindsPath[i]
	}

	out := make([]*domain.TraceRow, 0, len(ids))
	for i, id := range ids {
		n, err := r.GetSymbol(id)
		if err != nil {
			continue
		}
		row := &domain.TraceRow{ID: id, Name: n.Name, Kind: n.Kind, Line: n.LineStart}
		if i > 0 {
			row.EdgeKinds = kindsPath[i-1]
		}
		out = append(out, row)
	}
	return out, nil
}

// GetGrpcCalls 模块间调用原始行（field_trace.md §18.3/§18.7）：
// grpc_call 边（客户端调用方 → grpc_service）+ 经 grpc_impl 边反查
// 服务端实现类型；http_call 边（→ http_route，经 route.handler_id
// 反查服务端 handler 函数）。无实现/无 handler 时 ImplTypeID 空——
// 服务端不在仓库内（[外部服务]）。
func (r *Repo) GetGrpcCalls() ([]*domain.GrpcCallRow, error) {
	logger := zap.L()
	logger.Debug("enter (Repo).GetGrpcCalls")
	defer logger.Debug("exit (Repo).GetGrpcCalls")
	rows, err := r.Query(`SELECT e.source_id, e.target_id, n.name,
		COALESCE(json_extract(e.metadata, '$.method'), json_extract(e.metadata, '$.path'), ''),
		COALESCE(json_extract(e.metadata, '$.line_num'), 0),
		CASE WHEN e.kind = 'grpc_call' THEN
			(SELECT s.source_id FROM edges s JOIN nodes sn ON sn.id = s.target_id
			 WHERE s.kind = 'grpc_impl' AND sn.name = n.name LIMIT 1)
		ELSE json_extract(n.properties, '$.handler_id') END,
		e.kind
	FROM edges e JOIN nodes n ON n.id = e.target_id
	WHERE e.kind IN ('grpc_call','http_call') ORDER BY e.source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*domain.GrpcCallRow
	for rows.Next() {
		var (
			row       domain.GrpcCallRow
			caller    string
			svc       string
			name      string
			method    string
			line      int
			impl      sql.NullString
			transport string
		)
		if err := rows.Scan(&caller, &svc, &name, &method, &line, &impl, &transport); err != nil {
			return nil, err
		}
		row.CallerID = domain.CanonicalID(caller)
		row.ServiceID = domain.CanonicalID(svc)
		row.Transport = transport
		if transport == "grpc_call" {
			row.Service = strings.TrimPrefix(name, "svc.")
			if pkg := pkgOfID(row.ServiceID); pkg != "" {
				row.Service = pkg + "." + strings.TrimPrefix(name, "svc.")
			}
			row.Method = method
		} else {

			row.Method = method
			row.Service = name
		}
		row.Line = line
		if impl.Valid && impl.String != "" {
			row.ImplTypeID = domain.CanonicalID(impl.String)
		}
		out = append(out, &row)
	}
	return out, rows.Err()
}

// pkgOfID 从 canonical ID 提取包路径（symbol:go:<pkg>:<name>）。
func pkgOfID(id domain.CanonicalID) string {
	s := strings.TrimPrefix(string(id), "symbol:go:")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}
