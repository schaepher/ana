package ssa

import (
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/go/ssa"
	"gopkg.in/yaml.v3"
)

// loadSummaries 加载内置 + 用户摘要（用户覆盖同名内置），返回 函数全路径 → spec。
// YAML 解析失败/重复定义时跳过对应条目并输出警告（构建降级，不中止，Q59）。
func loadSummaries(repoPath string) (map[string]summarySpec, []string) {
	logger := zap.L()
	logger.Debug("enter loadSummaries")
	defer logger.Debug("exit loadSummaries")
	specs := builtinSummaries()
	var warnings []string

	data, err := os.ReadFile(filepath.Join(repoPath, "field-summary.yaml"))
	if err != nil {
		if !os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("读取 field-summary.yaml: %v", err))
		}
		return specs, warnings
	}
	var uf userSummaryFile
	if err := yaml.Unmarshal(data, &uf); err != nil {
		warnings = append(warnings, fmt.Sprintf("field-summary.yaml 解析失败，已忽略: %v", err))
		return specs, warnings
	}

	seenUser := map[string]bool{}
	for _, s := range uf.Summaries {
		if s.Func == "" && s.Iface == "" {
			warnings = append(warnings, "field-summary.yaml: 存在缺少 func/iface 的条目，已跳过")
			continue
		}
		key := s.Func
		if s.Iface != "" {
			if s.Method == "" {
				warnings = append(warnings, fmt.Sprintf("field-summary.yaml: %s 缺少 method，已跳过", s.Iface))
				continue
			}
			key = "iface:" + s.Iface + "." + s.Method
		}
		if seenUser[key] {
			warnings = append(warnings, fmt.Sprintf("field-summary.yaml: %s 重复定义，已忽略", key))
			continue
		}
		seenUser[key] = true
		specs[key] = summarySpec{
			Func:       s.Func,
			Interface:  s.Iface,
			Method:     s.Method,
			Kind:       s.Kind,
			Type:       s.Type,
			ChainTable: s.ChainTable,
			WhereArg:   s.WhereArg,
			ObjArg:     s.ObjArg,
			IDArg:      s.IDArg,
			SQLWrite:   s.SQLWrite,
			Reads:      s.Reads,
			Writes:     s.Writes,
			ParamIndex: s.ParamIndex,
			ORMWrite:   s.ORMWrite,
			ORMRead:    s.ORMRead,
		}
	}
	return specs, warnings
}

