// Package web 提供随 Limen 二进制发布的观测界面静态资源。
package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed assets/*
var embeddedAssets embed.FS

var assetFS, _ = fs.Sub(embeddedAssets, "assets")

// Handler 返回观测 UI 的静态资源处理器。
// Hash 路由不进入服务器路径；未知资源回退到 index.html，保证直接打开 UI 链接不会得到 404。
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/ui/")
		name = strings.TrimPrefix(path.Clean("/"+name), "/")
		if name == "" || !assetExists(name) {
			name = "index.html"
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		body, err := fs.ReadFile(assetFS, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write(body)
	})
}

// assetExists 判断嵌入式资源是否存在且不是目录。
func assetExists(name string) bool {
	info, err := fs.Stat(assetFS, name)
	return err == nil && !info.IsDir()
}
