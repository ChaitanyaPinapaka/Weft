// Package embed produces dense vector embeddings of note text and serializes
// them for storage in the index's `embeddings` BLOB column.
//
// The real bge-small-en-v1.5 model runs through hugot/onnxruntime and is gated
// behind the `ORT` build tag (see bge_ort.go). Default builds get a stub
// (bge_stub.go) so the codebase stays compilable without libonnxruntime.
package embed

import (
	"encoding/binary"
	"fmt"
	"math"
)

// BGESmallDim is the output dimensionality of bge-small-en-v1.5.
const BGESmallDim = 384

type Embedder interface {
	Embed(text string) ([]float32, error)
	Dim() int
	Close() error
}

// Encode packs a vector as little-endian float32. len(blob) == 4 * len(v).
func Encode(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// Decode is the inverse of Encode. Errors if len(blob) is not a multiple of 4.
func Decode(blob []byte) ([]float32, error) {
	if len(blob)%4 != 0 {
		return nil, fmt.Errorf("embed: blob length %d not a multiple of 4", len(blob))
	}
	v := make([]float32, len(blob)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return v, nil
}

// CosineSimilarity returns the cosine similarity of a and b in [-1, 1].
// Returns 0 for mismatched lengths, empty inputs, or zero-norm vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
