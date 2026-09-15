// Package web embeds the built frontend so the server ships as a single
// binary with no static file directory to deploy alongside it.
package web

import (
	"embed"
	"io/fs"
)

// dist holds the Vite build output. The placeholder index.html committed at
// web/dist keeps this embed valid before the frontend has ever been built.
//
//go:embed all:dist
var dist embed.FS

// Dist returns the built frontend rooted at the directory containing
// index.html.
func Dist() (fs.FS, error) { return fs.Sub(dist, "dist") }
