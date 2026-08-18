// Package ast 实现调用图适配器（对应 TD.md 的 CodeGraph 适配器角色，置信度 0.8）。
// 基于 golang.org/x/tools/go/packages 的 AST + 类型信息，纯 Go 无外部进程：
//   - CALLS 边：调用者函数 → 被调用函数/方法（精确调用点）
//   - IMPORTS 边：包 → 直接依赖的项目内包
package ast

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/schaepher/codeintel/internal/domain"
	"go.uber.org/zap"
)

// Adapter 是基于 go/packages 的调用图分析器。
type Adapter struct {
	// 包路径 → packages.Package：跨包解析构造器函数体（链式调用
	// 返回接口时分析 return 的具体类型），Index 时填充
	pkgsByPath map[string]*packages.Package
	// HTTP 路由表（§18.7，routes.yaml 人工维护）：path → http_route 节点
	routes []routeEntry
	// 增量更新的变更文件（相对仓库根路径，§20.3 AST 文件级跳过）；
	// nil = 全量分析所有文件
	changedFiles map[string]bool
}

// SetChangedFiles 限定增量分析的文件集合（orchestrator 增量构建注入，
// 见 field_trace.md §20.3）；传 nil 恢复全量。文件为相对仓库根的路径。
func (a *Adapter) SetChangedFiles(files []string) {
	if files == nil {
		a.changedFiles = nil
		return
	}
	a.changedFiles = make(map[string]bool, len(files))
	for _, f := range files {
		a.changedFiles[filepath.Clean(f)] = true
	}
}

// routeEntry 构建期路由表条目。
type routeEntry struct {
	path   string
	nodeID domain.CanonicalID
}

var _ domain.IndexerPort = (*Adapter)(nil)

// Name 实现 IndexerPort。
func (a *Adapter) Name() string {
	logger := zap.L()
	logger.Debug("enter (Adapter).Name")
	defer logger.Debug("exit (Adapter).Name")
	return "codegraph"
}

// Index 加载仓库全部包并产出 CALLS / IMPORTS 边。

// collectRegisterServers 遍历项目内包，收集定义在 .pb.go 中、函数名匹配
// RegisterXxxServer 的注册函数（key: "pkgPath:funcName"）。

// collectNewClients 遍历 .pb.go 收集 protoc 生成的 NewXxxClient 客户端
// 构造器（field_trace.md §18.2；key: "pkgPath:funcName" → 服务名）。

// newClientService 从 NewXxxClient 函数名提取服务名（NewGreeterClient →
// Greeter）；非客户端构造器返回 ok=false。

// clientTypeService 从客户端接口类型名提取服务名（GreeterClient → Greeter；
// 形参类型识别，§21.1——与构造器名 NewXxxClient 不同，无 New 前缀）。

// registerService 从 RegisterXxxServer 函数名提取服务名（RegisterGreeterServer
// → Greeter）。