// builtinSummaries 内置摘要（field_trace.md §7.3）。
// context.Context 为透明传递，无条目。
func builtinSummaries() map[string]summarySpec {
	specs := map[string]summarySpec{
		"encoding/json.Unmarshal": {
			Func: "encoding/json.Unmarshal", ParamIndex: 1,
			WritesAll: true,
		},
		"fmt.Printf": {
			Func: "fmt.Printf", ParamIndex: 1,
			ReadArgsAll: true,
		},
		"database/sql.(Rows).Scan": {
			Func: "database/sql.(Rows).Scan", ParamIndex: 1,
			WritesAll: true, ScanOut: true,
		},
		"database/sql.(Row).Scan": {
			Func: "database/sql.(Row).Scan", ParamIndex: 1,
			WritesAll: true, ScanOut: true,
		},
		"net/http.(Request).ParseForm": {
			Func: "net/http.(Request).ParseForm", ParamIndex: 0,
			Writes: []string{"Form"},
		},
		"net/http.(Request).FormValue": {
			Func: "net/http.(Request).FormValue", ParamIndex: 0,
			Reads: []string{"Form"},
		},
	}

	for _, fn := range []string{"Exec", "Query", "QueryRow", "Prepare"} {
		for _, recv := range []string{"(DB)", "(Tx)"} {
			specs["database/sql."+recv+"."+fn] = summarySpec{
				Func: "database/sql." + recv + "." + fn, ParamIndex: 1,
				SQLStmt: true, SQLWrite: fn == "Exec",
			}
		}
	}

	for _, fn := range []string{
		"prometheus.(Counter).Inc", "prometheus.(Counter).Add",
		"prometheus.(CounterVec).WithLabelValues",
		"prometheus.(Histogram).Observe",
		"prometheus.(Gauge).Set", "prometheus.(Gauge).Inc", "prometheus.(Gauge).Dec",
		"prometheus.(Summary).Observe",
	} {
		specs["github.com/prometheus/client_golang/"+fn] = summarySpec{
			Func: "github.com/prometheus/client_golang/" + fn, ParamIndex: 0,
			ReadArgsAll: true,
		}
	}

	for k, s := range gormSummarySpecs() {
		specs[k] = s
	}
	for k, s := range xormSummarySpecs() {
		specs[k] = s
	}
	specs["database/sql.(DB).Begin"] = summarySpec{Func: "database/sql.(DB).Begin", TxBoundary: "begin"}
	specs["database/sql.(Tx).Commit"] = summarySpec{Func: "database/sql.(Tx).Commit", TxBoundary: "commit"}
	specs["database/sql.(Tx).Rollback"] = summarySpec{Func: "database/sql.(Tx).Rollback", TxBoundary: "rollback"}

	for _, repoPath := range []string{
		"github.com/ixre/gof/ext/fw.Repository",
		"github.com/ixre/go2o/pkg/infra/fw.Repository",
	} {
		gofIface := "iface:" + repoPath + "."
		for _, fn := range []string{"Save", "Update", "Delete"} {
			specs[gofIface+fn] = summarySpec{Interface: repoPath,
				Method: fn, Kind: "write", ObjArg: 0}
		}
		for _, fn := range []string{"FindBy", "FindList"} {
			whereArg := 0
			if fn == "FindList" {
				whereArg = 1
			}
			specs[gofIface+fn] = summarySpec{Interface: repoPath,
				Method: fn, Kind: "read", WhereArg: whereArg}
		}
		specs[gofIface+"Get"] = summarySpec{Interface: repoPath,
			Method: "Get", Kind: "read", IDArg: 0}
		for _, fn := range []string{"Count", "DeleteBy"} {
			specs[gofIface+fn] = summarySpec{Interface: repoPath,
				Method: fn, Kind: "filter", WhereArg: 0}
		}
	}

	for _, fn := range []string{"ExecScalar", "Query", "QueryRow"} {
		specs["iface:github.com/ixre/gof/db.Connector."+fn] = summarySpec{
			Interface: "github.com/ixre/gof/db.Connector",
			Method:    fn, Kind: "sql", WhereArg: 0, SQLWrite: false}
	}
	specs["iface:github.com/ixre/gof/db.Connector.ExecNonQuery"] = summarySpec{
		Interface: "github.com/ixre/gof/db.Connector",
		Method:    "ExecNonQuery", Kind: "sql", WhereArg: 0, SQLWrite: true}

	specs["github.com/ixre/gof/db/orm.Save"] = summarySpec{
		Func: "github.com/ixre/gof/db/orm.Save", ParamIndex: 1, ORMWrite: true}

	specs["iface:github.com/ixre/gof/db/orm.Orm.Get"] = summarySpec{
		Interface: "github.com/ixre/gof/db/orm.Orm",
		Method:    "Get", Kind: "read", ObjArg: 1, IDArg: 0}

	// Q205 gof orm.Orm 字符串 where 形态（go2o p.o.Select/GetBy/Delete 等
	// 封装调用——此前无 spec，where 串列名不产 filter 节点，键关联漏报）：
	//   - Select(dst, where, args...) / GetBy(entity, where, args...)：读出
	//     （dst/entity 推断表名 + 全列 read）+ where 串列名 → filter 节点
	//   - Delete(entity, where, args...)：实体类型全列 write + where filter
	//   - SelectByQuery/GetByQuery(dst, sql, args...)：直写 SQL 同 database/sql
	//   - Save(primary, entity)：方法形态与包级 orm.Save 同语义（write）
	ormIface := "iface:github.com/ixre/gof/db/orm.Orm."
	for _, fn := range []string{"Select", "GetBy"} {
		specs[ormIface+fn] = summarySpec{
			Interface: "github.com/ixre/gof/db/orm.Orm",
			Method:    fn, Kind: "read", ObjArg: 0, WhereArg: 1}
	}
	specs[ormIface+"Delete"] = summarySpec{
		Interface: "github.com/ixre/gof/db/orm.Orm",
		Method:    "Delete", Kind: "write", ObjArg: 0, WhereArg: 1}
	for _, fn := range []string{"SelectByQuery", "GetByQuery"} {
		specs[ormIface+fn] = summarySpec{
			Interface: "github.com/ixre/gof/db/orm.Orm",
			Method:    fn, Kind: "sql", WhereArg: 1}
	}
	specs[ormIface+"Save"] = summarySpec{
		Interface: "github.com/ixre/gof/db/orm.Orm",
		Method:    "Save", Kind: "write", ObjArg: 1}
	return specs
}

// summaryKey 生成函数全路径摘要键：pkg.Func / pkg.(T).Method。
func summaryKey(fn *ssa.Function) string {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return ""
	}
	obj, ok := fn.Object().(*types.Func)
	if !ok || obj == nil {
		return ""
	}
	path := fn.Pkg.Pkg.Path()
	sig, _ := obj.Type().(*types.Signature)
	if sig != nil && sig.Recv() != nil {
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			return path + ".(" + named.Obj().Name() + ")." + fn.Name()
		}
	}
	return path + "." + fn.Name()
}

// lastPathSeg 取路径最后一段（instance_path 拼接用）。
func lastPathSeg(p string) string {
	if i := strings.LastIndex(p, "."); i >= 0 {
		return p[i+1:]
	}
	return p
}
