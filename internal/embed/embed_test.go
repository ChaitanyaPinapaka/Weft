package embed

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"scaled", []float32{2, 0, 0}, []float32{5, 0, 0}, 1},
		{"empty", []float32{}, []float32{}, 0},
		{"mismatched", []float32{1, 2}, []float32{1, 2, 3}, 0},
		{"zero", []float32{0, 0, 0}, []float32{1, 2, 3}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CosineSimilarity(tc.a, tc.b)
			if math.Abs(float64(got-tc.want)) > 1e-6 {
				t.Errorf("CosineSimilarity = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 3.14159, -2.71828, float32(math.MaxFloat32), float32(-math.MaxFloat32), float32(math.SmallestNonzeroFloat32)}
	blob := Encode(in)
	if len(blob) != 4*len(in) {
		t.Fatalf("Encode len = %d, want %d", len(blob), 4*len(in))
	}
	out, err := Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("Decode len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("idx %d: got %v, want %v", i, out[i], in[i])
		}
	}
}

func TestDecodeBadLength(t *testing.T) {
	if _, err := Decode([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for non-multiple-of-4 blob")
	}
}

func TestEncodeEmpty(t *testing.T) {
	blob := Encode(nil)
	if len(blob) != 0 {
		t.Fatalf("Encode(nil) len = %d, want 0", len(blob))
	}
	out, err := Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("Decode len = %d, want 0", len(out))
	}
}
