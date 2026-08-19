// Command seed creates the smallest historical codeintel graph that produces
// the order_tab.id false-FK result. It deliberately does not infer
// this graph from repro.go: current ORM summaries no longer emit the old
// row-read summary_io edge which made the original index reachable.
package main

import (
        "flag"
        "fmt"
        "log"

        "github.com/schaepher/codeintel/internal/domain"
        "github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

const (
        loadFunc  = "symbol:go:repro:loadOrders"
        queryFunc = "symbol:go:repro:queryRelatedData"
)

type target struct {
        table  string
        column string
        hops   int
}

var targets = []target{
        {table: "book_tab", column: "party_id", hops: 8},
        {table: "book_tab", column: "top_party_id", hops: 8},
        {table: "book_tab", column: "target_id", hops: 8},
        {table: "file_tab", column: "file_id", hops: 11},
        {table: "mapping_tab", column: "file_id", hops: 11},
}

func main() {
        repoPath := flag.String("repo", ".", "fixture repository path")
        flag.Parse()

        db, err := sqlite.Open(*repoPath)
        if err != nil {
                log.Fatal(err)
        }
        defer db.Close()
        repo := sqlite.NewRepo(db)
        if err := repo.ResetGraphTables(); err != nil {
                log.Fatal(err)
        }
        // ResetGraphTables deliberately keeps build metadata and relation caches for
        // normal reindexing. A reproducer must start with neither, otherwise an
        // earlier empty result can be served from relation_candidates.
        if _, err := repo.Exec("DELETE FROM relation_candidates"); err != nil {
                log.Fatal(err)
        }
        if _, err := repo.Exec("DELETE FROM build_metadata"); err != nil {
                log.Fatal(err)
        }
        if _, err := repo.Exec("DELETE FROM relation_rules"); err != nil {
                log.Fatal(err)
        }

        nodes, edges := graph()
        if _, err := repo.SaveBatchStats(nodes, edges, nil); err != nil {
                log.Fatal(err)
        }
        fmt.Printf("seeded %d nodes and %d edges in %s/.codeintel\n", len(nodes), len(edges), *repoPath)
}

func graph() ([]*domain.CodeEntity, []*domain.Fact) {
        start := id("order_tab.id.read")
        nodes := []*domain.CodeEntity{
                function(loadFunc, "loadOrders"),
                function(queryFunc, "queryRelatedData"),
                external(start, loadFunc, "order_tab.id", "read"),
        }
        var edges []*domain.Fact
        for _, t := range targets {
                branchNodes, branchEdges := branch(start, t)
                nodes = append(nodes, branchNodes...)
                edges = append(edges, branchEdges...)
        }
        return nodes, edges
}

func branch(start domain.CanonicalID, t target) ([]*domain.CodeEntity, []*domain.Fact) {
        prefix := t.table + "." + t.column
        row := id(prefix + ".row")
        rowID := id(prefix + ".row.ID.read")
        filter := id(prefix + ".filter")
        nodes := []*domain.CodeEntity{
                {ID: row, Kind: domain.KindSSAValue, Name: prefix + ".row", Properties: map[string]any{"func_id": loadFunc, "type_string": "*repro.Order"}},
                {ID: rowID, Kind: domain.KindFieldAccess, Name: prefix + ".row.ID", Properties: map[string]any{"func_id": loadFunc, "full_path": "repro.Order.ID", "access_kind": "read"}},
                external(filter, queryFunc, t.table+"."+t.column, "filter"),
        }
        edges := []*domain.Fact{
                edge(start, row, domain.FactSummaryIO),
                edge(row, rowID, domain.FactDataFlowsTo),
        }
        previous := rowID
        // start → row → row.ID → middle... → filter has exactly t.hops edges.
        for i := 0; i < t.hops-3; i++ {
                middle := id(fmt.Sprintf("%s.middle.%d", prefix, i))
                nodes = append(nodes, &domain.CodeEntity{ID: middle, Kind: domain.KindSSAValue, Name: string(middle), Properties: map[string]any{"func_id": queryFunc, "type_string": "uint64"}})
                edges = append(edges, edge(previous, middle, domain.FactDataFlowsTo))
                previous = middle
        }
        edges = append(edges, edge(previous, filter, domain.FactSummaryIO))
        return nodes, edges
}

func id(s string) domain.CanonicalID { return domain.CanonicalID("repro#" + s) }

func function(identifier, name string) *domain.CodeEntity {
        return &domain.CodeEntity{ID: domain.CanonicalID(identifier), Kind: domain.KindFunction, Name: name}
}

func external(identifier domain.CanonicalID, funcID, name, access string) *domain.CodeEntity {
        return &domain.CodeEntity{ID: identifier, Kind: domain.KindFieldAccess, Name: name, Properties: map[string]any{
                "func_id": funcID, "full_path": name, "access_kind": access,
                "type_string": "xorm", "is_external": "true",
        }}
}

func edge(source, target domain.CanonicalID, kind domain.FactKind) *domain.Fact {
        return &domain.Fact{SourceID: source, TargetID: target, Kind: kind, ToolSource: domain.ToolSSA, Confidence: 1}
}
