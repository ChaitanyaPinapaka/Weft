// Package web bundles the editor's static assets into the binary.
package web

import "embed"

//go:embed index.html editor.js style.css viewer.html viewer.js viewer.css graph.html graph.js graph.css tune.html tune.js tune.css tasks.html tasks.js
var FS embed.FS
