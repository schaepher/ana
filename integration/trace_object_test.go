//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/schaepher/codeintel/internal/infrastructure/sqlite"
)

// TestLocalObjectTraceSelfContained：⑭ 局部对象追踪——DAO 返回对象 →
// 局部变量 → helper 传参（起点须纳入与目标字段同类型的 local/phi 值）。

// TestInterfaceCallTraceSelfContained：⑮ 接口动态派发——接口方法调用
// 传参（无静态 callee）须经候选实现建立 argument 边，追踪进入实现。

// TestGlobalObjectTraceSelfContained：举一反三 A1——全局变量对象传参
// （var g Record; helper(&g)）trace-forward 起点（global 值来源格）。

// TestPhiObjectTraceSelfContained：举一反三 A2——phi 值传参
// （if 分支各自赋值后传 helper）。

// TestFuncValueCallTraceSelfContained：举一反三 B4——函数值调用
// （f := getHandler(); f(record)——f 来自返回值，调用点无静态 callee）。

// TestInterfaceReturnTraceSelfContained：举一反三——动态调用返回值贯通：
// err := w.Write(&Record{})——value-trace 从返回值节点应连到候选实现的
// Return 值（⑮ 只建了 argument，returns 边待验证）。
