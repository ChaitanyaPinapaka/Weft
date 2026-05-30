package server

import (
	"fmt"
	"os"
	"time"

	"weft/internal/embed"
	"weft/internal/index"
	syncpkg "weft/internal/sync"
	"weft/internal/vault"
)

// startAutoSync turns `weft serve` into a live sync node: if the vault has sync
// configured (weft sync init/join), it converges through the E2EE bucket on an
// interval and re-derives the index for anything pulled — so edits made on this
// device propagate and edits from other devices appear automatically. A no-op
// (with a one-line notice) when sync isn't configured or the passphrase isn't
// available, so the daemon always serves regardless.
//
// The vault key is unwrapped from the local keyfile using WEFT_PASSPHRASE; the
// key is held only in memory, never re-persisted. Interval defaults to 30s,
// overridable via WEFT_SYNC_INTERVAL (a Go duration, e.g. "10s").
func startAutoSync(v *vault.Vault, ix *index.Index, emb embed.Embedder) {
	if !syncpkg.Configured(v) {
		return
	}
	pass := os.Getenv("WEFT_PASSPHRASE")
	if pass == "" {
		fmt.Println("Sync  configured — set WEFT_PASSPHRASE to enable auto-sync")
		return
	}
	eng, err := syncpkg.Open(v, pass)
	if err != nil {
		fmt.Printf("Sync  disabled: %v\n", err)
		return
	}

	interval := 30 * time.Second
	if s := os.Getenv("WEFT_SYNC_INTERVAL"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			interval = d
		}
	}
	fmt.Printf("Sync  on — converging every %s\n", interval)

	go func() {
		syncOnce(eng, v, ix, emb) // converge once at startup
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			syncOnce(eng, v, ix, emb)
		}
	}()
}

func syncOnce(eng *syncpkg.Engine, v *vault.Vault, ix *index.Index, emb embed.Embedder) {
	res, err := eng.Sync()
	if err != nil {
		fmt.Printf("sync: %v\n", err)
		return
	}
	// Pulled changes wrote new .html; re-derive the index (incremental via Stale).
	if res.Applied > 0 || len(res.ConflictCopies) > 0 {
		_ = indexAll(v, ix, emb)
		fmt.Printf("sync: applied %d, conflicts %d\n", res.Applied, len(res.ConflictCopies))
	}
}
