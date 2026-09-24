//go:build webdist

package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// Dist returns the generated production browser bundle.
func Dist() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}
