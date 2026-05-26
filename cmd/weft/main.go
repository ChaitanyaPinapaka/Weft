package main

import (
	"fmt"
	"os"

	"weft/internal/server"
)

const usage = `Weft — HTML vault with brain memory.

Usage:
  weft serve <vault-path>    start the daemon and open browser
  weft capture "<text>"      quick-capture to today's daily note  (v0.2)
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

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}
