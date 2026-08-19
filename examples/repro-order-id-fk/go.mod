module github.com/schaepher/codeintel/examples/repro-clearing-order-id-fk

go 1.26

require (
	github.com/schaepher/codeintel v0.0.0
	xorm.io/xorm v0.0.0
)

require (
	github.com/mattn/go-sqlite3 v1.14.24 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	golang.org/x/mod v0.21.0 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/tools v0.26.0 // indirect
)

replace github.com/schaepher/codeintel => ../..

replace xorm.io/xorm => ./xorm
