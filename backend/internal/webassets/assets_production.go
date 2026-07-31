//go:build production

package webassets

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embeddedFiles embed.FS

func Files() fs.FS {
	files, err := fs.Sub(embeddedFiles, "dist")
	if err != nil {
		panic("读取嵌入式前端资源失败: " + err.Error())
	}
	return files
}
