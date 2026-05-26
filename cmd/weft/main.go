package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"weft/internal/clip"
	"weft/internal/imports/applenotes"
	"weft/internal/imports/bookmarks"
	"weft/internal/imports/markdown"
	"weft/internal/imports/notion"
	"weft/internal/index"
	"weft/internal/mcp"
	"weft/internal/server"
	"weft/internal/vault"
)

const usage = `Weft — HTML vault with brain memory.

Usage:
  weft serve <vault-path>    start the daemon and open browser
  weft capture "<text>"      quick-capture to today's daily note
  weft clip <url>            clip a URL to vault as clips/YYYY-MM-DD-slug.html
  weft mcp <vault-path>      run the MCP server on stdio (for Claude Code)
  weft import <kind> <src>   import notes from another format into the vault
                             kinds: markdown notion bookmarks apple-notes
                             flags: --force (overwrite existing notes)
                                    -v <vault>
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: weft serve <vault-path>")
			os.Exit(1)
		}
		if err := server.Run(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "capture":
		if err := runCapture(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "clip":
		if err := runClip(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "mcp":
		if err := runMCP(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	case "import":
		if err := runImport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

// runCapture parses `weft capture "<text>" [-v <path>]` and appends to today's
// daily note. The text is the only positional arg; -v overrides the vault path.
// Hand-rolled parser keeps the surface area small and the ordering loose so
// `weft capture "x" -v /tmp` and `weft capture -v /tmp "x"` both work.
func runCapture(args []string) error {
	vaultPath := ""
	var positional []string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-v", "--vault":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", a)
			}
			vaultPath = args[i+1]
			i++
		default:
			positional = append(positional, a)
		}
	}

	if len(positional) == 0 {
		return fmt.Errorf("usage: weft capture \"<text>\" [-v <vault>]")
	}
	if len(positional) > 1 {
		return fmt.Errorf("capture takes a single quoted text argument, got %d", len(positional))
	}
	text := positional[0]

	vaultPath = resolveVault(vaultPath)
	v, err := vault.New(vaultPath)
	if err != nil {
		return err
	}

	rel, err := v.AppendCapture(time.Now(), text)
	if err != nil {
		return err
	}

	fmt.Println(filepath.Join(v.Root, rel))
	return nil
}

// runClip fetches a URL, runs it through clip.Clean, and writes it to the
// vault at clips/YYYY-MM-DD-{slug}.html. Operates directly on the vault — does
// NOT talk to the daemon, so it works offline / without `weft serve` running.
//
// If the slug collides with an existing same-day clip, a -2, -3 suffix is
// appended until a free slot is found.
func runClip(args []string) error {
	vaultPath := ""
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-v", "--vault":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", a)
			}
			vaultPath = args[i+1]
			i++
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: weft clip <url> [-v <vault>]")
	}
	url := positional[0]

	vaultPath = resolveVault(vaultPath)
	v, err := vault.New(vaultPath)
	if err != nil {
		return err
	}

	// 20 MB cap on the fetched body. Most pages are <2 MB; this leaves head-
	// room for a long PDF reader page without unbounded memory use.
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fetch: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	cleaned, title, err := clip.Clean(raw, url)
	if err != nil {
		return fmt.Errorf("clean: %w", err)
	}

	rel := uniqueClipPath(v, time.Now(), clip.Slug(title))
	if err := v.Write(rel, cleaned); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	fmt.Println(filepath.Join(v.Root, rel))
	return nil
}

func runMCP(args []string) error {
	vaultPath := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-v", "--vault":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", args[i])
			}
			vaultPath = args[i+1]
			i++
		default:
			if vaultPath == "" {
				vaultPath = args[i]
			}
		}
	}
	vaultPath = resolveVault(vaultPath)

	v, err := vault.New(vaultPath)
	if err != nil {
		return err
	}
	ix, err := index.Open(v.Root)
	if err != nil {
		return fmt.Errorf("index: %w", err)
	}
	defer ix.Close()

	// MCP is on stdio; logs go to stderr only. Anything to stdout would
	// corrupt the JSON-RPC frames.
	fmt.Fprintf(os.Stderr, "weft mcp: serving %s\n", v.Root)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	srv := mcp.New(v, ix)
	return srv.Serve(ctx)
}

// resolveVault picks the vault path from the flag value, the WEFT_VAULT env
// var, or ~/notes as the final fallback.
func resolveVault(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("WEFT_VAULT"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "notes")
}

// runImport dispatches `weft import <kind> <src> [--force] [-v vault]` to one
// of the four importer packages. Each importer returns a Report; we surface a
// summary line per kind.
func runImport(args []string) error {
	vaultPath := ""
	force := false
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-v", "--vault":
			if i+1 >= len(args) {
				return fmt.Errorf("missing value for %s", a)
			}
			vaultPath = args[i+1]
			i++
		case "--force", "-f":
			force = true
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 2 {
		return fmt.Errorf("usage: weft import <kind> <src> [--force] [-v <vault>]\n  kinds: markdown notion bookmarks apple-notes")
	}
	kind, src := positional[0], positional[1]

	v, err := vault.New(resolveVault(vaultPath))
	if err != nil {
		return err
	}

	var imported, skipped int
	var errs []error
	switch kind {
	case "markdown", "md", "obsidian":
		r := markdown.Import(src, v, markdown.Options{Force: force, Now: time.Now()})
		imported, skipped, errs = len(r.Imported), len(r.Skipped), r.Errors
	case "notion":
		r := notion.Import(src, v, notion.Options{Force: force})
		imported, skipped, errs = len(r.Imported), len(r.Skipped), r.Errors
	case "bookmarks":
		r := bookmarks.Import(src, v, bookmarks.Options{Force: force})
		imported, skipped, errs = len(r.Imported), len(r.Skipped), r.Errors
	case "apple-notes", "applenotes":
		r := applenotes.Import(src, v, applenotes.Options{Force: force})
		imported, skipped, errs = len(r.Imported), len(r.Skipped), r.Errors
	default:
		return fmt.Errorf("unknown kind %q (want markdown|notion|bookmarks|apple-notes)", kind)
	}

	fmt.Printf("imported %d, skipped %d, errors %d (vault: %s)\n", imported, skipped, len(errs), v.Root)
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, " - ", e)
	}
	if imported == 0 && skipped == 0 {
		return fmt.Errorf("nothing imported — is %s the right source path?", src)
	}
	return nil
}

// uniqueClipPath returns the first clips/YYYY-MM-DD-{slug}.html path that
// doesn't already exist, suffixing -2, -3, … on collision.
func uniqueClipPath(v *vault.Vault, t time.Time, slug string) string {
	base := clip.ClipPath(t, slug)
	if !v.Exists(base) {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := clip.ClipPath(t, fmt.Sprintf("%s-%d", slug, i))
		if !v.Exists(candidate) {
			return candidate
		}
	}
	return base // give up; let Write error out
}
