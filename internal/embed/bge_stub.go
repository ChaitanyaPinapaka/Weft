//go:build !ORT

package embed

import "errors"

// ErrNoORT is returned by NewLocal in default builds. Build with `-tags ORT`
// (and the onnxruntime/tokenizers bootstrap from the README) to get the real
// hugot-backed implementation.
var ErrNoORT = errors.New("embed: built without ORT support — rebuild with `make build-ort` and install libonnxruntime")

// NewLocal is a stub in default builds. See the package doc for the bootstrap.
func NewLocal(cacheDir string) (Embedder, error) {
	return nil, ErrNoORT
}
