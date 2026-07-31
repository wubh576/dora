//go:build !production

package webassets

import "io/fs"

// Files 在开发构建中为空，页面继续由 Vite 提供。
func Files() fs.FS {
	return nil
}
