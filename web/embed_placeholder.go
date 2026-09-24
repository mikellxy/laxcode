//go:build !webdist

// Package webui exposes the browser bundle to the standalone web server.
package webui

import (
	"embed"
	"io/fs"
)

// The default source build stays compilable without generated Vite output.
// The executable rejects this placeholder at startup; release builds use the
// webdist tag after generating web/dist.
//
//go:embed embed-placeholder.txt
var embedded embed.FS

// Dist returns the files embedded in the current build.
func Dist() (fs.FS, error) {
	return embedded, nil
}
