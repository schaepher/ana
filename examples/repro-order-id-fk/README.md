# order_tab.id false-FK reproducer

`repro.go` is the business-shaped source example. `Order.ID` is a
shard-local physical primary key, while the five downstream predicates use
`PartyID`, `HostID`, `TargetID`, or `OrderID`.
They never use `Order.ID`.

The old settlement-service index contained a `summary_io` edge from the ORM
row read to a `Order` value. Current source indexing no longer emits
that edge for this fixture, so `reindex` alone does not reproduce the historic
result. `seed` writes the smallest equivalent historical graph, then invokes
the normal `codeintel query relations` implementation.

Build in the repository root, then run the fixture:

```sh
cd /path/to/codeintel
go build -o /tmp/codeintel-repro ./cmd/codeintel
cd examples/repro-order-id-fk
go run ./seed --repo .
/tmp/codeintel-repro query relations order_tab --type fk --json --repo .
```

The query must return these five false edges (the first three at 8 hops; the
last two at 11 hops):

```text
order_tab.id -> book_tab.party_id
order_tab.id -> book_tab.top_party_id
order_tab.id -> book_tab.target_id
order_tab.id -> file_tab.file_id
order_tab.id -> mapping_tab.file_id
```

The seed intentionally starts each branch at `order_tab.id`, traverses
an ORM row object, then reads a field named `ID`. The relation algorithm keeps
the `id` taint and treats every `*_id` filter as an FK. That is the false
assumption: row reachability proves only that the row object is available; it
does not prove that its physical `ID` field supplied the WHERE value.

The actual relationships are the non-`ID` fields shown in `repro.go`;
`file_id` is a task identity, not a source-table physical ID.
