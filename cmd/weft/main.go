package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	syncpkg "weft/internal/sync"
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
  weft sync init             set up E2EE multi-device sync on your own cloud
  weft sync join             enroll this device with the shared passphrase
  weft sync pair             enroll this device from another one (no passphrase)
  weft sync pair-approve <code>  approve a pairing request on an enrolled device
  weft sync recovery         print this vault's 24-word recovery phrase
  weft sync recover --phrase "..."   rebuild a vault from its recovery phrase
  weft sync check            verify bucket creds + connectivity (no data touched)
  weft sync                  run one convergence cycle (push + pull)
                             flags: -v <vault> --passphrase <p> --phrase "<24 words>"
                                    --provider {r2|aws|minio|b2|fs} --bucket --endpoint
                                    --region --access-key --secret --path-style --fs-path

Prefer the WEFT_SECRET env var (or AWS_SECRET_ACCESS_KEY) over --secret so the
cloud secret key stays off the process list and shell history.

The vault key is cached in your OS keychain after init/join/pair, so the daemon
and "weft sync" need no passphrase on that device (WEFT_SECRET_STORE=file forces
the on-disk fallback for headless hosts).
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

	case "sync":
		if err := runSync(os.Args[2:]); err != nil {
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

// runSync dispatches `weft sync [init|join]` and the bare run. Flags are
// hand-parsed (consistent with the other subcommands). The passphrase comes
// from --passphrase or the WEFT_PASSPHRASE env var.
func runSync(args []string) error {
	sub := ""
	subs := map[string]bool{
		"init": true, "join": true, "check": true,
		"recovery": true, "recover": true, "pair": true, "pair-approve": true,
	}
	if len(args) > 0 && subs[args[0]] {
		sub, args = args[0], args[1:]
	}

	var vaultPath, passphrase, phrase string
	var positional []string
	cfg := syncpkg.Config{Prefix: "weft/v1"}
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 >= len(args) {
				return ""
			}
			i++
			return args[i]
		}
		switch args[i] {
		case "-v", "--vault":
			vaultPath = next()
		case "--passphrase":
			passphrase = next()
		case "--provider":
			cfg.Provider = next()
		case "--bucket":
			cfg.Bucket = next()
		case "--endpoint":
			cfg.Endpoint = next()
		case "--region":
			cfg.Region = next()
		case "--access-key":
			cfg.AccessKeyID = next()
		case "--secret":
			cfg.SecretAccessKey = next()
		case "--prefix":
			cfg.Prefix = next()
		case "--fs-path":
			cfg.FSPath = next()
		case "--path-style":
			cfg.PathStyle = true
		case "--phrase":
			phrase = next()
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %q", args[i])
			}
			positional = append(positional, args[i]) // e.g. the pairing code
		}
	}
	if passphrase == "" {
		passphrase = os.Getenv("WEFT_PASSPHRASE")
	}
	// Prefer the env for the long-lived cloud secret so it stays off argv (and
	// out of `ps`/shell history). --secret remains as a fallback.
	if cfg.SecretAccessKey == "" {
		if s := os.Getenv("WEFT_SECRET"); s != "" {
			cfg.SecretAccessKey = s
		} else {
			cfg.SecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
		}
	}

	v, err := vault.New(resolveVault(vaultPath))
	if err != nil {
		return err
	}

	// `check` verifies the backend round-trip (creds + connectivity), no key needed.
	if sub == "check" {
		if cfg.Provider == "" { // fall back to the vault's saved config
			if saved, lerr := syncpkg.LoadConfig(v); lerr == nil {
				cfg = saved
			} else {
				return lerr
			}
		}
		if err := syncpkg.CheckConfig(cfg); err != nil {
			return fmt.Errorf("backend check failed: %w", err)
		}
		fmt.Printf("sync check: %s backend OK (put/get/head/list/delete round-trip)\n", cfg.Provider)
		return nil
	}

	// recovery: show this vault's 24-word recovery phrase (needs an unlocked device).
	if sub == "recovery" {
		vk, err := syncpkg.LocalVaultKey(v, passphrase)
		if err != nil {
			return fmt.Errorf("unlock this device first (keychain or --passphrase): %w", err)
		}
		ph, err := syncpkg.VaultKeyMnemonic(vk)
		if err != nil {
			return err
		}
		fmt.Println("Recovery phrase — write it down offline. Anyone with these words can read your vault.")
		fmt.Println("\n  " + ph + "\n")
		return nil
	}

	// recover: rebuild a vault on a fresh device from the recovery phrase + cloud flags.
	if sub == "recover" {
		if phrase == "" { // avoid the recovery phrase (== the raw key) on argv / in shell history
			phrase = os.Getenv("WEFT_RECOVERY_PHRASE")
		}
		if phrase == "" {
			fmt.Print("Enter your 24-word recovery phrase: ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			phrase = strings.TrimSpace(line)
		}
		if phrase == "" {
			return errors.New("a recovery phrase is required (stdin, WEFT_RECOVERY_PHRASE, or --phrase)")
		}
		vk, err := syncpkg.VaultKeyFromMnemonic(phrase)
		if err != nil {
			return err
		}
		eng, err := syncpkg.JoinWithKey(v, cfg, vk)
		if err != nil {
			return err
		}
		res, err := eng.Sync()
		if err != nil {
			return err
		}
		fmt.Printf("recovered — synced: applied %d, conflicts %d\n", res.Applied, len(res.ConflictCopies))
		return nil
	}

	// pair: enroll THIS new device by getting the key from an existing one (no passphrase).
	if sub == "pair" {
		p, err := syncpkg.StartPairing(v, cfg)
		if err != nil {
			return err
		}
		fmt.Printf("On a device that's already synced, run:\n\n  weft sync pair-approve %s\n\nWaiting for approval…\n", p.ReqID())
		for {
			ok, err := p.FetchResponderKey()
			if err != nil {
				return err
			}
			if ok {
				break
			}
			time.Sleep(2 * time.Second)
		}
		fmt.Printf("\nCheck this code matches the OTHER device's screen:  %s\n", p.SAS())
		if !confirm("Do the two codes match?") {
			return errors.New("aborted — codes did not match (someone may be intercepting)")
		}
		fmt.Println("Finishing…")
		var eng *syncpkg.Engine
		for {
			eng, err = p.Finish()
			if err != nil {
				return err
			}
			if eng != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		res, err := eng.Sync()
		if err != nil {
			return err
		}
		fmt.Printf("paired — synced: applied %d\n", res.Applied)
		return nil
	}

	// pair-approve: on an enrolled device, hand the key to a new device by its code.
	if sub == "pair-approve" {
		if len(positional) == 0 {
			return errors.New("usage: weft sync pair-approve <code>  (the code shown on the new device)")
		}
		vk, err := syncpkg.LocalVaultKey(v, passphrase)
		if err != nil {
			return fmt.Errorf("unlock this device first (keychain or --passphrase): %w", err)
		}
		be, err := syncpkg.OpenBackend(v)
		if err != nil {
			return err
		}
		a, err := syncpkg.BeginApprove(be, positional[0], vk)
		if err != nil {
			return err
		}
		fmt.Println("Waiting for the new device…")
		for {
			ok, err := a.AwaitReveal()
			if err != nil {
				return err
			}
			if ok {
				break
			}
			time.Sleep(2 * time.Second)
		}
		fmt.Printf("Check this code matches the NEW device's screen:  %s\n", a.SAS())
		if !confirm("Do the two codes match?") {
			return errors.New("aborted — codes did not match")
		}
		if err := a.Finalize(); err != nil {
			return err
		}
		fmt.Println("Approved — the new device will finish automatically.")
		return nil
	}

	var eng *syncpkg.Engine
	switch sub {
	case "init":
		eng, err = syncpkg.Init(v, cfg, passphrase)
	case "join":
		eng, err = syncpkg.Join(v, cfg, passphrase)
	default: // bare `weft sync` — converge once, preferring the cached key
		eng, err = syncpkg.OpenLocal(v)
		if errors.Is(err, syncpkg.ErrNeedPassphrase) {
			eng, err = syncpkg.Open(v, passphrase)
		}
	}
	if err != nil {
		return err
	}

	res, err := eng.Sync()
	if err != nil {
		return err
	}
	fmt.Printf("sync: pushed %d, applied %d, conflicts %d\n", res.Pushed, res.Applied, len(res.ConflictCopies))
	for _, c := range res.ConflictCopies {
		fmt.Println("  conflict copy:", c)
	}
	if sub == "init" {
		fmt.Println("initialized — enroll another device with `weft sync pair` (no passphrase) or `weft sync join`")
		fmt.Println("save your recovery phrase now: `weft sync recovery`")
	}
	return nil
}

// confirm reads a y/N answer from stdin for an interactive pairing step.
func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
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
