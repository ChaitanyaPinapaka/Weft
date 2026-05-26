package vault

import (
	"fmt"
	"time"
)

// DailyPath returns the vault-relative path for the daily note on date t,
// e.g. "daily/2026-05-25.html".
func (v *Vault) DailyPath(t time.Time) string {
	return fmt.Sprintf("daily/%s.html", t.Format("2006-01-02"))
}

// EnsureDaily creates the daily note for date t with a stub body
// ("<h1>YYYY-MM-DD</h1>\n") if it does not exist.
// Returns the vault-relative path. Never overwrites an existing note —
// dormant content survives untouched, per the vault's no-deletion invariant.
func (v *Vault) EnsureDaily(t time.Time) (string, error) {
	rel := v.DailyPath(t)
	if v.Exists(rel) {
		return rel, nil
	}
	stub := fmt.Sprintf("<h1>%s</h1>\n", t.Format("2006-01-02"))
	if err := v.Write(rel, []byte(stub)); err != nil {
		return "", err
	}
	return rel, nil
}
