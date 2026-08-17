//go:build integration

package integration

// TestFieldPrecisionSelfContained：⑥ 字段精度自包含用例（不依赖 radar）——
// 对象/SSA 值锚点不再扇出全部字段读；拷贝链（dest.ID = src.ID）经
// 值来源跳板保持闭合。

// TestCrossFunctionTraceSelfContained：⑩ 跨函数追踪复现——多种调用方
// 形态下 trace-forward 应连到被调函数内的实际字段写入：
//
//	A. 调用方参数传递（run2(c *Cfg) → fill(c)）
//	B. 调用方局部变量传递（var c Cfg; fill(&c)，调用方无字段访问无参数）
//	C. 调用方字段读后传参（s.c → fill）

// TestCrossFunctionNoiseSelfContained：⑩ 跨函数追踪噪音复现——A 传
// *Record 给 B，B 写 record.FinalFee 且读多个无关字段。trace-forward
// A 的 FinalFee 下游：应连到 B 的 record.FinalFee 写入，且不含
// Metadata/Status 等无关字段读（同名跳板过滤）。

// TestDeepChainNoIndirectWriteSelfContained：S1 集成固化——三层调用链
// （a→b→c）中 c 写自己内部对象（与实参无别名）时，a 不得有 T.A 间接写
// （别名排除须经跨函数参数 may 传播稳定生效，不依赖调用点处理顺序）。
