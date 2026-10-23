// Package web embeds the compiled admin interface (web/dist).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func Dist() fs.FS {
	f, _ := fs.Sub(dist, "dist")
	return f
}
