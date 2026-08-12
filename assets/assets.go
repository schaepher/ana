// Package assets 嵌入前端静态资源（AntV G6 依赖探索页面）。
package assets

import "embed"

// WebFS 前端静态文件（web/ 目录）。
//
//go:embed web
var WebFS embed.FS