// processFile 遍历单个 AST：定位每个调用点，连接调用者与被调用者。
func (a *Adapter) processFile(repo *domain.Repository, pkg *packages.Package, f *ast.File, emit domain.EmitFunc,
	serviceFlags map[domain.CanonicalID]map[string]bool, registerServers map[string]bool,
	newClients map[string]string) error {
	logger := zap.L()
	logger.Debug("enter (Adapter).processFile")
	defer logger.Debug("exit (Adapter).processFile")
	filePath := relPath(repo.Path, pkg.Fset.PositionFor(f.Pos(), false).Filename)
	if filePath == "" {
		return nil
	}
	// 增量更新（§20.3）：只分析变更文件，未变更文件跳过（节点保留在库中）
	if a.changedFiles != nil && !a.changedFiles[filepath.Clean(filePath)] {
		return nil
	}

	if err := a.emitMethodReceiver(repo, pkg, f, emit); err != nil {
		return err
	}
	if err := a.emitStructFields(repo, pkg, f, emit); err != nil {
		return err
	}

	// 遍历上下文（filectx.go：闭包状态打包 + visit/emitCall 等方法——
	// 2026-08-17 从本函数闭包拆分，逻辑逐行一致）
	ctx := &fileCtx{
		a:               a,
		repo:            repo,
		pkg:             pkg,
		f:               f,
		emit:            emit,
		serviceFlags:    serviceFlags,
		registerServers: registerServers,
		newClients:      newClients,
		// 对象流追踪：变量名 → 对象 ID（同一函数内）；表达式 Pos → 对象 ID（去重）
		objVars:  map[string]domain.CanonicalID{},
		objCache: map[token.Pos]domain.CanonicalID{},
		// gRPC 客户端对象（§18.2）：变量名 → 服务名（NewXxxClient 返回值，函数内追踪）
		grpcClients: map[string]string{},
		// 手写 client（§18.6）：同函数内 `method := "/pkg.Svc/M"` 一层赋值链
		methodVars: map[string]string{},
		// HTTP req 变量（P1-3）：req 名 → URL（req := http.NewRequest(...) 赋值追踪，
		// 供 client.Do(req) 消费防重复判断）
		reqVars: map[string]string{},
		reqMethods: map[string]string{},
		// 本函数已 emit http_call 的 URL（NewRequest 建边后，Do(req) 不重复）
		httpURLsSeen: map[string]bool{},
		// 函数值变量（P2-1）：f := g / f := obj.Method → f 名 → *types.Func
		// （f() 调用点 callee 解析失败时查此表，unused 误报收敛）
		varFuncs: map[string]*types.Func{},
	}
	ast.Inspect(f, ctx.visit)
	return nil
}

// funcValueRef 解析函数值引用（P2-1）：g（函数名 Ident）或 obj.M
// （方法值 SelectorExpr）；非函数值返回 ok=false。

// argFuncRef 将调用参数解析为函数引用（作为参数传入的回调）：
//
//	Ident（home）                          → 具名函数
//	SelectorExpr（s.PageHome / pkg.F）     → 方法/包函数引用
//	CallExpr（http.HandlerFunc(home)）     → 解包 HandlerFunc 包装
//
// 非函数引用（变量/字面量/匿名函数）返回 nil。

// markHTTPHandlers 标记实现 net/http.Handler 接口（ServeHTTP 方法）的项目内
// 类型，作为 HTTP 服务入口（serves_http）。

// emitStructFields 为文件内每个 struct 类型声明写入字段列表
// （properties.fields = [{"name","type"}...]，类型用 go/types 相对路径
// 字符串如 *domain.BuildMeta）。信息栏据此以表格展示字段。

// embeddedTypeName 匿名嵌入字段的显示名（解引用指针取具名类型名）。

// emitMethodReceiver 为文件内每个带 receiver 的方法声明建立 has_method 边
// （方法线：接收者类型 → 方法）。接收者类型节点如不存在则创建（与
// createObject 相同的轻量节点模式，SCIP 已建则 UPSERT 合并属性）。
// 展开接收者（struct）节点时前端即可连线到它的方法们。

// createObject 将初始化表达式（&T{} / T{} / new(T)）解析为 struct 类型：
//   - initializes 边：初始化者函数 → struct 类型（对象合并到类型节点，
//     不建独立 object 节点，避免同一类型的实例在图里分开）
//
// 返回类型 ID（作为实例的代表）；非 struct 初始化 / 外部类型 / 无 caller
// 时返回 false。

// isRegisterServerName 判断函数名是否匹配 protoc 生成惯例 RegisterXxxServer。

// handlerFuncNode 提取 http.Handle/HandleFunc 的 handler 参数（第二个参数），
// 支持形态：
//
//	http.Handle("/", myHandler)          // 变量（具名函数）
//	http.Handle("/", http.HandlerFunc(f)) // HandlerFunc 包装
//	http.HandleFunc("/", home)            // 具名函数
//
// 返回标记 serves_http 的节点；匿名函数（FuncLit）与外部函数返回 nil。

// serviceImplNode 提取 RegisterXxxServer 调用的第二个参数（服务实现），
// 生成标记 serves_grpc 的节点（作为顶层服务入口）。参数形态支持：
//
//	pb.RegisterGreeterServer(s, &greeterImpl{})   // 复合字面量
//	pb.RegisterGreeterServer(s, newGreeterServer()) // 构造函数
//	pb.RegisterGreeterServer(s, impl)               // 变量
//
// 返回 nil 表示无法解析为项目内类型。

