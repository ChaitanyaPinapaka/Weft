//go:build tools

// Package tools pins build-time tool dependencies that no runtime package
// imports, so `go mod tidy` keeps them in go.mod. golang.org/x/mobile is needed
// by `make ios` (gomobile bind of ./mobile into the iOS xcframework). This file
// is gated behind the `tools` build tag and never compiled into any binary.
package tools

import _ "golang.org/x/mobile/bind"
