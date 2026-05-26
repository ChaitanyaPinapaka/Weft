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
	// modelRepo is the HuggingFace repo we pull. Switched off bge-small to
	// MiniLM because the KnightsAnalytics bge-small fork is gated and BAAI's
	// official BGE doesn't ship in hugot's expected layout. MiniLM is hugot's
	// reference embedding model: public, 384-dim, ~22 MB.
	modelRepo = "KnightsAnalytics/all-MiniLM-L6-v2"
	modelDir  = "all-MiniLM-L6-v2"
)

type localEmbedder struct {
	mu       sync.Mutex
	ctx      context.Context
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
}

func NewLocal(cacheDir string) (Embedder, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("embed: create cache dir: %w", err)
	}

	ctx := context.Background()

	var session *hugot.Session
	var err error
	if libDir := findOnnxLibDir(); libDir != "" {
		// Misleadingly named: WithOnnxLibraryPath wants the *directory*
		// containing libonnxruntime.{so,dylib}, not the file itself.
		session, err = hugot.NewORTSession(ctx, options.WithOnnxLibraryPath(libDir))
	} else {
		session, err = hugot.NewORTSession(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("embed: new ORT session (is libonnxruntime installed? set WEFT_ONNXRUNTIME_LIB): %w", err)
	}

	modelPath := filepath.Join(cacheDir, modelDir)
	if _, statErr := os.Stat(filepath.Join(modelPath, "model.onnx")); os.IsNotExist(statErr) {
		downloaded, dErr := hugot.DownloadModel(ctx, modelRepo, cacheDir, hugot.NewDownloadOptions())
		if dErr != nil {
			_ = session.Destroy()
			return nil, fmt.Errorf("embed: download %s: %w", modelRepo, dErr)
		}
		modelPath = downloaded
	}

	cfg := hugot.FeatureExtractionConfig{
		ModelPath: modelPath,
		Name:      "weft-embedder",
		Options: []backends.PipelineOption[*pipelines.FeatureExtractionPipeline]{
			pipelines.WithNormalization(),
		},
	}

	pipe, err := hugot.NewPipeline(session, cfg)
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("embed: build pipeline: %w", err)
	}

	return &localEmbedder{ctx: ctx, session: session, pipeline: pipe}, nil
}

func (b *localEmbedder) Embed(text string) ([]float32, error) {
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
	if len(vec) != EmbeddingDim {
		return nil, fmt.Errorf("embed: expected dim %d, got %d", EmbeddingDim, len(vec))
	}
	return vec, nil
}

func (b *localEmbedder) Dim() int { return EmbeddingDim }

func (b *localEmbedder) Close() error {
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

// findOnnxLibDir probes brew + standard install paths and returns the
// *directory* containing libonnxruntime (what hugot expects). Override with
// WEFT_ONNXRUNTIME_LIB pointing at either the file or the directory.
func findOnnxLibDir() string {
	if p := os.Getenv("WEFT_ONNXRUNTIME_LIB"); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return filepath.Dir(p)
		}
		return p
	}
	for _, p := range []string{
		"/opt/homebrew/lib/libonnxruntime.dylib",
		"/usr/local/lib/libonnxruntime.dylib",
		"/usr/lib/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so",
	} {
		if _, err := os.Stat(p); err == nil {
			return filepath.Dir(p)
		}
	}
	return ""
}
