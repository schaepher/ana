//go:build integration

package integration

import (
	"path/filepath"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestORMChainDAOSelfContained：⑦ 链式 ORM 自包含用例——自定义 DAO 封装
// （Model(&X{主键}).Where(...).Update("col", v)）经 field-summary.yaml 的
// orm_write 条目映射为 表.列 虚拟节点（不依赖真实 gorm 模块）。

// TestORMChainFormsSelfContained：⑪ ORM 链式形态覆盖——结构体 Updates
// 链式（Model().Where().Updates(&Y{})）与无 Model 的字符串列名 Update
// （Where().Update("col", v)——表名无法溯源时跳过而非报错）。

// TestORMUpdateRecordScopeSelfContained：⑪ ORM——session.Where(...)
// .Update(record, scope) 对象实参形态：record 变量 → 表.列 节点 +
// 对象兜底持久化边（summary_io）。

// TestGofRepositoryInterfaceSelfContained：Q156 集成固化——gof 框架
// fw.Repository[M] 接口摘要（真实外部依赖）：init 前置 go mod tidy
// （模块缓存有 gof，离线可解析），断言表.列虚拟节点生成（表名取实体
// TableName）+ where filter 列。
