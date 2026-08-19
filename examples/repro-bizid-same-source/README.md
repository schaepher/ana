# biz_id same-source dual-write reproducer (Q225)

`bizid_same_source.go` demonstrates the business-id same-source dual-write
scenario: a business id is created first, inserted together with its row
into `a_tab` (`a.BizID = bizID; Insert(a)`), and later the same value is
updated into `b_tab` (`b.BizID = bizID; Update(b)`).

Expected relations inference: `a_tab.biz_id -> b_tab.biz_id` as a
same-source write relation (`write` type — the same value flow writes two
tables' columns).

## Variants

| Variant | Code | Expected |
|---|---|---|
| Same-function | `SyncBizSameFunc` | `a_tab.biz_id → b_tab.biz_id [write]` (always worked) |
| Cross-function | `SyncBizCrossFunc` + `InsertATab`/`UpdateBTab` | `a_tab.biz_id → b_tab.biz_id [write]` (Q225 fix: object → field-write carries field-name-matched taint; exact-name dual write passes the Q202c cross-function gate) |

## Run

Build in the repository root, then index the fixture and query relations:

```sh
cd /path/to/codeintel
go build -o /tmp/codeintel-repro ./cmd/codeintel
/tmp/codeintel-repro reindex --repo examples/repro-bizid-same-source
/tmp/codeintel-repro query relations a_tab --repo examples/repro-bizid-same-source
```

The query must include (same-function and cross-function variants both):

```text
a_tab.biz_id -> b_tab.biz_id  [write]
```

## Known output noise

The same-function variant (`SyncBizSameFunc`) may also emit
`a_tab.biz_id → b_tab.id [write]` — the ORM object-expansion writes every
exported field column of the object, and the object carries the `biz_id`
taint. Same-function write relations are intentionally loose (the strict
cross-function gate Q202c only guards crossed chains); `id`-suffixed target
columns are not aggregated by Q202b. The cross-function variant does *not*
emit this line — the unit test asserts `b_tab.id` is dropped there.

## Non-requirements

This fixture is the *positive* case: it must be recognized. The negative
case (false same-source, e.g. `role.id → res_id` where the value merely
shares a function context) is covered by unit tests
(`relations_sql_taint_test.go`, Q202 regression) — the cross-function
write gate keeps requiring either an FK-shaped target column or an
exact-name taint match.
