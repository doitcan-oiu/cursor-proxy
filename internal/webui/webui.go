// Package webui 把构建好的 Vue 管理界面内嵌进二进制并对外托管。
//
// 前端源码在 go-script/webui，`npm run build` 产物落在 webui/dist，
// 由 go:embed 打包，因此发布时仍然只有一个可执行文件。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Available 表示是否内嵌了真实的前端产物。
// 判据是构建产物目录 assets 存在——未跑过 npm run build 时只有占位 index.html。
func Available() bool {
	entries, err := fs.ReadDir(dist(), "assets")
	return err == nil && len(entries) > 0
}

func dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return embedded
	}
	return sub
}

// Handler 返回 SPA 处理器：静态资源直出，未知路径回落到 index.html
// 交给前端路由（history 模式）处理。
func Handler() http.Handler {
	root := dist()
	files := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "" || clean == "." {
			serveIndex(w, r, root)
			return
		}
		f, err := root.Open(clean)
		if err != nil {
			serveIndex(w, r, root)
			return
		}
		info, statErr := f.Stat()
		f.Close()
		if statErr != nil || info.IsDir() {
			serveIndex(w, r, root)
			return
		}
		// 带内容哈希的资源可长期缓存；index.html 不缓存。
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "web ui not built: run `npm --prefix webui run build`", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", zeroTime, strings.NewReader(string(data)))
}
