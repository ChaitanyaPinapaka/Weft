//go:build ORT

package embed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/backends"
	"github.com/knights-analytics/hugot/options"
	"github.com/knights-analytics/hugot/pipelines"
)

const (
	bgeRepo = "KnightsAnalytics/bge-small-en-v1.5"
	bgeDir  = "bge-small-en-v1.5"
)

type bgeSmall struct {
	mu       sync.Mutex
	ctx      context.Context
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
}

func NewBGESmall(cacheDir string) (Embedder, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("embed: create cache dir: %w", err)
	}

	ctx := context.Background()

	var session *hugot.Session
	var err error
	if libPath := findOnnxLib(); libPath != "" {
		session, err = hugot.NewORTSession(ctx, options.WithOnnxLibraryPath(libPath))
	} else {
		session, err = hugot.NewORTSession(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("embed: new ORT session (is libonnxruntime installed? set WEFT_ONNXRUNTIME_LIB): %w", err)
	}

	modelPath := filepath.Join(cacheDir, bgeDir)
	if _, statErr := os.Stat(filepath.Join(modelPath, "model.onnx")); os.IsNotExist(statErr) {
		downloaded, dErr := hugot.DownloadModel(ctx, bgeRepo, cacheDir, hugot.NewDownloadOptions())
		if dErr != nil {
			_ = session.Destroy()
			return nil, fmt.Errorf("embed: download %s: %w", bgeRepo, dErr)
		}
		modelPath = downloaded
	}

	cfg := hugot.FeatureExtractionConfig{
		ModelPath: modelPath,
		Name:      "weft-bge-small",
		Options: []backends.PipelineOption[*pipelines.FeatureExtractionPipeline]{
			pipelines.WithNormalization(),
		},
	}

	pipe, err := hugot.NewPipeline(session, cfg)
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("embed: build pipeline: %w", err)
	}

	return &bgeSmall{ctx: ctx, session: session, pipeline: pipe}, nil
}

func (b *bgeSmall) Embed(text string) ([]float32, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	out, err := b.pipeline.RunPipeline(b.ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: run pipeline: %w", err)
	}
	if len(out.Embeddings) != 1 {
		return nil, fmt.Errorf("embed: expected 1 embedding, got %d", len(out.Embeddings))
	}
	vec := out.Embeddings[0]
	if len(vec) != BGESmallDim {
		return nil, fmt.Errorf("embed: expected dim %d, got %d", BGESmallDim, len(vec))
	}
	return vec, nil
}

func (b *bgeSmall) Dim() int { return BGESmallDim }

func (b *bgeSmall) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session == nil {
		return nil
	}
	err := b.session.Destroy()
	b.session = nil
	b.pipeline = nil
	return err
}

// findOnnxLib probes the brew + standard install paths. Override with
// WEFT_ONNXRUNTIME_LIB pointing at the .dylib/.so file.
func findOnnxLib() string {
	if p := os.Getenv("WEFT_ONNXRUNTIME_LIB"); p != "" {
		return p
	}
	for _, p := range []string{
		"/opt/homebrew/lib/libonnxruntime.dylib",
		"/usr/local/lib/libonnxruntime.dylib",
		"/usr/lib/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