// resolveCallee 将调用表达式解析为被调用的 *types.Func。

// findCallerDecl 返回调用点所属的最近函数声明。

// isArgCall 判断 call 是否处于另一个调用点的参数位置（嵌套调用，
// 如 A(B(C())) 里的 B(C())）。参数位置的调用由外层处理为"持有返回
// 参数"链，不建 calls。

// handleNestedArg 处理参数位置的嵌套调用：接收者持有返回参数。
// A(B(C())) → A→B、B→C（passes_result），参数位置的调用不建 calls。

// concreteMethodFor 解析链式调用接收者表达式的实际方法目标：
//   - callee 是接口方法时，分析接收者表达式（如 NewService().DoSth() 的
//     NewService()）的实际返回类型——函数声明返回接口但 return 具体类型
//     （return impl{}）→ 解析到该具体类型的同名实现方法（main → (impl).DoSth）
//   - 无法确定（跨包/多态）→ 回退指向接口类型节点（main → Service）
//
// 返回 (targetID, targetKind, node)；targetID 为空表示放弃建边。

// concreteReturnType 解析表达式的"实际返回类型"：若声明返回类型是接口
// （如 NewService() Service），分析函数体的 return 语句找具体类型
// （return impl{} → impl）；无法确定时返回静态类型。

// findFuncDecl 通过位置查找 *types.Func 对应的 FuncDecl（同包内）。

// derefNamed 解引用指针后取具名类型。

// isInterfaceType 判断具名类型是否为接口。

// isInterfaceMethod 判断 *types.Func 是否为接口方法（接收者类型是接口）。
// 接口方法不作为独立节点：SCIP 适配器不建、AST 适配器调用处也不建。

// fnID 计算函数/方法的 canonical ID 与领域种类。
// 返回值 (id, kind, ok)。

// nodeFor 为函数/方法生成轻量节点（ID 与 SCIP 一致，行号/文件来自位置信息，
// signature 由 go/types 生成，与 SCIP 节点通过 properties 合并）。

// ensurePackageNode 保障包节点存在（SCIP 已建则跳过）。
// 注意：加载失败/无源码的包（pkg.Syntax 为空，如编译错误的包或测试变体）
// 仍会创建节点，只是不带 file_path，避免 index out of range。

// relPath 将绝对路径转为仓库相对路径；仓库外文件返回空串。

// isInModule 判断包路径是否属于任一被索引 module（自身或子包；P2-3
// 多 go.mod——任一 module 前缀匹配即项目内）。

// unquoteMethodPath 校验并还原字符串字面量为 gRPC 方法路径
// （/pkg.Service/Method 格式）；非路径返回空。

// isGrpcMethodPath gRPC 方法路径格式："/<包.服务>/<方法>"。

// directPathIdx 方法路径在实参中的位置：方法调用 Invoke 第 2 参 /
// NewStream 第 3 参；顶层 grpc.Invoke 第 3 参。

// directMethodPath 从 Invoke/NewStream 调用提取 gRPC 方法路径
// （§18.6）：字面量直接取；Ident 经一层常量传播（同函数 methodVars
// 或 types.Const）。非路径形态返回空。

// pkgOfID 从 canonical ID 提取包路径（symbol:go:<pkg>:<name>）。

// extractStringArg 从实参提取字符串值（字面量 / 同函数 methodVars /
// types.Const / 常量字符串拼接 P1-3），非字符串返回空。

// httpURLString 从 http.Get/NewRequest/NewRequestWithContext 调用提取
// URL 字符串（§18.7，P1-3 补 NewRequestWithContext——与 NewRequest 同
// 参数位，ctx 在 0 位）：Get 第 1 参 / NewRequest 第 2 参 /
// NewRequestWithContext 第 3 参；URL 须含 scheme 或以 / 开头（防
// 误伤同名方法）。动态变量返回 ok=false（盲区）。
