//go:build !ORT

package embed

import "errors"

// ErrNoORT is returned by NewBGESmall in default builds. Build with
// `-tags ORT` (and the onnxruntime/tokenizers bootstrap from the README)
// to get the real bge-small implementation.
var ErrNoORT = errors.New("embed: built without ORT support — rebuild with `make build-ort` and install libonnxruntime")

// NewBGESmall is a stub in default builds. See the package doc for the bootstrap.
func NewBGESmall(cacheDir string) (Embedder, error) {
	return nil, ErrNoORT
}
