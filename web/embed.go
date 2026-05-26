// Package web bundles the editor's static assets into the binary.
package web

import "embed"

//go:embed index.html editor.js style.css viewer.html viewer.js viewer.css
var FS embed.FS
