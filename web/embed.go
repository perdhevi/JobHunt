// Package web embeds the built React app. Run scripts/build.ps1, or
// "npm run build" in this folder, before "go build".
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built UI rooted at dist.
func FS() (fs.FS, error) { return fs.Sub(dist, "dist") }
