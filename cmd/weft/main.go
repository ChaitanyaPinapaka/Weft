package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"weft/internal/server"
	"weft/internal/vault"
)

const usage = `Weft — HTML vault with brain memory.

Usage:
  weft serve <vault-path>    start the daemon and open browser
  weft capture "<text>"      quick-capture to today's daily note
  weft clip <url>            clip a URL to vault                   (v0.2)
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

	if vaultPath == "" {
		vaultPath = os.Getenv("WEFT_VAULT")
	}
	if vaultPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		vaultPath = filepath.Join(home, "notes")
	}

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
